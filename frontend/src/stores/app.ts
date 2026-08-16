import { defineStore } from "pinia";
import { bridge } from "../bridge";
import type {
  BatchStart,
  Capability,
  GenerationRequest,
  HistoryPage,
  ImageInput,
  Model,
  PromptPolishRequest,
  PromptTemplate,
  Provider,
  ProviderPreset,
  TaskSnapshot,
  ToastMessage,
  WorkspaceDraft,
} from "../types";

const generationCapabilities: Capability[] = ["text_to_image", "text_to_video", "image_to_image", "image_to_video"];

function createDraft(capability: Capability): WorkspaceDraft {
  return {
    prompt: "",
    negativePrompt: "",
    image: null,
    targetIds: [],
    polishTargetIds: [],
    templateId: 0,
    mode: "parallel",
    size: capability.includes("video") ? "1280x720" : "1024x1024",
    durationSeconds: 5,
    count: 1,
  };
}

function errorText(error: unknown): string {
  if (error instanceof Error) return error.message;
  if (typeof error === "string") return error;
  if (error && typeof error === "object" && "message" in error) return String((error as { message: unknown }).message);
  return "操作失败";
}

let unsubscribeTasks: (() => void) | undefined;
let toastSequence = 1;

export const useAppStore = defineStore("app", {
  state: () => ({
    initialized: false,
    loading: false,
    dataDir: "",
    providers: [] as Provider[],
    models: [] as Model[],
    templates: [] as PromptTemplate[],
    presets: [] as ProviderPreset[],
    tasks: {} as Record<string, TaskSnapshot>,
    taskOrder: [] as string[],
    lastBatchByCapability: {} as Partial<Record<Capability, string>>,
    lastPolishBatchByCapability: {} as Partial<Record<Capability, string>>,
    drafts: Object.fromEntries(generationCapabilities.map((capability) => [capability, createDraft(capability)])) as Record<Capability, WorkspaceDraft>,
    historyPage: { items: [], total: 0, page: 1, pageSize: 24 } as HistoryPage,
    historyLoading: false,
    toasts: [] as ToastMessage[],
  }),

  getters: {
    enabledProviders(state): Provider[] {
      return state.providers.filter((provider) => provider.enabled);
    },

    activeTaskCount(state): number {
      return Object.values(state.tasks).filter((task) => !["succeeded", "failed", "canceled"].includes(task.status)).length;
    },
  },

  actions: {
    async initialize() {
      if (this.initialized || this.loading) return;
      this.loading = true;
      if (!unsubscribeTasks) {
        unsubscribeTasks = await bridge.subscribeTasks((task) => this.mergeTask(task));
      }
      try {
        const bootstrap = await bridge.getBootstrap();
        this.providers = bootstrap.data.providers ?? [];
        this.models = bootstrap.data.models ?? [];
        this.templates = bootstrap.data.promptTemplates ?? [];
        this.presets = bootstrap.data.providerPresets ?? [];
        this.dataDir = bootstrap.dataDir;
        bootstrap.activeTasks?.forEach((task) => this.mergeTask(task));
        this.historyPage = { items: bootstrap.data.recentHistory ?? [], total: bootstrap.data.recentHistory?.length ?? 0, page: 1, pageSize: 24 };
        this.initialized = true;
      } catch (error) {
        this.toast(errorText(error), "error");
      } finally {
        this.loading = false;
      }
    },

    dispose() {
      unsubscribeTasks?.();
      unsubscribeTasks = undefined;
    },

    mergeTask(task: TaskSnapshot) {
      const current = this.tasks[task.id];
      if (current && current.revision >= task.revision) return;
      this.tasks[task.id] = task;
      if (!this.taskOrder.includes(task.id)) this.taskOrder.push(task.id);
      if (task.status === "failed") this.toast(`${task.providerName} / ${task.modelName}：${task.errorMessage || "执行失败"}`, "error");
    },

    addBatch(batch: BatchStart, capability: Capability, polish = false) {
      batch.tasks.forEach((task) => this.mergeTask(task));
      if (polish) this.lastPolishBatchByCapability[capability] = batch.batchId;
      else this.lastBatchByCapability[capability] = batch.batchId;
    },

    modelsFor(capability: Capability): Model[] {
      const enabledProviders = new Set(this.providers.filter((provider) => provider.enabled).map((provider) => provider.id));
      return this.models.filter((model) => model.enabled && enabledProviders.has(model.providerId) && model.capabilities.includes(capability));
    },

    tasksForBatch(batchId?: string): TaskSnapshot[] {
      if (!batchId) return [];
      return this.taskOrder.map((id) => this.tasks[id]).filter((task) => task?.batchId === batchId);
    },

    async startGeneration(request: GenerationRequest) {
      try {
        const batch = await bridge.startGeneration(request);
        this.addBatch(batch, request.capability);
        this.toast(`已提交 ${batch.tasks.length} 个生成任务`, "success");
        return batch;
      } catch (error) {
        this.toast(errorText(error), "error");
        throw error;
      }
    },

    async startPolish(capability: Capability, request: PromptPolishRequest) {
      try {
        const batch = await bridge.startPolish(request);
        this.addBatch(batch, capability, true);
        this.toast(`已提交 ${batch.tasks.length} 个润色任务`, "success");
        return batch;
      } catch (error) {
        this.toast(errorText(error), "error");
        throw error;
      }
    },

    async cancelTask(taskId: string) {
      const task = this.tasks[taskId];
      if (task && !["succeeded", "failed", "canceled"].includes(task.status)) {
        this.tasks[taskId] = { ...task, stage: "正在取消", cancelMode: "local_only" };
      }
      await bridge.cancelTask(taskId);
    },

    async cancelBatch(batchId: string) {
      await bridge.cancelBatch(batchId);
      this.tasksForBatch(batchId).forEach((task) => {
        if (!["succeeded", "failed", "canceled"].includes(task.status)) {
          this.tasks[task.id] = { ...task, stage: "正在取消", cancelMode: "local_only" };
        }
      });
    },

    async retryTask(taskId: string) {
      try {
        const batch = await bridge.retryTask(taskId);
        const task = this.tasks[taskId];
        const capability = task?.capability || "text_to_image";
        this.addBatch(batch, capability, task?.kind === "prompt_polish");
      } catch (error) {
        this.toast(errorText(error), "error");
      }
    },

    async downloadTask(taskId: string) {
      try {
        const path = await bridge.downloadTask(taskId);
        if (path) this.toast(`已保存到 ${path}`, "success");
      } catch (error) {
        this.toast(errorText(error), "error");
      }
    },

    async chooseSourceImage(): Promise<ImageInput | null> {
      try {
        const image = await bridge.chooseSourceImage();
        return image.base64 || image.url ? image : null;
      } catch (error) {
        this.toast(errorText(error), "error");
        return null;
      }
    },

    async saveProvider(id: number | null, input: { name: string; baseUrl: string; apiKey: string; enabled: boolean }) {
      try {
        const provider = id
          ? await bridge.updateProvider(id, { name: input.name, baseUrl: input.baseUrl, enabled: input.enabled, ...(input.apiKey ? { apiKey: input.apiKey } : {}) })
          : await bridge.createProvider(input);
        const index = this.providers.findIndex((item) => item.id === provider.id);
        if (index >= 0) this.providers[index] = provider;
        else this.providers.push(provider);
        this.toast("供应商已保存", "success");
        return provider;
      } catch (error) {
        this.toast(errorText(error), "error");
        throw error;
      }
    },

    async removeProvider(id: number) {
      try {
        await bridge.deleteProvider(id);
        this.providers = this.providers.filter((provider) => provider.id !== id);
        this.models = this.models.filter((model) => model.providerId !== id);
        this.toast("供应商已删除", "success");
        return true;
      } catch (error) {
        this.toast(errorText(error), "error");
        return false;
      }
    },

    async toggleProvider(provider: Provider) {
      try {
        await bridge.setProviderEnabled(provider.id, !provider.enabled);
        provider.enabled = !provider.enabled;
        return true;
      } catch (error) {
        this.toast(errorText(error), "error");
        return false;
      }
    },

    async testProvider(id: number) {
      try {
        const result = await bridge.testProvider(id);
        this.toast(result.message || "连接成功", "success");
        return result;
      } catch (error) {
        this.toast(errorText(error), "error");
        throw error;
      }
    },

    async refreshProviderModels(id: number) {
      try {
        const models = await bridge.refreshProviderModels(id);
        this.models = this.models.filter((model) => model.providerId !== id).concat(models);
        this.toast(`已同步 ${models.length} 个模型`, "success");
        return models;
      } catch (error) {
        this.toast(errorText(error), "error");
        throw error;
      }
    },

    async saveModel(id: number | null, input: { providerId: number; remoteId: string; name: string; capabilities: Capability[]; enabled: boolean }) {
      try {
        const model = id
          ? await bridge.updateModel(id, { remoteId: input.remoteId, name: input.name, capabilities: input.capabilities, enabled: input.enabled })
          : await bridge.createModel(input);
        const index = this.models.findIndex((item) => item.id === model.id);
        if (index >= 0) this.models[index] = model;
        else this.models.push(model);
        return model;
      } catch (error) {
        this.toast(errorText(error), "error");
        return null;
      }
    },

    async removeModel(id: number) {
      try {
        await bridge.deleteModel(id);
        this.models = this.models.filter((model) => model.id !== id);
        this.toast("模型已删除", "success");
        return true;
      } catch (error) {
        this.toast(errorText(error), "error");
        return false;
      }
    },

    async saveTemplate(id: number | null, input: { name: string; systemPrompt: string; enabled: boolean }) {
      try {
        const template = id ? await bridge.updateTemplate(id, input) : await bridge.createTemplate(input);
        const index = this.templates.findIndex((item) => item.id === template.id);
        if (index >= 0) this.templates[index] = template;
        else this.templates.push(template);
        this.toast("润色模板已保存", "success");
        return template;
      } catch (error) {
        this.toast(errorText(error), "error");
        return null;
      }
    },

    async removeTemplate(id: number) {
      try {
        await bridge.deleteTemplate(id);
        this.templates = this.templates.filter((template) => template.id !== id);
        this.toast("润色模板已删除", "success");
        return true;
      } catch (error) {
        this.toast(errorText(error), "error");
        return false;
      }
    },

    async loadHistory(filter: { page?: number; pageSize?: number; capability?: Capability; favoriteOnly?: boolean; search?: string } = {}) {
      this.historyLoading = true;
      try {
        this.historyPage = await bridge.listHistory({ page: filter.page || 1, pageSize: filter.pageSize || 24, capability: filter.capability, favoriteOnly: filter.favoriteOnly, search: filter.search });
      } catch (error) {
        this.toast(errorText(error), "error");
      } finally {
        this.historyLoading = false;
      }
    },

    async toggleFavorite(historyId: number, favorite: boolean) {
      try {
        await bridge.setFavorite(historyId, favorite);
        const item = this.historyPage.items.find((history) => history.id === historyId);
        if (item) item.favorite = favorite;
        return true;
      } catch (error) {
        this.toast(errorText(error), "error");
        return false;
      }
    },

    async removeHistories(historyIds: number[]) {
      const ids = [...new Set(historyIds.filter((id) => Number.isInteger(id) && id > 0))];
      if (!ids.length) return false;
      try {
        await bridge.deleteHistories(ids);
        const selected = new Set(ids);
        this.historyPage.items = this.historyPage.items.filter((history) => !selected.has(history.id));
        this.historyPage.total = Math.max(0, this.historyPage.total - ids.length);
        this.toast(`已删除 ${ids.length} 条历史记录`, "success");
        return true;
      } catch (error) {
        this.toast(errorText(error), "error");
        return false;
      }
    },

    async removeHistory(historyId: number) {
      return this.removeHistories([historyId]);
    },

    toast(text: string, tone: ToastMessage["tone"] = "info") {
      const item = { id: toastSequence++, text, tone };
      this.toasts.push(item);
      window.setTimeout(() => {
        this.toasts = this.toasts.filter((toast) => toast.id !== item.id);
      }, 4200);
    },
  },
});
