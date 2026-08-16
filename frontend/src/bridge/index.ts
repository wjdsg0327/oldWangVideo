import type {
  BatchStart,
  BootstrapState,
  Capability,
  GenerationHistory,
  GenerationRequest,
  HistoryPage,
  ImageInput,
  Model,
  PromptPolishRequest,
  PromptTemplate,
  Provider,
  TaskSnapshot,
  TaskStatus,
} from "../types";

type StudioAPI = typeof import("../../bindings/oldwangvideo/index.js").StudioService;

const StudioService = new Proxy({} as StudioAPI, {
  get(_target, property: string) {
    return async (...args: unknown[]) => {
      const bindings = await import("../../bindings/oldwangvideo/index.js");
      const method = bindings.StudioService[property as keyof StudioAPI] as (...values: unknown[]) => unknown;
      return method(...args);
    };
  },
});

const browserWindow = typeof window === "undefined" ? undefined : window as Window & {
  _wails?: { invoke?: unknown };
  chrome?: { webview?: { postMessage?: unknown } };
  webkit?: { messageHandlers?: { external?: { postMessage?: unknown } } };
  wails?: { invoke?: unknown };
};

// Wails v3 beta 运行时使用 `_wails.invoke`，同时兼容旧版宿主注入方式。
const desktopRuntime = Boolean(
  browserWindow?._wails?.invoke
  || browserWindow?.chrome?.webview?.postMessage
  || browserWindow?.webkit?.messageHandlers?.external?.postMessage
  || browserWindow?.wails?.invoke,
);
const mockListeners = new Set<(task: TaskSnapshot) => void>();
const mockTimers = new Map<string, number[]>();
const mockRequests = new Map<string, { kind: "generation" | "prompt_polish"; request: GenerationRequest | PromptPolishRequest }>();
const mockTaskSnapshots = new Map<string, TaskSnapshot>();

let mockProviderSequence = 3;
let mockModelSequence = 6;
let mockTemplateSequence = 3;
let mockHistorySequence = 1;

const now = () => new Date().toISOString();

const mockState: BootstrapState = {
  data: {
    providers: [
      { id: 1, name: "硅基流动", baseUrl: "https://api.siliconflow.cn/v1", hasApiKey: true, enabled: true, createdAt: now(), updatedAt: now() },
      { id: 2, name: "本地工作站", baseUrl: "http://127.0.0.1:8000/v1", hasApiKey: false, enabled: true, createdAt: now(), updatedAt: now() },
    ],
    models: [
      { id: 1, providerId: 1, providerName: "硅基流动", remoteId: "black-forest-labs/FLUX.1-dev", name: "FLUX.1 Dev", capabilities: ["text_to_image", "image_to_image"], manual: false, enabled: true, createdAt: now(), updatedAt: now() },
      { id: 2, providerId: 1, providerName: "硅基流动", remoteId: "Wan-AI/Wan2.2-T2V-A14B", name: "Wan 2.2 Video", capabilities: ["text_to_video", "image_to_video"], manual: false, enabled: true, createdAt: now(), updatedAt: now() },
      { id: 3, providerId: 1, providerName: "硅基流动", remoteId: "Qwen/Qwen3-32B", name: "Qwen3 32B", capabilities: ["prompt_polish"], manual: false, enabled: true, createdAt: now(), updatedAt: now() },
      { id: 4, providerId: 2, providerName: "本地工作站", remoteId: "qwen3:14b", name: "Qwen3 Local", capabilities: ["prompt_polish"], manual: true, enabled: true, createdAt: now(), updatedAt: now() },
      { id: 5, providerId: 2, providerName: "本地工作站", remoteId: "flux-local", name: "Flux Local", capabilities: ["text_to_image", "image_to_image"], manual: true, enabled: true, createdAt: now(), updatedAt: now() },
    ],
    promptTemplates: [
      { id: 1, name: "写实优化", systemPrompt: "补充真实环境、自然光线、镜头焦段、景深、材质纹理与色彩关系。", enabled: true, createdAt: now(), updatedAt: now() },
      { id: 2, name: "动漫风格", systemPrompt: "优化为高质量动漫分镜，补充角色、动态构图、光影、色彩和镜头语言。", enabled: true, createdAt: now(), updatedAt: now() },
    ],
    providerPresets: [
      { id: "apikeyfun", name: "APIKEY.FUN", baseUrl: "https://api.apikey.fun/v1", description: "已适配图片与视频生成", local: false, apiKeyRequired: true },
      { id: "aixoras", name: "AIXORAS", baseUrl: "https://api.aixoras.com/v1", description: "已独立适配文生视频与图生视频", local: false, apiKeyRequired: true },
      { id: "siliconflow", name: "硅基流动", baseUrl: "https://api.siliconflow.cn/v1", description: "图片与视频生成平台", local: false, apiKeyRequired: true },
      { id: "together", name: "Together AI", baseUrl: "https://api.together.xyz/v1", description: "OpenAI 兼容推理服务", local: false, apiKeyRequired: true },
      { id: "fireworks", name: "Fireworks AI", baseUrl: "https://api.fireworks.ai/inference/v1", description: "高性能文本模型服务", local: false, apiKeyRequired: true },
      { id: "vllm", name: "本地 vLLM", baseUrl: "http://127.0.0.1:8000/v1", description: "本机或局域网 vLLM", local: true, apiKeyRequired: false },
      { id: "ollama", name: "本地 Ollama", baseUrl: "http://127.0.0.1:11434/v1", description: "Ollama OpenAI 兼容接口", local: true, apiKeyRequired: false },
    ],
    capabilities: ["text_to_image", "text_to_video", "image_to_image", "image_to_video", "prompt_polish"],
    recentHistory: [],
    favorites: [],
  },
  activeTasks: [],
  dataDir: "浏览器演示模式",
};

function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function emitMock(task: TaskSnapshot) {
  mockTaskSnapshots.set(task.id, clone(task));
  mockListeners.forEach((listener) => listener(clone(task)));
}

function updateMockActive(task: TaskSnapshot) {
  const index = mockState.activeTasks.findIndex((item) => item.id === task.id);
  if (index >= 0) mockState.activeTasks[index] = clone(task);
}

function removeMockActive(taskId: string) {
  mockState.activeTasks = mockState.activeTasks.filter((task) => task.id !== taskId);
}

function mockBatch(kind: "generation" | "prompt_polish", request: GenerationRequest | PromptPolishRequest): BatchStart {
  const batchId = crypto.randomUUID();
  const repeat = kind === "generation" ? Math.max(1, Math.min(8, Math.trunc((request as GenerationRequest).options.count || 1))) : 1;
  const taskTargets = request.targets.flatMap((target) => Array.from({ length: repeat }, () => target));
  const tasks = taskTargets.map((target, index) => {
    const model = mockState.data.models.find((item) => item.id === target.modelId)!;
    const provider = mockState.data.providers.find((item) => item.id === target.providerId)!;
    const task: TaskSnapshot = {
      id: crypto.randomUUID(),
      batchId,
      kind,
      capability: kind === "generation" ? (request as GenerationRequest).capability : "prompt_polish",
      providerId: provider.id,
      providerName: provider.name,
      modelId: model.id,
      modelName: model.name,
      status: "queued",
      stage: "等待执行",
      progress: 0,
      revision: index + 1,
      prompt: request.prompt,
      retryable: false,
      createdAt: now(),
      updatedAt: now(),
    };
    const taskRequest = clone(request);
    if (kind === "generation") (taskRequest as GenerationRequest).options.count = 1;
    mockRequests.set(task.id, { kind, request: taskRequest });
    const runningTimer = window.setTimeout(() => {
      const running: TaskSnapshot = { ...task, status: "running", stage: kind === "generation" ? "正在生成内容" : "正在润色提示词", progress: 36, revision: task.revision + 10, updatedAt: now() };
      updateMockActive(running);
      emitMock(running);
    }, 350 + index * 160);
    const completeTimer = window.setTimeout(() => {
      const completed: TaskSnapshot = {
        ...task,
        status: "succeeded",
        stage: kind === "generation" ? "生成完成" : "润色完成",
        progress: 100,
        revision: task.revision + 20,
        completedAt: now(),
        updatedAt: now(),
      };
      if (kind === "prompt_polish") {
        completed.polishResult = { text: `${request.prompt}，主体细节清晰，环境层次丰富，自然光线，电影级构图，材质真实，色彩克制` };
      } else {
        const capability = (request as GenerationRequest).capability;
        completed.result = {
          assets: [{
            type: capability.includes("video") ? "video" : "image",
            url: capability.includes("video") ? undefined : "/demo-output.jpg",
            mimeType: capability.includes("video") ? "video/mp4" : "image/jpeg",
          }],
        };
      }
      updateMockActive(completed);
      emitMock(completed);
      if (kind === "generation") {
        mockState.data.recentHistory.unshift({
          id: mockHistorySequence++,
          taskId: completed.id,
          batchId: completed.batchId,
          providerId: completed.providerId,
          modelId: completed.modelId,
          providerName: completed.providerName,
          modelName: completed.modelName,
          capability: completed.capability as Capability,
          request: clone(request as GenerationRequest),
          status: "succeeded",
          result: completed.result,
          favorite: false,
          createdAt: completed.createdAt,
          updatedAt: completed.updatedAt,
        });
      }
      mockTimers.delete(completed.id);
      window.setTimeout(() => removeMockActive(completed.id), 0);
    }, 1500 + index * 260);
    mockTimers.set(task.id, [runningTimer, completeTimer]);
    mockState.activeTasks.push(task);
    return task;
  });
  return { batchId, tasks };
}

export const bridge = {
  isDesktop: desktopRuntime,

  async subscribeTasks(listener: (task: TaskSnapshot) => void): Promise<() => void> {
    if (!desktopRuntime) {
      mockListeners.add(listener);
      return () => mockListeners.delete(listener);
    }
    const { Events } = await import("@wailsio/runtime");
    return Events.On("task:update", (event) => listener(event.data as TaskSnapshot));
  },

  async subscribeSourceImage(listener: (image: ImageInput) => void, onError: (message: string) => void): Promise<() => void> {
    if (!desktopRuntime) return () => undefined;
    const { Events } = await import("@wailsio/runtime");
    const removeImage = Events.On("source-image:dropped", (event) => listener(event.data as ImageInput));
    const removeError = Events.On("source-image:drop-error", (event) => onError(String(event.data || "读取来源图片失败")));
    return () => {
      removeImage();
      removeError();
    };
  },

  async getBootstrap(): Promise<BootstrapState> {
    return desktopRuntime ? (await StudioService.GetBootstrapState() as BootstrapState) : clone(mockState);
  },

  async startGeneration(request: GenerationRequest): Promise<BatchStart> {
    return desktopRuntime ? (await StudioService.StartGeneration(request as never) as BatchStart) : mockBatch("generation", request);
  },

  async startPolish(request: PromptPolishRequest): Promise<BatchStart> {
    return desktopRuntime ? (await StudioService.StartPolish(request as never) as BatchStart) : mockBatch("prompt_polish", request);
  },

  async cancelTask(taskId: string) {
    if (desktopRuntime) return StudioService.CancelTask(taskId);
    const task = mockTaskSnapshots.get(taskId) || mockState.activeTasks.find((item) => item.id === taskId);
    if (!task || ["succeeded", "failed", "canceled"].includes(task.status)) return false;
    mockTimers.get(taskId)?.forEach((timer) => window.clearTimeout(timer));
    mockTimers.delete(taskId);
    const canceled: TaskSnapshot = { ...task, status: "canceled", stage: "已取消", progress: task.progress, cancelMode: "local_only", revision: task.revision + 20, completedAt: now(), updatedAt: now() };
    emitMock(canceled);
    removeMockActive(taskId);
    return true;
  },

  async cancelBatch(batchId: string) {
    if (desktopRuntime) return StudioService.CancelBatch(batchId);
    const taskIds = mockState.activeTasks.filter((task) => task.batchId === batchId).map((task) => task.id);
    let canceled = 0;
    for (const taskId of taskIds) if (await this.cancelTask(taskId)) canceled++;
    return canceled;
  },

  async retryTask(taskId: string): Promise<BatchStart> {
    if (desktopRuntime) return await StudioService.RetryTask(taskId) as BatchStart;
    const task = mockTaskSnapshots.get(taskId) || mockState.activeTasks.find((item) => item.id === taskId);
    const saved = mockRequests.get(taskId);
    if (!task || !saved) throw new Error("演示任务不存在");
    const request = clone(saved.request);
    request.targets = [{ providerId: task.providerId, modelId: task.modelId }];
    return mockBatch(saved.kind, request);
  },

  async chooseSourceImage(): Promise<ImageInput> {
    return desktopRuntime ? await StudioService.ChooseSourceImage() as ImageInput : {};
  },

  async downloadTask(taskId: string) {
    return desktopRuntime ? StudioService.DownloadTaskResult(taskId) : "浏览器演示模式不写入文件";
  },

  async createProvider(input: { name: string; baseUrl: string; apiKey: string; enabled: boolean }): Promise<Provider> {
    if (desktopRuntime) return await StudioService.CreateProvider(input) as Provider;
    const item: Provider = { id: mockProviderSequence++, name: input.name, baseUrl: input.baseUrl, hasApiKey: Boolean(input.apiKey), enabled: input.enabled, createdAt: now(), updatedAt: now() };
    mockState.data.providers.push(item);
    return clone(item);
  },

  async updateProvider(id: number, input: { name: string; baseUrl: string; apiKey?: string; enabled: boolean }): Promise<Provider> {
    if (desktopRuntime) return await StudioService.UpdateProvider(id, input as never) as Provider;
    const item = mockState.data.providers.find((provider) => provider.id === id)!;
    Object.assign(item, { name: input.name, baseUrl: input.baseUrl, enabled: input.enabled, hasApiKey: input.apiKey ? true : item.hasApiKey, updatedAt: now() });
    return clone(item);
  },

  async deleteProvider(id: number) {
    if (desktopRuntime) return StudioService.DeleteProvider(id);
    mockState.data.providers = mockState.data.providers.filter((item) => item.id !== id);
    mockState.data.models = mockState.data.models.filter((item) => item.providerId !== id);
  },

  async setProviderEnabled(id: number, enabled: boolean) {
    if (desktopRuntime) return StudioService.SetProviderEnabled(id, enabled);
    const item = mockState.data.providers.find((provider) => provider.id === id);
    if (item) item.enabled = enabled;
  },

  async testProvider(id: number) {
    if (desktopRuntime) return StudioService.TestProvider(id);
    return { success: true, latencyMs: 128, models: [], message: "连接成功，发现 3 个模型" };
  },

  async refreshProviderModels(id: number): Promise<Model[]> {
    if (desktopRuntime) return await StudioService.RefreshProviderModels(id) as Model[];
    return clone(mockState.data.models.filter((item) => item.providerId === id));
  },

  async createModel(input: { providerId: number; remoteId: string; name: string; capabilities: Capability[]; enabled: boolean }): Promise<Model> {
    if (desktopRuntime) return await StudioService.CreateModel(input as never) as Model;
    const provider = mockState.data.providers.find((item) => item.id === input.providerId)!;
    const item: Model = { id: mockModelSequence++, providerId: input.providerId, providerName: provider.name, remoteId: input.remoteId, name: input.name || input.remoteId, capabilities: input.capabilities, manual: true, enabled: input.enabled, createdAt: now(), updatedAt: now() };
    mockState.data.models.push(item);
    return clone(item);
  },

  async updateModel(id: number, input: { remoteId: string; name: string; capabilities: Capability[]; enabled: boolean }): Promise<Model> {
    if (desktopRuntime) return await StudioService.UpdateModel(id, input as never) as Model;
    const item = mockState.data.models.find((model) => model.id === id)!;
    Object.assign(item, input, { updatedAt: now() });
    return clone(item);
  },

  async deleteModel(id: number) {
    if (desktopRuntime) return StudioService.DeleteModel(id);
    mockState.data.models = mockState.data.models.filter((item) => item.id !== id);
  },

  async createTemplate(input: { name: string; systemPrompt: string; enabled: boolean }): Promise<PromptTemplate> {
    if (desktopRuntime) return await StudioService.CreatePromptTemplate(input) as PromptTemplate;
    const item: PromptTemplate = { id: mockTemplateSequence++, ...input, createdAt: now(), updatedAt: now() };
    mockState.data.promptTemplates.push(item);
    return clone(item);
  },

  async updateTemplate(id: number, input: { name: string; systemPrompt: string; enabled: boolean }): Promise<PromptTemplate> {
    if (desktopRuntime) return await StudioService.UpdatePromptTemplate(id, input) as PromptTemplate;
    const item = mockState.data.promptTemplates.find((template) => template.id === id)!;
    Object.assign(item, input, { updatedAt: now() });
    return clone(item);
  },

  async deleteTemplate(id: number) {
    if (desktopRuntime) return StudioService.DeletePromptTemplate(id);
    mockState.data.promptTemplates = mockState.data.promptTemplates.filter((item) => item.id !== id);
  },

  async listHistory(filter: { page: number; pageSize: number; capability?: Capability; status?: TaskStatus; favoriteOnly?: boolean; search?: string }): Promise<HistoryPage> {
    if (desktopRuntime) return await StudioService.ListHistory(filter as never) as HistoryPage;
    let items = mockState.data.recentHistory;
    if (filter.favoriteOnly) items = items.filter((item) => item.favorite);
    if (filter.capability) items = items.filter((item) => item.capability === filter.capability);
    if (filter.search) items = items.filter((item) => `${item.providerName}${item.modelName}${item.request.prompt}`.toLowerCase().includes(filter.search!.toLowerCase()));
    return { items: clone(items), total: items.length, page: filter.page, pageSize: filter.pageSize };
  },

  async setFavorite(historyId: number, favorite: boolean) {
    if (desktopRuntime) return StudioService.SetFavorite(historyId, favorite);
    const item = mockState.data.recentHistory.find((history) => history.id === historyId);
    if (item) item.favorite = favorite;
  },

  async deleteHistory(historyId: number) {
    if (desktopRuntime) return StudioService.DeleteHistory(historyId);
    mockState.data.recentHistory = mockState.data.recentHistory.filter((item) => item.id !== historyId);
    mockHistorySequence++;
  },

  async deleteHistories(historyIds: number[]) {
    const ids = [...new Set(historyIds.filter((id) => Number.isInteger(id) && id > 0))];
    if (!ids.length) return;
    if (desktopRuntime) return StudioService.DeleteHistories(ids);
    const selected = new Set(ids);
    mockState.data.recentHistory = mockState.data.recentHistory.filter((item) => !selected.has(item.id));
    mockHistorySequence++;
  },
};
