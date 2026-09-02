<script setup lang="ts">
import { ref } from "vue";
import { AlertCircle, Check, Copy, X } from "lucide-vue-next";

const props = defineProps<{
  prompt: string;
  title?: string;
}>();

defineEmits<{
  close: [];
}>();

const copyState = ref<"idle" | "success" | "error">("idle");

async function copyPrompt() {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(props.prompt);
    } else {
      const textarea = document.createElement("textarea");
      textarea.value = props.prompt;
      textarea.style.position = "fixed";
      textarea.style.opacity = "0";
      document.body.appendChild(textarea);
      try {
        textarea.select();
        if (!document.execCommand("copy")) throw new Error("复制失败");
      } finally {
        textarea.remove();
      }
    }
    copyState.value = "success";
    window.setTimeout(() => { copyState.value = "idle"; }, 1800);
  } catch {
    copyState.value = "error";
    window.setTimeout(() => { copyState.value = "idle"; }, 1800);
  }
}
</script>

<template>
  <Teleport to="body">
    <div class="prompt-preview-backdrop" role="presentation" @click.self="$emit('close')">
      <section class="prompt-preview-dialog" role="dialog" aria-modal="true" aria-labelledby="prompt-preview-title">
        <header class="prompt-preview-header">
          <div>
            <strong id="prompt-preview-title">提示词</strong>
            <span>{{ title || "生成提示词" }}</span>
          </div>
          <button class="icon-button" type="button" title="关闭提示词" @click="$emit('close')"><X :size="18" /></button>
        </header>
        <div class="prompt-preview-content">
          <p>{{ prompt }}</p>
        </div>
        <footer class="prompt-preview-footer">
          <button class="button primary compact" type="button" @click="copyPrompt">
            <Check v-if="copyState === 'success'" :size="15" />
            <AlertCircle v-else-if="copyState === 'error'" :size="15" />
            <Copy v-else :size="15" />
            {{ copyState === 'success' ? "已复制" : copyState === 'error' ? "复制失败" : "一键复制" }}
          </button>
        </footer>
      </section>
    </div>
  </Teleport>
</template>
