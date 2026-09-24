<script setup lang="ts">
import { ref } from 'vue';
import type { Breakpoint, DebugState, DebugMode, PendingUserInput } from '../../composables/useDebugControl';
import type { DebugPausedPayload } from '../../types';
import { BREAKPOINT_OPTIONS } from '../../types/checkpoints';
import ComboBox from '../builder/ComboBox.vue';

const props = defineProps<{
  debugState: DebugState;
  isPaused: boolean;
  pausedAgent: string | null;
  pausedCheckpoint: string | null;
  pausedContext: DebugPausedPayload | null;
  breakpoints: Breakpoint[];
  isConnected: boolean;
  debugMode: DebugMode;
  isRunning?: boolean;
  pendingUserInput?: PendingUserInput | null;
}>();

const emit = defineEmits<{
  resume: [action: 'resume' | 'step' | 'step_in' | 'step_out' | 'stop'];
  'run-until': [condition: { checkpoint?: string; agent_name?: string; tool_name?: string; iteration?: number }];
  'set-breakpoint': [eventType: string, agentName: string];
  'clear-breakpoint': [eventType: string, agentName: string];
  'clear-all': [];
  'request-pause': [];
  'set-mode': [mode: DebugMode, tools?: string[]];
  'answer-user-input': [text: string];
}>();

// User-input answer input — only active when pendingUserInput is set.
const userInputAnswer = ref('');
function submitUserInputAnswer() {
  const text = userInputAnswer.value.trim();
  if (!text) return;
  emit('answer-user-input', text);
  userInputAnswer.value = '';
}

const showAdd = ref(false);
const showRunUntil = ref(false);
const runUntilType = ref<'checkpoint' | 'agent' | 'tool' | 'iteration'>('checkpoint');
const runUntilValue = ref('');
const newEventType = ref('pre_thought');
const newAgentName = ref('*');

function submitRunUntil() {
  const cond: Record<string, unknown> = {};
  const v = runUntilValue.value.trim();
  if (!v) return;
  switch (runUntilType.value) {
    case 'checkpoint': cond.checkpoint = v; break;
    case 'agent': cond.agent_name = v; break;
    case 'tool': cond.tool_name = v; break;
    case 'iteration': cond.iteration = parseInt(v, 10); break;
  }
  emit('run-until', cond);
  showRunUntil.value = false;
  runUntilValue.value = '';
}

const eventTypeOptions = BREAKPOINT_OPTIONS;

function addBreakpoint() {
  emit('set-breakpoint', newEventType.value, newAgentName.value);
  showAdd.value = false;
}
</script>

<template>
  <div class="breakpoint-bar" :class="{ paused: isPaused || !!pendingUserInput }">
    <!-- User-input wait state: agent called user_input and is blocked.
         Shown above the normal controls so it's the clear next action. -->
    <div v-if="pendingUserInput" class="user-input-row">
      <div class="user-input-label">
        <span class="ui-badge">USER INPUT</span>
        <span class="ui-agent">{{ pendingUserInput.agentName }}</span>
        <span class="ui-question">{{ pendingUserInput.question }}</span>
      </div>
      <form class="user-input-form" @submit.prevent="submitUserInputAnswer">
        <input
          v-model="userInputAnswer"
          type="text"
          class="user-input-field"
          :placeholder="pendingUserInput.default || 'Type answer…'"
          autofocus
        />
        <button type="submit" class="ctrl-btn answer" :disabled="!userInputAnswer.trim()">
          Send
        </button>
      </form>
    </div>

    <!-- Mode selector -->
    <div class="mode-selector">
      <button
        v-for="m in (['pipeline', 'agent', 'tool', 'custom'] as DebugMode[])"
        :key="m"
        class="mode-btn"
        :class="{ active: debugMode === m }"
        @click="emit('set-mode', m)"
      >{{ m }}</button>
    </div>

    <!-- Pause indicator -->
    <div v-if="isPaused" class="paused-indicator">
      <span class="pause-icon">||</span>
      <span class="pause-text">
        Paused{{ pausedAgent ? ` at ${pausedAgent}` : '' }}{{ pausedCheckpoint ? ` (${pausedCheckpoint})` : '' }}
        <template v-if="pausedContext">
          — iter {{ pausedContext.iteration }}/{{ pausedContext.max_iterations || '∞' }}<template v-if="pausedContext.total_tokens_in || pausedContext.total_tokens_out">, {{ (((pausedContext.total_tokens_in ?? 0) + (pausedContext.total_tokens_out ?? 0)) / 1000).toFixed(1) }}K tokens</template>
        </template>
      </span>
    </div>

    <!-- Controls -->
    <div class="controls">
      <button
        v-if="isPaused"
        class="ctrl-btn resume"
        title="Resume execution"
        @click="emit('resume', 'resume')"
      >&#9654; Resume</button>
      <button
        v-if="isPaused"
        class="ctrl-btn step"
        title="Step to next checkpoint"
        @click="emit('resume', 'step')"
      >&#9193; Step</button>
      <button
        v-if="isPaused"
        class="ctrl-btn step-in"
        title="Step into delegated agent"
        @click="emit('resume', 'step_in')"
      >&#8595; Step In</button>
      <button
        v-if="isPaused"
        class="ctrl-btn step-out"
        title="Step out to parent agent"
        @click="emit('resume', 'step_out')"
      >&#8593; Step Out</button>
      <button
        v-if="isPaused"
        class="ctrl-btn run-until"
        title="Run until condition"
        @click="showRunUntil = !showRunUntil"
      >&#10145; Run Until</button>
      <button
        v-if="isPaused"
        class="ctrl-btn stop"
        title="Stop execution"
        @click="emit('resume', 'stop')"
      >&#9209; Stop</button>

      <button
        v-if="!isPaused && isRunning"
        class="ctrl-btn pause"
        title="Pause at next checkpoint"
        @click="emit('request-pause')"
      >Pause</button>

      <!-- Breakpoint management -->
      <div class="bp-section">
        <button
          class="ctrl-btn bp-toggle"
          @click="showAdd = !showAdd"
        >{{ showAdd ? 'Cancel' : '+ Breakpoint' }}</button>
        <button
          v-if="breakpoints.length > 0"
          class="ctrl-btn clear-all"
          @click="emit('clear-all')"
        >Clear All</button>
      </div>
    </div>

    <!-- Add breakpoint form -->
    <div v-if="showAdd" class="bp-form">
      <ComboBox
        :model-value="newEventType"
        :options="eventTypeOptions"
        placeholder="Checkpoint type"
        :allow-custom="true"
        @update:model-value="newEventType = $event"
      />
      <input
        v-model="newAgentName"
        type="text"
        class="bp-input"
        placeholder="Agent (* = all)"
      />
      <button class="ctrl-btn add" @click="addBreakpoint">Add</button>
    </div>

    <!-- Run Until form -->
    <div v-if="showRunUntil" class="bp-form">
      <ComboBox
        :model-value="runUntilType"
        :options="[{value:'checkpoint',label:'Checkpoint'},{value:'agent',label:'Agent'},{value:'tool',label:'Tool'},{value:'iteration',label:'Iteration'}]"
        placeholder="Run until..."
        :allow-custom="false"
        @update:model-value="runUntilType = $event as any"
      />
      <input
        v-model="runUntilValue"
        type="text"
        class="bp-input"
        :placeholder="runUntilType === 'iteration' ? 'N' : 'name'"
        @keyup.enter="submitRunUntil"
      />
      <button class="ctrl-btn add" @click="submitRunUntil">Go</button>
    </div>

    <!-- Active breakpoints -->
    <div v-if="breakpoints.length > 0" class="bp-list">
      <span
        v-for="bp in breakpoints"
        :key="`${bp.event_type}-${bp.agent_name}`"
        class="bp-tag"
      >
        {{ bp.event_type }}{{ bp.agent_name !== '*' ? ` @ ${bp.agent_name}` : '' }}
        <button
          class="bp-remove"
          @click="emit('clear-breakpoint', bp.event_type, bp.agent_name)"
        >x</button>
      </span>
    </div>
  </div>
</template>

<style scoped>
.breakpoint-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  background: var(--surface-2);
  border-bottom: 1px solid var(--border-subtle);
  font-size: 12px;
  color: var(--text-primary);
}
.breakpoint-bar.paused {
  background: rgba(245, 158, 11, 0.1);
  border-bottom-color: var(--status-paused);
}

.user-input-row {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
  padding: 8px 10px;
  margin: -6px -12px 4px;
  background: rgba(99, 102, 241, 0.12);
  border-bottom: 1px solid rgba(99, 102, 241, 0.35);
}
.user-input-label {
  display: flex;
  align-items: baseline;
  gap: 8px;
  flex-wrap: wrap;
}
.ui-badge {
  font-family: var(--font-mono);
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.08em;
  padding: 2px 6px;
  background: var(--accent-agent);
  color: var(--surface-0);
  border-radius: 2px;
}
.ui-agent {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--text-secondary);
}
.ui-question {
  font-size: 13px;
  color: var(--text-primary);
}
.user-input-form {
  display: flex;
  gap: 6px;
}
.user-input-field {
  flex: 1;
  padding: 6px 10px;
  font-size: 13px;
  font-family: var(--font-sans);
  background: var(--surface-0);
  color: var(--text-primary);
  border: 1px solid var(--border-subtle);
  border-radius: 4px;
}
.user-input-field:focus {
  outline: none;
  border-color: var(--accent-agent);
}
.ctrl-btn.answer {
  background: var(--accent-agent);
  color: var(--surface-0);
}
.ctrl-btn.answer:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.paused-indicator {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--status-paused);
  font-weight: 600;
}
.pause-icon {
  font-weight: 900;
  font-size: 14px;
}

.controls {
  display: flex;
  align-items: center;
  gap: 4px;
}

.ctrl-btn {
  padding: 3px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--node-radius);
  background: var(--surface-3);
  font-size: 11px;
  cursor: pointer;
  color: var(--text-primary);
}
.ctrl-btn:disabled {
  opacity: 0.4;
  cursor: default;
}
.ctrl-btn.resume { color: var(--status-running); border-color: var(--status-running); }
.ctrl-btn.resume:hover { background: var(--status-running); color: white; }
.ctrl-btn.step { color: var(--accent-agent); border-color: var(--accent-agent); }
.ctrl-btn.step:hover { background: var(--accent-agent); color: white; }
.ctrl-btn.stop { color: var(--status-error); border-color: var(--status-error); }
.ctrl-btn.stop:hover { background: var(--status-error); color: white; }
.ctrl-btn.step-in { color: var(--accent-skill); border-color: var(--accent-skill); }
.ctrl-btn.step-in:hover { background: var(--accent-skill); color: white; }
.ctrl-btn.step-out { color: var(--accent-orch); border-color: var(--accent-orch); }
.ctrl-btn.step-out:hover { background: var(--accent-orch); color: white; }
.ctrl-btn.run-until { color: var(--accent-tool); border-color: var(--accent-tool); }
.ctrl-btn.run-until:hover { background: var(--accent-tool); color: white; }
.ctrl-btn.add { color: var(--accent-agent); border-color: var(--accent-agent); }
.ctrl-btn.add:hover { background: var(--accent-agent); color: white; }
.ctrl-btn.clear-all { color: var(--status-error); border-color: var(--status-error); margin-left: 4px; }
.ctrl-btn.clear-all:hover { background: var(--status-error); color: white; }

.bp-section {
  margin-left: 8px;
}

.bp-form {
  display: flex;
  align-items: center;
  gap: 4px;
  width: 100%;
  margin-top: 4px;
}

.bp-select, .bp-input {
  padding: 3px 6px;
  border: 1px solid var(--input-border);
  border-radius: var(--node-radius);
  font-size: 11px;
  outline: none;
  background: var(--input-bg);
  color: var(--input-text);
}
.bp-select:focus, .bp-input:focus { border-color: var(--border-focus); }
.bp-input { width: 120px; }

.bp-list {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  width: 100%;
  margin-top: 2px;
}

.bp-tag {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 8px;
  background: rgba(239, 68, 68, 0.15);
  color: var(--status-error);
  border-radius: 3px;
  font-size: 10px;
  font-weight: 500;
}

.bp-remove {
  background: none;
  border: none;
  cursor: pointer;
  color: var(--status-error);
  font-size: 11px;
  padding: 0 2px;
  line-height: 1;
}
.bp-remove:hover { color: #ff6b6b; }

.mode-selector {
  display: flex;
  gap: 2px;
  margin-right: 8px;
}
.mode-btn {
  padding: 2px 8px;
  border: 1px solid var(--border-default);
  background: var(--surface-3);
  font-size: 10px;
  text-transform: capitalize;
  cursor: pointer;
  border-radius: 3px;
  color: var(--text-secondary);
}
.mode-btn:first-child { border-radius: 3px 0 0 3px; }
.mode-btn:last-child { border-radius: 0 3px 3px 0; }
.mode-btn:not(:first-child):not(:last-child) { border-radius: 0; }
.mode-btn.active {
  background: var(--accent-agent);
  color: white;
  border-color: var(--accent-agent);
}
.mode-btn:hover:not(.active) { background: var(--surface-4); }

.ctrl-btn.pause { color: var(--status-paused); border-color: var(--status-paused); }
.ctrl-btn.pause:hover { background: var(--status-paused); color: white; }
</style>
