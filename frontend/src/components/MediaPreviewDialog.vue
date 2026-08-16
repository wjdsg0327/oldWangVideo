<script setup lang="ts">
import { Film, X } from "lucide-vue-next";
import type { MediaAsset } from "../types";

defineProps<{
  asset: MediaAsset;
  title?: string;
}>();

defineEmits<{
  close: [];
}>();
</script>

<template>
  <Teleport to="body">
    <div class="media-preview-backdrop" role="presentation" @click.self="$emit('close')">
      <section class="media-preview-dialog" role="dialog" aria-modal="true" aria-label="媒体预览">
        <header class="media-preview-header">
          <div>
            <strong>{{ title || (asset.type === 'video' ? '视频预览' : '图片预览') }}</strong>
            <span>{{ asset.type === 'video' ? '播放视频' : '查看原图' }}</span>
          </div>
          <button class="icon-button" type="button" title="关闭预览" @click="$emit('close')"><X :size="18" /></button>
        </header>
        <div class="media-preview-content">
          <video v-if="asset.type === 'video'" :src="asset.url" controls autoplay playsinline></video>
          <img v-else :src="asset.url" :alt="title || '图片预览'" />
          <div v-if="!asset.url" class="media-placeholder"><Film :size="28" /><span>媒体地址不可用</span></div>
        </div>
      </section>
    </div>
  </Teleport>
</template>
