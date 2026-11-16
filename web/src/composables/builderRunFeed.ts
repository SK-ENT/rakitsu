import { shallowRef } from 'vue';
import type { AgentEvent } from '../types';

// Builder owns the run subscription. Docked readers can display its events
// without opening another stream or modifying the Builder buffer.
export const builderRunEvents = shallowRef<readonly AgentEvent[]>([]);
export const builderReplayEvent = shallowRef<{
  sessionId: string;
  projectId?: string;
  event: AgentEvent;
  events: readonly AgentEvent[];
  position: number;
} | null>(null);
