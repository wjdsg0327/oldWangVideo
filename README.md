# 老王创作台

基于 Wails v3、Vue 3 + TypeScript 和 Go 的多供应商 AI 内容生成桌面工作台。所有供应商通过 OpenAI API 兼容客户端接入，配置、模型能力、任务历史和收藏保存在本地 SQLite。

## 功能

- 文生图、文生视频、图生图、图生视频四类生成工作台。
- 一个或多个供应商/模型并发生成，单任务失败与取消互不影响。
- 多模型提示词润色、风格模板保存和一键采纳。
- 供应商连通性检测、按供应商配置 HTTP/HTTPS/SOCKS5 代理、`/models` 拉取、模型能力手动标注和启停。
- 图片拖拽、粘贴、文件选择；远程媒体成功后立即落盘到本地。
- 图片或视频成功落盘后发送 Windows 系统通知。
- API Key 使用本地 AES-GCM 密钥加密，前端和历史记录不会返回完整密钥。

## 环境

- Go 1.25 或更新版本。
- Node.js 20.19+（当前开发环境为 Node 23）和 npm。
- Wails v3 beta.8 CLI，命令名为 `wails3`。

## 开发与构建

在项目根目录执行：

```powershell
go mod download
npm --prefix frontend install
wails3 dev
```

生产构建：

```powershell
npm --prefix frontend run typecheck
npm --prefix frontend run build
wails3 build
```

Windows 构建产物会写入 `bin/`。开发构建的运行时数据保存在项目根目录；独立发布后保存在 EXE 所在目录，其中 `oldwangvideo.db` 是 SQLite 数据库，`vault.key` 是加密密钥，生成媒体位于 `outputs/`。

## MCP 生图服务

项目内置 MCP stdio 服务，供本地 GPT、Claude Desktop、zcode 等支持 MCP 的客户端调用文生图和图生图。服务与桌面端共用 `oldwangvideo.db`、`vault.key` 以及供应商/模型配置；先在桌面端“供应商与模型”页保存 API Key，并为目标模型勾选“文生图”或“图生图”能力。

在项目根目录启动开发版 MCP 服务：

```powershell
go run . --mcp
```

发布版可直接启动 EXE：

```powershell
bin\OldWangVideo.exe --mcp
```

需要指定数据和输出目录时追加参数（目录不存在会自动创建）：

```powershell
bin\OldWangVideo.exe --mcp --data-dir D:\oldwangvideo-data --output-dir D:\oldwangvideo-data\outputs
```

MCP 使用 newline-delimited JSON-RPC over stdio；客户端配置中的 `command` 必须直接启动上述命令，标准输出只保留协议消息。Claude Desktop 配置示例（Windows `claude_desktop_config.json`）：

```json
{
  "mcpServers": {
    "oldwangvideo": {
      "command": "D:\\Documents\\go\\oldWangVideo\\bin\\OldWangVideo.exe",
      "args": [
        "--mcp",
        "--data-dir",
        "D:\\Documents\\go\\oldWangVideo",
        "--output-dir",
        "D:\\Documents\\go\\oldWangVideo\\outputs"
      ]
    }
  }
}
```

GPT 或 zcode 的 MCP 配置项同样填写 `command` 和 `args`；不同客户端的外层键名可能不同，但参数保持一致：

```json
{
  "command": "C:\\path\\to\\OldWangVideo.exe",
  "args": [
    "--mcp",
    "--data-dir",
    "C:\\path\\to\\oldwangvideo-data",
    "--output-dir",
    "C:\\path\\to\\oldwangvideo-data\\outputs"
  ]
}
```

支持的工具如下：

- `list_image_models`：列出已启用且标注图片能力的模型，返回 `providerId`、`modelId` 和远端模型 ID。
- `generate_image`：文生图，必填 `prompt`；可选 `provider_id`、`model_id`、`model`、`negative_prompt`、`size`、`width`、`height`、`count`。
- `image_to_image`：图生图，除上述参数外需要 `images` 或 `image_paths`；图片可使用本地路径、`http(s)` URL、`data:` URL，或带 `base64`/`url`/`mimeType` 字段的对象。

工具调用会等待任务完成，并返回 MCP `image` 内容和本地文件路径。若有多个候选模型，请先调用 `list_image_models`，再把返回的 `model_id` 传给生成工具。MCP 服务只支持 stdio，不提供 HTTP 端点；不要把日志写入 stdout，以免破坏协议通信。

## 供应商配置

在“供应商与模型”页填写显示名称、Base URL 和 API Key。需要代理时，可为该供应商填写 `http://127.0.0.1:7890`、`socks5://127.0.0.1:1080` 等代理地址；测试连接、模型同步、生成请求和媒体下载都会使用它，留空则沿用系统环境代理设置。可从硅基流动、Together AI、Fireworks、本地 vLLM 或 Ollama 预设开始；保存后用“测试连接”访问 `{Base URL}/models`，再为每个模型明确标注支持的能力。

APIKEY.FUN 已提供专用预设 `https://api.apikey.fun/v1`，并单独适配图片 SSE、`/images/edits` 图生图、`/videos/generations` 异步视频以及图生视频的 `image.url` 格式。同步模型后，仍需按模型实际能力手动勾选文生图、图生图、文生视频或图生视频。

AIXORAS 已提供专用预设 `https://api.aixoras.com/v1`。文生视频与图生视频分别构造独立请求，统一提交到 `/video/generations` 并通过 `/video/generations/{task_id}` 轮询；图生视频会按其协议发送 `images`、`image` 和 `input_reference` 字符串字段。

豆包方舟已提供专用预设 `https://ark.cn-beijing.volces.com/api/v3`，适配 Seedance 内容生成接口 `/contents/generations/tasks`。文生视频发送文本内容，图生视频追加 `reference_image` 图片内容，任务通过 `/contents/generations/tasks/{id}` 轮询；请求默认开启音频生成并关闭水印，尺寸会映射为方舟支持的 `ratio`，时长支持工作台的 5、8、10、11、12 秒选项。

FastAIToken 已提供专用预设 `https://fastaitoken.com/v1`，适配 Seedance 2.0 接口 `/seedance/api/v3/contents/generations/tasks`。创建任务返回 `id` 后，通过同路径的 `/{id}` 查询，成功视频位于 `content.video_url`；文生视频与图生视频均支持，图像会以 `data:` URL 发送，默认开启音频并关闭水印。模型 ID 使用 `doubao-seedance-2-0-260128` 或 `doubao-seedance-2-0-fast-260128`。令牌需要启用 `SeeDance2` 分组并使用按量计费模式；历史配置中的 `www.fastaitoken.com`、`api.fastaitoken.com` 或完整任务路径都会规范到案例使用的 `fastaitoken.com` Seedance 端点。

`/models` 通常只返回模型标识，不能可靠推断图片或视频能力，因此应用不会自动替用户开通能力。不同供应商的媒体端点存在差异，后端会根据 Base URL 识别内置协议 profile；未知供应商默认使用 OpenAI 兼容路径。

## 项目结构

- `internal/domain`：领域类型、能力枚举和供应商预设。
- `internal/store`、`internal/security`：SQLite 仓储、迁移和 API Key 加密。
- `internal/openai`：统一文本、图片、视频请求及供应商 profile。
- `internal/task`：并发任务、超时、取消、事件和错误隔离。
- `studio_service.go`：Wails 服务绑定入口。
- `frontend/src`：Vue 工作台、供应商管理、历史收藏和桥接层。
- `output/playwright`：已验证的桌面与移动视口截图。

## 验证

```powershell
go test ./...
go vet ./...
npm --prefix frontend run typecheck
npm --prefix frontend run build
```

浏览器开发模式会使用内置演示数据，不会写入供应商密钥；桌面端启动后才会调用真实 Go 服务。视频和部分图生媒体接口由供应商扩展协议提供，具体可用字段以供应商文档为准。
