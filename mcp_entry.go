package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	localmcp "oldwangvideo/internal/mcp"
)

// runMCPCommand 解析命令行参数并启动 stdio MCP 服务。
func runMCPCommand(args []string) error {
	flags := flag.NewFlagSet("oldwangvideo --mcp", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	// 保留 --mcp 开关本身，便于命令行解析器接收主函数已经识别的模式参数。
	_ = flags.Bool("mcp", false, "以 stdio 模式启动 MCP 服务")
	dataDirFlag := flags.String("data-dir", "", "本地数据库和密钥目录")
	outputDirFlag := flags.String("output-dir", "", "生成图片输出目录")
	if err := flags.Parse(args); err != nil {
		return err
	}
	var dataDir, outputDir string
	var err error
	// go run 生成的临时可执行文件不在项目目录，开发模式下优先使用当前工作区的数据。
	if currentDir, currentErr := os.Getwd(); currentErr == nil && isDevelopmentWorkspace(currentDir) {
		dataDir = currentDir
		outputDir = filepath.Join(currentDir, "outputs")
		if err := os.MkdirAll(outputDir, 0o700); err != nil {
			return fmt.Errorf("创建开发输出目录失败: %w", err)
		}
	} else {
		dataDir, outputDir, err = applicationDirectories()
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(*dataDirFlag) != "" {
		dataDir, err = filepath.Abs(strings.TrimSpace(*dataDirFlag))
		if err != nil {
			return fmt.Errorf("解析 --data-dir 失败: %w", err)
		}
		if strings.TrimSpace(*outputDirFlag) == "" {
			outputDir = filepath.Join(dataDir, "outputs")
		}
	}
	if strings.TrimSpace(*outputDirFlag) != "" {
		outputDir, err = filepath.Abs(strings.TrimSpace(*outputDirFlag))
		if err != nil {
			return fmt.Errorf("解析 --output-dir 失败: %w", err)
		}
		if err := os.MkdirAll(outputDir, 0o700); err != nil {
			return fmt.Errorf("创建 --output-dir 失败: %w", err)
		}
	}
	return localmcp.Run(context.Background(), localmcp.Options{DataDir: dataDir, OutputDir: outputDir})
}

// isDevelopmentWorkspace 判断当前目录是否为本项目工作区，避免 go run 使用临时目录保存密钥和数据库。
func isDevelopmentWorkspace(path string) bool {
	if _, err := os.Stat(filepath.Join(path, "go.mod")); err != nil {
		return false
	}
	frontendInfo, err := os.Stat(filepath.Join(path, "frontend"))
	return err == nil && frontendInfo.IsDir()
}
