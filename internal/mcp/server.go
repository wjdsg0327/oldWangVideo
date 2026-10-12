// Package mcp 提供供本地 AI 客户端调用的 Model Context Protocol 服务。
package mcp

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"oldwangvideo/internal/domain"
	"oldwangvideo/internal/store"
	taskmanager "oldwangvideo/internal/task"
)

const (
	// protocolVersion 是当前实现支持的 MCP 协议版本。
	protocolVersion = "2024-11-05"
	// defaultCallTimeout 防止供应商异常时长期占用 MCP 客户端连接。
	defaultCallTimeout = 10 * time.Minute
	// maxInputImageSize 与桌面端图生图输入限制保持一致。
	maxInputImageSize = 25 << 20
	// maxReturnedImageSize 限制通过 JSON-RPC 返回的图片大小，避免阻塞本地客户端。
	maxReturnedImageSize = 32 << 20
)

// Options 控制 MCP 服务使用的本地数据目录和生成输出目录。
type Options struct {
	DataDir   string
	OutputDir string
}

// Run 使用标准输入输出运行 MCP stdio 服务。
func Run(ctx context.Context, options Options) error {
	return RunWithIO(ctx, os.Stdin, os.Stdout, options)
}

// RunWithIO 使用指定输入输出运行服务，便于桌面进程和自动化验证复用同一实现。
func RunWithIO(ctx context.Context, input io.Reader, output io.Writer, options Options) error {
	if input == nil || output == nil {
		return fmt.Errorf("MCP 输入输出流不能为空")
	}
	// 调用方未提供上下文时使用可取消的根上下文，避免数据库和任务管理器收到 nil。
	if ctx == nil {
		ctx = context.Background()
	}
	dataDir := strings.TrimSpace(options.DataDir)
	if dataDir == "" {
		return fmt.Errorf("MCP 数据目录不能为空")
	}
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("解析 MCP 数据目录失败: %w", err)
	}
	outputDir := strings.TrimSpace(options.OutputDir)
	if outputDir == "" {
		outputDir = filepath.Join(dataDir, "outputs")
	}
	outputDir, err = filepath.Abs(outputDir)
	if err != nil {
		return fmt.Errorf("解析 MCP 输出目录失败: %w", err)
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return fmt.Errorf("创建 MCP 输出目录失败: %w", err)
	}
	database, err := store.Open(ctx, dataDir)
	if err != nil {
		return fmt.Errorf("打开 MCP 数据库失败: %w", err)
	}
	defer database.Close()
	manager := taskmanager.NewManager(ctx, database, outputDir, nil)
	defer manager.Shutdown()
	server := &server{database: database, manager: manager}
	return server.serve(ctx, input, output)
}

// server 持有一次 MCP 会话所需的数据库和任务管理器。
type server struct {
	database *store.Store
	manager  *taskmanager.Manager
}

// serve 按 MCP stdio 约定逐行读取 JSON-RPC 请求并写回响应。
func (s *server) serve(ctx context.Context, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 64<<20)
	writer := bufio.NewWriter(output)
	defer writer.Flush()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		response := s.handle(ctx, []byte(line))
		if response == nil {
			continue
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return fmt.Errorf("编码 MCP 响应失败: %w", err)
		}
		if _, err := writer.Write(append(encoded, '\n')); err != nil {
			return fmt.Errorf("写入 MCP 响应失败: %w", err)
		}
		if err := writer.Flush(); err != nil {
			return fmt.Errorf("刷新 MCP 响应失败: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取 MCP 请求失败: %w", err)
	}
	return nil
}

// jsonRPCRequest 是 MCP 使用的 JSON-RPC 2.0 请求最小结构。
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// jsonRPCResponse 是 MCP 请求的成功或失败响应。
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

// jsonRPCError 是 JSON-RPC 标准错误对象。
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// handle 分发 MCP 方法，通知消息不返回 JSON-RPC 响应。
func (s *server) handle(ctx context.Context, raw []byte) *jsonRPCResponse {
	var request jsonRPCRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return &jsonRPCResponse{JSONRPC: "2.0", Error: &jsonRPCError{Code: -32700, Message: "JSON 解析失败"}}
	}
	if request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" {
		return s.errorResponse(request.ID, -32600, "无效的 JSON-RPC 请求", nil)
	}
	if strings.HasPrefix(request.Method, "notifications/") {
		return nil
	}
	var result any
	var err error
	switch request.Method {
	case "initialize":
		result = initializeResult()
	case "ping":
		result = map[string]any{}
	case "tools/list":
		result = map[string]any{"tools": toolDefinitions()}
	case "tools/call":
		result, err = s.callTool(ctx, request.Params)
	default:
		return s.errorResponse(request.ID, -32601, "MCP 方法不存在", request.Method)
	}
	if err != nil {
		// MCP 工具执行失败使用 isError 结果，让客户端把错误显示在工具调用上下文中。
		if request.Method == "tools/call" {
			return &jsonRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: toolErrorResult(err)}
		}
		return s.errorResponse(request.ID, -32000, err.Error(), nil)
	}
	return &jsonRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: result}
}

// initializeResult 返回客户端协商所需的协议版本、能力和服务信息。
func initializeResult() map[string]any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "oldwangvideo-image", "version": "1.0.0"},
		"instructions":    "使用 generate_image 进行文生图，使用 image_to_image 进行图生图；模型可通过 list_image_models 查询。",
	}
}

// errorResponse 构造带原始请求编号的 JSON-RPC 错误响应。
func (s *server) errorResponse(id json.RawMessage, code int, message string, data any) *jsonRPCResponse {
	return &jsonRPCResponse{JSONRPC: "2.0", ID: id, Error: &jsonRPCError{Code: code, Message: message, Data: data}}
}

// toolDefinition 描述一个可被 MCP 客户端发现的工具。
type toolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// toolDefinitions 返回生图、图生图和模型查询工具的标准 MCP 描述。
func toolDefinitions() []toolDefinition {
	modelSelector := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"provider_id":     map[string]any{"type": "integer", "description": "供应商数据库编号，可选。"},
			"model_id":        map[string]any{"type": "integer", "description": "模型数据库编号，可选。"},
			"model":           map[string]any{"type": "string", "description": "模型远端编号，可选。"},
			"prompt":          map[string]any{"type": "string", "description": "图片生成提示词。"},
			"negative_prompt": map[string]any{"type": "string", "description": "反向提示词，可选。"},
			"size":            map[string]any{"type": "string", "description": "图片尺寸，例如 1024x1024。"},
			"width":           map[string]any{"type": "integer", "description": "图片宽度，可选。"},
			"height":          map[string]any{"type": "integer", "description": "图片高度，可选。"},
			"count":           map[string]any{"type": "integer", "description": "生成数量，范围 1 到 8。"},
		},
		"required": []string{"prompt"},
	}
	imageSchema := map[string]any{
		"type":        "array",
		"description": "来源图片数组。元素可为 http(s) URL、data URL、本地路径，或含 data/base64/url/mimeType 字段的对象。",
		"items": map[string]any{"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "object"},
		}},
	}
	imageToImage := cloneMap(modelSelector)
	properties := imageToImage["properties"].(map[string]any)
	properties["images"] = imageSchema
	properties["image_paths"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "本地来源图片路径，可选。"}
	properties["prompt"] = map[string]any{"type": "string", "description": "图生图提示词。"}
	return []toolDefinition{
		{Name: "list_image_models", Description: "列出当前老王创作台中已启用且标注为图片模型的供应商和模型。", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}},
		{Name: "generate_image", Description: "调用已配置的图片模型进行文生图，并返回 MCP 图片内容和本地文件路径。", InputSchema: modelSelector},
		{Name: "image_to_image", Description: "使用一张或多张来源图片调用已配置的图片模型进行图生图。", InputSchema: imageToImage},
	}
}

// cloneMap 深拷贝工具 schema 的顶层 map，避免两个工具共享可变属性。
func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	if properties, ok := source["properties"].(map[string]any); ok {
		copied := make(map[string]any, len(properties))
		for key, value := range properties {
			copied[key] = value
		}
		result["properties"] = copied
	}
	return result
}

// callTool 解析工具参数并执行对应的同步生成操作。
func (s *server) callTool(parent context.Context, raw json.RawMessage) (map[string]any, error) {
	var request struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("tools/call 缺少参数")
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, fmt.Errorf("tools/call 参数无效: %w", err)
	}
	if request.Arguments == nil {
		request.Arguments = []byte(`{}`)
	}
	switch request.Name {
	case "list_image_models":
		return s.listImageModels(parent)
	case "generate_image":
		return s.generate(parent, domain.CapabilityTextToImage, request.Arguments)
	case "image_to_image":
		return s.generate(parent, domain.CapabilityImageToImage, request.Arguments)
	default:
		return nil, fmt.Errorf("未知 MCP 工具: %s", request.Name)
	}
}

// listImageModels 返回供 MCP 客户端选择的图片模型及其数据库编号。
func (s *server) listImageModels(ctx context.Context) (map[string]any, error) {
	models, err := s.database.ListModels(ctx, domain.ModelFilter{EnabledOnly: true, UsableOnly: true})
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0)
	for _, model := range models {
		if !hasImageCapability(model.Capabilities) {
			continue
		}
		items = append(items, map[string]any{
			"providerId":   model.ProviderID,
			"provider":     model.ProviderName,
			"modelId":      model.ID,
			"model":        model.RemoteID,
			"name":         model.Name,
			"capabilities": model.Capabilities,
		})
	}
	return toolResult(map[string]any{"models": items, "count": len(items)}), nil
}

// hasImageCapability 判断模型是否标注了文生图或图生图能力。
func hasImageCapability(capabilities []domain.Capability) bool {
	for _, capability := range capabilities {
		if capability == domain.CapabilityTextToImage || capability == domain.CapabilityImageToImage {
			return true
		}
	}
	return false
}

// generationArguments 是 MCP 生图和图生图共用的输入结构。
type generationArguments struct {
	ProviderID     int64             `json:"provider_id"`
	ModelID        int64             `json:"model_id"`
	Model          string            `json:"model"`
	Prompt         string            `json:"prompt"`
	NegativePrompt string            `json:"negative_prompt"`
	Size           string            `json:"size"`
	Width          int               `json:"width"`
	Height         int               `json:"height"`
	Count          int               `json:"count"`
	Images         []json.RawMessage `json:"images"`
	ImagePaths     []string          `json:"image_paths"`
}

// generate 选择模型、提交任务、等待完成并返回图片内容。
func (s *server) generate(parent context.Context, capability domain.Capability, raw json.RawMessage) (map[string]any, error) {
	var arguments generationArguments
	if err := json.Unmarshal(raw, &arguments); err != nil {
		return nil, fmt.Errorf("生成参数无效: %w", err)
	}
	arguments.Prompt = strings.TrimSpace(arguments.Prompt)
	if arguments.Prompt == "" {
		return nil, fmt.Errorf("提示词不能为空")
	}
	ctx, cancel := context.WithTimeout(parent, defaultCallTimeout)
	defer cancel()
	model, err := s.selectModel(ctx, capability, arguments.ProviderID, arguments.ModelID, arguments.Model)
	if err != nil {
		return nil, err
	}
	images, err := parseImages(arguments.Images, arguments.ImagePaths)
	if err != nil {
		return nil, err
	}
	request := domain.GenerationRequest{
		Capability:     capability,
		Prompt:         arguments.Prompt,
		NegativePrompt: strings.TrimSpace(arguments.NegativePrompt),
		Images:         images,
		Targets:        []domain.ModelTarget{{ProviderID: model.ProviderID, ModelID: model.ID}},
		Options:        domain.GenerationOptions{Size: strings.TrimSpace(arguments.Size), Width: arguments.Width, Height: arguments.Height, Count: arguments.Count},
	}
	started, err := s.manager.StartGeneration(request)
	if err != nil {
		return nil, err
	}
	assets := make([]domain.MediaAsset, 0, len(started.Tasks))
	taskIDs := make([]string, 0, len(started.Tasks))
	for _, task := range started.Tasks {
		taskIDs = append(taskIDs, task.ID)
		history, waitErr := s.waitTask(ctx, task.ID)
		if waitErr != nil {
			return nil, waitErr
		}
		if history.Status != domain.TaskStatusSucceeded || history.Result == nil {
			message := history.ErrorMessage
			if message == "" {
				message = "生成任务未成功完成"
			}
			return nil, fmt.Errorf("任务 %s：%s", task.ID, message)
		}
		assets = append(assets, history.Result.Assets...)
	}
	return s.buildGenerationResult(model, taskIDs, assets), nil
}

// selectModel 根据数据库编号、远端编号或唯一可用模型选择目标模型。
func (s *server) selectModel(ctx context.Context, capability domain.Capability, providerID, modelID int64, remoteID string) (domain.Model, error) {
	models, err := s.database.ListModels(ctx, domain.ModelFilter{Capability: capability, EnabledOnly: true, UsableOnly: true})
	if err != nil {
		return domain.Model{}, err
	}
	remoteID = strings.TrimSpace(remoteID)
	filtered := make([]domain.Model, 0, len(models))
	for _, model := range models {
		if providerID > 0 && model.ProviderID != providerID {
			continue
		}
		if modelID > 0 && model.ID != modelID {
			continue
		}
		if remoteID != "" && model.RemoteID != remoteID {
			continue
		}
		filtered = append(filtered, model)
	}
	if len(filtered) == 1 {
		return filtered[0], nil
	}
	if len(filtered) == 0 {
		return domain.Model{}, fmt.Errorf("没有找到可用的 %s 模型，请先在桌面端启用并标注模型能力", capability)
	}
	choices := make([]string, 0, len(filtered))
	for _, model := range filtered {
		choices = append(choices, fmt.Sprintf("model_id=%d (%s/%s)", model.ID, model.ProviderName, model.RemoteID))
	}
	return domain.Model{}, fmt.Errorf("可用模型不唯一，请指定 model_id；候选：%s", strings.Join(choices, "，"))
}

// waitTask 从共享历史记录轮询单个任务，确保 MCP 进程与桌面端状态一致。
func (s *server) waitTask(ctx context.Context, taskID string) (domain.GenerationHistory, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		history, err := s.database.GetHistoryByTaskID(ctx, taskID)
		if err != nil {
			return domain.GenerationHistory{}, err
		}
		if history.Status.Terminal() {
			return history, nil
		}
		select {
		case <-ctx.Done():
			return domain.GenerationHistory{}, fmt.Errorf("等待任务 %s 完成失败: %w", taskID, ctx.Err())
		case <-ticker.C:
		}
	}
}

// buildGenerationResult 将本地文件和 MCP 图片内容组合成工具结果。
func (s *server) buildGenerationResult(model domain.Model, taskIDs []string, assets []domain.MediaAsset) map[string]any {
	content := make([]map[string]any, 0, len(assets)+1)
	paths := make([]string, 0, len(assets))
	content = append(content, map[string]any{"type": "text", "text": fmt.Sprintf("已使用 %s/%s 生成 %d 张图片。", model.ProviderName, model.Name, len(assets))})
	for _, asset := range assets {
		path := filepath.Clean(strings.TrimSpace(asset.LocalPath))
		if path != "" && path != "." {
			paths = append(paths, path)
		}
		if encoded, mimeType, ok := readImageContent(path, asset.MIMEType); ok {
			content = append(content, map[string]any{"type": "image", "data": encoded, "mimeType": mimeType})
		}
	}
	content = append(content, map[string]any{"type": "text", "text": fmt.Sprintf("本地文件：%s\n任务编号：%s", strings.Join(paths, "\n"), strings.Join(taskIDs, ", "))})
	return map[string]any{"content": content, "isError": false, "structuredContent": map[string]any{"paths": paths, "taskIds": taskIDs, "modelId": model.ID, "providerId": model.ProviderID}}
}

// readImageContent 从结果文件读取 MCP image content 所需的 Base64 数据。
func readImageContent(path, declaredMIME string) (string, string, bool) {
	if path == "" || path == "." {
		return "", "", false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || len(data) > maxReturnedImageSize {
		return "", "", false
	}
	mimeType := strings.TrimSpace(declaredMIME)
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		return "", "", false
	}
	return base64.StdEncoding.EncodeToString(data), mimeType, true
}

// toolResult 将结构化数据包装为 MCP 工具调用结果。
func toolResult(value any) map[string]any {
	encoded, _ := json.Marshal(value)
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(encoded)}}, "isError": false, "structuredContent": value}
}

// toolErrorResult 将工具运行错误包装为 MCP 约定的可展示结果。
func toolErrorResult(err error) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true}
}

// parseImages 接受 MCP 常见的字符串、对象和本地文件路径图片输入格式。
func parseImages(rawImages []json.RawMessage, paths []string) ([]domain.ImageInput, error) {
	images := make([]domain.ImageInput, 0, len(rawImages)+len(paths))
	for index, raw := range rawImages {
		image, err := parseImageArgument(raw)
		if err != nil {
			return nil, fmt.Errorf("第 %d 张来源图片：%w", index+1, err)
		}
		images = append(images, image)
	}
	for index, path := range paths {
		image, err := loadLocalImage(path)
		if err != nil {
			return nil, fmt.Errorf("第 %d 个图片路径：%w", index+1, err)
		}
		images = append(images, image)
	}
	return images, nil
}

// parseImageArgument 将字符串或对象输入统一转换为领域层 ImageInput。
func parseImageArgument(raw json.RawMessage) (domain.ImageInput, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			return domain.ImageInput{}, fmt.Errorf("图片内容为空")
		}
		if strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://") {
			return domain.ImageInput{URL: text}, nil
		}
		if strings.HasPrefix(text, "data:") {
			return domain.ImageInput{Base64: text}, nil
		}
		return loadLocalImage(text)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return domain.ImageInput{}, fmt.Errorf("图片对象格式无效: %w", err)
	}
	// MCP image content 使用 data 字段，兼容是否携带 type=image 的两种对象格式。
	if data, ok := object["data"].(string); ok {
		object["base64"] = data
	}
	image := domain.ImageInput{}
	image.Name, _ = object["name"].(string)
	image.MIMEType, _ = object["mimeType"].(string)
	image.URL, _ = object["url"].(string)
	image.Base64, _ = object["base64"].(string)
	if image.URL == "" && image.Base64 == "" {
		if path, ok := object["path"].(string); ok {
			return loadLocalImage(path)
		}
	}
	if image.URL == "" && image.Base64 == "" {
		return domain.ImageInput{}, fmt.Errorf("图片对象缺少 url、base64 或 data")
	}
	if strings.HasPrefix(strings.TrimSpace(image.Base64), "data:") {
		return image, nil
	}
	if image.Base64 != "" {
		if image.MIMEType == "" {
			image.MIMEType = "image/png"
		}
		if err := validateEncodedImage(image.Base64, image.MIMEType); err != nil {
			return domain.ImageInput{}, err
		}
	}
	return image, nil
}

// loadLocalImage 将本地图片文件编码为 data URL，供各供应商适配器复用。
func loadLocalImage(path string) (domain.ImageInput, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return domain.ImageInput{}, fmt.Errorf("图片路径为空")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.ImageInput{}, fmt.Errorf("读取图片失败: %w", err)
	}
	if len(data) == 0 || len(data) > maxInputImageSize {
		return domain.ImageInput{}, fmt.Errorf("图片大小必须在 1 字节到 25 MB 之间")
	}
	mimeType := http.DetectContentType(data)
	if mimeType != "image/png" && mimeType != "image/jpeg" && mimeType != "image/webp" {
		return domain.ImageInput{}, fmt.Errorf("仅支持 PNG、JPEG 或 WebP 图片")
	}
	return domain.ImageInput{Name: filepath.Base(path), MIMEType: mimeType, Base64: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)}, nil
}

// validateEncodedImage 检查 MCP 传入的 Base64 图片大小和 MIME 类型。
func validateEncodedImage(encoded, mimeType string) error {
	if mimeType != "image/png" && mimeType != "image/jpeg" && mimeType != "image/jpg" && mimeType != "image/webp" {
		return fmt.Errorf("仅支持 PNG、JPEG 或 WebP 图片")
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(encoded))
	}
	if err != nil {
		return fmt.Errorf("图片 Base64 编码无效")
	}
	if len(data) == 0 || len(data) > maxInputImageSize {
		return fmt.Errorf("图片大小必须在 1 字节到 25 MB 之间")
	}
	return nil
}
