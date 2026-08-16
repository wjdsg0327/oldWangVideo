<script setup lang="ts">
import { CheckCircle2, CircleAlert, Info } from "lucide-vue-next";
import { useAppStore } from "../stores/app";

const store = useAppStore();
</script>

<template>
  <div class="toast-viewport" aria-live="polite">
    <TransitionGroup name="toast">
      <div v-for="toast in store.toasts" :key="toast.id" class="toast-item" :class="`toast-${toast.tone}`">
        <CheckCircle2 v-if="toast.tone === 'success'" :size="17" />
        <CircleAlert v-else-if="toast.tone === 'error'" :size="17" />
        <Info v-else :size="17" />
        <span>{{ toast.text }}</span>
      </div>
    </TransitionGroup>
  </div>
</template>
