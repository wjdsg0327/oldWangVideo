package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func (c *Client) Generate(ctx context.Context, request GenerateRequest, onProgress ProgressFunc) (MediaResult, error) {
	request = normalizeGenerateRequest(request)
	switch request.Capability {
	case CapabilityTextToImage, CapabilityImageToImage:
		return c.generateImage(ctx, request, onProgress)
	case CapabilityTextToVideo, CapabilityImageToVideo:
		return c.generateVideo(ctx, request, onProgress)
	default:
		return MediaResult{}, &APIError{Code: "unsupported_capability", Message: "不支持的生成能力", Detail: string(request.Capability)}
	}
}

func normalizeGenerateRequest(request GenerateRequest) GenerateRequest {
	request.Model = strings.TrimSpace(request.Model)
	request.Prompt = strings.TrimSpace(request.Prompt)
	if request.Size == "" {
		request.Size = "1024x1024"
	}
	if request.Count <= 0 {
		request.Count = 1
	}
	// -1 是部分视频供应商支持的智能时长值，不能在统一归一化时被改写。
	if request.Seconds <= 0 && request.Seconds != -1 {
		request.Seconds = 5
	}
	return request
}

func (c *Client) generateImage(ctx context.Context, request GenerateRequest, onProgress ProgressFunc) (MediaResult, error) {
	emitProgress(onProgress, Progress{Stage: "提交图片请求", Status: "running", Progress: 10})
	if c.profile == ProfileAPIKeyFun {
		return c.generateAPIKeyFunImage(ctx, request, onProgress)
	}
	if request.Capability == CapabilityImageToImage && c.profile == ProfileOpenAI {
		return c.generateOpenAIImageEdit(ctx, request, onProgress)
	}
	payload := c.imageJSONPayload(request)
	var raw json.RawMessage
	if err := c.doJSON(ctx, http.MethodPost, c.endpoint("images/generations"), payload, &raw); err != nil {
		return MediaResult{}, err
	}
	result, err := parseImageResponse(raw)
	if err != nil {
		return MediaResult{}, err
	}
	emitProgress(onProgress, Progress{Stage: "图片已生成", Status: "succeeded", Progress: 100})
	return result, nil
}

func (c *Client) imageJSONPayload(request GenerateRequest) map[string]any {
	payload := map[string]any{
		"model":  request.Model,
		"prompt": request.Prompt,
	}
	switch c.profile {
	case ProfileSiliconFlow:
		payload["image_size"] = request.Size
		payload["batch_size"] = request.Count
		if request.NegativePrompt != "" {
			payload["negative_prompt"] = request.NegativePrompt
		}
		if request.Capability == CapabilityImageToImage {
			payload["image"] = request.SourceImageData
		}
	case ProfileTogether:
		payload["n"] = request.Count
		if width, height := parseSize(request.Size); width > 0 && height > 0 {
			payload["width"] = width
			payload["height"] = height
		}
		if request.Capability == CapabilityImageToImage {
			payload["image_url"] = request.SourceImageData
		}
	default:
		payload["n"] = request.Count
		payload["size"] = request.Size
	}
	return payload
}

func (c *Client) generateOpenAIImageEdit(ctx context.Context, request GenerateRequest, onProgress ProgressFunc) (MediaResult, error) {
	imageData, filename, err := c.loadSourceImage(ctx, request)
	if err != nil {
		return MediaResult{}, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", request.Model); err != nil {
		return MediaResult{}, err
	}
	if err := writer.WriteField("prompt", request.Prompt); err != nil {
		return MediaResult{}, err
	}
	_ = writer.WriteField("n", fmt.Sprint(request.Count))
	_ = writer.WriteField("size", request.Size)
	imageHeader := make(textproto.MIMEHeader)
	imageHeader.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
		"name":     "image",
		"filename": filename,
	}))
	imageHeader.Set("Content-Type", http.DetectContentType(imageData))
	part, err := writer.CreatePart(imageHeader)
	if err != nil {
		return MediaResult{}, err
	}
	if _, err := part.Write(imageData); err != nil {
		return MediaResult{}, err
	}
	if err := writer.Close(); err != nil {
		return MediaResult{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, c.endpoint("images/edits"), writer.FormDataContentType(), &body, true)
	if err != nil {
		return MediaResult{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return MediaResult{}, normalizeTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return MediaResult{}, parseAPIError(resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return MediaResult{}, &APIError{Code: "read_response_failed", Message: "读取图片响应失败", Detail: err.Error()}
	}
	result, err := parseImageResponse(raw)
	if err != nil {
		return MediaResult{}, err
	}
	emitProgress(onProgress, Progress{Stage: "图片已生成", Status: "succeeded", Progress: 100})
	return result, nil
}

func parseImageResponse(raw []byte) (MediaResult, error) {
	type imageAsset struct {
		URL     string `json:"url"`
		B64JSON string `json:"b64_json"`
		Base64  string `json:"base64"`
	}
	var response struct {
		Data   []imageAsset `json:"data"`
		Images []imageAsset `json:"images"`
		Output []imageAsset `json:"output"`
		URL    string       `json:"url"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return MediaResult{}, &APIError{Code: "invalid_image_response", Message: "图片响应格式无效", Detail: err.Error()}
	}
	assets := append(response.Data, response.Images...)
	assets = append(assets, response.Output...)
	if len(assets) == 0 && response.URL != "" {
		assets = append(assets, imageAsset{URL: response.URL})
	}
	if len(assets) == 0 {
		return MediaResult{}, &APIError{Code: "empty_image_result", Message: "供应商没有返回图片"}
	}
	asset := assets[0]
	encoded := asset.B64JSON
	if encoded == "" {
		encoded = asset.Base64
	}
	return MediaResult{URL: asset.URL, Base64: encoded, MimeType: "image/png"}, nil
}

func (c *Client) loadSourceImage(ctx context.Context, request GenerateRequest) ([]byte, string, error) {
	if request.SourceImagePath != "" {
		file, err := os.Open(request.SourceImagePath)
		if err != nil {
			return nil, "", &APIError{Code: "read_source_image_failed", Message: "读取来源图片失败", Detail: err.Error()}
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, maxSourceImageDownload+1))
		if err != nil {
			return nil, "", &APIError{Code: "read_source_image_failed", Message: "读取来源图片失败", Detail: err.Error()}
		}
		if len(data) > maxSourceImageDownload {
			return nil, "", &APIError{Code: "source_image_too_large", Message: "来源图片超过 25 MB"}
		}
		return validateSourceImageBytes(data, filepath.Base(request.SourceImagePath))
	}
	dataURL := request.SourceImageData
	if strings.HasPrefix(dataURL, "http://") || strings.HasPrefix(dataURL, "https://") {
		parsed, err := url.Parse(dataURL)
		if err != nil || parsed.Host == "" {
			return nil, "", &APIError{Code: "invalid_source_image", Message: "来源图片 URL 无效"}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return nil, "", &APIError{Code: "invalid_source_image", Message: "来源图片 URL 无效", Detail: err.Error()}
		}
		req.Header.Set("Accept", "image/png,image/jpeg,image/webp")
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, "", normalizeTransportError(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return nil, "", &APIError{Code: "source_image_download_failed", Message: "无法下载来源图片", StatusCode: resp.StatusCode}
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxSourceImageDownload+1))
		if err != nil {
			return nil, "", &APIError{Code: "source_image_download_failed", Message: "无法读取来源图片", Detail: err.Error()}
		}
		return validateSourceImageBytes(data, filepath.Base(parsed.Path))
	}
	comma := strings.IndexByte(dataURL, ',')
	if comma < 0 {
		return nil, "", &APIError{Code: "missing_source_image", Message: "请先选择来源图片"}
	}
	header, encoded := dataURL[:comma], dataURL[comma+1:]
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil {
		return nil, "", &APIError{Code: "invalid_source_image", Message: "来源图片编码无效", Detail: err.Error()}
	}
	mimeType := strings.TrimPrefix(strings.Split(header, ";")[0], "data:")
	if !supportedSourceImageMIME(mimeType) {
		return nil, "", &APIError{Code: "invalid_source_image", Message: "仅支持 PNG、JPEG 或 WebP 图片"}
	}
	return validateSourceImageBytes(data, "source"+sourceImageExtension(mimeType))
}

const maxSourceImageDownload = 25 << 20

func validateSourceImageBytes(data []byte, filename string) ([]byte, string, error) {
	if len(data) == 0 || len(data) > maxSourceImageDownload {
		return nil, "", &APIError{Code: "source_image_too_large", Message: "来源图片超过 25 MB"}
	}
	mimeType := http.DetectContentType(data)
	if !supportedSourceImageMIME(mimeType) {
		return nil, "", &APIError{Code: "invalid_source_image", Message: "仅支持 PNG、JPEG 或 WebP 图片"}
	}
	if strings.TrimSpace(filename) == "" || filepath.Base(filename) == "." {
		filename = "source" + sourceImageExtension(mimeType)
	}
	return data, filepath.Base(filename), nil
}

func supportedSourceImageMIME(value string) bool {
	return value == "image/png" || value == "image/jpeg" || value == "image/webp"
}

func sourceImageExtension(mimeType string) string {
	extensions, _ := mime.ExtensionsByType(mimeType)
	for _, extension := range extensions {
		if extension == ".png" || extension == ".jpg" || extension == ".jpeg" || extension == ".webp" {
			return extension
		}
	}
	return ".png"
}

func parseSize(value string) (int, int) {
	var width, height int
	_, _ = fmt.Sscanf(strings.ToLower(value), "%dx%d", &width, &height)
	return width, height
}

func emitProgress(callback ProgressFunc, progress Progress) {
	if callback != nil {
		callback(progress)
	}
}
