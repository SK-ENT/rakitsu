// Configuration types matching the Go backend YAML schema (internal/config/config.go)

// --- Settings ---

export interface DefaultSettings {
  model: string;
  temperature: number;
  max_tokens: number;
}

export interface ExecutionSettings {
  max_iterations: number;
  timeout_seconds: number;
  retry_attempts: number;
  max_total_tokens?: number;
  max_cost?: number;
}

export interface LoggingSettings {
  level: string;
  file: string;
}

export interface SpawnSettings {
  enabled?: boolean;
  max_concurrent?: number;
  max_depth?: number;
  timeout_seconds?: number;
}

export interface AgentChatSettings {
  enabled?: boolean;
  max_retained?: number;
  max_transcript_bytes?: number;
}

export interface ProviderDefinition {
  _id?: string; // Internal stable ID — survives renames
  type: string;
  default_model?: string;
  api_key?: string;
  base_url?: string;
  credentials_file?: string;
  location?: string;
  project?: string;
}

export interface PricingConfig {
  input: number;
  output: number;
}

export interface ServerConfig {
  port?: number;
  host?: string;
}

export interface Settings {
  default_provider: string;
  providers?: Record<string, ProviderDefinition>;
  api_keys: Record<string, string>;
  base_urls: Record<string, string>;
  credentials_files: Record<string, string>;
  locations?: Record<string, string>;
  projects?: Record<string, string>;
  allowed_commands: string[];
  defaults: DefaultSettings;
  execution: ExecutionSettings;
  pricing?: Record<string, PricingConfig>;
  logging: LoggingSettings;
  hub_url?: string;
  server?: ServerConfig;
  spawn?: SpawnSettings;
  agent_chat?: AgentChatSettings;
}

// --- Tools ---

export interface Parameter {
  type: string;
  description: string;
  required: boolean;
  default?: unknown;
  enum?: string[];
}

export interface ResourceLimits {
  cpu_limit: string;
  memory_limit: string;
  timeout_sec: number;
}

export interface SandboxConfig {
  type: 'local_restricted' | 'docker';
  image?: string;
  mount_workdir?: boolean;
  allowed_paths?: string[];
  network_isolated?: boolean;
  resource_limits?: ResourceLimits;
}

export interface ToolConfig {
  _id?: string; // Internal stable ID
  name: string;
  type: 'cli' | 'fs' | 'mcp_server' | 'a2a';
  description: string;
  command?: string;
  args?: string[];
  transport?: 'stdio' | 'http';
  url?: string;
  agent?: string;
  operation?: string;
  method?: string;
  executable?: string;
  script?: string;
  parameters?: Record<string, Parameter>;
  timeout_seconds?: number;
  env?: Record<string, string>;
  working_dir?: string;
  allowed_paths?: string[];
  allowed_exit_codes?: number[];
  sandbox?: SandboxConfig;
}

// --- Skills ---

export interface SkillConfig {
  _id?: string; // Internal stable ID
  name: string;
  description: string;
  tools: string[];
  prompt_template: string;
}

// --- Model & Agent Settings ---

export interface ModelConfig {
  temperature: number;
  max_tokens: number;
  top_p?: number;
  frequency_penalty?: number;
  presence_penalty?: number;
  timeout_sec?: number;
  no_stream_tools?: boolean;
  max_thinking_tokens?: number;
  thinking_offload?: boolean;
}

export interface ReflectionConfig {
  enabled: boolean;
  mode: 'after_tool' | 'before_answer' | 'both';
  frequency: 'always' | 'on_error' | 'every_n';
  every_n?: number;
  prompt?: string;
}

export interface GroundCheckConfig {
  enabled: boolean;
  confidence_threshold: number;
  max_retries: number;
  prompt?: string;
}

export interface RetrievalConfig {
  enabled?: boolean;
  top_k?: number;
  embedding_provider?: string;
  embedding_model?: string;
  embedding_url?: string;
  error_bias?: number;
}

export interface ContextConfig {
  strategy?: string;
  window_size?: number;
  keep_recent?: number;
  max_tool_output?: number;
  fence_outputs?: boolean;
  auto_full_threshold?: number;
  auto_compress_threshold?: number;
  context_budget_threshold?: number;
  context_retrieval_threshold?: number;
  retrieval?: RetrievalConfig;
}

export interface AgentSettings {
  max_iterations: number;
  max_total_tokens?: number;
  max_cost?: number;
  verbose: boolean;
  timeout: number;
  reflection: ReflectionConfig;
  ground_check: GroundCheckConfig;
  context?: ContextConfig;
}

// --- Agents ---

export interface AgentConfig {
  _id?: string; // Internal stable ID
  _inherited?: Record<string, boolean>; // field → true means auto-filled, syncs with source
  name: string;
  role: 'worker' | 'supervisor';
  provider: string;
  _providerId?: string; // Internal: stable reference to provider by ID
  model: string;
  model_config?: ModelConfig;
  system_prompt: string;
  tools: string[];
  skills?: string[];
  tools_inline?: ToolConfig[];
  vision?: boolean;
  providers?: { name: string; model?: string }[];
  settings?: AgentSettings;
}

// --- Orchestrator ---

export interface RoutingRule {
  condition: string;
  delegate_to: string;
}

export interface RoutingConfig {
  auto_delegate_tools: boolean;
  rules?: RoutingRule[];
}

export interface HandoffConfig {
  include_context: boolean;
  max_context_length: number;
  allow_cross_agent_calls: boolean;
}

// --- Pipeline ---

export interface PipelineStep {
  name: string;
  type?: 'sequential' | 'parallel' | 'loop' | 'decision' | 'retrieval';
  decision?: DecisionConfig;
  retrieval?: RetrievalStepConfig;
  agent?: string;
  _agentId?: string; // Internal: stable reference to agent by ID
  _groupId?: string; // Internal: source group that defined this step
  task?: string;
  steps?: PipelineStep[];
  max_iterations?: number;
  condition_agent?: string;
  condition_prompt?: string;
  timeout_sec?: number;
  depends_on?: string[];
}

export interface DecisionMatch {
  question: string;
  field: 'choice' | 'probability_true' | 'score';
  equals?: string;
  min?: number;
  max?: number;
}

export interface DecisionConfig {
  backend: 'jev' | 'laya' | 'openai' | 'gemini';
  url?: string;
  input?: { from: 'query' | 'step' | 'retrieval'; step?: string };
  questions: Record<string, {
    type: 'predicate' | 'choice' | 'score';
    instructions: string;
    choices?: Record<string, string>;
    levels?: string[];
  }>;
  routes?: { when: DecisionMatch; steps: PipelineStep[] }[];
  otherwise?: PipelineStep[];
}

export interface RetrievalStepConfig {
  directory: string;
  top_k?: number;
}

export interface PipelineConfig {
  steps: PipelineStep[];
  synthesis?: boolean;
  synthesis_prompt?: string;
}

export interface OrchestratorConfig {
  _id?: string; // Internal stable ID
  _inherited?: Record<string, boolean>;
  _connectedGroupId?: string; // Internal: pipeline group wired to this orchestrator
  _stepOverrides?: Record<string, PipelineStepOverride>; // keyed by _agentId/_groupId
  name: string;
  role: string;
  strategy: 'ReAct' | 'PlanAndExecute' | 'Hierarchical' | 'Pipeline';
  provider: string;
  _providerId?: string; // Internal: stable reference to provider by ID
  model: string;
  model_config?: ModelConfig;
  system_prompt: string;
  agents: string[];
  routing?: RoutingConfig;
  handoff?: HandoffConfig;
  pipeline?: PipelineConfig;
}

// --- Workflows ---

export interface WorkflowStep {
  agent: string;
  task: string;
  depends_on?: string[];
  condition?: string;
}

export interface SynthesisConfig {
  agent: string;
  prompt: string;
}

export interface WorkflowConfig {
  name: string;
  description: string;
  steps: WorkflowStep[];
  final_synthesis?: SynthesisConfig;
}

// --- Root Config ---

export interface Config {
  name: string;
  project_id?: string;
  version?: string;
  description?: string;
  interactive?: boolean;
  settings?: Settings;
  tools: ToolConfig[];
  skills?: SkillConfig[];
  agents: AgentConfig[];
  orchestrator?: OrchestratorConfig;
  orchestrators?: OrchestratorConfig[];
  workflows?: WorkflowConfig[];
}

// --- Groups (Block Programming) ---

export interface GroupStepConfig {
  agentName: string;
  _agentId?: string;
  task?: string;
  timeoutSec?: number;
  dependsOn?: string[];
}

export interface PipelineStepOverride {
  task?: string;
  timeout_sec?: number;
  depends_on?: string[];
  _inherited?: Record<string, boolean>;
}

export interface GroupConfig {
  _id?: string;
  name: string;
  blockType: 'pipeline' | 'parallel' | 'team' | 'loop' | 'generic';
  viewMode?: 'compact' | 'mixed' | 'full';
  collapsed?: boolean;
  autoFit?: boolean; // default true
  _width?: number;
  _height?: number;
  // Pipeline/parallel execution settings (owned by group, not orchestrator)
  stepDefaults?: Record<string, GroupStepConfig>; // keyed by child _agentId
  timeoutSec?: number;
  maxIterations?: number;
  conditionAgent?: string;
  conditionPrompt?: string;
}

// --- Vue Flow Node Types ---

export interface AgentNode {
  id: string;
  type: 'agent';
  position: { x: number; y: number };
  data: AgentConfig;
}

export interface ToolNode {
  id: string;
  type: 'tool';
  position: { x: number; y: number };
  data: ToolConfig;
}

export interface SkillNode {
  id: string;
  type: 'skill';
  position: { x: number; y: number };
  data: SkillConfig;
}

export interface OrchestratorNode {
  id: string;
  type: 'orchestrator';
  position: { x: number; y: number };
  data: OrchestratorConfig;
}

export type FlowNode = AgentNode | ToolNode | SkillNode | OrchestratorNode;

export interface FlowEdge {
  id: string;
  source: string;
  target: string;
  sourceHandle?: string;
  targetHandle?: string;
  label?: string;
  animated?: boolean;
}
