import { ref } from 'vue';
import type { AgentEvent } from '../types';

export interface SessionInfo {
  name: string;
  query: string;
  config_path: string;
  agents: string[];
  start_time: string;
}

const EVENT_TYPES = [
  'AGENT_START', 'AGENT_END', 'THOUGHT_START', 'THOUGHT_END',
  'TOOL_CALL_START', 'TOOL_CALL_END', 'AGENT_HANDOFF', 'AGENT_MESSAGE',
  'TOKEN_USAGE', 'ERROR', 'EXECUTION_COMPLETE',
  'REFLECTION_START', 'REFLECTION_END', 'GROUND_CHECK_START', 'GROUND_CHECK_END',
  'PIPELINE_START', 'PIPELINE_END', 'PIPELINE_STEP_START', 'PIPELINE_STEP_END',
  'DEBUG_PAUSED', 'DEBUG_RESUMED', 'TOKEN_CHUNK', 'REASONING_CHUNK', 'REPLAY_START', 'REPLAY_END',
  'RETRY_ATTEMPT', 'CONTEXT_COMPRESSED', 'RETRIEVAL_INJECTED',
];

export function createEventStream() {
  const events = ref<AgentEvent[]>([]);
  const isConnected = ref(false);
  const error = ref<string | null>(null);
  const sessionInfo = ref<SessionInfo | null>(null);

  let eventSource: EventSource | null = null;
  let baseUrl = '';

  async function fetchSessionInfo() {
    try {
      const res = await fetch(`${baseUrl}/api/session`);
      const data = await res.json();
      if (data.name) {
        sessionInfo.value = data as SessionInfo;
      }
    } catch {
      // Session info is optional
    }
  }

  const seenEventIds = new Set<string>();

  function connect(url: string = 'http://localhost:9100', sessionId?: string) {
    // If already connected to the same URL, don't reconnect — just reuse
    const normalizedUrl = url.replace(/\/+$/, '').replace(/\/events$/, '');
    if (eventSource && eventSource.readyState !== EventSource.CLOSED && baseUrl === normalizedUrl && !sessionId) {
      return;
    }

    if (eventSource) {
      eventSource.close();
    }

    baseUrl = normalizedUrl;
    let eventsUrl = `${baseUrl}/events`;
    if (sessionId) eventsUrl += `?session_id=${encodeURIComponent(sessionId)}`;

    eventSource = new EventSource(eventsUrl);

    eventSource.onopen = () => {
      seenEventIds.clear();
      for (const event of events.value) if (event.id) seenEventIds.add(event.id);
      isConnected.value = true;
      error.value = null;
      fetchSessionInfo();
    };

    const handler = (event: MessageEvent) => {
      try {
        const data = JSON.parse(event.data) as AgentEvent;
        if (data.id) {
          if (seenEventIds.has(data.id)) return;
          seenEventIds.add(data.id);
        }
        events.value.push(data);
      } catch (e) {
        console.error('Failed to parse event:', e);
      }
    };

    for (const type of EVENT_TYPES) {
      eventSource.addEventListener(type, handler);
    }

    eventSource.onerror = () => {
      error.value = 'Connection lost. Reconnecting...';
      isConnected.value = false;
    };
  }

  function disconnect() {
    if (eventSource) {
      eventSource.close();
      eventSource = null;
    }
    isConnected.value = false;
    sessionInfo.value = null;
  }

  function clearEvents() {
    seenEventIds.clear();
    events.value = [];
  }

  return {
    events,
    isConnected,
    error,
    sessionInfo,
    connect,
    disconnect,
    clearEvents,
  };
}

const workspaceEventStream = createEventStream();

export function useEventStream() {
  return workspaceEventStream;
}
