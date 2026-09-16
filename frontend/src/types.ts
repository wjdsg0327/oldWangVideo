export type Capability =
  | "text_to_image"
  | "text_to_video"
  | "image_to_image"
  | "image_to_video"
  | "prompt_polish";

export type TaskStatus = "queued" | "running" | "succeeded" | "failed" | "canceled";
export type TaskKind = "generation" | "prompt_polish";

export interface Provider {
  id: number;
  name: string;
  baseUrl: string;
  proxyUrl?: string;
  hasApiKey: boolean;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface ProviderPreset {
  id: string;
  name: string;
  baseUrl: string;
  description: string;
  local: boolean;
  apiKeyRequired: boolean;
}

export interface Model {
  id: number;
  providerId: number;
  providerName?: string;
  remoteId: string;
  name: string;
  capabilities: Capability[];
  manual: boolean;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface PromptTemplate {
  id: number;
  name: string;
  systemPrompt: string;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface ImageInput {
  name?: string;
  mimeType?: string;
  base64?: string;
  url?: string;
}

export interface ModelTarget {
  providerId: number;
  modelId: number;
}

export interface GenerationOptions {
  size?: string;
  aspectRatio?: string;
  width?: number;
  height?: number;
  durationSeconds?: number;
  fps?: number;
  steps?: number;
  guidanceScale?: number;
  seed?: number;
  count?: number;
  extra?: Record<string, unknown>;
}

export interface GenerationRequest {
  capability: Capability;
  prompt: string;
  negativePrompt?: string;
  images: ImageInput[];
  targets: ModelTarget[];
  options: GenerationOptions;
}

export interface PromptPolishRequest {
  prompt: string;
  systemPrompt?: string;
  templateId?: number;
  targets: ModelTarget[];
}

export interface MediaAsset {
  type: "image" | "video";
  url?: string;
  localPath?: string;
  thumbnailUrl?: string;
  mimeType?: string;
  width?: number;
  height?: number;
  duration?: number;
  metadata?: Record<string, unknown>;
}

export interface GenerationResult {
  assets: MediaAsset[];
  revisedPrompt?: string;
  metadata?: Record<string, unknown>;
}

export interface TaskSnapshot {
  id: string;
  batchId: string;
  kind: TaskKind;
  capability?: Capability;
  providerId: number;
  providerName: string;
  modelId: number;
  modelName: string;
  status: TaskStatus;
  stage?: string;
  progress: number;
  revision: number;
  prompt: string;
  result?: GenerationResult | null;
  polishResult?: { text: string; metadata?: Record<string, unknown> } | null;
  errorMessage?: string;
  errorCode?: string;
  retryable: boolean;
  cancelMode?: string;
  createdAt: string;
  startedAt?: string | null;
  completedAt?: string | null;
  updatedAt: string;
}

export interface BatchStart {
  batchId: string;
  tasks: TaskSnapshot[];
}

export interface GenerationHistory {
  id: number;
  taskId: string;
  batchId: string;
  providerId?: number;
  modelId?: number;
  providerName: string;
  modelName: string;
  capability: Capability;
  request: GenerationRequest;
  status: TaskStatus;
  result?: GenerationResult | null;
  errorMessage?: string;
  favorite: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface HistoryPage {
  items: GenerationHistory[];
  total: number;
  page: number;
  pageSize: number;
}

export interface BootstrapState {
  data: {
    providers: Provider[];
    models: Model[];
    promptTemplates: PromptTemplate[];
    providerPresets: ProviderPreset[];
    capabilities: Capability[];
    recentHistory: GenerationHistory[];
    favorites: GenerationHistory[];
  };
  activeTasks: TaskSnapshot[];
  dataDir: string;
}

export interface WorkspaceDraft {
  prompt: string;
  negativePrompt: string;
  images: ImageInput[];
  targetIds: number[];
  polishTargetIds: number[];
  templateId: number;
  mode: "single" | "parallel";
  size: string;
  durationSeconds: number;
  count: number;
}

export interface ToastMessage {
  id: number;
  text: string;
  tone: "success" | "error" | "info";
}
