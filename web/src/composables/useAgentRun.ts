import { ref, type Ref } from 'vue';

export interface ConfigEntry {
  id: string;
  name: string;
  path: string;
  agents: string[];
  tools: string[];
  strategy?: string;
  interactive?: boolean;
}

export function isDeletableConfig(c: ConfigEntry): boolean {
  // Inline uploads live under /tmp or equivalent OS temp dirs; on-disk
  // configs from examples/, configs/ are read-only.
  return /\brakitsu-configs\b/.test(c.path);
}

export type RunStatus = 'idle' | 'running' | 'completed' | 'error' | 'cancelled';

export function useAgentRun(baseUrl: Ref<string>) {
  const configs = ref<ConfigEntry[]>([]);
  const runStatus = ref<RunStatus>('idle');
  const sessionId = ref<string | null>(null);
  const runError = ref<string | null>(null);
  const runResult = ref<string | null>(null);
  const elapsedMs = ref(0);

  function apiUrl(path: string): string {
    const base = baseUrl.value.startsWith('http') ? baseUrl.value : `http://${baseUrl.value}`;
    return `${base.replace(/\/+$/, '')}${path}`;
  }

  async function fetchConfigs() {
    try {
      const res = await fetch(apiUrl('/api/configs'));
      configs.value = await res.json();
    } catch {
      configs.value = [];
    }
  }

  async function uploadConfig(file: File): Promise<ConfigEntry | null> {
    try {
      const isZip = file.name.endsWith('.zip');
      const form = new FormData();
      form.append('config', file);
      const endpoint = isZip ? '/api/configs/upload-zip' : '/api/configs/upload';
      const res = await fetch(apiUrl(endpoint), {
        method: 'POST',
        body: form,
      });
      if (!res.ok) {
        const data = await res.json();
        runError.value = data.error || 'Upload failed';
        return null;
      }
      const entry: ConfigEntry = await res.json();
      await fetchConfigs();
      return entry;
    } catch {
      runError.value = 'Upload failed';
      return null;
    }
  }

  async function fetchWorkdir(): Promise<string> {
    try {
      const res = await fetch(apiUrl('/api/workdir'));
      const data = await res.json() as { workdir: string };
      return data.workdir ?? '';
    } catch {
      return '';
    }
  }

  async function startRun(configId: string, query: string, timeout = 300, workdir = '', debug = false, envVars: Record<string, string> = {}, breakpoints: { event_type: string; agent_name: string }[] = [], projectId = '', runId?: string): Promise<string | null> {
    runError.value = null;
    runResult.value = null;
    try {
      const body: Record<string, unknown> = { config_id: configId, query, timeout, debug };
      if (projectId) body.project_id = projectId;
      if (runId) body.run_id = runId;
      if (workdir) body.workdir = workdir;
      if (Object.keys(envVars).length > 0) body.env_vars = envVars;
      if (breakpoints.length > 0) body.breakpoints = breakpoints;
      const res = await fetch(apiUrl('/api/run'), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      const data = await res.json();
      if (data.error) {
        runError.value = data.error;
        runStatus.value = 'error';
        return null;
      }
      sessionId.value = data.session_id;
      runStatus.value = 'running';
      pollStatus();
      return data.session_id ?? null;
    } catch {
      runError.value = 'Failed to start run';
      runStatus.value = 'error';
      return null;
    }
  }

  async function stopRun() {
    try {
      await fetch(apiUrl('/api/run/stop'), { method: 'POST' });
    } catch {
      // ignore
    }
  }

  let pollTimer: ReturnType<typeof setTimeout> | null = null;

  async function pollStatus() {
    if (pollTimer) clearTimeout(pollTimer);
    try {
      const res = await fetch(apiUrl('/api/run'));
      const data = await res.json();
      runStatus.value = data.status ?? 'idle';
      if (data.elapsed_ms) elapsedMs.value = data.elapsed_ms;
      if (data.result) runResult.value = data.result;
      if (data.error && data.status !== 'running') runError.value = data.error;

      if (data.status === 'running') {
        pollTimer = setTimeout(pollStatus, 1000);
      }
    } catch {
      // ignore
    }
  }

  function reset() {
    runStatus.value = 'idle';
    sessionId.value = null;
    runError.value = null;
    runResult.value = null;
    elapsedMs.value = 0;
    if (pollTimer) {
      clearTimeout(pollTimer);
      pollTimer = null;
    }
  }

  function cleanup() {
    if (pollTimer) {
      clearTimeout(pollTimer);
      pollTimer = null;
    }
  }

  return {
    configs, runStatus, sessionId, runError, runResult, elapsedMs,
    fetchConfigs, fetchWorkdir, uploadConfig, startRun, stopRun, reset, cleanup,
  };
}
