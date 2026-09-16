package main

import (
	"embed"
	"log"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"oldwangvideo/internal/domain"
)

//go:embed all:frontend/dist
var assets embed.FS

func init() {
	application.RegisterEvent[domain.TaskSnapshot]("task:update")
	application.RegisterEvent[[]domain.ImageInput]("source-image:dropped")
	application.RegisterEvent[string]("source-image:drop-error")
}

func main() {
	dataDir, outputDir, err := applicationDirectories()
	if err != nil {
		log.Fatal(err)
	}
	frontend := application.AssetFileServerFS(assets)
	notificationService := notifications.New()
	app := application.New(application.Options{
		Name:        "老王创作台",
		Description: "多供应商 AI 图片与视频生成工作台",
		Services: []application.Service{
			application.NewService(notificationService),
		},
		Assets: application.AssetOptions{
			Handler: &mediaHandler{root: outputDir, fallback: frontend},
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})
	service := NewStudioService(app, dataDir, outputDir, notificationService)
	app.RegisterService(application.NewService(service))
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "老王创作台",
		Width:            1440,
		Height:           900,
		MinWidth:         720,
		MinHeight:        600,
		URL:              "/",
		EnableFileDrop:   true,
		BackgroundColour: application.NewRGB(243, 246, 247),
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 40,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
	})
	window.OnWindowEvent(events.Common.WindowFilesDropped, func(event *application.WindowEvent) {
		details := event.Context().DropTargetDetails()
		if details == nil || details.ElementID != "source-image-drop" {
			return
		}
		files := event.Context().DroppedFiles()
		if len(files) == 0 {
			return
		}
		go func() {
			if len(files) > maxSourceImageCount {
				app.Event.Emit("source-image:drop-error", "一次最多拖入 16 张来源图片")
				files = files[:maxSourceImageCount]
			}
			images := make([]domain.ImageInput, 0, len(files))
			for _, path := range files {
				image, err := readImageInput(path)
				if err != nil {
					app.Event.Emit("source-image:drop-error", filepath.Base(path)+"："+err.Error())
					continue
				}
				images = append(images, image)
			}
			if len(images) > 0 {
				app.Event.Emit("source-image:dropped", images)
			}
		}()
	})
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func applicationDirectories() (string, string, error) {
	executablePath, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	dataDir := resolveApplicationRoot(executablePath)
	outputDir := filepath.Join(dataDir, "outputs")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return "", "", err
	}
	return dataDir, outputDir, nil
}

// resolveApplicationRoot 发布构建或 EXE 目录已有运行数据时使用 EXE 同目录。
func resolveApplicationRoot(executablePath string) string {
	current := filepath.Clean(filepath.Dir(executablePath))
	if productionBuild || hasRuntimeData(current) {
		return current
	}
	fallback := current
	for range 5 {
		if info, err := os.Stat(filepath.Join(current, "go.mod")); err == nil && !info.IsDir() {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return fallback
}

// hasRuntimeData 识别随发布包复制的数据库或密钥，兼容未显式传 production 标签的构建。
func hasRuntimeData(dataDir string) bool {
	for _, name := range []string{"oldwangvideo.db", "vault.key"} {
		if info, err := os.Stat(filepath.Join(dataDir, name)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}
