<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import {
  AlertCircle,
  Bookmark,
  Check,
  CircleStop,
  Download,
  Eye,
  FileImage,
  Film,
  Image as ImageIcon,
  LoaderCircle,
  Paperclip,
  Play,
  RotateCcw,
  Sparkles,
  Upload,
  WandSparkles,
  X,
} from "lucide-vue-next";
import { useAppStore } from "../stores/app";
import { bridge } from "../bridge";
import MediaPreviewDialog from "../components/MediaPreviewDialog.vue";
import type { Capability, GenerationHistory, ImageInput, MediaAsset, Model, TaskSnapshot } from "../types";

const route = useRoute();
const router = useRouter();
const store = useAppStore();
const fileInput = ref<HTMLInputElement | null>(null);
const polishOpen = ref(true);
const runningGeneration = ref(false);
const runningPolish = ref(false);
const favoritePickerOpen = ref(false);
const favoriteLoading = ref(false);
const favoriteConvertingId = ref<number | null>(null);
const favoriteSources = ref<GenerationHistory[]>([]);
const previewAsset = ref<MediaAsset | null>(null);
const previewTitle = ref("");

const moduleConfig: Record<string, { title: string; description: string; source: boolean; video: boolean }> = {
  text_to_image: { title: "文生图", description: "图像校样", source: false, video: false },
  text_to_video: { title: "文生视频", description: "动态分镜", source: false, video: true },
  image_to_image: { title: "图生图", description: "视觉变体", source: true, video: false },
  image_to_video: { title: "图生视频", description: "首帧动画", source: true, video: true },
};

const capability = computed<Capability>(() => {
  const value = String(route.params.capability);
  return value in moduleConfig ? value as Capability : "text_to_image";
});
const config = computed(() => moduleConfig[capability.value]);
const draft = computed(() => store.drafts[capability.value]);
const generationModels = computed(() => store.modelsFor(capability.value));
const polishModels = computed(() => store.modelsFor("prompt_polish"));
const generationBatchId = computed(() => store.lastBatchByCapability[capability.value]);
const polishBatchId = computed(() => store.lastPolishBatchByCapability[capability.value]);
const resultTasks = computed(() => store.tasksForBatch(generationBatchId.value));
const polishTasks = computed(() => store.tasksForBatch(polishBatchId.value));
const selectedModels = computed(() => draft.value.targetIds.map((id) => generationModels.value.find((model) => model.id === id)).filter(Boolean) as Model[]);
const selectedPolishModels = computed(() => draft.value.polishTargetIds.map((id) => polishModels.value.find((model) => model.id === id)).filter(Boolean) as Model[]);
const maxGenerationCount = computed(() => Math.min(8, Math.max(1, Math.floor(24 / Math.max(1, selectedModels.value.length)))));
const generationCount = computed(() => Math.min(maxGenerationCount.value, Math.max(1, Math.trunc(Number(draft.value.count) || 1))));
const totalTaskCount = computed(() => selectedModels.value.length * generationCount.value);
const maxSourceImageCount = computed(() => capability.value === "image_to_image" ? 16 : 1);
function imagePreview(image: ImageInput) {
  if (image.base64?.startsWith("data:")) return image.base64;
  if (image.base64) return `data:${image.mimeType || "image/png"};base64,${image.base64}`;
  return image.url || "";
}
const hasProviders = computed(() => store.enabledProviders.length > 0);
const canRun = computed(() => draft.value.prompt.trim() && selectedModels.value.length > 0 && generationCount.value >= 1 && (!config.value.source || draft.value.images.length > 0));
const batchStats = computed(() => {
  const stats = { running: 0, succeeded: 0, failed: 0, canceled: 0 };
  resultTasks.value.forEach((task) => {
    if (task.status === "queued" || task.status === "running") stats.running++;
    else stats[task.status]++;
  });
  return stats;
});

watch(() => draft.value.mode, (mode) => {
  if (mode === "single" && draft.value.targetIds.length > 1) draft.value.targetIds = draft.value.targetIds.slice(0, 1);
});

watch(generationModels, (models) => {
  const valid = new Set(models.map((model) => model.id));
  draft.value.targetIds = draft.value.targetIds.filter((id) => valid.has(id));
}, { immediate: true });

watch(polishModels, (models) => {
  const valid = new Set(models.map((model) => model.id));
  draft.value.polishTargetIds = draft.value.polishTargetIds.filter((id) => valid.has(id));
}, { immediate: true });

watch(maxGenerationCount, () => normalizeGenerationCount());

function providerName(model: Model) {
  return model.providerName || store.providers.find((provider) => provider.id === model.providerId)?.name || "未知供应商";
}

function marker(index: number) {
  return index < 26 ? String.fromCharCode(65 + index) : String(index + 1);
}

function selectedMarker(modelId: number) {
  const index = draft.value.targetIds.indexOf(modelId);
  return index >= 0 ? marker(index) : "";
}

function toggleTarget(model: Model) {
  if (draft.value.mode === "single") {
    draft.value.targetIds = [model.id];
    return;
  }
  const index = draft.value.targetIds.indexOf(model.id);
  if (index >= 0) draft.value.targetIds.splice(index, 1);
  else draft.value.targetIds.push(model.id);
}

function togglePolishTarget(modelId: number) {
  const index = draft.value.polishTargetIds.indexOf(modelId);
  if (index >= 0) draft.value.polishTargetIds.splice(index, 1);
  else draft.value.polishTargetIds.push(modelId);
}

function readImage(file: File): Promise<ImageInput | null> {
  const supportedTypes = new Set(["image/png", "image/jpeg", "image/webp"]);
  if (!supportedTypes.has(file.type.toLowerCase())) {
    store.toast(`${file.name} 不是受支持的图片`, "error");
    return Promise.resolve(null);
  }
  if (file.size > 25 * 1024 * 1024) {
    store.toast(`${file.name} 不能超过 25 MB`, "error");
    return Promise.resolve(null);
  }
  return new Promise((resolve) => {
    const reader = new FileReader();
    reader.onload = () => resolve({ name: file.name, mimeType: file.type, base64: String(reader.result) });
    reader.onerror = () => {
      store.toast(`读取 ${file.name} 失败`, "error");
      resolve(null);
    };
    reader.readAsDataURL(file);
  });
}

function addSourceImages(images: ImageInput[]) {
  if (!images.length) return;
  if (maxSourceImageCount.value === 1) {
    draft.value.images = [images[0]];
    if (images.length > 1) store.toast("图生视频只使用第一张来源图片", "info");
    return;
  }
  const available = maxSourceImageCount.value - draft.value.images.length;
  if (available <= 0) {
    store.toast(`图生图最多选择 ${maxSourceImageCount.value} 张来源图片`, "error");
    return;
  }
  draft.value.images.push(...images.slice(0, available));
  if (images.length > available) store.toast(`已达到 ${maxSourceImageCount.value} 张来源图片上限`, "info");
}

async function handleFiles(files: FileList | null) {
  const images = await Promise.all(Array.from(files || []).map(readImage));
  addSourceImages(images.filter((image): image is ImageInput => image !== null));
  if (fileInput.value) fileInput.value.value = "";
}

function handleDrop(event: DragEvent) {
  handleFiles(event.dataTransfer?.files || null);
}

function handlePaste(event: ClipboardEvent) {
  if (!config.value.source) return;
  const items = event.clipboardData ? Array.from(event.clipboardData.items) : [];
  const files = items
    .filter((candidate) => ["image/png", "image/jpeg", "image/webp"].includes(candidate.type.toLowerCase()))
    .map((item) => item.getAsFile())
    .filter((file): file is File => file !== null);
  if (files.length) void Promise.all(files.map(readImage)).then((images) => addSourceImages(images.filter((image): image is ImageInput => image !== null)));
}

async function chooseNativeImages() {
  addSourceImages(await store.chooseSourceImages());
}

function normalizeGenerationCount() {
  draft.value.count = generationCount.value;
}

async function openFavoritePicker() {
  favoritePickerOpen.value = true;
  favoriteLoading.value = true;
  try {
    const page = await bridge.listHistory({ page: 1, pageSize: 100, favoriteOnly: true });
    favoriteSources.value = page.items.filter((item) => item.status === "succeeded" && Boolean(item.result?.assets?.[0]?.url));
  } catch (error) {
    store.toast(error instanceof Error ? error.message : String(error), "error");
  } finally {
    favoriteLoading.value = false;
  }
}

function blobAsDataURL(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(new Error("读取收藏图片失败"));
    reader.readAsDataURL(blob);
  });
}

function waitForMediaEvent(media: HTMLMediaElement, eventName: string, timeout = 12000): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(() => finish(new Error("读取收藏视频超时")), timeout);
    const finish = (error?: Error) => {
      window.clearTimeout(timer);
      media.removeEventListener(eventName, onReady);
      media.removeEventListener("error", onError);
      if (error) reject(error);
      else resolve();
    };
    const onReady = () => finish();
    const onError = () => finish(new Error("无法读取收藏视频"));
    media.addEventListener(eventName, onReady, { once: true });
    media.addEventListener("error", onError, { once: true });
  });
}

async function favoriteVideoFrame(item: GenerationHistory, url: string): Promise<ImageInput> {
  const video = document.createElement("video");
  video.preload = "auto";
  video.muted = true;
  video.playsInline = true;
  try {
    const loaded = waitForMediaEvent(video, "loadeddata");
    video.src = url;
    video.load();
    await loaded;
    const seekTime = Number.isFinite(video.duration) ? Math.min(0.2, Math.max(0, video.duration / 2)) : 0;
    if (seekTime > 0.01) {
      try {
        const seeked = waitForMediaEvent(video, "seeked", 5000);
        video.currentTime = seekTime;
        await seeked;
      } catch {
        // 部分视频不支持精确定位，继续使用已解码的首帧。
      }
    }
    const sourceWidth = video.videoWidth || 1280;
    const sourceHeight = video.videoHeight || 720;
    const scale = Math.min(1, 1920 / Math.max(sourceWidth, sourceHeight));
    const canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.round(sourceWidth * scale));
    canvas.height = Math.max(1, Math.round(sourceHeight * scale));
    const context = canvas.getContext("2d");
    if (!context) throw new Error("无法创建视频首帧");
    context.drawImage(video, 0, 0, canvas.width, canvas.height);
    return {
      name: `收藏视频-${item.id}-首帧.jpg`,
      mimeType: "image/jpeg",
      base64: canvas.toDataURL("image/jpeg", 0.92),
    };
  } finally {
    video.pause();
    video.removeAttribute("src");
    video.load();
  }
}

async function favoriteHistoryAsImage(item: GenerationHistory): Promise<ImageInput> {
  const asset = item.result?.assets?.[0];
  if (!asset?.url) throw new Error("收藏结果没有可用媒体");
  if (asset.type === "video") return favoriteVideoFrame(item, asset.url);
  const response = await fetch(asset.url, { cache: "no-store" });
  if (!response.ok) throw new Error(`读取收藏图片失败（HTTP ${response.status}）`);
  const blob = await response.blob();
  const mimeType = (blob.type || asset.mimeType || "image/png").toLowerCase();
  if (!["image/png", "image/jpeg", "image/webp"].includes(mimeType)) throw new Error("收藏结果不是受支持的图片格式");
  if (blob.size > 25 * 1024 * 1024) throw new Error("收藏图片不能超过 25 MB");
  return {
    name: `收藏图片-${item.id}`,
    mimeType,
    base64: await blobAsDataURL(blob),
  };
}

async function chooseFavoriteSource(item: GenerationHistory) {
  favoriteConvertingId.value = item.id;
  try {
    addSourceImages([await favoriteHistoryAsImage(item)]);
    favoritePickerOpen.value = false;
    store.toast("已选择收藏素材", "success");
  } catch (error) {
    store.toast(error instanceof Error ? error.message : String(error), "error");
  } finally {
    favoriteConvertingId.value = null;
  }
}

async function runPolish() {
  if (!draft.value.prompt.trim() || selectedPolishModels.value.length === 0) return;
  runningPolish.value = true;
  try {
    await store.startPolish(capability.value, {
      prompt: draft.value.prompt,
      templateId: draft.value.templateId || undefined,
      targets: selectedPolishModels.value.map((model) => ({ providerId: model.providerId, modelId: model.id })),
    });
  } finally {
    runningPolish.value = false;
  }
}

async function runGeneration() {
  if (!canRun.value) return;
  runningGeneration.value = true;
  try {
    await store.startGeneration({
      capability: capability.value,
      prompt: draft.value.prompt,
      negativePrompt: draft.value.negativePrompt,
      images: draft.value.images,
      targets: selectedModels.value.map((model) => ({ providerId: model.providerId, modelId: model.id })),
      options: {
        size: draft.value.size,
        durationSeconds: config.value.video ? draft.value.durationSeconds : undefined,
        count: generationCount.value,
      },
    });
  } finally {
    runningGeneration.value = false;
  }
}

function adopt(task: TaskSnapshot) {
  if (!task.polishResult?.text) return;
  draft.value.prompt = task.polishResult.text;
  store.toast("已采用润色版本", "success");
}

function taskStatus(task: TaskSnapshot) {
  if (task.status === "succeeded") return "完成";
  if (task.status === "failed") return "失败";
  if (task.status === "canceled") return "已取消";
  if (task.status === "queued") return "排队";
  return "运行中";
}

function openPreview(task: TaskSnapshot) {
  const asset = task.result?.assets?.[0];
  if (!asset?.url) return;
  previewAsset.value = asset;
  previewTitle.value = `${task.providerName} · ${task.modelName}`;
}

let removeSourceImageListener: (() => void) | undefined;

onMounted(async () => {
  window.addEventListener("paste", handlePaste);
  removeSourceImageListener = await bridge.subscribeSourceImages(
    (images) => {
      if (config.value.source) addSourceImages(images);
    },
    (message) => store.toast(message, "error"),
  );
});
onBeforeUnmount(() => {
  window.removeEventListener("paste", handlePaste);
  removeSourceImageListener?.();
});
</script>

<template>
  <div class="workbench-layout">
    <section class="control-column">
      <div class="control-scroll">
        <div class="flow-step">
          <div class="step-heading"><span>01</span><div><strong>创作意图</strong><small>{{ config.description }}</small></div></div>
          <label class="visually-hidden" :for="`generation-prompt-${capability}`">生成提示词</label>
          <textarea :id="`generation-prompt-${capability}`" v-model="draft.prompt" class="prompt-input" rows="7" :placeholder="config.video ? '描述场景、动作、镜头运动与节奏' : '描述主体、环境、构图、光线与风格'" />
          <div class="inline-field">
            <label for="negative-prompt">排除内容</label>
            <input id="negative-prompt" v-model="draft.negativePrompt" placeholder="模糊、文字、水印" />
          </div>
        </div>

        <div v-if="config.source" class="flow-step">
          <div class="step-heading"><span>02</span><div><strong>来源图片</strong><small>PNG / JPEG / WebP · {{ draft.images.length }}/{{ maxSourceImageCount }}</small></div></div>
          <div v-if="draft.images.length" class="source-preview-list">
            <div v-for="(image, index) in draft.images" :key="`${image.name || 'image'}-${index}`" class="source-preview">
              <img :src="imagePreview(image)" :alt="image.name || `来源图片 ${index + 1}`" />
              <div><strong>{{ image.name || `来源图片 ${index + 1}` }}</strong><small>{{ image.mimeType }}</small></div>
              <button class="icon-button" type="button" title="移除图片" @click="draft.images.splice(index, 1)"><X :size="17" /></button>
            </div>
          </div>
          <div
            v-if="draft.images.length < maxSourceImageCount"
            id="source-image-drop"
            class="image-drop-zone"
            :class="{ compact: draft.images.length > 0 }"
            data-file-drop-target
            aria-label="来源图片拖放区域"
            @dragover.prevent
            @drop.prevent="handleDrop"
          >
            <Upload :size="22" />
            <strong>{{ draft.images.length ? '继续添加图片' : '拖入或粘贴图片' }}</strong>
            <div class="drop-actions">
              <button class="button secondary compact" type="button" @click="fileInput?.click()"><Paperclip :size="15" />选择文件</button>
              <button class="button ghost compact" type="button" @click="chooseNativeImages"><FileImage :size="15" />系统对话框</button>
              <button class="button ghost compact" type="button" @click="openFavoritePicker"><Bookmark :size="15" />收藏素材</button>
            </div>
          </div>
          <input ref="fileInput" class="visually-hidden" type="file" accept="image/png,image/jpeg,image/webp" :multiple="maxSourceImageCount > 1" @change="handleFiles(($event.target as HTMLInputElement).files)" />
        </div>

        <div class="flow-step polish-step">
          <button class="section-toggle" type="button" :aria-expanded="polishOpen" @click="polishOpen = !polishOpen">
            <div class="step-heading"><span>{{ config.source ? '03' : '02' }}</span><div><strong>提示词润色</strong><small>多模型候选</small></div></div>
            <WandSparkles :size="17" :class="{ active: polishOpen }" />
          </button>
          <div v-if="polishOpen" class="polish-body">
            <label class="visually-hidden" :for="`polish-template-${capability}`">润色模板</label>
            <select :id="`polish-template-${capability}`" v-model.number="draft.templateId">
              <option :value="0">自定义 / 默认规则</option>
              <option v-for="template in store.templates.filter((item) => item.enabled)" :key="template.id" :value="template.id">{{ template.name }}</option>
            </select>
            <div v-if="polishModels.length" class="target-list compact-targets">
              <button
                v-for="model in polishModels"
                :key="model.id"
                type="button"
                class="target-row"
                :class="{ selected: draft.polishTargetIds.includes(model.id) }"
                :aria-pressed="draft.polishTargetIds.includes(model.id)"
                @click="togglePolishTarget(model.id)"
              >
                <span class="check-box"><Check v-if="draft.polishTargetIds.includes(model.id)" :size="13" /></span>
                <span><strong>{{ model.name }}</strong><small>{{ providerName(model) }}</small></span>
              </button>
            </div>
            <div v-else class="inline-empty">没有标注“文本润色”能力的模型</div>
            <button class="button secondary full" type="button" :disabled="runningPolish || !draft.prompt.trim() || !selectedPolishModels.length" @click="runPolish">
              <LoaderCircle v-if="runningPolish" class="spin" :size="16" />
              <Sparkles v-else :size="16" />
              并发润色
            </button>
            <div v-if="polishTasks.length" class="polish-results">
              <article v-for="(task, index) in polishTasks" :key="task.id" class="polish-card">
                <div class="polish-card-head"><span class="proof-marker">{{ marker(index) }}</span><strong>{{ task.modelName }}</strong><span>{{ taskStatus(task) }}</span></div>
                <p v-if="task.polishResult?.text">{{ task.polishResult.text }}</p>
                <p v-else-if="task.errorMessage" class="error-copy">{{ task.errorMessage }}</p>
                <div v-else class="polish-loading"><LoaderCircle class="spin" :size="15" />{{ task.stage }}</div>
                <div class="polish-actions">
                  <button v-if="task.polishResult?.text" class="button ghost compact" type="button" @click="adopt(task)"><Check :size="14" />采用</button>
                  <button v-if="task.status === 'queued' || task.status === 'running'" class="icon-button" type="button" title="取消润色任务" @click="store.cancelTask(task.id)"><CircleStop :size="16" /></button>
                  <button v-if="task.status === 'failed' || task.status === 'canceled'" class="icon-button" type="button" title="重试润色任务" @click="store.retryTask(task.id)"><RotateCcw :size="16" /></button>
                </div>
              </article>
            </div>
          </div>
        </div>

        <div class="flow-step">
          <div class="step-heading"><span>{{ config.source ? '04' : '03' }}</span><div><strong>生成目标</strong><small>{{ selectedModels.length }} 个模型 · {{ generationCount }} 份</small></div></div>
          <div class="segmented-control" aria-label="生成模式">
            <button type="button" :aria-pressed="draft.mode === 'single'" :class="{ active: draft.mode === 'single' }" @click="draft.mode = 'single'">单模型</button>
            <button type="button" :aria-pressed="draft.mode === 'parallel'" :class="{ active: draft.mode === 'parallel' }" @click="draft.mode = 'parallel'">并发比较</button>
          </div>

          <div v-if="!hasProviders" class="action-empty">
            <Building2Icon />
            <strong>还没有可用供应商</strong>
            <button class="button secondary compact" type="button" @click="router.push('/providers')">添加供应商</button>
          </div>
          <div v-else-if="!generationModels.length" class="action-empty">
            <AlertCircle :size="20" />
            <strong>没有支持“{{ config.title }}”的模型</strong>
            <button class="button secondary compact" type="button" @click="router.push('/providers')">标注模型能力</button>
          </div>
          <div v-else class="target-list">
            <button
              v-for="model in generationModels"
              :key="model.id"
              type="button"
              class="target-row"
              :class="{ selected: draft.targetIds.includes(model.id) }"
              :aria-pressed="draft.targetIds.includes(model.id)"
              @click="toggleTarget(model)"
            >
              <span class="proof-marker muted">{{ selectedMarker(model.id) || '·' }}</span>
              <span><strong>{{ model.name }}</strong><small>{{ providerName(model) }} · {{ model.remoteId }}</small></span>
              <span class="selection-control" :class="draft.mode"></span>
            </button>
          </div>

          <div class="option-grid">
            <label><span>输出尺寸</span><select v-model="draft.size">
              <option v-if="!config.video" value="1024x1024">1024 × 1024</option>
              <option v-if="!config.video" value="1536x1024">1536 × 1024</option>
              <option v-if="!config.video" value="1024x1536">1024 × 1536</option>
              <option v-if="config.video" value="1280x720">1280 × 720</option>
              <option v-if="config.video" value="720x1280">720 × 1280</option>
            </select></label>
            <label v-if="config.video"><span>时长</span><select v-model.number="draft.durationSeconds"><option :value="5">5 秒</option><option :value="8">8 秒</option><option :value="10">10 秒</option><option :value="11">11 秒</option><option :value="12">12 秒</option></select></label>
            <label><span>生成数量</span><input v-model.number="draft.count" type="number" min="1" :max="maxGenerationCount" step="1" @change="normalizeGenerationCount" /></label>
          </div>
        </div>
      </div>

      <div class="run-bar">
        <div><span>{{ selectedModels.length ? `${totalTaskCount} 个任务` : '等待选择模型' }}</span><small>{{ config.title }} · {{ selectedModels.length }} 模型 × {{ generationCount }} 份</small></div>
        <button class="button primary run-button" type="button" :disabled="!canRun || runningGeneration" @click="runGeneration">
          <LoaderCircle v-if="runningGeneration" class="spin" :size="18" />
          <Play v-else :size="18" fill="currentColor" />
          开始生成
        </button>
      </div>
    </section>

    <section class="results-column">
      <div v-if="resultTasks.length" class="batch-rail">
        <div class="run-identity"><span>RUN</span><strong>{{ generationBatchId?.slice(0, 5).toUpperCase() }}</strong></div>
        <div class="rail-targets">
          <span v-for="(task, index) in resultTasks" :key="task.id" :class="`rail-${task.status}`"><b>{{ marker(index) }}</b>{{ taskStatus(task) }}</span>
        </div>
        <div class="rail-summary"><span>{{ batchStats.succeeded }} 完成</span><span>{{ batchStats.running }} 运行</span><span>{{ batchStats.failed }} 失败</span></div>
        <button v-if="batchStats.running" class="button ghost compact" type="button" @click="store.cancelBatch(generationBatchId!)"><CircleStop :size="15" />取消全部</button>
      </div>

      <div v-if="!resultTasks.length" class="results-empty">
        <div class="empty-media-strip"><span>A</span><span>B</span><span>C</span></div>
        <ImageIcon v-if="!config.video" :size="30" />
        <Film v-else :size="30" />
        <strong>等待第一批{{ config.title }}结果</strong>
        <span>选择模型后，结果会按供应商独立落位</span>
      </div>

      <div v-else class="result-grid">
        <article v-for="(task, index) in resultTasks" :key="task.id" class="result-card" :class="`status-${task.status}`">
          <header class="result-card-head">
            <span class="proof-marker">{{ marker(index) }}</span>
            <div><strong>{{ task.providerName }}</strong><small>{{ task.modelName }}</small></div>
            <span class="status-badge">{{ taskStatus(task) }}</span>
          </header>

          <div class="media-frame" :class="{ video: config.video }">
            <template v-if="task.status === 'succeeded' && task.result?.assets?.length">
              <video v-if="task.result.assets[0].type === 'video' && task.result.assets[0].url" :src="task.result.assets[0].url" controls preload="metadata"></video>
              <img v-else-if="task.result.assets[0].url" :src="task.result.assets[0].url" :alt="`${task.providerName} 生成结果`" />
              <div v-else class="media-placeholder"><Film :size="27" /><span>视频已保存</span></div>
            </template>
            <div v-else-if="task.status === 'failed'" class="media-placeholder error"><AlertCircle :size="28" /><span>此模型未返回结果</span></div>
            <div v-else-if="task.status === 'canceled'" class="media-placeholder"><CircleStop :size="26" /><span>任务已取消</span></div>
            <div v-else class="media-placeholder running"><LoaderCircle class="spin" :size="28" /><span>{{ task.stage || '等待供应商' }}</span></div>
          </div>

          <div v-if="task.status === 'queued' || task.status === 'running'" class="task-progress">
            <div><span>{{ task.stage }}</span><b>{{ task.progress || 0 }}%</b></div>
            <div class="progress-track" role="progressbar" :aria-label="`${task.modelName} 任务进度`" aria-valuemin="0" aria-valuemax="100" :aria-valuenow="task.progress || 0"><span :style="{ width: `${Math.max(4, task.progress || 4)}%` }"></span></div>
          </div>
          <p v-if="task.errorMessage" class="task-error">{{ task.errorMessage }}</p>
          <p v-if="task.cancelMode === 'local_only' && task.status === 'canceled'" class="task-note">仅停止本地等待，远端任务可能继续运行</p>

          <footer class="result-actions">
            <span class="task-time">{{ task.completedAt ? new Date(task.completedAt).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' }) : task.stage }}</span>
            <button v-if="task.status === 'queued' || task.status === 'running'" class="icon-button" type="button" title="取消任务" @click="store.cancelTask(task.id)"><CircleStop :size="17" /></button>
            <button v-if="task.status === 'succeeded' && task.result?.assets?.[0]?.url" class="icon-button" type="button" :title="task.result.assets[0].type === 'video' ? '播放视频' : '查看图片'" @click="openPreview(task)"><Play v-if="task.result.assets[0].type === 'video'" :size="17" fill="currentColor" /><Eye v-else :size="17" /></button>
            <button v-if="task.status === 'succeeded'" class="icon-button" type="button" title="下载结果" @click="store.downloadTask(task.id)"><Download :size="17" /></button>
            <button v-if="task.status === 'succeeded' || task.status === 'failed' || task.status === 'canceled'" class="icon-button" type="button" title="重新生成" @click="store.retryTask(task.id)"><RotateCcw :size="17" /></button>
          </footer>
        </article>
      </div>
    </section>
  </div>

  <Teleport to="body">
    <div v-if="favoritePickerOpen" class="favorite-picker-backdrop" role="presentation" @click.self="favoritePickerOpen = false">
      <section class="favorite-picker-dialog" role="dialog" aria-modal="true" aria-labelledby="favorite-picker-title">
        <header>
          <div><strong id="favorite-picker-title">收藏素材</strong><span>{{ favoriteSources.length }} 个可用结果</span></div>
          <button class="icon-button" type="button" title="关闭" @click="favoritePickerOpen = false"><X :size="18" /></button>
        </header>
        <div v-if="favoriteLoading" class="favorite-picker-empty"><LoaderCircle class="spin" :size="24" /><span>正在读取收藏</span></div>
        <div v-else-if="!favoriteSources.length" class="favorite-picker-empty"><Bookmark :size="25" /><span>收藏中没有可用媒体</span></div>
        <div v-else class="favorite-source-grid">
          <button v-for="item in favoriteSources" :key="item.id" class="favorite-source-item" type="button" :disabled="favoriteConvertingId !== null" @click="chooseFavoriteSource(item)">
            <span class="favorite-source-media">
              <video v-if="item.result?.assets?.[0]?.type === 'video'" :src="item.result.assets[0].url" muted preload="metadata"></video>
              <img v-else :src="item.result?.assets?.[0]?.url" :alt="item.request.prompt" />
              <b>{{ item.result?.assets?.[0]?.type === 'video' ? '视频首帧' : '图片' }}</b>
            </span>
            <span><strong>{{ item.modelName }}</strong><small>{{ favoriteConvertingId === item.id ? '正在处理' : item.providerName }}</small></span>
          </button>
        </div>
      </section>
    </div>
  </Teleport>

  <MediaPreviewDialog v-if="previewAsset" :asset="previewAsset" :title="previewTitle" @close="previewAsset = null" />
</template>

<script lang="ts">
import { Building2 } from "lucide-vue-next";
export default { components: { Building2Icon: Building2 } };
</script>
