package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxPolishResponseSize int64 = 32 << 20

type polishCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Delta struct {
			Content json.RawMessage `json:"content"`
		} `json:"delta"`
		Text string `json:"text"`
	} `json:"choices"`
	Error   json.RawMessage `json:"error"`
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
}

func (c *Client) Polish(ctx context.Context, model, systemPrompt, prompt string) (string, error) {
	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": prompt},
		},
		"temperature": 0.7,
		"stream":      false,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", &APIError{Code: "encode_request_failed", Message: "请求参数编码失败", Detail: err.Error()}
	}
	endpoint := c.endpoint("chat/completions")
	req, err := c.newRequest(ctx, http.MethodPost, endpoint, "application/json", bytes.NewReader(encoded), true)
	if err != nil {
		return "", &APIError{Code: "create_request_failed", Message: "无法创建润色请求", Detail: err.Error()}
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", normalizeTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", parseAPIError(resp)
	}
	limited := &io.LimitedReader{R: resp.Body, N: maxPolishResponseSize + 1}
	raw, err := io.ReadAll(limited)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", normalizeTransportError(ctxErr)
		}
		return "", &APIError{Code: "read_response_failed", Message: "读取润色响应失败", Detail: err.Error(), Retryable: true}
	}
	if int64(len(raw)) > maxPolishResponseSize {
		return "", &APIError{Code: "polish_response_too_large", Message: "润色响应超过 32 MB 限制"}
	}
	return parsePolishResponse(raw, resp.Header.Get("Content-Type"), endpoint)
}

func parsePolishResponse(raw []byte, contentType, endpoint string) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", &APIError{Code: "empty_polish_result", Message: "润色模型返回了空响应"}
	}
	mediaType := strings.ToLower(contentType)
	if trimmed[0] == '<' || strings.Contains(mediaType, "text/html") {
		return "", &APIError{
			Code:    "html_response",
			Message: "润色接口返回了 HTML 页面，请检查供应商 Base URL 或模型是否支持 /chat/completions",
			Detail:  endpoint,
		}
	}
	if strings.Contains(mediaType, "text/event-stream") || bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) {
		return parsePolishSSE(trimmed)
	}
	response, err := decodePolishFrame(trimmed)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(polishText(response))
	if text == "" {
		return "", &APIError{Code: "empty_polish_result", Message: "润色模型没有返回文本"}
	}
	return text, nil
}

// parsePolishSSE 兼容忽略 stream=false 并仍返回 OpenAI 增量事件的供应商。
func parsePolishSSE(raw []byte) (string, error) {
	reader := bufio.NewReader(bytes.NewReader(raw))
	var eventName string
	var eventData strings.Builder
	var result strings.Builder
	handleEvent := func() (bool, error) {
		payload := strings.TrimSpace(eventData.String())
		eventData.Reset()
		currentEvent := strings.ToLower(strings.TrimSpace(eventName))
		eventName = ""
		if payload == "" {
			return false, nil
		}
		if payload == "[DONE]" {
			return true, nil
		}
		response, err := decodePolishFrame([]byte(payload))
		if err != nil {
			return false, err
		}
		if strings.Contains(currentEvent, "error") && response.Message != "" {
			return false, &APIError{Code: "provider_error", Message: response.Message}
		}
		result.WriteString(polishText(response))
		return false, nil
	}

	for {
		line, readErr := reader.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case trimmed == "":
			done, err := handleEvent()
			if err != nil {
				return "", err
			}
			if done {
				text := strings.TrimSpace(result.String())
				if text == "" {
					return "", &APIError{Code: "empty_polish_result", Message: "润色模型没有返回文本"}
				}
				return text, nil
			}
		case strings.HasPrefix(trimmed, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
		case strings.HasPrefix(trimmed, "data:"):
			if eventData.Len() > 0 {
				eventData.WriteByte('\n')
			}
			eventData.WriteString(strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
		case strings.HasPrefix(trimmed, ":"):
		default:
			if eventData.Len() > 0 {
				eventData.WriteByte('\n')
			}
			eventData.WriteString(trimmed)
		}
		if readErr != nil {
			if readErr != io.EOF {
				return "", &APIError{Code: "read_response_failed", Message: "读取润色流式响应失败", Detail: readErr.Error(), Retryable: true}
			}
			_, err := handleEvent()
			if err != nil {
				return "", err
			}
			text := strings.TrimSpace(result.String())
			if text == "" {
				return "", &APIError{Code: "empty_polish_result", Message: "润色模型没有返回文本"}
			}
			return text, nil
		}
	}
}

func decodePolishFrame(raw []byte) (polishCompletionResponse, error) {
	var response polishCompletionResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return polishCompletionResponse{}, &APIError{Code: "invalid_response", Message: "供应商返回了无法解析的润色数据", Detail: err.Error()}
	}
	if err := polishResponseError(response); err != nil {
		return polishCompletionResponse{}, err
	}
	return response, nil
}

func polishResponseError(response polishCompletionResponse) error {
	if len(response.Error) == 0 || string(response.Error) == "null" || string(response.Error) == "{}" || string(response.Error) == "false" {
		return nil
	}
	message := strings.TrimSpace(response.Message)
	code := strings.TrimSpace(rawPolishValue(response.Code))
	var envelope struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if json.Unmarshal(response.Error, &envelope) == nil {
		if strings.TrimSpace(envelope.Message) != "" {
			message = strings.TrimSpace(envelope.Message)
		} else if strings.TrimSpace(envelope.Detail) != "" {
			message = strings.TrimSpace(envelope.Detail)
		}
		if envelope.Code != nil {
			code = fmt.Sprint(envelope.Code)
		}
	} else {
		var text string
		if json.Unmarshal(response.Error, &text) == nil {
			message = strings.TrimSpace(text)
		}
	}
	if message == "" {
		message = "润色供应商返回了错误"
	}
	if code == "" {
		code = "provider_error"
	}
	return &APIError{Code: code, Message: message}
}

func rawPolishValue(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value any
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func polishText(response polishCompletionResponse) string {
	var builder strings.Builder
	for _, choice := range response.Choices {
		text := extractTextContent(choice.Delta.Content)
		if text == "" {
			text = extractTextContent(choice.Message.Content)
		}
		if text == "" {
			text = choice.Text
		}
		builder.WriteString(text)
	}
	return builder.String()
}

func extractTextContent(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var builder strings.Builder
		for _, part := range parts {
			builder.WriteString(part.Text)
		}
		return builder.String()
	}
	return ""
}
