package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"oldwangvideo/internal/domain"
	openaiapi "oldwangvideo/internal/openai"
	"oldwangvideo/internal/store"
	taskmanager "oldwangvideo/internal/task"
)

const (
	maxSourceImageSize  = 25 << 20
	maxSourceImageCount = 16
)

type BootstrapState struct {
	Data        domain.Bootstrap      `json:"data"`
	ActiveTasks []domain.TaskSnapshot `json:"activeTasks"`
	DataDir     string                `json:"dataDir"`
}

type StudioService struct {
	app       *application.App
	dataDir   string
	outputDir string
	notifier  *notifications.NotificationService

	mu      sync.RWMutex
	ctx     context.Context
	store   *store.Store
	manager *taskmanager.Manager
}

func NewStudioService(app *application.App, dataDir, outputDir string, notifier *notifications.NotificationService) *StudioService {
	return &StudioService{app: app, dataDir: dataDir, outputDir: outputDir, notifier: notifier}
}

func (s *StudioService) ServiceName() string {
	return "StudioService"
}

func (s *StudioService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	database, err := store.Open(ctx, s.dataDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.outputDir, 0o700); err != nil {
		_ = database.Close()
		return fmt.Errorf("创建输出目录失败: %w", err)
	}
	manager := taskmanager.NewManager(ctx, database, s.outputDir, func(snapshot domain.TaskSnapshot) {
		s.app.Event.Emit("task:update", snapshot)
		s.notifyGenerationCompleted(snapshot)
	})
	s.mu.Lock()
	s.ctx = ctx
	s.store = database
	s.manager = manager
	s.mu.Unlock()
	if err := s.ensureDefaultTemplates(ctx); err != nil {
		_ = database.Close()
		return err
	}
	return nil
}

func (s *StudioService) notifyGenerationCompleted(snapshot domain.TaskSnapshot) {
	if runtime.GOOS != "windows" || s.notifier == nil {
		return
	}
	options, ok := generationNotificationOptions(snapshot)
	if !ok {
		return
	}
	if err := s.notifier.SendNotification(options); err != nil {
		log.Printf("发送生成完成通知失败: %v", err)
	}
}

// generationNotificationOptions 只为成功落盘的媒体生成任务创建系统通知。
func generationNotificationOptions(snapshot domain.TaskSnapshot) (notifications.NotificationOptions, bool) {
	if snapshot.Kind != domain.TaskKindGeneration || snapshot.Status != domain.TaskStatusSucceeded || snapshot.Result == nil {
		return notifications.NotificationOptions{}, false
	}
	mediaName := "内容"
	switch snapshot.Capability {
	case domain.CapabilityTextToImage, domain.CapabilityImageToImage:
		mediaName = "图片"
	case domain.CapabilityTextToVideo, domain.CapabilityImageToVideo:
		mediaName = "视频"
	}
	body := fmt.Sprintf("%s · %s 的%s已生成并保存到本地", snapshot.ProviderName, snapshot.ModelName, mediaName)
	return notifications.NotificationOptions{
		ID:                "generation-" + snapshot.ID,
		Title:             "老王创作台：生成完成",
		Body:              body,
		ThreadID:          "generation-completed",
		InterruptionLevel: notifications.InterruptionLevelActive,
	}, true
}

func (s *StudioService) ServiceShutdown() error {
	s.mu.Lock()
	manager := s.manager
	database := s.store
	s.manager = nil
	s.store = nil
	s.mu.Unlock()
	if manager != nil {
		manager.Shutdown()
	}
	if database != nil {
		return database.Close()
	}
	return nil
}

func (s *StudioService) GetBootstrapState() (BootstrapState, error) {
	database, manager, ctx, err := s.ready()
	if err != nil {
		return BootstrapState{}, err
	}
	data, err := database.LoadBootstrap(ctx, 24)
	if err != nil {
		return BootstrapState{}, err
	}
	return BootstrapState{Data: data, ActiveTasks: manager.Snapshots(), DataDir: s.dataDir}, nil
}

func (s *StudioService) CreateProvider(input domain.ProviderCreateInput) (domain.Provider, error) {
	database, _, ctx, err := s.ready()
	if err != nil {
		return domain.Provider{}, err
	}
	return database.CreateProvider(ctx, input)
}

func (s *StudioService) UpdateProvider(id int64, input domain.ProviderUpdateInput) (domain.Provider, error) {
	database, _, ctx, err := s.ready()
	if err != nil {
		return domain.Provider{}, err
	}
	return database.UpdateProvider(ctx, id, input)
}

func (s *StudioService) DeleteProvider(id int64) error {
	database, _, ctx, err := s.ready()
	if err != nil {
		return err
	}
	return database.DeleteProvider(ctx, id)
}

func (s *StudioService) SetProviderEnabled(id int64, enabled bool) error {
	database, _, ctx, err := s.ready()
	if err != nil {
		return err
	}
	return database.SetProviderEnabled(ctx, id, enabled)
}

func (s *StudioService) TestProvider(id int64) (openaiapi.ConnectivityResult, error) {
	database, _, serviceContext, err := s.ready()
	if err != nil {
		return openaiapi.ConnectivityResult{}, err
	}
	ctx, cancel := context.WithTimeout(serviceContext, 30*time.Second)
	defer cancel()
	var result openaiapi.ConnectivityResult
	err = database.UseProviderCredentials(ctx, id, func(provider domain.Provider, apiKey string) error {
		client, err := openaiapi.NewClientWithProxy(provider.BaseURL, apiKey, provider.ProxyURL, 30*time.Second)
		if err != nil {
			return err
		}
		result = client.TestConnection(ctx)
		if !result.Success {
			return fmt.Errorf("%s", result.Message)
		}
		return nil
	})
	return result, err
}

func (s *StudioService) RefreshProviderModels(id int64) ([]domain.Model, error) {
	database, _, serviceContext, err := s.ready()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(serviceContext, 45*time.Second)
	defer cancel()
	remoteModels := make([]domain.RemoteModel, 0)
	err = database.UseProviderCredentials(ctx, id, func(provider domain.Provider, apiKey string) error {
		client, err := openaiapi.NewClientWithProxy(provider.BaseURL, apiKey, provider.ProxyURL, 45*time.Second)
		if err != nil {
			return err
		}
		models, err := client.ListModels(ctx)
		if err != nil {
			return err
		}
		for _, model := range models {
			remoteModels = append(remoteModels, domain.RemoteModel{ID: model.ID, Name: model.ID})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return database.UpsertRemoteModels(ctx, id, remoteModels)
}

func (s *StudioService) CreateModel(input domain.ModelCreateInput) (domain.Model, error) {
	database, _, ctx, err := s.ready()
	if err != nil {
		return domain.Model{}, err
	}
	return database.CreateModel(ctx, input)
}

func (s *StudioService) UpdateModel(id int64, input domain.ModelUpdateInput) (domain.Model, error) {
	database, _, ctx, err := s.ready()
	if err != nil {
		return domain.Model{}, err
	}
	return database.UpdateModel(ctx, id, input)
}

func (s *StudioService) DeleteModel(id int64) error {
	database, _, ctx, err := s.ready()
	if err != nil {
		return err
	}
	return database.DeleteModel(ctx, id)
}

func (s *StudioService) SetModelEnabled(id int64, enabled bool) error {
	database, _, ctx, err := s.ready()
	if err != nil {
		return err
	}
	return database.SetModelEnabled(ctx, id, enabled)
}

func (s *StudioService) CreatePromptTemplate(input domain.PromptTemplateInput) (domain.PromptTemplate, error) {
	database, _, ctx, err := s.ready()
	if err != nil {
		return domain.PromptTemplate{}, err
	}
	return database.CreatePromptTemplate(ctx, input)
}

func (s *StudioService) UpdatePromptTemplate(id int64, input domain.PromptTemplateInput) (domain.PromptTemplate, error) {
	database, _, ctx, err := s.ready()
	if err != nil {
		return domain.PromptTemplate{}, err
	}
	return database.UpdatePromptTemplate(ctx, id, input)
}

func (s *StudioService) DeletePromptTemplate(id int64) error {
	database, _, ctx, err := s.ready()
	if err != nil {
		return err
	}
	return database.DeletePromptTemplate(ctx, id)
}

func (s *StudioService) StartGeneration(request domain.GenerationRequest) (domain.BatchStart, error) {
	_, manager, _, err := s.ready()
	if err != nil {
		return domain.BatchStart{}, err
	}
	return manager.StartGeneration(request)
}

func (s *StudioService) StartPolish(request domain.PromptPolishRequest) (domain.BatchStart, error) {
	_, manager, _, err := s.ready()
	if err != nil {
		return domain.BatchStart{}, err
	}
	return manager.StartPolish(request)
}

func (s *StudioService) CancelTask(taskID string) bool {
	_, manager, _, err := s.ready()
	return err == nil && manager.CancelTask(strings.TrimSpace(taskID))
}

func (s *StudioService) CancelBatch(batchID string) int {
	_, manager, _, err := s.ready()
	if err != nil {
		return 0
	}
	return manager.CancelBatch(strings.TrimSpace(batchID))
}

func (s *StudioService) RetryTask(taskID string) (domain.BatchStart, error) {
	_, manager, _, err := s.ready()
	if err != nil {
		return domain.BatchStart{}, err
	}
	return manager.RetryTask(strings.TrimSpace(taskID))
}

func (s *StudioService) ListHistory(filter domain.HistoryFilter) (domain.HistoryPage, error) {
	database, _, ctx, err := s.ready()
	if err != nil {
		return domain.HistoryPage{}, err
	}
	return database.ListHistory(ctx, filter)
}

func (s *StudioService) SetFavorite(historyID int64, favorite bool) error {
	database, _, ctx, err := s.ready()
	if err != nil {
		return err
	}
	return database.SetFavorite(ctx, historyID, favorite)
}

func (s *StudioService) DeleteHistory(historyID int64) error {
	database, _, ctx, err := s.ready()
	if err != nil {
		return err
	}
	return database.DeleteHistory(ctx, historyID)
}

func (s *StudioService) DeleteHistories(historyIDs []int64) error {
	database, _, ctx, err := s.ready()
	if err != nil {
		return err
	}
	return database.DeleteHistories(ctx, historyIDs)
}

func (s *StudioService) ChooseSourceImage() ([]domain.ImageInput, error) {
	paths, err := s.app.Dialog.OpenFile().
		AddFilter("图片文件", "*.png;*.jpg;*.jpeg;*.webp").
		PromptForMultipleSelection()
	if err != nil || len(paths) == 0 {
		return nil, err
	}
	if len(paths) > maxSourceImageCount {
		return nil, fmt.Errorf("一次最多选择 %d 张来源图片", maxSourceImageCount)
	}
	images := make([]domain.ImageInput, 0, len(paths))
	for _, path := range paths {
		image, err := readImageInput(path)
		if err != nil {
			return nil, fmt.Errorf("%s：%w", filepath.Base(path), err)
		}
		images = append(images, image)
	}
	return images, nil
}

func (s *StudioService) DownloadTaskResult(taskID string) (string, error) {
	database, _, ctx, err := s.ready()
	if err != nil {
		return "", err
	}
	history, err := database.GetHistoryByTaskID(ctx, strings.TrimSpace(taskID))
	if err != nil {
		return "", err
	}
	if history.Result == nil || len(history.Result.Assets) == 0 {
		return "", fmt.Errorf("任务没有可下载的本地结果")
	}
	asset := &history.Result.Assets[0]
	source, err := s.resolveGeneratedSource(*asset)
	if err != nil {
		return "", err
	}
	if filepath.Clean(asset.LocalPath) != filepath.Clean(source) {
		asset.LocalPath = source
		// 下载成功定位到新目录后，把迁移后的路径写回历史，后续无需重复解析。
		_, _ = database.UpdateGenerationHistory(ctx, history.TaskID, domain.HistoryStatusUpdate{Status: history.Status, Result: history.Result})
	}
	filename := filepath.Base(source)
	extension := filepath.Ext(filename)
	pattern := "*" + extension
	path, err := s.app.Dialog.SaveFile().
		SetFilename(filename).
		AddFilter("生成结果", pattern).
		CanCreateDirectories(true).
		PromptForSingleSelection()
	if err != nil || path == "" {
		return "", err
	}
	if filepath.Clean(path) == filepath.Clean(source) {
		return path, nil
	}
	if err := copyFile(source, path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *StudioService) resolveGeneratedSource(asset domain.MediaAsset) (string, error) {
	candidates := make([]string, 0, 3)
	seen := make(map[string]struct{})
	appendCandidate := func(path string) {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "." || path == "" {
			return
		}
		key := strings.ToLower(path)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, path)
	}

	appendCandidate(asset.LocalPath)
	if relative, ok := mediaRelativePath(asset.URL); ok {
		appendCandidate(filepath.Join(s.outputDir, relative))
	}
	if relative, ok := outputRelativePath(asset.LocalPath); ok {
		appendCandidate(filepath.Join(s.outputDir, relative))
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if asset.LocalPath == "" {
		return "", fmt.Errorf("生成结果文件不存在")
	}
	return "", fmt.Errorf("生成结果文件不存在，已检查旧路径和当前输出目录: %s", asset.LocalPath)
}

func mediaRelativePath(mediaURL string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(mediaURL))
	if err != nil || parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/media/") {
		return "", false
	}
	relative := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(parsed.Path, "/media/")))
	if relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return relative, true
}

func outputRelativePath(path string) (string, bool) {
	normalized := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	search := "/" + strings.TrimPrefix(normalized, "/")
	marker := "/outputs/"
	index := strings.LastIndex(strings.ToLower(search), marker)
	if index < 0 {
		return "", false
	}
	relative := filepath.Clean(filepath.FromSlash(search[index+len(marker):]))
	if relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return relative, true
}

func (s *StudioService) ready() (*store.Store, *taskmanager.Manager, context.Context, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.store == nil || s.manager == nil || s.ctx == nil {
		return nil, nil, nil, fmt.Errorf("应用服务尚未就绪")
	}
	return s.store, s.manager, s.ctx, nil
}

func (s *StudioService) ensureDefaultTemplates(ctx context.Context) error {
	templates, err := s.store.ListPromptTemplates(ctx, true)
	if err != nil {
		return err
	}
	if len(templates) > 0 {
		return nil
	}
	defaults := []domain.PromptTemplateInput{
		{Name: "写实优化", SystemPrompt: "保留用户主体与动作，补充真实环境、自然光线、镜头焦段、景深、材质纹理和色彩关系，只输出一段可直接用于写实图像或视频生成的中文提示词。", Enabled: true},
		{Name: "动漫风格", SystemPrompt: "保留用户叙事意图，将内容优化为高质量动漫分镜提示词，补充角色外观、场景层次、动态构图、光影、色彩与镜头语言，只输出润色后的提示词。", Enabled: true},
	}
	for _, input := range defaults {
		if _, err := s.store.CreatePromptTemplate(ctx, input); err != nil {
			return err
		}
	}
	return nil
}

func readImageInput(path string) (domain.ImageInput, error) {
	file, err := os.Open(path)
	if err != nil {
		return domain.ImageInput{}, fmt.Errorf("打开图片失败: %w", err)
	}
	defer file.Close()
	limited := io.LimitReader(file, maxSourceImageSize+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return domain.ImageInput{}, fmt.Errorf("读取图片失败: %w", err)
	}
	if len(data) > maxSourceImageSize {
		return domain.ImageInput{}, fmt.Errorf("图片不能超过 25 MB")
	}
	mimeType := http.DetectContentType(data)
	if mimeType != "image/png" && mimeType != "image/jpeg" && mimeType != "image/webp" {
		return domain.ImageInput{}, fmt.Errorf("仅支持 PNG、JPEG 或 WebP 图片")
	}
	return domain.ImageInput{Name: filepath.Base(path), MIMEType: mimeType, Base64: base64.StdEncoding.EncodeToString(data)}, nil
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("打开生成结果失败: %w", err)
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("创建下载目录失败: %w", err)
	}
	output, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("创建下载文件失败: %w", err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return fmt.Errorf("写入下载文件失败: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("关闭下载文件失败: %w", closeErr)
	}
	return nil
}
