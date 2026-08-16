<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import {
  CheckCircle2,
  CircleAlert,
  CloudDownload,
  Eye,
  EyeOff,
  KeyRound,
  Plus,
  RefreshCw,
  Save,
  Server,
  Trash2,
  X,
} from "lucide-vue-next";
import { useAppStore } from "../stores/app";
import type { Capability, Model, Provider } from "../types";

const store = useAppStore();
const selectedId = ref<number | null>(null);
const editing = ref(false);
const saving = ref(false);
const testing = ref(false);
const syncing = ref(false);
const keyVisible = ref(false);
const manualOpen = ref(false);
const connection = ref<{ success: boolean; latencyMs?: number; message: string } | null>(null);

const form = reactive({ name: "", baseUrl: "", apiKey: "", enabled: true, presetId: "" });
const manual = reactive({ remoteId: "", name: "", capabilities: [] as Capability[], enabled: true });

const selectedProvider = computed(() => store.providers.find((provider) => provider.id === selectedId.value) || null);
const providerModels = computed(() => store.models.filter((model) => model.providerId === selectedId.value));

const capabilityOptions: { value: Capability; label: string }[] = [
  { value: "text_to_image", label: "文生图" },
  { value: "text_to_video", label: "文生视频" },
  { value: "image_to_image", label: "图生图" },
  { value: "image_to_video", label: "图生视频" },
  { value: "prompt_polish", label: "文本润色" },
];

function beginNew() {
  selectedId.value = null;
  Object.assign(form, { name: "", baseUrl: "", apiKey: "", enabled: true, presetId: "" });
  connection.value = null;
  editing.value = true;
}

function beginEdit(provider: Provider) {
  selectedId.value = provider.id;
  Object.assign(form, { name: provider.name, baseUrl: provider.baseUrl, apiKey: "", enabled: provider.enabled, presetId: "" });
  connection.value = null;
  editing.value = true;
}

function applyPreset() {
  const preset = store.presets.find((item) => item.id === form.presetId);
  if (!preset) return;
  form.name = preset.name;
  form.baseUrl = preset.baseUrl;
}

async function saveProvider() {
  if (!form.name.trim() || !form.baseUrl.trim()) {
    store.toast("请填写供应商名称和 Base URL", "error");
    return;
  }
  saving.value = true;
  try {
    const provider = await store.saveProvider(selectedId.value, { name: form.name, baseUrl: form.baseUrl, apiKey: form.apiKey, enabled: form.enabled });
    selectedId.value = provider.id;
    form.apiKey = "";
  } finally {
    saving.value = false;
  }
}

async function removeProvider() {
  if (!selectedId.value || !window.confirm("删除供应商及其模型？历史结果会保留。")) return;
  const removed = await store.removeProvider(selectedId.value);
  if (!removed) return;
  editing.value = false;
  selectedId.value = null;
}

async function testConnection() {
  if (!selectedId.value) return;
  testing.value = true;
  connection.value = null;
  try {
    const result = await store.testProvider(selectedId.value);
    connection.value = result;
  } catch (error) {
    connection.value = { success: false, message: error instanceof Error ? error.message : "连接失败" };
  } finally {
    testing.value = false;
  }
}

async function syncModels() {
  if (!selectedId.value) return;
  syncing.value = true;
  try {
    await store.refreshProviderModels(selectedId.value);
  } finally {
    syncing.value = false;
  }
}

async function toggleCapability(model: Model, capability: Capability) {
  const capabilities = model.capabilities.includes(capability)
    ? model.capabilities.filter((item) => item !== capability)
    : [...model.capabilities, capability];
  await store.saveModel(model.id, { providerId: model.providerId, remoteId: model.remoteId, name: model.name, capabilities, enabled: model.enabled });
}

async function toggleModel(model: Model) {
  await store.saveModel(model.id, { providerId: model.providerId, remoteId: model.remoteId, name: model.name, capabilities: model.capabilities, enabled: !model.enabled });
}

async function removeModel(model: Model) {
  if (!window.confirm(`删除模型“${model.name}”？`)) return;
  await store.removeModel(model.id);
}

function toggleManualCapability(capability: Capability) {
  const index = manual.capabilities.indexOf(capability);
  if (index >= 0) manual.capabilities.splice(index, 1);
  else manual.capabilities.push(capability);
}

async function addManualModel() {
  if (!selectedId.value || !manual.remoteId.trim()) {
    store.toast("请填写模型 ID", "error");
    return;
  }
  const model = await store.saveModel(null, { providerId: selectedId.value, remoteId: manual.remoteId, name: manual.name || manual.remoteId, capabilities: manual.capabilities, enabled: manual.enabled });
  if (!model) return;
  Object.assign(manual, { remoteId: "", name: "", capabilities: [], enabled: true });
  manualOpen.value = false;
  store.toast("模型已添加", "success");
}

onMounted(() => {
  if (store.providers.length) beginEdit(store.providers[0]);
  else beginNew();
});
</script>

<template>
  <div class="management-page provider-page">
    <section class="management-list">
      <div class="section-toolbar">
        <div><strong>供应商</strong><span>{{ store.providers.length }} 项配置</span></div>
        <button class="button primary compact" type="button" @click="beginNew"><Plus :size="15" />新增</button>
      </div>

      <div v-if="!store.providers.length" class="management-empty">
        <Server :size="28" />
        <strong>还没有供应商</strong>
        <button class="button secondary compact" type="button" @click="beginNew">添加第一项</button>
      </div>
      <div v-else class="provider-list">
        <button v-for="provider in store.providers" :key="provider.id" type="button" class="provider-row" :class="{ active: selectedId === provider.id }" @click="beginEdit(provider)">
          <span class="provider-symbol">{{ provider.name.slice(0, 1).toUpperCase() }}</span>
          <span class="provider-copy"><strong>{{ provider.name }}</strong><small>{{ provider.baseUrl }}</small></span>
          <span class="provider-health" :class="{ enabled: provider.enabled }">{{ provider.enabled ? "启用" : "停用" }}</span>
        </button>
      </div>
    </section>

    <section v-if="editing" class="management-editor">
      <div class="editor-header">
        <div><span>{{ selectedProvider ? "EDIT PROVIDER" : "NEW PROVIDER" }}</span><h2>{{ selectedProvider?.name || "添加供应商" }}</h2></div>
        <button class="icon-button" type="button" title="关闭" @click="editing = false"><X :size="18" /></button>
      </div>

      <div class="editor-scroll">
        <form class="form-section" @submit.prevent="saveProvider">
          <div class="form-section-title"><strong>基础配置</strong><span>3 个必需项</span></div>
          <label class="field"><span>预设模板</span><select v-model="form.presetId" @change="applyPreset"><option value="">手动配置</option><option v-for="preset in store.presets" :key="preset.id" :value="preset.id">{{ preset.name }}{{ preset.local ? " · 本地" : "" }}</option></select></label>
          <label class="field"><span>供应商名称</span><input v-model="form.name" placeholder="例如：硅基流动" /></label>
          <label class="field"><span>Base URL</span><input v-model="form.baseUrl" placeholder="https://api.example.com/v1" /></label>
          <label class="field"><span>API Key</span><div class="input-with-action"><KeyRound :size="16" /><input v-model="form.apiKey" :type="keyVisible ? 'text' : 'password'" autocomplete="new-password" :placeholder="selectedProvider?.hasApiKey ? '已保存，留空则不修改' : '本地服务可留空'" /><button type="button" :title="keyVisible ? '隐藏密钥' : '显示密钥'" @click="keyVisible = !keyVisible"><EyeOff v-if="keyVisible" :size="16" /><Eye v-else :size="16" /></button></div></label>
          <label class="toggle-field"><span><strong>启用供应商</strong><small>停用后不会出现在生成目标中</small></span><input v-model="form.enabled" type="checkbox" /><i></i></label>
          <div class="form-actions">
            <button v-if="selectedProvider" class="button danger ghost" type="button" @click="removeProvider"><Trash2 :size="15" />删除</button>
            <button class="button primary" type="submit" :disabled="saving"><RefreshCw v-if="saving" class="spin" :size="16" /><Save v-else :size="16" />保存配置</button>
          </div>
        </form>

        <div v-if="selectedProvider" class="form-section connectivity-section">
          <div class="form-section-title"><strong>连接</strong><span>/models</span></div>
          <div class="connection-row">
            <div v-if="connection" class="connection-result" :class="{ success: connection.success }">
              <CheckCircle2 v-if="connection.success" :size="18" />
              <CircleAlert v-else :size="18" />
              <span><strong>{{ connection.success ? "连接正常" : "连接失败" }}</strong><small>{{ connection.message }}<template v-if="connection.latencyMs"> · {{ connection.latencyMs }} ms</template></small></span>
            </div>
            <div v-else class="connection-result neutral"><Server :size="18" /><span><strong>尚未检测</strong><small>验证凭据与模型接口</small></span></div>
            <button class="button secondary compact" type="button" :disabled="testing" @click="testConnection"><RefreshCw :class="{ spin: testing }" :size="15" />测试连接</button>
            <button class="button secondary compact" type="button" :disabled="syncing" @click="syncModels"><CloudDownload :class="{ spin: syncing }" :size="15" />拉取模型</button>
          </div>
        </div>

        <div v-if="selectedProvider" class="form-section model-section">
          <div class="form-section-title"><div><strong>模型与能力</strong><span>{{ providerModels.length }} 个模型</span></div><button class="button ghost compact" type="button" @click="manualOpen = !manualOpen"><Plus :size="15" />手动添加</button></div>

          <div v-if="manualOpen" class="manual-model-form">
            <label class="field"><span>模型 ID</span><input v-model="manual.remoteId" placeholder="provider/model-name" /></label>
            <label class="field"><span>显示名称</span><input v-model="manual.name" placeholder="留空使用模型 ID" /></label>
            <div class="capability-picker"><span>支持能力</span><button v-for="item in capabilityOptions" :key="item.value" type="button" :aria-pressed="manual.capabilities.includes(item.value)" :class="{ active: manual.capabilities.includes(item.value) }" @click="toggleManualCapability(item.value)">{{ item.label }}</button></div>
            <div class="form-actions"><button class="button ghost compact" type="button" @click="manualOpen = false">取消</button><button class="button primary compact" type="button" @click="addManualModel">添加模型</button></div>
          </div>

          <div v-if="!providerModels.length" class="inline-empty model-empty">拉取模型，或手动添加模型 ID</div>
          <div v-else class="model-table">
            <div class="model-table-head"><span>模型</span><span>能力标注</span><span>状态</span><span></span></div>
            <div v-for="model in providerModels" :key="model.id" class="model-table-row">
              <div class="model-identity"><strong>{{ model.name }}</strong><code>{{ model.remoteId }}</code></div>
              <div class="capability-grid">
                <button v-for="item in capabilityOptions" :key="item.value" type="button" :aria-pressed="model.capabilities.includes(item.value)" :class="{ active: model.capabilities.includes(item.value) }" @click="toggleCapability(model, item.value)">{{ item.label }}</button>
              </div>
              <button class="mini-toggle" type="button" :class="{ active: model.enabled }" :title="model.enabled ? '停用模型' : '启用模型'" @click="toggleModel(model)"><span></span></button>
              <button class="icon-button danger" type="button" title="删除模型" @click="removeModel(model)"><Trash2 :size="15" /></button>
            </div>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>
