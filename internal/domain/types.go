package domain

import "time"

// Capability 表示模型支持的业务能力。
type Capability string

const (
	CapabilityTextToImage  Capability = "text_to_image"
	CapabilityTextToVideo  Capability = "text_to_video"
	CapabilityImageToImage Capability = "image_to_image"
	CapabilityImageToVideo Capability = "image_to_video"
	CapabilityPromptPolish Capability = "prompt_polish"
)

// AllCapabilities 返回界面可配置的全部能力。
func AllCapabilities() []Capability {
	return []Capability{
		CapabilityTextToImage,
		CapabilityTextToVideo,
		CapabilityImageToImage,
		CapabilityImageToVideo,
		CapabilityPromptPolish,
	}
}

// Valid 判断能力值是否受支持。
func (c Capability) Valid() bool {
	switch c {
	case CapabilityTextToImage,
		CapabilityTextToVideo,
		CapabilityImageToImage,
		CapabilityImageToVideo,
		CapabilityPromptPolish:
		return true
	default:
		return false
	}
}

// IsGeneration 判断能力是否属于媒体生成模块。
func (c Capability) IsGeneration() bool {
	return c.Valid() && c != CapabilityPromptPolish
}

// Provider 是可安全返回到前端的供应商信息。
type Provider struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	BaseURL   string    `json:"baseUrl"`
	HasAPIKey bool      `json:"hasApiKey"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ProviderCreateInput 是新增供应商的输入。
type ProviderCreateInput struct {
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Enabled bool   `json:"enabled"`
}

// ProviderUpdateInput 是更新供应商的输入，APIKey 为 nil 时保留原值。
type ProviderUpdateInput struct {
	Name    string  `json:"name"`
	BaseURL string  `json:"baseUrl"`
	APIKey  *string `json:"apiKey,omitempty"`
	Enabled bool    `json:"enabled"`
}

// ProviderPreset 是一键填充供应商地址的内置预设。
type ProviderPreset struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	BaseURL        string `json:"baseUrl"`
	Description    string `json:"description"`
	Local          bool   `json:"local"`
	APIKeyRequired bool   `json:"apiKeyRequired"`
}

// Model 是供应商下的模型配置。
type Model struct {
	ID           int64        `json:"id"`
	ProviderID   int64        `json:"providerId"`
	ProviderName string       `json:"providerName,omitempty"`
	RemoteID     string       `json:"remoteId"`
	Name         string       `json:"name"`
	Capabilities []Capability `json:"capabilities"`
	Manual       bool         `json:"manual"`
	Enabled      bool         `json:"enabled"`
	CreatedAt    time.Time    `json:"createdAt"`
	UpdatedAt    time.Time    `json:"updatedAt"`
}

// ModelCreateInput 是手动新增模型的输入。
type ModelCreateInput struct {
	ProviderID   int64        `json:"providerId"`
	RemoteID     string       `json:"remoteId"`
	Name         string       `json:"name"`
	Capabilities []Capability `json:"capabilities"`
	Enabled      bool         `json:"enabled"`
}

// ModelUpdateInput 是更新模型及能力的输入。
type ModelUpdateInput struct {
	RemoteID     string       `json:"remoteId"`
	Name         string       `json:"name"`
	Capabilities []Capability `json:"capabilities"`
	Enabled      bool         `json:"enabled"`
}

// RemoteModel 是从兼容接口拉取到的最小模型信息。
type RemoteModel struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Capabilities []Capability `json:"capabilities,omitempty"`
}

// ModelFilter 控制模型列表的筛选条件。
type ModelFilter struct {
	ProviderID  int64      `json:"providerId,omitempty"`
	Capability  Capability `json:"capability,omitempty"`
	EnabledOnly bool       `json:"enabledOnly,omitempty"`
	UsableOnly  bool       `json:"usableOnly,omitempty"`
}

// ModelTarget 标识一次请求选中的供应商与模型。
type ModelTarget struct {
	ProviderID int64 `json:"providerId"`
	ModelID    int64 `json:"modelId"`
}

// ImageInput 表示图生图或图生视频的输入图片。
type ImageInput struct {
	Name     string `json:"name,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	Base64   string `json:"base64,omitempty"`
	URL      string `json:"url,omitempty"`
}

// GenerationOptions 是各媒体接口共享的常用参数。
type GenerationOptions struct {
	Size            string         `json:"size,omitempty"`
	AspectRatio     string         `json:"aspectRatio,omitempty"`
	Width           int            `json:"width,omitempty"`
	Height          int            `json:"height,omitempty"`
	DurationSeconds int            `json:"durationSeconds,omitempty"`
	FPS             int            `json:"fps,omitempty"`
	Steps           int            `json:"steps,omitempty"`
	GuidanceScale   float64        `json:"guidanceScale,omitempty"`
	Seed            int64          `json:"seed,omitempty"`
	Count           int            `json:"count,omitempty"`
	Extra           map[string]any `json:"extra,omitempty"`
}

// GenerationRequest 是四类生成模块共用的请求结构。
type GenerationRequest struct {
	Capability     Capability        `json:"capability"`
	Prompt         string            `json:"prompt"`
	NegativePrompt string            `json:"negativePrompt,omitempty"`
	Images         []ImageInput      `json:"images"`
	Targets        []ModelTarget     `json:"targets"`
	Options        GenerationOptions `json:"options"`
}

// PromptPolishRequest 是提示词多模型润色请求。
type PromptPolishRequest struct {
	Prompt       string        `json:"prompt"`
	SystemPrompt string        `json:"systemPrompt,omitempty"`
	TemplateID   int64         `json:"templateId,omitempty"`
	Targets      []ModelTarget `json:"targets"`
}

// MediaType 区分图片与视频结果。
type MediaType string

const (
	MediaTypeImage MediaType = "image"
	MediaTypeVideo MediaType = "video"
)

// MediaAsset 是生成结果中的单个媒体文件。
type MediaAsset struct {
	Type         MediaType      `json:"type"`
	URL          string         `json:"url,omitempty"`
	LocalPath    string         `json:"localPath,omitempty"`
	ThumbnailURL string         `json:"thumbnailUrl,omitempty"`
	MIMEType     string         `json:"mimeType,omitempty"`
	Width        int            `json:"width,omitempty"`
	Height       int            `json:"height,omitempty"`
	Duration     float64        `json:"duration,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// GenerationResult 是一个模型任务返回的标准化结果。
type GenerationResult struct {
	Assets        []MediaAsset   `json:"assets"`
	RevisedPrompt string         `json:"revisedPrompt,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

// PromptPolishResult 是一个润色模型返回的候选版本。
type PromptPolishResult struct {
	Text     string         `json:"text"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// TaskKind 区分媒体生成任务和提示词润色任务。
type TaskKind string

const (
	TaskKindGeneration   TaskKind = "generation"
	TaskKindPromptPolish TaskKind = "prompt_polish"
)

// TaskStatus 表示独立任务的生命周期状态。
type TaskStatus string

const (
	TaskStatusQueued    TaskStatus = "queued"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusSucceeded TaskStatus = "succeeded"
	TaskStatusFailed    TaskStatus = "failed"
	TaskStatusCanceled  TaskStatus = "canceled"
)

// Valid 判断任务状态是否受支持。
func (s TaskStatus) Valid() bool {
	switch s {
	case TaskStatusQueued, TaskStatusRunning, TaskStatusSucceeded, TaskStatusFailed, TaskStatusCanceled:
		return true
	default:
		return false
	}
}

// Terminal 判断任务是否已经结束。
func (s TaskStatus) Terminal() bool {
	return s == TaskStatusSucceeded || s == TaskStatusFailed || s == TaskStatusCanceled
}

// TaskSnapshot 是可通过事件推送到前端的任务快照。
type TaskSnapshot struct {
	ID           string              `json:"id"`
	BatchID      string              `json:"batchId"`
	Kind         TaskKind            `json:"kind"`
	Capability   Capability          `json:"capability,omitempty"`
	ProviderID   int64               `json:"providerId"`
	ProviderName string              `json:"providerName"`
	ModelID      int64               `json:"modelId"`
	ModelName    string              `json:"modelName"`
	Status       TaskStatus          `json:"status"`
	Stage        string              `json:"stage,omitempty"`
	Progress     int                 `json:"progress"`
	Revision     int64               `json:"revision"`
	Prompt       string              `json:"prompt"`
	Result       *GenerationResult   `json:"result,omitempty"`
	PolishResult *PromptPolishResult `json:"polishResult,omitempty"`
	ErrorMessage string              `json:"errorMessage,omitempty"`
	ErrorCode    string              `json:"errorCode,omitempty"`
	Retryable    bool                `json:"retryable"`
	CancelMode   string              `json:"cancelMode,omitempty"`
	CreatedAt    time.Time           `json:"createdAt"`
	StartedAt    *time.Time          `json:"startedAt,omitempty"`
	CompletedAt  *time.Time          `json:"completedAt,omitempty"`
	UpdatedAt    time.Time           `json:"updatedAt"`
}

// BatchStart 是提交并发任务后立即返回给前端的占位数据。
type BatchStart struct {
	BatchID string         `json:"batchId"`
	Tasks   []TaskSnapshot `json:"tasks"`
}

// PromptTemplate 是可复用的润色系统提示词。
type PromptTemplate struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	SystemPrompt string    `json:"systemPrompt"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// PromptTemplateInput 是新增或更新润色模板的输入。
type PromptTemplateInput struct {
	Name         string `json:"name"`
	SystemPrompt string `json:"systemPrompt"`
	Enabled      bool   `json:"enabled"`
}

// HistoryCreateInput 是创建生成历史记录的输入。
type HistoryCreateInput struct {
	TaskID       string            `json:"taskId"`
	BatchID      string            `json:"batchId"`
	ProviderID   int64             `json:"providerId"`
	ModelID      int64             `json:"modelId"`
	ProviderName string            `json:"providerName"`
	ModelName    string            `json:"modelName"`
	Capability   Capability        `json:"capability"`
	Request      GenerationRequest `json:"request"`
	Status       TaskStatus        `json:"status"`
	CreatedAt    time.Time         `json:"createdAt,omitempty"`
}

// HistoryStatusUpdate 是任务执行过程中写回历史的增量信息。
type HistoryStatusUpdate struct {
	Status       TaskStatus        `json:"status"`
	Result       *GenerationResult `json:"result,omitempty"`
	ErrorMessage *string           `json:"errorMessage,omitempty"`
	StartedAt    *time.Time        `json:"startedAt,omitempty"`
	CompletedAt  *time.Time        `json:"completedAt,omitempty"`
}

// GenerationHistory 是持久化后的单模型生成记录。
type GenerationHistory struct {
	ID           int64             `json:"id"`
	TaskID       string            `json:"taskId"`
	BatchID      string            `json:"batchId"`
	ProviderID   int64             `json:"providerId,omitempty"`
	ModelID      int64             `json:"modelId,omitempty"`
	ProviderName string            `json:"providerName"`
	ModelName    string            `json:"modelName"`
	Capability   Capability        `json:"capability"`
	Request      GenerationRequest `json:"request"`
	Status       TaskStatus        `json:"status"`
	Result       *GenerationResult `json:"result,omitempty"`
	ErrorMessage string            `json:"errorMessage,omitempty"`
	Favorite     bool              `json:"favorite"`
	FavoritedAt  *time.Time        `json:"favoritedAt,omitempty"`
	CreatedAt    time.Time         `json:"createdAt"`
	StartedAt    *time.Time        `json:"startedAt,omitempty"`
	CompletedAt  *time.Time        `json:"completedAt,omitempty"`
	UpdatedAt    time.Time         `json:"updatedAt"`
}

// PageRequest 是统一的分页输入，页码从一开始。
type PageRequest struct {
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
}

// HistoryFilter 控制生成历史的查询条件。
type HistoryFilter struct {
	PageRequest
	Capability   Capability `json:"capability,omitempty"`
	Status       TaskStatus `json:"status,omitempty"`
	FavoriteOnly bool       `json:"favoriteOnly,omitempty"`
	Search       string     `json:"search,omitempty"`
}

// HistoryPage 是生成历史的分页结果。
type HistoryPage struct {
	Items    []GenerationHistory `json:"items"`
	Total    int64               `json:"total"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"pageSize"`
}

// Favorite 是带收藏时间的生成历史快照。
type Favorite struct {
	History   GenerationHistory `json:"history"`
	CreatedAt time.Time         `json:"createdAt"`
}

// FavoritePage 是收藏结果的分页结构。
type FavoritePage struct {
	Items    []Favorite `json:"items"`
	Total    int64      `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
}

// Bootstrap 是应用首屏所需的本地数据集合。
type Bootstrap struct {
	Providers       []Provider          `json:"providers"`
	Models          []Model             `json:"models"`
	PromptTemplates []PromptTemplate    `json:"promptTemplates"`
	ProviderPresets []ProviderPreset    `json:"providerPresets"`
	Capabilities    []Capability        `json:"capabilities"`
	RecentHistory   []GenerationHistory `json:"recentHistory"`
	Favorites       []GenerationHistory `json:"favorites"`
}
