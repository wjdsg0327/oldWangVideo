package openai

import (
	"fmt"
	"time"
)

type Capability string

const (
	CapabilityTextToImage  Capability = "text_to_image"
	CapabilityTextToVideo  Capability = "text_to_video"
	CapabilityImageToImage Capability = "image_to_image"
	CapabilityImageToVideo Capability = "image_to_video"
)

type ProtocolProfile string

const (
	ProfileOpenAI      ProtocolProfile = "openai"
	ProfileSiliconFlow ProtocolProfile = "siliconflow"
	ProfileTogether    ProtocolProfile = "together"
	ProfileAPIKeyFun   ProtocolProfile = "apikeyfun"
	ProfileAIXORAS     ProtocolProfile = "aixoras"
	ProfileDoubao      ProtocolProfile = "doubao"
	ProfileFastAI      ProtocolProfile = "fastai"
)

type ModelInfo struct {
	ID      string `json:"id"`
	OwnedBy string `json:"ownedBy"`
}

type GenerateRequest struct {
	Capability      Capability `json:"capability"`
	Model           string     `json:"model"`
	Prompt          string     `json:"prompt"`
	NegativePrompt  string     `json:"negativePrompt"`
	SourceImagePath string     `json:"sourceImagePath"`
	SourceImageData string     `json:"sourceImageData"`
	Size            string     `json:"size"`
	Seconds         int        `json:"seconds"`
	Count           int        `json:"count"`
}

type Progress struct {
	Stage    string `json:"stage"`
	Status   string `json:"status"`
	Progress int    `json:"progress"`
	RemoteID string `json:"remoteId"`
}

type ProgressFunc func(Progress)

type MediaResult struct {
	RemoteID          string `json:"remoteId"`
	URL               string `json:"url"`
	Base64            string `json:"base64"`
	MimeType          string `json:"mimeType"`
	AuthenticatedURL  bool   `json:"authenticatedUrl"`
	ProviderExpiresAt int64  `json:"providerExpiresAt"`
}

type APIError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Detail     string `json:"detail,omitempty"`
	StatusCode int    `json:"statusCode"`
	Retryable  bool   `json:"retryable"`
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Detail == "" {
		return e.Message
	}
	return fmt.Sprintf("%s：%s", e.Message, e.Detail)
}

type ConnectivityResult struct {
	Success   bool        `json:"success"`
	LatencyMS int64       `json:"latencyMs"`
	Models    []ModelInfo `json:"models"`
	Message   string      `json:"message"`
}

const defaultRequestTimeout = 90 * time.Second
