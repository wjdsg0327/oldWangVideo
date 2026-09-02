package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const doubaoPollInterval = 3

type doubaoVideoResponse struct {
	ID        string `json:"id"`
	Model     string `json:"model"`
	Status    string `json:"status"`
	Progress  int    `json:"progress"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	Content   struct {
		VideoURL     string `json:"video_url"`
		LastFrameURL string `json:"last_frame_url"`
	} `json:"content"`
	VideoURL string          `json:"video_url"`
	Error    json.RawMessage `json:"error"`
}

func (c *Client) generateDoubaoVideo(ctx context.Context, request GenerateRequest, onProgress ProgressFunc) (MediaResult, error) {
	if request.Capability != CapabilityTextToVideo && request.Capability != CapabilityImageToVideo {
		return MediaResult{}, &APIError{Code: "unsupported_capability", Message: "豆包方舟适配仅支持文生视频和图生视频", Detail: string(request.Capability)}
	}
	if err := ctx.Err(); err != nil {
		return MediaResult{}, normalizeTransportError(err)
	}

	content, err := doubaoVideoContent(request)
	if err != nil {
		return MediaResult{}, err
	}
	payload := map[string]any{
		"model":          request.Model,
		"content":        content,
		"generate_audio": true,
		"ratio":          doubaoRatio(request.Size),
		"duration":       doubaoDuration(request.Seconds),
		"watermark":      false,
	}
	emitProgress(onProgress, Progress{Stage: "提交豆包视频任务", Status: "running", Progress: 5})
	var submitted doubaoVideoResponse
	if err := c.doJSON(ctx, http.MethodPost, c.endpoint("contents/generations/tasks"), payload, &submitted); err != nil {
		return MediaResult{}, err
	}
	if isFailedVideoStatus(submitted.Status) {
		return MediaResult{}, doubaoVideoFailure(submitted)
	}
	remoteID := strings.TrimSpace(submitted.ID)
	if result, ok, err := c.completedDoubaoVideo(submitted, remoteID); err != nil {
		return MediaResult{}, err
	} else if ok {
		emitProgress(onProgress, Progress{Stage: "视频已生成", Status: "succeeded", Progress: 100, RemoteID: remoteID})
		return result, nil
	}
	if remoteID == "" {
		return MediaResult{}, &APIError{Code: "missing_remote_id", Message: "豆包方舟没有返回视频任务编号"}
	}
	emitProgress(onProgress, Progress{Stage: videoStage(submitted.Status), Status: normalizeVideoStatus(submitted.Status), Progress: doubaoProgress(submitted), RemoteID: remoteID})
	return c.pollDoubaoVideo(ctx, remoteID, onProgress)
}

func doubaoVideoContent(request GenerateRequest) ([]map[string]any, error) {
	content := []map[string]any{{
		"type": "text",
		"text": request.Prompt,
	}}
	if request.Capability == CapabilityImageToVideo {
		source, err := sourceAsDataURL(request)
		if err != nil {
			return nil, err
		}
		content = append(content, map[string]any{
			"type": "image_url",
			"image_url": map[string]string{
				"url": source,
			},
			"role": "reference_image",
		})
	}
	return content, nil
}

func (c *Client) pollDoubaoVideo(ctx context.Context, remoteID string, onProgress ProgressFunc) (MediaResult, error) {
	endpoint := c.endpoint("contents/generations/tasks/" + url.PathEscape(remoteID))
	for {
		if err := waitDoubaoPoll(ctx); err != nil {
			return MediaResult{}, normalizeTransportError(err)
		}
		var response doubaoVideoResponse
		if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
			return MediaResult{}, err
		}
		response.ID = remoteID
		emitProgress(onProgress, Progress{Stage: videoStage(response.Status), Status: normalizeVideoStatus(response.Status), Progress: doubaoProgress(response), RemoteID: remoteID})
		if result, ok, err := c.completedDoubaoVideo(response, remoteID); err != nil {
			return MediaResult{}, err
		} else if ok {
			return result, nil
		}
		if isFailedVideoStatus(response.Status) {
			return MediaResult{}, doubaoVideoFailure(response)
		}
	}
}

func (c *Client) completedDoubaoVideo(response doubaoVideoResponse, remoteID string) (MediaResult, bool, error) {
	status := normalizeVideoStatus(response.Status)
	videoURL := strings.TrimSpace(response.Content.VideoURL)
	if videoURL == "" {
		videoURL = strings.TrimSpace(response.VideoURL)
	}
	if status != "succeeded" && !(strings.TrimSpace(response.Status) == "" && videoURL != "") {
		return MediaResult{}, false, nil
	}
	if videoURL == "" {
		return MediaResult{}, false, &APIError{Code: "missing_video_url", Message: "豆包方舟视频任务已完成，但没有返回 content.video_url"}
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

func doubaoVideoFailure(response doubaoVideoResponse) error {
	detail := strings.TrimSpace(rawDoubaoError(response.Error))
	if detail == "" {
		detail = "豆包方舟视频任务失败"
	}
	return &APIError{Code: "doubao_video_failed", Message: "豆包方舟未能生成视频", Detail: detail}
}

func rawDoubaoError(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var envelope struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		parts := make([]string, 0, 2)
		if strings.TrimSpace(envelope.Code) != "" {
			parts = append(parts, strings.TrimSpace(envelope.Code))
		}
		if strings.TrimSpace(envelope.Message) != "" {
			parts = append(parts, strings.TrimSpace(envelope.Message))
		} else if strings.TrimSpace(envelope.Detail) != "" {
			parts = append(parts, strings.TrimSpace(envelope.Detail))
		}
		return strings.Join(parts, ": ")
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(string(raw))
}

func doubaoRatio(size string) string {
	normalized := strings.ToLower(strings.TrimSpace(size))
	if normalized == "16:9" || normalized == "9:16" || normalized == "1:1" {
		return normalized
	}
	width, height := parseSize(normalized)
	if width <= 0 || height <= 0 {
		return "16:9"
	}
	switch {
	case width*9 == height*16:
		return "16:9"
	case width*16 == height*9:
		return "9:16"
	case width == height:
		return "1:1"
	default:
		return "16:9"
	}
}

func doubaoDuration(seconds int) int {
	if seconds < 1 {
		return 5
	}
	if seconds > 12 {
		return 12
	}
	return seconds
}

func doubaoProgress(response doubaoVideoResponse) int {
	if response.Progress > 0 {
		return min(response.Progress, 95)
	}
	switch normalizeVideoStatus(response.Status) {
	case "queued":
		return 10
	case "succeeded":
		return 100
	default:
		return 60
	}
}

func waitDoubaoPoll(ctx context.Context) error {
	timer := time.NewTimer(doubaoPollInterval * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
