<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Call, Events } from '../../wails-runtime-compat'
import { fetchAppSettings } from '../../services/appSettings'
import { ImagePlus, Loader2, Mic, SendHorizontal, Square, X } from '@lucide/vue'
import { petApi } from './petApi'
import {
  buildPetRuntimeContext,
  cleanPetAssistantText,
  extractPetPlan,
  formatPlanError,
  localPetTimeZone
} from './petPlan'
import { buildPetChatPersona } from './petChatProtocol'
import type {
  PetAgentConfig,
  PetDreamHistoryRecord,
  PetPlanRecord,
  PetPlanScript
} from './petTypes'

interface PetChatProps {
  petId?: string
  petName?: string
  agent?: PetAgentConfig | null
  dreams?: PetDreamHistoryRecord[]
}

const props = withDefaults(defineProps<PetChatProps>(), {
  petId: 'default',
  petName: 'Kapi',
  agent: null,
  dreams: () => []
})
const emit = defineEmits<{
  (event: 'close'): void
  // PetWindow 用这些事件把聊天状态同步到跟随宠物的气泡；Codex thread
  // 是历史事实源，聊天浮窗只负责当前请求状态，不展示 transcript。
  (event: 'status-change', payload: { text: string; tone: 'muted' | 'error' }): void
  (event: 'bubble', payload: { text: string; tone: 'muted' | 'error'; duration?: number }): void
}>()
const { t } = useI18n()

type ChatPhase = 'idle' | 'starting' | 'streaming' | 'cancelling' | 'error' | 'unavailable'
type PlanPhase = 'idle' | 'loading' | 'ready' | 'error'
type NormalizedEventType = 'started' | 'progress' | 'delta' | 'completed' | 'failed' | 'cancelled'

interface PetChatImage {
  id: string
  data: string
  mediaType: string
  previewUrl: string
}

interface PetChatImagePayload {
  data: string
  mediaType: string
}

interface ChatEvent {
  type: NormalizedEventType
  petId: string
  requestId: string
  sequence: number
  delta: string
  text: string
  errorCode: string
}

interface ChatAvailability {
  ready: boolean
  message: string
}

interface PetChatRequest {
  petId: string
  requestId: string
  persona: string
  runtimeContext: string
  userText: string
  images: PetChatImagePayload[]
}

interface PetTranscriptionRequest {
  petId: string
  provider: {
    platform: string
    providerId: string
    model: string
    capability: 'transcription'
    autoFallback: false
  }
  data: string
  mediaType: string
  fileName: string
}

interface PetTranscriptionProvider {
  platform: string
  providerId: string
  model: string
}

interface PetSchedulerSchedulePlanResult {
  planId?: string
  jobsEnqueued?: boolean
  planRecordPersisted?: boolean
}

const PET_AI_SERVICE = 'codeswitch/services.PetAIAPIService'
const PET_AI_METHODS = {
  startChat: PET_AI_SERVICE + '.StartChat',
  cancelChat: PET_AI_SERVICE + '.CancelChat',
  transcribeAudio: PET_AI_SERVICE + '.TranscribeAudio'
} as const

// 当前 main.go 注册的是 PetSchedulerAPI；payload 必须匹配 Go 的结构化输入，不能
// 复用源项目的 IPC cron:add/cron:delete，那套边界在 Wails 宿主中不存在。
const PET_SCHEDULER_SERVICE = 'codeswitch/services.PetSchedulerAPI'
const PET_SCHEDULER_METHODS = {
  schedulePlan: PET_SCHEDULER_SERVICE + '.SchedulePlan',
  cancel: PET_SCHEDULER_SERVICE + '.Cancel'
} as const

// 主控把核心 PetAIService 包装为 PetAIAPIService 后注册；事件仍由核心 emitter 统一转发。
const PET_AI_EVENT = 'pet.ai'

function hasChatWorkspaceBinding(agent: PetAgentConfig | null | undefined): boolean {
  // 新配置以 projectId 为事实源，旧版本只保存 projectFolder；后端 resolver
  // 已明确保留旧字段兼容读取，前端门禁必须与这个契约一致，不能提前拦截老宠物。
  return Boolean(agent?.projectId?.trim() || agent?.projectFolder?.trim())
}

const inputText = ref('')
const phase = ref<ChatPhase>('idle')
const failureMessage = ref('')
const plans = ref<PetPlanRecord[]>([])
const plansPhase = ref<PlanPhase>('idle')
const plansError = ref('')
const planBusyId = ref('')
const planNotice = ref('')
const cancelledPlanIds = ref(new Set<string>())
const pendingImages = ref<PetChatImage[]>([])
const attachmentMessage = ref('')
const imageInputRef = ref<HTMLInputElement | null>(null)
const voiceInputMessage = ref('')
const isStartingRecording = ref(false)
const isRecording = ref(false)
const isStoppingRecording = ref(false)
const isTranscribing = ref(false)

const PET_CHAT_IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'] as const
const PET_CHAT_MAX_IMAGES = 4
const PET_CHAT_MAX_IMAGE_BYTES = 128 * 1024
const PET_CHAT_MAX_TOTAL_IMAGE_BYTES = 192 * 1024
const PET_CHAT_MAX_VOICE_DURATION_MS = 60_000
// 默认 Go multipart 请求上限是 256 KiB；预留表单字段和边界开销，前端在
// 录音阶段先停住，避免用户录完才收到一个必然失败的上传请求。
const PET_CHAT_MAX_VOICE_BYTES = 240 * 1024
// Codex turn 是长生命周期请求，浏览器/MCP 工具链可以稳定运行数分钟；这里的
// 计时器只负责捕获“长时间没有任何协议进展”的失联状态，收到 progress/delta 后续期。
const PET_CHAT_REQUEST_WATCHDOG_MS = 5 * 60_000

let activeRequestId = ''
let activeRawAssistantText = ''
let lastEventSequence = 0
let lastFailedText = ''
let lastFailedImages: PetChatImage[] = []
let imageSelectionGeneration = 0
let stopChatEvent: (() => void) | null = null
let plansLoadInFlight = false
let plansReloadRequested = false
let voiceSessionGeneration = 0
let activeRecorder: MediaRecorder | null = null
let activeRecorderStream: MediaStream | null = null
let activeRecorderChunks: Blob[] = []
let activeRecorderPetId = ''
let activeRecorderMimeType = 'audio/webm'
let activeRecorderShouldSubmit = false
let activeRecorderByteLength = 0
let activeRecorderStopTimer: number | undefined
let activeRecorderStopReason: 'duration' | 'size' | '' = ''
let stoppingRecorder: MediaRecorder | null = null
let activeTranscriptionCall: ReturnType<typeof Call.ByName> | null = null
let activeRequestWatchdog: number | undefined
let componentMounted = false

const chatAvailability = computed<ChatAvailability>(() => {
  if (!props.agent) {
    return { ready: false, message: t('pet.chat.availability.loading') }
  }
  // Codex runtime 会按 petId 从后端解析 project，前端只用绑定 ID 做发送门禁；
  // provider/model 已不再属于主聊天请求，避免 UI 维护第二套路由事实源。
  if (!hasChatWorkspaceBinding(props.agent)) {
    return { ready: false, message: t('pet.chat.availability.bindingRequired') }
  }
  return { ready: true, message: '' }
})

const isBusy = computed(() =>
  phase.value === 'starting' || phase.value === 'streaming' || phase.value === 'cancelling'
)

const isComposerBusy = computed(() =>
  isBusy.value || isStartingRecording.value || isRecording.value || isStoppingRecording.value || isTranscribing.value
)

// 配置缺失不能把发送入口直接锁死，否则 sendMessage 内已有的可读配置提示永远无法触发；
// 真正发起请求前仍由 chatAvailability 做硬校验，避免把未绑定项目的消息送进 runtime。
const canSend = computed(() =>
  !isComposerBusy.value &&
  (inputText.value.trim().length > 0 || pendingImages.value.length > 0)
)

const statusText = computed(() => {
  if (attachmentMessage.value) return attachmentMessage.value
  if (isStartingRecording.value) return t('pet.chat.voice.starting')
  if (isRecording.value) return t('pet.chat.voice.recording')
  if (isTranscribing.value) return t('pet.chat.voice.transcribing')
  if (voiceInputMessage.value) return voiceInputMessage.value
  if (!chatAvailability.value.ready) return chatAvailability.value.message
  if (phase.value === 'starting') return t('pet.chat.status.connecting')
  if (phase.value === 'streaming') return t('pet.chat.status.replying')
  if (phase.value === 'cancelling') return t('pet.chat.status.stopping')
  if (phase.value === 'error') return failureMessage.value || t('pet.chat.status.failed')
  return t('pet.chat.status.online')
})

const statusTone = computed(() => {
  if (attachmentMessage.value) return 'error'
  if (voiceInputMessage.value) return 'error'
  if (!chatAvailability.value.ready || phase.value === 'unavailable') return 'muted'
  if (phase.value === 'error') return 'error'
  if (isBusy.value || isRecording.value || isTranscribing.value) return 'active'
  return 'ready'
})

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function asString(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function asPositiveNumber(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : 0
}

function decodeEventData(value: unknown): Record<string, unknown> {
  // Wails v3 的 app.Event.Emit 接收的是 variadic data，单个事件会在
  // WebView 侧变成 [event]；浏览器 bridge 则直接发送 event。两条入口必须
  // 在这里统一解包，否则桌面端只会显示发送前写入的“正在连接宠物”。
  const unwrapped = Array.isArray(value) && value.length === 1 ? value[0] : value
  if (isRecord(unwrapped)) return unwrapped
  if (typeof unwrapped !== 'string' || !unwrapped.trim()) return {}
  try {
    const parsed: unknown = JSON.parse(unwrapped)
    const parsedValue = Array.isArray(parsed) && parsed.length === 1 ? parsed[0] : parsed
    return isRecord(parsedValue)
      ? parsedValue
      : { text: typeof parsedValue === 'string' ? parsedValue : '' }
  } catch {
    return { text: unwrapped }
  }
}

function normalizeChatEvent(value: unknown): ChatEvent | null {
  const source = isRecord(value) ? value : decodeEventData(value)
  const data = decodeEventData(source.data)
  const typeValue = asString(source.type || data.type)
  const typeMap: Record<string, NormalizedEventType | undefined> = {
    start: 'started',
    started: 'started',
    progress: 'progress',
    delta: 'delta',
    usage: 'progress',
    completed: 'completed',
    done: 'completed',
    failed: 'failed',
    error: 'failed',
    cancelled: 'cancelled',
    canceled: 'cancelled'
  }
  const type = typeMap[typeValue]
  const requestId = asString(source.requestId || source.request_id || data.requestId || data.request_id)
  if (!type || !requestId) return null

  const errorValue = source.error ?? data.error
  const errorRecord = isRecord(errorValue) ? errorValue : null
  const errorCode = asString(errorRecord?.code) || asString(source.code) || asString(data.code) || asString(errorValue)
  const delta = asString(source.delta) || asString(data.delta)
  const text = asString(source.text) || asString(data.text) || asString(data.content)
  const sequence = asPositiveNumber(source.sequence || data.sequence)

  return {
    type,
    petId: asString(source.petId || source.pet_id || data.petId || data.pet_id),
    requestId,
    sequence,
    delta,
    text,
    errorCode
  }
}

function createRequestId(): string {
  if (typeof globalThis.crypto?.randomUUID === 'function') {
    return globalThis.crypto.randomUUID()
  }
  return 'pet-chat-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 10)
}

function cloneImages(images: PetChatImage[]): PetChatImage[] {
  return images.map((image) => ({ ...image }))
}

function buildPersona(): string {
  return buildPetChatPersona(props.agent?.systemPrompt, props.petName)
}

function buildChatRequest(
  requestId: string,
  userText: string,
  images: PetChatImage[]
): PetChatRequest {
  const agent = props.agent
  if (!hasChatWorkspaceBinding(agent)) {
    throw new Error('PET_CHAT_NOT_CONFIGURED')
  }

  const request: PetChatRequest = {
    petId: props.petId,
    requestId,
    persona: buildPersona(),
    runtimeContext: buildPetRuntimeContext(),
    userText,
    images: images.map(({ data, mediaType }) => ({ data, mediaType }))
  }
  return request
}

function createImageId(): string {
  return 'pet-image-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 10)
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = ''
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000))
  }
  return btoa(binary)
}

function petErrorCode(error: unknown): string {
  const seen = new Set<object>()

  function find(value: unknown, depth: number): string {
    if (depth > 8 || value == null) return ''
    if (typeof value === 'string') {
      try {
        return find(JSON.parse(value), depth + 1)
      } catch {
        const match = value.match(/\b(PET_[A-Z0-9_]+)\b/)
        return match?.[1] ?? ''
      }
    }
    if (typeof value !== 'object') return ''
    if (seen.has(value)) return ''
    seen.add(value)

    if (Array.isArray(value)) {
      for (const item of value) {
        const code = find(item, depth + 1)
        if (code) return code
      }
      return ''
    }

    const source = value as Record<string, unknown>
    const directCode = asString(source.code).trim()
    if (directCode) return directCode

    const causeCode = find(source.cause, depth + 1)
    if (causeCode) return causeCode

    const message = asString(source.message)
    if (message) {
      const parsedCode = find(message, depth + 1)
      if (parsedCode) return parsedCode
    }
    return ''
  }

  return find(error, 0)
}

function voiceErrorMessage(error: unknown): string {
  if (typeof DOMException !== 'undefined' && error instanceof DOMException && (error.name === 'NotAllowedError' || error.name === 'SecurityError')) {
    return t('pet.chat.voice.permissionDenied')
  }
  if (isMissingBindingError(error)) return t('pet.chat.voice.bindingUnavailable')

  const messageKeys: Record<string, string> = {
    PET_AI_DEPENDENCY_UNAVAILABLE: 'pet.chat.voice.bindingUnavailable',
    PET_AI_MEDIA_TYPE_INVALID: 'pet.chat.voice.failed',
    PET_AI_INVALID_REQUEST: 'pet.chat.voice.failed',
    PET_AI_RESPONSE_INVALID: 'pet.chat.voice.failed',
    PET_AI_TIMEOUT: 'pet.chat.voice.timeout',
    PET_AI_UPSTREAM_ERROR: 'pet.chat.voice.failed',
    PET_CAPABILITY_UNSUPPORTED: 'pet.chat.voice.providerUnsupported',
    PET_MODEL_NOT_CONFIGURED: 'pet.chat.voice.notConfigured',
    PET_PROVIDER_CONFIG_INVALID: 'pet.chat.voice.notConfigured',
    PET_PROVIDER_NOT_FOUND: 'pet.chat.voice.notConfigured',
    PET_REFERENCE_INVALID: 'pet.chat.voice.notConfigured'
  }
  const key = messageKeys[petErrorCode(error)]
  return key ? t(key) : t('pet.chat.voice.failed')
}

function stopMediaStream(stream: MediaStream | null): void {
  if (!stream) return
  for (const track of stream.getTracks()) {
    try {
      track.stop()
    } catch {
      // 麦克风轨道释放失败不能阻塞组件销毁和下一次录音。
    }
  }
}

function cancelActiveTranscription(): void {
  const call = activeTranscriptionCall
  activeTranscriptionCall = null
  if (!call) return
  try {
    // Wails binding 返回可取消 promise；生命周期失效时同步撤销上传，避免旧宠物音频继续占用请求槽。
    void call.cancel().catch(() => undefined)
  } catch {
    // 已结束或 runtime 已关闭时取消可能抛错，但不能阻塞组件切换和销毁。
  }
}

function clearActiveRecorderStopTimer(): void {
  if (activeRecorderStopTimer === undefined) return
  window.clearTimeout(activeRecorderStopTimer)
  activeRecorderStopTimer = undefined
}

function voiceRecordingLimitMessage(reason: 'duration' | 'size'): string {
  return reason === 'duration'
    ? t('pet.chat.voice.tooLong')
    : t('pet.chat.voice.tooLarge')
}

function stopRecorderForLimit(recorder: MediaRecorder, reason: 'duration' | 'size'): void {
  if (activeRecorder !== recorder || !activeRecorderShouldSubmit) return
  activeRecorderShouldSubmit = false
  activeRecorderStopReason = reason
  stoppingRecorder = recorder
  isStoppingRecording.value = true
  try {
    if (recorder.state !== 'inactive') recorder.stop()
  } catch {
    // 设备可能在限制触发时已经断开；丢弃当前捕获并释放轨道，不能上传半段数据。
    voiceInputMessage.value = voiceRecordingLimitMessage(reason)
    discardVoiceCapture()
  }
}

function discardVoiceCapture(): void {
  // generation 让 getUserMedia/MediaRecorder 的晚到回调失去提交资格，
  // 这是切换宠物、卸载组件时避免误发上一只宠物语音的核心边界。
  voiceSessionGeneration += 1
  cancelActiveTranscription()
  activeRecorderShouldSubmit = false
  const recorder = activeRecorder
  const stream = activeRecorderStream
  const recorderAlreadyStopping = stoppingRecorder === recorder
  clearActiveRecorderStopTimer()
  stoppingRecorder = null
  activeRecorder = null
  activeRecorderStream = null
  activeRecorderChunks = []
  activeRecorderPetId = ''
  activeRecorderByteLength = 0
  activeRecorderStopReason = ''
  isStartingRecording.value = false
  isRecording.value = false
  isStoppingRecording.value = false
  isTranscribing.value = false
  // stop() 会异步派发 dataavailable/stop；切宠物或卸载紧跟在用户点击停止之后时，
  // stoppingRecorder 已经拥有停止权，不能再次调用 stop() 触发 InvalidStateError。
  if (recorder && !recorderAlreadyStopping && recorder.state !== 'inactive') {
    try {
      recorder.stop()
    } catch {
      // 某些浏览器在设备已断开时 stop 会抛错，轨道仍需继续释放。
    }
  }
  stopMediaStream(stream)
}

async function transcribeVoiceCapture(
  blob: Blob,
  mediaType: string,
  session: number,
  petId: string,
  provider: PetTranscriptionProvider
): Promise<void> {
  let transcriptionCall: ReturnType<typeof Call.ByName> | null = null
  try {
    if (blob.size > PET_CHAT_MAX_VOICE_BYTES) {
      if (session === voiceSessionGeneration && componentMounted && props.petId === petId) {
        voiceInputMessage.value = t('pet.chat.voice.tooLarge')
      }
      return
    }
    const data = bytesToBase64(new Uint8Array(await blob.arrayBuffer()))
    if (session !== voiceSessionGeneration || !componentMounted || props.petId !== petId) return

    const normalizedMediaType = mediaType || 'audio/webm'
    const request: PetTranscriptionRequest = {
      petId,
      provider: {
        platform: provider.platform,
        providerId: provider.providerId,
        model: provider.model,
        capability: 'transcription',
        autoFallback: false
      },
      data,
      mediaType: normalizedMediaType,
      fileName: audioFileNameForMediaType(normalizedMediaType)
    }
    transcriptionCall = Call.ByName(PET_AI_METHODS.transcribeAudio, request)
    activeTranscriptionCall = transcriptionCall
    const rawResult = await transcriptionCall
    if (activeTranscriptionCall === transcriptionCall) activeTranscriptionCall = null
    if (session !== voiceSessionGeneration || !componentMounted || props.petId !== petId) return
    const result = isRecord(rawResult) ? rawResult : {}
    const text = asString(result.text).trim()
    if (!text) {
      voiceInputMessage.value = t('pet.chat.voice.empty')
      return
    }
    voiceInputMessage.value = ''
    // 转写和文字发送必须共享同一条 sendMessage 路径，保证历史、图片和
    // 流式事件状态与手动输入完全一致，不额外造第二套聊天状态机。
    await sendMessage(text)
  } catch (error) {
    if (session === voiceSessionGeneration && componentMounted && props.petId === petId) {
      voiceInputMessage.value = voiceErrorMessage(error)
    }
  } finally {
    if (transcriptionCall && activeTranscriptionCall === transcriptionCall) activeTranscriptionCall = null
    if (session === voiceSessionGeneration) isTranscribing.value = false
  }
}

function audioFileNameForMediaType(mediaType: string): string {
  const baseType = mediaType.split(';', 1)[0].trim().toLowerCase()
  const extensionByType: Record<string, string> = {
    'audio/aac': 'aac',
    'audio/flac': 'flac',
    'audio/m4a': 'm4a',
    'audio/mp4': 'mp4',
    'audio/mpeg': 'mp3',
    'audio/ogg': 'ogg',
    'audio/wav': 'wav',
    'audio/webm': 'webm',
    'audio/x-m4a': 'm4a'
  }
  const fallbackExtension = baseType.startsWith('audio/')
    ? baseType.slice('audio/'.length).replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')
    : ''
  return `voice-input.${extensionByType[baseType] || fallbackExtension || 'webm'}`
}

async function toggleVoiceInput(): Promise<void> {
  if (isBusy.value || isStartingRecording.value || isStoppingRecording.value || isTranscribing.value) return
  if (activeRecorder) {
    const recorder = activeRecorder
    stoppingRecorder = recorder
    isStoppingRecording.value = true
    try {
      recorder.stop()
    } catch (error) {
      voiceInputMessage.value = voiceErrorMessage(error)
      discardVoiceCapture()
    }
    return
  }
  if (!chatAvailability.value.ready) {
    voiceInputMessage.value = t('pet.chat.voice.notConfigured')
    return
  }
  if (typeof navigator === 'undefined' || !navigator.mediaDevices?.getUserMedia || typeof MediaRecorder === 'undefined') {
    voiceInputMessage.value = t('pet.chat.voice.unsupported')
    return
  }

  const petId = props.petId
  const session = ++voiceSessionGeneration
  let stream: MediaStream | null = null
  isStartingRecording.value = true
  voiceInputMessage.value = ''
  try {
    const settings = await fetchAppSettings()
    const transcriptionProvider = {
      platform: settings.speech_provider_platform?.trim() ?? '',
      providerId: settings.speech_provider_id?.trim() ?? '',
      model: settings.speech_model_id.trim()
    }
    // 三个字段必须来自应用级语音识别选择；任一缺失都不能偷偷复用 PetAgent 的聊天或 TTS 引用。
    if (!transcriptionProvider.platform || !transcriptionProvider.providerId || !transcriptionProvider.model) {
      if (session === voiceSessionGeneration && componentMounted && props.petId === petId) {
        isStartingRecording.value = false
        voiceInputMessage.value = t('pet.chat.voice.notConfigured')
      }
      return
    }
    if (session !== voiceSessionGeneration || !componentMounted || props.petId !== petId) return

    stream = await navigator.mediaDevices.getUserMedia({ audio: true })
    if (session !== voiceSessionGeneration || !componentMounted || props.petId !== petId) {
      stopMediaStream(stream)
      if (session === voiceSessionGeneration) isStartingRecording.value = false
      return
    }

    const preferredMimeType = 'audio/webm;codecs=opus'
    const mimeType = typeof MediaRecorder.isTypeSupported === 'function' && MediaRecorder.isTypeSupported(preferredMimeType)
      ? preferredMimeType
      : 'audio/webm'
    let recorder: MediaRecorder
    try {
      recorder = new MediaRecorder(stream, { mimeType })
    } catch {
      // 某些浏览器只接受默认编码器；启动后仍以 recorder.mimeType 为准，
      // 这样上传媒体类型与文件名扩展名保持同一套事实来源。
      recorder = new MediaRecorder(stream)
    }

    activeRecorder = recorder
    activeRecorderStream = stream
    activeRecorderChunks = []
    activeRecorderPetId = petId
    activeRecorderMimeType = recorder.mimeType || mimeType || 'audio/webm'
    activeRecorderShouldSubmit = true
    activeRecorderByteLength = 0
    activeRecorderStopReason = ''
    recorder.ondataavailable = (event) => {
      if (activeRecorder !== recorder || !activeRecorderShouldSubmit || event.data.size <= 0) return
      if (activeRecorderByteLength + event.data.size > PET_CHAT_MAX_VOICE_BYTES) {
        stopRecorderForLimit(recorder, 'size')
        return
      }
      activeRecorderChunks.push(event.data)
      activeRecorderByteLength += event.data.size
    }
    recorder.onerror = () => {
      if (activeRecorder !== recorder) return
      voiceInputMessage.value = t('pet.chat.voice.failed')
      discardVoiceCapture()
    }
    recorder.onstop = () => {
      const isCurrentRecorder = activeRecorder === recorder
      const ownsStoppingState = stoppingRecorder === recorder
      const chunks = isCurrentRecorder ? activeRecorderChunks.slice() : []
      const stopReason = isCurrentRecorder ? activeRecorderStopReason : ''
      const shouldSubmit = isCurrentRecorder &&
        activeRecorderShouldSubmit &&
        session === voiceSessionGeneration &&
        activeRecorderPetId === petId &&
        componentMounted &&
        props.petId === petId
      const recordedMimeType = isCurrentRecorder ? activeRecorderMimeType : 'audio/webm'
      if (isCurrentRecorder) {
        activeRecorder = null
        activeRecorderStream = null
        activeRecorderChunks = []
        activeRecorderPetId = ''
        activeRecorderShouldSubmit = false
        activeRecorderByteLength = 0
        activeRecorderStopReason = ''
        clearActiveRecorderStopTimer()
      }
      if (ownsStoppingState) {
        stoppingRecorder = null
        isStoppingRecording.value = false
      }
      stopMediaStream(stream)
      // 旧 recorder 的晚到 onstop 只能释放自己的 stream，不能改写当前 recorder 的 UI 状态。
      if (isCurrentRecorder) isRecording.value = false
      if (!shouldSubmit) {
        if (stopReason) voiceInputMessage.value = voiceRecordingLimitMessage(stopReason)
        return
      }

      const blob = new Blob(chunks, { type: recordedMimeType })
      // 源项目同样丢弃极短录音；它们通常是误触或静音，不应触发一次网络请求。
      if (blob.size < 1000) {
        voiceInputMessage.value = t('pet.chat.voice.empty')
        return
      }
      isTranscribing.value = true
      void transcribeVoiceCapture(blob, recordedMimeType, session, petId, transcriptionProvider)
    }
    recorder.start()
    activeRecorderMimeType = recorder.mimeType || mimeType || 'audio/webm'
    activeRecorderStopTimer = window.setTimeout(() => {
      if (activeRecorder === recorder && activeRecorderShouldSubmit) stopRecorderForLimit(recorder, 'duration')
    }, PET_CHAT_MAX_VOICE_DURATION_MS)
    isStartingRecording.value = false
    isRecording.value = true
  } catch (error) {
    const shouldReport = session === voiceSessionGeneration && componentMounted && props.petId === petId
    if (activeRecorder) discardVoiceCapture()
    stopMediaStream(stream)
    if (session === voiceSessionGeneration) isStartingRecording.value = false
    if (shouldReport) voiceInputMessage.value = voiceErrorMessage(error)
  }
}

function isSupportedImageType(mediaType: string): boolean {
  return (PET_CHAT_IMAGE_TYPES as readonly string[]).includes(mediaType.toLowerCase())
}

async function readImage(file: File): Promise<PetChatImage | null> {
  const mediaType = file.type.trim().toLowerCase()
  if (!isSupportedImageType(mediaType)) {
    attachmentMessage.value = t('pet.chat.attachments.typeUnsupported')
    return null
  }
  if (file.size <= 0 || file.size > PET_CHAT_MAX_IMAGE_BYTES) {
    attachmentMessage.value = t('pet.chat.attachments.singleTooLarge')
    return null
  }
  try {
    const bytes = new Uint8Array(await file.arrayBuffer())
    if (bytes.length <= 0 || bytes.length > PET_CHAT_MAX_IMAGE_BYTES) {
      attachmentMessage.value = t('pet.chat.attachments.singleTooLarge')
      return null
    }
    const data = bytesToBase64(bytes)
    return {
      id: createImageId(),
      data,
      mediaType,
      previewUrl: `data:${mediaType};base64,${data}`
    }
  } catch {
    attachmentMessage.value = t('pet.chat.attachments.readFailed')
    return null
  }
}

function openImagePicker(): void {
  if (isComposerBusy.value) return
  attachmentMessage.value = ''
  imageInputRef.value?.click()
}

async function handleImageSelection(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  input.value = ''
  if (files.length === 0 || isComposerBusy.value) return

  const generation = ++imageSelectionGeneration
  attachmentMessage.value = ''
  const remaining = PET_CHAT_MAX_IMAGES - pendingImages.value.length
  if (remaining <= 0) {
    attachmentMessage.value = t('pet.chat.attachments.maxImages')
    return
  }
  if (files.length > remaining) {
    attachmentMessage.value = t('pet.chat.attachments.maxImages')
    return
  }

  const additions: PetChatImage[] = []
  let totalBytes = pendingImages.value.reduce((total, image) => total + Math.floor(image.data.length * 3 / 4), 0)
  for (const file of files) {
    const image = await readImage(file)
    if (!image || generation !== imageSelectionGeneration) return
    const imageBytes = Math.floor(image.data.length * 3 / 4)
    if (totalBytes > PET_CHAT_MAX_TOTAL_IMAGE_BYTES - imageBytes) {
      attachmentMessage.value = t('pet.chat.attachments.totalTooLarge')
      return
    }
    totalBytes += imageBytes
    additions.push(image)
  }
  if (generation !== imageSelectionGeneration) return
  pendingImages.value = [...pendingImages.value, ...additions]
}

function removePendingImage(imageId: string): void {
  if (isComposerBusy.value) return
  pendingImages.value = pendingImages.value.filter((image) => image.id !== imageId)
  attachmentMessage.value = ''
}

function isCurrentEvent(event: ChatEvent): boolean {
  if (!activeRequestId || event.requestId !== activeRequestId) return false
  if (event.petId && event.petId !== props.petId) return false
  // sequence 是后端为并发/取消竞态提供的单调序号；旧事件必须在 UI 层再次丢弃。
  if (event.sequence > 0 && event.sequence <= lastEventSequence) return false
  if (event.sequence > 0) lastEventSequence = event.sequence
  return true
}

function clearRequestWatchdog(): void {
  if (activeRequestWatchdog === undefined) return
  window.clearTimeout(activeRequestWatchdog)
  activeRequestWatchdog = undefined
}

function cancelBackendRequest(requestId: string): void {
  if (!requestId) return
  // 取消是幂等的；组件切换/卸载时不再等待 IPC，避免旧页面生命周期被挂住。
  void Call.ByName(PET_AI_METHODS.cancelChat, requestId).catch(() => undefined)
}

function armRequestWatchdog(requestId: string, userText: string): void {
  clearRequestWatchdog()
  activeRequestWatchdog = window.setTimeout(() => {
    activeRequestWatchdog = undefined
    if (!componentMounted || activeRequestId !== requestId) return
    // 先撤销后端请求，再丢掉前端 ownership；晚到的 SSE/Wails 事件会因 requestId
    // 不再匹配而被丢弃，超时错误也不会被后续 cancelled 事件覆盖。
    cancelBackendRequest(requestId)
    settleFailure(requestId, userText, 'PET_AI_TIMEOUT')
  }, PET_CHAT_REQUEST_WATCHDOG_MS)
}

function settleRequest(): void {
  clearRequestWatchdog()
  activeRequestId = ''
  activeRawAssistantText = ''
  lastEventSequence = 0
}

function safeErrorMessage(errorCode: string): string {
  const messageKeys: Record<string, string> = {
    PET_AI_INVALID_REQUEST: 'pet.chat.errors.invalidRequest',
    PET_AI_REQUEST_CANCELLED: 'pet.chat.errors.cancelled',
    PET_AI_TIMEOUT: 'pet.chat.errors.timeout',
    PET_AI_REQUEST_IN_FLIGHT: 'pet.chat.errors.inFlight',
    PET_AI_REQUEST_TOO_LARGE: 'pet.chat.errors.requestTooLarge',
    PET_AI_RESPONSE_TOO_LARGE: 'pet.chat.errors.responseTooLarge',
    PET_AI_RESPONSE_INVALID: 'pet.chat.errors.responseInvalid',
    PET_AI_SSE_INVALID: 'pet.chat.errors.sseInvalid',
    PET_AI_EVENT_ERROR: 'pet.chat.errors.eventError',
    // PET_AI_UPSTREAM_ERROR 的稳定值历史上是 PET_UPSTREAM_ERROR；两种写法
    // 都保留，兼容 Wails 直接返回常量名或错误值的不同版本。
    PET_UPSTREAM_ERROR: 'pet.chat.errors.upstream',
    PET_AI_UPSTREAM_ERROR: 'pet.chat.errors.upstream',
    PET_PROVIDER_NOT_FOUND: 'pet.chat.errors.providerNotFound',
    PET_MODEL_NOT_CONFIGURED: 'pet.chat.errors.modelNotConfigured',
    PET_MODEL_UNSUPPORTED: 'pet.chat.errors.modelUnsupported',
    PET_REFERENCE_INVALID: 'pet.chat.errors.referenceInvalid',
    PET_CAPABILITY_UNSUPPORTED: 'pet.chat.errors.capabilityUnsupported',
    PET_CHAT_NOT_CONFIGURED: 'pet.chat.errors.notConfigured',
    PET_AI_WORKSPACE_UNAVAILABLE: 'pet.chat.errors.workspaceUnavailable',
    PET_AI_DEPENDENCY_UNAVAILABLE: 'pet.chat.errors.dependencyUnavailable'
  }
  return messageKeys[errorCode] ? t(messageKeys[errorCode]) : t('pet.chat.errors.generic')
}

function schedulerErrorMessage(error: unknown): string {
  if (error instanceof Error) {
    // Wails 有些版本只把结构化错误的 JSON 放入 Error.message；先尝试还原
    // code/message，再让通用格式化器处理普通网络或 runtime 错误。
    try {
      const parsed: unknown = JSON.parse(error.message)
      if (isRecord(parsed)) return formatPlanError(parsed)
    } catch {
      // 普通 Error 文案直接走下方分支。
    }
  }
  return formatPlanError(error)
}

async function loadPlans(showLoading = true): Promise<void> {
  if (plansLoadInFlight) {
    plansReloadRequested = true
    return
  }

  plansLoadInFlight = true
  if (showLoading || plansPhase.value === 'idle') plansPhase.value = 'loading'
  plansError.value = ''
  try {
    // scheduler API 没有 ListPlans；计划记录由 PetSchedulerAPI 写入 pet_plans，
    // 现有稳定读取边界是 PetService.GetSnapshot -> PetApi.getSnapshot。
    const snapshot = await petApi.getSnapshot(props.petId)
    plans.value = snapshot.plans
    plansPhase.value = 'ready'
  } catch (error) {
    plansPhase.value = 'error'
    plansError.value = schedulerErrorMessage(error)
  } finally {
    plansLoadInFlight = false
    if (plansReloadRequested) {
      plansReloadRequested = false
      void loadPlans(false)
    }
  }
}

function isPlanCancelled(planId: string): boolean {
  return cancelledPlanIds.value.has(planId)
}

function showPlanError(error: unknown): void {
  const message = schedulerErrorMessage(error)
  plansPhase.value = 'error'
  plansError.value = message
  planNotice.value = message
}

async function schedulePetPlan(plan: PetPlanScript): Promise<void> {
  planNotice.value = ''
  try {
    // Wails 方法签名对应 PetSchedulerSchedulePlanInput：plan 是结构化 JSON，
    // 不要 stringify 成 IPC 字符串，也不要传入源项目的 cron payload。
    const rawResult = await Call.ByName(PET_SCHEDULER_METHODS.schedulePlan, {
      plan,
      timeZone: localPetTimeZone()
    })
    const result = isRecord(rawResult) ? (rawResult as PetSchedulerSchedulePlanResult) : {}
    if (!result.planId || result.jobsEnqueued === false) {
      throw new Error(t('pet.chat.plan.enqueueUnconfirmed'))
    }
    planNotice.value = t('pet.chat.plan.noticeQueued', { title: plan.title || t('pet.chat.plan.defaultTitle') })
    await loadPlans(false)
  } catch (error) {
    showPlanError(error)
  }
}

async function cancelPetPlan(plan: PetPlanRecord): Promise<void> {
  if (!plan.planId || planBusyId.value || isPlanCancelled(plan.planId)) return
  planBusyId.value = plan.planId
  planNotice.value = ''
  try {
    // Cancel 只接受二选一 target；这里按计划维度取消全部尚未完成的 job。
    const rawResult = await Call.ByName(PET_SCHEDULER_METHODS.cancel, { planId: plan.planId })
    const result = isRecord(rawResult) ? rawResult : {}
    if (result.cancelled !== true) {
      throw new Error(t('pet.chat.plan.cancelUnavailable'))
    }
    cancelledPlanIds.value = new Set(cancelledPlanIds.value).add(plan.planId)
    planNotice.value = t('pet.chat.plan.noticeCancelled', { title: plan.title || t('pet.chat.plan.defaultTitle') })
    await loadPlans(false)
  } catch (error) {
    showPlanError(error)
  } finally {
    planBusyId.value = ''
  }
}

function isMissingBindingError(error: unknown): boolean {
  const text = error instanceof Error ? error.message : String(error)
  return /unknown method|method not found|service not found|binding|not registered|does not exist/i.test(text)
}

function settleFailure(requestId: string, userText: string, errorCode = ''): void {
  if (activeRequestId !== requestId) return
  lastFailedText = userText
  failureMessage.value = safeErrorMessage(errorCode)
  settleRequest()
  phase.value = 'error'
  emit('status-change', { text: failureMessage.value, tone: 'error' })
}

function handleChatEvent(value: unknown): void {
  const event = normalizeChatEvent(value)
  if (!event || !isCurrentEvent(event)) return

  // 不把“收到事件”误当成完成；只续期空闲保护，最终状态仍由 completed/failed/cancelled
  // 事件决定。这样 tool call 期间没有可见文本时，Codex 仍能继续工作。
  armRequestWatchdog(activeRequestId, lastFailedText)

  if (event.type === 'started') {
    phase.value = 'streaming'
    emit('status-change', { text: t('pet.chat.status.connecting'), tone: 'muted' })
    emit('bubble', { text: t('pet.chat.status.connecting'), tone: 'muted', duration: 60_000 })
    return
  }
  if (event.type === 'progress') {
    phase.value = 'streaming'
    return
  }
  if (event.type === 'delta') {
    // 先保留原始流，再按完整前缀清洗；未闭合协议从起点开始整段隐藏，
    // 否则 JSON 会随着 delta 碎片短暂闪现在气泡里。
    activeRawAssistantText += event.delta
    const visibleText = cleanPetAssistantText(activeRawAssistantText)
    phase.value = 'streaming'
    emit('status-change', { text: t('pet.chat.status.replying'), tone: 'muted' })
    emit('bubble', {
      text: visibleText || t('pet.chat.status.replying'),
      tone: 'muted',
      duration: visibleText ? undefined : 60_000
    })
    return
  }
  if (event.type === 'completed') {
    // completed 事件携带全文；没有全文时退回已累积的原始 delta。
    const rawReply = event.text || activeRawAssistantText
    if (event.text) activeRawAssistantText = event.text
    const extractedPlan = extractPetPlan(rawReply)
    const visibleText = cleanPetAssistantText(rawReply) || t('pet.chat.message.noText')
    settleRequest()
    phase.value = 'idle'
    failureMessage.value = ''
    emit('status-change', { text: '', tone: 'muted' })
    emit('bubble', { text: visibleText, tone: 'muted' })
    if (extractedPlan.error) {
      showPlanError(extractedPlan.error)
    } else if (extractedPlan.plan) {
      // 计划失败不能回滚已经完成的回复；只在计划面板显示错误，保证
      // malformed JSON 或调度服务故障不会把流式聊天状态打成失败。
      void schedulePetPlan(extractedPlan.plan)
    }
    return
  }
  if (event.type === 'cancelled') {
    settleRequest()
    phase.value = 'idle'
    emit('status-change', { text: '', tone: 'muted' })
    return
  }

  settleFailure(activeRequestId, lastFailedText, event.errorCode)
}

async function sendMessage(textOverride?: string, imagesOverride?: PetChatImage[]): Promise<void> {
  const text = (textOverride ?? inputText.value).trim()
  const images = cloneImages(imagesOverride ?? pendingImages.value)
  if ((!text && images.length === 0) || isBusy.value) return
  if (!chatAvailability.value.ready) {
    phase.value = 'unavailable'
    return
  }

  const requestId = createRequestId()
  inputText.value = ''
  pendingImages.value = []
  attachmentMessage.value = ''
  voiceInputMessage.value = ''
  failureMessage.value = ''
  lastFailedText = text
  lastFailedImages = cloneImages(images)
  activeRequestId = requestId
  activeRawAssistantText = ''
  lastEventSequence = 0
  armRequestWatchdog(requestId, text)
  phase.value = 'starting'
  emit('status-change', { text: t('pet.chat.status.connecting'), tone: 'muted' })
  emit('bubble', { text: t('pet.chat.status.connecting'), tone: 'muted', duration: 60_000 })

  try {
    const request = buildChatRequest(requestId, text, images)
    const result = await Call.ByName(PET_AI_METHODS.startChat, request)
    if (activeRequestId !== requestId) return
    if (isRecord(result) && result.requestId && result.requestId !== requestId) {
      settleFailure(requestId, text, 'PET_REFERENCE_INVALID')
      return
    }
    // StartChat 只代表后端接受请求，真正的完成边界由事件决定。
    phase.value = 'streaming'
  } catch (error) {
    if (activeRequestId !== requestId) return
    // Wails 可能把 Go 的结构化错误包装成 Error.message；优先保留后端
    // 错误码，避免 workspace/provider 等可定位故障被吞成统一的“回复失败”。
    const errorCode = petErrorCode(error)
    settleFailure(
      requestId,
      text,
      errorCode || (isMissingBindingError(error) ? 'PET_AI_DEPENDENCY_UNAVAILABLE' : '')
    )
  }
}

async function cancelMessage(): Promise<void> {
  const requestId = activeRequestId
  if (!requestId || !isBusy.value) return

  phase.value = 'cancelling'
  clearRequestWatchdog()
  // 先让 UI 失去这条 request 的所有权，再请求后端取消；这样晚到的 delta
  // 即便已经排进 runtime 事件队列，也无法污染下一次会话。
  activeRequestId = ''
  activeRawAssistantText = ''
  lastEventSequence = 0

  try {
    await Call.ByName(PET_AI_METHODS.cancelChat, requestId)
    phase.value = 'idle'
  } catch (error) {
    failureMessage.value = isMissingBindingError(error)
      ? t('pet.chat.cancel.apiUnavailable')
      : t('pet.chat.cancel.failed')
    phase.value = 'error'
  }
}

function retryLastMessage(): void {
  if ((!lastFailedText && lastFailedImages.length === 0) || isBusy.value || !chatAvailability.value.ready) return
  const text = lastFailedText
  const images = cloneImages(lastFailedImages)
  lastFailedText = ''
  lastFailedImages = []
  void sendMessage(text, images)
}

function handleInputKeydown(event: KeyboardEvent): void {
  if (event.isComposing || event.key !== 'Enter' || event.shiftKey) return
  event.preventDefault()
  void sendMessage()
}

watch(
  () => props.petId,
  () => {
    const requestId = activeRequestId
    // 宠物切换时必须清空当前请求状态，避免旧请求的事件污染下一只宠物。
    imageSelectionGeneration += 1
    discardVoiceCapture()
    isTranscribing.value = false
    voiceInputMessage.value = ''
    clearRequestWatchdog()
    activeRequestId = ''
    lastEventSequence = 0
    inputText.value = ''
    pendingImages.value = []
    attachmentMessage.value = ''
    failureMessage.value = ''
    lastFailedText = ''
    lastFailedImages = []
    plans.value = []
    plansPhase.value = 'idle'
    plansError.value = ''
    planNotice.value = ''
    cancelledPlanIds.value = new Set()
    phase.value = 'idle'
    cancelBackendRequest(requestId)
    void loadPlans()
  }
)

onMounted(() => {
  componentMounted = true
  void loadPlans()
  stopChatEvent = Events.On(PET_AI_EVENT, (event) => {
    handleChatEvent(event.data)
  })
})

onBeforeUnmount(() => {
  componentMounted = false
  imageSelectionGeneration += 1
  discardVoiceCapture()
  isTranscribing.value = false
  stopChatEvent?.()
  stopChatEvent = null
  const requestId = activeRequestId
  clearRequestWatchdog()
  activeRequestId = ''
  lastEventSequence = 0
  if (requestId) {
    // 组件销毁时只发取消信号，不等待结果，也不把 runtime 错误写入新页面状态。
    cancelBackendRequest(requestId)
  }
  pendingImages.value = []
  lastFailedImages = []
})
</script>

<template>
  <section class="pet-chat" :class="{ 'is-busy': isBusy }" :aria-label="t('pet.chat.aria.chatPanel')">
    <div
      v-if="attachmentMessage || voiceInputMessage || failureMessage || (phase === 'unavailable' && !chatAvailability.ready)"
      class="pet-chat__status"
      :class="'is-' + statusTone"
      aria-live="polite"
    >
      <span class="pet-chat__status-dot" aria-hidden="true"></span>
      <span>{{ statusText }}</span>
      <button
        v-if="phase === 'error' && (lastFailedText || lastFailedImages.length > 0) && chatAvailability.ready"
        type="button"
        class="pet-chat__retry"
        @click="retryLastMessage"
      >
        {{ t('pet.common.retry') }}
      </button>
    </div>

    <form class="pet-chat__composer" @submit.prevent="sendMessage()">
      <input
        ref="imageInputRef"
        class="pet-chat__file-input"
        type="file"
        accept="image/png,image/jpeg,image/gif,image/webp"
        multiple
        :aria-label="t('pet.chat.attachments.selectImage')"
        @change="handleImageSelection"
      />
      <div v-if="pendingImages.length > 0" class="pet-chat__attachment-strip" :aria-label="t('pet.chat.attachments.pendingImages')">
        <div v-for="image in pendingImages" :key="image.id" class="pet-chat__attachment">
          <img :src="image.previewUrl" :alt="t('pet.chat.attachments.pendingImageAlt')" />
          <button
            type="button"
            class="pet-chat__attachment-remove"
            :disabled="isComposerBusy"
            :aria-label="t('pet.chat.attachments.removeImage', { id: image.id })"
            :title="t('pet.chat.attachments.removeImageTitle')"
            @click="removePendingImage(image.id)"
          >
            ×
          </button>
        </div>
      </div>
      <div class="pet-chat__composer-row">
        <button
          type="button"
          class="pet-chat__attach"
          :disabled="isComposerBusy || pendingImages.length >= PET_CHAT_MAX_IMAGES"
          :aria-label="t('pet.chat.attachments.addImage')"
          :title="t('pet.chat.attachments.addImage')"
          @click="openImagePicker"
        >
          <ImagePlus class="pet-chat__control-icon" :size="15" :stroke-width="1.8" aria-hidden="true" />
        </button>
        <button
          type="button"
          class="pet-chat__voice"
          :class="{ 'is-recording': isRecording }"
          :disabled="isBusy || isStartingRecording || isStoppingRecording || isTranscribing || !chatAvailability.ready"
          :aria-label="isRecording ? t('pet.chat.voice.stop') : t('pet.chat.voice.input')"
          :title="isRecording ? t('pet.chat.voice.stop') : t('pet.chat.voice.input')"
          @click="toggleVoiceInput"
        >
          <Loader2 v-if="isStartingRecording || isTranscribing" class="pet-chat__control-icon is-spinning" :size="15" :stroke-width="1.8" aria-hidden="true" />
          <Square v-else-if="isRecording" class="pet-chat__control-icon is-pulsing" :size="14" :stroke-width="1.8" aria-hidden="true" />
          <Mic v-else class="pet-chat__control-icon" :size="15" :stroke-width="1.8" aria-hidden="true" />
        </button>
        <textarea
          v-model="inputText"
          rows="1"
          maxlength="16000"
          :disabled="isComposerBusy"
          :placeholder="t('pet.chat.composer.placeholder')"
          :aria-label="t('pet.chat.aria.input')"
          @keydown="handleInputKeydown"
        ></textarea>
        <button
          v-if="isBusy"
          type="button"
          class="pet-chat__send pet-chat__send--cancel"
          :disabled="phase === 'cancelling'"
          @click="cancelMessage"
        >
          <Loader2 v-if="phase === 'cancelling'" class="pet-chat__control-icon is-spinning" :size="15" :stroke-width="1.8" aria-hidden="true" />
          <Square v-else class="pet-chat__control-icon" :size="14" :stroke-width="1.8" aria-hidden="true" />
        </button>
        <button v-else type="submit" class="pet-chat__send" :disabled="!canSend || isComposerBusy">
          <SendHorizontal class="pet-chat__control-icon" :size="15" :stroke-width="1.8" aria-hidden="true" />
        </button>
        <button
          type="button"
          class="pet-chat__close"
          :aria-label="t('update.close')"
          :title="t('update.close')"
          @click="emit('close')"
        >
          <X class="pet-chat__control-icon" :size="15" :stroke-width="1.8" aria-hidden="true" />
        </button>
      </div>
    </form>
  </section>
</template>

<style scoped>
.pet-chat {
  --pet-chat-ink: var(--pet-ink, var(--mac-text, #1d1d1f));
  --pet-chat-muted: var(--pet-muted, var(--mac-text-secondary, #6e6e73));
  --pet-chat-line: var(--pet-line, var(--mac-border, rgba(15, 23, 42, 0.12)));
  --pet-chat-surface: var(--pet-surface, rgba(255, 255, 255, 0.78));
  display: block;
  width: 100%;
  max-width: 292px;
  min-width: 0;
  box-sizing: border-box;
  overflow: hidden;
  border: 1px solid var(--pet-chat-line);
  border-radius: 12px;
  background: var(--mac-surface, #ffffff);
  color: var(--pet-chat-ink);
  box-shadow: 0 8px 22px color-mix(in srgb, #243247 10%, transparent);
  font-family: var(--mac-font, system-ui, sans-serif);
}

.pet-chat__status {
  display: flex;
  min-width: 0;
  min-height: 26px;
  align-items: center;
  gap: 5px;
  box-sizing: border-box;
  padding: 0 8px;
  border-bottom: 1px solid var(--pet-chat-line);
  color: var(--pet-chat-muted);
  font-size: 10px;
  line-height: 15px;
}

.pet-chat__status > span:nth-child(2) {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-chat__status-dot {
  width: 6px;
  height: 6px;
  flex: 0 0 6px;
  border-radius: 50%;
  background: var(--pet-chat-muted);
}

.pet-chat__status.is-ready .pet-chat__status-dot {
  background: #32b36b;
}

.pet-chat__status.is-active .pet-chat__status-dot {
  background: var(--mac-accent, #0a84ff);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--mac-accent, #0a84ff) 14%, transparent);
}

.pet-chat__status.is-error,
.pet-chat__status.is-error .pet-chat__status-dot {
  color: #bd4f4f;
}

.pet-chat__status.is-error .pet-chat__status-dot {
  background: #bd4f4f;
}

.pet-chat__retry {
  flex: 0 0 auto;
  margin-left: auto;
  border: 0;
  border-radius: 6px;
  padding: 3px 7px;
  background: color-mix(in srgb, #bd4f4f 12%, transparent);
  color: #bd4f4f;
  cursor: pointer;
  font: inherit;
  font-size: 10px;
}

.pet-chat__retry:disabled {
  cursor: wait;
  opacity: 0.45;
}

.pet-chat__composer {
  display: flex;
  width: 100%;
  min-width: 0;
  flex-direction: column;
  box-sizing: border-box;
  gap: 6px;
  padding: 10px;
}

.pet-chat__composer-row {
  display: flex;
  width: 100%;
  min-width: 0;
  align-items: center;
  gap: 6px;
}

.pet-chat__file-input {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0 0 0 0);
  white-space: nowrap;
}

.pet-chat__attachment-strip {
  display: flex;
  width: 100%;
  min-width: 0;
  max-width: 100%;
  gap: 6px;
  overflow-x: auto;
  padding: 0 0 1px;
  scrollbar-width: thin;
}

.pet-chat__attachment {
  position: relative;
  width: 34px;
  height: 34px;
  flex: 0 0 34px;
  overflow: hidden;
  box-sizing: border-box;
  border: 1px solid var(--pet-chat-line);
  border-radius: 7px;
  background: color-mix(in srgb, var(--pet-chat-muted) 8%, transparent);
}

.pet-chat__attachment img {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.pet-chat__attachment-remove {
  position: absolute;
  top: 2px;
  right: 2px;
  display: grid;
  width: 16px;
  height: 16px;
  place-items: center;
  border: 0;
  border-radius: 50%;
  margin: 0;
  padding: 0;
  background: rgba(0, 0, 0, 0.66);
  color: #fff;
  cursor: pointer;
  font: inherit;
  font-size: 12px;
  line-height: 1;
}

.pet-chat__attachment-remove:disabled {
  cursor: wait;
  opacity: 0.5;
}

/* Wails 模板的 public/style.css 给所有 button 注入左侧外边距；桌宠 composer 必须在本地恢复固定控件的盒模型。 */
.pet-chat__voice,
.pet-chat__attach,
.pet-chat__send,
.pet-chat__close {
  display: inline-flex;
  width: 32px;
  height: 32px;
  min-width: 32px;
  min-height: 0;
  flex: 0 0 32px;
  align-items: center;
  justify-content: center;
  box-sizing: border-box;
  border-radius: 8px;
  margin: 0;
  padding: 0;
  font: inherit;
  line-height: 1;
}

.pet-chat__attach {
  border: 1px solid var(--pet-chat-line);
  background: transparent;
  color: var(--pet-chat-muted);
  cursor: pointer;
}

.pet-chat__voice {
  border: 1px solid var(--pet-chat-line);
  background: transparent;
  color: var(--pet-chat-muted);
  cursor: pointer;
}

.pet-chat__voice.is-recording {
  border-color: color-mix(in srgb, #bd4f4f 62%, var(--pet-chat-line));
  background: color-mix(in srgb, #bd4f4f 12%, transparent);
  color: #bd4f4f;
}

.pet-chat__voice:hover:not(:disabled),
.pet-chat__attach:hover:not(:disabled),
.pet-chat__close:hover,
.pet-chat__close:focus-visible {
  border-color: color-mix(in srgb, var(--mac-accent, #0a84ff) 50%, var(--pet-chat-line));
  color: var(--pet-chat-ink);
}

.pet-chat__voice:disabled,
.pet-chat__attach:disabled,
.pet-chat__send:disabled {
  cursor: not-allowed;
  opacity: 0.42;
}

.pet-chat__composer textarea {
  display: block;
  width: auto;
  height: 32px;
  flex: 1 1 auto;
  min-width: 0;
  min-height: 32px;
  max-height: 32px;
  box-sizing: border-box;
  resize: none;
  overflow-x: auto;
  overflow-y: hidden;
  border: 1px solid var(--pet-chat-line);
  border-radius: 8px;
  padding: 7px 8px;
  outline: none;
  background: color-mix(in srgb, var(--mac-surface, #fff) 52%, transparent);
  color: var(--pet-chat-ink);
  cursor: text;
  font: inherit;
  font-size: 11px;
  line-height: 16px;
  user-select: text;
  white-space: nowrap;
}

.pet-chat__composer textarea::placeholder {
  color: color-mix(in srgb, var(--pet-chat-muted) 78%, transparent);
}

.pet-chat__composer textarea:focus {
  border-color: color-mix(in srgb, var(--mac-accent, #0a84ff) 52%, var(--pet-chat-line));
}

.pet-chat__composer textarea:disabled {
  cursor: wait;
  opacity: 0.72;
}

.pet-chat__send {
  border: 0;
  background: var(--mac-accent, #0a84ff);
  color: #fff;
  cursor: pointer;
  font-size: 0;
  transition: opacity 0.18s ease, transform 0.18s ease;
}

.pet-chat__send:hover:not(:disabled) {
  transform: translateY(-1px);
}

.pet-chat__send--cancel {
  background: #bd4f4f;
}

.pet-chat__close {
  border: 0;
  background: transparent;
  color: var(--pet-chat-muted);
  cursor: pointer;
}

.pet-chat__close:hover,
.pet-chat__close:focus-visible {
  background: color-mix(in srgb, var(--pet-chat-muted) 10%, transparent);
  color: var(--pet-chat-ink);
  outline: none;
}

.pet-chat__control-icon {
  flex: 0 0 auto;
}

.pet-chat__control-icon.is-spinning {
  animation: pet-chat-spin 0.9s linear infinite;
}

.pet-chat__control-icon.is-pulsing {
  animation: pet-chat-blink 1s steps(2, end) infinite;
}

@keyframes pet-chat-spin {
  to {
    transform: rotate(360deg);
  }
}

@keyframes pet-chat-blink {
  50% {
    opacity: 0.35;
  }
}

@media (prefers-reduced-motion: reduce) {
  .pet-chat__control-icon.is-spinning,
  .pet-chat__control-icon.is-pulsing {
    animation: none;
  }

  .pet-chat__send {
    transition: none;
  }
}
</style>
