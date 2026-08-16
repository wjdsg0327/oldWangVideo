<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { FilePenLine, Plus, Save, Sparkles, Trash2 } from "lucide-vue-next";
import { useAppStore } from "../stores/app";
import type { PromptTemplate } from "../types";

const store = useAppStore();
const selectedId = ref<number | null>(null);
const saving = ref(false);
const form = reactive({ name: "", systemPrompt: "", enabled: true });
const selected = computed(() => store.templates.find((template) => template.id === selectedId.value) || null);

function selectTemplate(template: PromptTemplate) {
  selectedId.value = template.id;
  Object.assign(form, { name: template.name, systemPrompt: template.systemPrompt, enabled: template.enabled });
}

function beginNew() {
  selectedId.value = null;
  Object.assign(form, { name: "", systemPrompt: "", enabled: true });
}

async function save() {
  if (!form.name.trim() || !form.systemPrompt.trim()) {
    store.toast("请填写模板名称和系统提示词", "error");
    return;
  }
  saving.value = true;
  try {
    const template = await store.saveTemplate(selectedId.value, form);
    if (template) selectTemplate(template);
  } finally {
    saving.value = false;
  }
}

async function remove() {
  if (!selectedId.value || !window.confirm(`删除模板“${selected.value?.name}”？`)) return;
  const removed = await store.removeTemplate(selectedId.value);
  if (!removed) return;
  beginNew();
}

onMounted(() => {
  if (store.templates.length) selectTemplate(store.templates[0]);
  else beginNew();
});
</script>

<template>
  <div class="management-page template-page">
    <section class="management-list">
      <div class="section-toolbar"><div><strong>风格模板</strong><span>{{ store.templates.length }} 项</span></div><button class="button primary compact" type="button" @click="beginNew"><Plus :size="15" />新增</button></div>
      <div v-if="!store.templates.length" class="management-empty"><Sparkles :size="28" /><strong>还没有润色模板</strong><button class="button secondary compact" type="button" @click="beginNew">新建模板</button></div>
      <div v-else class="template-list">
        <button v-for="template in store.templates" :key="template.id" type="button" class="template-row" :class="{ active: selectedId === template.id }" @click="selectTemplate(template)">
          <FilePenLine :size="18" />
          <span><strong>{{ template.name }}</strong><small>{{ template.systemPrompt }}</small></span>
          <i :class="{ enabled: template.enabled }"></i>
        </button>
      </div>
    </section>

    <section class="management-editor template-editor">
      <div class="editor-header"><div><span>{{ selected ? 'EDIT TEMPLATE' : 'NEW TEMPLATE' }}</span><h2>{{ selected?.name || "新建润色模板" }}</h2></div></div>
      <div class="editor-scroll">
        <div class="form-section">
          <label class="field"><span>模板名称</span><input v-model="form.name" placeholder="例如：电影感镜头" /></label>
          <label class="field"><span>系统提示词</span><textarea v-model="form.systemPrompt" rows="13" placeholder="说明如何保留用户意图，以及需要补充的视觉细节。要求模型只输出润色后的提示词。"></textarea></label>
          <label class="toggle-field"><span><strong>启用模板</strong><small>停用后不会出现在生成工作台</small></span><input v-model="form.enabled" type="checkbox" /><i></i></label>
          <div class="template-preview">
            <span>PREVIEW</span>
            <p>{{ form.systemPrompt || "系统提示词预览" }}</p>
          </div>
          <div class="form-actions">
            <button v-if="selected" class="button danger ghost" type="button" @click="remove"><Trash2 :size="15" />删除</button>
            <button class="button primary" type="button" :disabled="saving" @click="save"><Save :size="16" />保存模板</button>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>
