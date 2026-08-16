package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxMediaDownloadSize int64 = 512 << 20

type videoResponse struct {
	ID        string `json:"id"`
	RequestID string `json:"requestId"`
	Status    string `json:"status"`
	Progress  int    `json:"progress"`
	ExpiresAt int64  `json:"expires_at"`
	URL       string `json:"url"`
	VideoURL  string `json:"video_url"`
	Error     struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Data struct {
		Status  string `json:"status"`
		Reason  string `json:"reason"`
		Results struct {
			Videos []struct {
				URL string `json:"url"`
			} `json:"videos"`
		} `json:"results"`
	} `json:"data"`
	Results struct {
		Videos []struct {
			URL string `json:"url"`
		} `json:"videos"`
	} `json:"results"`
	Outputs []struct {
		URL string `json:"url"`
	} `json:"outputs"`
}

func (c *Client) generateVideo(ctx context.Context, request GenerateRequest, onProgress ProgressFunc) (MediaResult, error) {
	switch c.profile {
	case ProfileAIXORAS:
		return c.generateAIXORASVideo(ctx, request, onProgress)
	case ProfileAPIKeyFun:
		return c.generateAPIKeyFunVideo(ctx, request, onProgress)
	case ProfileSiliconFlow:
		return c.generateSiliconFlowVideo(ctx, request, onProgress)
	case ProfileTogether:
		return c.generateTogetherVideo(ctx, request, onProgress)
	default:
		return c.generateOpenAIVideo(ctx, request, onProgress)
	}
}

func (c *Client) generateOpenAIVideo(ctx context.Context, request GenerateRequest, onProgress ProgressFunc) (MediaResult, error) {
	emitProgress(onProgress, Progress{Stage: "提交视频任务", Status: "running", Progress: 5})
	var response videoResponse
	var err error
	if request.Capability == CapabilityImageToVideo {
		response, err = c.submitOpenAIReferenceVideo(ctx, request)
	} else {
		payload := map[string]any{
			"model":   request.Model,
			"prompt":  request.Prompt,
			"seconds": fmt.Sprint(request.Seconds),
			"size":    request.Size,
		}
		err = c.doJSON(ctx, http.MethodPost, c.endpoint("videos"), payload, &response)
	}
	if err != nil {
		return MediaResult{}, err
	}
	if result, ok := completedVideoResult(response, false); ok {
		if result.URL == "" {
			result.URL = c.videoContentURL(response.ID)
			result.AuthenticatedURL = true
		}
		return result, nil
	}
	if isFailedVideoStatus(response.Status) {
		return MediaResult{}, videoFailure(response)
	}
	if response.ID == "" {
		return MediaResult{}, &APIError{Code: "missing_remote_id", Message: "供应商没有返回视频任务编号"}
	}
	return c.pollOpenAIVideo(ctx, response.ID, onProgress)
}

func (c *Client) submitOpenAIReferenceVideo(ctx context.Context, request GenerateRequest) (videoResponse, error) {
	imageData, filename, err := c.loadSourceImage(ctx, request)
	if err != nil {
		return videoResponse{}, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", request.Model)
	_ = writer.WriteField("prompt", request.Prompt)
	_ = writer.WriteField("seconds", fmt.Sprint(request.Seconds))
	_ = writer.WriteField("size", request.Size)
	part, err := writer.CreateFormFile("input_reference", filename)
	if err != nil {
		return videoResponse{}, err
	}
	if _, err := part.Write(imageData); err != nil {
		return videoResponse{}, err
	}
	if err := writer.Close(); err != nil {
		return videoResponse{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, c.endpoint("videos"), writer.FormDataContentType(), &body, true)
	if err != nil {
		return videoResponse{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return videoResponse{}, normalizeTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return videoResponse{}, parseAPIError(resp)
	}
	var result videoResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&result); err != nil {
		return videoResponse{}, &APIError{Code: "invalid_video_response", Message: "视频响应格式无效", Detail: err.Error()}
	}
	return result, nil
}

func (c *Client) pollOpenAIVideo(ctx context.Context, remoteID string, onProgress ProgressFunc) (MediaResult, error) {
	endpoint := c.endpoint("videos/" + url.PathEscape(remoteID))
	for {
		if err := waitForPoll(ctx); err != nil {
			return MediaResult{}, normalizeTransportError(err)
		}
		var response videoResponse
		if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
			return MediaResult{}, err
		}
		response.ID = remoteID
		emitProgress(onProgress, Progress{Stage: videoStage(response.Status), Status: normalizeVideoStatus(response.Status), Progress: response.Progress, RemoteID: remoteID})
		if result, ok := completedVideoResult(response, false); ok {
			if result.URL == "" {
				result.URL = c.videoContentURL(remoteID)
				result.AuthenticatedURL = true
			}
			return result, nil
		}
		if isFailedVideoStatus(response.Status) {
			return MediaResult{}, videoFailure(response)
		}
	}
}

func (c *Client) generateSiliconFlowVideo(ctx context.Context, request GenerateRequest, onProgress ProgressFunc) (MediaResult, error) {
	payload := map[string]any{
		"model":      request.Model,
		"prompt":     request.Prompt,
		"image_size": request.Size,
	}
	if request.Capability == CapabilityImageToVideo {
		source, err := sourceAsDataURL(request)
		if err != nil {
			return MediaResult{}, err
		}
		payload["image"] = source
	}
	var submitted videoResponse
	emitProgress(onProgress, Progress{Stage: "提交视频任务", Status: "running", Progress: 5})
	if err := c.doJSON(ctx, http.MethodPost, c.endpoint("video/submit"), payload, &submitted); err != nil {
		return MediaResult{}, err
	}
	remoteID := submitted.RequestID
	if remoteID == "" {
		remoteID = submitted.ID
	}
	if remoteID == "" {
		return MediaResult{}, &APIError{Code: "missing_remote_id", Message: "硅基流动没有返回视频任务编号"}
	}
	for {
		if err := waitForPoll(ctx); err != nil {
			return MediaResult{}, normalizeTransportError(err)
		}
		var response videoResponse
		if err := c.doJSON(ctx, http.MethodPost, c.endpoint("video/status"), map[string]string{"requestId": remoteID}, &response); err != nil {
			return MediaResult{}, err
		}
		status := response.Status
		if response.Data.Status != "" {
			status = response.Data.Status
		}
		emitProgress(onProgress, Progress{Stage: videoStage(status), Status: normalizeVideoStatus(status), Progress: response.Progress, RemoteID: remoteID})
		if result, ok := completedVideoResult(response, false); ok {
			result.RemoteID = remoteID
			return result, nil
		}
		if isFailedVideoStatus(status) {
			return MediaResult{}, videoFailure(response)
		}
	}
}

func (c *Client) generateTogetherVideo(ctx context.Context, request GenerateRequest, onProgress ProgressFunc) (MediaResult, error) {
	payload := map[string]any{
		"model":   request.Model,
		"prompt":  request.Prompt,
		"seconds": request.Seconds,
		"size":    request.Size,
	}
	if request.Capability == CapabilityImageToVideo {
		source, err := sourceAsDataURL(request)
		if err != nil {
			return MediaResult{}, err
		}
		payload["image_url"] = source
	}
	var response videoResponse
	endpoint := c.rootEndpoint("v2/videos")
	emitProgress(onProgress, Progress{Stage: "提交视频任务", Status: "running", Progress: 5})
	if err := c.doJSON(ctx, http.MethodPost, endpoint, payload, &response); err != nil {
		return MediaResult{}, err
	}
	if result, ok := completedVideoResult(response, false); ok {
		return result, nil
	}
	if response.ID == "" {
		return MediaResult{}, &APIError{Code: "missing_remote_id", Message: "Together 没有返回视频任务编号"}
	}
	remoteID := response.ID
	for {
		if err := waitForPoll(ctx); err != nil {
			return MediaResult{}, normalizeTransportError(err)
		}
		if err := c.doJSON(ctx, http.MethodGet, c.rootEndpoint("v2/videos/"+url.PathEscape(remoteID)), nil, &response); err != nil {
			return MediaResult{}, err
		}
		emitProgress(onProgress, Progress{Stage: videoStage(response.Status), Status: normalizeVideoStatus(response.Status), Progress: response.Progress, RemoteID: remoteID})
		if result, ok := completedVideoResult(response, false); ok {
			result.RemoteID = remoteID
			return result, nil
		}
		if isFailedVideoStatus(response.Status) {
			return MediaResult{}, videoFailure(response)
		}
	}
}

func completedVideoResult(response videoResponse, authenticated bool) (MediaResult, bool) {
	status := response.Status
	if response.Data.Status != "" {
		status = response.Data.Status
	}
	if status != "" && normalizeVideoStatus(status) != "succeeded" {
		return MediaResult{}, false
	}
	videoURL := response.URL
	if videoURL == "" {
		videoURL = response.VideoURL
	}
	if videoURL == "" && len(response.Outputs) > 0 {
		videoURL = response.Outputs[0].URL
	}
	if videoURL == "" && len(response.Results.Videos) > 0 {
		videoURL = response.Results.Videos[0].URL
	}
	if videoURL == "" && len(response.Data.Results.Videos) > 0 {
		videoURL = response.Data.Results.Videos[0].URL
	}
	completed := normalizeVideoStatus(status) == "succeeded" || (status == "" && videoURL != "")
	if !completed {
		return MediaResult{}, false
	}
	return MediaResult{RemoteID: response.ID, URL: videoURL, MimeType: "video/mp4", AuthenticatedURL: authenticated, ProviderExpiresAt: response.ExpiresAt}, true
}

func normalizeVideoStatus(status string) string {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(status), "_", ""))
	switch normalized {
	case "completed", "complete", "done", "succeeded", "success", "succeed", "ready", "finished":
		return "succeeded"
	case "failed", "failure", "error", "cancelled", "canceled":
		return "failed"
	case "queued", "pending", "inqueue", "submitted":
		return "queued"
	default:
		return "running"
	}
}

func isFailedVideoStatus(status string) bool {
	return normalizeVideoStatus(status) == "failed"
}

func videoStage(status string) string {
	switch normalizeVideoStatus(status) {
	case "queued":
		return "供应商队列中"
	case "succeeded":
		return "视频已生成"
	case "failed":
		return "视频生成失败"
	default:
		return "正在生成视频"
	}
}

func videoFailure(response videoResponse) error {
	detail := response.Error.Message
	if detail == "" {
		detail = response.Data.Reason
	}
	return &APIError{Code: "video_failed", Message: "供应商未能生成视频", Detail: detail}
}

func waitForPoll(ctx context.Context) error {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func sourceAsDataURL(request GenerateRequest) (string, error) {
	if strings.HasPrefix(request.SourceImageData, "data:image/") {
		return request.SourceImageData, nil
	}
	if strings.HasPrefix(request.SourceImageData, "http://") || strings.HasPrefix(request.SourceImageData, "https://") {
		return request.SourceImageData, nil
	}
	if request.SourceImagePath == "" {
		return "", &APIError{Code: "missing_source_image", Message: "请先选择来源图片"}
	}
	data, err := os.ReadFile(request.SourceImagePath)
	if err != nil {
		return "", &APIError{Code: "read_source_image_failed", Message: "读取来源图片失败", Detail: err.Error()}
	}
	mimeType := http.DetectContentType(data)
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func (c *Client) videoContentURL(remoteID string) string {
	endpoint, err := url.Parse(c.endpoint("videos/" + url.PathEscape(remoteID) + "/content"))
	if err != nil {
		return c.endpoint("videos/" + url.PathEscape(remoteID) + "/content")
	}
	query := endpoint.Query()
	query.Set("variant", "video")
	endpoint.RawQuery = query.Encode()
	return endpoint.String()
}

func (c *Client) DownloadTo(ctx context.Context, result MediaResult, destination string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return "", &APIError{Code: "create_output_directory_failed", Message: "无法创建输出目录", Detail: err.Error()}
	}
	temporary := destination + ".part"
	file, err := os.Create(temporary)
	if err != nil {
		return "", &APIError{Code: "create_output_failed", Message: "无法创建输出文件", Detail: err.Error()}
	}
	mimeType, copyErr := c.copyMedia(ctx, result, file)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(temporary)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(temporary)
		return "", &APIError{Code: "write_output_failed", Message: "写入输出文件失败", Detail: closeErr.Error()}
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return "", &APIError{Code: "save_output_failed", Message: "保存输出文件失败", Detail: err.Error()}
	}
	return mimeType, nil
}

func (c *Client) copyMedia(ctx context.Context, result MediaResult, target io.Writer) (string, error) {
	if result.Base64 != "" {
		decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(result.Base64))
		if _, err := io.Copy(target, io.LimitReader(decoder, maxMediaDownloadSize+1)); err != nil {
			return "", &APIError{Code: "decode_media_failed", Message: "解码生成结果失败", Detail: err.Error()}
		}
		return result.MimeType, nil
	}
	if result.URL == "" {
		return "", &APIError{Code: "empty_media_result", Message: "生成结果没有可下载的媒体"}
	}
	// 下载入口再次规范化媒体地址，兼容供应商或历史任务返回的相对路径。
	resolved, err := c.resolveProviderMediaResult(result)
	if err != nil {
		return "", err
	}
	result = resolved
	req, err := c.newRequest(ctx, http.MethodGet, result.URL, "", nil, result.AuthenticatedURL)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "*/*")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", normalizeTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", parseAPIError(resp)
	}
	limited := &io.LimitedReader{R: resp.Body, N: maxMediaDownloadSize + 1}
	written, err := io.Copy(target, limited)
	if err != nil {
		return "", &APIError{Code: "download_media_failed", Message: "下载生成结果失败", Detail: err.Error(), Retryable: true}
	}
	if written > maxMediaDownloadSize {
		return "", &APIError{Code: "media_too_large", Message: "生成结果超过 512 MB 限制"}
	}
	mimeType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if mimeType == "" {
		mimeType = result.MimeType
	}
	return mimeType, nil
}
