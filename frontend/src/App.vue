<script setup lang="ts">
import { computed, onBeforeUnmount } from "vue";
import { useRoute } from "vue-router";
import {
  Bookmark,
  Building2,
  Clock3,
  FileImage,
  Film,
  Images,
  PanelsTopLeft,
  Sparkles,
  WandSparkles,
} from "lucide-vue-next";
import ToastViewport from "./components/ToastViewport.vue";
import { useAppStore } from "./stores/app";

const route = useRoute();
const store = useAppStore();

const titles: Record<string, string> = {
  text_to_image: "文生图",
  text_to_video: "文生视频",
  image_to_image: "图生图",
  image_to_video: "图生视频",
};

const pageTitle = computed(() => {
  if (route.path.startsWith("/generate/")) return titles[String(route.params.capability)] || "生成工作台";
  if (route.path === "/providers") return "供应商与模型";
  if (route.path === "/templates") return "润色模板";
  if (route.path === "/favorites") return "收藏结果";
  return "生成历史";
});

onBeforeUnmount(() => store.dispose());
</script>

<template>
  <div class="app-frame">
    <aside class="side-nav">
      <div class="brand-block">
        <div class="brand-mark"><PanelsTopLeft :size="18" /></div>
        <div>
          <strong>星流创作台</strong>
          <span>STUDIO 01</span>
        </div>
      </div>

      <nav class="nav-groups" aria-label="主导航">
        <div class="nav-group">
          <p class="nav-label">生成</p>
          <RouterLink to="/generate/text_to_image" aria-label="文生图" title="文生图"><FileImage :size="17" /><span>文生图</span></RouterLink>
          <RouterLink to="/generate/text_to_video" aria-label="文生视频" title="文生视频"><Film :size="17" /><span>文生视频</span></RouterLink>
          <RouterLink to="/generate/image_to_image" aria-label="图生图" title="图生图"><Images :size="17" /><span>图生图</span></RouterLink>
          <RouterLink to="/generate/image_to_video" aria-label="图生视频" title="图生视频"><Sparkles :size="17" /><span>图生视频</span></RouterLink>
        </div>
        <div class="nav-group">
          <p class="nav-label">管理</p>
          <RouterLink to="/providers" aria-label="供应商" title="供应商"><Building2 :size="17" /><span>供应商</span></RouterLink>
          <RouterLink to="/templates" aria-label="润色模板" title="润色模板"><WandSparkles :size="17" /><span>润色模板</span></RouterLink>
          <RouterLink to="/history" aria-label="历史" title="历史"><Clock3 :size="17" /><span>历史</span></RouterLink>
          <RouterLink to="/favorites" aria-label="收藏" title="收藏"><Bookmark :size="17" /><span>收藏</span></RouterLink>
        </div>
      </nav>

      <div class="nav-status">
        <span class="status-dot" :class="{ active: store.activeTaskCount > 0 }"></span>
        <span>{{ store.activeTaskCount ? `${store.activeTaskCount} 个任务运行中` : "任务队列空闲" }}</span>
      </div>
    </aside>

    <main class="app-main">
      <header class="context-bar">
        <div>
          <span class="context-kicker">OLDWANG / {{ route.path.includes('generate') ? 'CREATE' : 'LIBRARY' }}</span>
          <h1>{{ pageTitle }}</h1>
        </div>
        <div class="context-meta">
          <span>{{ store.providers.filter((item) => item.enabled).length }} 个供应商启用</span>
          <span>{{ store.models.filter((item) => item.enabled).length }} 个模型可用</span>
        </div>
      </header>
      <RouterView />
    </main>

    <ToastViewport />
  </div>
</template>
