// Mirrors internal/server/wake_status.go (WakeStatus).
export type WakeState = 'ok' | 'stale' | 'stopped' | 'failed' | 'starting'

export interface WakeStatus {
  id: string
  state: WakeState
  label?: string
  last_tick?: string
  last_alarm?: string
  turns_last_hour: number
  max_turns_per_hour: number
  running_tasks: number
  next_tick_in_seconds: number
  interval_seconds: number
}
