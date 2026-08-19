export interface PetAudioSpeechRequest {
  petId: string
  requestId: string
  provider: unknown
  text: string
  voice?: string
  instruction?: string
  voiceMode?: string
  voiceTag?: string
}

export interface PetAudioBridge {
  startSpeechStream(request: PetAudioSpeechRequest): Promise<unknown>
  synthesizeSpeech(request: PetAudioSpeechRequest): Promise<unknown>
  cancelSpeech(requestId: string): Promise<unknown>
}

export interface PetAudioStreamEvent {
  type: string
  petId: string
  requestId: string
  sequence: number | null
  data: unknown
  mediaType: string
  format: string
  error: unknown
}

export interface PetAudioPlaybackOptions {
  preferStream?: boolean
}

export const PET_AUDIO_PCM_SAMPLE_RATE = 24_000
const PET_AUDIO_STREAM_TIMEOUT_MS = 120_000
const PET_AUDIO_ELEMENT_TIMEOUT_MS = 120_000

interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
  reject: (reason: unknown) => void
}

interface ActivePlayback {
  sessionId: number
  request: PetAudioSpeechRequest
  streamRequestId: string | null
  context: AudioContext | null
  lastSequence: number
  pendingPCMByte: number | null
  receivedPCM: boolean
  completed: boolean
  settled: boolean
  cancelled: boolean
  nextTime: number
  finishTimer: ReturnType<typeof setTimeout> | null
  streamTimeout: ReturnType<typeof setTimeout> | null
  mediaCleanup: ((interrupt: boolean) => void) | null
  sources: Set<AudioBufferSourceNode>
  done: Deferred<boolean>
}

interface AudioContextGlobals {
  AudioContext?: new () => AudioContext
  webkitAudioContext?: new () => AudioContext
}

function createDeferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function parseRecord(value: unknown): Record<string, unknown> | null {
  if (isRecord(value)) return value
  if (typeof value !== 'string' || !value.trim()) return null
  try {
    const parsed: unknown = JSON.parse(value)
    return isRecord(parsed) ? parsed : null
  } catch {
    return null
  }
}

function asNonEmptyString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function asSequence(value: unknown): number | null {
  const sequence = typeof value === 'number' ? value : Number(value)
  return Number.isSafeInteger(sequence) && sequence > 0 ? sequence : null
}

function decodeBase64(value: string): Uint8Array | null {
  const commaIndex = value.indexOf(',')
  const raw = commaIndex >= 0 ? value.slice(commaIndex + 1) : value
  const compact = raw.replace(/\s/g, '').replace(/-/g, '+').replace(/_/g, '/')
  if (!compact || compact.length % 4 === 1 || !/^[A-Za-z0-9+/]*={0,2}$/.test(compact)) return null
  const padded = compact + '='.repeat((4 - (compact.length % 4)) % 4)
  try {
    const binary = atob(padded)
    const bytes = new Uint8Array(binary.length)
    for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index)
    return bytes.length > 0 ? bytes : null
  } catch {
    return null
  }
}

/**
 * Wails 对 []byte 通常序列化为 base64，但浏览器 mock、旧 runtime 和测试宿主
 * 可能分别给出 ArrayBuffer、TypedArray 或 number[]；统一在事件边界复制一份，
 * 防止后续播放器持有宿主仍可能复用的可变底层 buffer。
 */
export function normalizePetAudioBytes(value: unknown, depth = 0): Uint8Array | null {
  if (depth > 3) return null
  if (typeof value === 'string') return decodeBase64(value)
  if (value instanceof ArrayBuffer) return new Uint8Array(value.slice(0))
  if (ArrayBuffer.isView(value)) {
    return new Uint8Array(value.buffer.slice(value.byteOffset, value.byteOffset + value.byteLength))
  }
  if (Array.isArray(value) && value.every((item) => typeof item === 'number' && Number.isFinite(item))) {
    const bytes = new Uint8Array(value.length)
    for (let index = 0; index < value.length; index += 1) {
      bytes[index] = Math.max(0, Math.min(255, Math.trunc(value[index] as number)))
    }
    return bytes.length > 0 ? bytes : null
  }
  if (isRecord(value)) {
    for (const key of ['base64', 'data', 'audio', 'bytes']) {
      const nested = normalizePetAudioBytes(value[key], depth + 1)
      if (nested) return nested
    }
  }
  return null
}

export function normalizePetAudioEvent(value: unknown): PetAudioStreamEvent | null {
  const source = parseRecord(value)
  if (!source) return null
  const payload = isRecord(source.payload) ? source.payload : source
  const requestId = asNonEmptyString(payload.requestId ?? payload.request_id)
  const type = asNonEmptyString(payload.type).toLowerCase()
  if (!requestId || !type) return null
  return {
    type,
    petId: asNonEmptyString(payload.petId ?? payload.pet_id),
    requestId,
    sequence: asSequence(payload.sequence),
    data: payload.data,
    mediaType: asNonEmptyString(payload.mediaType ?? payload.media_type).toLowerCase(),
    format: asNonEmptyString(payload.format).toLowerCase(),
    error: payload.error
  }
}

function mediaTypeWithoutParameters(value: string): string {
  return value.split(';', 1)[0]?.trim().toLowerCase() ?? ''
}

function isPCM16Event(event: PetAudioStreamEvent): boolean {
  const format = mediaTypeWithoutParameters(event.format)
  const mediaType = mediaTypeWithoutParameters(event.mediaType)
  return format === 'pcm16' || format === 'pcm' || format === 'audio/pcm' || format === 'audio/raw' ||
    format === 'audio/l16' || mediaType === 'audio/pcm' || mediaType === 'audio/raw' || mediaType === 'audio/l16'
}

/**
 * 将任意 chunk 边界拼回完整的 little-endian sample。网络层偶尔会把两个字节
 * 拆开，不能用 Math.floor(length / 2) 直接静默丢掉尾字节，否则后续音频会整体错位。
 */
export function appendPCM16LittleEndian(
  pendingByte: number | null,
  bytes: Uint8Array
): { samples: Float32Array; pendingByte: number | null } {
  if (bytes.length === 0) return { samples: new Float32Array(0), pendingByte }
  const totalBytes = bytes.length + (pendingByte === null ? 0 : 1)
  const sampleCount = Math.floor(totalBytes / 2)
  const samples = new Float32Array(sampleCount)
  let byteOffset = 0
  let sampleOffset = 0

  if (pendingByte !== null && bytes.length > 0) {
    let value = pendingByte | (bytes[0] << 8)
    if (value >= 0x8000) value -= 0x10000
    samples[sampleOffset] = value / 0x8000
    sampleOffset += 1
    byteOffset = 1
  }

  while (sampleOffset < sampleCount) {
    const low = bytes[byteOffset]
    const high = bytes[byteOffset + 1]
    let value = low | (high << 8)
    if (value >= 0x8000) value -= 0x10000
    samples[sampleOffset] = value / 0x8000
    sampleOffset += 1
    byteOffset += 2
  }

  return {
    samples,
    pendingByte: byteOffset < bytes.length ? bytes[byteOffset] : null
  }
}

function speechResultAudio(value: unknown): { bytes: Uint8Array; mediaType: string } | null {
  if (!isRecord(value)) return null
  const bytes = normalizePetAudioBytes(value.audio ?? value.data ?? value.base64)
  if (!bytes) return null
  const mediaType = asNonEmptyString(value.mediaType ?? value.media_type) || 'audio/mpeg'
  return { bytes, mediaType }
}

function errorText(value: unknown): string {
  if (typeof value === 'string' && value.trim()) return value.trim()
  if (isRecord(value)) {
    const message = asNonEmptyString(value.message ?? value.code)
    if (message) return message
  }
  return 'pet audio stream failed'
}

export class PetAudioPlayer {
  private context: AudioContext | null = null
  private active: ActivePlayback | null = null
  private sessionId = 0
  private disposed = false
  private readonly bridge: PetAudioBridge

  constructor(bridge: PetAudioBridge) {
    this.bridge = bridge
  }

  /**
   * 句子按顺序等待上一句完成；新会话会清空旧队列并取消当前远端请求。
   * chunk 本身则通过 AudioContext 的 nextTime 串行排程，避免并发 source 互相覆盖。
   */
  async playSentences(
    requests: readonly PetAudioSpeechRequest[],
    options: PetAudioPlaybackOptions = {}
  ): Promise<boolean> {
    const queue = requests.filter((request) => request.text.trim() && request.requestId.trim())
    if (this.disposed || queue.length === 0) return false

    this.stop()
    const currentSessionId = this.sessionId
    for (const request of queue) {
      if (currentSessionId !== this.sessionId || this.disposed) return false
      const completed = await this.playOne(request, currentSessionId, options)
      if (!completed) return false
    }
    return true
  }

  /** Wails 事件监听器只把原始 event.data 转交给这一处 owner。 */
  handleEvent(value: unknown, petId: string): void {
    const event = normalizePetAudioEvent(value)
    const active = this.active
    if (!event || !active || active.streamRequestId !== event.requestId) return
    if (event.petId && event.petId !== petId) return
    if (active.settled || active.cancelled) return

    // 同一 request 的事件必须单调消费；重复事件和取消竞态中的旧事件直接丢弃。
    if (event.sequence !== null) {
      if (event.sequence <= active.lastSequence) return
      active.lastSequence = event.sequence
    }

    switch (event.type) {
      case 'started':
        return
      case 'chunk':
        if (active.completed || !isPCM16Event(event)) {
          this.failActive(active, new Error('pet audio stream is not PCM16'))
          return
        }
        {
          const bytes = normalizePetAudioBytes(event.data)
          if (!bytes) {
            this.failActive(active, new Error('pet audio chunk is invalid'))
            return
          }
          try {
            this.schedulePCM(active, bytes)
          } catch (error) {
            this.failActive(active, error)
          }
        }
        return
      case 'completed':
        if (active.pendingPCMByte !== null) {
          this.failActive(active, new Error('pet audio stream ended on a partial PCM16 sample'))
          return
        }
        if (!active.receivedPCM) {
          this.failActive(active, new Error('pet audio stream returned no PCM16 data'))
          return
        }
        active.completed = true
        this.finishStreamAfterTail(active)
        return
      case 'cancelled':
        active.cancelled = true
        // 后端取消事件可能晚于本地 stop 到达；这里也立即释放已排程 source，
        // 否则没有下一条事件时，旧 PCM 仍会在队列尾部响完。
        this.releaseActive(active, true)
        return
      case 'failed':
        this.failActive(active, new Error(errorText(event.error)))
        return
      default:
        return
    }
  }

  /** 停止当前句子、清空句子队列，并尽快让后端停止生成。 */
  stop(): void {
    this.sessionId += 1
    const active = this.active
    this.active = null
    if (!active) return
    active.cancelled = true
    void this.cancelRemote(active.streamRequestId ?? active.request.requestId)
    this.releaseActive(active, true)
  }

  /** 组件卸载时关闭 AudioContext，避免透明桌宠窗口反复打开后积累原生音频资源。 */
  dispose(): void {
    if (this.disposed) return
    this.stop()
    this.disposed = true
    const context = this.context
    this.context = null
    if (context && context.state !== 'closed') void context.close().catch(() => undefined)
  }

  private async playOne(
    request: PetAudioSpeechRequest,
    currentSessionId: number,
    options: PetAudioPlaybackOptions
  ): Promise<boolean> {
    const preferStream = options.preferStream !== false
    const context = preferStream ? await this.getAudioContext() : null
    if (context && currentSessionId === this.sessionId) {
      const streamActive = this.createActive(request, currentSessionId, context)
      try {
        const result = await this.bridge.startSpeechStream(request)
        if (!this.isActive(streamActive, currentSessionId)) return false
        if (isRecord(result)) {
          const responseRequestId = asNonEmptyString(result.requestId ?? result.request_id)
          if (responseRequestId && responseRequestId !== request.requestId) {
            throw new Error('pet audio stream request id mismatch')
          }
        }
        const completed = await streamActive.done.promise
        if (!this.isActive(streamActive, currentSessionId)) return false
        this.releaseActive(streamActive, !completed)
        return completed
      } catch (error) {
        if (!this.isActive(streamActive, currentSessionId)) return false
        // 流式接口或事件格式失败时只回退当前句子；后续句子仍保持串行，
        // 这样旧 stream 的迟到 chunk 不可能污染同步音频的播放权。
        this.releaseActive(streamActive, true)
        await this.cancelRemote(streamActive.streamRequestId ?? request.requestId)
        if (streamActive.cancelled || currentSessionId !== this.sessionId) return false
        console.warn('[Pet][audio] stream unavailable, falling back to sync speech:', error)
      }
    }

    if (currentSessionId !== this.sessionId || this.disposed) return false
    const syncRequest: PetAudioSpeechRequest = {
      ...request,
      // 流式尝试失败后换 requestId，避免 provider 仍在释放旧 request 时触发 in-flight 冲突。
      requestId: `${request.requestId}:sync`
    }
    return this.playSynchronously(syncRequest, currentSessionId)
  }

  private async playSynchronously(
    request: PetAudioSpeechRequest,
    currentSessionId: number
  ): Promise<boolean> {
    const active = this.createActive(request, currentSessionId, null)
    try {
      const result = await this.bridge.synthesizeSpeech(request)
      if (!this.isActive(active, currentSessionId)) return false
      const audio = speechResultAudio(result)
      if (!audio) throw new Error('synchronous pet speech returned no audio')
      await this.playAudioElement(active, audio.bytes, audio.mediaType)
      if (!this.isActive(active, currentSessionId)) return false
      this.releaseActive(active, false)
      return true
    } catch (error) {
      if (this.isActive(active, currentSessionId)) {
        this.releaseActive(active, true)
        await this.cancelRemote(request.requestId)
      }
      throw error
    }
  }

  private createActive(
    request: PetAudioSpeechRequest,
    currentSessionId: number,
    context: AudioContext | null
  ): ActivePlayback {
    const active: ActivePlayback = {
      sessionId: currentSessionId,
      request,
      streamRequestId: context ? request.requestId : null,
      context,
      lastSequence: 0,
      pendingPCMByte: null,
      receivedPCM: false,
      completed: false,
      settled: false,
      cancelled: false,
      nextTime: 0,
      finishTimer: null,
      streamTimeout: null,
      mediaCleanup: null,
      sources: new Set(),
      done: createDeferred<boolean>()
    }
    this.active = active
    if (context) {
      // 事件丢失时不能让句子队列永久等待；超时后交给 playOne 的既有
      // 失败收敛和同步语音 fallback 处理，并同时取消远端生成。
      active.streamTimeout = setTimeout(() => {
        if (this.active === active && !active.settled && !active.cancelled) {
          this.failActive(active, new Error('pet audio stream timed out'))
        }
      }, PET_AUDIO_STREAM_TIMEOUT_MS)
    }
    return active
  }

  private isActive(active: ActivePlayback, currentSessionId: number): boolean {
    return !this.disposed && active.sessionId === currentSessionId && currentSessionId === this.sessionId && this.active === active && !active.cancelled
  }

  private async getAudioContext(): Promise<AudioContext | null> {
    if (this.disposed) return null
    if (this.context?.state === 'closed') this.context = null
    if (!this.context) {
      const globals = globalThis as typeof globalThis & AudioContextGlobals
      const Constructor = globals.AudioContext ?? globals.webkitAudioContext
      if (!Constructor) return null
      try {
        this.context = new Constructor()
      } catch {
        return null
      }
    }
    if (this.context.state === 'suspended') {
      try {
        await this.context.resume()
      } catch {
        // 浏览器自动播放策略可能拒绝 resume；同步 Audio 元素仍可作为兼容路径尝试。
        return null
      }
    }
    return this.context.state === 'closed' ? null : this.context
  }

  private schedulePCM(active: ActivePlayback, bytes: Uint8Array): void {
    const context = active.context
    if (!context) throw new Error('AudioContext is unavailable')
    const decoded = appendPCM16LittleEndian(active.pendingPCMByte, bytes)
    active.pendingPCMByte = decoded.pendingByte
    if (decoded.samples.length === 0) return

    const buffer = context.createBuffer(1, decoded.samples.length, PET_AUDIO_PCM_SAMPLE_RATE)
    const channelData = new Float32Array(decoded.samples.length)
    channelData.set(decoded.samples)
    buffer.copyToChannel(channelData, 0)
    const source = context.createBufferSource()
    source.buffer = buffer
    source.connect(context.destination)
    const startAt = Math.max(context.currentTime + 0.08, active.nextTime)
    source.start(startAt)
    active.nextTime = startAt + buffer.duration
    active.receivedPCM = true
    active.sources.add(source)
    if (!active.mediaCleanup) {
      active.mediaCleanup = (interrupt) => {
        for (const pendingSource of active.sources) {
          if (interrupt) {
            try {
              pendingSource.stop()
            } catch {
              // source 可能已经自然结束，stop 的重复调用不应阻断取消流程。
            }
          }
          try {
            pendingSource.disconnect()
          } catch {
            // disconnect 对已释放 source 的兼容行为依赖浏览器实现，释放失败可忽略。
          }
        }
        active.sources.clear()
      }
    }
    source.onended = () => {
      active.sources.delete(source)
      try {
        source.disconnect()
      } catch {
        // 已断开的 source 无需再次处理。
      }
    }
  }

  private finishStreamAfterTail(active: ActivePlayback): void {
    const context = active.context
    if (!context || active.settled) return
    const remainingMs = Math.max(0, (active.nextTime - context.currentTime) * 1_000 + 120)
    if (remainingMs === 0) {
      this.settleActive(active, true)
      return
    }
    active.finishTimer = setTimeout(() => {
      active.finishTimer = null
      if (this.active === active && !active.cancelled) this.settleActive(active, true)
    }, remainingMs)
  }

  private failActive(active: ActivePlayback, error: unknown): void {
    if (active.settled || active.cancelled) return
    active.settled = true
    active.done.reject(error)
  }

  private settleActive(active: ActivePlayback, completed: boolean): void {
    if (active.settled) return
    active.settled = true
    active.done.resolve(completed)
  }

  private releaseActive(active: ActivePlayback, interrupt: boolean): void {
    if (active.streamTimeout !== null) {
      clearTimeout(active.streamTimeout)
      active.streamTimeout = null
    }
    if (active.finishTimer !== null) {
      clearTimeout(active.finishTimer)
      active.finishTimer = null
    }
    active.mediaCleanup?.(interrupt)
    active.mediaCleanup = null
    if (!active.settled) this.settleActive(active, false)
    if (this.active === active) this.active = null
  }

  private async playAudioElement(active: ActivePlayback, bytes: Uint8Array, mediaType: string): Promise<void> {
    const url = URL.createObjectURL(new Blob([bytes as BlobPart], { type: mediaType }))
    const audio = new Audio(url)
    audio.preload = 'auto'

    await new Promise<void>((resolve, reject) => {
      let settled = false
      const cleanup = (interrupt: boolean): void => {
        if (settled) return
        settled = true
        clearTimeout(playbackTimeout)
        if (interrupt) audio.pause()
        audio.removeEventListener('ended', onEnded)
        audio.removeEventListener('error', onError)
        audio.removeEventListener('pause', onPause)
        URL.revokeObjectURL(url)
        if (active.mediaCleanup === cleanup) active.mediaCleanup = null
        resolve()
      }
      const onEnded = (): void => cleanup(false)
      const onPause = (): void => cleanup(false)
      const onError = (_error?: unknown): void => {
        if (settled) return
        settled = true
        clearTimeout(playbackTimeout)
        audio.removeEventListener('ended', onEnded)
        audio.removeEventListener('error', onError)
        audio.removeEventListener('pause', onPause)
        URL.revokeObjectURL(url)
        if (active.mediaCleanup === cleanup) active.mediaCleanup = null
        reject(new Error('synchronous pet speech playback failed'))
      }
      const playbackTimeout = setTimeout(() => {
        onError(new Error('synchronous pet speech playback timed out'))
      }, PET_AUDIO_ELEMENT_TIMEOUT_MS)
      active.mediaCleanup = cleanup
      audio.addEventListener('ended', onEnded)
      audio.addEventListener('error', onError)
      audio.addEventListener('pause', onPause)
      void audio.play().catch(onError)
    })
  }

  private async cancelRemote(requestId: string): Promise<void> {
    if (!requestId) return
    try {
      await this.bridge.cancelSpeech(requestId)
    } catch {
      // 请求可能已经完成或被后端释放；前端播放权已先失效，不再把取消失败冒泡到 UI。
    }
  }
}
