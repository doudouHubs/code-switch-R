<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Call, Events } from '../../wails-runtime-compat'
import { fetchAppSettings } from '../../services/appSettings'
import { petApi } from './petApi'
import {
  buildPetPlanInstructions,
  cleanPetAssistantText,
  extractPetPlan,
  formatPlanDate,
  formatPlanError,
  formatPlanStep,
  localPetTimeZone
} from './petPlan'
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
  /** 兼容宿主显式传入平台；正常路径优先使用快照里的 agent.providerPlatform。 */
  providerPlatform?: string
}

const props = withDefaults(defineProps<PetChatProps>(), {
  petId: 'default',
  petName: 'Kapi',
  agent: null,
  dreams: () => [],
  providerPlatform: ''
})
const { t, locale } = useI18n()

type ChatRole = 'user' | 'assistant'
type ChatMessageStatus = 'complete' | 'streaming' | 'cancelled' | 'error'
type ChatPhase = 'idle' | 'starting' | 'streaming' | 'cancelling' | 'error' | 'unavailable'
type PlanPhase = 'idle' | 'loading' | 'ready' | 'error'
type NormalizedEventType = 'started' | 'delta' | 'completed' | 'failed' | 'cancelled'

interface ChatMessage {
  id: string
  role: ChatRole
  content: string
  images: PetChatImage[]
  createdAt: number
  status: ChatMessageStatus
}

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
  provider: {
    platform: string
    providerId: string
    model: string
    capability: 'chat'
    autoFallback: boolean
  }
  persona: string
  userText: string
  images: PetChatImagePayload[]
  history: Array<{ role: ChatRole; content: string; images?: PetChatImagePayload[] }>
  projectFolder: string | null
  reasoning?: string
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

const messages = ref<ChatMessage[]>([])
const inputText = ref('')
const phase = ref<ChatPhase>('idle')
const failureMessage = ref('')
const showDreams = ref(false)
const showPlans = ref(false)
const selectedDreamId = ref('')
const messageListRef = ref<HTMLElement | null>(null)
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

const configuredProviderPlatform = computed(() =>
  props.agent?.providerPlatform?.trim() || props.providerPlatform.trim()
)

let activeRequestId = ''
let activeAssistantId = ''
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
let stoppingRecorder: MediaRecorder | null = null
let activeTranscriptionCall: ReturnType<typeof Call.ByName> | null = null
let componentMounted = false

const chatAvailability = computed<ChatAvailability>(() => {
  if (!props.agent) {
    return { ready: false, message: t('pet.chat.availability.loading') }
  }
  if (!props.agent.providerId || !props.agent.modelId) {
    return { ready: false, message: t('pet.chat.availability.bindingRequired') }
  }
  if (!configuredProviderPlatform.value) {
    return { ready: false, message: t('pet.chat.availability.platformRequired') }
  }
  return { ready: true, message: '' }
})

const petName = computed(() => props.petName?.trim() || 'Kapi')

const isBusy = computed(() =>
  phase.value === 'starting' || phase.value === 'streaming' || phase.value === 'cancelling'
)

const isComposerBusy = computed(() =>
  isBusy.value || isStartingRecording.value || isRecording.value || isStoppingRecording.value || isTranscribing.value
)

const canSend = computed(() =>
  chatAvailability.value.ready &&
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

const selectedDream = computed(() => {
  if (!selectedDreamId.value) return null
  return props.dreams.find((dream) => dream.id === selectedDreamId.value) ?? null
})

const dreamCountLabel = computed(() => {
  const count = props.dreams.length
  return count > 99 ? '99+' : String(count)
})

const planCountLabel = computed(() => {
  const count = plans.value.length
  return count > 99 ? '99+' : String(count)
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
  if (isRecord(value)) return value
  if (typeof value !== 'string' || !value.trim()) return {}
  try {
    const parsed: unknown = JSON.parse(value)
    return isRecord(parsed) ? parsed : { text: typeof parsed === 'string' ? parsed : '' }
  } catch {
    return { text: value }
  }
}

function normalizeChatEvent(value: unknown): ChatEvent | null {
  const source = isRecord(value) ? value : decodeEventData(value)
  const data = decodeEventData(source.data)
  const typeValue = asString(source.type || data.type)
  const typeMap: Record<string, NormalizedEventType | undefined> = {
    start: 'started',
    started: 'started',
    delta: 'delta',
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

function createMessage(
  role: ChatRole,
  content: string,
  status: ChatMessageStatus,
  images: PetChatImage[] = []
): ChatMessage {
  return {
    id: role + '-' + Date.now() + '-' + Math.random().toString(36).slice(2, 8),
    role,
    content,
    images: cloneImages(images),
    createdAt: Date.now(),
    status
  }
}

function buildHistory(excludeMessageId = ''): Array<{ role: ChatRole; content: string; images?: PetChatImagePayload[] }> {
  return messages.value
    .filter((message) =>
      message.id !== excludeMessageId &&
      (message.content.trim() || message.images.length > 0) &&
      message.status === 'complete'
    )
    .slice(-24)
    .map((message) => ({
      role: message.role,
      content: message.content,
      ...(message.images.length > 0
        ? { images: message.images.map(({ data, mediaType }) => ({ data, mediaType })) }
        : {})
    }))
}

function buildPersona(): string {
  const configured = props.agent?.systemPrompt.trim() ?? ''
  const base = configured || '你是' + petName.value + '，一个简短、友善、会记得当前对话的桌面宠物。'
  // 计划标签是模型与 UI 之间的隐藏协议；只传用户自定义 persona 会让模型
  // 不知道何时生成协议，也会把“稍后提醒”退化成普通文字回答。
  return `${base}\n\n${buildPetPlanInstructions()}`
}

function resolveProjectFolder(): string | null {
  const projectFolder = props.agent?.projectFolder
  if (typeof projectFolder !== 'string') return null

  const normalized = projectFolder.trim()
  // 目录只能来自后端 agent 快照；当前项目没有跨页面的选中项目状态，
  // 因此无法安全取得时必须显式传 null，不能用 projectName、项目列表或用户文本猜路径。
  return normalized || null
}

function buildChatRequest(
  requestId: string,
  userText: string,
  images: PetChatImage[],
  excludeMessageId = ''
): PetChatRequest {
  const agent = props.agent
  if (!agent?.providerId || !agent.modelId || !configuredProviderPlatform.value) {
    throw new Error('PET_CHAT_NOT_CONFIGURED')
  }

  const request: PetChatRequest = {
    petId: props.petId,
    requestId,
    provider: {
      platform: configuredProviderPlatform.value,
      providerId: agent.providerId.trim(),
      model: agent.modelId.trim(),
      capability: 'chat',
      autoFallback: true
    },
    persona: buildPersona(),
    userText,
    images: images.map(({ data, mediaType }) => ({ data, mediaType })),
    history: buildHistory(excludeMessageId),
    projectFolder: resolveProjectFolder()
  }
  // request 只携带 provider/model 引用；API Key 和 provider 实体由后端 resolver 读取。
  if (agent.reasoningEffort) request.reasoning = agent.reasoningEffort
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

function discardVoiceCapture(): void {
  // generation 让 getUserMedia/MediaRecorder 的晚到回调失去提交资格，
  // 这是切换宠物、卸载组件时避免误发上一只宠物语音的核心边界。
  voiceSessionGeneration += 1
  cancelActiveTranscription()
  activeRecorderShouldSubmit = false
  const recorder = activeRecorder
  const stream = activeRecorderStream
  const recorderAlreadyStopping = stoppingRecorder === recorder
  stoppingRecorder = null
  activeRecorder = null
  activeRecorderStream = null
  activeRecorderChunks = []
  activeRecorderPetId = ''
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
    recorder.ondataavailable = (event) => {
      if (activeRecorder === recorder && event.data.size > 0) activeRecorderChunks.push(event.data)
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
      }
      if (ownsStoppingState) {
        stoppingRecorder = null
        isStoppingRecording.value = false
      }
      stopMediaStream(stream)
      // 旧 recorder 的晚到 onstop 只能释放自己的 stream，不能改写当前 recorder 的 UI 状态。
      if (isCurrentRecorder) isRecording.value = false
      if (!shouldSubmit) return

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
  await scrollToLatest()
}

function removePendingImage(imageId: string): void {
  if (isComposerBusy.value) return
  pendingImages.value = pendingImages.value.filter((image) => image.id !== imageId)
  attachmentMessage.value = ''
}

function findMessage(messageId: string): ChatMessage | undefined {
  return messages.value.find((message) => message.id === messageId)
}

function isCurrentEvent(event: ChatEvent): boolean {
  if (!activeRequestId || event.requestId !== activeRequestId) return false
  if (event.petId && event.petId !== props.petId) return false
  // sequence 是后端为并发/取消竞态提供的单调序号；旧事件必须在 UI 层再次丢弃。
  if (event.sequence > 0 && event.sequence <= lastEventSequence) return false
  if (event.sequence > 0) lastEventSequence = event.sequence
  return true
}

function settleRequest(): void {
  activeRequestId = ''
  activeAssistantId = ''
  activeRawAssistantText = ''
  lastEventSequence = 0
}

function safeErrorMessage(errorCode: string): string {
  const messageKeys: Record<string, string> = {
    PET_AI_REQUEST_CANCELLED: 'pet.chat.errors.cancelled',
    PET_AI_TIMEOUT: 'pet.chat.errors.timeout',
    PET_AI_REQUEST_IN_FLIGHT: 'pet.chat.errors.inFlight',
    PET_PROVIDER_NOT_FOUND: 'pet.chat.errors.providerNotFound',
    PET_MODEL_NOT_CONFIGURED: 'pet.chat.errors.modelNotConfigured',
    PET_MODEL_UNSUPPORTED: 'pet.chat.errors.modelUnsupported',
    PET_REFERENCE_INVALID: 'pet.chat.errors.referenceInvalid',
    PET_CAPABILITY_UNSUPPORTED: 'pet.chat.errors.capabilityUnsupported',
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
  const assistant = findMessage(activeAssistantId)
  if (assistant) {
    assistant.content = cleanPetAssistantText(assistant.content)
    assistant.content = assistant.content || t('pet.chat.message.incomplete')
    assistant.status = 'error'
  }
  lastFailedText = userText
  failureMessage.value = safeErrorMessage(errorCode)
  settleRequest()
  phase.value = 'error'
}

function handleChatEvent(value: unknown): void {
  const event = normalizeChatEvent(value)
  if (!event || !isCurrentEvent(event)) return

  if (event.type === 'started') {
    phase.value = 'streaming'
    return
  }
  if (event.type === 'delta') {
    const assistant = findMessage(activeAssistantId)
    if (!assistant) return
    // 先保留原始流，再按完整前缀清洗；未闭合协议从起点开始整段隐藏，
    // 否则 JSON 会随着 delta 碎片短暂闪现在气泡里。
    activeRawAssistantText += event.delta
    assistant.content = cleanPetAssistantText(activeRawAssistantText)
    assistant.status = 'streaming'
    phase.value = 'streaming'
    void scrollToLatest()
    return
  }
  if (event.type === 'completed') {
    const assistant = findMessage(activeAssistantId)
    // completed 事件携带全文；没有全文时退回已累积的原始 delta。
    const rawReply = event.text || activeRawAssistantText
    if (event.text) activeRawAssistantText = event.text
    const extractedPlan = extractPetPlan(rawReply)
    if (assistant) assistant.content = cleanPetAssistantText(rawReply)
    if (assistant) {
      assistant.status = 'complete'
      assistant.content = assistant.content || t('pet.chat.message.noText')
    }
    settleRequest()
    phase.value = 'idle'
    failureMessage.value = ''
    void scrollToLatest()
    if (extractedPlan.error) {
      showPlanError(extractedPlan.error)
    } else if (extractedPlan.plan) {
      // 计划失败不能回滚已经完成的聊天消息；只在计划面板显示错误，保证
      // malformed JSON 或调度服务故障不会把流式聊天状态打成失败。
      void schedulePetPlan(extractedPlan.plan)
    }
    return
  }
  if (event.type === 'cancelled') {
    const assistant = findMessage(activeAssistantId)
    if (assistant) {
      assistant.content = assistant.content || t('pet.chat.message.cancelled')
      assistant.status = 'cancelled'
    }
    settleRequest()
    phase.value = 'idle'
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
  const userMessage = createMessage('user', text, 'complete', images)
  const assistantMessage = createMessage('assistant', '', 'streaming')
  messages.value.push(userMessage, assistantMessage)
  inputText.value = ''
  pendingImages.value = []
  attachmentMessage.value = ''
  voiceInputMessage.value = ''
  failureMessage.value = ''
  lastFailedText = text
  lastFailedImages = cloneImages(images)
  activeRequestId = requestId
  activeAssistantId = assistantMessage.id
  activeRawAssistantText = ''
  lastEventSequence = 0
  phase.value = 'starting'
  await scrollToLatest()

  try {
    const request = buildChatRequest(requestId, text, images, userMessage.id)
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
    settleFailure(
      requestId,
      text,
      isMissingBindingError(error) ? 'PET_AI_DEPENDENCY_UNAVAILABLE' : ''
    )
  }
}

async function cancelMessage(): Promise<void> {
  const requestId = activeRequestId
  const assistantId = activeAssistantId
  if (!requestId || !isBusy.value) return

  phase.value = 'cancelling'
  // 先让 UI 失去这条 request 的所有权，再请求后端取消；这样晚到的 delta
  // 即便已经排进 runtime 事件队列，也无法污染下一次会话。
  activeRequestId = ''
  activeAssistantId = ''
  lastEventSequence = 0
  const assistant = findMessage(assistantId)
  if (assistant) {
    assistant.content = assistant.content || t('pet.chat.message.cancelled')
    assistant.status = 'cancelled'
  }

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

function showChat(): void {
  showDreams.value = false
  showPlans.value = false
}

function toggleDreams(): void {
  showDreams.value = !showDreams.value
  showPlans.value = false
  if (showDreams.value && !selectedDreamId.value) {
    selectedDreamId.value = props.dreams[0]?.id ?? ''
  }
}

function togglePlans(): void {
  showPlans.value = !showPlans.value
  showDreams.value = false
  if (showPlans.value && plansPhase.value === 'idle') void loadPlans()
}

function selectDream(id: string): void {
  selectedDreamId.value = id
}

function previewDream(dream: PetDreamHistoryRecord): string {
  const text = dream.sleepTalk || dream.dream || dream.effectivePrompt
  const compact = text.replace(/\s+/g, ' ').trim()
  return compact.length > 70 ? compact.slice(0, 70) + '...' : compact || t('pet.chat.dream.noText')
}

function formatDreamDate(createdAt: number): string {
  if (!createdAt) return t('pet.common.unknownDate')
  try {
    return new Intl.DateTimeFormat(locale.value, {
      month: 'numeric',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit'
    }).format(createdAt)
  } catch {
    return t('pet.common.unknownDate')
  }
}

function formatMessageTime(createdAt: number): string {
  try {
    return new Intl.DateTimeFormat(locale.value, {
      hour: '2-digit',
      minute: '2-digit'
    }).format(createdAt)
  } catch {
    return ''
  }
}

async function scrollToLatest(): Promise<void> {
  await nextTick()
  const element = messageListRef.value
  if (element) element.scrollTop = element.scrollHeight
}

function handleInputKeydown(event: KeyboardEvent): void {
  if (event.isComposing || event.key !== 'Enter' || event.shiftKey) return
  event.preventDefault()
  void sendMessage()
}

watch(
  () => props.dreams,
  (dreams) => {
    if (selectedDreamId.value && dreams.some((dream) => dream.id === selectedDreamId.value)) return
    selectedDreamId.value = dreams[0]?.id ?? ''
  },
  { immediate: true }
)

watch(
  () => props.petId,
  () => {
    // 宠物切换时必须清空会话内历史，避免把上一只宠物的上下文串进下一条请求。
    imageSelectionGeneration += 1
    discardVoiceCapture()
    isTranscribing.value = false
    voiceInputMessage.value = ''
    activeRequestId = ''
    activeAssistantId = ''
    lastEventSequence = 0
    messages.value = []
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
  activeRequestId = ''
  activeAssistantId = ''
  lastEventSequence = 0
  if (requestId) {
    // 组件销毁时只发取消信号，不等待结果，也不把 runtime 错误写入新页面状态。
    void Call.ByName(PET_AI_METHODS.cancelChat, requestId).catch(() => undefined)
  }
  pendingImages.value = []
  lastFailedImages = []
})
</script>

<template>
  <section class="pet-chat" :class="{ 'is-busy': isBusy, 'is-dreams': showDreams, 'is-plans': showPlans }" :aria-label="t('pet.chat.aria.chatPanel')">
    <header class="pet-chat__header">
      <div class="pet-chat__heading">
        <span class="pet-chat__eyebrow">{{ t('pet.chat.eyebrow') }}</span>
        <strong>{{ petName }}</strong>
      </div>
      <nav class="pet-chat__tabs" :aria-label="t('pet.chat.aria.contentTabs')">
        <button
          type="button"
          class="pet-chat__tab"
          :class="{ 'is-active': !showDreams && !showPlans }"
          :aria-selected="!showDreams && !showPlans"
          @click="showChat"
        >
          {{ t('pet.chat.tabs.chat') }}
        </button>
        <button
          type="button"
          class="pet-chat__tab"
          :class="{ 'is-active': showDreams }"
          :aria-selected="showDreams"
          @click="toggleDreams"
        >
          {{ t('pet.chat.tabs.dreams') }} <span class="pet-chat__count">{{ dreamCountLabel }}</span>
        </button>
        <button
          type="button"
          class="pet-chat__tab"
          :class="{ 'is-active': showPlans }"
          :aria-selected="showPlans"
          @click="togglePlans"
        >
          {{ t('pet.chat.tabs.plans') }} <span class="pet-chat__count">{{ planCountLabel }}</span>
        </button>
      </nav>
    </header>

    <template v-if="showDreams">
      <div class="pet-chat__dream-layout">
        <div class="pet-chat__dream-list" :aria-label="t('pet.chat.aria.dreamHistory')">
          <div v-if="props.dreams.length === 0" class="pet-chat__empty-state">
            <span class="pet-chat__empty-glyph" aria-hidden="true">✦</span>
            <span>{{ t('pet.chat.dream.empty') }}</span>
            <small>{{ t('pet.chat.dream.emptyHint') }}</small>
          </div>
          <button
            v-for="dream in props.dreams"
            :key="dream.id"
            type="button"
            class="pet-chat__dream-item"
            :class="{ 'is-selected': selectedDreamId === dream.id }"
            @click="selectDream(dream.id)"
          >
            <span class="pet-chat__dream-date">{{ formatDreamDate(dream.createdAt) }}</span>
            <strong>{{ dream.title || t('pet.chat.dream.untitled') }}</strong>
            <span>{{ previewDream(dream) }}</span>
          </button>
        </div>

        <article v-if="selectedDream" class="pet-chat__dream-detail" aria-live="polite">
          <header class="pet-chat__dream-detail-header">
            <div>
              <span class="pet-chat__dream-date">{{ formatDreamDate(selectedDream.createdAt) }}</span>
              <h3>{{ selectedDream.title || t('pet.chat.dream.untitled') }}</h3>
            </div>
            <span v-if="selectedDream.emotion" class="pet-chat__emotion">{{ t(`pet.dreamHistory.emotions.${selectedDream.emotion}`) }}</span>
          </header>
          <div class="pet-chat__dream-body">
            <p v-if="selectedDream.dream"><strong>{{ t('pet.chat.dream.dream') }}</strong>{{ selectedDream.dream }}</p>
            <p v-if="selectedDream.sleepTalk"><strong>{{ t('pet.chat.dream.sleepTalk') }}</strong>{{ selectedDream.sleepTalk }}</p>
            <p v-if="!selectedDream.dream && !selectedDream.sleepTalk">{{ t('pet.chat.dream.noText') }}</p>
          </div>
        </article>
        <div v-else-if="props.dreams.length > 0" class="pet-chat__empty-detail">
          {{ t('pet.chat.dream.selectHint') }}
        </div>
      </div>
    </template>

    <template v-else-if="showPlans">
      <div class="pet-chat__plan-layout" :aria-label="t('pet.chat.aria.planList')">
        <header class="pet-chat__plan-header">
          <div>
            <strong>{{ t('pet.chat.plan.title') }}</strong>
            <span>{{ t('pet.chat.plan.subtitle') }}</span>
          </div>
          <button type="button" class="pet-chat__retry" :disabled="plansPhase === 'loading'" @click="loadPlans()">
            {{ t('pet.common.refresh') }}
          </button>
        </header>

        <div v-if="plansPhase === 'loading'" class="pet-chat__plan-state" aria-live="polite">
          {{ t('pet.chat.plan.loading') }}
        </div>
        <div v-else-if="plansPhase === 'error'" class="pet-chat__plan-state is-error" aria-live="assertive">
          <span>{{ plansError || t('pet.chat.plan.loadFailed') }}</span>
          <button type="button" class="pet-chat__retry" @click="loadPlans()">{{ t('pet.common.retry') }}</button>
        </div>
        <div v-else-if="plans.length === 0" class="pet-chat__empty-state">
          <span class="pet-chat__empty-glyph" aria-hidden="true">◇</span>
          <span>{{ t('pet.chat.plan.empty') }}</span>
          <small>{{ t('pet.chat.plan.emptyHint') }}</small>
        </div>
        <div v-else class="pet-chat__plans">
          <article v-for="plan in plans" :key="plan.planId" class="pet-chat__plan-item">
            <header>
              <div>
                <strong>{{ plan.title || t('pet.chat.plan.defaultTitle') }}</strong>
                <span class="pet-chat__plan-date">{{ formatPlanDate(plan.updatedAt) }}</span>
              </div>
              <span class="pet-chat__plan-status" :class="{ 'is-cancelled': isPlanCancelled(plan.planId) }">
                {{ isPlanCancelled(plan.planId) ? t('pet.chat.plan.cancelled') : t('pet.chat.plan.queued') }}
              </span>
            </header>
            <ul>
              <li v-for="(step, index) in plan.script.steps" :key="plan.planId + '-' + index">
                {{ formatPlanStep(step) }}
              </li>
            </ul>
            <button
              type="button"
              class="pet-chat__plan-cancel"
              :disabled="Boolean(planBusyId) || isPlanCancelled(plan.planId)"
              @click="cancelPetPlan(plan)"
            >
              {{ planBusyId === plan.planId ? t('pet.chat.plan.cancelling') : t('pet.chat.plan.cancel') }}
            </button>
          </article>
        </div>
        <p v-if="planNotice" class="pet-chat__plan-notice" aria-live="polite">{{ planNotice }}</p>
      </div>
    </template>

    <template v-else>
      <div ref="messageListRef" class="pet-chat__messages" role="log" aria-live="polite" :aria-label="t('pet.chat.aria.messages')">
        <div v-if="messages.length === 0" class="pet-chat__empty-state">
          <span class="pet-chat__empty-glyph" aria-hidden="true">◌</span>
          <span>{{ t('pet.chat.empty') }}</span>
          <small>{{ t('pet.chat.emptyHint') }}</small>
        </div>
        <article
          v-for="message in messages"
          :key="message.id"
          class="pet-chat__message"
          :class="['is-' + message.role, 'is-' + message.status]"
        >
          <div class="pet-chat__message-meta">
            <span>{{ message.role === 'user' ? t('pet.chat.you') : petName }}</span>
            <time :datetime="new Date(message.createdAt).toISOString()">{{ formatMessageTime(message.createdAt) }}</time>
          </div>
          <div v-if="message.images.length > 0" class="pet-chat__message-images">
            <img
              v-for="image in message.images"
              :key="image.id"
              :src="image.previewUrl"
              class="pet-chat__message-image"
              :alt="message.role === 'user' ? t('pet.chat.attachments.sentImageAlt') : t('pet.chat.attachments.imageAlt')"
            />
          </div>
          <p>{{ message.content }}</p>
          <span v-if="message.status === 'streaming'" class="pet-chat__typing" :aria-label="t('pet.chat.message.generating')">...</span>
          <span v-else-if="message.status === 'error'" class="pet-chat__message-state">{{ t('pet.chat.message.incomplete') }}</span>
          <span v-else-if="message.status === 'cancelled'" class="pet-chat__message-state">{{ t('pet.chat.message.cancelledShort') }}</span>
        </article>
      </div>

      <div class="pet-chat__status" :class="'is-' + statusTone" aria-live="polite">
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
        <textarea
          v-model="inputText"
          rows="2"
          maxlength="16000"
           :disabled="isComposerBusy"
          :placeholder="t('pet.chat.composer.placeholder')"
          :aria-label="t('pet.chat.aria.input')"
          @keydown="handleInputKeydown"
        ></textarea>
        <div class="pet-chat__composer-actions">
          <button
            type="button"
            class="pet-chat__voice"
            :class="{ 'is-recording': isRecording }"
            :disabled="isBusy || isStartingRecording || isStoppingRecording || isTranscribing || !chatAvailability.ready"
            :aria-label="isRecording ? t('pet.chat.voice.stop') : t('pet.chat.voice.input')"
            :title="isRecording ? t('pet.chat.voice.stop') : t('pet.chat.voice.input')"
            @click="toggleVoiceInput"
          >
            {{ isStartingRecording ? t('pet.chat.voice.startingShort') : isTranscribing ? t('pet.chat.voice.transcribingShort') : isRecording ? t('pet.chat.voice.stopShort') : t('pet.chat.voice.inputShort') }}
          </button>
          <button
            type="button"
            class="pet-chat__attach"
            :disabled="isComposerBusy || pendingImages.length >= PET_CHAT_MAX_IMAGES"
            :aria-label="t('pet.chat.attachments.addImage')"
            :title="t('pet.chat.attachments.addImage')"
            @click="openImagePicker"
          >
            {{ t('pet.chat.attachments.imageButton') }}
          </button>
          <button
            v-if="isBusy"
            type="button"
            class="pet-chat__send pet-chat__send--cancel"
            :disabled="phase === 'cancelling'"
            @click="cancelMessage"
          >
            {{ phase === 'cancelling' ? t('pet.chat.composer.stopping') : t('pet.chat.composer.stop') }}
          </button>
          <button v-else type="submit" class="pet-chat__send" :disabled="!canSend || isComposerBusy">
            {{ t('pet.chat.composer.send') }}
          </button>
        </div>
      </form>
    </template>
  </section>
</template>

<style scoped>
.pet-chat {
  --pet-chat-ink: var(--pet-ink, var(--mac-text, #1d1d1f));
  --pet-chat-muted: var(--pet-muted, var(--mac-text-secondary, #6e6e73));
  --pet-chat-line: var(--pet-line, var(--mac-border, rgba(15, 23, 42, 0.12)));
  --pet-chat-surface: var(--pet-surface, rgba(255, 255, 255, 0.78));
  display: flex;
  width: 100%;
  max-width: 100%;
  min-width: 0;
  min-height: 0;
  flex-direction: column;
  overflow: hidden;
  box-sizing: border-box;
  border: 1px solid var(--pet-chat-line);
  border-radius: 14px;
  background: var(--pet-chat-surface);
  color: var(--pet-chat-ink);
  box-shadow: 0 8px 22px color-mix(in srgb, #243247 10%, transparent);
  backdrop-filter: blur(14px);
  font-family: var(--mac-font, system-ui, sans-serif);
}

.pet-chat__header,
.pet-chat__heading,
.pet-chat__tabs,
.pet-chat__status,
.pet-chat__message-meta,
.pet-chat__dream-detail-header {
  display: flex;
  align-items: center;
}

.pet-chat__header {
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
  padding: 9px 10px 8px;
  border-bottom: 1px solid var(--pet-chat-line);
}

.pet-chat__heading {
  min-width: 0;
  flex-direction: column;
  align-items: flex-start;
  gap: 1px;
}

.pet-chat__heading strong {
  max-width: 150px;
  overflow: hidden;
  color: var(--pet-chat-ink);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-chat__eyebrow,
.pet-chat__dream-date,
.pet-chat__message-meta,
.pet-chat__status,
.pet-chat__empty-state small,
.pet-chat__message-state {
  color: var(--pet-chat-muted);
  font-size: 10px;
}

.pet-chat__tabs {
  flex: 0 0 auto;
  gap: 3px;
  padding: 2px;
  border: 1px solid var(--pet-chat-line);
  border-radius: 8px;
  background: color-mix(in srgb, var(--pet-chat-muted) 7%, transparent);
}

.pet-chat__tab,
.pet-chat__send,
.pet-chat__retry {
  border: 0;
  cursor: pointer;
  font: inherit;
}

.pet-chat__tab {
  min-height: 26px;
  padding: 3px 8px;
  border-radius: 6px;
  background: transparent;
  color: var(--pet-chat-muted);
  font-size: 10px;
  transition: background 0.18s ease, color 0.18s ease;
}

.pet-chat__tab.is-active {
  background: var(--pet-chat-surface);
  color: var(--pet-chat-ink);
  box-shadow: 0 1px 4px color-mix(in srgb, #243247 12%, transparent);
}

.pet-chat__count {
  display: inline-flex;
  min-width: 15px;
  height: 15px;
  align-items: center;
  justify-content: center;
  margin-left: 2px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--mac-accent, #0a84ff) 12%, transparent);
  color: var(--mac-accent, #0a84ff);
  font-size: 9px;
  line-height: 1;
}

.pet-chat__messages {
  display: flex;
  min-height: 112px;
  max-height: 220px;
  flex-direction: column;
  gap: 8px;
  overflow-x: hidden;
  overflow-y: auto;
  padding: 9px;
  overscroll-behavior: contain;
  scrollbar-width: thin;
}

.pet-chat__empty-state {
  display: flex;
  min-height: 96px;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 4px;
  color: var(--pet-chat-muted);
  font-size: 11px;
  text-align: center;
}

.pet-chat__empty-state small {
  max-width: 220px;
  line-height: 15px;
}

.pet-chat__empty-glyph {
  color: color-mix(in srgb, var(--mac-accent, #0a84ff) 68%, var(--pet-chat-muted));
  font-size: 22px;
  line-height: 1;
}

.pet-chat__message {
  width: min(88%, 270px);
  min-width: 0;
  box-sizing: border-box;
  padding: 7px 9px;
  border: 1px solid var(--pet-chat-line);
  border-radius: 10px;
  background: color-mix(in srgb, var(--mac-surface, #fff) 42%, transparent);
}

.pet-chat__message.is-user {
  align-self: flex-end;
  border-color: color-mix(in srgb, var(--mac-accent, #0a84ff) 22%, var(--pet-chat-line));
  background: color-mix(in srgb, var(--mac-accent, #0a84ff) 9%, transparent);
}

.pet-chat__message.is-assistant {
  align-self: flex-start;
}

.pet-chat__message.is-error {
  border-color: color-mix(in srgb, #bd4f4f 34%, var(--pet-chat-line));
}

.pet-chat__message-meta {
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 3px;
}

.pet-chat__message p,
.pet-chat__dream-body p {
  margin: 0;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.pet-chat__message p {
  color: var(--pet-chat-ink);
  font-size: 11px;
  line-height: 17px;
}

.pet-chat__typing {
  display: inline-block;
  color: var(--mac-accent, #0a84ff);
  font-size: 12px;
  letter-spacing: 1px;
  animation: pet-chat-blink 1s steps(2, end) infinite;
}

.pet-chat__message-state {
  display: block;
  margin-top: 4px;
  color: #bd4f4f;
}

.pet-chat__message.is-cancelled .pet-chat__message-state {
  color: var(--pet-chat-muted);
}

.pet-chat__status {
  gap: 5px;
  min-width: 0;
  min-height: 28px;
  padding: 0 9px;
  border-top: 1px solid var(--pet-chat-line);
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
  flex: 0 0 auto;
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
  padding: 3px 7px;
  border-radius: 6px;
  background: color-mix(in srgb, #bd4f4f 12%, transparent);
  color: #bd4f4f;
  font-size: 10px;
}

.pet-chat__composer {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  min-width: 0;
  align-items: flex-end;
  gap: 6px;
  padding: 8px;
}

.pet-chat__file-input {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0 0 0 0);
  white-space: nowrap;
}

.pet-chat__attachment-strip,
.pet-chat__message-images {
  display: flex;
  min-width: 0;
  gap: 6px;
  overflow-x: auto;
}

.pet-chat__attachment-strip {
  grid-column: 1 / -1;
  padding: 2px 0;
}

.pet-chat__attachment,
.pet-chat__message-image {
  position: relative;
  overflow: hidden;
  border: 1px solid var(--pet-chat-line);
  border-radius: 7px;
  background: color-mix(in srgb, var(--pet-chat-muted) 8%, transparent);
}

.pet-chat__attachment {
  width: 42px;
  height: 42px;
  flex: 0 0 auto;
}

.pet-chat__attachment img,
.pet-chat__message-image {
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
  background: rgba(0, 0, 0, 0.66);
  color: #fff;
  cursor: pointer;
  font-size: 12px;
  line-height: 1;
}

.pet-chat__attachment-remove:disabled {
  cursor: wait;
  opacity: 0.5;
}

.pet-chat__message-images {
  margin: 0 0 5px;
}

.pet-chat__message-image {
  width: 76px;
  height: 76px;
  flex: 0 0 auto;
}

.pet-chat__composer-actions {
  display: flex;
  align-items: stretch;
  gap: 6px;
}

.pet-chat__voice,
.pet-chat__attach {
  min-height: 38px;
  border: 1px solid var(--pet-chat-line);
  border-radius: 8px;
  padding: 6px 8px;
  background: transparent;
  color: var(--pet-chat-muted);
  cursor: pointer;
  font: inherit;
  font-size: 11px;
}

.pet-chat__voice.is-recording {
  border-color: color-mix(in srgb, #bd4f4f 62%, var(--pet-chat-line));
  background: color-mix(in srgb, #bd4f4f 12%, transparent);
  color: #bd4f4f;
}

.pet-chat__voice:hover:not(:disabled),
.pet-chat__attach:hover:not(:disabled) {
  border-color: color-mix(in srgb, var(--mac-accent, #0a84ff) 50%, var(--pet-chat-line));
  color: var(--pet-chat-ink);
}

.pet-chat__voice:disabled,
.pet-chat__attach:disabled {
  cursor: not-allowed;
  opacity: 0.42;
}

.pet-chat__composer textarea {
  grid-column: 1;
  display: block;
  width: 100%;
  min-width: 0;
  min-height: 38px;
  max-height: 96px;
  box-sizing: border-box;
  resize: vertical;
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
  transition: border-color 0.18s ease, box-shadow 0.18s ease;
}

.pet-chat__composer textarea::placeholder {
  color: color-mix(in srgb, var(--pet-chat-muted) 78%, transparent);
}

.pet-chat__composer textarea:focus {
  border-color: color-mix(in srgb, var(--mac-accent, #0a84ff) 52%, var(--pet-chat-line));
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--mac-accent, #0a84ff) 13%, transparent);
}

.pet-chat__composer textarea:disabled {
  cursor: wait;
  opacity: 0.72;
}

.pet-chat__send {
  min-width: 48px;
  min-height: 38px;
  flex: 0 0 auto;
  border-radius: 8px;
  padding: 6px 8px;
  background: var(--mac-accent, #0a84ff);
  color: #fff;
  font-size: 11px;
  transition: opacity 0.18s ease, transform 0.18s ease;
}

@media (max-width: 440px) {
  .pet-chat__composer {
    grid-template-columns: minmax(0, 1fr);
  }

  .pet-chat__composer textarea,
  .pet-chat__composer-actions {
    grid-column: 1;
  }

  .pet-chat__composer-actions {
    justify-content: flex-end;
  }
}

.pet-chat__send:hover:not(:disabled) {
  transform: translateY(-1px);
}

.pet-chat__send:disabled {
  cursor: not-allowed;
  opacity: 0.42;
}

.pet-chat__send--cancel {
  background: #bd4f4f;
}

.pet-chat__dream-layout {
  display: grid;
  min-width: 0;
  max-height: 260px;
  grid-template-columns: minmax(0, 0.85fr) minmax(0, 1.15fr);
  gap: 7px;
  padding: 8px;
}

.pet-chat__dream-list,
.pet-chat__dream-detail,
.pet-chat__empty-detail {
  min-width: 0;
  max-height: 244px;
  overflow-x: hidden;
  overflow-y: auto;
  border-radius: 9px;
  background: color-mix(in srgb, var(--mac-surface, #fff) 34%, transparent);
  scrollbar-width: thin;
}

.pet-chat__dream-list {
  display: flex;
  flex-direction: column;
  gap: 5px;
  padding: 5px;
}

.pet-chat__dream-item {
  display: flex;
  min-width: 0;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
  border: 1px solid transparent;
  border-radius: 7px;
  padding: 7px;
  background: transparent;
  color: var(--pet-chat-muted);
  cursor: pointer;
  font: inherit;
  text-align: left;
}

.pet-chat__dream-item strong,
.pet-chat__dream-item > span:last-child {
  width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-chat__dream-item strong {
  color: var(--pet-chat-ink);
  font-size: 10px;
}

.pet-chat__dream-item > span:last-child {
  font-size: 10px;
}

.pet-chat__dream-item:hover,
.pet-chat__dream-item.is-selected {
  border-color: color-mix(in srgb, var(--mac-accent, #0a84ff) 28%, var(--pet-chat-line));
  background: color-mix(in srgb, var(--mac-accent, #0a84ff) 8%, transparent);
}

.pet-chat__dream-detail,
.pet-chat__empty-detail {
  padding: 10px;
}

.pet-chat__dream-detail-header {
  align-items: flex-start;
  justify-content: space-between;
  gap: 6px;
}

.pet-chat__dream-detail h3 {
  margin: 3px 0 0;
  overflow-wrap: anywhere;
  color: var(--pet-chat-ink);
  font-size: 12px;
  line-height: 16px;
}

.pet-chat__emotion {
  flex: 0 0 auto;
  border-radius: 999px;
  padding: 3px 6px;
  background: color-mix(in srgb, #c87e9c 14%, transparent);
  color: #a65b7c;
  font-size: 9px;
}

.pet-chat__dream-body {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin-top: 12px;
  color: var(--pet-chat-ink);
  font-size: 11px;
  line-height: 17px;
}

.pet-chat__dream-body strong {
  display: block;
  margin-bottom: 2px;
  color: var(--pet-chat-muted);
  font-size: 10px;
  font-weight: 600;
}

.pet-chat__empty-detail {
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--pet-chat-muted);
  font-size: 10px;
  text-align: center;
}

.pet-chat__plan-layout {
  display: flex;
  min-width: 0;
  max-height: 260px;
  flex-direction: column;
  gap: 7px;
  overflow: hidden;
  padding: 8px;
}

.pet-chat__plan-header,
.pet-chat__plan-item > header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 8px;
}

.pet-chat__plan-header > div,
.pet-chat__plan-item > header > div {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}

.pet-chat__plan-header strong,
.pet-chat__plan-item strong {
  overflow-wrap: anywhere;
  color: var(--pet-chat-ink);
  font-size: 11px;
}

.pet-chat__plan-header span,
.pet-chat__plan-date {
  color: var(--pet-chat-muted);
  font-size: 9px;
}

.pet-chat__plan-state {
  display: flex;
  min-height: 90px;
  align-items: center;
  justify-content: center;
  gap: 8px;
  color: var(--pet-chat-muted);
  font-size: 10px;
  text-align: center;
}

.pet-chat__plan-state.is-error {
  flex-direction: column;
  color: #bd4f4f;
}

.pet-chat__plans {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 6px;
  overflow-y: auto;
  scrollbar-width: thin;
}

.pet-chat__plan-item {
  min-width: 0;
  padding: 8px;
  border: 1px solid var(--pet-chat-line);
  border-radius: 9px;
  background: color-mix(in srgb, var(--mac-surface, #fff) 34%, transparent);
}

.pet-chat__plan-status {
  flex: 0 0 auto;
  color: #327c52;
  font-size: 9px;
}

.pet-chat__plan-status.is-cancelled {
  color: var(--pet-chat-muted);
}

.pet-chat__plan-item ul {
  display: flex;
  flex-direction: column;
  gap: 3px;
  margin: 7px 0;
  padding-left: 16px;
  color: var(--pet-chat-ink);
  font-size: 10px;
  line-height: 15px;
}

.pet-chat__plan-item li {
  overflow-wrap: anywhere;
}

.pet-chat__plan-cancel {
  border: 0;
  border-radius: 6px;
  padding: 3px 7px;
  background: color-mix(in srgb, #bd4f4f 12%, transparent);
  color: #bd4f4f;
  cursor: pointer;
  font: inherit;
  font-size: 10px;
}

.pet-chat__plan-cancel:disabled,
.pet-chat__retry:disabled {
  cursor: wait;
  opacity: 0.45;
}

.pet-chat__plan-notice {
  flex: 0 0 auto;
  margin: 0;
  color: var(--pet-chat-muted);
  font-size: 10px;
  line-height: 15px;
}

@keyframes pet-chat-blink {
  50% {
    opacity: 0.35;
  }
}

@media (max-width: 340px) {
  .pet-chat__header {
    align-items: flex-start;
    flex-direction: column;
  }

  .pet-chat__tabs {
    width: 100%;
    box-sizing: border-box;
  }

  .pet-chat__tab {
    width: 33.333%;
  }

  .pet-chat__dream-layout {
    max-height: 360px;
    grid-template-columns: minmax(0, 1fr);
  }

  .pet-chat__dream-list,
  .pet-chat__dream-detail,
  .pet-chat__empty-detail {
    max-height: 158px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .pet-chat__typing {
    animation: none;
  }

  .pet-chat__tab,
  .pet-chat__send,
  .pet-chat__composer textarea {
    transition: none;
  }
}
</style>
