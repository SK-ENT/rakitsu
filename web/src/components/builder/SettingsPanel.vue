<script setup lang="ts">
import { ref, watch, computed, nextTick } from 'vue';
import type { Settings, ProviderDefinition, PricingConfig } from '../../types';
import ComboBox from './ComboBox.vue';
import { generateEntityId } from '../../utils/entityId';
import { useWallpaperPrefs } from '../../composables/useWallpaperPrefs';

const { enabled: wallpaperEnabled, shaderIndex: wallpaperShader, dim: wallpaperDim, shaderCount: wallpaperCount } = useWallpaperPrefs();
const WALLPAPER_LABELS = ['Circuit Flow', 'Plasma Lattice', 'Volumetric Fog', 'Voronoi Cells', 'Aurora Waves', 'Water Ripple', 'Suminagashi'];
const showAppearance = ref(false);

function pickWallpaper(i: number) {
  wallpaperShader.value = i;
  wallpaperEnabled.value = true;
}

const props = defineProps<{
  settings: Settings;
  version?: string;
  description?: string;
  interactive?: boolean;
}>();

const emit = defineEmits<{
  (e: 'update', settings: Settings): void;
  (e: 'close'): void;
  (e: 'provider-renamed', providerId: string, oldName: string, newName: string): void;
  (e: 'apply-provider-to-all', providerName: string, defaultModel: string): void;
  (e: 'update:version', val: string): void;
  (e: 'update:description', val: string): void;
  (e: 'update:interactive', val: boolean): void;
}>();

const localSettings = ref<Settings>(JSON.parse(JSON.stringify(props.settings)));
// The Hub/Server section binds directly into localSettings.server — it must
// exist before the first render, not just lazily on focus (see ensureServer
// below), or expanding that section on settings with no server config throws.
ensureServer();
// Backfill IDs for providers that don't have them
setTimeout(() => ensureProviderIds(), 0);

// --- Saved Provider Library (localStorage) ---
const SAVED_PROVIDERS_KEY = 'rakitsu-saved-providers';
const showSavedProviders = ref(false);

interface SavedProvider {
  name: string;
  _id: string;
  def: ProviderDefinition;
}

// A base_url can carry a credential itself (https://user:pass@host/v1 or
// ...?api_key=...). Before anything goes to localStorage, drop userinfo,
// the fragment, and query parameters whose name looks like a credential.
// Other parameters stay: some endpoints need them (Azure's api-version).
// A ${VAR} reference or unparsable value is kept as-is.
const CREDENTIAL_PARAM = /key|token|secret|pass|auth|sig|cred/i;

function stripUrlCredentials(url: string | undefined): string | undefined {
  if (!url || url.startsWith('${')) return url;
  try {
    const u = new URL(url);
    u.username = '';
    u.password = '';
    u.hash = '';
    for (const name of [...u.searchParams.keys()]) {
      if (CREDENTIAL_PARAM.test(name)) u.searchParams.delete(name);
    }
    return u.toString();
  } catch { return url; }
}

function getSavedProviders(): SavedProvider[] {
  try {
    const raw = localStorage.getItem(SAVED_PROVIDERS_KEY);
    const saved: SavedProvider[] = raw ? JSON.parse(raw) : [];
    const cleaned = saved.map(s => ({ ...s, def: { ...s.def, base_url: stripUrlCredentials(s.def?.base_url) } }));
    // Entries saved by older versions may still hold credentials in
    // base_url — write the cleaned list back, not just return it.
    // A failed write (quota, read-only storage) must not hide the list we
    // already read, so it gets its own try.
    const out = JSON.stringify(cleaned);
    if (raw && out !== raw) {
      try { localStorage.setItem(SAVED_PROVIDERS_KEY, out); } catch { /* keep the in-memory list */ }
    }
    return cleaned;
  } catch { return []; }
}

function saveProviderToLibrary(name: string, def: ProviderDefinition) {
  const saved = getSavedProviders();
  const id = def._id || generateEntityId();
  // Update if same _id exists, otherwise add
  const idx = saved.findIndex(s => s._id === id);
  // api_key/credentials_file are real secrets — never persist them to
  // localStorage, even for this explicit "save for reuse" library.
  const entry: SavedProvider = {
    name, _id: id,
    def: { ...def, _id: id, api_key: undefined, credentials_file: undefined, base_url: stripUrlCredentials(def.base_url) },
  };
  if (idx >= 0) { saved[idx] = entry; } else { saved.push(entry); }
  localStorage.setItem(SAVED_PROVIDERS_KEY, JSON.stringify(saved));
  savedProviderList.value = saved;
}

function removeSavedProvider(id: string) {
  const saved = getSavedProviders().filter(s => s._id !== id);
  localStorage.setItem(SAVED_PROVIDERS_KEY, JSON.stringify(saved));
  savedProviderList.value = saved;
}

function importSavedProvider(sp: SavedProvider) {
  if (!localSettings.value.providers) localSettings.value.providers = {};
  localSettings.value.providers[sp.name] = { ...sp.def };
  showSavedProviders.value = false;
}

// localStorage is not reactive, so this is a ref that save/remove refresh
// explicitly; a computed over getSavedProviders() only ever ran once per
// page load and the list looked stale until a refresh.
const savedProviderList = ref<SavedProvider[]>(getSavedProviders());

// Allowed Commands
const allowedCommandsString = computed({
  get: () => (localSettings.value.allowed_commands || []).join(', '),
  set: (val: string) => {
    localSettings.value.allowed_commands = val.split(',').map(s => s.trim()).filter(s => s);
  }
});

// --- Service Providers ---

interface ProviderEntry {
  name: string;
  def: ProviderDefinition;
}

const providerTypes = [
  { value: 'openai', label: 'OpenAI' },
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'gemini', label: 'Google Gemini' },
  { value: 'ollama', label: 'Ollama (Local)' },
  { value: 'litellm', label: 'LiteLLM (Proxy)' },
  { value: 'codex', label: 'Codex (ChatGPT subscription)' },
];

function isCodexProvider(providerName: string): boolean {
  return (localSettings.value.providers?.[providerName] as ProviderDefinition | undefined)?.type === 'codex';
}

const providerEntries = computed({
  get: (): ProviderEntry[] => {
    const map = localSettings.value.providers || {};
    return Object.entries(map).map(([name, def]) => ({ name, def: { ...def } }));
  },
  set: (entries: ProviderEntry[]) => {
    const map: Record<string, ProviderDefinition> = {};
    for (const e of entries) {
      if (e.name.trim()) map[e.name.trim()] = e.def;
    }
    localSettings.value.providers = map;
  }
});

// Provider names for Default Provider selector (only defined providers)
const definedProviderNames = computed(() =>
  Object.keys(localSettings.value.providers || {}).filter(Boolean)
);

function addProvider() {
  const map = { ...(localSettings.value.providers || {}) };
  // Suggest next unused provider type
  const used = new Set(Object.values(map).map(d => d.type).filter(Boolean));
  const nextType = providerTypes.find(t => !used.has(t.value));
  const name = nextType?.value ?? `provider-${Object.keys(map).length + 1}`;
  map[name] = { _id: generateEntityId(), type: nextType?.value ?? 'openai' };
  localSettings.value.providers = map;
}

// Ensure all providers have IDs (backfill for existing configs)
function ensureProviderIds() {
  const providers = localSettings.value.providers;
  if (!providers) return;
  let changed = false;
  for (const [, def] of Object.entries(providers)) {
    if (!def._id) {
      def._id = generateEntityId();
      changed = true;
    }
  }
  if (changed) localSettings.value.providers = { ...providers };
}

function removeProvider(name: string) {
  const map = { ...(localSettings.value.providers || {}) };
  delete map[name];
  localSettings.value.providers = map;
  // Clear default if it was this provider
  if (localSettings.value.default_provider === name) {
    localSettings.value.default_provider = '';
  }
}

function updateProviderName(oldName: string, newName: string) {
  if (!newName.trim() || oldName === newName) return;
  const map = { ...(localSettings.value.providers || {}) };
  const def = map[oldName];
  if (!def) return;
  // Ensure ID exists before rename
  if (!def._id) def._id = generateEntityId();
  delete map[oldName];
  map[newName.trim()] = def;
  localSettings.value.providers = map;
  // Update default if it was renamed
  if (localSettings.value.default_provider === oldName) {
    localSettings.value.default_provider = newName.trim();
  }
  // Sync flat maps
  syncFlatMaps(oldName, newName.trim());
  // Notify parent to update all node references (always emit — use both ID and name)
  emit('provider-renamed', def._id!, oldName, newName.trim());
}

function updateProviderField(name: string, field: keyof ProviderDefinition, value: string) {
  const map = { ...(localSettings.value.providers || {}) };
  if (map[name]) {
    map[name] = { ...map[name], [field]: value };
    localSettings.value.providers = map;
    // Sync to flat maps for backend compatibility
    if (field === 'api_key') {
      if (!localSettings.value.api_keys) localSettings.value.api_keys = {};
      if (value) localSettings.value.api_keys[name] = value;
      else delete localSettings.value.api_keys[name];
    }
    if (field === 'base_url') {
      if (!localSettings.value.base_urls) localSettings.value.base_urls = {};
      if (value) localSettings.value.base_urls[name] = value;
      else delete localSettings.value.base_urls[name];
    }
    if (field === 'credentials_file') {
      if (!localSettings.value.credentials_files) localSettings.value.credentials_files = {};
      if (value) localSettings.value.credentials_files[name] = value;
      else delete localSettings.value.credentials_files[name];
    }
  }
}

function syncFlatMaps(oldName: string, newName: string) {
  for (const field of ['api_keys', 'base_urls', 'credentials_files'] as const) {
    const map = localSettings.value[field];
    if (map && oldName in map) {
      const val = map[oldName]!;
      delete map[oldName];
      map[newName] = val;
    }
  }
}

// Section toggles
const showExecution = ref(false);
const showLogging = ref(false);
const showServer = ref(false);
const showPricing = ref(false);

// --- Model Discovery ---

const discoveredModels = ref<string[]>([]);
const modelDiscoveryLoading = ref(false);

// Per-provider model cache
const providerModelsCache = ref<Record<string, string[]>>({});

// Model info from proxy
interface ModelInfo {
  max_tokens?: number;
  max_input_tokens?: number;
  supports_vision?: boolean;
  supports_function_calling?: boolean;
  input_cost_per_token?: number;
  output_cost_per_token?: number;
}
const modelInfoMap = ref<Record<string, ModelInfo>>({});

function getProviderBaseUrl(providerName: string): string {
  return (localSettings.value.providers?.[providerName] as ProviderDefinition | undefined)?.base_url
    ?? localSettings.value.base_urls?.[providerName]
    ?? '';
}

function getProviderApiKey(providerName: string): string {
  return (localSettings.value.providers?.[providerName] as ProviderDefinition | undefined)?.api_key
    ?? localSettings.value.api_keys?.[providerName]
    ?? '';
}

const defaultModelOptions = computed(() => {
  const provider = localSettings.value.default_provider;
  const baseUrl = getProviderBaseUrl(provider);

  // If provider has a base_url (or is codex), show ONLY discovered models
  if (baseUrl || isCodexProvider(provider)) return discoveredModels.value;

  // No base_url — use static list based on provider type
  const provDef = localSettings.value.providers?.[provider] as ProviderDefinition | undefined;
  const provType = provDef?.type ?? provider;
  const staticModels: Record<string, string[]> = {
    openai: ['gpt-4o', 'gpt-4o-mini', 'o3-mini', 'gpt-4.1', 'gpt-4.1-mini'],
    anthropic: ['claude-opus-4-6', 'claude-sonnet-4-6', 'claude-haiku-4-5-20251001'],
    gemini: ['gemini-2.5-flash', 'gemini-2.5-pro', 'gemini-2.0-flash'],
    ollama: ['llama3.3', 'mistral', 'qwen2.5', 'deepseek-r1'],
  };
  return staticModels[provType] ?? staticModels[provider] ?? [];
});

async function discoverModels() {
  const provider = localSettings.value.default_provider;
  if (!provider) { discoveredModels.value = []; return; }
  const baseUrl = getProviderBaseUrl(provider);
  const apiKey = getProviderApiKey(provider);
  const codex = isCodexProvider(provider);
  if (!baseUrl && !codex) { discoveredModels.value = []; return; }

  modelDiscoveryLoading.value = true;
  try {
    const params = codex
      ? new URLSearchParams({ type: 'codex', credentials_file: (localSettings.value.providers?.[provider] as ProviderDefinition | undefined)?.credentials_file ?? '' })
      : new URLSearchParams({ base_url: baseUrl });
    const headers: HeadersInit = {};
    if (apiKey) headers['X-Provider-Key'] = apiKey;
    const [modelsRes, infoRes] = await Promise.allSettled([
      fetch(`/api/providers/models?${params}`, { headers }),
      // Codex has no model-info endpoint; skip it instead of 404-ing.
      codex ? Promise.reject(new Error('n/a')) : fetch(`/api/providers/model-info?${params}`, { headers }),
    ]);

    if (modelsRes.status === 'fulfilled' && modelsRes.value.ok) {
      const data = await modelsRes.value.json() as { models?: string[] };
      discoveredModels.value = data.models ?? [];
    }

    if (infoRes.status === 'fulfilled' && infoRes.value.ok) {
      const data = await infoRes.value.json();
      const map: Record<string, ModelInfo> = {};
      for (const item of (data?.data ?? [])) {
        const name = item.model_name ?? item.model_group ?? '';
        const info = item.model_info ?? item;
        if (name) {
          map[name] = {
            max_tokens: info.max_tokens ?? info.max_output_tokens,
            max_input_tokens: info.max_input_tokens,
            supports_vision: info.supports_vision ?? info.supports_image_input,
            supports_function_calling: info.supports_function_calling ?? info.supports_tools,
            input_cost_per_token: info.input_cost_per_token,
            output_cost_per_token: info.output_cost_per_token,
          };
        }
      }
      modelInfoMap.value = map;
    }
  } catch {
    discoveredModels.value = [];
  } finally {
    modelDiscoveryLoading.value = false;
  }
}

const currentDefaultModelInfo = computed(() =>
  modelInfoMap.value[localSettings.value.defaults.model] ?? null
);

function formatTokens(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(0)}k`;
  return String(n);
}

// Per-provider model list (discovered or static fallback)
const providerDiscoveryPending = new Set<string>();

function getProviderModels(providerName: string): string[] {
  // If we have discovered models, return only those
  if (providerModelsCache.value[providerName]?.length) return providerModelsCache.value[providerName];

  const provDef = localSettings.value.providers?.[providerName] as ProviderDefinition | undefined;
  const baseUrl = getProviderBaseUrl(providerName);

  // Codex has no base_url; its catalog comes from the local Codex CLI login.
  if ((baseUrl || provDef?.type === 'codex') && !providerDiscoveryPending.has(providerName)) {
    providerDiscoveryPending.add(providerName);
    discoverProviderModels(providerName).finally(() => providerDiscoveryPending.delete(providerName));
    return []; // Will populate after discovery completes
  }

  // No base_url — use static list based on provider type
  const provType = provDef?.type ?? providerName;
  const staticModels: Record<string, string[]> = {
    openai: ['gpt-4o', 'gpt-4o-mini', 'o3-mini', 'gpt-4.1', 'gpt-4.1-mini'],
    anthropic: ['claude-opus-4-6', 'claude-sonnet-4-6', 'claude-haiku-4-5-20251001'],
    gemini: ['gemini-2.5-flash', 'gemini-2.5-pro', 'gemini-2.0-flash'],
    ollama: ['llama3.3', 'mistral', 'qwen2.5', 'deepseek-r1'],
  };
  return staticModels[provType] ?? [];
}

// Discover models for a specific provider
async function discoverProviderModels(providerName: string) {
  const baseUrl = getProviderBaseUrl(providerName);
  const apiKey = getProviderApiKey(providerName);
  const codex = isCodexProvider(providerName);
  if (!baseUrl && !codex) { delete providerModelsCache.value[providerName]; return; }
  try {
    const params = codex
      ? new URLSearchParams({ type: 'codex', credentials_file: (localSettings.value.providers?.[providerName] as ProviderDefinition | undefined)?.credentials_file ?? '' })
      : new URLSearchParams({ base_url: baseUrl });
    const headers: HeadersInit = {};
    if (apiKey) headers['X-Provider-Key'] = apiKey;
    const res = await fetch(`/api/providers/models?${params}`, { headers });
    if (res.ok) {
      const data = await res.json() as { models?: string[] };
      providerModelsCache.value = { ...providerModelsCache.value, [providerName]: data.models ?? [] };
    }
  } catch { /* ignore */ }
}

// Discover models for all providers on load and when providers change
async function discoverAllProviderModels() {
  const providers = localSettings.value.providers || {};
  const promises: Promise<void>[] = [];
  for (const name of Object.keys(providers)) {
    if (getProviderBaseUrl(name) || isCodexProvider(name)) promises.push(discoverProviderModels(name));
  }
  await Promise.allSettled(promises);
}

// Initial discovery on load
discoverAllProviderModels();

// Re-discover when default provider or providers change
watch(() => localSettings.value.default_provider, () => discoverModels());
watch(() => localSettings.value.providers, () => { discoverModels(); discoverAllProviderModels(); }, { deep: true });

// Auto-populate defaults when model changes and info is available
watch(() => localSettings.value.defaults.model, (model) => {
  if (!model) return;
  const info = modelInfoMap.value[model];
  if (!info) return;
  // Don't auto-fill max_tokens — setting it to the model's max can exceed
  // context window when input is large. Leave at 0 to let the provider decide.
});

// --- Pricing ---

interface PricingEntry {
  model: string;
  config: PricingConfig;
}

const pricingEntries = computed({
  get: (): PricingEntry[] => {
    const map = localSettings.value.pricing || {};
    return Object.entries(map).map(([model, config]) => ({ model, config: { ...config } }));
  },
  set: (entries: PricingEntry[]) => {
    const map: Record<string, PricingConfig> = {};
    for (const e of entries) {
      if (e.model.trim()) map[e.model.trim()] = e.config;
    }
    localSettings.value.pricing = map;
  }
});

function addPricingEntry() {
  const map = { ...(localSettings.value.pricing || {}) };
  map['model-name'] = { input: 0, output: 0 };
  localSettings.value.pricing = map;
}

function removePricingEntry(model: string) {
  const map = { ...(localSettings.value.pricing || {}) };
  delete map[model];
  localSettings.value.pricing = map;
}

function updatePricingModel(oldModel: string, newModel: string) {
  if (!newModel.trim() || oldModel === newModel) return;
  const map = { ...(localSettings.value.pricing || {}) };
  const cfg = map[oldModel];
  if (!cfg) return;
  delete map[oldModel];
  map[newModel.trim()] = cfg;
  localSettings.value.pricing = map;
}

function updatePricingField(model: string, field: 'input' | 'output', value: number) {
  const map = { ...(localSettings.value.pricing || {}) };
  if (map[model]) {
    map[model] = { ...map[model], [field]: value };
    localSettings.value.pricing = map;
  }
}

function ensureServer() {
  if (!localSettings.value.server) {
    localSettings.value.server = {};
  }
}

// Auto-apply settings changes (debounced)
let settingsApplyTimer: ReturnType<typeof setTimeout> | null = null;
let lastEmittedJSON = '';
let suppressApply = false;

watch(localSettings, () => {
  if (suppressApply) return;
  if (settingsApplyTimer) clearTimeout(settingsApplyTimer);
  settingsApplyTimer = setTimeout(() => {
    const json = JSON.stringify(localSettings.value);
    lastEmittedJSON = json;
    emit('update', JSON.parse(json));
  }, 300);
}, { deep: true });

// Watch for external settings changes (e.g., undo/redo)
watch(() => props.settings, (newSettings) => {
  const newJSON = JSON.stringify(newSettings);
  if (newJSON === lastEmittedJSON) return;
  suppressApply = true;
  localSettings.value = JSON.parse(newJSON);
  ensureServer();
  nextTick(() => { suppressApply = false; });
}, { deep: true });
</script>

<template>
  <div class="settings-panel" @keydown.escape="emit('close')">
    <div class="panel-header">
      <h3>Project Settings</h3>
      <button class="close-btn" @click="emit('close')">x</button>
    </div>

    <div class="panel-content">
      <!-- Version & Description -->
      <div class="form-row">
        <div class="form-group half">
          <label>Version</label>
          <input type="text" :value="version" @input="emit('update:version', ($event.target as HTMLInputElement).value)" placeholder="1.0" />
        </div>
      </div>
      <div class="form-group">
        <label>Description</label>
        <textarea :value="description" @input="emit('update:description', ($event.target as HTMLTextAreaElement).value)" rows="2" placeholder="Project description"></textarea>
      </div>

      <!-- Interactive mode toggle -->
      <div class="form-group">
        <label class="checkbox-label">
          <input
            type="checkbox"
            :checked="!!interactive"
            @change="emit('update:interactive', ($event.target as HTMLInputElement).checked)"
          />
          Interactive mode (run as chat instead of one-shot)
        </label>
        <div class="field-hint">
          When on, <code>rakitsu run</code> launches a chat TUI (web UI chat panel in follow-up).
          Users can converse with the agent/orchestrator across turns.
        </div>
      </div>

      <!-- Appearance (canvas wallpaper) -->
      <div class="section-toggle" @click="showAppearance = !showAppearance">
        <span>{{ showAppearance ? 'v' : '>' }} Appearance</span>
      </div>
      <template v-if="showAppearance">
        <div class="form-group">
          <label class="checkbox-label">
            <input type="checkbox" v-model="wallpaperEnabled" />
            Animated shader wallpaper on canvas
          </label>
          <div class="field-hint">
            Adds a live WebGL background behind the Builder and Debugger canvases.
            Keyboard 1-{{ wallpaperCount }} switches shader while the canvas tab is visible.
          </div>
        </div>
        <div class="form-group">
          <label>Shader</label>
          <div class="wallpaper-thumb-grid">
            <button
              v-for="(lbl, i) in WALLPAPER_LABELS"
              :key="i"
              type="button"
              class="wallpaper-thumb"
              :class="[`wallpaper-thumb-${i + 1}`, { active: wallpaperEnabled && wallpaperShader === i }]"
              :title="lbl"
              @click="pickWallpaper(i)"
            >
              <span class="wallpaper-thumb-label">{{ lbl }}</span>
            </button>
          </div>
        </div>
        <div class="form-group">
          <label>
            Dim <span class="hint">({{ wallpaperDim }}%)</span>
          </label>
          <input
            v-model.number="wallpaperDim"
            type="range"
            min="0"
            max="100"
            :disabled="!wallpaperEnabled"
            class="wallpaper-dim-slider"
          />
        </div>
      </template>

      <!-- 1. Service Providers (define first) -->
      <div class="section-label primary">Service Providers</div>
      <div v-for="entry in providerEntries" :key="entry.name" class="provider-card">
        <div class="provider-header">
          <input
            :value="entry.name"
            type="text"
            class="provider-name-input"
            placeholder="provider name"
            @change="updateProviderName(entry.name, ($event.target as HTMLInputElement).value)"
          />
          <button class="remove-btn" @click="removeProvider(entry.name)">x</button>
        </div>
        <div class="form-group">
          <label>Type</label>
          <ComboBox
            :model-value="entry.def.type ?? ''"
            :options="providerTypes"
            placeholder="Select type"
            :allow-custom="false"
            @update:model-value="updateProviderField(entry.name, 'type', $event)"
          />
        </div>
        <div class="form-group">
          <label>Default Model</label>
          <ComboBox
            :model-value="entry.def.default_model ?? ''"
            :options="getProviderModels(entry.name)"
            :placeholder="getProviderModels(entry.name).length > 0 ? 'Select model' : 'Type model name'"
            :allow-custom="true"
            @update:model-value="updateProviderField(entry.name, 'default_model', $event)"
          />
          <span v-if="getProviderModels(entry.name).length > 0" class="hint">
            {{ getProviderModels(entry.name).length }} models discovered
          </span>
        </div>
        <div v-if="entry.def.type === 'codex'" class="form-group">
          <span class="hint">Uses your Codex CLI login (~/.codex/auth.json, run <code>codex login</code> once). Leave the model empty to use the one in ~/.codex/config.toml.</span>
        </div>
        <template v-else>
        <div class="form-group">
          <label>API Key</label>
          <input
            :value="entry.def.api_key || ''"
            type="password"
            placeholder="sk-..."
            @input="updateProviderField(entry.name, 'api_key', ($event.target as HTMLInputElement).value)"
          />
        </div>
        <div class="form-group">
          <label>Base URL</label>
          <input
            :value="entry.def.base_url || ''"
            type="text"
            placeholder="https://api.openai.com (leave empty for default)"
            @input="updateProviderField(entry.name, 'base_url', ($event.target as HTMLInputElement).value)"
          />
        </div>
        <div class="form-group">
          <label>Credentials File <span class="hint">(Vertex AI / service accounts)</span></label>
          <input
            :value="entry.def.credentials_file || ''"
            type="text"
            placeholder="./keys/service-account.json"
            @input="updateProviderField(entry.name, 'credentials_file', ($event.target as HTMLInputElement).value)"
          />
        </div>
        <div class="form-row">
          <div class="form-group half">
            <label>Location <span class="hint">(Vertex AI)</span></label>
            <input
              :value="entry.def.location || ''"
              type="text"
              placeholder="us-central1"
              @input="updateProviderField(entry.name, 'location', ($event.target as HTMLInputElement).value)"
            />
          </div>
          <div class="form-group half">
            <label>Project <span class="hint">(Vertex AI)</span></label>
            <input
              :value="entry.def.project || ''"
              type="text"
              placeholder="my-gcp-project"
              @input="updateProviderField(entry.name, 'project', ($event.target as HTMLInputElement).value)"
            />
          </div>
        </div>
        </template>
        <div class="provider-actions">
          <button
            class="btn btn-apply-all"
            @click="emit('apply-provider-to-all', entry.name, entry.def.default_model || '')"
            :title="`Set all agents/orchestrators to use ${entry.name}`"
          >
            Apply to All
          </button>
          <button
            class="btn btn-save-provider"
            @click="saveProviderToLibrary(entry.name, entry.def)"
            title="Save to provider library for reuse in other projects"
          >
            Save
          </button>
        </div>
      </div>

      <div class="provider-buttons">
        <button class="btn btn-add" @click="addProvider">+ Add Provider</button>
        <button class="btn btn-add btn-load" @click="showSavedProviders = !showSavedProviders">
          {{ showSavedProviders ? 'Hide Saved' : 'Load Saved' }} ({{ savedProviderList.length }})
        </button>
      </div>

      <!-- Saved provider library -->
      <div v-if="showSavedProviders && savedProviderList.length > 0" class="saved-provider-list">
        <div v-for="sp in savedProviderList" :key="sp._id" class="saved-provider-item">
          <div class="saved-info">
            <span class="saved-name">{{ sp.name }}</span>
            <span class="saved-type">{{ sp.def.type }}</span>
            <span v-if="sp.def.base_url" class="saved-url">{{ sp.def.base_url }}</span>
          </div>
          <div class="saved-actions">
            <button class="btn-small btn-import" @click="importSavedProvider(sp)">Import</button>
            <button class="btn-small btn-remove" @click="removeSavedProvider(sp._id)">x</button>
          </div>
        </div>
      </div>
      <div v-if="showSavedProviders && savedProviderList.length === 0" class="empty-hint">
        No saved providers yet. Click "Save" on a provider card to add one.
      </div>

      <div v-if="definedProviderNames.length === 0" class="empty-hint">
        Define at least one service provider above to get started
      </div>

      <!-- 2. Default Provider (select from defined) -->
      <template v-if="definedProviderNames.length > 0">
        <div class="section-label">Defaults</div>
        <div class="form-group">
          <label>Default Provider</label>
          <ComboBox
            :model-value="localSettings.default_provider"
            :options="definedProviderNames"
            placeholder="Select from providers above"
            :allow-custom="false"
            @update:model-value="localSettings.default_provider = $event"
          />
        </div>

        <!-- 3. Default Model (auto-discovers from default provider) -->
        <div class="form-group">
          <label>Default Model <span v-if="modelDiscoveryLoading" class="hint">discovering...</span><span v-else-if="discoveredModels.length > 0" class="hint discovered">{{ discoveredModels.length }} discovered</span></label>
          <ComboBox
            :model-value="localSettings.defaults.model"
            :options="defaultModelOptions"
            placeholder="Select or type model"
            :loading="modelDiscoveryLoading"
            loading-text="Discovering models..."
            @update:model-value="localSettings.defaults.model = $event"
          />
          <div v-if="currentDefaultModelInfo" class="model-info-bar">
            <span v-if="currentDefaultModelInfo.max_input_tokens" class="mi-tag">ctx: {{ formatTokens(currentDefaultModelInfo.max_input_tokens) }}</span>
            <span v-if="currentDefaultModelInfo.max_tokens" class="mi-tag">out: {{ formatTokens(currentDefaultModelInfo.max_tokens) }}</span>
            <span v-if="currentDefaultModelInfo.supports_vision" class="mi-tag vision">vision</span>
            <span v-if="currentDefaultModelInfo.supports_function_calling" class="mi-tag tools">tools</span>
            <span v-if="currentDefaultModelInfo.input_cost_per_token" class="mi-tag cost">${{ (currentDefaultModelInfo.input_cost_per_token * 1e6).toFixed(2) }}/M in</span>
          </div>
        </div>

        <div class="form-row">
          <div class="form-group half">
            <label>Temperature</label>
            <input v-model.number="localSettings.defaults.temperature" type="number" min="0" max="2" step="0.1" />
          </div>
          <div class="form-group half">
            <label>Max Output Tokens <span v-if="currentDefaultModelInfo?.max_tokens" class="hint discovered">auto</span></label>
            <input v-model.number="localSettings.defaults.max_tokens" type="number" min="1" />
          </div>
        </div>
      </template>

      <!-- Allowed Commands -->
      <div class="form-group">
        <label>Allowed Commands <span class="hint">(comma-separated)</span></label>
        <input v-model="allowedCommandsString" type="text" placeholder="python3, node, git" />
      </div>

      <!-- Execution Settings -->
      <div class="section-toggle" @click="showExecution = !showExecution">
        <span>{{ showExecution ? 'v' : '>' }} Execution</span>
      </div>
      <template v-if="showExecution">
        <div class="form-row">
          <div class="form-group half">
            <label>Max Iterations</label>
            <input v-model.number="localSettings.execution.max_iterations" type="number" min="1" />
          </div>
          <div class="form-group half">
            <label>Timeout (sec)</label>
            <input v-model.number="localSettings.execution.timeout_seconds" type="number" min="1" />
          </div>
        </div>
        <div class="form-group">
          <label>Retry Attempts</label>
          <input v-model.number="localSettings.execution.retry_attempts" type="number" min="0" />
        </div>
        <div class="form-row">
          <div class="form-group half">
            <label>Max Total Tokens <span class="hint">(budget)</span></label>
            <input v-model.number="localSettings.execution.max_total_tokens" type="number" min="0" placeholder="0 = unlimited" />
          </div>
          <div class="form-group half">
            <label>Max Cost ($) <span class="hint">(budget)</span></label>
            <input v-model.number="localSettings.execution.max_cost" type="number" min="0" step="0.01" placeholder="0 = unlimited" />
          </div>
        </div>
      </template>

      <!-- Logging Settings -->
      <div class="section-toggle" @click="showLogging = !showLogging">
        <span>{{ showLogging ? 'v' : '>' }} Logging</span>
      </div>
      <template v-if="showLogging">
        <div class="form-row">
          <div class="form-group half">
            <label>Level</label>
            <ComboBox
              :model-value="localSettings.logging.level"
              :options="[{value:'',label:'-- Default --'},{value:'debug',label:'Debug'},{value:'info',label:'Info'},{value:'warn',label:'Warn'},{value:'error',label:'Error'}]"
              placeholder="Select level"
              :allow-custom="false"
              @update:model-value="localSettings.logging.level = $event"
            />
          </div>
          <div class="form-group half">
            <label>Log File</label>
            <input v-model="localSettings.logging.file" type="text" placeholder="./rakitsu.log" />
          </div>
        </div>
      </template>

      <!-- Hub Connection -->
      <div class="section-toggle" @click="showServer = !showServer">
        <span>{{ showServer ? 'v' : '>' }} Hub / Server</span>
      </div>
      <template v-if="showServer">
        <div class="form-group">
          <label>Hub URL</label>
          <input v-model="localSettings.hub_url" type="text" placeholder="http://localhost:9100" />
          <span class="hint">SSE hub for live monitoring. Overridden by --hub flag.</span>
        </div>
        <template v-if="localSettings.server">
          <div class="form-row">
            <div class="form-group half">
              <label>Port</label>
              <input v-model.number="localSettings.server!.port" type="number" min="1" max="65535" placeholder="9100" @focus="ensureServer()" />
            </div>
            <div class="form-group half">
              <label>Host</label>
              <input v-model="localSettings.server!.host" type="text" placeholder="localhost" @focus="ensureServer()" />
            </div>
          </div>
        </template>
      </template>

      <!-- Pricing Config -->
      <div class="section-toggle" @click="showPricing = !showPricing">
        <span>{{ showPricing ? 'v' : '>' }} Pricing <span class="hint">({{ pricingEntries.length }} models)</span></span>
      </div>
      <template v-if="showPricing">
        <div v-for="entry in pricingEntries" :key="entry.model" class="provider-card">
          <div class="provider-header">
            <input
              :value="entry.model"
              type="text"
              class="provider-name-input"
              placeholder="model name"
              @change="updatePricingModel(entry.model, ($event.target as HTMLInputElement).value)"
            />
            <button class="remove-btn" @click="removePricingEntry(entry.model)">x</button>
          </div>
          <div class="form-row">
            <div class="form-group half">
              <label>Input ($/1M tokens)</label>
              <input
                :value="entry.config.input"
                type="number"
                min="0"
                step="0.01"
                placeholder="0.00"
                @input="updatePricingField(entry.model, 'input', parseFloat(($event.target as HTMLInputElement).value) || 0)"
              />
            </div>
            <div class="form-group half">
              <label>Output ($/1M tokens)</label>
              <input
                :value="entry.config.output"
                type="number"
                min="0"
                step="0.01"
                placeholder="0.00"
                @input="updatePricingField(entry.model, 'output', parseFloat(($event.target as HTMLInputElement).value) || 0)"
              />
            </div>
          </div>
        </div>
        <button class="btn btn-add" @click="addPricingEntry">+ Add Model Pricing</button>
      </template>
    </div>

  </div>
</template>

<style scoped>
.settings-panel {
  width: 360px;
  background: var(--surface-1);
  border-left: 1px solid var(--border-default);
  display: flex;
  flex-direction: column;
  height: 100%;
}

.panel-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px;
  border-bottom: 1px solid var(--border-default);
  background: var(--surface-0);
}

.panel-header h3 {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary);
}

.close-btn {
  background: none;
  border: none;
  font-size: 16px;
  color: var(--text-secondary);
  cursor: pointer;
  padding: 2px 6px;
  line-height: 1;
}
.close-btn:hover { color: var(--text-primary); }

.panel-content {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
}

.form-group { margin-bottom: 12px; }
.form-group label {
  display: block;
  font-size: 12px;
  font-weight: 500;
  color: var(--text-secondary);
  margin-bottom: 4px;
}

.form-group input,
.form-group select,
.form-group textarea {
  width: 100%;
  padding: 7px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  font-size: 13px;
  transition: border-color 0.2s;
  box-sizing: border-box;
  background: var(--surface-1);
  color: var(--text-primary);
}
.form-group input:focus,
.form-group select:focus,
.form-group textarea:focus {
  outline: none;
  border-color: var(--border-focus);
}
.form-group textarea {
  resize: vertical;
  font-family: var(--font-mono);
  font-size: 12px;
}
.form-row { display: flex; gap: 10px; }
.form-group.half { flex: 1; }

.hint {
  font-weight: 400;
  color: var(--text-muted);
  font-size: 11px;
}
.hint.discovered { color: var(--status-running); }

.section-label {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  margin-top: 12px;
  margin-bottom: 8px;
  border-top: 1px solid var(--border-subtle);
  padding-top: 8px;
}
.section-label.primary {
  margin-top: 0;
  border-top: none;
  padding-top: 0;
  font-size: 13px;
  color: var(--accent-agent);
}

.section-toggle {
  padding: 8px 0;
  cursor: pointer;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-muted);
  border-top: 1px solid var(--border-subtle);
  margin-top: 8px;
  user-select: none;
}
.section-toggle:hover { color: var(--text-primary); }

.provider-card {
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  padding: 10px;
  margin-bottom: 10px;
  background: var(--surface-2);
}

.provider-header {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 8px;
}

.provider-name-input {
  flex: 1;
  padding: 5px 8px;
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
  background: var(--surface-1);
}
.provider-name-input:focus {
  outline: none;
  border-color: var(--border-focus);
}

.remove-btn {
  background: none;
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  color: var(--text-muted);
  cursor: pointer;
  padding: 4px 8px;
  font-size: 12px;
  line-height: 1;
}
.remove-btn:hover { color: var(--status-error); border-color: var(--status-error); }

.btn-add {
  width: 100%;
  padding: 6px;
  background: none;
  border: 1px dashed var(--border-default);
  border-radius: var(--node-radius);
  color: var(--accent-agent);
  cursor: pointer;
  font-size: 12px;
  font-weight: 500;
  margin-bottom: 8px;
}
.btn-add:hover { border-color: var(--accent-agent); background: var(--surface-3); }

.provider-actions { display: flex; gap: 6px; }
.btn-apply-all, .btn-save-provider {
  flex: 1;
  padding: 5px;
  background: none;
  border-radius: var(--node-radius);
  font-size: 11px;
  font-weight: 600;
  cursor: pointer;
}
.btn-apply-all {
  border: 1px solid var(--accent-agent);
  color: var(--accent-agent);
  cursor: pointer;
  margin-top: 8px;
}
.btn-apply-all:hover { background: rgba(91, 141, 239, 0.1); }
.btn-save-provider { border: 1px solid var(--status-running); color: var(--status-running); }
.btn-save-provider:hover { background: rgba(34, 197, 94, 0.1); }

.provider-buttons { display: flex; gap: 6px; margin-top: 4px; }
.btn-load { border-color: var(--status-paused); color: var(--status-paused); }
.btn-load:hover { background: rgba(245, 158, 11, 0.1); }

.saved-provider-list {
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  margin-top: 6px;
  overflow: hidden;
}
.saved-provider-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 10px;
  border-bottom: 1px solid var(--border-subtle);
  font-size: 12px;
}
.saved-provider-item:last-child { border-bottom: none; }
.saved-info { display: flex; gap: 6px; align-items: center; flex: 1; overflow: hidden; }
.saved-name { font-weight: 600; color: var(--text-primary); }
.saved-type { font-size: 10px; color: var(--text-secondary); background: var(--surface-3); padding: 1px 5px; border-radius: var(--node-radius); }
.saved-url { font-size: 10px; color: var(--text-muted); font-family: var(--font-mono); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.saved-actions { display: flex; gap: 4px; flex-shrink: 0; }
.btn-import { color: var(--accent-agent); border-color: var(--accent-agent); }
.btn-remove { color: var(--status-error); border-color: var(--status-error); }

.empty-hint {
  text-align: center;
  padding: 16px;
  color: var(--text-muted);
  font-size: 12px;
  border: 1px dashed var(--border-default);
  border-radius: var(--node-radius);
  margin: 8px 0;
}

.model-info-bar {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-top: 4px;
}
.mi-tag {
  padding: 1px 6px;
  background: var(--surface-3);
  border-radius: var(--node-radius);
  font-size: 10px;
  color: var(--text-secondary);
}
.mi-tag.vision { background: rgba(34, 197, 94, 0.15); color: var(--status-running); }
.mi-tag.tools { background: rgba(91, 141, 239, 0.15); color: var(--accent-agent); }
.mi-tag.cost { background: rgba(245, 158, 11, 0.15); color: var(--status-paused); }

.panel-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 16px;
  border-top: 1px solid var(--border-default);
  background: var(--surface-0);
}

.btn {
  padding: 8px 16px;
  border-radius: var(--node-radius);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  border: none;
  transition: all 0.2s;
}
.btn-primary { background: var(--accent-agent); color: var(--surface-0); }
.btn-primary:hover { background: #4a7ae0; }
.btn-secondary { background: var(--surface-3); color: var(--text-primary); }
.btn-secondary:hover { background: var(--surface-4); }

/* Appearance — wallpaper thumbnails */
.wallpaper-thumb-grid {
  display: grid;
  grid-template-columns: repeat(5, 1fr);
  gap: 8px;
}
.wallpaper-thumb {
  all: unset;
  cursor: pointer;
  position: relative;
  aspect-ratio: 16 / 10;
  border-radius: 6px;
  overflow: hidden;
  border: 1px solid var(--border-default);
  transition: transform 0.15s ease, border-color 0.15s ease, box-shadow 0.15s ease;
}
.wallpaper-thumb:hover {
  transform: translateY(-1px);
  border-color: var(--text-secondary);
}
.wallpaper-thumb.active {
  border-color: var(--accent-agent);
  box-shadow: 0 0 0 1px var(--accent-agent);
}
.wallpaper-thumb-label {
  position: absolute;
  left: 6px;
  bottom: 4px;
  right: 6px;
  font-family: var(--font-mono, 'SF Mono', ui-monospace, Menlo, Consolas, monospace);
  font-size: 9px;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: #fff;
  text-shadow: 0 1px 2px rgba(0, 0, 0, 0.6);
  pointer-events: none;
}
.wallpaper-thumb-1 {
  background:
    radial-gradient(120% 80% at 30% 40%, #5b8def44 0%, transparent 60%),
    radial-gradient(120% 80% at 80% 80%, #2dd4a033 0%, transparent 60%),
    linear-gradient(135deg, #1a1a2e, #12121f);
}
.wallpaper-thumb-2 {
  background:
    repeating-linear-gradient(45deg, #c084fc22 0 2px, transparent 2px 8px),
    repeating-linear-gradient(-45deg, #5b8def22 0 2px, transparent 2px 8px),
    #12121f;
}
.wallpaper-thumb-3 {
  background: radial-gradient(70% 60% at 55% 45%, #f472b655 0%, #12121f 70%);
}
.wallpaper-thumb-4 {
  background:
    radial-gradient(40% 40% at 25% 30%, #2dd4a055 0%, transparent 60%),
    radial-gradient(40% 40% at 70% 60%, #c084fc55 0%, transparent 60%),
    radial-gradient(40% 40% at 50% 80%, #5b8def44 0%, transparent 60%),
    #12121f;
}
.wallpaper-thumb-5 {
  background: linear-gradient(170deg, #12121f 0%, #5b8def33 35%, #2dd4a022 55%, #c084fc33 75%, #12121f 100%);
}
.wallpaper-thumb-6 {
  background:
    radial-gradient(ellipse 70% 40% at 50% 30%, rgba(180, 230, 230, 0.35) 0%, transparent 60%),
    radial-gradient(circle at 30% 70%, rgba(212, 134, 55, 0.4) 0%, transparent 35%),
    radial-gradient(circle at 70% 65%, rgba(95, 180, 200, 0.35) 0%, transparent 35%),
    linear-gradient(180deg, #2c8ca8 0%, #134a72 60%, #0a2240 100%);
}
.wallpaper-thumb-7 {
  background:
    repeating-radial-gradient(circle at 42% 48%, #1f1d1a 0 1.5px, #e8e2d2 1.5px 7px),
    #e8e2d2;
}
.wallpaper-thumb-7 .wallpaper-thumb-label {
  color: #3a3630;
  text-shadow: none;
}
.wallpaper-dim-slider {
  width: 100%;
  accent-color: var(--accent-agent);
}
.wallpaper-dim-slider:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
</style>
