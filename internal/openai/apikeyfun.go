package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxAPIKeyFunImageResponseSize int64 = 128 << 20

type apiKeyFunImageAsset struct {
	URL     string `json:"url"`
	B64JSON string `json:"b64_json"`
	Base64  string `json:"base64"`
}

type apiKeyFunImageEvent struct {
	Type    string                `json:"type"`
	URL     string                `json:"url"`
	B64JSON string                `json:"b64_json"`
	Base64  string                `json:"base64"`
	Data    []apiKeyFunImageAsset `json:"data"`
	Images  []apiKeyFunImageAsset `json:"images"`
	Item    struct {
		URL     string          `json:"url"`
		B64JSON string          `json:"b64_json"`
		Base64  string          `json:"base64"`
		Result  json.RawMessage `json:"result"`
	} `json:"item"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
}

type apiKeyFunVideoResponse struct {
	ID        string `json:"id"`
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	Progress  int    `json:"progress"`
	ExpiresAt int64  `json:"expires_at"`
	URL       string `json:"url"`
	VideoURL  string `json:"video_url"`
	Video     struct {
		URL string `json:"url"`
	} `json:"video"`
	Error   json.RawMessage `json:"error"`
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
}

func (c *Client) generateAPIKeyFunImage(ctx context.Context, request GenerateRequest, onProgress ProgressFunc) (MediaResult, error) {
	payload := map[string]any{
		"model":           request.Model,
		"prompt":          request.Prompt,
		"n":               request.Count,
		"size":            request.Size,
		"stream":          true,
		"response_format": "b64_json",
	}
	endpoint := c.endpoint("images/generations")
	if request.Capability == CapabilityImageToImage {
		sources := sourceImageValues(request)
		if len(sources) == 0 {
			source, err := sourceAsDataURL(request)
			if err != nil {
				return MediaResult{}, err
			}
			sources = []string{source}
		}
		images := make([]map[string]string, 0, len(sources))
		for _, source := range sources {
			images = append(images, map[string]string{"image_url": source})
		}
		payload["images"] = images
		payload["quality"] = "auto"
		endpoint = c.endpoint("images/edits")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return MediaResult{}, &APIError{Code: "encode_request_failed", Message: "请求参数编码失败", Detail: err.Error()}
	}
	req, err := c.newRequest(ctx, http.MethodPost, endpoint, "application/json", bytes.NewReader(encoded), true)
	if err != nil {
		return MediaResult{}, &APIError{Code: "create_request_failed", Message: "无法创建 APIKEY.FUN 图片请求", Detail: err.Error()}
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return MediaResult{}, normalizeTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return MediaResult{}, parseAPIError(resp)
	}
	result, err := readAPIKeyFunImageResponse(ctx, resp.Body, onProgress)
	if err != nil {
		return MediaResult{}, err
	}
	result, err = c.resolveProviderMediaResult(result)
	if err != nil {
		return MediaResult{}, err
	}
	emitProgress(onProgress, Progress{Stage: "图片已生成", Status: "succeeded", Progress: 100})
	return result, nil
}

// readAPIKeyFunImageResponse 同时兼容 SSE 和服务端偶尔返回的普通 JSON。
func readAPIKeyFunImageResponse(ctx context.Context, body io.Reader, onProgress ProgressFunc) (MediaResult, error) {
	limited := &io.LimitedReader{R: body, N: maxAPIKeyFunImageResponseSize + 1}
	reader := bufio.NewReader(limited)
	var eventName string
	var data bytes.Buffer
	var total int64
	handle := func() (MediaResult, bool, bool, error) {
		payload := strings.TrimSpace(data.String())
		data.Reset()
		currentEvent := eventName
		eventName = ""
		if payload == "" {
			return MediaResult{}, false, false, nil
		}
		return parseAPIKeyFunImageFrame([]byte(payload), currentEvent, onProgress)
	}
	for {
		line, readErr := reader.ReadString('\n')
		total += int64(len(line))
		if total > maxAPIKeyFunImageResponseSize {
			return MediaResult{}, &APIError{Code: "image_response_too_large", Message: "APIKEY.FUN 图片响应超过 128 MB 限制"}
		}
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case trimmed == "":
			result, found, done, frameErr := handle()
			if frameErr != nil {
				return MediaResult{}, frameErr
			}
			if found {
				return result, nil
			}
			if done {
				return MediaResult{}, &APIError{Code: "empty_image_result", Message: "APIKEY.FUN 没有返回最终图片"}
			}
		case strings.HasPrefix(trimmed, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
		case strings.HasPrefix(trimmed, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
		case strings.HasPrefix(trimmed, ":"):
		default:
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(trimmed)
		}
		if readErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return MediaResult{}, normalizeTransportError(ctxErr)
			}
			if readErr != io.EOF {
				return MediaResult{}, &APIError{Code: "read_response_failed", Message: "读取 APIKEY.FUN 图片响应失败", Detail: readErr.Error(), Retryable: true}
			}
			result, found, _, frameErr := handle()
			if frameErr != nil {
				return MediaResult{}, frameErr
			}
			if found {
				return result, nil
			}
			return MediaResult{}, &APIError{Code: "empty_image_result", Message: "APIKEY.FUN 没有返回最终图片"}
		}
	}
}

func parseAPIKeyFunImageFrame(payload []byte, eventName string, onProgress ProgressFunc) (MediaResult, bool, bool, error) {
	if string(payload) == "[DONE]" {
		return MediaResult{}, false, true, nil
	}
	var event apiKeyFunImageEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return MediaResult{}, false, false, &APIError{Code: "invalid_image_response", Message: "APIKEY.FUN 图片事件格式无效", Detail: err.Error()}
	}
	eventType := strings.ToLower(strings.TrimSpace(event.Type))
	if eventType == "" {
		eventType = strings.ToLower(strings.TrimSpace(eventName))
	}
	if eventErr := apiKeyFunEventError(event.Error, event.Code, event.Message, eventType, "图片生成失败"); eventErr != nil {
		return MediaResult{}, false, false, eventErr
	}
	if strings.Contains(eventType, "partial") {
		emitProgress(onProgress, Progress{Stage: "正在生成图片", Status: "running", Progress: 60})
		return MediaResult{}, false, false, nil
	}
	if result, ok := apiKeyFunImageResult(event); ok {
		return result, true, false, nil
	}
	if strings.Contains(eventType, "completed") || strings.Contains(eventType, "done") || strings.Contains(eventType, "succeeded") {
		return MediaResult{}, false, false, &APIError{Code: "empty_image_result", Message: "APIKEY.FUN 完成事件没有包含图片"}
	}
	return MediaResult{}, false, false, nil
}

func apiKeyFunImageResult(event apiKeyFunImageEvent) (MediaResult, bool) {
	assets := []apiKeyFunImageAsset{{URL: event.URL, B64JSON: event.B64JSON, Base64: event.Base64}}
	assets = append(assets, event.Data...)
	assets = append(assets, event.Images...)
	assets = append(assets, apiKeyFunImageAsset{URL: event.Item.URL, B64JSON: event.Item.B64JSON, Base64: event.Item.Base64})
	for _, asset := range assets {
		if result, ok := apiKeyFunMediaFromAsset(asset); ok {
			return result, true
		}
	}
	for _, raw := range []json.RawMessage{event.Item.Result, event.Result} {
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) == nil {
			if result, ok := apiKeyFunMediaFromValue(value); ok {
				return result, true
			}
		}
		var asset apiKeyFunImageAsset
		if json.Unmarshal(raw, &asset) == nil {
			if result, ok := apiKeyFunMediaFromAsset(asset); ok {
				return result, true
			}
		}
	}
	return MediaResult{}, false
}

func apiKeyFunMediaFromAsset(asset apiKeyFunImageAsset) (MediaResult, bool) {
	if strings.TrimSpace(asset.URL) != "" {
		return MediaResult{URL: strings.TrimSpace(asset.URL), MimeType: "image/png"}, true
	}
	encoded := asset.B64JSON
	if encoded == "" {
		encoded = asset.Base64
	}
	return apiKeyFunMediaFromValue(encoded)
}

func apiKeyFunMediaFromValue(value string) (MediaResult, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return MediaResult{}, false
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return MediaResult{URL: value, MimeType: "image/png"}, true
	}
	mimeType := "image/png"
	if strings.HasPrefix(value, "data:") {
		comma := strings.IndexByte(value, ',')
		if comma < 0 {
			return MediaResult{}, false
		}
		header := value[:comma]
		value = value[comma+1:]
		declared := strings.TrimPrefix(strings.Split(header, ";")[0], "data:")
		if strings.HasPrefix(declared, "image/") {
			mimeType = declared
		}
	}
	return MediaResult{Base64: value, MimeType: mimeType}, true
}

func (c *Client) generateAPIKeyFunVideo(ctx context.Context, request GenerateRequest, onProgress ProgressFunc) (MediaResult, error) {
	if request.Seconds < 1 || request.Seconds > 15 {
		return MediaResult{}, &APIError{Code: "invalid_duration", Message: "APIKEY.FUN 视频时长必须在 1 到 15 秒之间"}
	}
	payload := map[string]any{
		"model":      request.Model,
		"prompt":     request.Prompt,
		"resolution": apiKeyFunResolution(request.Size),
		"duration":   request.Seconds,
	}
	if request.Capability == CapabilityImageToVideo {
		source, err := sourceAsDataURL(request)
		if err != nil {
			return MediaResult{}, err
		}
		payload["image"] = map[string]string{"url": source}
	}
	emitProgress(onProgress, Progress{Stage: "提交视频任务", Status: "running", Progress: 5})
	var submitted apiKeyFunVideoResponse
	if err := c.doJSON(ctx, http.MethodPost, c.endpoint("videos/generations"), payload, &submitted); err != nil {
		return MediaResult{}, err
	}
	if err := apiKeyFunEventError(submitted.Error, submitted.Code, submitted.Message, submitted.Status, "视频生成失败"); err != nil {
		return MediaResult{}, err
	}
	remoteID := strings.TrimSpace(submitted.RequestID)
	if remoteID == "" {
		remoteID = strings.TrimSpace(submitted.ID)
	}
	if result, ok, resultErr := c.apiKeyFunCompletedVideo(submitted, remoteID); resultErr != nil {
		return MediaResult{}, resultErr
	} else if ok {
		return result, nil
	}
	if isFailedVideoStatus(submitted.Status) {
		return MediaResult{}, apiKeyFunVideoFailure(submitted)
	}
	if remoteID == "" {
		return MediaResult{}, &APIError{Code: "missing_remote_id", Message: "APIKEY.FUN 没有返回视频任务编号"}
	}
	return c.pollAPIKeyFunVideo(ctx, remoteID, onProgress)
}

func (c *Client) pollAPIKeyFunVideo(ctx context.Context, remoteID string, onProgress ProgressFunc) (MediaResult, error) {
	if err := ctx.Err(); err != nil {
		return MediaResult{}, normalizeTransportError(err)
	}
	endpoint := c.endpoint("videos/" + url.PathEscape(remoteID))
	for {
		if err := waitForPoll(ctx); err != nil {
			return MediaResult{}, normalizeTransportError(err)
		}
		var response apiKeyFunVideoResponse
		if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
			return MediaResult{}, err
		}
		if err := apiKeyFunEventError(response.Error, response.Code, response.Message, response.Status, "视频生成失败"); err != nil {
			return MediaResult{}, err
		}
		emitProgress(onProgress, Progress{Stage: videoStage(response.Status), Status: normalizeVideoStatus(response.Status), Progress: response.Progress, RemoteID: remoteID})
		if result, ok, resultErr := c.apiKeyFunCompletedVideo(response, remoteID); resultErr != nil {
			return MediaResult{}, resultErr
		} else if ok {
			return result, nil
		}
		if isFailedVideoStatus(response.Status) {
			return MediaResult{}, apiKeyFunVideoFailure(response)
		}
	}
}

func (c *Client) apiKeyFunCompletedVideo(response apiKeyFunVideoResponse, remoteID string) (MediaResult, bool, error) {
	videoURL := strings.TrimSpace(response.Video.URL)
	if videoURL == "" {
		videoURL = strings.TrimSpace(response.URL)
	}
	if videoURL == "" {
		videoURL = strings.TrimSpace(response.VideoURL)
	}
	status := normalizeVideoStatus(response.Status)
	if status != "succeeded" && !(strings.TrimSpace(response.Status) == "" && videoURL != "") {
		return MediaResult{}, false, nil
	}
	if videoURL == "" && remoteID == "" {
		return MediaResult{}, false, nil
	}
	result := MediaResult{RemoteID: remoteID, URL: videoURL, MimeType: "video/mp4", ProviderExpiresAt: response.ExpiresAt}
	if result.URL == "" {
		result.URL = c.endpoint("videos/" + url.PathEscape(remoteID) + "/content")
		result.AuthenticatedURL = true
	}
	result, err := c.resolveProviderMediaResult(result)
	if err != nil {
		return MediaResult{}, false, err
	}
	return result, true, nil
}

func apiKeyFunResolution(size string) string {
	normalized := strings.ToLower(strings.TrimSpace(size))
	if normalized == "480p" || strings.Contains(normalized, "480") {
		return "480p"
	}
	return "720p"
}

func apiKeyFunVideoFailure(response apiKeyFunVideoResponse) error {
	detail := apiKeyFunRawErrorMessage(response.Error)
	if detail == "" {
		detail = response.Message
	}
	return &APIError{Code: "video_failed", Message: "APIKEY.FUN 未能生成视频", Detail: detail}
}

func apiKeyFunEventError(rawError, rawCode json.RawMessage, message, eventType, fallback string) error {
	message = strings.TrimSpace(message)
	code := strings.TrimSpace(apiKeyFunRawValue(rawCode))
	hasRawError := apiKeyFunHasError(rawError)
	if hasRawError {
		if detail := apiKeyFunRawErrorMessage(rawError); detail != "" {
			message = detail
		}
		var envelope struct {
			Code any `json:"code"`
		}
		if json.Unmarshal(rawError, &envelope) == nil && envelope.Code != nil {
			code = fmt.Sprint(envelope.Code)
		}
	}
	normalizedType := strings.ToLower(strings.TrimSpace(eventType))
	failed := hasRawError || strings.Contains(normalizedType, "error") || strings.Contains(normalizedType, "failed") || strings.Contains(normalizedType, "cancelled") || strings.Contains(normalizedType, "canceled")
	if !failed {
		return nil
	}
	if message == "" {
		message = "APIKEY.FUN " + fallback
	}
	if code == "" {
		code = "apikeyfun_error"
	}
	return &APIError{Code: code, Message: message}
}

func apiKeyFunHasError(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.TrimSpace(typed) != ""
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func apiKeyFunRawErrorMessage(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	var envelope struct {
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		if strings.TrimSpace(envelope.Message) != "" {
			return strings.TrimSpace(envelope.Message)
		}
		return strings.TrimSpace(envelope.Detail)
	}
	return ""
}

func apiKeyFunRawValue(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value any
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
