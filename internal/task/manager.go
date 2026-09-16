package task

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"oldwangvideo/internal/domain"
	openaiapi "oldwangvideo/internal/openai"
	"oldwangvideo/internal/store"
)

const (
	defaultPolishPrompt    = "你是一名专业的视觉创作提示词编辑。保留用户意图，补充主体、环境、构图、光线、材质、镜头与风格细节，只输出可直接用于生成模型的提示词。"
	maxSourceImageBytes    = 25 << 20
	maxSourceImageCount    = 16
	maxConcurrentTasks     = 8
	maxRetainedTaskBatches = 8
	maxGenerationCount     = 8
	maxGenerationTasks     = 24
)

type Emitter func(domain.TaskSnapshot)

type taskEntry struct {
	snapshot   domain.TaskSnapshot
	generation *domain.GenerationRequest
	polish     *domain.PromptPolishRequest
	target     domain.ModelTarget
	cancel     context.CancelFunc
	canceling  bool
	completing bool
}

type validatedTarget struct {
	target   domain.ModelTarget
	provider domain.Provider
	model    domain.Model
}

type Manager struct {
	ctx       context.Context
	cancelAll context.CancelFunc
	store     *store.Store
	outputDir string
	emit      Emitter

	mu       sync.RWMutex
	tasks    map[string]*taskEntry
	revision int64

	lifecycle sync.Mutex
	closed    bool
	wg        sync.WaitGroup
	slots     chan struct{}
}

func NewManager(ctx context.Context, database *store.Store, outputDir string, emitter Emitter) *Manager {
	managerContext, cancel := context.WithCancel(ctx)
	return &Manager{
		ctx:       managerContext,
		cancelAll: cancel,
		store:     database,
		outputDir: outputDir,
		emit:      emitter,
		tasks:     make(map[string]*taskEntry),
		slots:     make(chan struct{}, maxConcurrentTasks),
	}
}

// Shutdown 先拒绝新任务，再取消并等待所有执行协程退出。
func (m *Manager) Shutdown() {
	m.lifecycle.Lock()
	if m.closed {
		m.lifecycle.Unlock()
		m.wg.Wait()
		return
	}
	m.closed = true
	m.lifecycle.Unlock()
	m.CancelAll()
	if m.cancelAll != nil {
		m.cancelAll()
	}
	m.wg.Wait()
}

func (m *Manager) StartGeneration(request domain.GenerationRequest) (domain.BatchStart, error) {
	m.lifecycle.Lock()
	defer m.lifecycle.Unlock()
	if m.closed {
		return domain.BatchStart{}, fmt.Errorf("任务管理器正在关闭")
	}
	if !request.Capability.IsGeneration() {
		return domain.BatchStart{}, fmt.Errorf("生成能力无效")
	}
	request.Prompt = strings.TrimSpace(request.Prompt)
	if request.Prompt == "" {
		return domain.BatchStart{}, fmt.Errorf("提示词不能为空")
	}
	if len(request.Targets) == 0 {
		return domain.BatchStart{}, fmt.Errorf("请至少选择一个供应商模型")
	}
	if len(request.Targets) > 12 {
		return domain.BatchStart{}, fmt.Errorf("一次最多并发 12 个模型")
	}
	count := request.Options.Count
	if count <= 0 {
		count = 1
	}
	if count > maxGenerationCount {
		return domain.BatchStart{}, fmt.Errorf("每个模型一次最多生成 %d 个结果", maxGenerationCount)
	}
	request.Options.Count = count
	if (request.Capability == domain.CapabilityImageToImage || request.Capability == domain.CapabilityImageToVideo) && len(request.Images) == 0 {
		return domain.BatchStart{}, fmt.Errorf("当前模块需要来源图片")
	}
	if err := validateGenerationImages(request); err != nil {
		return domain.BatchStart{}, err
	}
	validated, err := m.validateTargets(request.Targets, request.Capability)
	if err != nil {
		return domain.BatchStart{}, err
	}
	totalTasks := len(validated) * count
	if totalTasks > maxGenerationTasks {
		return domain.BatchStart{}, fmt.Errorf("单批最多创建 %d 个生成任务，当前为 %d 个", maxGenerationTasks, totalTasks)
	}
	batchID := uuid.NewString()
	type preparedTask struct {
		snapshot domain.TaskSnapshot
		target   validatedTarget
	}
	prepared := make([]preparedTask, 0, totalTasks)
	historyIDs := make([]int64, 0, totalTasks)
	historyRequest := sanitizeHistoryRequest(request)
	for _, item := range validated {
		for range count {
			taskID := uuid.NewString()
			now := time.Now().UTC()
			snapshot := domain.TaskSnapshot{
				ID:           taskID,
				BatchID:      batchID,
				Kind:         domain.TaskKindGeneration,
				Capability:   request.Capability,
				ProviderID:   item.provider.ID,
				ProviderName: item.provider.Name,
				ModelID:      item.model.ID,
				ModelName:    item.model.Name,
				Status:       domain.TaskStatusQueued,
				Stage:        "等待执行",
				Prompt:       request.Prompt,
				CreatedAt:    now,
				UpdatedAt:    now,
			}
			history, err := m.store.CreateGenerationRecord(m.ctx, domain.HistoryCreateInput{
				TaskID:       taskID,
				BatchID:      batchID,
				ProviderID:   item.provider.ID,
				ModelID:      item.model.ID,
				ProviderName: item.provider.Name,
				ModelName:    item.model.Name,
				Capability:   request.Capability,
				Request:      historyRequest,
				Status:       domain.TaskStatusQueued,
				CreatedAt:    now,
			})
			if err != nil {
				m.rollbackHistory(historyIDs)
				return domain.BatchStart{}, fmt.Errorf("创建生成历史失败: %w", err)
			}
			historyIDs = append(historyIDs, history.ID)
			prepared = append(prepared, preparedTask{snapshot: snapshot, target: item})
		}
	}

	started := domain.BatchStart{BatchID: batchID, Tasks: make([]domain.TaskSnapshot, 0, len(prepared))}
	type launchTask struct {
		context context.Context
		entry   *taskEntry
		target  validatedTarget
	}
	launches := make([]launchTask, 0, len(prepared))
	for _, item := range prepared {
		taskContext, cancel := context.WithCancel(m.ctx)
		requestCopy := request
		requestCopy.Options.Count = 1
		entry := &taskEntry{snapshot: item.snapshot, generation: &requestCopy, target: item.target.target, cancel: cancel}
		m.addEntry(entry)
		started.Tasks = append(started.Tasks, m.snapshot(item.snapshot.ID))
		launches = append(launches, launchTask{context: taskContext, entry: entry, target: item.target})
	}
	for _, launch := range launches {
		m.wg.Add(1)
		go func(item launchTask) {
			defer m.wg.Done()
			defer item.entry.cancel()
			m.runGeneration(item.context, item.entry.snapshot.ID, item.target, *item.entry.generation)
		}(launch)
	}
	return started, nil
}

func (m *Manager) StartPolish(request domain.PromptPolishRequest) (domain.BatchStart, error) {
	m.lifecycle.Lock()
	defer m.lifecycle.Unlock()
	if m.closed {
		return domain.BatchStart{}, fmt.Errorf("任务管理器正在关闭")
	}
	request.Prompt = strings.TrimSpace(request.Prompt)
	if request.Prompt == "" {
		return domain.BatchStart{}, fmt.Errorf("待润色提示词不能为空")
	}
	if len(request.Targets) == 0 {
		return domain.BatchStart{}, fmt.Errorf("请至少选择一个润色模型")
	}
	if len(request.Targets) > 12 {
		return domain.BatchStart{}, fmt.Errorf("一次最多并发 12 个润色模型")
	}
	if request.TemplateID > 0 {
		template, err := m.store.GetPromptTemplate(m.ctx, request.TemplateID)
		if err != nil {
			return domain.BatchStart{}, fmt.Errorf("读取润色模板失败: %w", err)
		}
		request.SystemPrompt = template.SystemPrompt
	}
	if strings.TrimSpace(request.SystemPrompt) == "" {
		request.SystemPrompt = defaultPolishPrompt
	}
	validated, err := m.validateTargets(request.Targets, domain.CapabilityPromptPolish)
	if err != nil {
		return domain.BatchStart{}, err
	}
	batchID := uuid.NewString()
	started := domain.BatchStart{BatchID: batchID, Tasks: make([]domain.TaskSnapshot, 0, len(validated))}
	launches := make([]struct {
		context context.Context
		entry   *taskEntry
		target  validatedTarget
	}, 0, len(validated))
	for _, item := range validated {
		taskID := uuid.NewString()
		now := time.Now().UTC()
		snapshot := domain.TaskSnapshot{
			ID:           taskID,
			BatchID:      batchID,
			Kind:         domain.TaskKindPromptPolish,
			Capability:   domain.CapabilityPromptPolish,
			ProviderID:   item.provider.ID,
			ProviderName: item.provider.Name,
			ModelID:      item.model.ID,
			ModelName:    item.model.Name,
			Status:       domain.TaskStatusQueued,
			Stage:        "等待润色",
			Prompt:       request.Prompt,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		taskContext, cancel := context.WithCancel(m.ctx)
		requestCopy := request
		entry := &taskEntry{snapshot: snapshot, polish: &requestCopy, target: item.target, cancel: cancel}
		m.addEntry(entry)
		started.Tasks = append(started.Tasks, m.snapshot(taskID))
		launches = append(launches, struct {
			context context.Context
			entry   *taskEntry
			target  validatedTarget
		}{context: taskContext, entry: entry, target: item})
	}
	for _, launch := range launches {
		m.wg.Add(1)
		go func(item struct {
			context context.Context
			entry   *taskEntry
			target  validatedTarget
		}) {
			defer m.wg.Done()
			defer item.entry.cancel()
			m.runPolish(item.context, item.entry.snapshot.ID, item.target, *item.entry.polish)
		}(launch)
	}
	return started, nil
}

func (m *Manager) RetryTask(taskID string) (domain.BatchStart, error) {
	m.mu.RLock()
	entry, ok := m.tasks[taskID]
	if !ok {
		m.mu.RUnlock()
		return domain.BatchStart{}, fmt.Errorf("任务不存在")
	}
	target := entry.target
	var generation *domain.GenerationRequest
	var polish *domain.PromptPolishRequest
	if entry.generation != nil {
		copyRequest := *entry.generation
		generation = &copyRequest
	}
	if entry.polish != nil {
		copyRequest := *entry.polish
		polish = &copyRequest
	}
	m.mu.RUnlock()
	if generation != nil {
		generation.Targets = []domain.ModelTarget{target}
		return m.StartGeneration(*generation)
	}
	if polish != nil {
		polish.Targets = []domain.ModelTarget{target}
		return m.StartPolish(*polish)
	}
	return domain.BatchStart{}, fmt.Errorf("任务缺少可重试的请求")
}

func (m *Manager) CancelTask(taskID string) bool {
	m.mu.Lock()
	entry, ok := m.tasks[taskID]
	if !ok || entry.snapshot.Status.Terminal() || entry.completing {
		m.mu.Unlock()
		return false
	}
	entry.canceling = true
	entry.snapshot.Stage = "正在取消"
	entry.snapshot.CancelMode = "local_only"
	entry.snapshot.UpdatedAt = time.Now().UTC()
	entry.snapshot.Revision = m.nextRevisionLocked()
	cancel := entry.cancel
	snapshot := entry.snapshot
	m.mu.Unlock()
	m.emitSnapshot(snapshot)
	if cancel != nil {
		cancel()
	}
	return true
}

func (m *Manager) CancelBatch(batchID string) int {
	m.mu.RLock()
	ids := make([]string, 0)
	for id, entry := range m.tasks {
		if entry.snapshot.BatchID == batchID && !entry.snapshot.Status.Terminal() {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()
	canceled := 0
	for _, id := range ids {
		if m.CancelTask(id) {
			canceled++
		}
	}
	return canceled
}

func (m *Manager) CancelAll() int {
	m.mu.RLock()
	ids := make([]string, 0, len(m.tasks))
	for id, entry := range m.tasks {
		if !entry.snapshot.Status.Terminal() {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()
	canceled := 0
	for _, id := range ids {
		if m.CancelTask(id) {
			canceled++
		}
	}
	return canceled
}

func (m *Manager) Snapshots() []domain.TaskSnapshot {
	m.mu.RLock()
	result := make([]domain.TaskSnapshot, 0, len(m.tasks))
	for _, entry := range m.tasks {
		if entry.snapshot.Status.Terminal() {
			continue
		}
		result = append(result, entry.snapshot)
	}
	m.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result
}

func (m *Manager) runGeneration(ctx context.Context, taskID string, target validatedTarget, request domain.GenerationRequest) {
	defer func() {
		if recovered := recover(); recovered != nil {
			m.failTask(taskID, "panic_recovered", fmt.Sprintf("任务执行异常：%v", recovered), false)
		}
	}()
	if err := m.acquireSlot(ctx); err != nil {
		m.finishWithError(taskID, err)
		return
	}
	defer m.releaseSlot()
	executionContext, timeoutCancel := context.WithTimeout(ctx, generationTimeout(request))
	defer timeoutCancel()
	ctx = executionContext
	if err := m.markRunning(taskID, "正在连接供应商"); err != nil {
		m.failTask(taskID, "history_update_failed", fmt.Sprintf("写入任务状态失败：%v", err), true)
		return
	}
	var media openaiapi.MediaResult
	var client *openaiapi.Client
	err := m.store.UseProviderCredentials(ctx, target.provider.ID, func(provider domain.Provider, apiKey string) error {
		var createErr error
		client, createErr = openaiapi.NewClientWithProxy(provider.BaseURL, apiKey, provider.ProxyURL, generationTimeout(request))
		if createErr != nil {
			return createErr
		}
		media, createErr = client.Generate(ctx, mapGenerateRequest(request, target.model.RemoteID), func(progress openaiapi.Progress) {
			m.updateProgress(taskID, progress)
		})
		return createErr
	})
	if err != nil {
		m.finishWithError(taskID, err)
		return
	}
	if err := ctx.Err(); err != nil {
		m.finishWithError(taskID, err)
		return
	}
	m.updateStage(taskID, "正在保存结果", 96)
	asset, err := m.saveMedia(ctx, client, taskID, media, request.Capability)
	if err != nil {
		m.finishWithError(taskID, err)
		return
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(asset.LocalPath)
		m.finishWithError(taskID, err)
		return
	}
	result := domain.GenerationResult{Assets: []domain.MediaAsset{asset}}
	m.succeedGeneration(taskID, result)
}

func (m *Manager) runPolish(ctx context.Context, taskID string, target validatedTarget, request domain.PromptPolishRequest) {
	defer func() {
		if recovered := recover(); recovered != nil {
			m.failTask(taskID, "panic_recovered", fmt.Sprintf("润色任务异常：%v", recovered), false)
		}
	}()
	if err := m.acquireSlot(ctx); err != nil {
		m.finishWithError(taskID, err)
		return
	}
	defer m.releaseSlot()
	executionContext, timeoutCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer timeoutCancel()
	ctx = executionContext
	if err := m.markRunning(taskID, "正在润色提示词"); err != nil {
		m.failTask(taskID, "history_update_failed", fmt.Sprintf("写入任务状态失败：%v", err), true)
		return
	}
	var text string
	err := m.store.UseProviderCredentials(ctx, target.provider.ID, func(provider domain.Provider, apiKey string) error {
		client, err := openaiapi.NewClientWithProxy(provider.BaseURL, apiKey, provider.ProxyURL, 5*time.Minute)
		if err != nil {
			return err
		}
		text, err = client.Polish(ctx, target.model.RemoteID, request.SystemPrompt, request.Prompt)
		return err
	})
	if err != nil {
		m.finishWithError(taskID, err)
		return
	}
	if err := ctx.Err(); err != nil {
		m.finishWithError(taskID, err)
		return
	}
	m.succeedPolish(taskID, text)
}

func (m *Manager) acquireSlot(ctx context.Context) error {
	select {
	case m.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) releaseSlot() {
	<-m.slots
}

func (m *Manager) validateTargets(targets []domain.ModelTarget, capability domain.Capability) ([]validatedTarget, error) {
	seen := make(map[int64]struct{}, len(targets))
	result := make([]validatedTarget, 0, len(targets))
	for _, target := range targets {
		if _, exists := seen[target.ModelID]; exists {
			continue
		}
		model, err := m.store.GetModel(m.ctx, target.ModelID)
		if err != nil {
			return nil, fmt.Errorf("读取模型失败: %w", err)
		}
		if model.ProviderID != target.ProviderID {
			return nil, fmt.Errorf("模型 %s 不属于所选供应商", model.Name)
		}
		provider, err := m.store.GetProvider(m.ctx, target.ProviderID)
		if err != nil {
			return nil, fmt.Errorf("读取供应商失败: %w", err)
		}
		if !provider.Enabled || !model.Enabled {
			return nil, fmt.Errorf("供应商或模型 %s 已停用", model.Name)
		}
		if !supports(model.Capabilities, capability) {
			return nil, fmt.Errorf("模型 %s 未标注当前能力", model.Name)
		}
		seen[target.ModelID] = struct{}{}
		result = append(result, validatedTarget{target: target, provider: provider, model: model})
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("没有可执行的模型")
	}
	return result, nil
}

func supports(capabilities []domain.Capability, expected domain.Capability) bool {
	for _, capability := range capabilities {
		if capability == expected {
			return true
		}
	}
	return false
}

func validateGenerationImages(request domain.GenerationRequest) error {
	if request.Capability != domain.CapabilityImageToImage && request.Capability != domain.CapabilityImageToVideo {
		return nil
	}
	if request.Capability == domain.CapabilityImageToVideo && len(request.Images) != 1 {
		return fmt.Errorf("图生视频只支持一张来源图片")
	}
	if request.Capability == domain.CapabilityImageToImage && (len(request.Images) == 0 || len(request.Images) > maxSourceImageCount) {
		return fmt.Errorf("图生图需要 1 到 %d 张来源图片", maxSourceImageCount)
	}
	for index, image := range request.Images {
		if err := validateGenerationImage(image); err != nil {
			return fmt.Errorf("第 %d 张来源图片：%w", index+1, err)
		}
	}
	return nil
}

func validateGenerationImage(image domain.ImageInput) error {
	if strings.TrimSpace(image.Base64) == "" && strings.TrimSpace(image.URL) == "" {
		return fmt.Errorf("内容为空")
	}
	if image.Base64 != "" {
		encoded := strings.TrimSpace(image.Base64)
		mimeType := strings.ToLower(strings.TrimSpace(image.MIMEType))
		if strings.HasPrefix(encoded, "data:") {
			comma := strings.IndexByte(encoded, ',')
			if comma < 0 {
				return fmt.Errorf("数据格式无效")
			}
			header := encoded[:comma]
			encoded = encoded[comma+1:]
			mimeType = strings.ToLower(strings.TrimPrefix(strings.Split(header, ";")[0], "data:"))
		}
		if !supportedImageMIME(mimeType) {
			return fmt.Errorf("仅支持 PNG、JPEG 或 WebP 图片")
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			data, err = base64.RawStdEncoding.DecodeString(encoded)
		}
		if err != nil {
			return fmt.Errorf("编码无效")
		}
		if len(data) == 0 || len(data) > maxSourceImageBytes {
			return fmt.Errorf("图片不能超过 25 MB")
		}
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(image.URL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("URL 必须是 http 或 https 地址")
	}
	if image.MIMEType != "" && !supportedImageMIME(strings.ToLower(strings.TrimSpace(image.MIMEType))) {
		return fmt.Errorf("仅支持 PNG、JPEG 或 WebP 图片")
	}
	return nil
}

func supportedImageMIME(value string) bool {
	switch value {
	case "image/png", "image/jpeg", "image/jpg", "image/webp":
		return true
	default:
		return false
	}
}

func (m *Manager) rollbackHistory(ids []int64) {
	if len(ids) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for index := len(ids) - 1; index >= 0; index-- {
		_ = m.store.DeleteHistory(ctx, ids[index])
	}
}

func mapGenerateRequest(request domain.GenerationRequest, model string) openaiapi.GenerateRequest {
	size := request.Options.Size
	if size == "" && request.Options.Width > 0 && request.Options.Height > 0 {
		size = fmt.Sprintf("%dx%d", request.Options.Width, request.Options.Height)
	}
	mapped := openaiapi.GenerateRequest{
		Capability:     openaiapi.Capability(request.Capability),
		Model:          model,
		Prompt:         request.Prompt,
		NegativePrompt: request.NegativePrompt,
		Size:           size,
		Seconds:        request.Options.DurationSeconds,
		Count:          request.Options.Count,
	}
	for _, image := range request.Images {
		data := image.Base64
		if data != "" && !strings.HasPrefix(data, "data:") {
			mimeType := image.MIMEType
			if mimeType == "" {
				mimeType = "image/png"
			}
			data = "data:" + mimeType + ";base64," + data
		}
		if data == "" {
			data = image.URL
		}
		mapped.SourceImages = append(mapped.SourceImages, data)
	}
	if len(mapped.SourceImages) > 0 {
		// 保留单图字段，兼容现有图生视频适配器。
		mapped.SourceImageData = mapped.SourceImages[0]
	}
	return mapped
}

func (m *Manager) saveMedia(ctx context.Context, client *openaiapi.Client, taskID string, media openaiapi.MediaResult, capability domain.Capability) (domain.MediaAsset, error) {
	m.mu.RLock()
	entry := m.tasks[taskID]
	batchID := entry.snapshot.BatchID
	m.mu.RUnlock()
	relativeDir := filepath.Join(time.Now().UTC().Format("2006-01-02"), batchID)
	temporaryRelative := filepath.Join(relativeDir, taskID+".download")
	temporary := filepath.Join(m.outputDir, temporaryRelative)
	mimeType, err := client.DownloadTo(ctx, media, temporary)
	if err != nil {
		return domain.MediaAsset{}, err
	}
	extension := mediaExtension(mimeType, capability)
	if capability == domain.CapabilityTextToVideo || capability == domain.CapabilityImageToVideo {
		extension = mediaExtension(mimeType, capability)
	}
	relative := filepath.Join(relativeDir, taskID+extension)
	destination := filepath.Join(m.outputDir, relative)
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return domain.MediaAsset{}, fmt.Errorf("保存媒体文件失败: %w", err)
	}
	mediaType := domain.MediaTypeImage
	if capability == domain.CapabilityTextToVideo || capability == domain.CapabilityImageToVideo {
		mediaType = domain.MediaTypeVideo
	}
	return domain.MediaAsset{
		Type:      mediaType,
		URL:       "/media/" + filepath.ToSlash(relative),
		LocalPath: destination,
		MIMEType:  mimeType,
		Metadata: map[string]any{
			"remoteId":          media.RemoteID,
			"providerExpiresAt": media.ProviderExpiresAt,
		},
	}, nil
}

func mediaExtension(mimeType string, capability domain.Capability) string {
	if capability == domain.CapabilityTextToVideo || capability == domain.CapabilityImageToVideo {
		return ".mp4"
	}
	if extensions, _ := mime.ExtensionsByType(mimeType); len(extensions) > 0 {
		for _, extension := range extensions {
			switch extension {
			case ".png", ".jpg", ".jpeg", ".webp":
				return extension
			}
		}
	}
	return ".png"
}

func generationTimeout(request domain.GenerationRequest) time.Duration {
	if seconds, ok := request.Options.Extra["timeoutSeconds"]; ok {
		switch value := seconds.(type) {
		case float64:
			if value >= 10 && value <= 7200 {
				return time.Duration(value) * time.Second
			}
		case int:
			if value >= 10 && value <= 7200 {
				return time.Duration(value) * time.Second
			}
		}
	}
	if request.Capability == domain.CapabilityTextToVideo || request.Capability == domain.CapabilityImageToVideo {
		return 45 * time.Minute
	}
	return 10 * time.Minute
}

func sanitizeHistoryRequest(request domain.GenerationRequest) domain.GenerationRequest {
	result := request
	result.Images = make([]domain.ImageInput, len(request.Images))
	for index, image := range request.Images {
		result.Images[index] = domain.ImageInput{Name: image.Name, MIMEType: image.MIMEType, URL: image.URL}
	}
	return result
}

func (m *Manager) addEntry(entry *taskEntry) {
	m.mu.Lock()
	entry.snapshot.Revision = m.nextRevisionLocked()
	m.tasks[entry.snapshot.ID] = entry
	snapshot := entry.snapshot
	m.mu.Unlock()
	m.emitSnapshot(snapshot)
}

func (m *Manager) snapshot(taskID string) domain.TaskSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tasks[taskID].snapshot
}

func (m *Manager) markRunning(taskID, stage string) error {
	now := time.Now().UTC()
	if err := m.persistHistory(taskID, domain.HistoryStatusUpdate{Status: domain.TaskStatusRunning, StartedAt: &now}); err != nil {
		return err
	}
	m.updateTask(taskID, func(snapshot *domain.TaskSnapshot) {
		snapshot.Status = domain.TaskStatusRunning
		snapshot.Stage = stage
		snapshot.Progress = 1
		snapshot.StartedAt = &now
	})
	return nil
}

func (m *Manager) updateProgress(taskID string, progress openaiapi.Progress) {
	m.updateTask(taskID, func(snapshot *domain.TaskSnapshot) {
		snapshot.Stage = progress.Stage
		if progress.Progress > snapshot.Progress {
			snapshot.Progress = min(progress.Progress, 95)
		}
	})
}

func (m *Manager) updateStage(taskID, stage string, progress int) {
	m.updateTask(taskID, func(snapshot *domain.TaskSnapshot) {
		snapshot.Stage = stage
		snapshot.Progress = progress
	})
}

func (m *Manager) succeedGeneration(taskID string, result domain.GenerationResult) {
	m.mu.Lock()
	entry := m.tasks[taskID]
	if entry == nil {
		m.mu.Unlock()
		return
	}
	if entry.canceling {
		m.mu.Unlock()
		for _, asset := range result.Assets {
			if asset.LocalPath != "" {
				_ = os.Remove(asset.LocalPath)
			}
		}
		m.cancelTaskFinal(taskID, "cancelled_local", "任务已在本地取消，远端任务可能仍在运行")
		return
	}
	entry.completing = true
	m.mu.Unlock()
	now := time.Now().UTC()
	if err := m.persistHistory(taskID, domain.HistoryStatusUpdate{Status: domain.TaskStatusSucceeded, Result: &result, CompletedAt: &now}); err != nil {
		m.mu.Lock()
		if current := m.tasks[taskID]; current != nil {
			current.completing = false
		}
		m.mu.Unlock()
		m.failTask(taskID, "history_update_failed", fmt.Sprintf("结果已生成，但写入历史失败：%v", err), true)
		return
	}
	m.updateTask(taskID, func(snapshot *domain.TaskSnapshot) {
		snapshot.Status = domain.TaskStatusSucceeded
		snapshot.Stage = "生成完成"
		snapshot.Progress = 100
		snapshot.Result = &result
		snapshot.CompletedAt = &now
	})
}

func (m *Manager) succeedPolish(taskID, text string) {
	m.mu.Lock()
	entry := m.tasks[taskID]
	if entry == nil {
		m.mu.Unlock()
		return
	}
	if entry.canceling {
		m.mu.Unlock()
		m.cancelTaskFinal(taskID, "cancelled_local", "润色任务已取消")
		return
	}
	entry.completing = true
	m.mu.Unlock()
	now := time.Now().UTC()
	result := &domain.PromptPolishResult{Text: text}
	m.updateTask(taskID, func(snapshot *domain.TaskSnapshot) {
		snapshot.Status = domain.TaskStatusSucceeded
		snapshot.Stage = "润色完成"
		snapshot.Progress = 100
		snapshot.PolishResult = result
		snapshot.CompletedAt = &now
	})
}

func (m *Manager) finishWithError(taskID string, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		code := "cancelled_local"
		message := "任务已在本地取消，远端任务可能仍在运行"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "timeout"
			message = "任务执行超时"
		}
		m.cancelTaskFinal(taskID, code, message)
		return
	}
	var apiError *openaiapi.APIError
	if errors.As(err, &apiError) {
		if apiError.Code == "cancelled_local" {
			m.cancelTaskFinal(taskID, apiError.Code, apiError.Message)
			return
		}
		m.failTask(taskID, apiError.Code, apiError.Error(), apiError.Retryable)
		return
	}
	m.failTask(taskID, "task_failed", err.Error(), false)
}

func (m *Manager) cancelTaskFinal(taskID, code, message string) {
	now := time.Now().UTC()
	if err := m.persistHistory(taskID, domain.HistoryStatusUpdate{Status: domain.TaskStatusCanceled, ErrorMessage: &message, CompletedAt: &now}); err != nil {
		message = message + "；写入历史失败：" + err.Error()
	}
	m.updateTask(taskID, func(snapshot *domain.TaskSnapshot) {
		snapshot.Status = domain.TaskStatusCanceled
		snapshot.Stage = "已取消"
		snapshot.ErrorCode = code
		snapshot.ErrorMessage = message
		snapshot.CancelMode = "local_only"
		snapshot.CompletedAt = &now
	})
}

func (m *Manager) failTask(taskID, code, message string, retryable bool) {
	now := time.Now().UTC()
	if err := m.persistHistory(taskID, domain.HistoryStatusUpdate{Status: domain.TaskStatusFailed, ErrorMessage: &message, CompletedAt: &now}); err != nil {
		message = message + "；写入历史失败：" + err.Error()
	}
	m.updateTask(taskID, func(snapshot *domain.TaskSnapshot) {
		snapshot.Status = domain.TaskStatusFailed
		snapshot.Stage = "执行失败"
		snapshot.ErrorCode = code
		snapshot.ErrorMessage = message
		snapshot.Retryable = retryable
		snapshot.CompletedAt = &now
	})
}

func (m *Manager) updateTask(taskID string, mutate func(*domain.TaskSnapshot)) {
	m.mu.Lock()
	entry, ok := m.tasks[taskID]
	if !ok {
		m.mu.Unlock()
		return
	}
	mutate(&entry.snapshot)
	entry.snapshot.UpdatedAt = time.Now().UTC()
	entry.snapshot.Revision = m.nextRevisionLocked()
	snapshot := entry.snapshot
	m.mu.Unlock()
	m.emitSnapshot(snapshot)
	if snapshot.Status.Terminal() {
		m.pruneTerminalBatches()
	}
}

// pruneTerminalBatches 限制重试请求和来源图片在内存中的保留批次数。
func (m *Manager) pruneTerminalBatches() {
	type batchState struct {
		id      string
		created time.Time
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	batches := make(map[string]batchState)
	active := make(map[string]bool)
	for _, entry := range m.tasks {
		batchID := entry.snapshot.BatchID
		if entry.snapshot.Status.Terminal() {
			state := batches[batchID]
			if state.id == "" || entry.snapshot.CreatedAt.Before(state.created) {
				batches[batchID] = batchState{id: batchID, created: entry.snapshot.CreatedAt}
			}
		} else {
			active[batchID] = true
		}
	}
	if len(batches) <= maxRetainedTaskBatches {
		return
	}
	ordered := make([]batchState, 0, len(batches))
	for _, state := range batches {
		if !active[state.id] {
			ordered = append(ordered, state)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].created.Before(ordered[j].created) })
	removeCount := len(batches) - maxRetainedTaskBatches
	for _, state := range ordered {
		if removeCount <= 0 {
			break
		}
		for taskID, entry := range m.tasks {
			if entry.snapshot.BatchID == state.id && entry.snapshot.Status.Terminal() {
				delete(m.tasks, taskID)
			}
		}
		removeCount--
	}
}

func (m *Manager) nextRevisionLocked() int64 {
	m.revision++
	return m.revision
}

func (m *Manager) emitSnapshot(snapshot domain.TaskSnapshot) {
	if m.emit != nil {
		m.emit(snapshot)
	}
}

func (m *Manager) updateHistory(taskID string, update domain.HistoryStatusUpdate) (domain.GenerationHistory, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.store.UpdateGenerationRecord(ctx, taskID, update)
}

func (m *Manager) persistHistory(taskID string, update domain.HistoryStatusUpdate) error {
	m.mu.RLock()
	entry := m.tasks[taskID]
	isGeneration := entry != nil && entry.generation != nil
	m.mu.RUnlock()
	if !isGeneration {
		return nil
	}
	_, err := m.updateHistory(taskID, update)
	return err
}
