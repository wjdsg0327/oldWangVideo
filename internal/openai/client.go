package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxErrorBody = 1 << 20

type Client struct {
	baseURL *url.URL
	apiKey  string
	profile ProtocolProfile
	http    *http.Client
}

func NewClient(baseURL, apiKey string, timeout time.Duration) (*Client, error) {
	normalized := strings.TrimSpace(baseURL)
	parsed, err := url.Parse(normalized)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, &APIError{Code: "invalid_base_url", Message: "Base URL 无效", Detail: normalized}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, &APIError{Code: "invalid_base_url", Message: "Base URL 仅支持 http 或 https", Detail: parsed.Scheme}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	return &Client{
		baseURL: parsed,
		apiKey:  strings.TrimSpace(apiKey),
		profile: DetectProfile(parsed),
		http:    &http.Client{Timeout: timeout},
	}, nil
}

func DetectProfile(baseURL *url.URL) ProtocolProfile {
	host := strings.ToLower(baseURL.Hostname())
	switch {
	case host == "aixoras.com" || strings.HasSuffix(host, ".aixoras.com"):
		return ProfileAIXORAS
	case host == "apikey.fun" || strings.HasSuffix(host, ".apikey.fun"):
		return ProfileAPIKeyFun
	case strings.Contains(host, "siliconflow"):
		return ProfileSiliconFlow
	case strings.Contains(host, "together"):
		return ProfileTogether
	default:
		return ProfileOpenAI
	}
}

func (c *Client) Profile() ProtocolProfile {
	return c.profile
}

func (c *Client) endpoint(relative string) string {
	result := *c.baseURL
	result.Path = strings.TrimRight(result.Path, "/") + "/" + strings.TrimLeft(relative, "/")
	return result.String()
}

func (c *Client) rootEndpoint(versionedPath string) string {
	result := *c.baseURL
	result.Path = "/" + strings.TrimLeft(versionedPath, "/")
	return result.String()
}

func (c *Client) newRequest(ctx context.Context, method, endpoint, contentType string, body io.Reader, authenticated bool) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OldWangVideo/0.1")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if authenticated && c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return req, nil
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, payload any, target any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return &APIError{Code: "encode_request_failed", Message: "请求参数编码失败", Detail: err.Error()}
		}
		body = bytes.NewReader(encoded)
	}
	req, err := c.newRequest(ctx, method, endpoint, "application/json", body, true)
	if err != nil {
		return &APIError{Code: "create_request_failed", Message: "无法创建供应商请求", Detail: err.Error()}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return normalizeTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return parseAPIError(resp)
	}
	if target == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 32<<20))
	if err := decoder.Decode(target); err != nil {
		return &APIError{Code: "invalid_response", Message: "供应商返回了无法解析的数据", Detail: err.Error(), StatusCode: resp.StatusCode}
	}
	return nil
}

func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
	var response struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, c.endpoint("models"), nil, &response); err != nil {
		return nil, err
	}
	models := make([]ModelInfo, 0, len(response.Data))
	for _, item := range response.Data {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		models = append(models, ModelInfo{ID: item.ID, OwnedBy: item.OwnedBy})
	}
	return models, nil
}

func (c *Client) TestConnection(ctx context.Context) ConnectivityResult {
	started := time.Now()
	models, err := c.ListModels(ctx)
	result := ConnectivityResult{Success: err == nil, LatencyMS: time.Since(started).Milliseconds(), Models: models}
	if err != nil {
		result.Message = err.Error()
	} else {
		result.Message = fmt.Sprintf("连接成功，发现 %d 个模型", len(models))
	}
	return result
}

func parseAPIError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	detail := strings.TrimSpace(string(data))
	code := "provider_error"
	message := fmt.Sprintf("供应商请求失败（HTTP %d）", resp.StatusCode)
	var envelope struct {
		Error struct {
			Code    any    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Code    any    `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &envelope) == nil {
		if envelope.Error.Message != "" {
			message = envelope.Error.Message
			detail = ""
			if envelope.Error.Code != nil {
				code = fmt.Sprint(envelope.Error.Code)
			}
		} else if envelope.Message != "" {
			message = envelope.Message
			detail = ""
			if envelope.Code != nil {
				code = fmt.Sprint(envelope.Code)
			}
		}
	}
	retryable := resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError
	return &APIError{Code: code, Message: message, Detail: detail, StatusCode: resp.StatusCode, Retryable: retryable}
}

func normalizeTransportError(err error) error {
	if errors.Is(err, context.Canceled) {
		return &APIError{Code: "cancelled_local", Message: "任务已在本地取消"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &APIError{Code: "timeout", Message: "供应商请求超时", Retryable: true}
	}
	return &APIError{Code: "network_error", Message: "无法连接供应商", Detail: err.Error(), Retryable: true}
}
