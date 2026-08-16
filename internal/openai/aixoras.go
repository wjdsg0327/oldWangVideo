package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const aixorasPollInterval = 3 * time.Second

type aixorasVideoResponse struct {
	ID       string          `json:"id"`
	TaskID   string          `json:"task_id"`
	Object   string          `json:"object"`
	Model    string          `json:"model"`
	Status   string          `json:"status"`
	Progress int             `json:"progress"`
	Error    json.RawMessage `json:"error"`
	Metadata struct {
		URL string `json:"url"`
	} `json:"metadata"`
}

func (c *Client) generateAIXORASVideo(ctx context.Context, request GenerateRequest, onProgress ProgressFunc) (MediaResult, error) {
	if err := ctx.Err(); err != nil {
		return MediaResult{}, normalizeTransportError(err)
	}

	emitProgress(onProgress, Progress{Stage: "提交视频任务", Status: "running", Progress: 5})
	var (
		submitted aixorasVideoResponse
		err       error
	)
	switch request.Capability {
	case CapabilityTextToVideo:
		submitted, err = c.submitAIXORASTextToVideo(ctx, request)
	case CapabilityImageToVideo:
		submitted, err = c.submitAIXORASImageToVideo(ctx, request)
	default:
		return MediaResult{}, &APIError{Code: "unsupported_capability", Message: "AIXORAS 仅支持文生视频和图生视频适配", Detail: string(request.Capability)}
	}
	if err != nil {
		return MediaResult{}, err
	}
	if isFailedVideoStatus(submitted.Status) {
		return MediaResult{}, aixorasVideoFailure(submitted)
	}
	if err := aixorasResponseError(submitted); err != nil {
		return MediaResult{}, err
	}

	remoteID := strings.TrimSpace(submitted.TaskID)
	if remoteID == "" {
		remoteID = strings.TrimSpace(submitted.ID)
	}
	if result, ok, resultErr := c.completedAIXORASVideo(submitted, remoteID); resultErr != nil {
		return MediaResult{}, resultErr
	} else if ok {
		emitProgress(onProgress, Progress{Stage: "视频已生成", Status: "succeeded", Progress: 100, RemoteID: remoteID})
		return result, nil
	}
	if isAIXORASUnknownStatus(submitted.Status) {
		return MediaResult{}, aixorasUnknownStatusError(submitted.Status)
	}
	if remoteID == "" {
		return MediaResult{}, &APIError{Code: "missing_remote_id", Message: "AIXORAS 没有返回视频任务编号"}
	}

	emitProgress(onProgress, Progress{
		Stage:    videoStage(submitted.Status),
		Status:   normalizeVideoStatus(submitted.Status),
		Progress: submitted.Progress,
		RemoteID: remoteID,
	})
	return c.pollAIXORASVideo(ctx, remoteID, onProgress)
}

// submitAIXORASTextToVideo 只提交文生视频字段，避免参考图参数影响模型校验。
func (c *Client) submitAIXORASTextToVideo(ctx context.Context, request GenerateRequest) (aixorasVideoResponse, error) {
	payload := aixorasVideoPayload(request)
	if metadata := aixorasSeedanceMetadata(request, false); len(metadata) > 0 {
		payload["metadata"] = metadata
	}
	var response aixorasVideoResponse
	err := c.doJSON(ctx, http.MethodPost, c.endpoint("video/generations"), payload, &response)
	return response, err
}

// submitAIXORASImageToVideo 同时发送服务端支持的三种字符串参考图字段。
func (c *Client) submitAIXORASImageToVideo(ctx context.Context, request GenerateRequest) (aixorasVideoResponse, error) {
	source, err := sourceAsDataURL(request)
	if err != nil {
		return aixorasVideoResponse{}, err
	}
	payload := aixorasVideoPayload(request)
	payload["images"] = []string{source}
	payload["image"] = source
	payload["input_reference"] = source
	if metadata := aixorasSeedanceMetadata(request, true); len(metadata) > 0 {
		payload["metadata"] = metadata
	}
	var response aixorasVideoResponse
	err = c.doJSON(ctx, http.MethodPost, c.endpoint("video/generations"), payload, &response)
	return response, err
}

func aixorasVideoPayload(request GenerateRequest) map[string]any {
	return map[string]any{
		"model":      request.Model,
		"prompt":     request.Prompt,
		"duration":   request.Seconds,
		"seconds":    strconv.Itoa(request.Seconds),
		"resolution": aixorasResolution(request.Size),
	}
}

func aixorasSeedanceMetadata(request GenerateRequest, includeWatermark bool) map[string]any {
	if !strings.Contains(strings.ToLower(request.Model), "seedance") {
		return nil
	}
	metadata := map[string]any{}
	if aspectRatio := aixorasAspectRatio(request.Size); aspectRatio != "" {
		metadata["aspect_ratio"] = aspectRatio
	}
	if includeWatermark {
		metadata["watermark"] = false
	}
	return metadata
}

func (c *Client) pollAIXORASVideo(ctx context.Context, remoteID string, onProgress ProgressFunc) (MediaResult, error) {
	if err := ctx.Err(); err != nil {
		return MediaResult{}, normalizeTransportError(err)
	}
	endpoint := c.endpoint("video/generations/" + url.PathEscape(remoteID))
	for {
		if err := waitForAIXORASPoll(ctx); err != nil {
			return MediaResult{}, normalizeTransportError(err)
		}
		var response aixorasVideoResponse
		if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
			return MediaResult{}, err
		}
		if isFailedVideoStatus(response.Status) {
			return MediaResult{}, aixorasVideoFailure(response)
		}
		if err := aixorasResponseError(response); err != nil {
			return MediaResult{}, err
		}
		if isAIXORASUnknownStatus(response.Status) {
			return MediaResult{}, aixorasUnknownStatusError(response.Status)
		}
		emitProgress(onProgress, Progress{
			Stage:    videoStage(response.Status),
			Status:   normalizeVideoStatus(response.Status),
			Progress: response.Progress,
			RemoteID: remoteID,
		})
		if result, ok, resultErr := c.completedAIXORASVideo(response, remoteID); resultErr != nil {
			return MediaResult{}, resultErr
		} else if ok {
			return result, nil
		}
	}
}

func (c *Client) completedAIXORASVideo(response aixorasVideoResponse, remoteID string) (MediaResult, bool, error) {
	if normalizeVideoStatus(response.Status) != "succeeded" {
		return MediaResult{}, false, nil
	}
	videoURL := strings.TrimSpace(response.Metadata.URL)
	if videoURL == "" {
		return MediaResult{}, false, &APIError{Code: "missing_video_url", Message: "AIXORAS 视频任务已完成，但没有返回 metadata.url"}
	}
	result, err := c.resolveProviderMediaResult(MediaResult{
		RemoteID: remoteID,
		URL:      videoURL,
		MimeType: "video/mp4",
	})
	if err != nil {
		return MediaResult{}, false, err
	}
	return result, true, nil
}

func aixorasResolution(size string) string {
	normalized := strings.ToLower(strings.TrimSpace(size))
	switch {
	case normalized == "1080p", strings.Contains(normalized, "1080"):
		return "1080p"
	case normalized == "480p", strings.Contains(normalized, "480"):
		return "480p"
	default:
		return "720p"
	}
}

func aixorasAspectRatio(size string) string {
	normalized := strings.ToLower(strings.TrimSpace(size))
	for _, ratio := range []string{"16:9", "9:16", "4:3", "3:4", "1:1", "21:9"} {
		if normalized == ratio {
			return ratio
		}
	}
	width, height := parseSize(normalized)
	if width <= 0 || height <= 0 {
		return ""
	}
	switch {
	case width == height:
		return "1:1"
	case width*9 == height*21:
		return "21:9"
	case width*9 == height*16:
		return "16:9"
	case width*3 == height*4:
		return "4:3"
	case width*4 == height*3:
		return "3:4"
	case width*16 == height*9:
		return "9:16"
	default:
		return ""
	}
}

func waitForAIXORASPoll(ctx context.Context) error {
	timer := time.NewTimer(aixorasPollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func aixorasResponseError(response aixorasVideoResponse) error {
	detail, code, hasError := aixorasErrorDetail(response.Error)
	if !hasError {
		return nil
	}
	if detail == "" {
		detail = "AIXORAS 返回了未说明的错误"
	}
	if code == "" {
		code = "aixoras_error"
	}
	return &APIError{Code: code, Message: "AIXORAS 视频请求失败", Detail: detail}
}

func aixorasVideoFailure(response aixorasVideoResponse) error {
	detail, code, _ := aixorasErrorDetail(response.Error)
	if code == "" {
		code = "video_failed"
	}
	return &APIError{Code: code, Message: "AIXORAS 未能生成视频", Detail: detail}
}

func aixorasUnknownStatusError(status string) error {
	return &APIError{Code: "unknown_video_status", Message: "AIXORAS 返回未知的视频任务状态", Detail: strings.TrimSpace(status)}
}

func isAIXORASUnknownStatus(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "unknown")
}

func aixorasErrorDetail(raw json.RawMessage) (string, string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", "", false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return strings.TrimSpace(string(raw)), "", true
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed), "", strings.TrimSpace(typed) != ""
	case bool:
		if !typed {
			return "", "", false
		}
		return "", "", true
	case map[string]any:
		if len(typed) == 0 {
			return "", "", false
		}
		detail := firstAIXORASErrorString(typed, "message", "detail", "error")
		code := firstAIXORASErrorString(typed, "code", "type")
		if detail == "" {
			encoded, _ := json.Marshal(typed)
			detail = string(encoded)
		}
		return detail, code, true
	default:
		return strings.TrimSpace(fmt.Sprint(typed)), "", true
	}
}

func firstAIXORASErrorString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := values[key]
		if !ok || value == nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text != "" {
			return text
		}
	}
	return ""
}
