<script setup lang="ts">
import { ref, watch, computed, nextTick, inject } from 'vue';
import type { AgentConfig, ToolConfig, SkillConfig, OrchestratorConfig, PipelineStep, ProviderDefinition, GroupConfig } from '../../types';
import type { AgentEvent } from '../../types/events';
import type { Breakpoint } from '../../composables/useDebugControl';
import type { Ref } from 'vue';
import ComboBox from './ComboBox.vue';
import { resolveProviderName, findProviderId } from '../../utils/entityId';

type NodeData = AgentConfig | ToolConfig | SkillConfig | OrchestratorConfig | GroupConfig;

const props = withDefaults(defineProps<{
  nodeId: string;
  nodeType: 'agent' | 'tool' | 'skill' | 'orchestrator' | 'group';
  nodeData: NodeData;
  providerNames?: string[];
  providerMap?: Record<string, ProviderDefinition>;
  toolNames?: string[];
  agentNames?: string[];
  skillNames?: string[];
  baseUrls?: Record<string, string>;
  apiKeys?: Record<string, string>;
  defaultSettings?: { temperature?: number; max_tokens?: number; model?: string };
  executionSettings?: { max_iterations?: number; timeout_seconds?: number };
  groupChildren?: { id: string; type: string; name: string; _agentId?: string }[];
  groupStepDefaults?: Record<string, import('../../types').GroupStepConfig>;
  allNames?: string[];
}>(), {
  providerNames: () => [],
  providerMap: () => ({}),
  toolNames: () => [],
  agentNames: () => [],
  skillNames: () => [],
  baseUrls: () => ({}),
  apiKeys: () => ({}),
  defaultSettings: () => ({}),
  executionSettings: () => ({}),
  groupChildren: () => [],
  groupStepDefaults: () => ({}),
  allNames: () => [],
});

// Provider options: only defined providers from Settings
const allProviderOptions = computed(() => props.providerNames);

// --- Global name uniqueness ---
const nameSuggestion = ref('');

// Check on blur: if name is duplicate, revert and show suggestion
function onNameBlur() {
  const data = localData.value as { name?: string };
  const name = data.name;
  if (!name) return;
  const dupes = props.allNames.filter(n => n === name);
  if (dupes.length > 1) {
    // Generate suggestion and revert
    const stripped = name.replace(/_\d+$/, '');
    const existing = new Set(props.allNames);
    let i = 2;
    while (existing.has(`${stripped}_${i}`)) i++;
    nameSuggestion.value = `${stripped}_${i}`;
    data.name = nameSuggestion.value;
    // Clear suggestion after 3s
    setTimeout(() => { nameSuggestion.value = ''; }, 3000);
  }
}

const isDuplicateName = computed(() => {
  const name = (localData.value as { name?: string }).name;
  if (!name) return false;
  return props.allNames.filter(n => n === name).length > 1;
});

// Resolve provider display name from _providerId
function resolvedProviderName(data: AgentConfig | OrchestratorConfig): string {
  return resolveProviderName(data._providerId, data.provider, props.providerMap);
}

// When provider is selected, store both name and _providerId
function setProviderByName(data: AgentConfig | OrchestratorConfig, name: string) {
  data.provider = name;
  data._providerId = findProviderId(name, props.providerMap) || data._providerId;
}

const emit = defineEmits<{
  (e: 'update', nodeId: string, data: NodeData): void;
  (e: 'close'): void;
  (e: 'add-provider', name: string): void;
  (e: 'wire-toggle', sourceId: string, targetName: string, targetType: string, connect: boolean): void;
  (e: 'reorder-children', groupId: string, orderedChildIds: string[]): void;
}>();

// --- Drag-and-drop step reordering ---
const dragIndex = ref<number | null>(null);
const dropIndex = ref<number | null>(null);

function onStepDragStart(idx: number, ev: DragEvent) {
  dragIndex.value = idx;
  if (ev.dataTransfer) {
    ev.dataTransfer.effectAllowed = 'move';
    ev.dataTransfer.setData('text/plain', String(idx));
  }
}
function onStepDragOver(idx: number, ev: DragEvent) {
  ev.preventDefault();
  if (ev.dataTransfer) ev.dataTransfer.dropEffect = 'move';
  dropIndex.value = idx;
}
function onStepDragLeave() {
  dropIndex.value = null;
}
function onStepDrop(idx: number) {
  if (dragIndex.value === null || dragIndex.value === idx) {
    dragIndex.value = null;
    dropIndex.value = null;
    return;
  }
  // Reorder: move dragIndex to idx position
  const ordered = [...props.groupChildren];
  const [moved] = ordered.splice(dragIndex.value, 1);
  if (moved) ordered.splice(idx, 0, moved);
  emit('reorder-children', props.nodeId, ordered.map(c => c.id));
  dragIndex.value = null;
  dropIndex.value = null;
}
function onStepDragEnd() {
  dragIndex.value = null;
  dropIndex.value = null;
}

// --- Inherit/Override helpers ---
// Defines where each field gets its default value from
function getInheritedValue(field: string): unknown {
  if (!isAgent.value && !isOrchestrator.value) return undefined;
  const data = localData.value as AgentConfig | OrchestratorConfig;
  const provDef = props.providerMap[data.provider];
  switch (field) {
    case 'model': return provDef?.default_model;
    case 'temperature': return props.defaultSettings?.temperature;
    case 'max_tokens': return props.defaultSettings?.max_tokens;
    case 'max_iterations': return props.executionSettings?.max_iterations;
    case 'timeout': return props.executionSettings?.timeout_seconds;
    default: return undefined;
  }
}

function isInherited(field: string): boolean {
  if (!isAgent.value && !isOrchestrator.value) return false;
  const data = localData.value as AgentConfig | OrchestratorConfig;
  return data._inherited?.[field] === true;
}

function markInherited(field: string, inherited: boolean) {
  const data = localData.value as AgentConfig | OrchestratorConfig;
  if (!data._inherited) data._inherited = {};
  data._inherited[field] = inherited;
}

function resetToInherited(field: string) {
  const val = getInheritedValue(field);
  if (val === undefined) return;
  const data = localData.value as AgentConfig | OrchestratorConfig;
  switch (field) {
    case 'model':
      data.model = val as string;
      break;
    case 'temperature':
      if (!data.model_config) data.model_config = { temperature: 0.7, max_tokens: 4096 };
      data.model_config.temperature = val as number;
      break;
    case 'max_tokens':
      if (!data.model_config) data.model_config = { temperature: 0.7, max_tokens: 4096 };
      data.model_config.max_tokens = val as number;
      break;
    case 'max_iterations':
      if (isAgent.value) {
        const ad = data as AgentConfig;
        if (!ad.settings) ad.settings = { max_iterations: 10, verbose: false, timeout: 300, reflection: { enabled: false, mode: 'after_tool', frequency: 'always' }, ground_check: { enabled: false, confidence_threshold: 0.8, max_retries: 2 } };
        ad.settings.max_iterations = val as number;
      }
      break;
    case 'timeout':
      if (isAgent.value) {
        const ad = data as AgentConfig;
        if (!ad.settings) ad.settings = { max_iterations: 10, verbose: false, timeout: 300, reflection: { enabled: false, mode: 'after_tool', frequency: 'always' }, ground_check: { enabled: false, confidence_threshold: 0.8, max_retries: 2 } };
        ad.settings.timeout = val as number;
      }
      break;
  }
  markInherited(field, true);
}

// When user explicitly changes a field value
function handleFieldOverride(field: string, value: unknown) {
  const inherited = getInheritedValue(field);
  // If value matches the inherited source, keep it inherited
  markInherited(field, value !== undefined && value === inherited);
}

// Check if a field has a source to inherit from
function hasInheritSource(field: string): boolean {
  return getInheritedValue(field) !== undefined;
}

// When user types a custom provider not in the list, auto-add to Settings
function handleProviderChange(value: string) {
  if (value && !props.providerNames.includes(value)) {
    emit('add-provider', value);
  }
  // Set _providerId when provider is selected
  if (isAgent.value || isOrchestrator.value) {
    const data = localData.value as AgentConfig | OrchestratorConfig;
    setProviderByName(data, value);
    // Auto-fill model from provider's default_model
    const provDef = props.providerMap[value];
    if (provDef?.default_model) {
      // If model is inherited or empty, sync to new provider's default
      if (isInherited('model') || !data.model) {
        data.model = provDef.default_model;
        markInherited('model', true);
      }
    }
  }
  return value;
}

const localData = ref<NodeData>(JSON.parse(JSON.stringify(props.nodeData)));

watch(() => props.nodeId, () => {
  suppressApply = true;
  localData.value = JSON.parse(JSON.stringify(props.nodeData));
  // Resolve provider name from _providerId (in case provider was renamed)
  if (isAgent.value || isOrchestrator.value) {
    const data = localData.value as AgentConfig | OrchestratorConfig;
    if (data._providerId) {
      data.provider = resolvedProviderName(data);
    }
    // Initialize inherited state if not set
    if (!data._inherited) {
      data._inherited = {};
      // Auto-detect inherited state by comparing with source values
      for (const field of ['model', 'temperature', 'max_tokens', 'max_iterations', 'timeout']) {
        const src = getInheritedValue(field);
        if (src === undefined) continue;
        let current: unknown;
        switch (field) {
          case 'model': current = data.model; break;
          case 'temperature': current = data.model_config?.temperature; break;
          case 'max_tokens': current = data.model_config?.max_tokens; break;
          case 'max_iterations': current = (data as AgentConfig).settings?.max_iterations; break;
          case 'timeout': current = (data as AgentConfig).settings?.timeout; break;
        }
        data._inherited[field] = !current || current === src;
      }
    }
  }
  nextTick(() => { suppressApply = false; });
  // Reset all section toggles so stale open sections don't render against new data
  showReflection.value = false;
  showGroundCheck.value = false;
  showContext.value = false;
  showRetrieval.value = false;
  showModelConfig.value = false;
  showSandbox.value = false;
  showRouting.value = false;
  showHandoff.value = false;
  showPipeline.value = false;
});

const isAgent = computed(() => props.nodeType === 'agent');
const isTool = computed(() => props.nodeType === 'tool');
const isSkill = computed(() => props.nodeType === 'skill');
const isOrchestrator = computed(() => props.nodeType === 'orchestrator');
const isGroup = computed(() => props.nodeType === 'group');

// Group step defaults: get/set per-step config keyed by _agentId
function getStepDefault(agentId: string, field: string): unknown {
  const step = groupData.value.stepDefaults?.[agentId];
  if (!step) return undefined;
  return (step as unknown as Record<string, unknown>)[field];
}

function setStepDefault(agentId: string, field: string, value: unknown) {
  if (!groupData.value.stepDefaults) groupData.value.stepDefaults = {};
  if (!groupData.value.stepDefaults[agentId]) {
    groupData.value.stepDefaults[agentId] = { agentName: '' };
  }
  (groupData.value.stepDefaults[agentId] as unknown as Record<string, unknown>)[field] = value;
  // Set agentName from children
  const child = props.groupChildren.find(c => (c._agentId || c.id) === agentId);
  if (child) groupData.value.stepDefaults[agentId].agentName = child.name;
}

// --- Orchestrator step override helpers (Phase 2: inherit/override) ---
const hasConnectedGroup = computed(() => !!(orchestratorData.value as OrchestratorConfig)?._connectedGroupId);

// Detect stale overrides — keys in _stepOverrides that no longer match any pipeline step
const staleOverrideIds = computed(() => {
  const orch = localData.value as OrchestratorConfig;
  if (!orch._stepOverrides || !orch.pipeline?.steps) return [] as string[];
  const activeIds = new Set<string>();
  function collectIds(steps: import('../../types').PipelineStep[]) {
    for (const s of steps) {
      if (s._agentId) activeIds.add(s._agentId);
      if (s._groupId) activeIds.add(s._groupId);
      if (s.steps) collectIds(s.steps);
    }
  }
  collectIds(orch.pipeline.steps);
  return Object.keys(orch._stepOverrides).filter(id => !activeIds.has(id));
});

function cleanStaleOverrides() {
  const orch = localData.value as OrchestratorConfig;
  if (!orch._stepOverrides) return;
  for (const id of staleOverrideIds.value) {
    delete orch._stepOverrides[id];
  }
}

function getStepMergedValue(agentId: string, field: string): unknown {
  const overrides = (localData.value as OrchestratorConfig)._stepOverrides;
  const override = overrides?.[agentId];
  if (override && (override as Record<string, unknown>)[field] !== undefined) {
    return (override as Record<string, unknown>)[field];
  }
  const base = props.groupStepDefaults[agentId];
  if (!base) return undefined;
  return (base as unknown as Record<string, unknown>)[field];
}

function isStepInherited(agentId: string, field: string): boolean {
  const overrides = (localData.value as OrchestratorConfig)._stepOverrides;
  const override = overrides?.[agentId];
  return !override || (override as Record<string, unknown>)[field] === undefined;
}

function setStepOverride(agentId: string, field: string, value: unknown) {
  const orch = localData.value as OrchestratorConfig;
  if (!orch._stepOverrides) orch._stepOverrides = {};
  if (!orch._stepOverrides[agentId]) orch._stepOverrides[agentId] = {};
  (orch._stepOverrides[agentId] as Record<string, unknown>)[field] = value;
}

function resetStepOverride(agentId: string, field: string) {
  const orch = localData.value as OrchestratorConfig;
  if (!orch._stepOverrides?.[agentId]) return;
  delete (orch._stepOverrides[agentId] as Record<string, unknown>)[field];
  if (Object.keys(orch._stepOverrides[agentId]).length === 0) {
    delete orch._stepOverrides[agentId];
  }
}

const groupData = computed({
  get: () => localData.value as GroupConfig,
  set: (val) => { localData.value = val; }
});

const agentData = computed({
  get: () => localData.value as AgentConfig,
  set: (val) => { localData.value = val; }
});

const toolData = computed({
  get: () => localData.value as ToolConfig,
  set: (val) => { localData.value = val; }
});

const skillData = computed({
  get: () => localData.value as SkillConfig,
  set: (val) => { localData.value = val; }
});

const orchestratorData = computed({
  get: () => localData.value as OrchestratorConfig,
  set: (val) => { localData.value = val; }
});

// --- Options ---

const providerModels: Record<string, string[]> = {
  openai: ['gpt-4o', 'gpt-4o-mini', 'o3-mini', 'o1', 'o1-mini', 'gpt-4-turbo', 'gpt-4.1', 'gpt-4.1-mini', 'gpt-4.1-nano'],
  anthropic: ['claude-opus-4-6', 'claude-sonnet-4-6', 'claude-haiku-4-5-20251001', 'claude-sonnet-4-20250514', 'claude-3-5-haiku-20241022'],
  gemini: ['gemini-2.5-flash', 'gemini-2.5-pro', 'gemini-2.0-flash', 'gemini-1.5-pro', 'gemini-1.5-flash', 'gemini-3.1-flash-lite-preview'],
  ollama: ['llama3.3', 'llama3.1', 'llama3', 'codellama', 'mistral', 'mixtral', 'phi3', 'qwen2.5', 'deepseek-r1', 'gemma2'],
  // LiteLLM proxy — common model aliases (type any custom name for your proxy)
  litellm: ['gpt-4o', 'claude-sonnet-4-20250514', 'gemini-2.5-flash', 'command-r-plus', 'mistral-large-latest'],
};

// Dynamic model discovery from OpenAI-compatible endpoints
const discoveredModels = ref<string[]>([]);
const modelDiscoveryLoading = ref(false);

function isCodex(provider: string): boolean {
  return props.providerMap[provider]?.type === 'codex';
}

async function discoverModels(provider: string) {
  const baseUrl = props.baseUrls[provider] ?? '';
  if (!baseUrl && !isCodex(provider)) {
    discoveredModels.value = [];
    return;
  }
  modelDiscoveryLoading.value = true;
  try {
    const apiKey = props.apiKeys[provider] ?? '';
    const params = isCodex(provider)
      ? new URLSearchParams({ type: 'codex', credentials_file: props.providerMap[provider]?.credentials_file ?? '' })
      : new URLSearchParams({ base_url: baseUrl });
    const headers: HeadersInit = {};
    if (apiKey) headers['X-Provider-Key'] = apiKey;
    const res = await fetch(`/api/providers/models?${params}`, { headers });
    const data = await res.json() as { models?: string[] };
    discoveredModels.value = data.models ?? [];
  } catch {
    discoveredModels.value = [];
  } finally {
    modelDiscoveryLoading.value = false;
  }
}

// Trigger discovery when provider changes on agent or orchestrator node
const activeProvider = computed(() => {
  if (isAgent.value) return agentData.value.provider;
  if (isOrchestrator.value) return orchestratorData.value.provider;
  return null;
});

watch(activeProvider, (provider) => {
  if (provider) {
    discoverModels(provider);
    fetchModelInfo(provider);
  }
}, { immediate: true });

// Model info cache from LiteLLM /model/info
interface ModelInfo {
  max_tokens?: number;
  max_input_tokens?: number;
  supports_vision?: boolean;
  supports_function_calling?: boolean;
  input_cost_per_token?: number;
  output_cost_per_token?: number;
}
const modelInfoMap = ref<Record<string, ModelInfo>>({});

async function fetchModelInfo(provider: string) {
  const baseUrl = props.baseUrls[provider] ?? '';
  if (!baseUrl) return;
  try {
    const apiKey = props.apiKeys[provider] ?? '';
    const params = new URLSearchParams({ base_url: baseUrl });
    const headers: HeadersInit = {};
    if (apiKey) headers['X-Provider-Key'] = apiKey;
    const res = await fetch(`/api/providers/model-info?${params}`, { headers });
    const data = await res.json();
    // LiteLLM returns { data: [{ model_name, model_info: {...} }] }
    const map: Record<string, ModelInfo> = {};
    const items = data?.data ?? [];
    for (const item of items) {
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
  } catch {
    // silent
  }
}

// Auto-populate model_config and vision when model changes and info is available
watch(() => isAgent.value ? agentData.value.model : null, (model) => {
  if (!model || Object.keys(modelInfoMap.value).length === 0) return;
  const info = modelInfoMap.value[model];
  if (!info) return;

  // Auto-set vision
  if (info.supports_vision != null) {
    agentData.value.vision = info.supports_vision;
  }

  // Don't auto-fill max_tokens — the model's max output can exceed context
  // window when input is large. Leave at 0 to let the provider decide.
});

const currentModelInfo = computed(() => {
  if (isAgent.value) return modelInfoMap.value[agentData.value.model] ?? null;
  if (isOrchestrator.value) return modelInfoMap.value[orchestratorData.value.model] ?? null;
  return null;
});

function formatTokens(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(0)}k`;
  return String(n);
}

const toolTypes = [
  { value: 'cli', label: 'CLI Command' },
  { value: 'fs', label: 'File System' },
  { value: 'mcp_server', label: 'MCP Server' },
  { value: 'a2a', label: 'A2A (Agent-to-Agent)' },
];

const agentRoles = [
  { value: 'worker', label: 'Worker' },
  { value: 'supervisor', label: 'Supervisor' },
];

const strategies = [
  { value: 'ReAct', label: 'ReAct (Reason + Act)' },
  { value: 'PlanAndExecute', label: 'Plan and Execute' },
  { value: 'Hierarchical', label: 'Hierarchical' },
  { value: 'Pipeline', label: 'Pipeline (Deterministic)' },
];

const contextStrategies = [
  { value: '', label: '-- Default --' },
  { value: 'full', label: 'Full' },
  { value: 'sliding_window', label: 'Sliding Window' },
  { value: 'step_log', label: 'Step Log' },
  { value: 'auto', label: 'Auto (escalating)' },
];

const reflectionModes = [
  { value: 'after_tool', label: 'After Tool Call' },
  { value: 'before_answer', label: 'Before Answer' },
  { value: 'both', label: 'Both' },
];

const reflectionFrequencies = [
  { value: 'always', label: 'Always' },
  { value: 'on_error', label: 'On Error' },
  { value: 'every_n', label: 'Every N Iterations' },
];

const sandboxTypes = [
  { value: 'local_restricted', label: 'Local Restricted' },
  { value: 'docker', label: 'Docker Container' },
];

const fsOperations = [
  { value: 'read', label: 'Read' },
  { value: 'read_image', label: 'Read image' },
  { value: 'write', label: 'Write' },
  { value: 'list', label: 'List' },
  { value: 'search', label: 'Search' },
  { value: 'tree', label: 'Tree' },
];

// --- Computed helpers ---

function getModelsForProvider(provider: string): string[] {
  return providerModels[provider] || providerModels['openai']!;
}

// If provider has a base_url, show ONLY discovered models (no static fallback)
const hasBaseUrl = computed(() => {
  const provider = activeProvider.value ?? '';
  return !!(props.baseUrls[provider]) || isCodex(provider);
});

const agentModels = computed(() => {
  if (hasBaseUrl.value) return discoveredModels.value;
  return getModelsForProvider(agentData.value.provider);
});
const orchestratorModels = computed(() => {
  if (hasBaseUrl.value) return discoveredModels.value;
  return getModelsForProvider(orchestratorData.value.provider);
});

// Comma-separated string helpers
const skillsString = computed({
  get: () => {
    if (isAgent.value) return agentData.value.skills?.join(', ') || '';
    if (isSkill.value) return skillData.value.tools?.join(', ') || '';
    return '';
  },
  set: (val: string) => {
    const arr = val.split(',').map(t => t.trim()).filter(t => t);
    if (isAgent.value) agentData.value.skills = arr;
    if (isSkill.value) skillData.value.tools = arr;
  }
});


// Chip multi-select helpers
const customToolInput = ref('');
const customSkillInput = ref('');
const customAgentInput = ref('');

function toggleTool(name: string) {
  const arr = agentData.value.tools ?? [];
  const idx = arr.indexOf(name);
  const connecting = idx < 0;
  if (idx >= 0) arr.splice(idx, 1);
  else arr.push(name);
  agentData.value.tools = [...arr];
  emit('wire-toggle', props.nodeId, name, 'tool', connecting);
}

function addCustomTool() {
  const v = customToolInput.value.trim();
  if (!v) return;
  const arr = agentData.value.tools ?? [];
  if (!arr.includes(v)) agentData.value.tools = [...arr, v];
  customToolInput.value = '';
}

function removeTool(name: string) {
  agentData.value.tools = (agentData.value.tools ?? []).filter(t => t !== name);
}

function toggleSkill(name: string) {
  if (isAgent.value) {
    const arr = agentData.value.skills ?? [];
    const idx = arr.indexOf(name);
    const connecting = idx < 0;
    if (idx >= 0) arr.splice(idx, 1);
    else arr.push(name);
    agentData.value.skills = [...arr];
    emit('wire-toggle', props.nodeId, name, 'skill', connecting);
  }
}

function addCustomSkill() {
  const v = customSkillInput.value.trim();
  if (!v) return;
  if (isAgent.value) {
    const arr = agentData.value.skills ?? [];
    if (!arr.includes(v)) agentData.value.skills = [...arr, v];
  }
  customSkillInput.value = '';
}

function removeSkill(name: string) {
  if (isAgent.value) {
    agentData.value.skills = (agentData.value.skills ?? []).filter(s => s !== name);
  }
}

function toggleAgent(name: string) {
  const arr = orchestratorData.value.agents ?? [];
  const idx = arr.indexOf(name);
  const connecting = idx < 0;
  if (idx >= 0) arr.splice(idx, 1);
  else arr.push(name);
  orchestratorData.value.agents = [...arr];
  emit('wire-toggle', props.nodeId, name, 'agent', connecting);
}

function addCustomAgent() {
  const v = customAgentInput.value.trim();
  if (!v) return;
  const arr = orchestratorData.value.agents ?? [];
  if (!arr.includes(v)) orchestratorData.value.agents = [...arr, v];
  customAgentInput.value = '';
}

function removeAgent(name: string) {
  orchestratorData.value.agents = (orchestratorData.value.agents ?? []).filter(a => a !== name);
}

// Validation: tools/skills not on canvas
const missingTools = computed(() =>
  (agentData.value.tools ?? []).filter(t => props.toolNames.length > 0 && !props.toolNames.includes(t))
);
const missingSkills = computed(() =>
  (agentData.value.skills ?? []).filter(s => props.skillNames.length > 0 && !props.skillNames.includes(s))
);
const missingAgents = computed(() => {
  // For pipeline strategy, agents are managed through steps — don't validate flat list
  if (orchestratorData.value.strategy === 'Pipeline' && orchestratorData.value.pipeline?.steps?.length) return [];
  return (orchestratorData.value.agents ?? []).filter(a => props.agentNames.length > 0 && !props.agentNames.includes(a));
});

const allowedPathsString = computed({
  get: () => {
    if (isTool.value) return toolData.value.allowed_paths?.join(', ') || '';
    return '';
  },
  set: (val: string) => {
    if (isTool.value) toolData.value.allowed_paths = val.split(',').map(t => t.trim()).filter(t => t);
  }
});

const sandboxAllowedPathsString = computed({
  get: () => toolData.value.sandbox?.allowed_paths?.join(', ') || '',
  set: (val: string) => {
    if (toolData.value.sandbox) {
      toolData.value.sandbox.allowed_paths = val.split(',').map(t => t.trim()).filter(t => t);
    }
  }
});

const envString = computed({
  get: () => {
    if (!toolData.value.env) return '';
    return Object.entries(toolData.value.env).map(([k, v]) => `${k}=${v}`).join(', ');
  },
  set: (val: string) => {
    const env: Record<string, string> = {};
    val.split(',').map(s => s.trim()).filter(s => s).forEach(pair => {
      const idx = pair.indexOf('=');
      if (idx > 0) env[pair.slice(0, idx).trim()] = pair.slice(idx + 1).trim();
    });
    toolData.value.env = Object.keys(env).length ? env : undefined;
  }
});

const exitCodesString = computed({
  get: () => toolData.value.allowed_exit_codes?.join(', ') || '',
  set: (val: string) => {
    const codes = val.split(',').map(s => parseInt(s.trim(), 10)).filter(n => !isNaN(n));
    toolData.value.allowed_exit_codes = codes.length ? codes : undefined;
  }
});

// Toggle helpers for nested settings
function ensureAgentSettings() {
  if (!agentData.value.settings) {
    agentData.value.settings = {
      max_iterations: 10,
      verbose: false,
      timeout: 300,
      reflection: { enabled: false, mode: 'after_tool', frequency: 'always' },
      ground_check: { enabled: false, confidence_threshold: 0.7, max_retries: 1 },
      context: { retrieval: {} },
    };
    return;
  }
  const s = agentData.value.settings;
  if (!s.reflection) s.reflection = { enabled: false, mode: 'after_tool', frequency: 'always' };
  if (!s.ground_check) s.ground_check = { enabled: false, confidence_threshold: 0.7, max_retries: 1 };
  if (!s.context) s.context = { retrieval: {} };
  else if (!s.context.retrieval) s.context.retrieval = {};
}

// --- DAG depends_on helpers for group step editor ---
function otherStepNames(idx: number): string[] {
  return props.groupChildren.filter((_, i) => i !== idx).map(c => c.name);
}

function isStepDep(idx: number, name: string): boolean {
  const child = props.groupChildren[idx];
  if (!child) return false;
  const agentId = child._agentId || child.id;
  const deps = groupData.value.stepDefaults?.[agentId]?.dependsOn;
  return deps?.includes(name) ?? false;
}

function toggleStepDep(idx: number, name: string) {
  const child = props.groupChildren[idx];
  if (!child) return;
  const agentId = child._agentId || child.id;
  if (!groupData.value.stepDefaults) groupData.value.stepDefaults = {};
  if (!groupData.value.stepDefaults[agentId]) {
    groupData.value.stepDefaults[agentId] = { agentName: child.name };
  }
  const step = groupData.value.stepDefaults[agentId];
  if (!step.dependsOn) step.dependsOn = [];
  const i = step.dependsOn.indexOf(name);
  if (i >= 0) step.dependsOn.splice(i, 1);
  else step.dependsOn.push(name);
}

// --- Provider Fallback Chain helpers ---
const fallbackChain = computed({
  get: () => (agentData.value.providers ?? []).map(p => ({ name: p.name, model: p.model ?? '' })),
  set: (val) => { agentData.value.providers = val.map(v => ({ name: v.name, model: v.model || undefined })); }
});

const providerOptions = computed(() => props.providerNames);

function addFallback() {
  const arr = agentData.value.providers ?? [];
  agentData.value.providers = [...arr, { name: '', model: '' }];
}

function removeFallback(i: number) {
  const arr = [...(agentData.value.providers ?? [])];
  arr.splice(i, 1);
  agentData.value.providers = arr;
}

function updateFallback(i: number, field: 'name' | 'model', val: string) {
  const arr = [...(agentData.value.providers ?? [])];
  if (arr[i]) {
    arr[i] = { ...arr[i], [field]: val };
    agentData.value.providers = arr;
  }
}

// --- MCP Server args string helper ---
const mcpArgsStr = computed({
  get: () => (toolData.value.args ?? []).join(', '),
  set: (val: string) => {
    toolData.value.args = val.split(',').map(s => s.trim()).filter(s => s);
  }
});

// --- A2A agent name options ---
const agentNameOptions = computed(() => props.agentNames);

// --- Pipeline Synthesis helpers ---
const synthEnabled = computed({
  get: () => orchestratorData.value.pipeline?.synthesis ?? false,
  set: (val) => {
    if (!orchestratorData.value.pipeline) orchestratorData.value.pipeline = { steps: [] };
    orchestratorData.value.pipeline.synthesis = val;
  }
});
const synthPrompt = computed({
  get: () => orchestratorData.value.pipeline?.synthesis_prompt ?? '',
  set: (val) => {
    if (!orchestratorData.value.pipeline) orchestratorData.value.pipeline = { steps: [] };
    orchestratorData.value.pipeline.synthesis_prompt = val;
  }
});

// --- Retrieval computed helpers ---
const retrievalEnabled = computed({
  get: () => agentData.value.settings?.context?.retrieval?.enabled ?? false,
  set: (val) => {
    ensureAgentSettings();
    agentData.value.settings!.context!.retrieval!.enabled = val;
  }
});
const retrievalTopK = computed({
  get: () => agentData.value.settings?.context?.retrieval?.top_k ?? 5,
  set: (val) => {
    ensureAgentSettings();
    agentData.value.settings!.context!.retrieval!.top_k = val;
  }
});
const retrievalEmbeddingProvider = computed({
  get: () => agentData.value.settings?.context?.retrieval?.embedding_provider ?? '',
  set: (val) => {
    ensureAgentSettings();
    agentData.value.settings!.context!.retrieval!.embedding_provider = val;
  }
});
const retrievalEmbeddingModel = computed({
  get: () => agentData.value.settings?.context?.retrieval?.embedding_model ?? '',
  set: (val) => {
    ensureAgentSettings();
    agentData.value.settings!.context!.retrieval!.embedding_model = val;
  }
});
const retrievalEmbeddingUrl = computed({
  get: () => agentData.value.settings?.context?.retrieval?.embedding_url ?? '',
  set: (val) => {
    ensureAgentSettings();
    agentData.value.settings!.context!.retrieval!.embedding_url = val;
  }
});

function ensureModelConfig(data: AgentConfig | OrchestratorConfig) {
  if (!data.model_config) {
    data.model_config = { temperature: 0.7, max_tokens: 4096 };
  }
}

function ensureSandbox() {
  if (!toolData.value.sandbox) {
    toolData.value.sandbox = { type: 'local_restricted', resource_limits: { cpu_limit: '', memory_limit: '', timeout_sec: 30 } };
  } else if (!toolData.value.sandbox.resource_limits) {
    toolData.value.sandbox.resource_limits = { cpu_limit: '', memory_limit: '', timeout_sec: 30 };
  }
}

function ensureRouting() {
  if (!orchestratorData.value.routing) {
    orchestratorData.value.routing = { auto_delegate_tools: true };
  }
}

function ensureHandoff() {
  if (!orchestratorData.value.handoff) {
    orchestratorData.value.handoff = { include_context: true, max_context_length: 6000, allow_cross_agent_calls: false };
  }
}

// Section collapse state
const showReflection = ref(false);
const showGroundCheck = ref(false);
const showContext = ref(false);
const showRetrieval = ref(false);
const showModelConfig = ref(false);
const showSandbox = ref(false);
const showRouting = ref(false);
const showHandoff = ref(false);
const showPipeline = ref(false);

// Pipeline helpers
const pipelineStepTypes = [
  { value: 'sequential', label: 'Sequential (single agent)' },
  { value: 'parallel', label: 'Parallel (concurrent agents)' },
  { value: 'loop', label: 'Loop (repeat until condition)' },
];

function ensurePipeline() {
  if (!orchestratorData.value.pipeline) {
    orchestratorData.value.pipeline = { steps: [] };
  }
}

function addPipelineStep() {
  ensurePipeline();
  orchestratorData.value.pipeline!.steps.push({ name: '', agent: '', task: '' });
}

function addParallelSubStep(step: PipelineStep) {
  if (!step.steps) step.steps = [];
  step.steps.push({ name: '', agent: '', task: '' });
}

function removePipelineStep(index: number) {
  orchestratorData.value.pipeline?.steps.splice(index, 1);
}

function removeSubStep(step: PipelineStep, index: number) {
  step.steps?.splice(index, 1);
}

// Auto-apply changes to canvas (debounced, skip if resetting from prop)
let applyTimer: ReturnType<typeof setTimeout> | null = null;
let suppressApply = false;
let lastEmittedJSON = '';

watch(localData, () => {
  if (suppressApply) return;
  if (applyTimer) clearTimeout(applyTimer);
  applyTimer = setTimeout(() => {
    const json = JSON.stringify(localData.value);
    lastEmittedJSON = json;
    emit('update', props.nodeId, JSON.parse(json));
  }, 300);
}, { deep: true });

// Watch for external data changes on the same node (e.g., pipeline sync, provider rename)
// Skip if the change was triggered by our own emit (compare with lastEmittedJSON)
watch(() => props.nodeData, (newData) => {
  const newJSON = JSON.stringify(newData);
  if (newJSON === lastEmittedJSON) return;
  suppressApply = true;
  localData.value = JSON.parse(newJSON);
  nextTick(() => { suppressApply = false; });
}, { deep: true });

function handleKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close');
}

// --- Breakpoints & Event History (injected from VisualBuilder) ---
const breakpoints = inject<Ref<Breakpoint[]>>('breakpoints', ref([]));
const injectedSetBreakpoint = inject<(eventType: string, agentName: string) => Promise<void>>('setBreakpoint');
const injectedClearBreakpoint = inject<(eventType: string, agentName: string) => Promise<void>>('clearBreakpoint');
const liveEvents = inject<Ref<AgentEvent[]>>('liveEvents', ref([]));

const CHECKPOINT_TYPES = [
  { value: 'pre_thought', label: 'Before Thought' },
  { value: 'pre_tool', label: 'Before Tool' },
  { value: 'pre_reflection', label: 'Before Reflection' },
  { value: 'pre_ground_check', label: 'Before Ground Check' },
  { value: 'pre_agent', label: 'Before Agent' },
  { value: 'pre_pipeline_step', label: 'Before Pipeline Step' },
] as const;

const currentNodeName = computed(() => {
  if (isAgent.value) return agentData.value.name;
  if (isOrchestrator.value) return orchestratorData.value.name;
  return '';
});

const nodeBreakpoints = computed(() =>
  breakpoints.value.filter(b => b.agent_name === currentNodeName.value || b.agent_name === '*')
);

function hasCheckpoint(eventType: string): boolean {
  return nodeBreakpoints.value.some(b => b.event_type === eventType);
}

async function toggleCheckpoint(eventType: string) {
  const name = currentNodeName.value;
  if (!name) return;
  if (hasCheckpoint(eventType)) {
    await injectedClearBreakpoint?.(eventType, name);
  } else {
    await injectedSetBreakpoint?.(eventType, name);
  }
}

// Tool-node breakpoint
const injectedToggleToolBreakpoint = inject<(toolName: string) => void>('toggleToolBreakpoint');
const toolHasBreakpoint = computed(() => {
  if (!isTool.value) return false;
  // Check if any pre_tool breakpoint covers a parent agent of this tool
  return breakpoints.value.some(b => b.event_type === 'pre_tool');
});

// Filtered events for current node
const showEventHistory = ref(false);
const nodeEvents = computed(() => {
  const name = currentNodeName.value;
  if (!name) return [];
  return liveEvents.value.filter(e => e.agent_name === name);
});

const expandedEvents = ref<Set<number>>(new Set());
function toggleEventExpand(idx: number) {
  if (expandedEvents.value.has(idx)) expandedEvents.value.delete(idx);
  else expandedEvents.value.add(idx);
}

function eventTypeLabel(type: string): string {
  return type.replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase());
}

function fmtDuration(ms?: number): string {
  if (!ms) return '';
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}
</script>

<template>
  <div class="node-editor" @keydown="handleKeydown">
    <div class="editor-header">
      <h3>
        <span v-if="isAgent">Edit Agent</span>
        <span v-else-if="isTool">Edit Tool</span>
        <span v-else-if="isSkill">Edit Skill</span>
        <span v-else-if="isOrchestrator">Edit Orchestrator</span>
      </h3>
      <button class="close-btn" @click="emit('close')">x</button>
    </div>

    <div class="editor-content">
      <!-- ==================== AGENT EDITOR ==================== -->
      <template v-if="isAgent">
        <div class="form-group">
          <label>Name</label>
          <input v-model="agentData.name" type="text" placeholder="Agent name" :class="{ 'input-error': isDuplicateName }" @blur="onNameBlur" />
          <span v-if="isDuplicateName" class="dup-warn">Name already exists</span>
          <span v-if="nameSuggestion" class="dup-suggest">Renamed to: {{ nameSuggestion }}</span>
        </div>

        <div class="form-row">
          <div class="form-group half">
            <label>Role</label>
            <ComboBox
              :model-value="agentData.role"
              :options="agentRoles"
              placeholder="Select role"
              :allow-custom="false"
              @update:model-value="agentData.role = $event as any"
            />
          </div>
          <div class="form-group half">
            <label>Provider <span v-if="allProviderOptions.length === 0" class="hint-inline">define in Settings</span></label>
            <ComboBox
              :model-value="agentData.provider"
              :options="allProviderOptions"
              :placeholder="allProviderOptions.length > 0 ? 'Select provider' : 'Type to create'"
              @update:model-value="agentData.provider = handleProviderChange($event)"
            />
          </div>
        </div>

        <div class="form-group">
          <label>
            Model
            <span v-if="isInherited('model')" class="inherit-badge" title="Synced with provider default — click to override">
              <span class="inherit-icon">&#x1F517;</span> inherited
            </span>
            <button v-else-if="providerMap[agentData.provider]?.default_model" class="reset-inherit-btn" title="Reset to provider default" @click="resetToInherited('model')">
              reset
            </button>
          </label>
          <ComboBox
            :model-value="agentData.model"
            :options="agentModels"
            placeholder="Select or type model"
            :loading="modelDiscoveryLoading"
            loading-text="Discovering models..."
            :hint="discoveredModels.length > 0 ? `${discoveredModels.length} models discovered` : ''"
            :hint-class="discoveredModels.length > 0 ? 'discovered' : ''"
            @update:model-value="agentData.model = $event; handleFieldOverride('model', $event)"
          />
          <div v-if="currentModelInfo" class="model-info-bar">
            <span v-if="currentModelInfo.max_input_tokens" class="mi-tag">ctx: {{ formatTokens(currentModelInfo.max_input_tokens) }}</span>
            <span v-if="currentModelInfo.max_tokens" class="mi-tag">out: {{ formatTokens(currentModelInfo.max_tokens) }}</span>
            <span v-if="currentModelInfo.supports_vision" class="mi-tag vision">vision</span>
            <span v-if="currentModelInfo.supports_function_calling" class="mi-tag tools">tools</span>
            <span v-if="currentModelInfo.input_cost_per_token" class="mi-tag cost">${{ (currentModelInfo.input_cost_per_token * 1e6).toFixed(2) }}/M in</span>
          </div>
        </div>

        <div class="form-group inline-check">
          <label>
            <input type="checkbox" v-model="agentData.vision" />
            Vision
          </label>
          <span v-if="currentModelInfo?.supports_vision" class="hint-inline discovered">auto-detected</span>
          <span v-else class="hint-inline">Agent can process image inputs</span>
        </div>

        <details class="form-group" v-if="isAgent">
          <summary class="section-toggle compact">Provider Fallback Chain</summary>
          <div v-for="(entry, i) in fallbackChain" :key="i" class="fallback-entry form-row">
            <div class="form-group half">
              <ComboBox :model-value="entry.name" :options="providerOptions" @update:model-value="updateFallback(i, 'name', $event)" placeholder="Provider" />
            </div>
            <div class="form-group half">
              <input type="text" :value="entry.model" @input="updateFallback(i, 'model', ($event.target as HTMLInputElement).value)" class="field-input" placeholder="Model override" />
            </div>
            <button class="remove-btn" @click="removeFallback(i)">x</button>
          </div>
          <button class="btn btn-small" @click="addFallback">+ Add Fallback</button>
        </details>

        <div class="form-group">
          <label>System Prompt</label>
          <textarea v-model="agentData.system_prompt" placeholder="You are a helpful assistant..." rows="4"></textarea>
        </div>

        <div class="form-group">
          <label>Tools <span v-if="toolNames.length > 0" class="hint-inline">click to toggle</span></label>
          <div class="chip-select">
            <span v-if="toolNames.length === 0 && (agentData.tools ?? []).length === 0" class="hint">
              Draw wire from agent to tool to auto-add, or click below
            </span>
            <div class="chip-options">
              <button
                v-for="t in toolNames"
                :key="t"
                class="chip-option"
                :class="{ selected: (agentData.tools ?? []).includes(t) }"
                @click="toggleTool(t)"
              >{{ t }}</button>
            </div>
            <div v-if="(agentData.tools ?? []).some(t => !toolNames.includes(t))" class="chip-selected">
              <span
                v-for="t in (agentData.tools ?? []).filter(t => !toolNames.includes(t))"
                :key="t"
                class="chip-tag custom"
              >{{ t }}<button class="chip-x" @click="removeTool(t)">x</button></span>
            </div>
            <div class="chip-add-row">
              <input
                v-model="customToolInput"
                type="text"
                class="chip-add-input"
                placeholder="Type to add..."
                @keydown.enter.prevent="addCustomTool()"
              />
              <button class="chip-add-btn" :disabled="!customToolInput.trim()" @click="addCustomTool()">+</button>
            </div>
            <span v-if="missingTools.length > 0" class="validation-warn">
              Not on canvas: {{ missingTools.join(', ') }}
            </span>
          </div>
        </div>

        <div class="form-group">
          <label>Skills <span v-if="skillNames.length > 0" class="hint-inline">click to toggle</span></label>
          <div class="chip-select">
            <span v-if="skillNames.length === 0 && (agentData.skills ?? []).length === 0" class="hint">
              Draw wire from agent to skill to auto-add, or click below
            </span>
            <div class="chip-options">
              <button
                v-for="s in skillNames"
                :key="s"
                class="chip-option"
                :class="{ selected: (agentData.skills ?? []).includes(s) }"
                @click="toggleSkill(s)"
              >{{ s }}</button>
            </div>
            <div v-if="(agentData.skills ?? []).some(s => !skillNames.includes(s))" class="chip-selected">
              <span
                v-for="s in (agentData.skills ?? []).filter(s => !skillNames.includes(s))"
                :key="s"
                class="chip-tag custom"
              >{{ s }}<button class="chip-x" @click="removeSkill(s)">x</button></span>
            </div>
            <div class="chip-add-row">
              <input
                v-model="customSkillInput"
                type="text"
                class="chip-add-input"
                placeholder="Type to add..."
                @keydown.enter.prevent="addCustomSkill()"
              />
              <button class="chip-add-btn" :disabled="!customSkillInput.trim()" @click="addCustomSkill()">+</button>
            </div>
            <span v-if="missingSkills.length > 0" class="validation-warn">
              Not on canvas: {{ missingSkills.join(', ') }}
            </span>
          </div>
        </div>

        <!-- Model Config -->
        <div class="section-toggle" @click="showModelConfig = !showModelConfig; ensureModelConfig(agentData)">
          <span>{{ showModelConfig ? 'v' : '>' }} Model Config</span>
        </div>
        <template v-if="showModelConfig && agentData.model_config">
          <div class="form-row">
            <div class="form-group half">
              <label>
                Temperature
                <span v-if="isInherited('temperature')" class="inherit-badge" title="From settings defaults">inherited</span>
                <button v-else-if="hasInheritSource('temperature')" class="reset-inherit-btn" @click="resetToInherited('temperature')">reset</button>
              </label>
              <input v-model.number="agentData.model_config.temperature" type="number" min="0" max="2" step="0.1" @change="handleFieldOverride('temperature', agentData.model_config!.temperature)" />
            </div>
            <div class="form-group half">
              <label>
                Max Tokens
                <span v-if="isInherited('max_tokens')" class="inherit-badge" title="From settings defaults">inherited</span>
                <button v-else-if="hasInheritSource('max_tokens')" class="reset-inherit-btn" @click="resetToInherited('max_tokens')">reset</button>
              </label>
              <input v-model.number="agentData.model_config.max_tokens" type="number" min="1" @change="handleFieldOverride('max_tokens', agentData.model_config!.max_tokens)" />
            </div>
          </div>
          <div class="form-row">
            <div class="form-group half">
              <label>Top P</label>
              <input v-model.number="agentData.model_config.top_p" type="number" min="0" max="1" step="0.1" />
            </div>
            <div class="form-group half">
              <label>Freq Penalty</label>
              <input v-model.number="agentData.model_config.frequency_penalty" type="number" min="-2" max="2" step="0.1" />
            </div>
          </div>
          <div class="form-row">
            <div class="form-group half">
              <label>Presence Penalty</label>
              <input v-model.number="agentData.model_config.presence_penalty" type="number" min="-2" max="2" step="0.1" />
            </div>
            <div class="form-group half">
              <label>Timeout (sec)</label>
              <input v-model.number="agentData.model_config.timeout_sec" type="number" min="0" placeholder="0 = default" />
            </div>
          </div>
          <div class="form-row">
            <div class="form-group half">
              <label>Max Thinking Tokens</label>
              <input v-model.number="agentData.model_config.max_thinking_tokens" type="number" min="0" placeholder="0 = default" />
            </div>
            <div class="form-group half">
              <label style="padding-top:18px" class="checkbox-label">
                <input type="checkbox" v-model="agentData.model_config.no_stream_tools" />
                No Stream Tools
              </label>
            </div>
          </div>
          <div class="form-group">
            <label class="checkbox-label">
              <input type="checkbox" v-model="agentData.model_config.thinking_offload" />
              Thinking Offload (Anthropic extended thinking)
            </label>
          </div>
        </template>

        <!-- Agent Settings -->
        <div class="section-toggle" @click="ensureAgentSettings()">
          <span>Agent Settings</span>
        </div>
        <template v-if="agentData.settings">
          <div class="form-row">
            <div class="form-group half">
              <label>
                Max Iterations
                <span v-if="isInherited('max_iterations')" class="inherit-badge">inherited</span>
                <button v-else-if="hasInheritSource('max_iterations')" class="reset-inherit-btn" @click="resetToInherited('max_iterations')">reset</button>
              </label>
              <input v-model.number="agentData.settings.max_iterations" type="number" min="1" max="100" @change="handleFieldOverride('max_iterations', agentData.settings!.max_iterations)" />
            </div>
            <div class="form-group half">
              <label>
                Timeout (sec)
                <span v-if="isInherited('timeout')" class="inherit-badge">inherited</span>
                <button v-else-if="hasInheritSource('timeout')" class="reset-inherit-btn" @click="resetToInherited('timeout')">reset</button>
              </label>
              <input v-model.number="agentData.settings.timeout" type="number" min="1" @change="handleFieldOverride('timeout', agentData.settings!.timeout)" />
            </div>
          </div>
          <div class="form-row">
            <div class="form-group half">
              <label>Max Total Tokens <span class="hint">(budget)</span></label>
              <input v-model.number="agentData.settings.max_total_tokens" type="number" min="0" placeholder="0 = unlimited" />
            </div>
            <div class="form-group half">
              <label>Max Cost ($) <span class="hint">(budget)</span></label>
              <input v-model.number="agentData.settings.max_cost" type="number" min="0" step="0.01" placeholder="0 = unlimited" />
            </div>
          </div>
          <div class="form-group">
            <label class="checkbox-label">
              <input type="checkbox" v-model="agentData.settings.verbose" />
              Verbose logging
            </label>
          </div>

          <!-- Reflection -->
          <div class="section-toggle sub" @click="showReflection = !showReflection; ensureAgentSettings()">
            <span>{{ showReflection ? 'v' : '>' }} Reflection</span>
          </div>
          <template v-if="showReflection && agentData.settings?.reflection">
            <div class="form-group">
              <label class="checkbox-label">
                <input type="checkbox" v-model="agentData.settings.reflection.enabled" />
                Enable Reflection
              </label>
            </div>
            <template v-if="agentData.settings.reflection.enabled">
              <div class="form-row">
                <div class="form-group half">
                  <label>Mode</label>
                  <ComboBox
                    :model-value="agentData.settings.reflection.mode"
                    :options="reflectionModes"
                    placeholder="Select mode"
                    :allow-custom="false"
                    @update:model-value="agentData.settings!.reflection!.mode = $event as any"
                  />
                </div>
                <div class="form-group half">
                  <label>Frequency</label>
                  <ComboBox
                    :model-value="agentData.settings.reflection.frequency"
                    :options="reflectionFrequencies"
                    placeholder="Select frequency"
                    :allow-custom="false"
                    @update:model-value="agentData.settings!.reflection!.frequency = $event as any"
                  />
                </div>
              </div>
              <div v-if="agentData.settings.reflection.frequency === 'every_n'" class="form-group">
                <label>Every N</label>
                <input v-model.number="agentData.settings.reflection.every_n" type="number" min="1" />
              </div>
              <div class="form-group">
                <label>Custom Prompt</label>
                <textarea v-model="agentData.settings.reflection.prompt" rows="2" placeholder="Optional custom reflection prompt..."></textarea>
              </div>
            </template>
          </template>

          <!-- Ground Check -->
          <div class="section-toggle sub" @click="showGroundCheck = !showGroundCheck; ensureAgentSettings()">
            <span>{{ showGroundCheck ? 'v' : '>' }} Ground Check</span>
          </div>
          <template v-if="showGroundCheck && agentData.settings?.ground_check">
            <div class="form-group">
              <label class="checkbox-label">
                <input type="checkbox" v-model="agentData.settings.ground_check.enabled" />
                Enable Ground Check
              </label>
            </div>
            <template v-if="agentData.settings.ground_check.enabled">
              <div class="form-row">
                <div class="form-group half">
                  <label>Confidence Threshold</label>
                  <input v-model.number="agentData.settings.ground_check.confidence_threshold" type="number" min="0" max="1" step="0.1" />
                </div>
                <div class="form-group half">
                  <label>Max Retries</label>
                  <input v-model.number="agentData.settings.ground_check.max_retries" type="number" min="0" max="5" />
                </div>
              </div>
              <div class="form-group">
                <label>Custom Prompt</label>
                <textarea v-model="agentData.settings.ground_check.prompt" rows="2" placeholder="Optional custom validation prompt..."></textarea>
              </div>
            </template>
          </template>

          <!-- Context -->
          <div class="section-toggle sub" @click="showContext = !showContext; ensureAgentSettings()">
            <span>{{ showContext ? 'v' : '>' }} Context</span>
          </div>
          <template v-if="showContext && agentData.settings?.context">
            <div class="form-row">
              <div class="form-group half">
                <label>Strategy</label>
                <ComboBox
                  :model-value="agentData.settings!.context!.strategy ?? ''"
                  :options="contextStrategies"
                  placeholder="Select strategy"
                  :allow-custom="false"
                  @update:model-value="agentData.settings!.context!.strategy = $event"
                />
              </div>
              <div class="form-group half">
                <label>Window Size</label>
                <input v-model.number="agentData.settings.context.window_size" type="number" min="1" placeholder="20" />
              </div>
            </div>
            <div class="form-row">
              <div class="form-group half">
                <label>Keep Recent</label>
                <input v-model.number="agentData.settings.context.keep_recent" type="number" min="1" placeholder="3" />
              </div>
              <div class="form-group half">
                <label>Max Tool Output</label>
                <input v-model.number="agentData.settings.context.max_tool_output" type="number" min="0" placeholder="2000" />
              </div>
            </div>
            <div class="form-group">
              <label class="checkbox-label">
                <input type="checkbox" v-model="agentData.settings.context.fence_outputs" />
                Fence tool outputs (markdown blocks)
              </label>
            </div>
            <template v-if="agentData.settings.context.strategy === 'auto'">
              <div class="form-row">
                <div class="form-group half">
                  <label>Full→Window at (msgs)</label>
                  <input v-model.number="agentData.settings.context.auto_full_threshold" type="number" min="1" placeholder="30" />
                </div>
                <div class="form-group half">
                  <label>Window→StepLog at (msgs)</label>
                  <input v-model.number="agentData.settings.context.auto_compress_threshold" type="number" min="1" placeholder="60" />
                </div>
              </div>
              <div class="form-row">
                <div class="form-group half">
                  <label>Budget threshold (0–1)</label>
                  <input v-model.number="agentData.settings.context.context_budget_threshold" type="number" min="0" max="1" step="0.05" placeholder="0.75" />
                </div>
                <div class="form-group half">
                  <label>Retrieval threshold (0–1)</label>
                  <input v-model.number="agentData.settings.context.context_retrieval_threshold" type="number" min="0" max="1" step="0.05" placeholder="0.90" />
                </div>
              </div>
            </template>
            <!-- Retrieval sub-section -->
            <div class="section-toggle sub" @click="showRetrieval = !showRetrieval; ensureAgentSettings()">
              <span>{{ showRetrieval ? 'v' : '>' }} Retrieval</span>
            </div>
            <template v-if="showRetrieval && agentData.settings?.context?.retrieval">
              <div class="form-group">
                <label class="checkbox-label">
                  <input type="checkbox" v-model="retrievalEnabled" />
                  Enable semantic retrieval
                </label>
              </div>
              <template v-if="retrievalEnabled">
                <div class="form-group">
                  <label>Top K</label>
                  <input type="number" v-model.number="retrievalTopK" min="1" max="20" />
                </div>
                <div class="form-group">
                  <label>Embedding Provider</label>
                  <ComboBox :model-value="retrievalEmbeddingProvider" :options="[{value:'',label:'BM25 (default)'},{value:'ollama',label:'Ollama'},{value:'openai',label:'OpenAI'},{value:'gemini',label:'Gemini'}]" @update:model-value="retrievalEmbeddingProvider = $event" />
                </div>
                <div class="form-group">
                  <label>Embedding Model</label>
                  <input type="text" v-model="retrievalEmbeddingModel" placeholder="nomic-embed-text" />
                </div>
                <div class="form-group">
                  <label>Embedding URL</label>
                  <input type="text" v-model="retrievalEmbeddingUrl" placeholder="http://localhost:11434" />
                </div>
                <div class="form-group">
                  <label>Error Bias <span class="hint">(weight for failed steps, default 2.0)</span></label>
                  <input v-model.number="agentData.settings.context.retrieval.error_bias" type="number" min="0" step="0.1" placeholder="2.0" />
                </div>
              </template>
            </template>
          </template>
        </template>
      </template>

      <!-- ==================== TOOL EDITOR ==================== -->
      <template v-else-if="isTool">
        <div class="form-group">
          <label>Name</label>
          <input v-model="toolData.name" type="text" placeholder="tool_name" :class="{ 'input-error': isDuplicateName }" @blur="onNameBlur" />
          <span v-if="isDuplicateName" class="dup-warn">Name already exists</span>
          <span v-if="nameSuggestion" class="dup-suggest">Renamed to: {{ nameSuggestion }}</span>
        </div>

        <div class="form-group">
          <label>Type</label>
          <ComboBox
            :model-value="toolData.type"
            :options="toolTypes"
            placeholder="Select type"
            :allow-custom="false"
            @update:model-value="toolData.type = $event as any"
          />
        </div>

        <div class="form-group">
          <label>Description</label>
          <textarea v-model="toolData.description" placeholder="Describe what this tool does..." rows="2"></textarea>
        </div>

        <div v-if="toolData.type === 'cli'" class="form-group">
          <label>Command</label>
          <input v-model="toolData.command" type="text" placeholder="git status" />
        </div>

        <div v-if="toolData.type === 'fs'" class="form-group">
          <label>Operation</label>
          <ComboBox
            :model-value="toolData.operation ?? ''"
            :options="fsOperations"
            placeholder="Select operation"
            :allow-custom="false"
            @update:model-value="toolData.operation = $event"
          />
        </div>

        <!-- MCP Server fields -->
        <template v-if="toolData.type === 'mcp_server'">
          <div class="form-group">
            <label>Command</label>
            <input type="text" v-model="toolData.command" class="field-input" placeholder="npx" />
          </div>
          <div class="form-group">
            <label>Args</label>
            <input type="text" v-model="mcpArgsStr" class="field-input" placeholder="arg1, arg2, ..." />
          </div>
          <div class="form-group">
            <label>Transport</label>
            <ComboBox :model-value="toolData.transport ?? 'stdio'" :options="[{value:'stdio',label:'stdio'},{value:'http',label:'http'}]" :allow-custom="false" @update:model-value="toolData.transport = $event as any" />
          </div>
          <div v-if="toolData.transport === 'http'" class="form-group">
            <label>URL</label>
            <input type="text" v-model="toolData.url" class="field-input" placeholder="http://localhost:8080" />
          </div>
        </template>

        <!-- A2A fields -->
        <template v-if="toolData.type === 'a2a'">
          <div class="form-group">
            <label>Delegate to Agent</label>
            <ComboBox :model-value="toolData.agent ?? ''" :options="agentNameOptions" placeholder="Select agent" @update:model-value="toolData.agent = $event" />
          </div>
        </template>

        <div class="form-group">
          <label>Allowed Paths (comma-separated)</label>
          <input v-model="allowedPathsString" type="text" placeholder="./, /var/log/" />
        </div>

        <!-- Additional tool fields -->
        <div v-if="toolData.type === 'cli'" class="form-group">
          <label>Executable</label>
          <input v-model="toolData.executable" type="text" placeholder="/usr/bin/python3" />
        </div>

        <div v-if="toolData.type === 'cli'" class="form-group">
          <label>Script</label>
          <textarea v-model="toolData.script" rows="3" placeholder="Inline script content..."></textarea>
        </div>

        <div class="form-group">
          <label>Method</label>
          <input v-model="toolData.method" type="text" placeholder="GET, POST, etc." />
        </div>

        <div class="form-group">
          <label>Working Directory</label>
          <input v-model="toolData.working_dir" type="text" placeholder="./workspace/" />
        </div>

        <div class="form-group">
          <label>Timeout (sec)</label>
          <input v-model.number="toolData.timeout_seconds" type="number" min="0" placeholder="30" />
        </div>

        <div class="form-group">
          <label>Environment Variables (KEY=VAL, comma-separated)</label>
          <input v-model="envString" type="text" placeholder="NODE_ENV=production, DEBUG=1" />
        </div>

        <div class="form-group">
          <label>Allowed Exit Codes (comma-separated)</label>
          <input v-model="exitCodesString" type="text" placeholder="0, 1" />
        </div>

        <!-- Sandbox -->
        <div class="section-toggle" @click="showSandbox = !showSandbox; ensureSandbox()">
          <span>{{ showSandbox ? 'v' : '>' }} Sandbox</span>
        </div>
        <template v-if="showSandbox && toolData.sandbox">
          <div class="form-group">
            <label>Sandbox Type</label>
            <ComboBox
              :model-value="toolData.sandbox!.type ?? ''"
              :options="sandboxTypes"
              placeholder="Select sandbox"
              :allow-custom="false"
              @update:model-value="toolData.sandbox!.type = $event as any"
            />
          </div>
          <template v-if="toolData.sandbox.type === 'docker'">
            <div class="form-group">
              <label>Docker Image</label>
              <input v-model="toolData.sandbox.image" type="text" placeholder="alpine:latest" />
            </div>
            <div class="form-group">
              <label class="checkbox-label">
                <input type="checkbox" v-model="toolData.sandbox.mount_workdir" />
                Mount Working Directory
              </label>
            </div>
            <div class="form-group">
              <label class="checkbox-label">
                <input type="checkbox" v-model="toolData.sandbox.network_isolated" />
                Network Isolated
              </label>
            </div>
          </template>
          <div class="form-group">
            <label>Sandbox Allowed Paths</label>
            <input v-model="sandboxAllowedPathsString" type="text" placeholder="./" />
          </div>
          <template v-if="toolData.sandbox.resource_limits">
            <div class="form-row">
              <div class="form-group half">
                <label>CPU Limit</label>
                <input v-model="toolData.sandbox.resource_limits.cpu_limit" type="text" placeholder="0.5" />
              </div>
              <div class="form-group half">
                <label>Memory Limit</label>
                <input v-model="toolData.sandbox.resource_limits.memory_limit" type="text" placeholder="128m" />
              </div>
            </div>
            <div class="form-group">
              <label>Timeout (sec)</label>
              <input v-model.number="toolData.sandbox.resource_limits.timeout_sec" type="number" min="1" />
            </div>
          </template>
        </template>
      </template>

      <!-- ==================== SKILL EDITOR ==================== -->
      <template v-else-if="isSkill">
        <div class="form-group">
          <label>Name</label>
          <input v-model="skillData.name" type="text" placeholder="Skill name" :class="{ 'input-error': isDuplicateName }" @blur="onNameBlur" />
          <span v-if="isDuplicateName" class="dup-warn">Name already exists</span>
          <span v-if="nameSuggestion" class="dup-suggest">Renamed to: {{ nameSuggestion }}</span>
        </div>
        <div class="form-group">
          <label>Description</label>
          <textarea v-model="skillData.description" placeholder="What this skill does..." rows="2"></textarea>
        </div>
        <div class="form-group">
          <label>Tools (comma-separated)</label>
          <input v-model="skillsString" type="text" placeholder="read_file, search_files" />
        </div>
        <div class="form-group">
          <label>Prompt Template</label>
          <textarea v-model="skillData.prompt_template" placeholder="Skill prompt template..." rows="6"></textarea>
        </div>
      </template>

      <!-- ==================== ORCHESTRATOR EDITOR ==================== -->
      <template v-else-if="isOrchestrator">
        <div class="form-group">
          <label>Name</label>
          <input v-model="orchestratorData.name" type="text" placeholder="Orchestrator name" :class="{ 'input-error': isDuplicateName }" @blur="onNameBlur" />
          <span v-if="isDuplicateName" class="dup-warn">Name already exists</span>
          <span v-if="nameSuggestion" class="dup-suggest">Renamed to: {{ nameSuggestion }}</span>
        </div>

        <div class="form-row">
          <div class="form-group half">
            <label>Provider</label>
            <ComboBox
              :model-value="orchestratorData.provider"
              :options="allProviderOptions"
              placeholder="openai"
              @update:model-value="orchestratorData.provider = handleProviderChange($event)"
            />
          </div>
          <div class="form-group half">
            <label>Strategy</label>
            <ComboBox
              :model-value="orchestratorData.strategy"
              :options="strategies"
              placeholder="Select strategy"
              :allow-custom="false"
              @update:model-value="orchestratorData.strategy = $event as any"
            />
          </div>
        </div>

        <div class="form-group">
          <label>
            Model
            <span v-if="isInherited('model')" class="inherit-badge" title="Synced with provider default — click to override">
              <span class="inherit-icon">&#x1F517;</span> inherited
            </span>
            <button v-else-if="providerMap[orchestratorData.provider]?.default_model" class="reset-inherit-btn" title="Reset to provider default" @click="resetToInherited('model')">
              reset
            </button>
          </label>
          <ComboBox
            :model-value="orchestratorData.model"
            :options="orchestratorModels"
            placeholder="Select or type model"
            :loading="modelDiscoveryLoading"
            loading-text="Discovering models..."
            :hint="discoveredModels.length > 0 ? `${discoveredModels.length} models discovered` : ''"
            :hint-class="discoveredModels.length > 0 ? 'discovered' : ''"
            @update:model-value="orchestratorData.model = $event; handleFieldOverride('model', $event)"
          />
          <div v-if="currentModelInfo" class="model-info-bar">
            <span v-if="currentModelInfo.max_input_tokens" class="mi-tag">ctx: {{ formatTokens(currentModelInfo.max_input_tokens) }}</span>
            <span v-if="currentModelInfo.max_tokens" class="mi-tag">out: {{ formatTokens(currentModelInfo.max_tokens) }}</span>
            <span v-if="currentModelInfo.supports_vision" class="mi-tag vision">vision</span>
            <span v-if="currentModelInfo.supports_function_calling" class="mi-tag tools">tools</span>
          </div>
        </div>

        <div class="form-group">
          <label>System Prompt</label>
          <textarea v-model="orchestratorData.system_prompt" placeholder="You are the orchestrator..." rows="4"></textarea>
        </div>

        <div class="form-group">
          <label>Managed Agents</label>

          <!-- Pipeline with steps defined: show step visualization -->
          <div v-if="orchestratorData.strategy === 'Pipeline' && orchestratorData.pipeline?.steps?.length" class="pipeline-agents-info">
            <span class="hint">Pipeline steps (from config or connected group):</span>
            <div class="pipeline-step-list">
              <div v-for="step in orchestratorData.pipeline.steps" :key="step.name" class="pipeline-step-item">
                <span class="step-badge" :class="step.type === 'parallel' ? 'parallel' : 'sequential'">
                  {{ step.type === 'parallel' ? '||' : '>>' }}
                </span>
                <span class="step-name">{{ step.name }}</span>
                <template v-if="step.agent">
                  <span class="step-agent">{{ step.agent }}</span>
                </template>
                <template v-if="step.type === 'parallel' && step.steps?.length">
                  <span class="step-sub">{{ step.steps.map(s => s.agent || s.name).join(', ') }}</span>
                </template>
              </div>
            </div>
          </div>

          <!-- Pipeline strategy hint when no steps yet -->
          <div v-if="orchestratorData.strategy === 'Pipeline' && !(orchestratorData.pipeline?.steps?.length)" class="pipeline-hint">
            <span class="hint">Connect a Pipeline group to define steps, or select agents below:</span>
          </div>

          <!-- Agent selector: always shown for all strategies (pipeline uses it when no group connected) -->
          <div class="chip-select">
            <div class="chip-options">
              <button
                v-for="a in agentNames"
                :key="a"
                class="chip-option"
                :class="{ selected: (orchestratorData.agents ?? []).includes(a) }"
                @click="toggleAgent(a)"
              >{{ a }}</button>
            </div>
            <div v-if="(orchestratorData.agents ?? []).some(a => !agentNames.includes(a))" class="chip-selected">
              <span
                v-for="a in (orchestratorData.agents ?? []).filter(a => !agentNames.includes(a))"
                :key="a"
                class="chip-tag custom"
              >{{ a }}<button class="chip-x" @click="removeAgent(a)">x</button></span>
            </div>
            <div class="chip-add-row">
              <input
                v-model="customAgentInput"
                type="text"
                class="chip-add-input"
                placeholder="Type to add..."
                @keydown.enter.prevent="addCustomAgent()"
              />
              <button class="chip-add-btn" :disabled="!customAgentInput.trim()" @click="addCustomAgent()">+</button>
            </div>
            <span v-if="missingAgents.length > 0" class="validation-warn">
              Not on canvas: {{ missingAgents.join(', ') }}
            </span>
            <span class="hint">Draw wire from orchestrator to agent to auto-add</span>
          </div>
        </div>

        <!-- Model Config -->
        <div class="section-toggle" @click="showModelConfig = !showModelConfig; ensureModelConfig(orchestratorData)">
          <span>{{ showModelConfig ? 'v' : '>' }} Model Config</span>
        </div>
        <template v-if="showModelConfig && orchestratorData.model_config">
          <div class="form-row">
            <div class="form-group half">
              <label>Temperature</label>
              <input v-model.number="orchestratorData.model_config.temperature" type="number" min="0" max="2" step="0.1" />
            </div>
            <div class="form-group half">
              <label>Max Tokens</label>
              <input v-model.number="orchestratorData.model_config.max_tokens" type="number" min="1" />
            </div>
          </div>
          <div class="form-row">
            <div class="form-group half">
              <label>Top P</label>
              <input v-model.number="orchestratorData.model_config.top_p" type="number" min="0" max="1" step="0.1" />
            </div>
            <div class="form-group half">
              <label>Freq Penalty</label>
              <input v-model.number="orchestratorData.model_config.frequency_penalty" type="number" min="-2" max="2" step="0.1" />
            </div>
          </div>
          <div class="form-row">
            <div class="form-group half">
              <label>Presence Penalty</label>
              <input v-model.number="orchestratorData.model_config.presence_penalty" type="number" min="-2" max="2" step="0.1" />
            </div>
            <div class="form-group half">
              <label>Timeout (sec)</label>
              <input v-model.number="orchestratorData.model_config.timeout_sec" type="number" min="0" placeholder="0 = default" />
            </div>
          </div>
          <div class="form-row">
            <div class="form-group half">
              <label>Max Thinking Tokens</label>
              <input v-model.number="orchestratorData.model_config.max_thinking_tokens" type="number" min="0" placeholder="0 = default" />
            </div>
            <div class="form-group half">
              <label style="padding-top:18px" class="checkbox-label">
                <input type="checkbox" v-model="orchestratorData.model_config.no_stream_tools" />
                No Stream Tools
              </label>
            </div>
          </div>
        </template>

        <!-- Routing Config -->
        <div class="section-toggle" @click="showRouting = !showRouting; ensureRouting()">
          <span>{{ showRouting ? 'v' : '>' }} Routing</span>
        </div>
        <template v-if="showRouting && orchestratorData.routing">
          <div class="form-group">
            <label class="checkbox-label">
              <input type="checkbox" v-model="orchestratorData.routing.auto_delegate_tools" />
              Auto-delegate tools
            </label>
          </div>
        </template>

        <!-- Handoff Config -->
        <div class="section-toggle" @click="showHandoff = !showHandoff; ensureHandoff()">
          <span>{{ showHandoff ? 'v' : '>' }} Handoff</span>
        </div>
        <template v-if="showHandoff && orchestratorData.handoff">
          <div class="form-group">
            <label class="checkbox-label">
              <input type="checkbox" v-model="orchestratorData.handoff.include_context" />
              Include context in handoff
            </label>
          </div>
          <div class="form-group">
            <label>Max Context Length</label>
            <input v-model.number="orchestratorData.handoff.max_context_length" type="number" min="0" />
          </div>
          <div class="form-group">
            <label class="checkbox-label">
              <input type="checkbox" v-model="orchestratorData.handoff.allow_cross_agent_calls" />
              Allow cross-agent calls
            </label>
          </div>
        </template>

        <!-- Pipeline Config (shown when strategy is Pipeline) -->
        <template v-if="orchestratorData.strategy === 'Pipeline'">
          <div class="section-toggle" @click="showPipeline = !showPipeline; ensurePipeline()">
            <span>{{ showPipeline ? 'v' : '>' }} Pipeline Steps</span>
            <span v-if="hasConnectedGroup" class="connected-badge">connected</span>
          </div>

          <!-- CONNECTED MODE: merged view with inherit/override badges -->
          <template v-if="showPipeline && orchestratorData.pipeline && hasConnectedGroup">
            <div class="group-step-list">
              <div v-for="(step, si) in orchestratorData.pipeline.steps" :key="step._agentId || si" class="group-step-card">
                <div class="step-header">
                  <span class="step-type-badge" :class="step._groupId ? 'group' : 'agent'">{{ step._groupId ? 'G' : 'A' }}</span>
                  <span class="step-agent-name">{{ step.name }}</span>
                  <span class="step-order-badge">#{{ si + 1 }}</span>
                </div>
                <template v-if="step._agentId">
                  <div class="form-group compact">
                    <label>
                      Task
                      <span v-if="isStepInherited(step._agentId, 'task')" class="inherit-badge" @click="setStepOverride(step._agentId!, 'task', getStepMergedValue(step._agentId!, 'task') || '')">inherited</span>
                      <button v-else class="reset-inherit-btn" @click="resetStepOverride(step._agentId!, 'task')">reset</button>
                    </label>
                    <input
                      :value="getStepMergedValue(step._agentId, 'task') || ''"
                      type="text"
                      :placeholder="isStepInherited(step._agentId, 'task') ? '(from group)' : 'Override task...'"
                      :class="{ inherited: isStepInherited(step._agentId, 'task') }"
                      @input="setStepOverride(step._agentId!, 'task', ($event.target as HTMLInputElement).value)"
                    />
                  </div>
                  <div class="form-group compact">
                    <label>
                      Timeout (s)
                      <span v-if="isStepInherited(step._agentId, 'timeout_sec')" class="inherit-badge" @click="setStepOverride(step._agentId!, 'timeout_sec', getStepMergedValue(step._agentId!, 'timeout_sec') || 0)">inherited</span>
                      <button v-else class="reset-inherit-btn" @click="resetStepOverride(step._agentId!, 'timeout_sec')">reset</button>
                    </label>
                    <input
                      :value="getStepMergedValue(step._agentId, 'timeout_sec') || ''"
                      type="number"
                      min="0"
                      :placeholder="isStepInherited(step._agentId, 'timeout_sec') ? '(from group)' : '0'"
                      :class="{ inherited: isStepInherited(step._agentId, 'timeout_sec') }"
                      @input="setStepOverride(step._agentId!, 'timeout_sec', Number(($event.target as HTMLInputElement).value) || undefined)"
                    />
                  </div>
                </template>
                <template v-else-if="step.steps?.length">
                  <div class="hint">{{ step.type }} group — {{ step.steps.length }} sub-steps</div>
                </template>
              </div>
            </div>
            <!-- Stale overrides warning -->
            <div v-if="staleOverrideIds.length > 0" class="stale-warning">
              <span class="stale-icon">!</span>
              {{ staleOverrideIds.length }} stale override{{ staleOverrideIds.length > 1 ? 's' : '' }} for removed agent{{ staleOverrideIds.length > 1 ? 's' : '' }}
              <button class="stale-clean-btn" @click="cleanStaleOverrides">Clean up</button>
            </div>
          </template>

          <!-- MANUAL MODE: free-form pipeline editor (no connected group) -->
          <template v-else-if="showPipeline && orchestratorData.pipeline && !hasConnectedGroup">
            <div v-for="(step, si) in orchestratorData.pipeline.steps" :key="si" class="pipeline-step">
              <div class="step-header">
                <span class="step-index">#{{ si + 1 }}</span>
                <button class="remove-btn" @click="removePipelineStep(si)">x</button>
              </div>
              <div class="form-row">
                <div class="form-group half">
                  <label>Step Name</label>
                  <input v-model="step.name" type="text" placeholder="step_name" />
                </div>
                <div class="form-group half">
                  <label>Type</label>
                  <ComboBox
                    :model-value="step.type ?? 'sequential'"
                    :options="pipelineStepTypes"
                    placeholder="Select type"
                    :allow-custom="false"
                    @update:model-value="step.type = $event as 'sequential' | 'parallel' | 'loop'"
                  />
                </div>
              </div>

              <!-- Common step fields (all types) -->
              <div class="form-row">
                <div class="form-group half">
                  <label>Timeout (sec)</label>
                  <input v-model.number="step.timeout_sec" type="number" min="0" placeholder="0 = default" />
                </div>
                <div class="form-group half">
                  <label>Depends On <span class="hint">(comma-sep names)</span></label>
                  <input :value="step.depends_on?.join(', ') || ''" type="text" placeholder="step_a, step_b"
                    @input="step.depends_on = ($event.target as HTMLInputElement).value.split(',').map(s => s.trim()).filter(s => s)" />
                </div>
              </div>

              <!-- Sequential step fields -->
              <template v-if="!step.type || step.type === 'sequential'">
                <div class="form-group">
                  <label>Agent</label>
                  <input v-model="step.agent" type="text" placeholder="Agent name" />
                </div>
                <div class="form-group">
                  <label>Task</label>
                  <textarea v-model="step.task" rows="2" placeholder="Task description..."></textarea>
                </div>
              </template>

              <!-- Parallel step fields -->
              <template v-if="step.type === 'parallel'">
                <div v-for="(sub, subi) in step.steps" :key="subi" class="sub-step">
                  <div class="step-header">
                    <span class="step-index">#{{ si + 1 }}.{{ subi + 1 }}</span>
                    <button class="remove-btn" @click="removeSubStep(step, subi)">x</button>
                  </div>
                  <div class="form-group">
                    <label>Name</label>
                    <input v-model="sub.name" type="text" placeholder="sub_step_name" />
                  </div>
                  <div class="form-group">
                    <label>Agent</label>
                    <input v-model="sub.agent" type="text" placeholder="Agent name" />
                  </div>
                  <div class="form-group">
                    <label>Task</label>
                    <textarea v-model="sub.task" rows="2" placeholder="Task description..."></textarea>
                  </div>
                </div>
                <button class="btn btn-small" @click="addParallelSubStep(step)">+ Sub-step</button>
              </template>

              <!-- Loop step fields -->
              <template v-if="step.type === 'loop'">
                <div class="form-row">
                  <div class="form-group half">
                    <label>Max Iterations</label>
                    <input v-model.number="step.max_iterations" type="number" min="1" max="20" />
                  </div>
                  <div class="form-group half">
                    <label>Condition Agent</label>
                    <input v-model="step.condition_agent" type="text" placeholder="Agent name" />
                  </div>
                </div>
                <div class="form-group">
                  <label>Condition Prompt</label>
                  <textarea v-model="step.condition_prompt" rows="2" placeholder="Are all issues resolved? Answer PASS or FAIL."></textarea>
                </div>
                <div v-for="(sub, subi) in step.steps" :key="subi" class="sub-step">
                  <div class="step-header">
                    <span class="step-index">#{{ si + 1 }}.{{ subi + 1 }}</span>
                    <button class="remove-btn" @click="removeSubStep(step, subi)">x</button>
                  </div>
                  <div class="form-group">
                    <label>Name</label>
                    <input v-model="sub.name" type="text" placeholder="sub_step_name" />
                  </div>
                  <div class="form-group">
                    <label>Agent</label>
                    <input v-model="sub.agent" type="text" placeholder="Agent name" />
                  </div>
                  <div class="form-group">
                    <label>Task</label>
                    <textarea v-model="sub.task" rows="2" placeholder="Task description..."></textarea>
                  </div>
                </div>
                <button class="btn btn-small" @click="addParallelSubStep(step)">+ Loop Step</button>
              </template>
            </div>
            <button class="btn btn-small" @click="addPipelineStep()">+ Add Step</button>
          </template>

          <!-- Synthesis toggle -->
          <div class="form-group">
            <label class="checkbox-label">
              <input type="checkbox" v-model="synthEnabled" />
              Enable Synthesis
            </label>
          </div>
          <div v-if="synthEnabled" class="form-group">
            <label>Synthesis Prompt</label>
            <textarea v-model="synthPrompt" rows="3" placeholder="Custom synthesis prompt (optional)..."></textarea>
          </div>
        </template>
      </template>

      <!-- ========== GROUP ========== -->
      <template v-if="isGroup">
      <div class="form-group">
        <label>Group Name</label>
        <input v-model="groupData.name" type="text" placeholder="Group name" :class="{ 'input-error': isDuplicateName }" @blur="onNameBlur" />
        <span v-if="isDuplicateName" class="dup-warn">Name already exists</span>
        <span v-if="nameSuggestion" class="dup-suggest">Renamed to: {{ nameSuggestion }}</span>
      </div>

      <div class="form-group">
        <label>Block Type</label>
        <ComboBox
          :model-value="groupData.blockType"
          :options="[
            { label: 'Pipeline (sequential)', value: 'pipeline' },
            { label: 'Parallel (concurrent)', value: 'parallel' },
            { label: 'Team (hierarchical)', value: 'team' },
            { label: 'Loop (iterative)', value: 'loop' },
            { label: 'Generic (visual only)', value: 'generic' },
          ]"
          :allow-custom="false"
          @update:model-value="groupData.blockType = $event as any"
        />
      </div>

      <template v-if="groupData.blockType === 'pipeline' || groupData.blockType === 'parallel' || groupData.blockType === 'loop'">
        <div class="form-group">
          <label>Timeout (sec) <span class="hint-inline">0 = no limit</span></label>
          <input v-model.number="groupData.timeoutSec" type="number" min="0" placeholder="0" />
        </div>
      </template>

      <template v-if="groupData.blockType === 'pipeline' || groupData.blockType === 'loop'">
        <div class="form-group">
          <label>Max Iterations <span class="hint-inline">0 = no loop</span></label>
          <input v-model.number="groupData.maxIterations" type="number" min="0" :placeholder="groupData.blockType === 'loop' ? '5' : '0 = no loop'" />
        </div>

        <div v-if="(groupData.maxIterations ?? 0) > 0 || groupData.blockType === 'loop'" class="form-group">
          <label>Condition Agent</label>
          <ComboBox
            :model-value="groupData.conditionAgent ?? ''"
            :options="agentNames"
            placeholder="Agent to check loop condition"
            @update:model-value="groupData.conditionAgent = $event"
          />
        </div>

        <div v-if="(groupData.maxIterations ?? 0) > 0 || groupData.blockType === 'loop'" class="form-group">
          <label>Condition Prompt</label>
          <textarea v-model="groupData.conditionPrompt" placeholder="Answer PASS to stop or FAIL to continue" rows="2"></textarea>
        </div>
      </template>

      <div class="form-group">
        <label>
          <input type="checkbox" :checked="groupData.autoFit !== false" @change="groupData.autoFit = ($event.target as HTMLInputElement).checked" />
          Auto-fit to children
        </label>
      </div>

      <!-- Step editor: shows children as configurable pipeline steps -->
      <template v-if="(groupData.blockType === 'pipeline' || groupData.blockType === 'parallel' || groupData.blockType === 'loop') && groupChildren.length > 0">
        <div class="section-label">Steps ({{ groupChildren.length }})</div>
        <div class="group-step-list">
          <div
            v-for="(child, idx) in groupChildren"
            :key="child.id"
            class="group-step-card"
            :class="{ dragging: dragIndex === idx, 'drop-target': dropIndex === idx && dragIndex !== idx }"
            draggable="true"
            @dragstart="onStepDragStart(idx, $event)"
            @dragover="onStepDragOver(idx, $event)"
            @dragleave="onStepDragLeave"
            @drop="onStepDrop(idx)"
            @dragend="onStepDragEnd"
          >
            <div class="step-header">
              <span class="drag-handle" title="Drag to reorder">⠿</span>
              <span class="step-type-badge" :class="child.type">{{ child.type === 'agent' ? 'A' : child.type === 'group' ? 'G' : '?' }}</span>
              <span class="step-agent-name">{{ child.name }}</span>
              <span class="step-order-badge">#{{ idx + 1 }}</span>
            </div>
            <div class="form-group compact">
              <label>Task</label>
              <input
                :value="getStepDefault(child._agentId || child.id, 'task')"
                type="text"
                placeholder="What should this step do?"
                @input="setStepDefault(child._agentId || child.id, 'task', ($event.target as HTMLInputElement).value)"
              />
            </div>
            <div class="form-row">
              <div class="form-group half compact">
                <label>Timeout (s)</label>
                <input
                  :value="getStepDefault(child._agentId || child.id, 'timeoutSec') || ''"
                  type="number"
                  min="0"
                  placeholder="0"
                  @input="setStepDefault(child._agentId || child.id, 'timeoutSec', Number(($event.target as HTMLInputElement).value) || undefined)"
                />
              </div>
            </div>
            <div v-if="otherStepNames(idx).length > 0" class="form-group compact">
              <label>Depends On</label>
              <div class="chip-row">
                <button v-for="otherStep in otherStepNames(idx)" :key="otherStep"
                  class="chip-option" :class="{ selected: isStepDep(idx, otherStep) }"
                  @click="toggleStepDep(idx, otherStep)">
                  {{ otherStep }}
                </button>
              </div>
            </div>
          </div>
          <div v-if="groupChildren.length === 0" class="hint">
            Add agents to this group to configure steps
          </div>
        </div>
      </template>
      </template>

      <!-- ==================== BREAKPOINTS (agent/orchestrator) ==================== -->
      <template v-if="isAgent || isOrchestrator">
        <div class="editor-section">
          <div class="section-toggle" @click="showEventHistory = !showEventHistory">
            <span class="section-label">BREAKPOINTS</span>
            <span v-if="nodeBreakpoints.length" class="bp-count">{{ nodeBreakpoints.length }}</span>
          </div>
          <div class="checkpoint-chips">
            <button
              v-for="cp in CHECKPOINT_TYPES"
              :key="cp.value"
              class="checkpoint-chip"
              :class="{ active: hasCheckpoint(cp.value) }"
              @click="toggleCheckpoint(cp.value)"
              :title="hasCheckpoint(cp.value) ? `Remove ${cp.label} breakpoint` : `Set ${cp.label} breakpoint`"
            >
              {{ cp.label }}
            </button>
          </div>
        </div>
      </template>

      <!-- ==================== BREAKPOINT (tool) ==================== -->
      <template v-if="isTool">
        <div class="editor-section">
          <span class="section-label">BREAKPOINT</span>
          <div class="checkpoint-chips">
            <button
              class="checkpoint-chip"
              :class="{ active: toolHasBreakpoint }"
              @click="injectedToggleToolBreakpoint?.(toolData.name)"
              :title="toolHasBreakpoint ? 'Remove tool breakpoint' : 'Break before this tool is called'"
            >
              Before Tool Call
            </button>
          </div>
        </div>
      </template>

      <!-- ==================== RUN HISTORY (agent/orchestrator) ==================== -->
      <template v-if="(isAgent || isOrchestrator) && nodeEvents.length > 0">
        <div class="editor-section">
          <div class="section-toggle" @click="showEventHistory = !showEventHistory">
            <span class="section-label">RUN HISTORY</span>
            <span class="event-count">{{ nodeEvents.length }}</span>
            <span class="toggle-arrow">{{ showEventHistory ? '▲' : '▼' }}</span>
          </div>
          <div v-if="showEventHistory" class="event-history">
            <div
              v-for="(evt, idx) in nodeEvents"
              :key="idx"
              class="event-item"
              :class="evt.event_type.toLowerCase()"
              @click="toggleEventExpand(idx)"
            >
              <div class="event-row">
                <span class="event-type-badge">{{ eventTypeLabel(evt.event_type) }}</span>
                <span v-if="evt.iteration" class="event-iter">iter {{ evt.iteration }}</span>
                <span v-if="evt.duration_ms" class="event-dur">{{ fmtDuration(evt.duration_ms) }}</span>
                <span v-if="evt.token_usage?.total_tokens" class="event-tokens">{{ evt.token_usage.total_tokens }} tkns</span>
              </div>
              <div v-if="expandedEvents.has(idx)" class="event-payload">
                <pre>{{ JSON.stringify(evt.payload, null, 2) }}</pre>
              </div>
            </div>
          </div>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.node-editor {
  width: 340px;
  background: var(--surface-1);
  border-left: 1px solid var(--border-default);
  display: flex;
  flex-direction: column;
  height: 100%;
  color: var(--text-primary);
  font-family: var(--font-sans);
}

.editor-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px;
  border-bottom: 1px solid var(--border-default);
  background: var(--surface-2);
}

.editor-header h3 {
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

.close-btn:hover {
  color: var(--text-primary);
}

.editor-content {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
}

.form-group {
  margin-bottom: 12px;
}

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
  background: var(--surface-1);
  color: var(--text-primary);
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  font-size: 13px;
  transition: border-color 0.2s;
}

.form-group input:focus,
.form-group select:focus,
.form-group textarea:focus {
  outline: none;
  border-color: var(--border-focus);
}
.input-error {
  border-color: var(--status-error) !important;
  background: rgba(239, 68, 68, 0.1);
}
.dup-warn {
  color: var(--status-error);
  font-size: 11px;
  margin-top: 2px;
  display: block;
}
.dup-suggest {
  color: var(--status-running);
  font-size: 11px;
  margin-top: 2px;
  display: block;
}

.form-group textarea {
  resize: vertical;
  font-family: inherit;
}

.form-group select {
  cursor: pointer;
}

.form-row {
  display: flex;
  gap: 10px;
}

.form-group.half {
  flex: 1;
}

.checkbox-label {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  color: var(--text-primary);
}

.checkbox-label input[type="checkbox"] {
  width: auto;
}

.hint {
  display: block;
  font-size: 11px;
  color: var(--text-muted);
  margin-top: 4px;
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
  font-family: var(--font-mono);
  color: var(--text-secondary);
}
.mi-tag.vision { background: rgba(34, 197, 94, 0.15); color: var(--status-running); }
.mi-tag.tools { background: rgba(91, 141, 239, 0.15); color: var(--accent-agent); }
.mi-tag.cost { background: rgba(245, 158, 11, 0.15); color: var(--status-paused); }

.inline-check {
  display: flex;
  align-items: center;
  gap: 4px;
}
.inline-check label {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  cursor: pointer;
  color: var(--text-primary);
}
.hint-inline {
  font-size: 10px;
  font-weight: 400;
  color: var(--text-muted);
  margin-left: 4px;
}
.hint-inline.discovered {
  color: var(--status-running);
}

/* Inherit/Override indicators */
/* Group step editor */
.group-step-list { display: flex; flex-direction: column; gap: 8px; padding-bottom: 40px; }
.group-step-card {
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--node-radius);
  padding: 8px;
  cursor: grab;
  transition: opacity 0.15s, border-color 0.15s, box-shadow 0.15s;
}
.group-step-card.dragging {
  opacity: 0.4;
}
.group-step-card.drop-target {
  border-color: var(--accent-agent);
  box-shadow: 0 -2px 0 0 var(--accent-agent);
}
.drag-handle {
  cursor: grab;
  color: var(--text-muted);
  font-size: 14px;
  line-height: 1;
  user-select: none;
}
.drag-handle:active { cursor: grabbing; }
.step-order-badge {
  font-size: 10px;
  font-family: var(--font-mono);
  color: var(--text-muted);
  margin-left: auto;
}
.step-header {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 6px;
}
.step-type-badge {
  width: 18px; height: 18px;
  border-radius: var(--node-radius);
  display: inline-flex; align-items: center; justify-content: center;
  color: var(--surface-0); font-size: 10px; font-weight: 700;
}
.step-type-badge.agent { background: var(--accent-agent); }
.step-type-badge.group { background: var(--text-secondary); }
.step-agent-name { font-weight: 600; font-size: 12px; color: var(--text-primary); }
.form-group.compact { margin-bottom: 4px; }
.form-group.compact label { font-size: 10px; margin-bottom: 1px; }
.form-group.compact input { padding: 3px 6px; font-size: 11px; }

/* Pipeline agents display */
.pipeline-agents-info {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.pipeline-step-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.pipeline-step-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  background: var(--surface-2);
  border-radius: var(--node-radius);
  font-size: 12px;
}
.step-badge {
  font-size: 10px;
  font-weight: 700;
  font-family: var(--font-mono);
  padding: 1px 4px;
  border-radius: var(--node-radius);
  color: var(--surface-0);
}
.step-badge.sequential { background: var(--accent-agent); }
.step-badge.parallel { background: var(--accent-tool); }
.step-name { font-weight: 600; color: var(--text-primary); }
.step-agent { color: var(--accent-agent); }
.step-sub { color: var(--text-secondary); font-size: 11px; }

.inherit-badge {
  font-size: 9px;
  font-weight: 500;
  color: var(--accent-agent);
  background: rgba(91, 141, 239, 0.1);
  padding: 1px 5px;
  border-radius: var(--node-radius);
  margin-left: 4px;
}
.inherit-icon { font-size: 8px; }
.reset-inherit-btn {
  font-size: 9px;
  background: none;
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  color: var(--text-secondary);
  padding: 0 4px;
  margin-left: 4px;
  cursor: pointer;
}
.reset-inherit-btn:hover { background: var(--surface-4); color: var(--text-primary); }
.connected-badge {
  font-size: 9px;
  font-weight: 500;
  color: var(--accent-tool);
  background: rgba(45, 212, 160, 0.1);
  padding: 1px 5px;
  border-radius: var(--node-radius);
  margin-left: auto;
}
.stale-warning {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 10px;
  margin-top: 8px;
  background: rgba(245, 158, 11, 0.1);
  border: 1px solid rgba(245, 158, 11, 0.3);
  border-radius: var(--node-radius);
  font-size: 11px;
  color: var(--status-paused);
}
.stale-icon {
  width: 18px; height: 18px;
  border-radius: 50%;
  background: var(--status-paused);
  color: var(--surface-0);
  font-size: 11px;
  font-weight: 700;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.stale-clean-btn {
  margin-left: auto;
  font-size: 10px;
  padding: 2px 8px;
  border: 1px solid var(--status-paused);
  border-radius: var(--node-radius);
  background: var(--surface-1);
  color: var(--status-paused);
  cursor: pointer;
}
.stale-clean-btn:hover { background: rgba(245, 158, 11, 0.15); }
input.inherited, textarea.inherited {
  color: var(--text-muted);
  font-style: italic;
}

/* Chip multi-select */
.chip-select {
  border: 1px solid var(--border-subtle);
  border-radius: var(--node-radius);
  padding: 6px;
  background: var(--surface-2);
}
.chip-options {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-bottom: 4px;
}
.chip-options:empty { display: none; }
.chip-option {
  padding: 3px 8px;
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  background: var(--surface-1);
  font-size: 11px;
  cursor: pointer;
  color: var(--text-muted);
  transition: all 0.15s;
}
.chip-option:hover { border-color: var(--accent-agent); color: var(--accent-agent); }
.chip-option.selected {
  background: var(--accent-agent);
  color: var(--surface-0);
  border-color: var(--accent-agent);
}
.chip-selected {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-bottom: 4px;
}
.chip-tag {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 2px 6px;
  border-radius: var(--node-radius);
  font-size: 10px;
  background: rgba(91, 141, 239, 0.15);
  color: var(--text-primary);
}
.chip-tag.custom {
  background: rgba(245, 158, 11, 0.15);
  color: var(--status-paused);
}
.chip-x {
  border: none;
  background: none;
  font-size: 10px;
  cursor: pointer;
  color: var(--text-muted);
  padding: 0 2px;
  line-height: 1;
}
.chip-x:hover { color: var(--status-error); }
.chip-add-row {
  display: flex;
  gap: 3px;
}
.chip-add-input {
  flex: 1;
  min-width: 0;
  padding: 3px 6px;
  background: var(--surface-1);
  color: var(--text-primary);
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  font-size: 11px;
  outline: none;
}
.chip-add-input:focus { border-color: var(--border-focus); }
.chip-add-btn {
  padding: 3px 8px;
  border: 1px solid var(--accent-tool);
  border-radius: var(--node-radius);
  background: var(--surface-1);
  color: var(--accent-tool);
  font-size: 11px;
  font-weight: 700;
  cursor: pointer;
}
.chip-add-btn:hover:not(:disabled) { background: var(--accent-tool); color: var(--surface-0); }
.chip-add-btn:disabled { opacity: 0.4; cursor: default; }

.validation-warn {
  display: block;
  font-size: 10px;
  color: var(--status-paused);
  margin-top: 4px;
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

.section-toggle:hover {
  color: var(--text-primary);
}

.section-toggle.sub {
  padding-left: 8px;
  font-weight: 500;
  border-top: 1px dashed var(--border-subtle);
}

.editor-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 16px;
  border-top: 1px solid var(--border-default);
  background: var(--surface-2);
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

.btn-primary {
  background: var(--accent-agent);
  color: var(--surface-0);
}

.btn-primary:hover {
  background: color-mix(in srgb, var(--accent-agent) 85%, white);
}

.btn-secondary {
  background: var(--surface-3);
  color: var(--text-primary);
}

.btn-secondary:hover {
  background: var(--surface-4);
}

.btn-small {
  padding: 4px 10px;
  font-size: 12px;
  background: var(--surface-3);
  border: 1px dashed var(--border-default);
  border-radius: var(--node-radius);
  cursor: pointer;
  color: var(--text-muted);
  margin-top: 4px;
}

.btn-small:hover {
  background: var(--surface-4);
  border-color: var(--text-muted);
}

.pipeline-step {
  border: 1px solid var(--border-subtle);
  border-radius: var(--node-radius);
  padding: 10px;
  margin-bottom: 10px;
  background: var(--surface-2);
}

.sub-step {
  border: 1px dashed var(--border-default);
  border-radius: var(--node-radius);
  padding: 8px;
  margin-bottom: 8px;
  background: var(--surface-1);
}

.step-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
}

.step-index {
  font-size: 11px;
  font-weight: 600;
  font-family: var(--font-mono);
  color: var(--accent-agent);
}

.remove-btn {
  background: none;
  border: none;
  color: var(--status-error);
  font-size: 14px;
  cursor: pointer;
  padding: 0 4px;
  line-height: 1;
}

.remove-btn:hover {
  color: color-mix(in srgb, var(--status-error) 80%, white);
}

/* --- Breakpoints section --- */
.editor-section {
  margin-top: 12px;
  padding-top: 10px;
  border-top: 1px solid var(--border-subtle);
}

.section-toggle {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  padding: 2px 0;
  margin-bottom: 8px;
}

.section-label {
  font-size: 9px;
  font-weight: 600;
  color: var(--text-muted);
  font-family: var(--font-mono);
  letter-spacing: 0.05em;
}

.bp-count, .event-count {
  font-size: 9px;
  font-family: var(--font-mono);
  background: var(--surface-4);
  color: var(--text-secondary);
  padding: 1px 5px;
  border-radius: 2px;
}

.toggle-arrow {
  margin-left: auto;
  font-size: 10px;
  color: var(--text-muted);
}

.checkpoint-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.checkpoint-chip {
  padding: 3px 8px;
  border-radius: 2px;
  font-size: 10px;
  font-family: var(--font-mono);
  cursor: pointer;
  border: 1px solid var(--border-default);
  background: var(--surface-3);
  color: var(--text-secondary);
  transition: all 0.15s;
}

.checkpoint-chip:hover {
  border-color: var(--status-error);
  color: var(--text-primary);
}

.checkpoint-chip.active {
  background: rgba(239, 68, 68, 0.15);
  border-color: var(--status-error);
  color: var(--status-error);
  font-weight: 600;
}

/* --- Event history --- */
.event-history {
  max-height: 300px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.event-item {
  padding: 4px 8px;
  border-radius: 2px;
  background: var(--surface-3);
  cursor: pointer;
  transition: background 0.1s;
}

.event-item:hover { background: var(--surface-4); }

.event-row {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}

.event-type-badge {
  font-size: 9px;
  font-family: var(--font-mono);
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.03em;
  color: var(--text-secondary);
}

.event-item.agent_start .event-type-badge,
.event-item.agent_end .event-type-badge { color: var(--accent-agent); }
.event-item.thought_end .event-type-badge { color: var(--accent-skill); }
.event-item.tool_call_start .event-type-badge,
.event-item.tool_call_end .event-type-badge { color: var(--accent-tool); }
.event-item.error .event-type-badge { color: var(--status-error); }
.event-item.debug_paused .event-type-badge { color: var(--status-paused); }

.event-iter, .event-dur, .event-tokens {
  font-size: 9px;
  font-family: var(--font-mono);
  color: var(--text-muted);
}

.event-tokens { margin-left: auto; }

.event-payload {
  margin-top: 4px;
  padding: 4px 6px;
  background: var(--surface-0);
  border-radius: 2px;
  overflow-x: auto;
}

.event-payload pre {
  margin: 0;
  font-size: 10px;
  font-family: var(--font-mono);
  color: var(--text-secondary);
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 200px;
  overflow-y: auto;
}
</style>
