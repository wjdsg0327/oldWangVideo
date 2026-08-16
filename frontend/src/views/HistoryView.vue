<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { Bookmark, BookmarkCheck, Download, Eye, Film, Image as ImageIcon, Play, RefreshCw, Search, Trash2 } from "lucide-vue-next";
import MediaPreviewDialog from "../components/MediaPreviewDialog.vue";
import { useAppStore } from "../stores/app";
import type { Capability, GenerationHistory, MediaAsset } from "../types";

const props = defineProps<{ favoriteOnly: boolean }>();
const store = useAppStore();
const search = ref("");
const capability = ref<Capability | "">("");
const selectedIds = ref<Set<number>>(new Set());
const previewAsset = ref<MediaAsset | null>(null);
const previewTitle = ref("");

const title = computed(() => props.favoriteOnly ? "收藏结果" : "生成历史");
const emptyText = computed(() => props.favoriteOnly ? "还没有收藏结果" : "还没有生成记录");

const capabilityLabels: Record<Capability, string> = {
  text_to_image: "文生图",
  text_to_video: "文生视频",
  image_to_image: "图生图",
  image_to_video: "图生视频",
  prompt_polish: "文本润色",
};

async function load(page = 1) {
  selectedIds.value = new Set();
  await store.loadHistory({ page, favoriteOnly: props.favoriteOnly, capability: capability.value || undefined, search: search.value.trim() || undefined });
}

const selectedCount = computed(() => selectedIds.value.size);
const allVisibleSelected = computed(() => store.historyPage.items.length > 0 && store.historyPage.items.every((item) => selectedIds.value.has(item.id)));

function toggleSelection(historyId: number) {
  const next = new Set(selectedIds.value);
  if (next.has(historyId)) next.delete(historyId);
  else next.add(historyId);
  selectedIds.value = next;
}

function toggleSelectAll() {
  const next = new Set(selectedIds.value);
  if (allVisibleSelected.value) store.historyPage.items.forEach((item) => next.delete(item.id));
  else store.historyPage.items.forEach((item) => next.add(item.id));
  selectedIds.value = next;
}

async function removeSelected() {
  const ids = [...selectedIds.value];
  if (!ids.length) return;
  if (!window.confirm(`删除选中的 ${ids.length} 条历史记录？本地媒体文件会保留。`)) return;
  if (!await store.removeHistories(ids)) return;
  selectedIds.value = new Set();
  const pageCount = Math.max(1, Math.ceil(store.historyPage.total / store.historyPage.pageSize));
  await load(Math.min(store.historyPage.page, pageCount));
}

async function toggleFavorite(item: GenerationHistory) {
  const nextFavorite = !item.favorite;
  const changed = await store.toggleFavorite(item.id, nextFavorite);
  if (changed && props.favoriteOnly && !nextFavorite) {
    store.historyPage.items = store.historyPage.items.filter((history) => history.id !== item.id);
    const next = new Set(selectedIds.value);
    next.delete(item.id);
    selectedIds.value = next;
  }
}

async function remove(item: GenerationHistory) {
  if (!window.confirm("删除这条生成记录？本地媒体文件会保留。")) return;
  if (await store.removeHistory(item.id)) {
    const next = new Set(selectedIds.value);
    next.delete(item.id);
    selectedIds.value = next;
  }
}

function openPreview(item: GenerationHistory) {
  const asset = item.result?.assets?.[0];
  if (!asset?.url) return;
  previewAsset.value = asset;
  previewTitle.value = `${item.providerName} · ${item.modelName}`;
}

watch(() => props.favoriteOnly, () => load());
onMounted(() => load());
</script>

<template>
  <div class="history-page">
    <div class="history-toolbar">
      <div><strong>{{ title }}</strong><span>{{ store.historyPage.total }} 条记录</span></div>
      <div class="history-toolbar-actions">
        <div class="history-selection-actions">
          <label class="history-select-all"><input type="checkbox" :checked="allVisibleSelected" :indeterminate="selectedCount > 0 && !allVisibleSelected" @change="toggleSelectAll" /><span>全选本页</span></label>
          <button class="button danger compact" type="button" :disabled="selectedCount === 0" @click="removeSelected"><Trash2 :size="15" />删除选中<span v-if="selectedCount">({{ selectedCount }})</span></button>
        </div>
        <div class="history-filters">
          <label class="search-field"><Search :size="16" /><input v-model="search" placeholder="搜索提示词、供应商或模型" @keyup.enter="load()" /></label>
          <select v-model="capability" @change="load()"><option value="">全部模块</option><option value="text_to_image">文生图</option><option value="text_to_video">文生视频</option><option value="image_to_image">图生图</option><option value="image_to_video">图生视频</option></select>
          <button class="icon-button" type="button" title="刷新" @click="load()"><RefreshCw :class="{ spin: store.historyLoading }" :size="17" /></button>
        </div>
      </div>
    </div>

    <div v-if="store.historyLoading && !store.historyPage.items.length" class="history-empty"><RefreshCw class="spin" :size="26" /><strong>正在读取记录</strong></div>
    <div v-else-if="!store.historyPage.items.length" class="history-empty"><Bookmark v-if="favoriteOnly" :size="29" /><ImageIcon v-else :size="29" /><strong>{{ emptyText }}</strong><RouterLink class="button secondary compact" to="/generate/text_to_image">开始生成</RouterLink></div>
    <div v-else class="history-grid">
      <article v-for="item in store.historyPage.items" :key="item.id" class="history-card">
        <label class="history-select" @click.stop><input type="checkbox" :checked="selectedIds.has(item.id)" @change="toggleSelection(item.id)" /><span class="visually-hidden">选择这条历史记录</span></label>
        <div class="history-media">
          <template v-if="item.result?.assets?.[0]?.url">
            <video v-if="item.result.assets[0].type === 'video'" :src="item.result.assets[0].url" controls preload="metadata"></video>
            <img v-else :src="item.result.assets[0].url" :alt="item.request.prompt" />
          </template>
          <div v-else><Film v-if="item.capability.includes('video')" :size="26" /><ImageIcon v-else :size="26" /><span>{{ item.status === 'failed' ? '生成失败' : '无预览' }}</span></div>
          <span class="history-module">{{ capabilityLabels[item.capability] }}</span>
        </div>
        <div class="history-body">
          <p>{{ item.request.prompt }}</p>
          <div><span>{{ item.providerName }}</span><code>{{ item.modelName }}</code></div>
          <small>{{ new Date(item.createdAt).toLocaleString('zh-CN') }}</small>
        </div>
        <footer>
          <button v-if="item.status === 'succeeded' && item.result?.assets?.[0]?.url" class="icon-button" type="button" :title="item.result.assets[0].type === 'video' ? '播放视频' : '查看图片'" @click="openPreview(item)"><Play v-if="item.result.assets[0].type === 'video'" :size="17" fill="currentColor" /><Eye v-else :size="17" /></button>
          <button class="icon-button" type="button" :title="item.favorite ? '取消收藏' : '收藏'" @click="toggleFavorite(item)"><BookmarkCheck v-if="item.favorite" :size="17" /><Bookmark v-else :size="17" /></button>
          <button v-if="item.status === 'succeeded'" class="icon-button" type="button" title="下载" @click="store.downloadTask(item.taskId)"><Download :size="17" /></button>
          <button class="icon-button danger" type="button" title="删除记录" @click="remove(item)"><Trash2 :size="16" /></button>
        </footer>
      </article>
    </div>

    <div v-if="store.historyPage.total > store.historyPage.pageSize" class="pagination">
      <button class="button ghost compact" type="button" :disabled="store.historyPage.page <= 1" @click="load(store.historyPage.page - 1)">上一页</button>
      <span>{{ store.historyPage.page }} / {{ Math.ceil(store.historyPage.total / store.historyPage.pageSize) }}</span>
      <button class="button ghost compact" type="button" :disabled="store.historyPage.page >= Math.ceil(store.historyPage.total / store.historyPage.pageSize)" @click="load(store.historyPage.page + 1)">下一页</button>
    </div>

    <MediaPreviewDialog v-if="previewAsset" :asset="previewAsset" :title="previewTitle" @close="previewAsset = null" />
  </div>
</template>
