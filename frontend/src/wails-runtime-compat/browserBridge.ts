import { isWailsRuntimeAvailable } from './runtime'

const DEFAULT_BRIDGE_URL = 'http://127.0.0.1:18101/api/pet/call'
const BRIDGE_URL_QUERY_KEY = 'petBridge'
const REQUEST_TIMEOUT_MS = 35_000
let bridgeToken: string | null = null
let bridgeTokenRequest: Promise<string> | null = null
let eventSource: EventSource | null = null
let eventReconnectTimer: number | undefined

function normalizeBridgeURL(value: string): string | null {
  try {
    const parsed = new URL(value.trim())
    const hostname = parsed.hostname.toLowerCase()
    const isLoopback = hostname === 'localhost' || hostname === '127.0.0.1' ||
      hostname === '::1' || hostname === '[::1]'
    const pathname = parsed.pathname.replace(/\/+$/, '')
    if (!isLoopback || (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') ||
      parsed.username || parsed.password || parsed.search || parsed.hash ||
      pathname !== '/api/pet/call') {
      return null
    }
    return parsed.toString().replace(/\/$/, '')
  } catch {
    return null
  }
}

function bridgeURL(): string {
  const configured = (window as Window & { __CODESWITCH_PET_BRIDGE_URL__?: unknown })
    .__CODESWITCH_PET_BRIDGE_URL__
  if (typeof configured === 'string' && configured.trim()) {
    const normalized = normalizeBridgeURL(configured)
    if (normalized) return normalized
  }

  // 正常浏览器预览继续使用固定的本地 bridge；隔离验证或多实例场景通过 URL
  // 指定端口，避免为了测试新 bundle 去抢占旧桌面进程的 18101。hash 路由把
  // 查询参数放在 `#/pet/settings?...` 后面，因此 search 和 hash 两处都要读取。
  // 覆盖值必须仍然是本机 bridge，防止页面把本地 bridge token 发送给外部地址。
  const queryStrings = [window.location.search]
  const hashQueryIndex = window.location.hash.indexOf('?')
  if (hashQueryIndex >= 0) queryStrings.push(window.location.hash.slice(hashQueryIndex + 1))
  for (const query of queryStrings) {
    const override = new URLSearchParams(query).get(BRIDGE_URL_QUERY_KEY)
    if (override) {
      const normalized = normalizeBridgeURL(override)
      if (normalized) return normalized
    }
  }

  return DEFAULT_BRIDGE_URL
}

export function isBrowserBridgeAvailable(): boolean {
  return !isWailsRuntimeAvailable() && typeof window !== 'undefined' && typeof window.fetch === 'function'
}

function healthURL(): string {
  return bridgeURL().replace(/\/api\/pet\/call\/?$/, '/api/pet/health')
}

async function fetchBridgeToken(signal?: AbortSignal): Promise<string> {
  const response = await window.fetch(healthURL(), { method: 'GET', signal })
  const payload = await response.json() as { ok?: unknown; token?: unknown }
  if (!response.ok || payload.ok !== true || typeof payload.token !== 'string' || !payload.token.trim()) {
    throw new Error('宠物本地服务没有返回有效授权。')
  }
  bridgeToken = payload.token.trim()
  return bridgeToken
}

async function loadBridgeToken(signal?: AbortSignal): Promise<string> {
  if (bridgeToken) return bridgeToken
  if (bridgeTokenRequest) return bridgeTokenRequest
  bridgeTokenRequest = fetchBridgeToken(signal).finally(() => {
    bridgeTokenRequest = null
  })
  return bridgeTokenRequest
}

export async function callBrowserBridge(
  method: string,
  args: readonly unknown[],
  signal?: AbortSignal
): Promise<unknown> {
  if (!isBrowserBridgeAvailable()) {
    throw new Error('Browser pet bridge is unavailable.')
  }

  const controller = new AbortController()
  const abortFromCaller = (): void => controller.abort()
  if (signal?.aborted) controller.abort()
  signal?.addEventListener('abort', abortFromCaller, { once: true })
  const timeout = window.setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS)
  try {
    const token = await loadBridgeToken(controller.signal)
    if (controller.signal.aborted) throw new DOMException('The operation was aborted.', 'AbortError')
    const response = await window.fetch(bridgeURL(), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-CodeSwitch-Pet-Token': token },
      body: JSON.stringify({ method, args }),
      signal: controller.signal
    })
    const text = await response.text()
    let payload: unknown = null
    if (text.trim()) {
      try {
        payload = JSON.parse(text)
      } catch {
        throw new Error(`宠物 bridge 返回了无效响应（HTTP ${response.status}）。`)
      }
    }

    const record = payload && typeof payload === 'object' && !Array.isArray(payload)
      ? payload as { ok?: unknown; data?: unknown; error?: unknown }
      : null
    if (response.status === 401) {
      bridgeToken = null
    }
    if (!response.ok || record?.ok !== true) {
      const message = typeof record?.error === 'string' && record.error.trim()
        ? record.error.trim()
        : `宠物 bridge 请求失败（HTTP ${response.status}）。`
      throw new Error(message)
    }
    return record.data
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') {
      if (signal?.aborted) throw error
      throw new Error('连接宠物本地服务超时，请确认 CodeSwitch 桌面进程正在运行。')
    }
    if (error instanceof TypeError && /fetch/i.test(error.message)) {
      throw new Error('无法连接宠物本地服务，请先启动 CodeSwitch 桌面进程。')
    }
    throw error
  } finally {
    window.clearTimeout(timeout)
    signal?.removeEventListener('abort', abortFromCaller)
  }
}

export async function connectBrowserEventStream(
  dispatch: (name: string, data: unknown) => void
): Promise<() => void> {
  if (!isBrowserBridgeAvailable() || typeof EventSource === 'undefined') return () => undefined

  let stopped = false
  let reconnectDelay = 500

  const clearReconnectTimer = (): void => {
    if (eventReconnectTimer === undefined) return
    window.clearTimeout(eventReconnectTimer)
    eventReconnectTimer = undefined
  }

  const scheduleReconnect = (): void => {
    if (stopped || eventReconnectTimer !== undefined) return
    eventReconnectTimer = window.setTimeout(() => {
      eventReconnectTimer = undefined
      void connect()
    }, reconnectDelay)
    reconnectDelay = Math.min(10_000, reconnectDelay * 2)
  }

  const connect = async (): Promise<void> => {
    if (stopped) return
    try {
      const token = await loadBridgeToken()
      if (stopped) return
      if (eventSource) eventSource.close()
      const source = new EventSource(
        `${bridgeURL().replace(/\/api\/pet\/call\/?$/, '/api/pet/events')}?token=${encodeURIComponent(token)}`
      )
      eventSource = source
      source.onopen = () => {
        reconnectDelay = 500
      }
      source.onmessage = (event) => {
        try {
          const payload = JSON.parse(event.data) as { name?: unknown; data?: unknown }
          if (typeof payload.name === 'string' && payload.name.trim()) dispatch(payload.name, payload.data)
        } catch {
          // 单条事件损坏不应让后续 SSE 事件全部失效；下一次状态读取仍是最终兜底。
        }
      }
      source.onerror = () => {
        if (eventSource === source) eventSource = null
        source.close()
        // 桌面进程可能刚启动、重启或更换 token；重新取 health，不能永久复用旧 token。
        bridgeToken = null
        scheduleReconnect()
      }
    } catch {
      scheduleReconnect()
    }
  }

  void connect()
  return () => {
    stopped = true
    clearReconnectTimer()
    if (eventSource) {
      eventSource.close()
      eventSource = null
    }
  }
}
