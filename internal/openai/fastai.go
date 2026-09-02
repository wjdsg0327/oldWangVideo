package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const fastAIPollInterval = 15 * time.Second

// fastAIVideoResponse 是 FastAIToken 创建与查询接口共用的响应结构。
type fastAIVideoResponse struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	Status    string `json:"status"`
	Progress  int    `json:"progress"`
	ExpiresAt int64  `json:"expires_at"`
	Content   struct {
		VideoURL string `json:"video_url"`
	} `json:"content"`
	Error json.RawMessage `json:"error"`
}

// generateFastAIVideo 提交 FastAIToken Seedance 任务并轮询直至终态。
func (c *Client) generateFastAIVideo(ctx context.Context, request GenerateRequest, onProgress ProgressFunc) (MediaResult, error) {
	if request.Capability != CapabilityTextToVideo && request.Capability != CapabilityImageToVideo {
		return MediaResult{}, &APIError{Code: "unsupported_capability", Message: "FastAIToken 适配仅支持文生视频和图生视频", Detail: string(request.Capability)}
	}
	if err := ctx.Err(); err != nil {
		return MediaResult{}, normalizeTransportError(err)
	}

	content, err := c.fastAIVideoContent(ctx, request)
	if err != nil {
		return MediaResult{}, err
	}
	payload := map[string]any{
		"model":          fastAIModelID(request.Model),
		"content":        content,
		"resolution":     fastAIResolution(request.Size),
		"ratio":          fastAIRatio(request.Size),
		"duration":       fastAIDuration(request.Seconds),
		"generate_audio": true,
		"watermark":      false,
	}
	endpoint := c.fastAISeedanceEndpoint("seedance/api/v3/contents/generations/tasks")
	emitProgress(onProgress, Progress{Stage: "提交 FastAI 视频任务", Status: "running", Progress: 5})
	var submitted fastAIVideoResponse
	if err := c.doJSON(ctx, http.MethodPost, endpoint, payload, &submitted); err != nil {
		return MediaResult{}, err
	}
	if err := fastAITaskError(submitted); err != nil {
		return MediaResult{}, err
	}
	remoteID := strings.TrimSpace(submitted.ID)
	if remoteID == "" {
		remoteID = strings.TrimSpace(submitted.TaskID)
	}
	if result, done, err := c.completedFastAIVideo(submitted, remoteID); err != nil {
		return MediaResult{}, err
	} else if done {
		emitProgress(onProgress, Progress{Stage: "视频已生成", Status: "succeeded", Progress: 100, RemoteID: remoteID})
		return result, nil
	}
	if remoteID == "" {
		return MediaResult{}, &APIError{Code: "missing_remote_id", Message: "FastAIToken 没有返回视频任务编号"}
	}
	if status, known := fastAIStatus(submitted.Status); !known {
		return MediaResult{}, fastAIUnknownStatus(submitted.Status)
	} else {
		emitProgress(onProgress, Progress{Stage: fastAIVideoStage(status), Status: status, Progress: fastAIProgress(submitted.Progress, status), RemoteID: remoteID})
	}
	return c.pollFastAIVideo(ctx, remoteID, onProgress)
}

// fastAIModelID 兼容用户把官方模型显示名填写到远程模型 ID 的历史配置。
func fastAIModelID(value string) string {
	trimmed := strings.TrimSpace(value)
	normalized := strings.ToLower(trimmed)
	switch normalized {
	case "seedance 2.0", "seedance2.0", "seedance-2.0", "doubao-seedance-2-0":
		return "doubao-seedance-2-0-260128"
	case "seedance 2.0 fast", "seedance2.0 fast", "seedance-2.0-fast", "doubao-seedance-2-0-fast":
		return "doubao-seedance-2-0-fast-260128"
	default:
		return trimmed
	}
}

// fastAIVideoContent 只构造当前工作台支持的文本和单图输入。
func (c *Client) fastAIVideoContent(ctx context.Context, request GenerateRequest) ([]map[string]any, error) {
	content := []map[string]any{{
		"type": "text",
		"text": request.Prompt,
	}}
	if request.Capability != CapabilityImageToVideo {
		return content, nil
	}
	source, err := c.fastAISourceDataURL(ctx, request)
	if err != nil {
		return nil, err
	}
	content = append(content, map[string]any{
		"type": "image_url",
		"image_url": map[string]string{
			"url": source,
		},
		// 工作台的图生视频只有一张来源图，按 Seedance 首帧模式提交。
		"role": "first_frame",
	})
	return content, nil
}

// fastAISourceDataURL 将本地、远程或已有 Base64 图片统一为 data URL。
func (c *Client) fastAISourceDataURL(ctx context.Context, request GenerateRequest) (string, error) {
	data, _, err := c.loadSourceImage(ctx, request)
	if err != nil {
		return "", err
	}
	mimeType := http.DetectContentType(data)
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// pollFastAIVideo 查询 FastAIToken 任务，expired 和 failed 都会立即结束轮询。
func (c *Client) pollFastAIVideo(ctx context.Context, remoteID string, onProgress ProgressFunc) (MediaResult, error) {
	endpoint := c.fastAISeedanceEndpoint("seedance/api/v3/contents/generations/tasks/" + url.PathEscape(remoteID))
	for {
		if err := waitFastAIPoll(ctx); err != nil {
			return MediaResult{}, normalizeTransportError(err)
		}
		var response fastAIVideoResponse
		if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
			return MediaResult{}, err
		}
		response.ID = remoteID
		if err := fastAITaskError(response); err != nil {
			return MediaResult{}, err
		}
		status, known := fastAIStatus(response.Status)
		if !known {
			return MediaResult{}, fastAIUnknownStatus(response.Status)
		}
		emitProgress(onProgress, Progress{Stage: fastAIVideoStage(status), Status: status, Progress: fastAIProgress(response.Progress, status), RemoteID: remoteID})
		if result, done, err := c.completedFastAIVideo(response, remoteID); err != nil {
			return MediaResult{}, err
		} else if done {
			return result, nil
		}
	}
}

// completedFastAIVideo 提取成功任务中的签名视频地址，并保持下载免鉴权标记。
func (c *Client) completedFastAIVideo(response fastAIVideoResponse, remoteID string) (MediaResult, bool, error) {
	status, known := fastAIStatus(response.Status)
	if !known {
		return MediaResult{}, false, fastAIUnknownStatus(response.Status)
	}
	if status == "failed" {
		return MediaResult{}, false, fastAIVideoFailure(response)
	}
	if status == "expired" {
		return MediaResult{}, false, fastAIVideoExpired(response)
	}
	videoURL := strings.TrimSpace(response.Content.VideoURL)
	// 少数网关在返回视频地址时省略 status，地址本身可作为完成信号。
	if status == "running" && strings.TrimSpace(response.Status) == "" && videoURL != "" {
		status = "succeeded"
	}
	if status != "succeeded" {
		return MediaResult{}, false, nil
	}
	if videoURL == "" {
		return MediaResult{}, false, &APIError{Code: "missing_video_url", Message: "FastAIToken 视频任务已完成，但没有返回 content.video_url"}
	}
	return MediaResult{
		RemoteID:          remoteID,
		URL:               videoURL,
		MimeType:          "video/mp4",
		AuthenticatedURL:  false,
		ProviderExpiresAt: response.ExpiresAt,
	}, true, nil
}

// fastAITaskError 处理任务响应中的失败和过期状态，并保留 error 字段详情。
func fastAITaskError(response fastAIVideoResponse) error {
	status, known := fastAIStatus(response.Status)
	if !known {
		return fastAIUnknownStatus(response.Status)
	}
	if status == "failed" {
		return fastAIVideoFailure(response)
	}
	if status == "expired" {
		return fastAIVideoExpired(response)
	}
	return nil
}

func fastAIStatus(value string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return "running", true
	}
	switch normalized {
	case "queued":
		return "queued", true
	case "running":
		return "running", true
	case "succeeded":
		return "succeeded", true
	case "failed":
		return "failed", true
	case "expired":
		return "expired", true
	default:
		return "", false
	}
}

func fastAIProgress(value int, status string) int {
	if status == "succeeded" {
		return 100
	}
	if value > 0 {
		if value > 95 {
			return 95
		}
		return value
	}
	switch status {
	case "queued":
		return 10
	case "failed", "expired":
		return 100
	default:
		return 60
	}
}

func fastAIVideoStage(status string) string {
	switch status {
	case "queued":
		return "FastAI 任务排队中"
	case "succeeded":
		return "视频已生成"
	case "failed":
		return "视频生成失败"
	case "expired":
		return "FastAI 任务已过期"
	default:
		return "正在生成视频"
	}
}

func fastAIVideoFailure(response fastAIVideoResponse) error {
	detail := fastAIErrorDetail(response.Error)
	if detail == "" {
		detail = "FastAIToken 视频任务失败"
	}
	return &APIError{Code: "fastai_video_failed", Message: "FastAIToken 未能生成视频", Detail: detail}
}

func fastAIVideoExpired(response fastAIVideoResponse) error {
	detail := fastAIErrorDetail(response.Error)
	if detail == "" {
		detail = "FastAIToken 视频任务已过期"
	}
	return &APIError{Code: "fastai_video_expired", Message: "FastAIToken 视频任务已过期", Detail: detail}
}

func fastAIUnknownStatus(status string) error {
	return &APIError{Code: "unknown_video_status", Message: "FastAIToken 返回未知的视频任务状态", Detail: strings.TrimSpace(status)}
}

func fastAIErrorDetail(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	var envelope map[string]any
	if json.Unmarshal(raw, &envelope) == nil {
		parts := make([]string, 0, 3)
		for _, key := range []string{"code", "message", "detail", "type"} {
			if value, ok := envelope[key]; ok && value != nil {
				valueText := strings.TrimSpace(fmt.Sprint(value))
				if valueText != "" {
					parts = append(parts, valueText)
				}
			}
		}
		return strings.Join(parts, ": ")
	}
	return strings.TrimSpace(string(raw))
}

func fastAIResolution(size string) string {
	normalized := strings.ToLower(strings.TrimSpace(size))
	switch normalized {
	case "480p":
		return "480p"
	case "1080p":
		return "1080p"
	}
	width, height := parseSize(normalized)
	if width <= 0 || height <= 0 {
		return "720p"
	}
	maxDimension := width
	if height > maxDimension {
		maxDimension = height
	}
	// FastAI 的分辨率档位按像素面积定义，使用表中各档位的最大边做稳妥映射。
	switch {
	case maxDimension <= 992:
		return "480p"
	case maxDimension <= 1470:
		return "720p"
	default:
		return "1080p"
	}
}

func fastAIRatio(size string) string {
	normalized := strings.ToLower(strings.TrimSpace(size))
	for _, ratio := range []string{"16:9", "4:3", "1:1", "3:4", "9:16", "21:9", "adaptive"} {
		if normalized == ratio {
			return ratio
		}
	}
	width, height := parseSize(normalized)
	if width <= 0 || height <= 0 {
		return "adaptive"
	}
	switch {
	case width == height:
		return "1:1"
	case width*9 == height*16:
		return "16:9"
	case width*3 == height*4:
		return "4:3"
	case width*4 == height*3:
		return "3:4"
	case width*16 == height*9:
		return "9:16"
	case width*9 == height*21:
		return "21:9"
	default:
		return "adaptive"
	}
}

func fastAIDuration(seconds int) int {
	if seconds == -1 {
		return -1
	}
	if seconds < 4 {
		return 5
	}
	if seconds > 15 {
		return 15
	}
	return seconds
}

func waitFastAIPoll(ctx context.Context) error {
	timer := time.NewTimer(fastAIPollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
