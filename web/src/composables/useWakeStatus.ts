import { onBeforeUnmount, ref, watch, type Ref } from 'vue'
import type { WakeStatus } from '../types/wake'

const POLL_MS = 5000

// getAuthHeaders returns fetch headers with Authorization if a token is available.
function getAuthHeaders(): HeadersInit {
  const headers: HeadersInit = {}
  // Check if RAKITSU_API_TOKEN was set at serve startup via env or UI config
  // Try sessionStorage first (user-configured), then localStorage (persisted preference)
  const token = sessionStorage.getItem('rakitsu_api_token') || localStorage.getItem('rakitsu_api_token')
  if (token) {
    headers['Authorization'] = `Bearer ${token}`
  }
  return headers
}

export function useWakeStatus(sessionId: Ref<string>) {
  const status = ref<WakeStatus | null>(null)
  const error = ref('')
  const busy = ref(false)
  let timer: ReturnType<typeof setInterval> | undefined

  const base = () => `/api/chat/${encodeURIComponent(sessionId.value)}/wake`

  async function refresh() {
    try {
      const res = await fetch(`${base()}/status`, { headers: getAuthHeaders() })
      if (!res.ok) throw new Error(`status ${res.status}`)
      status.value = (await res.json()) as WakeStatus
      error.value = ''
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    }
  }

  async function act(action: 'stop' | 'resume') {
    busy.value = true
    try {
      const res = await fetch(`${base()}/${action}`, { method: 'POST', headers: getAuthHeaders() })
      if (!res.ok) throw new Error(`${action} failed: ${res.status}`)
      error.value = ''
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      busy.value = false
      await refresh()
    }
  }

  function stopPolling() {
    if (timer) clearInterval(timer)
    timer = undefined
  }

  watch(
    sessionId,
    () => {
      stopPolling()
      status.value = null
      void refresh()
      timer = setInterval(refresh, POLL_MS)
    },
    { immediate: true },
  )

  onBeforeUnmount(stopPolling)

  return { status, error, busy, refresh, stop: () => act('stop'), resume: () => act('resume') }
}
