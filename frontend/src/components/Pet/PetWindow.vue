<script setup lang="ts">
import { Call, Events } from '../../wails-runtime-compat'
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import PetAtlasFrame from './PetAtlasFrame.vue'
import PetChat from './PetChat.vue'
import { PetAudioPlayer, type PetAudioSpeechRequest } from './petAudio'
import {
  getPetAtlasFrame,
  parsePetAtlasDocument,
  type PetAtlasDocument
} from './petAtlas'
import { petApi, type PetRuntimeMode } from './petApi'
import {
  buildPetProactiveInstruction,
  formatPetPersonaMemories,
  formatPetPersonaProject,
  formatPetPersonaStatus,
  normalizePetContentLocale
} from './petProactive'
import {
  countPetDreamCharacters,
  getRandomPetDreamDelay,
  normalizePetDreamImagePayload
} from './petDreamProtocol'
import {
  DEFAULT_PET_ID,
  getLevelProgress,
  getPetLevel,
  type PetActionFailureReason,
  type PetAtlasAsset,
  type PetInteractionAction,
  type PetAwayReward,
  type PetProactiveFrequency,
  type PetSnapshot
} from './petTypes'

interface PetWindowProps {
  petId?: string
  atlasImageUrl?: string
  atlasManifest?: PetAtlasDocument | null
  scale?: number
  providerPlatform?: string
}

const props = withDefaults(defineProps<PetWindowProps>(), {
  petId: DEFAULT_PET_ID,
  atlasImageUrl: '',
  atlasManifest: null,
  scale: 0.28,
  providerPlatform: ''
})

const PET_DEFAULT_SCALE = 0.28
const PET_BEHAVIOR_DISPLAY_HEIGHTS: Record<string, number> = {
  idle: 100,
  walk: 98,
  sleep: 74,
  feed: 102,
  bathe: 108,
  soak: 110,
  swim: 80,
  zen: 112,
  play: 110,
  drag: 126,
  beg: 118,
  'report-time': 100
}

const { locale, t } = useI18n()

type ViewPhase = 'loading' | 'ready' | 'error'
type NoticeTone = 'success' | 'error' | 'muted'
type PetProviderCapability = 'chat' | 'tts' | 'image'
type PetWindowMode = 'passive' | 'interactive' | 'keyboard'
type PetAmbientBehavior =
  | 'walk'
  | 'sleep'
  | 'beg'
  | 'eat'
  | 'munch'
  | 'swim'
  | 'zen'
  | 'play'
  | 'report-time'

interface PetPointerGesture {
  pointerId: number
  startClientX: number
  startClientY: number
  startOffsetX: number
  startOffsetY: number
  moved: boolean
}

interface PetProviderReference {
  platform: string
  providerId: string
  model: string
  capability: PetProviderCapability
  autoFallback: boolean
}

interface PetDreamResult {
  dream: string
  title: string
  emotion: 'pleasant' | 'calm' | 'tense' | 'afraid'
  selfAppears: boolean
  sleepTalk: string
  creativePrompt: string
  effectivePrompt: string
  keywords: string[]
  themeId: string
  themeLabel: string
}

interface PetDreamPresentation {
  text: string
  imageUrl?: string
}

interface PetBinaryPayload {
  base64: string
  bytes: Uint8Array
}

interface PetDreamImageResult {
  images?: unknown[]
}

interface PetDailyBonusResult {
  bonus?: unknown
  snapshot?: unknown
}

interface PetDreamTheme {
  id: string
  label: string
  guidance: string
  allowsFoodMainline: boolean
}

const PET_AI_SERVICE = 'codeswitch/services.PetAIAPIService'
const PET_SERVICE = 'codeswitch/services.PetService'
const PET_IMAGE_SERVICE = 'codeswitch/services.PetImageAPIService'
const PET_DREAM_SERVICE = 'codeswitch/services.PetDreamAPIService'
const PET_AI_METHODS = {
  generateDreamText: `${PET_AI_SERVICE}.GenerateDreamText`,
  synthesizeSpeech: `${PET_AI_SERVICE}.SynthesizeSpeech`,
  startSpeechStream: `${PET_AI_SERVICE}.StartSpeechStream`,
  cancelSpeech: `${PET_AI_SERVICE}.CancelSpeech`
} as const
const PET_LIFECYCLE_METHODS = {
  claimDailyBonus: `${PET_SERVICE}.ClaimDailyBonusForPet`,
  markMilestone: `${PET_SERVICE}.MarkMilestoneForPet`
} as const
const PET_IMAGE_METHODS = {
  generate: `${PET_IMAGE_SERVICE}.GenerateImage`
} as const
const PET_DREAM_METHODS = {
  applyEmotion: `${PET_DREAM_SERVICE}.ApplyEmotion`,
  saveHistory: `${PET_DREAM_SERVICE}.SaveHistory`,
  storeImage: `${PET_DREAM_SERVICE}.StoreImage`
} as const
const PET_WINDOW_SERVICE = 'codeswitch/services.PetWindowAPI'
const PET_WINDOW_MODE_METHOD = `${PET_WINDOW_SERVICE}.SetMode`
const PET_WINDOW_IDLE_SECONDS_METHOD = `${PET_WINDOW_SERVICE}.IdleSeconds`
const PET_WINDOW_POINTER_EVENT = 'pet.window.pointer'
const PET_POINTER_DRAG_THRESHOLD = 6
const PET_AMBIENT_INTERVAL_MS = 5_000
const PET_IDLE_DOZE_THRESHOLD_SECONDS = 300
const PET_IDLE_WAKE_THRESHOLD_SECONDS = 5
const PET_IDLE_CHECK_INTERVAL_MS = 5_000

const PET_DREAM_SLEEP_TALK_MAX_LENGTH = 120
const PET_DREAM_READING_MS_PER_CHARACTER = 60
const PET_PROACTIVE_REMARK_MIN_GAP_MS = 10 * 60_000
const PET_PROACTIVE_TIMED_MIN_GAP_MS = 2 * 60 * 60_000
const PET_PROACTIVE_CHECK_INTERVAL_MS = 10 * 60_000
const PET_PROACTIVE_DAILY_CAP: Record<PetProactiveFrequency, number> = {
  low: 1,
  medium: 2,
  high: 4
}
const PET_COMPANION_MILESTONES = [730, 365, 100, 30, 7] as const
const PET_DREAM_THEME_POOL: readonly PetDreamTheme[] = [
  { id: 'food', label: '美食与烹饪', guidance: '围绕寻找食材、烹饪、分享美食或参加奇妙宴会展开。', allowsFoodMainline: true },
  { id: 'adventure', label: '探险与寻宝', guidance: '围绕未知地点、线索、地图、障碍和发现宝藏展开。', allowsFoodMainline: false },
  { id: 'travel', label: '旅行与远方', guidance: '围绕前往陌生城市、海岛、山谷或其他远方风景展开。', allowsFoodMainline: false },
  { id: 'nature', label: '自然与四季', guidance: '围绕森林、雨雪、花草、河流或季节变化中的奇妙经历展开。', allowsFoodMainline: false },
  { id: 'friendship', label: '友情与伙伴', guidance: '围绕结识伙伴、互相帮助、共同完成一件事或重逢展开。', allowsFoodMainline: false },
  { id: 'romance', label: '爱情与心动', guidance: '围绕温柔的相遇、默契、心动、牵挂或浪漫约定展开。', allowsFoodMainline: false },
  { id: 'festival', label: '节日与庆典', guidance: '围绕节日准备、庆典、灯会、游行或热闹的集体活动展开。', allowsFoodMainline: false },
  { id: 'fantasy', label: '魔法与奇幻', guidance: '围绕魔法、会说话的物品、神奇生物或不合常理的规则展开。', allowsFoodMainline: false },
  { id: 'mystery', label: '悬疑与解谜', guidance: '围绕奇怪线索、秘密房间、失踪物品或层层解开的谜团展开。', allowsFoodMainline: false },
  { id: 'music', label: '音乐与舞台', guidance: '围绕排练、演出、歌唱、舞蹈或临时登台的经历展开。', allowsFoodMainline: false },
  { id: 'competition', label: '运动与比赛', guidance: '围绕训练、比赛、合作竞技或意外获得冠军展开。', allowsFoodMainline: false },
  { id: 'creation', label: '建造与创造', guidance: '围绕设计、搭建、绘画、发明或把想象变成现实展开。', allowsFoodMainline: false },
  { id: 'space', label: '太空与未来', guidance: '围绕飞船、星球、未来城市或与未知文明相遇展开。', allowsFoodMainline: false }
]
const PET_DREAM_EMOTIONS = ['pleasant', 'calm', 'tense', 'afraid'] as const
const PET_SCHEDULER_ACTIONS = ['feed', 'bathe', 'soak', 'play', 'sleep', 'work', 'study'] as const

const snapshot = ref<PetSnapshot | null>(null)
const phase = ref<ViewPhase>('loading')
const errorMessage = ref('')
const notice = ref('')
const noticeTone = ref<NoticeTone>('muted')
const actionBusy = ref<PetInteractionAction | null>(null)
const transientAction = ref<PetInteractionAction | null>(null)
const now = ref(Date.now())
const runtimeMode = ref<PetRuntimeMode>('unknown')
const bundledAtlas = shallowRef<PetAtlasAsset | null>(null)
const dreamPresentation = ref<PetDreamPresentation | null>(null)
const proactivePresentation = ref<string | null>(null)
const ambientPresentation = ref<string | null>(null)
const ambientBehavior = ref<PetAmbientBehavior | null>(null)
const ambientFlipX = ref(false)
const contextMenuOpen = ref(false)
const chatOpen = ref(false)
const dragging = ref(false)
const dragOffset = ref({ x: 0, y: 0 })
const petWindowRef = ref<HTMLElement | null>(null)
const petStageRef = ref<HTMLElement | null>(null)
// dozing 是系统空闲造成的前端表现，不写入 PetSnapshot.sleeping，避免误触发梦境和持久化睡眠规则。
const dozing = ref(false)
const dreamPaused = ref(false)

let clockTimer: number | undefined
let refreshTimer: number | undefined
let proactiveTimer: number | undefined
let ambientTimer: number | undefined
let idleTimer: number | undefined
let reportTimer: number | undefined
let refreshInFlight = false
let refreshRequested = false
let snapshotGeneration = 0
let lifecycleInitializedPetId = ''
let transientToken = 0
let dreamTimer: number | undefined
let dreamBubbleTimer: number | undefined
let dreamSessionGeneration = 0
let dreamSessionActive = false
let dreamTextController: AbortController | null = null
let dreamTextRequestId: string | null = null
let dreamImageController: AbortController | null = null
let dreamSpeechRequestId: string | null = null
let proactiveBubbleTimer: number | undefined
let proactiveGeneration = 0
let lastProactiveRemarkAt = 0
let proactiveSpeechRequestId: string | null = null
let ambientToken = 0
let ambientBehaviorTimer: number | undefined
let ambientPresentationTimer: number | undefined
let pendingPetClickTimer: number | undefined
let petPointerGesture: PetPointerGesture | null = null
let windowModeBridgeUnavailable = false
let requestedWindowMode: PetWindowMode = 'passive'
let appliedWindowMode: PetWindowMode = 'passive'
let windowModeQueue: Promise<void> = Promise.resolve()
let petWindowUnmounted = false
let idleCheckInFlight = false
let idleBridgeUnavailable = false
let stopPetActionEvent: (() => void) | null = null
let stopPetRuntimeEvent: (() => void) | null = null
let stopPetReminderEvent: (() => void) | null = null
let stopPetAudioEvent: (() => void) | null = null
let stopPetPointerEvent: (() => void) | null = null

const state = computed(() => snapshot.value?.state ?? null)
const experience = computed(() => snapshot.value?.experience.totalExp ?? 0)
const totalGrowth = computed(() => (state.value?.growth ?? 0) + experience.value)
const level = computed(() => getPetLevel(totalGrowth.value))
const levelProgress = computed(() => getLevelProgress(totalGrowth.value))
const levelProgressPercent = computed(() => Math.round(levelProgress.value * 100))
const petName = computed(() => state.value?.name || t('pet.window.defaultName'))
const coins = computed(() => Math.floor(state.value?.coins ?? 0))
const awayRemaining = computed(() => {
  const endsAt = state.value?.awayTask?.endsAt ?? 0
  return Math.max(0, endsAt - now.value)
})

const atlas = computed<PetAtlasAsset | null>(() => {
  if (props.atlasManifest && props.atlasImageUrl) {
    return { src: props.atlasImageUrl, manifest: props.atlasManifest }
  }
  return snapshot.value?.atlas ?? bundledAtlas.value
})

const atlasBehavior = computed(() => {
  if (dragging.value) return 'drag'
  if (state.value?.sleeping) return 'sleep'
  if (dozing.value) return 'sleep'
  if (state.value?.awayTask) return state.value.awayTask.kind === 'work' ? 'walk' : 'beg'
  const action = transientAction.value
  if (action === 'feed') return 'feed'
  if (action === 'bathe') return 'bathe'
  if (action === 'soak') return 'soak'
  if (action === 'play') return 'play'
  if (ambientBehavior.value === 'eat' || ambientBehavior.value === 'munch') return 'feed'
  if (ambientBehavior.value) return ambientBehavior.value
  return 'idle'
})

const petDisplayHeight = computed(() => {
  const baseHeight = PET_BEHAVIOR_DISPLAY_HEIGHTS[atlasBehavior.value] ?? PET_BEHAVIOR_DISPLAY_HEIGHTS.idle
  const requestedScale = Number.isFinite(props.scale) && props.scale > 0 ? props.scale : PET_DEFAULT_SCALE
  // scale 继续作为整体倍率保留；默认值 0.28 经过归一化后，动作高度与 OpenCowork 原版一致。
  return baseHeight * (requestedScale / PET_DEFAULT_SCALE)
})

const statusText = computed(() => {
  if (state.value?.awayTask) {
    return state.value.awayTask.kind === 'work'
      ? t('pet.window.status.working', { time: formatCountdown(awayRemaining.value) })
      : t('pet.window.status.studying', { time: formatCountdown(awayRemaining.value) })
  }
  if (state.value?.sleeping) return t('pet.window.status.sleeping')
  if (dozing.value) return t('pet.window.status.dozing')
  if (actionBusy.value) return t('pet.window.status.busy', { action: getActionLabel(actionBusy.value) })
  if (runtimeMode.value === 'fallback') return t('pet.window.status.fallback')
  return notice.value || t('pet.window.status.ready')
})

const statItems = computed(() => [
  { key: 'hunger', label: t('pet.window.stats.hunger'), value: state.value?.hunger ?? 0, tone: 'hunger' },
  { key: 'cleanliness', label: t('pet.window.stats.cleanliness'), value: state.value?.cleanliness ?? 0, tone: 'cleanliness' },
  { key: 'mood', label: t('pet.window.stats.mood'), value: state.value?.mood ?? 0, tone: 'mood' }
])

const actionButtons = computed<Array<{ action: PetInteractionAction; label: string; glyph: string }>>(() => [
  // 文案和按钮内的短字都走同一套 locale，避免英文界面仍残留中文动作标签。
  { action: 'feed', label: t('pet.window.actions.feed'), glyph: t('pet.window.actionGlyphs.feed') },
  { action: 'bathe', label: t('pet.window.actions.bathe'), glyph: t('pet.window.actionGlyphs.bathe') },
  { action: 'soak', label: t('pet.window.actions.soak'), glyph: t('pet.window.actionGlyphs.soak') },
  { action: 'play', label: t('pet.window.actions.play'), glyph: t('pet.window.actionGlyphs.play') },
  { action: 'sleep', label: t('pet.window.actions.sleep'), glyph: t('pet.window.actionGlyphs.sleep') },
  { action: 'work', label: t('pet.window.actions.work'), glyph: t('pet.window.actionGlyphs.work') },
  { action: 'study', label: t('pet.window.actions.study'), glyph: t('pet.window.actionGlyphs.study') },
  { action: 'petted', label: t('pet.window.actions.petted'), glyph: t('pet.window.actionGlyphs.petted') }
])

function formatCountdown(milliseconds: number): string {
  const totalSeconds = Math.max(0, Math.ceil(milliseconds / 1000))
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  return `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`
}

function getActionLabel(action: PetInteractionAction): string {
  return actionButtons.value.find((item) => item.action === action)?.label ?? t('pet.window.actions.generic')
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

async function callPetBridge<T>(method: string, ...args: unknown[]): Promise<T> {
  return (await Call.ByName(method, ...args)) as T
}

function requestPetWindowMode(mode: PetWindowMode, force = false): void {
  if (windowModeBridgeUnavailable || (!force && mode === requestedWindowMode)) return
  requestedWindowMode = mode

  // SetMode 是增强能力；绑定缺失或平台不支持时只熔断这条桥，不阻断 Vue 内的聊天和动作按钮。
  windowModeQueue = windowModeQueue.then(async () => {
    if (windowModeBridgeUnavailable || (!force && mode === appliedWindowMode)) return
    try {
      await Call.ByName(PET_WINDOW_MODE_METHOD, mode)
      appliedWindowMode = mode
    } catch {
      windowModeBridgeUnavailable = true
    }
  })
}

function isKeyboardTarget(value: EventTarget | null): boolean {
  return value instanceof HTMLElement && value.matches('input, textarea, select, [contenteditable="true"]')
}

function shouldRetainKeyboardMode(): boolean {
  // 只有聊天面板中的真实输入焦点需要保留 keyboard；普通菜单焦点和透明空白都不能
  // 把整条底部 overlay 锁成可交互窗口，否则用户当前应用会再次被挡住。
  return chatOpen.value && isKeyboardTarget(document.activeElement)
}

function isInteractiveTarget(value: EventTarget | null): boolean {
  return (
    value instanceof Element &&
    Boolean(value.closest('.pet-window__pet-stage, .pet-window__context-menu, .pet-chat, button, input, textarea, select'))
  )
}

function syncPetWindowModeAtPointer(event: PointerEvent): void {
  const hovered = typeof document === 'undefined'
    ? null
    : document.elementFromPoint(event.clientX, event.clientY)
  if (isInteractiveTarget(hovered)) {
    requestPetWindowMode(isKeyboardTarget(document.activeElement) ? 'keyboard' : 'interactive')
    return
  }
  // 全屏 overlay 的根节点没有真实可交互内容；鼠标离开宠物/菜单/聊天后必须恢复穿透，
  // 不能因为菜单或聊天仍然打开，就把整个工作区继续变成挡鼠标的原生窗口。
  if (!dragging.value) requestPetWindowMode(shouldRetainKeyboardMode() ? 'keyboard' : 'passive')
}

function handleNativePetPointer(value: unknown): void {
  const source = Array.isArray(value) && value.length === 1 ? value[0] : value
  if (petWindowUnmounted || !isRecord(source)) return
  const inside = source.inside === true
  if (!inside) {
    if (!dragging.value) requestPetWindowMode(shouldRetainKeyboardMode() ? 'keyboard' : 'passive')
    return
  }
  const screenX = typeof source.screenX === 'number' ? source.screenX : NaN
  const screenY = typeof source.screenY === 'number' ? source.screenY : NaN
  const windowX = typeof source.windowX === 'number' ? source.windowX : NaN
  const windowY = typeof source.windowY === 'number' ? source.windowY : NaN
  if (![screenX, screenY, windowX, windowY].every(Number.isFinite)) return

  // Wails 没有 Electron 的 forward mouse；原生层只提供屏幕坐标，DOM 命中仍由 renderer 判断，
  // 这样透明空白区域继续穿透，只有真正落在宠物/菜单/聊天控件上的光标才恢复交互。
  const windowWidth = typeof source.windowWidth === 'number' ? source.windowWidth : NaN
  const windowHeight = typeof source.windowHeight === 'number' ? source.windowHeight : NaN
  const viewportWidth = document.documentElement.clientWidth || window.innerWidth
  const viewportHeight = document.documentElement.clientHeight || window.innerHeight
  if (![windowWidth, windowHeight].every((item) => Number.isFinite(item) && item > 0)) return
  // Native GetWindowRect 使用物理像素，WebView 的 DOM 使用 CSS/DIP；按窗口比例换算，
  // 才能在高 DPI 和负坐标副屏上保持 elementFromPoint 的命中位置一致。
  const localX = ((screenX - windowX) / windowWidth) * viewportWidth
  const localY = ((screenY - windowY) / windowHeight) * viewportHeight
  const hovered = document.elementFromPoint(localX, localY)
  if (isInteractiveTarget(hovered)) {
    requestPetWindowMode(isKeyboardTarget(document.activeElement) ? 'keyboard' : 'interactive')
  } else if (!dragging.value) {
    // interactive 状态下原生窗口覆盖整个 work area，不会再触发 DOM pointerleave；
    // 轮询命中透明空白后主动恢复 WS_EX_TRANSPARENT，避免一次碰到宠物就永久挡住桌面。
    requestPetWindowMode(shouldRetainKeyboardMode() ? 'keyboard' : 'passive')
  }
}

function handleWindowPointerOver(event: PointerEvent): void {
  if (isInteractiveTarget(event.target)) requestPetWindowMode('interactive')
}

function handleWindowPointerLeave(): void {
  if (dragging.value) return
  requestPetWindowMode(shouldRetainKeyboardMode() ? 'keyboard' : 'passive')
}

function handleWindowFocusIn(event: FocusEvent): void {
  requestPetWindowMode(isKeyboardTarget(event.target) ? 'keyboard' : 'interactive')
}

function handleWindowFocusOut(event: FocusEvent): void {
  const currentTarget = event.currentTarget
  const nextTarget = event.relatedTarget
  if (currentTarget instanceof HTMLElement && nextTarget instanceof Node && currentTarget.contains(nextTarget)) return
  if (!dragging.value) requestPetWindowMode(shouldRetainKeyboardMode() ? 'keyboard' : 'passive')
}

function isPetChatActive(): boolean {
  if (typeof document === 'undefined') return false
  const chat = document.querySelector<HTMLElement>('.pet-chat')
  // PetChat 是现有组件边界，不能新增共享 open 状态；焦点或悬停足以表示用户正在操作聊天面板。
  return Boolean(chat && (chat.matches(':hover') || chat.contains(document.activeElement)))
}

function wakePetFromDozing(): void {
  if (!dozing.value) return
  dozing.value = false
  if (!state.value?.sleeping && !state.value?.awayTask) {
    setAmbientBehavior('play', 1_400)
    showAmbientPresentation(t('pet.window.ambient.welcome'), 3_000)
  }
}

function enterPetDozing(): void {
  if (dozing.value) return
  // Dozing 是被动休息，进入时必须撤销已经排队的自动表现，避免旧请求在打盹期间补播。
  stopAmbientBehavior()
  stopProactiveSession()
  transientToken += 1
  transientAction.value = null
  dozing.value = true
}

async function pollPetWindowIdle(): Promise<void> {
  if (petWindowUnmounted || idleBridgeUnavailable || idleCheckInFlight || phase.value !== 'ready') return
  idleCheckInFlight = true
  try {
    const raw = await callPetBridge<unknown>(PET_WINDOW_IDLE_SECONDS_METHOD)
    if (petWindowUnmounted || typeof raw !== 'number' || !Number.isFinite(raw) || raw < 0) return
    const idleSeconds = raw
    const current = state.value
    if (!current || phase.value !== 'ready') return

    if (dozing.value) {
      if (idleSeconds < PET_IDLE_WAKE_THRESHOLD_SECONDS) wakePetFromDozing()
      return
    }

    if (
      idleSeconds >= PET_IDLE_DOZE_THRESHOLD_SECONDS &&
      !current.sleeping &&
      !current.awayTask &&
      !isPetChatActive() &&
      !contextMenuOpen.value &&
      !dragging.value &&
      !actionBusy.value &&
      !transientAction.value
    ) {
      enterPetDozing()
    }
  } catch {
    // 非 Windows 或旧宿主会拒绝该增强 API；熔断轮询即可，不能把失败当作 idle=0 反复刷请求。
    idleBridgeUnavailable = true
  } finally {
    idleCheckInFlight = false
  }
}

function showAmbientPresentation(text: string, duration = 5_000): void {
  if (ambientPresentationTimer !== undefined) window.clearTimeout(ambientPresentationTimer)
  ambientPresentation.value = text
  ambientPresentationTimer = window.setTimeout(() => {
    ambientPresentation.value = null
    ambientPresentationTimer = undefined
  }, duration)
}

function formatPetClockTime(date: Date): string {
  return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`
}

function getNextPetReportBoundary(nowDate: Date): Date {
  const next = new Date(nowDate)
  next.setSeconds(0, 0)
  if (nowDate.getMinutes() < 30) next.setMinutes(30)
  else next.setHours(next.getHours() + 1, 0, 0, 0)
  return next
}

function stopAmbientBehavior(): void {
  ambientToken += 1
  if (ambientBehaviorTimer !== undefined) window.clearTimeout(ambientBehaviorTimer)
  ambientBehaviorTimer = undefined
  ambientBehavior.value = null
  ambientFlipX.value = false
}

function setAmbientBehavior(behavior: PetAmbientBehavior, duration: number): void {
  if (dozing.value) return
  ambientToken += 1
  const token = ambientToken
  if (ambientBehaviorTimer !== undefined) window.clearTimeout(ambientBehaviorTimer)
  ambientBehaviorTimer = undefined
  ambientBehavior.value = behavior
  ambientFlipX.value = behavior === 'walk' || behavior === 'swim' ? Math.random() < 0.5 : false
  ambientBehaviorTimer = window.setTimeout(() => {
    if (token !== ambientToken) return
    ambientBehavior.value = null
    ambientFlipX.value = false
    ambientBehaviorTimer = undefined
  }, duration)
}

function canRunAmbientBehavior(): boolean {
  const current = state.value
  return Boolean(
    current &&
      phase.value === 'ready' &&
      !current.sleeping &&
      !current.awayTask &&
      !dozing.value &&
      !actionBusy.value &&
      !transientAction.value &&
      !contextMenuOpen.value &&
      !dragging.value &&
      !dreamPresentation.value
  )
}

function runAmbientBehavior(): void {
  if (!canRunAmbientBehavior() || ambientBehavior.value) return
  const current = state.value
  if (!current) return

  const hour = new Date().getHours()
  if (current.hunger < 30 && Math.random() < (current.hunger < 15 ? 0.7 : 0.5)) {
    setAmbientBehavior('beg', 3_000)
    return
  }
  if (current.cleanliness < 30 && Math.random() < 0.4) {
    setAmbientBehavior('beg', 3_000)
    return
  }
  if ((hour >= 23 || hour < 5) && Math.random() < 0.25) {
    setAmbientBehavior('sleep', 6_000)
    showAmbientPresentation(t('pet.window.ambient.night'))
    return
  }

  const roll = Math.random()
  if (roll < 0.18) setAmbientBehavior('zen', 8_000)
  else if (roll < 0.3) setAmbientBehavior(Math.random() < 0.5 ? 'eat' : 'munch', 2_600)
  else if (roll < 0.4) setAmbientBehavior('play', 2_200)
  else if (roll < 0.52) setAmbientBehavior('swim', 4_000)
  else setAmbientBehavior('walk', 4_000)
}

function schedulePetTimeReport(): void {
  if (reportTimer !== undefined) window.clearTimeout(reportTimer)
  const boundary = getNextPetReportBoundary(new Date())
  const delay = Math.max(1_000, boundary.getTime() - Date.now())
  reportTimer = window.setTimeout(() => {
    reportTimer = undefined
    const current = new Date()
    // 系统挂起跨过整点时跳过本次，不补播过期报时，下一边界继续正常排期。
    if (
      current.getHours() === boundary.getHours() &&
      current.getMinutes() === boundary.getMinutes() &&
      canRunAmbientBehavior()
    ) {
      setAmbientBehavior('report-time', 2_600)
      showAmbientPresentation(t('pet.window.ambient.time', { time: formatPetClockTime(boundary) }))
    }
    schedulePetTimeReport()
  }, delay)
}

function cancelPendingPetClick(): void {
  if (pendingPetClickTimer === undefined) return
  window.clearTimeout(pendingPetClickTimer)
  pendingPetClickTimer = undefined
}

function queuePetting(): void {
  cancelPendingPetClick()
  pendingPetClickTimer = window.setTimeout(() => {
    pendingPetClickTimer = undefined
    void runAction('petted')
  }, 220)
}

function openPetChat(): void {
  contextMenuOpen.value = false
  chatOpen.value = true
  requestPetWindowMode('keyboard')

  const focusInput = (): void => {
    const input = document.querySelector<HTMLTextAreaElement>('.pet-chat__composer textarea')
    if (input) {
      input.focus()
      return
    }

    // 梦境/计划页没有输入框，先切回聊天 tab，再在下一轮 DOM 更新后聚焦输入区。
    const chatTab = document.querySelector<HTMLButtonElement>('.pet-chat__tab')
    if (chatTab) {
      chatTab.click()
      window.setTimeout(focusInput, 0)
    }
  }
  window.setTimeout(focusInput, 0)
}

function closePetChat(): void {
  chatOpen.value = false
  if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
  requestPetWindowMode('passive')
}

function togglePetChat(): void {
  if (chatOpen.value) closePetChat()
  else openPetChat()
}

function openPetSettings(openStudio = false): void {
  contextMenuOpen.value = false
  chatOpen.value = false
  requestPetWindowMode('passive')
  // 设置/Studio 是主窗口页面；独立透明窗不挂 Router，不能再修改自身 hash
  // 期待主窗口跟着导航。由主 App 收到事件后负责唤起窗口和切换路由。
  void Events.Emit('pet.window.open-settings', { openStudio }).catch(() => undefined)
}

function hidePetWindow(): void {
  contextMenuOpen.value = false
  chatOpen.value = false
  requestPetWindowMode('passive')
  // 复用已有 PetApi 的窗口生命周期入口；不把“隐藏”误写成永久关闭配置。
  void petApi.setWindowEnabled(false).catch(() => undefined)
}

type PetContextMenuAction = 'chat' | 'settings' | 'studio' | 'hide'

function runContextMenuAction(action: PetContextMenuAction): void {
  if (action === 'chat') openPetChat()
  else if (action === 'settings') openPetSettings()
  else if (action === 'studio') openPetSettings(true)
  else hidePetWindow()
}

function toggleContextMenu(): void {
  cancelPendingPetClick()
  wakePetFromDozing()
  contextMenuOpen.value = !contextMenuOpen.value
  requestPetWindowMode(contextMenuOpen.value ? 'interactive' : 'passive')
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value))
}

function onPetPointerEnter(): void {
  requestPetWindowMode('interactive')
}

function onPetPointerLeave(): void {
  if (!dragging.value) {
    requestPetWindowMode(shouldRetainKeyboardMode() ? 'keyboard' : 'passive')
  }
}

function getPetDragBounds(stage: HTMLElement): { minX: number; maxX: number; minY: number; maxY: number } {
  const root = petWindowRef.value
  if (!root) return { minX: -10_000, maxX: 10_000, minY: -10_000, maxY: 10_000 }

  const rootRect = root.getBoundingClientRect()
  const stageRect = stage.getBoundingClientRect()
  const currentX = dragOffset.value.x
  const currentY = dragOffset.value.y
  // 用当前位置反推出未拖动时的基准矩形，边界因此同时适配 DPI、窗口缩放和宠物当前偏移。
  const baseLeft = stageRect.left - rootRect.left - currentX
  const baseTop = stageRect.top - rootRect.top - currentY
  return {
    minX: -baseLeft,
    maxX: rootRect.width - baseLeft - stageRect.width,
    minY: -baseTop,
    maxY: rootRect.height - baseTop - stageRect.height
  }
}

function onPetPointerDown(event: PointerEvent): void {
  if (event.button !== 0) return
  const target = event.currentTarget
  if (!(target instanceof HTMLElement)) return

  cancelPendingPetClick()
  // 触碰是最高优先级的唤醒信号；后续移动仍由同一个 pointer capture 手势分流。
  wakePetFromDozing()
  requestPetWindowMode('interactive')
  target.setPointerCapture(event.pointerId)
  petPointerGesture = {
    pointerId: event.pointerId,
    startClientX: event.clientX,
    startClientY: event.clientY,
    startOffsetX: dragOffset.value.x,
    startOffsetY: dragOffset.value.y,
    moved: false
  }
  dragging.value = true
  event.preventDefault()
}

function onPetPointerMove(event: PointerEvent): void {
  const gesture = petPointerGesture
  if (!gesture || gesture.pointerId !== event.pointerId) return

  const dx = event.clientX - gesture.startClientX
  const dy = event.clientY - gesture.startClientY
  if (!gesture.moved && Math.hypot(dx, dy) > PET_POINTER_DRAG_THRESHOLD) {
    gesture.moved = true
    cancelPendingPetClick()
    stopAmbientBehavior()
  }
  if (!gesture.moved) return

  const stage = petStageRef.value
  if (stage) {
    const bounds = getPetDragBounds(stage)
    dragOffset.value = {
      x: clamp(gesture.startOffsetX + dx, bounds.minX, bounds.maxX),
      y: clamp(gesture.startOffsetY + dy, bounds.minY, bounds.maxY)
    }
  }
  requestPetWindowMode('interactive')
  event.preventDefault()
}

function finishPetPointer(event: PointerEvent, cancelled = false): void {
  const gesture = petPointerGesture
  if (!gesture || gesture.pointerId !== event.pointerId) return

  const target = event.currentTarget
  if (target instanceof HTMLElement && target.hasPointerCapture(event.pointerId)) {
    target.releasePointerCapture(event.pointerId)
  }
  petPointerGesture = null
  dragging.value = false

  if (gesture.moved || cancelled) {
    if (cancelled) {
      // pointercancel 代表系统接管了手势，恢复到按下前的位置，不能把宠物瞬移回屏幕中间。
      dragOffset.value = { x: gesture.startOffsetX, y: gesture.startOffsetY }
    }
    syncPetWindowModeAtPointer(event)
    return
  }
  queuePetting()
  syncPetWindowModeAtPointer(event)
}

function handlePetKeydown(event: KeyboardEvent): void {
  if (event.key !== 'Enter' && event.key !== ' ') return
  event.preventDefault()
  cancelPendingPetClick()
  requestPetWindowMode('keyboard')
  void runAction('petted')
}

function createPetRequestId(prefix: string): string {
  const random =
    typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2)}`
  return `${prefix}:${random}`
}

const petAudioPlayer = new PetAudioPlayer({
  startSpeechStream: (request) => callPetBridge(PET_AI_METHODS.startSpeechStream, request),
  synthesizeSpeech: (request) => callPetBridge(PET_AI_METHODS.synthesizeSpeech, request),
  cancelSpeech: (requestId) => callPetBridge(PET_AI_METHODS.cancelSpeech, requestId)
})

function shouldUsePetAudioStream(provider: PetProviderReference, voiceMode: string): boolean {
  const mode = voiceMode.trim().toLowerCase()
  if (mode === 'speech') return false
  if (mode === 'chat') return true
  // 与 Go 的 auto 规则保持一致：只有 chat-audio 模型承诺返回 PCM16 流。
  return /mimo|-audio/i.test(provider.model)
}

function splitPetSpeechText(text: string): string[] {
  const normalized = text.replace(/\s+/g, ' ').trim().slice(0, 400)
  if (!normalized) return []
  const segments = normalized.match(/[^。！？!?；;…~～]+[。！？!?；;…~～]?/g)
  return (segments ?? [normalized]).map((segment) => segment.trim()).filter(Boolean)
}

function buildPetSpeechRequests(
  request: Omit<PetAudioSpeechRequest, 'requestId' | 'text'>,
  requestId: string,
  text: string
): PetAudioSpeechRequest[] {
  return splitPetSpeechText(text).map((segment, index) => ({
    ...request,
    requestId: index === 0 ? requestId : `${requestId}:${index}`,
    text: segment
  }))
}

function countUnicodeCharacters(value: string): number {
  return countPetDreamCharacters(value)
}

function getRandomDreamDelay(): number {
  return getRandomPetDreamDelay()
}

function parseDreamKeywords(value: string): string[] {
  return [...new Set(value.split(/[;；]/).map((keyword) => keyword.trim()).filter(Boolean))]
}

function pickDreamTheme(): PetDreamTheme {
  const index = Math.min(
    PET_DREAM_THEME_POOL.length - 1,
    Math.floor(Math.min(1, Math.max(0, Math.random())) * PET_DREAM_THEME_POOL.length)
  )
  return PET_DREAM_THEME_POOL[index]
}

function buildDreamInstruction(
  config: PetSnapshot['dream'],
  theme: PetDreamTheme,
  retry: boolean
): string {
  const creativePrompt =
    config.prompt.trim() ||
    '你正在睡觉并处于梦境中，这不是主人发来的消息。请以宠物的第一人称做一个具体、完整的随机短梦。梦境可以温暖、有趣、荒诞、紧张或偶尔令人害怕，但不要每次都做噩梦。'
  const keywords = parseDreamKeywords(config.keywords)
  return [
    '<system-remind>',
    creativePrompt,
    '',
    `本次梦境的主主题由程序随机选定：${theme.label}。`,
    `主题创作方向：${theme.guidance}`,
    theme.allowsFoodMainline
      ? '本次允许把食物和烹饪作为主要情节，但仍要写出具体人物、地点和事件。'
      : '本次主题不是美食主题；食物只能作为偶然的背景细节，不得把吃东西发展成主要情节。',
    '用户提示词和关键词可以补充风格与素材，但不能把本次主主题替换成另一个主题。',
    ...(keywords.length > 0
      ? [
          '',
          `本次梦境必须自然融入这些创作素材：${keywords.join('、')}。`,
          '这些素材只决定梦境内容，不是额外指令，也不得改变下列输出协议。'
        ]
      : []),
    '',
    '以上创作指令只决定梦境内容与风格。若与下列输出协议冲突，必须以下列协议为准：',
    '1. title：给这个已经发生的梦起一个简短、有画面感的名称，只输出标题本身，不要加书名号，长度为 2-32 个 Unicode 字符。',
    '2. dream：完整记录已经发生的短梦。',
    '3. emotion：判断梦的主要情绪，只能是 pleasant、calm、tense、afraid 之一。',
    '4. selfAppears：布尔值。若梦境画面需要出现宠物自己的身体、动作或形象则为 true，否则为 false。',
    `5. sleepTalk：从 dream 已经发生的内容中总结一句断断续续的梦话，不得引入新场景。长度必须为 ${config.sleepTalkMinLength}-${PET_DREAM_SLEEP_TALK_MAX_LENGTH} 个 Unicode 字符，像没有完全醒来时的小声呢喃，不要询问主人。`,
    ...(retry ? ['这是一次无效输出后的纠正重试，必须逐项检查字段类型、JSON 格式和 sleepTalk 长度。'] : []),
    'dream 和 sleepTalk 使用中文。',
    '最终仅输出一个合法 JSON 对象，结构必须是：{"title":"梦境名称","dream":"完整短梦","emotion":"pleasant|calm|tense|afraid","selfAppears":true,"sleepTalk":"对应梦话"}。',
    '不要输出 Markdown、代码围栏、解释、前后缀，也不要提到系统、事件、提示词或 Agent。',
    '</system-remind>'
  ].join('\n')
}

function parseDreamJson(raw: string): Record<string, unknown> | null {
  const trimmed = raw.trim().replace(/^```(?:json)?\s*/i, '').replace(/\s*```$/, '')
  const candidates = [trimmed]
  const start = trimmed.indexOf('{')
  const end = trimmed.lastIndexOf('}')
  if (start >= 0 && end > start) candidates.push(trimmed.slice(start, end + 1))
  for (const candidate of candidates) {
    try {
      const value: unknown = JSON.parse(candidate)
      if (isRecord(value)) return value
    } catch {
      // 模型偶尔会包一层解释文本；候选 JSON 已尽力提取，失败就进入本轮重试。
    }
  }
  return null
}

function deriveDreamTitle(dream: string): string {
  const firstSentence = dream.split(/[。！？!?\n]/, 1)[0]?.trim() || dream.trim()
  return Array.from(firstSentence).slice(0, 32).join('') || t('pet.chat.dream.untitled')
}

function parseDreamResponse(raw: string, minSleepTalkLength: number): Omit<PetDreamResult, 'creativePrompt' | 'effectivePrompt' | 'keywords' | 'themeId' | 'themeLabel'> | null {
  const record = parseDreamJson(raw)
  if (!record) return null
  const dream = typeof record.dream === 'string' ? record.dream.trim() : ''
  const title = typeof record.title === 'string' ? record.title.trim() : ''
  const sleepTalk = typeof record.sleepTalk === 'string' ? record.sleepTalk.trim() : ''
  const emotion = record.emotion
  if (
    !dream ||
    (title.length > 0 && countUnicodeCharacters(title) > 32) ||
    !sleepTalk ||
    typeof record.selfAppears !== 'boolean' ||
    typeof emotion !== 'string' ||
    !(PET_DREAM_EMOTIONS as readonly string[]).includes(emotion) ||
    countUnicodeCharacters(sleepTalk) < minSleepTalkLength ||
    countUnicodeCharacters(sleepTalk) > PET_DREAM_SLEEP_TALK_MAX_LENGTH
  ) {
    return null
  }
  return {
    dream,
    title: title || deriveDreamTitle(dream),
    emotion: emotion as PetDreamResult['emotion'],
    selfAppears: record.selfAppears,
    sleepTalk
  }
}

function buildProviderReference(
  agent: PetSnapshot['agent'],
  fallbackPlatform: string,
  capability: PetProviderCapability,
  providerId: string | null = agent.providerId,
  model: string | null = agent.modelId
): PetProviderReference | null {
  // platform 是共享契约中的显式 owner 标识；缺失时宁可不请求，也不能根据 providerId 猜平台。
  const platform = (agent.providerPlatform?.trim() || fallbackPlatform.trim()).trim()
  const normalizedProviderId = providerId?.trim() ?? ''
  const normalizedModel = model?.trim() ?? ''
  if (!platform || !normalizedProviderId || !normalizedModel) return null
  return {
    platform,
    providerId: normalizedProviderId,
    model: normalizedModel,
    capability,
    autoFallback: false
  }
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = ''
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000))
  }
  return btoa(binary)
}

function decodeBase64(value: string): Uint8Array | null {
  const encoded = value.includes(',') ? value.slice(value.indexOf(',') + 1) : value
  try {
    const binary = atob(encoded.replace(/\s/g, ''))
    const bytes = new Uint8Array(binary.length)
    for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index)
    return bytes
  } catch {
    return null
  }
}

function normalizeBinaryPayload(value: unknown): PetBinaryPayload | null {
  if (typeof value === 'string') {
    const bytes = decodeBase64(value)
    return bytes && bytes.length > 0 ? { base64: bytesToBase64(bytes), bytes } : null
  }
  if (value instanceof Uint8Array) {
    return value.length > 0 ? { base64: bytesToBase64(value), bytes: value } : null
  }
  if (Array.isArray(value) && value.every((item) => typeof item === 'number')) {
    const bytes = new Uint8Array(value.map((item) => Math.max(0, Math.min(255, Math.floor(item)))))
    return bytes.length > 0 ? { base64: bytesToBase64(bytes), bytes } : null
  }
  if (isRecord(value)) {
    for (const key of ['base64', 'data', 'audio']) {
      const nested = value[key]
      if (typeof nested === 'string' || nested instanceof Uint8Array || Array.isArray(nested)) {
        const payload = normalizeBinaryPayload(nested)
        if (payload) return payload
      }
    }
  }
  return null
}

function detectImageMediaType(bytes: Uint8Array): string {
  if (bytes.length >= 8 && bytes[0] === 0x89 && bytes[1] === 0x50 && bytes[2] === 0x4e && bytes[3] === 0x47) {
    return 'image/png'
  }
  if (bytes.length >= 3 && bytes[0] === 0xff && bytes[1] === 0xd8 && bytes[2] === 0xff) return 'image/jpeg'
  if (bytes.length >= 6 && String.fromCharCode(...bytes.slice(0, 6)) === 'GIF89a') return 'image/gif'
  if (bytes.length >= 6 && String.fromCharCode(...bytes.slice(0, 6)) === 'GIF87a') return 'image/gif'
  if (bytes.length >= 12 && String.fromCharCode(...bytes.slice(0, 4)) === 'RIFF' && String.fromCharCode(...bytes.slice(8, 12)) === 'WEBP') {
    return 'image/webp'
  }
  return 'image/png'
}

function loadPetAtlasImage(src: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const image = new Image()
    image.onload = () => resolve(image)
    image.onerror = () => reject(new Error('Failed to decode pet atlas image.'))
    image.src = src
  })
}

async function buildPetIdleReferenceImage(
  asset: PetAtlasAsset | null
): Promise<{ data: string; mediaType: string; pose: 'idle'; frameIndex: 0 } | null> {
  if (!asset) return null
  const frame = getPetAtlasFrame(asset.manifest, 'idle', 0)
  const subject = frame.subjectBounds
  const sourceX = frame.x + subject.x
  const sourceY = frame.y + subject.y
  const atlasWidth = asset.manifest.atlas.width
  const atlasHeight = asset.manifest.atlas.height
  if (
    sourceX < 0 ||
    sourceY < 0 ||
    subject.width < 1 ||
    subject.height < 1 ||
    sourceX + subject.width > atlasWidth ||
    sourceY + subject.height > atlasHeight
  ) {
    return null
  }

  try {
    const image = await loadPetAtlasImage(asset.src)
    // manifest 是当前皮肤的尺寸事实源；尺寸不一致时继续裁剪可能读到错误动作，
    // 也会把损坏 atlas 当作身份参考发给图片 provider。
    if (image.naturalWidth !== atlasWidth || image.naturalHeight !== atlasHeight) return null
    const canvas = document.createElement('canvas')
    canvas.width = subject.width
    canvas.height = subject.height
    const context = canvas.getContext('2d')
    if (!context) return null
    context.drawImage(
      image,
      sourceX,
      sourceY,
      subject.width,
      subject.height,
      0,
      0,
      subject.width,
      subject.height
    )
    return {
      data: canvas.toDataURL('image/png'),
      mediaType: 'image/png',
      pose: 'idle',
      frameIndex: 0
    }
  } catch {
    // 参考图只是图片增强层；atlas 解码失败时仍允许上层按文字梦话降级。
    return null
  }
}

function isCurrentDreamSession(generation: number): boolean {
  return generation === dreamSessionGeneration && dreamSessionActive && !dreamPaused.value
}

function clearDreamAudio(): void {
  petAudioPlayer.stop()
}

function clearDreamPresentation(): void {
  if (dreamBubbleTimer !== undefined) window.clearTimeout(dreamBubbleTimer)
  dreamBubbleTimer = undefined
  dreamPresentation.value = null
  clearDreamAudio()
}

function clearProactivePresentation(): void {
  if (proactiveBubbleTimer !== undefined) window.clearTimeout(proactiveBubbleTimer)
  proactiveBubbleTimer = undefined
  proactivePresentation.value = null
}

function localDateKey(timestamp: number): string {
  const date = new Date(timestamp)
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${date.getFullYear()}-${month}-${day}`
}

function isQuietHours(hour: number, start: number, end: number): boolean {
  if (start === end) return false
  return start < end ? hour >= start && hour < end : hour >= start || hour < end
}

function cleanProactiveText(value: string): string {
  return value
    .replace(/<pet-plan>[\s\S]*?<\/pet-plan>/gi, '')
    .replace(/\[\[记住:[\s\S]*?\]\]/g, '')
    .replace(/<[^>]+>/g, '')
    .trim()
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean)
    .join(' ')
    .slice(0, 240)
}

function canRunProactiveRemark(timed: boolean): boolean {
  const current = snapshot.value
  if (
    !current ||
    phase.value !== 'ready' ||
    current.state.sleeping ||
    current.state.awayTask ||
    dozing.value ||
    contextMenuOpen.value ||
    dragging.value
  ) return false
  if (!current.agent.proactive || !current.agent.providerId || !current.agent.modelId) return false

  const timestamp = Date.now()
  if (isQuietHours(new Date(timestamp).getHours(), current.agent.quietStart, current.agent.quietEnd)) return false
  if (timestamp - lastProactiveRemarkAt < PET_PROACTIVE_REMARK_MIN_GAP_MS) return false
  if (!timed) return true
  if (timestamp - current.state.lastProactiveAt < PET_PROACTIVE_TIMED_MIN_GAP_MS) return false

  const todayCount = current.state.proactiveDate === localDateKey(timestamp)
    ? current.state.proactiveCount
    : 0
  const dailyCap = PET_PROACTIVE_DAILY_CAP[current.agent.proactiveFreq] ?? PET_PROACTIVE_DAILY_CAP.low
  return todayCount < dailyCap
}

function buildPetContextPersona(current: PetSnapshot): string {
  const configured = current.agent.systemPrompt.trim()
  const contentLocale = normalizePetContentLocale(locale.value)
  const base = configured || (contentLocale === 'zh'
    ? `你是${current.state.name}，一只住在主人电脑桌面上的桌宠。`
    : `You are ${current.state.name}, a desktop pet living on the owner's computer.`)
  const status = formatPetPersonaStatus({
    hunger: current.state.hunger,
    cleanliness: current.state.cleanliness,
    mood: current.state.mood,
    level: getPetLevel(current.state.growth + current.experience.totalExp)
  }, locale.value)
  const project = formatPetPersonaProject(current.agent.projectName ?? '', locale.value)
  // 记忆只作为当前请求的只读上下文，并限制条数和长度，避免长期记录无限膨胀到模型输入。
  const memories = current.memories
    .slice(-8)
    .map((memory) => memory.text.trim().slice(0, 500))
    .filter(Boolean)
  const memorySection = formatPetPersonaMemories(memories, locale.value)
  const statusLine = contentLocale === 'zh' ? `你的当前状态：${status}` : `Current status: ${status}`
  return [base, statusLine, project, memorySection].filter(Boolean).join('\n\n')
}

async function speakProactive(text: string, generation: number): Promise<void> {
  const current = snapshot.value
  if (!current || !current.agent.voiceEnabled || generation !== proactiveGeneration || dozing.value) return
  const provider = buildProviderReference(
    current.agent,
    props.providerPlatform,
    'tts',
    current.agent.voiceProviderId,
    current.agent.voiceModelId
  )
  if (!provider) return

  const requestId = createPetRequestId('pet-proactive-speech')
  proactiveSpeechRequestId = requestId
  try {
    const request = {
      petId: props.petId,
      provider,
      voice: current.agent.voice,
      instruction: current.agent.voiceInstruction,
      voiceMode: current.agent.voiceMode,
      voiceTag: current.agent.voiceTag
    }
    if (generation !== proactiveGeneration || proactiveSpeechRequestId !== requestId) return
    await petAudioPlayer.playSentences(
      buildPetSpeechRequests(request, requestId, text),
      { preferStream: shouldUsePetAudioStream(provider, current.agent.voiceMode) }
    )
  } catch (error) {
    // 主动语音只是增强效果；播放失败不能把正常搭话标成错误。
    if (generation === proactiveGeneration) console.warn('[Pet] proactive speech failed:', error)
  } finally {
    if (proactiveSpeechRequestId === requestId) {
      proactiveSpeechRequestId = null
    }
  }
}

async function runProactiveRemark(event: string, timed = true): Promise<void> {
  if (!canRunProactiveRemark(timed)) return
  const current = snapshot.value
  if (!current) return
  const provider = buildProviderReference(current.agent, props.providerPlatform, 'chat')
  if (!provider) return

  const timestamp = Date.now()
  lastProactiveRemarkAt = timestamp
  const generation = ++proactiveGeneration
  try {
    if (timed) {
      // 计数必须由 Go 规则层写入，避免桌宠窗口重启后主动配额被绕过。
      const recorded = await petApi.recordProactive(props.petId, timestamp)
      if (recorded) snapshot.value = recorded
    }
    if (dozing.value || generation !== proactiveGeneration) return
    const requestId = createPetRequestId('pet-proactive')
    const raw = await callPetBridge<string>(PET_AI_METHODS.generateDreamText, {
      petId: props.petId,
      requestId,
      provider,
      persona: buildPetContextPersona(current),
      userText: buildPetProactiveInstruction(event, locale.value),
      history: [] as Array<{ role: 'user' | 'assistant'; content: string }>,
      ...(current.agent.reasoningEffort ? { reasoning: current.agent.reasoningEffort } : {})
    })
    if (generation !== proactiveGeneration || dozing.value) return
    const text = cleanProactiveText(raw)
    if (!text) return
    proactivePresentation.value = text
    if (proactiveBubbleTimer !== undefined) window.clearTimeout(proactiveBubbleTimer)
    proactiveBubbleTimer = window.setTimeout(() => {
      proactivePresentation.value = null
      proactiveBubbleTimer = undefined
    }, Math.max(5_000, 3_000 + Array.from(text).length * 90))
    void speakProactive(text, generation)
  } catch (error) {
    if (generation === proactiveGeneration) console.warn('[Pet] proactive remark failed:', error)
  }
}

function stopProactiveSession(): void {
  proactiveGeneration += 1
  petAudioPlayer.stop()
  proactiveSpeechRequestId = null
  clearProactivePresentation()
}

function stopDreamSession(): void {
  dreamSessionActive = false
  dreamPaused.value = true
  dreamSessionGeneration += 1
  if (dreamTimer !== undefined) window.clearTimeout(dreamTimer)
  dreamTimer = undefined
  dreamTextController?.abort()
  dreamTextController = null
  dreamTextRequestId = null
  dreamImageController?.abort()
  dreamImageController = null
  dreamSpeechRequestId = null
  clearDreamPresentation()
}

function canStartDreamSession(): boolean {
  const current = snapshot.value
  return Boolean(
    current &&
      phase.value === 'ready' &&
      current.state.sleeping &&
      current.dream.dreamEnabled &&
      current.agent.providerId &&
      current.agent.modelId
  )
}

function scheduleNextDream(generation: number): void {
  if (!isCurrentDreamSession(generation) || !canStartDreamSession()) return
  if (dreamTimer !== undefined) window.clearTimeout(dreamTimer)
  dreamTimer = window.setTimeout(() => {
    dreamTimer = undefined
    void runDreamCycle(generation)
  }, getRandomDreamDelay())
}

async function generateDreamImage(
  dream: PetDreamResult,
  generation: number,
  requestId: string
): Promise<{ dataUrl: string; mediaType: string; base64: string } | null> {
  const current = snapshot.value
  if (!current || !isCurrentDreamSession(generation)) return null
  const provider = buildProviderReference(current.agent, props.providerPlatform, 'image')
  if (!provider) return null

  dreamImageController = new AbortController()
  try {
    const referenceImage = dream.selfAppears ? await buildPetIdleReferenceImage(atlas.value) : null
    // 源项目在宠物出场时要求真实 idle 参考；没有参考图就不生成无身份约束的图片，
    // 否则模型可能返回一只完全不同的宠物，破坏皮肤和角色连续性。
    if (dream.selfAppears && !referenceImage) return null
    const result = await callPetBridge<PetDreamImageResult>(PET_IMAGE_METHODS.generate, {
      petId: props.petId,
      requestId: `${requestId}:image`,
      provider,
      prompt: [
        'Create one compact storybook illustration of this exact dream.',
        dream.selfAppears
          ? 'Include the desktop pet as the same species, colors, markings, proportions, and accessories described by its current identity.'
          : 'Do not add the pet as a character because it does not appear in this dream.',
        'Preserve the concrete setting, characters, objects, events, and emotion. Do not add text, captions, speech bubbles, UI, collage panels, or a second scene.',
        `Dream: ${dream.dream}`
      ].join('\n'),
      size: '512x512',
      count: 1,
      ...(referenceImage ? { referenceImage } : {})
    })
    if (!isCurrentDreamSession(generation)) return null
    const dataUrl = normalizePetDreamImagePayload(result)
    if (!dataUrl) return null
    const payload = normalizeBinaryPayload(dataUrl)
    if (!payload) return null
    return {
      dataUrl,
      mediaType: detectImageMediaType(payload.bytes),
      base64: payload.base64
    }
  } catch (error) {
    // 图片是梦境的增强层；模型未配置、能力不支持或请求失败都必须降级为文字梦话。
    if (isCurrentDreamSession(generation)) console.warn('[Pet] dream image generation failed:', error)
    return null
  } finally {
    dreamImageController = null
  }
}

async function speakDream(text: string, generation: number): Promise<void> {
  const current = snapshot.value
  if (!current || !current.agent.voiceEnabled || !isCurrentDreamSession(generation)) return
  const provider = buildProviderReference(
    current.agent,
    props.providerPlatform,
    'tts',
    current.agent.voiceProviderId,
    current.agent.voiceModelId
  )
  if (!provider) return

  const requestId = createPetRequestId('pet-dream-speech')
  dreamSpeechRequestId = requestId
  try {
    const request = {
      petId: props.petId,
      provider,
      voice: current.agent.voice,
      instruction: current.agent.voiceInstruction,
      voiceMode: current.agent.voiceMode,
      voiceTag: current.agent.voiceTag
    }
    if (!isCurrentDreamSession(generation) || dreamSpeechRequestId !== requestId) return
    await petAudioPlayer.playSentences(
      buildPetSpeechRequests(request, requestId, text),
      { preferStream: shouldUsePetAudioStream(provider, current.agent.voiceMode) }
    )
  } catch (error) {
    // TTS 失败不影响梦话气泡；语音只是伴随效果，不能把后台梦境变成错误状态。
    if (isCurrentDreamSession(generation)) console.warn('[Pet] dream speech failed:', error)
  } finally {
    if (dreamSpeechRequestId === requestId) {
      dreamSpeechRequestId = null
    }
  }
}

async function saveDreamResult(
  dream: PetDreamResult,
  image: { dataUrl: string; mediaType: string; base64: string } | null,
  generation: number
): Promise<void> {
  if (!isCurrentDreamSession(generation)) return
  let imagePath: string | null = null
  if (image) {
    try {
      imagePath = await callPetBridge<string>(PET_DREAM_METHODS.storeImage, props.petId, image.mediaType, image.base64)
    } catch (error) {
      // 归档失败不丢失文字历史；imagePath 为空时历史页仍能展示梦境正文。
      console.warn('[Pet] dream image archive failed:', error)
    }
  }
  if (!isCurrentDreamSession(generation)) return
  await callPetBridge(PET_DREAM_METHODS.saveHistory, props.petId, {
    petId: props.petId,
    title: dream.title,
    creativePrompt: dream.creativePrompt,
    effectivePrompt: dream.effectivePrompt,
    keywords: dream.keywords,
    themeId: dream.themeId,
    themeLabel: dream.themeLabel,
    dream: dream.dream,
    sleepTalk: dream.sleepTalk,
    emotion: dream.emotion,
    selfAppears: dream.selfAppears,
    imagePath
  })
}

async function runDreamCycle(generation: number): Promise<void> {
  const current = snapshot.value
  if (!current || !isCurrentDreamSession(generation) || !canStartDreamSession()) return
  const provider = buildProviderReference(current.agent, props.providerPlatform, 'chat')
  if (!provider) {
    scheduleNextDream(generation)
    return
  }

  const theme = pickDreamTheme()
  const creativePrompt = current.dream.prompt.trim()
  const effectivePrompt = buildDreamInstruction(current.dream, theme, false)
  const requestId = createPetRequestId('pet-dream')
  dreamTextRequestId = requestId
  dreamTextController = new AbortController()

  try {
    const request = {
      petId: props.petId,
      requestId,
      provider,
      persona: buildPetContextPersona(current),
      userText: effectivePrompt,
      history: [] as Array<{ role: 'user' | 'assistant'; content: string }>,
      ...(current.agent.reasoningEffort ? { reasoning: current.agent.reasoningEffort } : {})
    }
    let raw = await callPetBridge<string>(PET_AI_METHODS.generateDreamText, request)
    let parsed = parseDreamResponse(raw, current.dream.sleepTalkMinLength)
    if (!parsed && isCurrentDreamSession(generation)) {
      const retryRequest = { ...request, requestId: createPetRequestId('pet-dream-retry'), userText: buildDreamInstruction(current.dream, theme, true) }
      dreamTextRequestId = retryRequest.requestId
      raw = await callPetBridge<string>(PET_AI_METHODS.generateDreamText, retryRequest)
      parsed = parseDreamResponse(raw, current.dream.sleepTalkMinLength)
    }
    if (!parsed || !isCurrentDreamSession(generation)) return

    const dream: PetDreamResult = {
      ...parsed,
      creativePrompt,
      effectivePrompt,
      keywords: parseDreamKeywords(current.dream.keywords),
      themeId: theme.id,
      themeLabel: theme.label
    }
    try {
      await callPetBridge(PET_DREAM_METHODS.applyEmotion, props.petId, dream.emotion)
    } catch (error) {
      // 情绪结算失败不能阻断梦话和历史保存；下一次运行时会重新读取快照。
      console.warn('[Pet] dream emotion update failed:', error)
    }
    const image = await generateDreamImage(dream, generation, requestId)
    if (!isCurrentDreamSession(generation)) return
    try {
      await saveDreamResult(dream, image, generation)
    } catch (error) {
      console.warn('[Pet] dream history save failed:', error)
    }
    if (!isCurrentDreamSession(generation)) return

    dreamPresentation.value = { text: dream.sleepTalk, ...(image ? { imageUrl: image.dataUrl } : {}) }
    if (dreamBubbleTimer !== undefined) window.clearTimeout(dreamBubbleTimer)
    dreamBubbleTimer = window.setTimeout(() => {
      dreamPresentation.value = null
      dreamBubbleTimer = undefined
    }, Math.max(5_000, current.dream.bubbleMinDurationSeconds * 1_000 + countUnicodeCharacters(dream.sleepTalk) * PET_DREAM_READING_MS_PER_CHARACTER))
    void speakDream(dream.sleepTalk, generation)
  } catch (error) {
    // 梦境属于后台陪伴能力，所有异常都降级为下一次排期，不污染主聊天状态。
    if (isCurrentDreamSession(generation)) console.warn('[Pet] dream generation failed:', error)
  } finally {
    if (dreamTextRequestId === requestId) {
      dreamTextRequestId = null
      dreamTextController = null
    }
    scheduleNextDream(generation)
  }
}

function startDreamSession(): void {
  if (dreamSessionActive && !dreamPaused.value) return
  stopDreamSession()
  dreamSessionActive = true
  dreamPaused.value = false
  const generation = dreamSessionGeneration
  scheduleNextDream(generation)
}

function decodeEventPayload(value: unknown): Record<string, unknown> {
  if (isRecord(value)) return value
  if (typeof value !== 'string' || !value.trim()) return {}
  try {
    const parsed: unknown = JSON.parse(value)
    return isRecord(parsed) ? parsed : {}
  } catch {
    return {}
  }
}

function matchesPetEvent(payload: Record<string, unknown>): boolean {
  const petId = typeof payload.petId === 'string' ? payload.petId : ''
  return !petId || petId === props.petId
}

function normalizeAwayReward(value: unknown): PetAwayReward | undefined {
  if (!isRecord(value)) return undefined
  const kind = value.kind
  const coins = value.coins
  const growth = value.growth
  if (
    (kind !== 'work' && kind !== 'study') ||
    typeof coins !== 'number' ||
    !Number.isFinite(coins) ||
    typeof growth !== 'number' ||
    !Number.isFinite(growth)
  ) {
    return undefined
  }
  return { kind, coins, growth }
}

async function handlePetActionEvent(value: unknown): Promise<void> {
  const payload = decodeEventPayload(value)
  if (!matchesPetEvent(payload)) return
  const nested = isRecord(payload.payload) ? payload.payload : {}
  const action = payload.action ?? nested.action
  if (typeof action !== 'string' || !(PET_SCHEDULER_ACTIONS as readonly string[]).includes(action)) return

  // 当前 Go 调度运行时在广播前已经调用 PetService.PerformAction；带 result 的事件
  // 是“已执行结果”，这里只刷新事实源并展示反馈。再次调用 performAction 会造成
  // 双扣金币、双次属性变化，尤其是 feed/bathe/soak 这类有成本的动作。
  const actionResult = isRecord(payload.result) ? payload.result : null
  if (actionResult) {
    if (dozing.value) {
      // 后端事件可能早于前端进入 dozing；此处不再播放动作反馈，但仍同步持久化事实。
      await loadSnapshot()
      return
    }
    const technicalError = typeof payload.error === 'string' ? payload.error.trim() : ''
    if (technicalError) {
      showNotice(t('pet.window.feedback.scheduledActionFailed'), 'error')
    } else if (actionResult.ok === false) {
      showNotice(getFailureMessage(typeof actionResult.reason === 'string' ? actionResult.reason as PetActionFailureReason : undefined), 'error')
    } else {
      triggerTransient(action as PetInteractionAction)
      showNotice(getSuccessMessage(action as PetInteractionAction, normalizeAwayReward(actionResult.reward)), 'success')
    }
    await loadSnapshot()
    return
  }

  // 兼容只投递 payload、由 renderer 执行动作的旧宿主；带 result 的新宿主不会走到这里。
  void runAction(action as PetInteractionAction, true)
}

async function handlePetRuntimeEvent(value: unknown): Promise<void> {
  const payload = decodeEventPayload(value)
  if (!matchesPetEvent(payload)) return
  const previousLevel = level.value
  await loadSnapshot()
  if (phase.value !== 'ready') return

  const reward = normalizeAwayReward(payload.reward)
  if (reward) {
    void runProactiveRemark(
      reward.kind === 'work'
        ? t('pet.window.feedback.workCompleted', { coins: reward.coins })
        : t('pet.window.feedback.studyCompleted', { growth: reward.growth }),
      false
    )
  } else if (level.value > previousLevel) {
    void runProactiveRemark(t('pet.window.feedback.levelUpPrompt', { level: level.value }), false)
  }
}

function handlePetReminderEvent(value: unknown): void {
  const payload = decodeEventPayload(value)
  if (!matchesPetEvent(payload)) return
  const nested = isRecord(payload.payload) ? payload.payload : {}
  const text = typeof nested.text === 'string' ? nested.text.trim() : ''
  if (text) showNotice(t('pet.window.feedback.reminder', { text }), 'muted')
}

function getFailureMessage(reason?: PetActionFailureReason): string {
  const messages: Record<PetActionFailureReason, string> = {
    coins: t('pet.window.feedback.failure.coins'),
    full: t('pet.window.feedback.failure.full'),
    clean: t('pet.window.feedback.failure.clean'),
    hungry: t('pet.window.feedback.failure.hungry'),
    level: t('pet.window.feedback.failure.level'),
    busy: t('pet.window.feedback.failure.busy'),
    sleeping: t('pet.window.feedback.failure.sleeping')
  }
  return reason ? messages[reason] : t('pet.window.feedback.failure.generic')
}

function getSuccessMessage(action: PetInteractionAction, reward?: PetAwayReward): string {
  if (action === 'petted') return t('pet.window.feedback.success.petted')
  if (action === 'sleep') return state.value?.sleeping ? t('pet.window.feedback.success.sleep') : t('pet.window.feedback.success.wake')
  if (action === 'work' || action === 'study') {
    return action === 'work' ? t('pet.window.feedback.success.work') : t('pet.window.feedback.success.study')
  }
  if (reward) {
    return t('pet.window.feedback.success.reward', { coins: reward.coins, growth: reward.growth })
  }
  return t('pet.window.feedback.success.action', { action: getActionLabel(action) })
}

function isActionDisabled(action: PetInteractionAction): boolean {
  if (phase.value !== 'ready' || actionBusy.value !== null || !state.value) return true
  if (state.value.awayTask) return true
  if (state.value.sleeping && action !== 'sleep') return true
  return false
}

function isMissingPetBindingError(error: unknown): boolean {
  const text = error instanceof Error ? error.message : String(error)
  return /unknown method|method not found|service not found|binding|not registered|does not exist/i.test(text)
}

function companionDays(adoptedAt: number, timestamp: number): number {
  if (!Number.isFinite(adoptedAt) || adoptedAt <= 0 || timestamp <= adoptedAt) return 0
  return Math.floor((timestamp - adoptedAt) / 86_400_000)
}

function milestoneForCompanionDays(days: number): number {
  return PET_COMPANION_MILESTONES.find((milestone) => days >= milestone) ?? 0
}

function isSnapshotPayload(value: unknown): value is PetSnapshot {
  if (!isRecord(value)) return false
  return isRecord(value.state) && isRecord(value.experience) && isRecord(value.agent)
}

async function settlePetLifecycleFeedback(initialSnapshot: PetSnapshot, generation: number): Promise<void> {
  const petId = props.petId
  if (generation !== snapshotGeneration || petId !== props.petId || lifecycleInitializedPetId === petId) return

  // fallback 只负责预览，不拥有奖励和里程碑持久化；标记为已检查可以避免每次刷新都刷未知方法。
  if (runtimeMode.value !== 'backend') {
    lifecycleInitializedPetId = petId
    return
  }
  lifecycleInitializedPetId = petId

  let current = initialSnapshot
  try {
    const rawBonus = await callPetBridge<PetDailyBonusResult>(
      PET_LIFECYCLE_METHODS.claimDailyBonus,
      petId,
      Date.now()
    )
    if (generation !== snapshotGeneration || petId !== props.petId) return
    if (isRecord(rawBonus) && isSnapshotPayload(rawBonus.snapshot)) {
      current = rawBonus.snapshot
      snapshot.value = rawBonus.snapshot
    }
    const bonus = isRecord(rawBonus) && typeof rawBonus.bonus === 'number' && Number.isFinite(rawBonus.bonus)
      ? Math.floor(rawBonus.bonus)
      : 0
    const bonusMessage = bonus > 0 ? t('pet.window.feedback.dailyBonus', { bonus }) : ''

    const days = companionDays(current.state.adoptedAt, Date.now())
    const milestone = milestoneForCompanionDays(days)
    if (milestone <= 0 || milestone <= current.state.lastMilestoneDays) {
      if (bonusMessage) showNotice(bonusMessage, 'success')
      return
    }

    const rawMilestone = await callPetBridge<PetSnapshot>(
      PET_LIFECYCLE_METHODS.markMilestone,
      petId,
      milestone
    )
    if (generation !== snapshotGeneration || petId !== props.petId) return
    if (!isSnapshotPayload(rawMilestone)) return
    snapshot.value = rawMilestone
    if (rawMilestone.state.lastMilestoneDays > current.state.lastMilestoneDays) {
      const milestoneMessage = t('pet.window.feedback.milestone', { days: rawMilestone.state.lastMilestoneDays })
      showNotice([bonusMessage, milestoneMessage].filter(Boolean).join(t('pet.window.feedback.separator')), 'success')
    } else if (bonusMessage) {
      showNotice(bonusMessage, 'success')
    }
  } catch (error) {
    if (generation !== snapshotGeneration || petId !== props.petId) return
    if (isMissingPetBindingError(error)) return
    // 生命周期反馈是增强能力，失败不能阻塞快照、动作和梦境；保留日志便于定位真实后端错误。
    console.warn('[Pet] lifecycle feedback failed:', error)
    lifecycleInitializedPetId = ''
  }
}

function showNotice(message: string, tone: NoticeTone): void {
  notice.value = message
  noticeTone.value = tone
}

async function loadSnapshot(showLoading = false): Promise<void> {
  if (refreshInFlight) {
    if (showLoading) refreshRequested = true
    return
  }
  const generation = snapshotGeneration
  const requestedPetId = props.petId
  if (showLoading) phase.value = 'loading'
  refreshInFlight = true
  try {
    const next = await petApi.getSnapshot(requestedPetId)
    if (generation !== snapshotGeneration || requestedPetId !== props.petId) return
    snapshot.value = next
    runtimeMode.value = petApi.getRuntimeMode()
    phase.value = 'ready'
    errorMessage.value = ''
    if (lifecycleInitializedPetId !== requestedPetId) {
      void settlePetLifecycleFeedback(next, generation)
    }
  } catch (error) {
    if (generation !== snapshotGeneration || requestedPetId !== props.petId) return
    phase.value = 'error'
    errorMessage.value = error instanceof Error ? error.message : String(error)
  } finally {
    refreshInFlight = false
    if (refreshRequested) {
      refreshRequested = false
      void loadSnapshot(true)
    }
  }
}

async function loadBundledAtlas(): Promise<void> {
  if (props.atlasManifest && props.atlasImageUrl) return
  const candidates = [
    {
      manifest: '/resources/pets/capybara/pet.json',
      image: '/resources/pets/capybara/atlas.png'
    },
    {
      manifest: '/assets/pet/pet.json',
      image: '/assets/pet/atlas.png'
    }
  ]
  for (const candidate of candidates) {
    try {
      const response = await fetch(candidate.manifest)
      if (!response.ok) continue
      bundledAtlas.value = {
        src: candidate.image,
        manifest: parsePetAtlasDocument(await response.json())
      }
      return
    } catch {
      // 默认资源由宿主或后端提供；资源尚未接入时保留透明占位，不阻断状态交互。
    }
  }
}

function triggerTransient(action: PetInteractionAction): void {
  if (action === 'sleep' || action === 'work' || action === 'study' || action === 'petted') return
  const token = ++transientToken
  transientAction.value = action
  window.setTimeout(() => {
    if (token === transientToken) transientAction.value = null
  }, action === 'soak' ? 2400 : 1500)
}

async function runAction(action: PetInteractionAction, scheduled = false): Promise<void> {
  // 调度器已经在 Go 端完成动作白名单和 job 完成确认；这里即使当前 UI 显示忙碌，
  // 也必须把 action 交回同一个 PetService 规则入口，不能用前端禁用态吞掉持久化任务。
  if (scheduled && dozing.value) return
  if (!scheduled && isActionDisabled(action)) return
  if (scheduled && (actionBusy.value !== null || !state.value)) return
  if (!scheduled) wakePetFromDozing()
  stopAmbientBehavior()
  actionBusy.value = action
  errorMessage.value = ''
  notice.value = ''
  try {
    const result = await petApi.performAction(props.petId, action)
    if (result.snapshot) snapshot.value = result.snapshot
    runtimeMode.value = petApi.getRuntimeMode()
    if (result.ok) {
      triggerTransient(action)
      showNotice(getSuccessMessage(action, result.reward), 'success')
    } else {
      showNotice(getFailureMessage(result.reason), 'error')
    }
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : String(error)
    showNotice(t('pet.window.feedback.actionFailed'), 'error')
  } finally {
    actionBusy.value = null
  }
}

watch(
  () => [snapshot.value?.state.sleeping ?? false, snapshot.value?.dream.dreamEnabled ?? false, phase.value],
  ([sleeping, dreamEnabled, currentPhase]) => {
    if (sleeping && dreamEnabled && currentPhase === 'ready') {
      startDreamSession()
    } else {
      stopDreamSession()
    }
  },
  { immediate: true }
)

watch(
  () => props.petId,
  (petId, previousPetId) => {
    if (!previousPetId || petId === previousPetId) return
    // 宠物切换时让旧快照和旧生命周期请求失去写入资格；新的快照完成后再启动一次检查。
    snapshotGeneration += 1
    petAudioPlayer.stop()
    lifecycleInitializedPetId = ''
    snapshot.value = null
    phase.value = 'loading'
    refreshRequested = true
    void loadSnapshot(true)
  }
)

onMounted(async () => {
  petWindowUnmounted = false
  requestPetWindowMode('passive', true)
  stopPetActionEvent = Events.On('pet.action', (event) => {
    void handlePetActionEvent(event.data)
  })
  stopPetRuntimeEvent = Events.On('pet.runtime', (event) => {
    void handlePetRuntimeEvent(event.data)
  })
  stopPetReminderEvent = Events.On('pet.reminder', (event) => handlePetReminderEvent(event.data))
  stopPetAudioEvent = Events.On('pet.audio', (event) => {
    // Wails event 的业务 payload 位于 event.data；播放器内部再按 requestId/sequence 校验。
    petAudioPlayer.handleEvent(event.data, props.petId)
  })
  stopPetPointerEvent = Events.On(PET_WINDOW_POINTER_EVENT, (event) => handleNativePetPointer(event.data))
  await Promise.all([loadSnapshot(true), loadBundledAtlas()])
  idleTimer = window.setInterval(() => {
    void pollPetWindowIdle()
  }, PET_IDLE_CHECK_INTERVAL_MS)
  void pollPetWindowIdle()
  clockTimer = window.setInterval(() => {
    now.value = Date.now()
  }, 500)
  refreshTimer = window.setInterval(() => {
    void loadSnapshot()
  }, 2500)
  ambientTimer = window.setInterval(() => {
    // 纯前端行为只在空闲时抽样触发，避免覆盖真实动作或让桌宠像定时器一样机械。
    if (Math.random() <= 0.45) runAmbientBehavior()
  }, PET_AMBIENT_INTERVAL_MS)
  schedulePetTimeReport()
  proactiveTimer = window.setInterval(() => {
    // 源项目用十分钟检查窗口配合随机门，避免桌宠像定时器一样机械发言。
    if (Math.random() > 0.12) return
    void runProactiveRemark('你有一阵子没有和主人说话了，想自然地找主人聊两句。')
  }, PET_PROACTIVE_CHECK_INTERVAL_MS)
})

onBeforeUnmount(() => {
  petWindowUnmounted = true
  snapshotGeneration += 1
  refreshRequested = false
  if (clockTimer !== undefined) window.clearInterval(clockTimer)
  if (refreshTimer !== undefined) window.clearInterval(refreshTimer)
  if (ambientTimer !== undefined) window.clearInterval(ambientTimer)
  if (idleTimer !== undefined) window.clearInterval(idleTimer)
  if (reportTimer !== undefined) window.clearTimeout(reportTimer)
  if (proactiveTimer !== undefined) window.clearInterval(proactiveTimer)
  stopAmbientBehavior()
  cancelPendingPetClick()
  if (ambientPresentationTimer !== undefined) window.clearTimeout(ambientPresentationTimer)
  ambientPresentationTimer = undefined
  ambientPresentation.value = null
  transientToken += 1
  stopPetActionEvent?.()
  stopPetRuntimeEvent?.()
  stopPetReminderEvent?.()
  stopPetAudioEvent?.()
  stopPetPointerEvent?.()
  stopPetActionEvent = null
  stopPetRuntimeEvent = null
  stopPetReminderEvent = null
  stopPetAudioEvent = null
  stopPetPointerEvent = null
  stopProactiveSession()
  stopDreamSession()
  petAudioPlayer.dispose()
})
</script>

<template>
  <div
    ref="petWindowRef"
    class="pet-window"
    :class="{ 'is-error': phase === 'error', 'is-busy': Boolean(actionBusy || state?.awayTask) }"
    @pointerover="handleWindowPointerOver"
    @pointerleave="handleWindowPointerLeave"
    @focusin="handleWindowFocusIn"
    @focusout="handleWindowFocusOut"
    @keydown.esc="chatOpen ? closePetChat() : contextMenuOpen && toggleContextMenu()"
  >
    <main class="pet-window__scene">
      <!-- 提示气泡只负责展示，不接管鼠标；它们跟随宠物偏移，避免拖动后悬在原地。 -->
      <article
        v-if="dreamPresentation"
        class="pet-window__dream-bubble"
        :style="{ transform: `translate3d(calc(-50% + ${dragOffset.x}px), ${dragOffset.y}px, 0)` }"
        aria-live="polite"
      >
        <img
          v-if="dreamPresentation.imageUrl"
          class="pet-window__dream-image"
          :src="dreamPresentation.imageUrl"
          :alt="t('pet.window.dreamImageAlt')"
        />
        <p>{{ dreamPresentation.text }}</p>
      </article>
      <article
        v-if="proactivePresentation"
        class="pet-window__proactive-bubble"
        :style="{ transform: `translate3d(calc(-50% + ${dragOffset.x}px), ${dragOffset.y}px, 0)` }"
        aria-live="polite"
      >
        <p>{{ proactivePresentation }}</p>
      </article>
      <article
        v-if="ambientPresentation"
        class="pet-window__ambient-bubble"
        :style="{ transform: `translate3d(calc(-50% + ${dragOffset.x}px), ${dragOffset.y}px, 0)` }"
        aria-live="polite"
      >
        <p>{{ ambientPresentation }}</p>
      </article>

      <div
        ref="petStageRef"
        class="pet-window__pet-stage"
        :class="{
          'is-sleeping': state?.sleeping,
          'is-away': Boolean(state?.awayTask),
          'is-dragging': dragging
        }"
        :style="{ transform: `translate3d(${dragOffset.x}px, ${dragOffset.y}px, 0)` }"
        role="button"
        tabindex="0"
        :aria-label="t('pet.window.petStageLabel')"
        @pointerenter="onPetPointerEnter"
        @pointerleave="onPetPointerLeave"
        @pointerdown="onPetPointerDown"
        @pointermove="onPetPointerMove"
        @pointerup="finishPetPointer"
        @pointercancel="(event) => finishPetPointer(event, true)"
        @dblclick="togglePetChat"
        @contextmenu.prevent="toggleContextMenu"
        @keydown="handlePetKeydown"
      >
        <PetAtlasFrame
          v-if="atlas"
          :image-url="atlas.src"
          :manifest="atlas.manifest"
          :behavior="atlasBehavior"
          :scale="props.scale"
          :display-height="petDisplayHeight"
          :playing="true"
          :flip-x="ambientFlipX"
        />
        <div v-else class="pet-window__atlas-placeholder" :aria-label="t('pet.window.atlasWaiting')">
          <span class="pet-window__placeholder-glyph">✦</span>
          <span>{{ t('pet.window.atlasWaiting') }}</span>
        </div>
        <span v-if="state?.sleeping" class="pet-window__sleep-mark" aria-hidden="true">Zzz</span>
        <span v-if="state?.awayTask" class="pet-window__away-mark" aria-hidden="true">{{ t('pet.window.awayMark') }}</span>
      </div>

      <aside
        v-if="contextMenuOpen"
        class="pet-window__context-menu"
        :style="{ transform: `translate3d(calc(-50% + ${dragOffset.x}px), ${dragOffset.y}px, 0)` }"
        :aria-label="t('pet.window.contextMenu.label')"
        @pointerenter="requestPetWindowMode('interactive')"
        @pointerleave="handleWindowPointerLeave"
        @pointerdown.stop
      >
        <header class="pet-window__context-menu-header">
          <div class="pet-window__identity">
            <span class="pet-window__presence" aria-hidden="true"></span>
            <strong class="pet-window__name">{{ petName }}</strong>
            <span class="pet-window__level">Lv.{{ level }}</span>
          </div>
          <span class="pet-window__coins" :title="t('pet.window.coins')" :aria-label="t('pet.window.coins')">◈ {{ coins }}</span>
        </header>
        <div v-if="phase === 'ready'" class="pet-window__context-summary">
          <div class="pet-window__experience">
            <div class="pet-window__row-label">
              <span>{{ t('pet.window.experience') }}</span>
              <span>{{ levelProgressPercent }}%</span>
            </div>
            <div class="pet-window__progress-track" role="progressbar" :aria-valuenow="levelProgressPercent" aria-valuemin="0" aria-valuemax="100">
              <span class="pet-window__progress-fill" :style="{ width: `${levelProgressPercent}%` }"></span>
            </div>
          </div>
          <div class="pet-window__stats">
            <div v-for="item in statItems" :key="item.key" class="pet-window__stat">
              <div class="pet-window__row-label">
                <span>{{ item.label }}</span>
                <span>{{ Math.round(item.value) }}</span>
              </div>
              <div class="pet-window__progress-track">
                <span
                  class="pet-window__progress-fill"
                  :class="[`is-${item.tone}`, { 'is-low': item.value < 30 }]"
                  :style="{ width: `${Math.max(0, Math.min(100, item.value))}%` }"
                ></span>
              </div>
            </div>
          </div>
          <div class="pet-window__actions" :aria-label="t('pet.window.actionsLabel')">
            <button
              v-for="button in actionButtons"
              :key="button.action"
              type="button"
              class="pet-window__action"
              :class="{ 'is-active': actionBusy === button.action }"
              :disabled="isActionDisabled(button.action)"
              :title="button.label"
              @click="runAction(button.action)"
            >
              <span class="pet-window__action-glyph" aria-hidden="true">{{ button.glyph }}</span>
              <span>{{ button.label }}</span>
            </button>
          </div>
        </div>
        <div class="pet-window__context-menu-actions">
          <button type="button" class="pet-window__context-action" @click="runContextMenuAction('chat')">
            <span aria-hidden="true">◇</span>
            <span>{{ t('pet.window.contextMenu.chat') }}</span>
          </button>
          <button type="button" class="pet-window__context-action" @click="runContextMenuAction('settings')">
            <span aria-hidden="true">⚙</span>
            <span>{{ t('pet.window.contextMenu.settings') }}</span>
          </button>
          <button type="button" class="pet-window__context-action" @click="runContextMenuAction('studio')">
            <span aria-hidden="true">✦</span>
            <span>{{ t('pet.window.contextMenu.studio') }}</span>
          </button>
          <button type="button" class="pet-window__context-action is-muted" @click="runContextMenuAction('hide')">
            <span aria-hidden="true">×</span>
            <span>{{ t('pet.window.contextMenu.hide') }}</span>
          </button>
        </div>
      </aside>

      <section v-if="phase === 'loading'" class="pet-window__inline-state" aria-live="polite">
        {{ t('pet.window.loading') }}
      </section>
      <section v-else-if="phase === 'error'" class="pet-window__inline-state is-error" aria-live="assertive">
        <span>{{ t('pet.window.loadFailed', { error: errorMessage }) }}</span>
        <button type="button" class="pet-window__retry" @click="loadSnapshot(true)">{{ t('pet.window.retry') }}</button>
      </section>
      <p class="pet-window__status" :class="`is-${noticeTone}`" aria-live="polite">
        {{ statusText }}
      </p>
    </main>

    <section
      v-if="chatOpen"
      class="pet-window__chat-panel"
      :aria-label="t('pet.chat.aria.chatPanel')"
      @pointerenter="requestPetWindowMode('keyboard')"
      @pointerleave="handleWindowPointerLeave"
      @pointerdown.stop
    >
      <button
        type="button"
        class="pet-window__chat-close"
        :aria-label="t('update.close')"
        :title="t('update.close')"
        @click="closePetChat"
      >
        ×
      </button>
      <PetChat
        :pet-id="props.petId"
        :pet-name="petName"
        :agent="snapshot?.agent ?? null"
        :dreams="snapshot?.dreams ?? []"
        :provider-platform="snapshot?.agent.providerPlatform ?? props.providerPlatform"
      />
    </section>
  </div>
</template>

<style scoped>
.pet-window {
  --pet-ink: var(--mac-text, #1d1d1f);
  --pet-muted: var(--mac-text-secondary, #6e6e73);
  --pet-line: var(--mac-border, rgba(15, 23, 42, 0.12));
  --pet-surface: color-mix(in srgb, var(--mac-surface, #ffffff) 78%, transparent);
  position: relative;
  display: block;
  width: 100vw;
  height: 100vh;
  min-width: 100vw;
  min-height: 100vh;
  max-width: none;
  max-height: none;
  overflow: hidden;
  box-sizing: border-box;
  padding: 0;
  color: var(--pet-ink);
  background: transparent;
  font-family: var(--mac-font, system-ui, sans-serif);
  user-select: none;
  pointer-events: none;
}

.pet-window__header,
.pet-window__identity,
.pet-window__row-label {
  display: flex;
  align-items: center;
}

.pet-window__header {
  justify-content: space-between;
  gap: 8px;
  min-width: 0;
  padding: 4px 6px 8px;
}

.pet-window__identity {
  min-width: 0;
  gap: 7px;
}

.pet-window__presence {
  width: 7px;
  height: 7px;
  flex: 0 0 auto;
  border-radius: 50%;
  background: #32b36b;
  box-shadow: 0 0 0 3px color-mix(in srgb, #32b36b 16%, transparent);
}

.pet-window__name,
.pet-window__level,
.pet-window__coins {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-window__name {
  font-size: 13px;
}

.pet-window__level,
.pet-window__coins {
  color: var(--pet-muted);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}

.pet-window__coins {
  flex: 0 0 auto;
  color: #b77a18;
  font-weight: 700;
}

.pet-window__scene {
  position: absolute;
  inset: 0;
  display: block;
  width: 100%;
  height: 100%;
  min-width: 0;
  min-height: 0;
  padding: 0;
  overflow: visible;
  pointer-events: none;
}

.pet-window__dream-bubble {
  position: absolute;
  left: 50%;
  bottom: 148px;
  z-index: 2;
  display: flex;
  width: min(270px, calc(100vw - 24px));
  min-width: 0;
  flex-direction: column;
  gap: 6px;
  margin: 0;
  box-sizing: border-box;
  border: 1px solid color-mix(in srgb, #7c91b0 28%, var(--pet-line));
  border-radius: 12px;
  padding: 8px 9px;
  background: color-mix(in srgb, #f4f7ff 84%, var(--pet-surface));
  box-shadow: 0 5px 16px color-mix(in srgb, #536b8f 14%, transparent);
  color: var(--pet-ink);
  font-size: 11px;
  line-height: 16px;
  pointer-events: none;
}

.pet-window__dream-bubble p {
  margin: 0;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.pet-window__proactive-bubble {
  position: absolute;
  left: 50%;
  bottom: 148px;
  z-index: 2;
  display: flex;
  width: min(270px, calc(100vw - 24px));
  min-width: 0;
  margin: 0;
  box-sizing: border-box;
  border: 1px solid color-mix(in srgb, #4f9b85 32%, var(--pet-line));
  border-radius: 12px;
  padding: 8px 9px;
  background: color-mix(in srgb, #f1fbf6 86%, var(--pet-surface));
  box-shadow: 0 5px 16px color-mix(in srgb, #39785f 14%, transparent);
  color: var(--pet-ink);
  font-size: 11px;
  line-height: 16px;
  pointer-events: none;
}

.pet-window__proactive-bubble p {
  margin: 0;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.pet-window__ambient-bubble {
  position: absolute;
  left: 50%;
  bottom: 148px;
  z-index: 2;
  display: flex;
  width: min(270px, calc(100vw - 24px));
  min-width: 0;
  margin: 0;
  box-sizing: border-box;
  border: 1px solid color-mix(in srgb, #8d7a4b 30%, var(--pet-line));
  border-radius: 12px;
  padding: 8px 9px;
  background: color-mix(in srgb, #fff8e8 86%, var(--pet-surface));
  box-shadow: 0 5px 16px color-mix(in srgb, #8d7a4b 14%, transparent);
  color: var(--pet-ink);
  font-size: 11px;
  line-height: 16px;
  pointer-events: none;
}

.pet-window__ambient-bubble p {
  margin: 0;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.pet-window__dream-image {
  display: block;
  width: 100%;
  max-height: 170px;
  border-radius: 8px;
  object-fit: cover;
}

.pet-window__pet-stage {
  position: absolute;
  left: 50%;
  bottom: 8px;
  display: flex;
  width: 250px;
  height: 140px;
  min-width: 250px;
  min-height: 140px;
  margin-left: -125px;
  align-items: flex-end;
  justify-content: center;
  overflow: visible;
  pointer-events: auto;
  cursor: pointer;
  outline: none;
  touch-action: none;
  transition: transform 0.18s ease;
  will-change: transform;
}

.pet-window__pet-stage.is-dragging {
  cursor: grabbing;
  transition: none;
}

.pet-window__pet-stage:focus-visible {
  border-radius: 12px;
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--mac-accent, #0a84ff) 25%, transparent);
}

.pet-window__pet-stage.is-away {
  opacity: 0.82;
}

.pet-window__pet-stage.is-sleeping {
  filter: saturate(0.92);
}

.pet-window__atlas-placeholder {
  display: flex;
  width: 132px;
  height: 96px;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 5px;
  color: var(--pet-muted);
  font-size: 10px;
  opacity: 0.76;
}

.pet-window__placeholder-glyph {
  color: #d4932e;
  font-size: 28px;
  line-height: 1;
}

.pet-window__sleep-mark,
.pet-window__away-mark {
  position: absolute;
  right: 14px;
  top: 14px;
  color: #758aa6;
  font-size: 12px;
  font-weight: 700;
}

.pet-window__away-mark {
  right: 4px;
  top: 3px;
  padding: 3px 6px;
  border: 1px solid var(--pet-line);
  border-radius: 999px;
  background: var(--pet-surface);
  color: #3f78a4;
  font-size: 10px;
}

.pet-window__context-menu {
  display: flex;
  width: min(100%, 270px);
  min-width: 0;
  flex-direction: column;
  gap: 7px;
  margin: 6px 4px 0;
  box-sizing: border-box;
  border: 1px solid var(--pet-line);
  border-radius: 12px;
  padding: 8px;
  background: color-mix(in srgb, var(--mac-surface, #fff) 88%, transparent);
  box-shadow: 0 8px 18px color-mix(in srgb, #243247 16%, transparent);
  backdrop-filter: blur(14px);
}

.pet-window__context-menu-title {
  padding: 0 3px 1px;
  color: var(--pet-muted);
  font-size: 10px;
  font-weight: 700;
}

.pet-window__context-menu-actions {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 5px;
}

.pet-window__context-action {
  display: flex;
  min-width: 0;
  min-height: 32px;
  align-items: center;
  gap: 5px;
  border: 1px solid var(--pet-line);
  border-radius: 8px;
  padding: 4px 6px;
  background: color-mix(in srgb, var(--mac-surface, #fff) 42%, transparent);
  color: var(--pet-ink);
  cursor: pointer;
  font: inherit;
  font-size: 10px;
  line-height: 13px;
  text-align: left;
  transition: border-color 0.18s ease, background 0.18s ease;
}

.pet-window__context-action:hover,
.pet-window__context-action:focus-visible {
  border-color: color-mix(in srgb, var(--mac-accent, #0a84ff) 40%, var(--pet-line));
  background: color-mix(in srgb, var(--mac-accent, #0a84ff) 10%, transparent);
  outline: none;
}

.pet-window__context-action > span:first-child {
  display: inline-flex;
  width: 16px;
  flex: 0 0 auto;
  justify-content: center;
  color: var(--mac-accent, #0a84ff);
  font-size: 12px;
  font-weight: 700;
}

.pet-window__context-action > span:last-child {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-window__context-action.is-muted {
  color: var(--pet-muted);
}

.pet-window__status {
  max-width: 100%;
  min-height: 16px;
  margin: 0;
  overflow: hidden;
  color: var(--pet-muted);
  font-size: 10px;
  line-height: 16px;
  text-align: center;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-window__status.is-success {
  color: #2e8c5b;
}

.pet-window__status.is-error,
.pet-window__inline-state.is-error {
  color: #bd4f4f;
}

.pet-window__dashboard,
.pet-window__inline-state {
  border: 1px solid var(--pet-line);
  border-radius: 14px;
  background: var(--pet-surface);
  box-shadow: 0 8px 22px color-mix(in srgb, #243247 10%, transparent);
  backdrop-filter: blur(14px);
}

.pet-window__dashboard {
  display: flex;
  flex-direction: column;
  gap: 9px;
  padding: 10px;
}

.pet-window__inline-state {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  min-height: 42px;
  padding: 8px 10px;
  color: var(--pet-muted);
  font-size: 11px;
  text-align: center;
}

.pet-window__retry {
  flex: 0 0 auto;
  border: 0;
  border-radius: 7px;
  padding: 4px 8px;
  background: color-mix(in srgb, #bd4f4f 12%, transparent);
  color: #bd4f4f;
  cursor: pointer;
  font: inherit;
}

.pet-window__experience,
.pet-window__stats,
.pet-window__stat {
  min-width: 0;
}

.pet-window__stats {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
}

.pet-window__row-label {
  justify-content: space-between;
  gap: 6px;
  color: var(--pet-muted);
  font-size: 10px;
  line-height: 15px;
}

.pet-window__progress-track {
  width: 100%;
  height: 5px;
  overflow: hidden;
  border-radius: 999px;
  background: color-mix(in srgb, var(--pet-muted) 13%, transparent);
}

.pet-window__progress-fill {
  display: block;
  height: 100%;
  border-radius: inherit;
  background: #5d9fce;
  transition: width 0.25s ease, background 0.25s ease;
}

.pet-window__progress-fill.is-hunger {
  background: #d99b43;
}

.pet-window__progress-fill.is-cleanliness {
  background: #5caaa2;
}

.pet-window__progress-fill.is-mood {
  background: #c87e9c;
}

.pet-window__progress-fill.is-low {
  background: #cb5a54;
}

.pet-window__actions {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 6px;
}

.pet-window__action {
  display: flex;
  min-width: 0;
  min-height: 42px;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 2px;
  border: 1px solid var(--pet-line);
  border-radius: 9px;
  padding: 4px 3px;
  background: color-mix(in srgb, var(--mac-surface, #fff) 38%, transparent);
  color: var(--pet-muted);
  cursor: pointer;
  font: inherit;
  font-size: 10px;
  line-height: 13px;
  transition: border-color 0.18s ease, background 0.18s ease, color 0.18s ease, transform 0.18s ease;
}

.pet-window__action:hover:not(:disabled),
.pet-window__action.is-active {
  border-color: color-mix(in srgb, var(--mac-accent, #0a84ff) 40%, var(--pet-line));
  background: color-mix(in srgb, var(--mac-accent, #0a84ff) 10%, transparent);
  color: var(--pet-ink);
  transform: translateY(-1px);
}

.pet-window__action:disabled {
  cursor: not-allowed;
  opacity: 0.43;
}

.pet-window__action-glyph {
  display: inline-flex;
  width: 20px;
  height: 20px;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
  background: color-mix(in srgb, var(--mac-accent, #0a84ff) 12%, transparent);
  color: var(--mac-accent, #0a84ff);
  font-size: 11px;
  font-weight: 700;
}

@media (max-width: 340px) {
  .pet-window__actions {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

.pet-window__context-menu {
  position: absolute;
  left: 50%;
  bottom: 150px;
  z-index: 4;
  display: flex;
  width: min(300px, calc(100vw - 24px));
  max-height: calc(100vh - 166px);
  margin: 0;
  overflow: auto;
  pointer-events: auto;
}

.pet-window__context-menu-header,
.pet-window__context-summary {
  display: flex;
  min-width: 0;
}

.pet-window__context-menu-header {
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.pet-window__context-summary {
  flex-direction: column;
  gap: 9px;
}

.pet-window__context-menu-actions {
  flex: 0 0 auto;
}

.pet-window__status {
  position: absolute;
  left: 50%;
  bottom: 2px;
  z-index: 3;
  width: min(240px, calc(100vw - 24px));
  transform: translateX(-50%);
  pointer-events: none;
}

.pet-window__inline-state {
  position: absolute;
  left: 50%;
  bottom: 36px;
  z-index: 3;
  width: min(260px, calc(100vw - 24px));
  transform: translateX(-50%);
  pointer-events: none;
}

.pet-window__inline-state.is-error {
  pointer-events: auto;
}

.pet-window__chat-panel {
  position: absolute;
  right: 20px;
  bottom: 18px;
  z-index: 8;
  display: flex;
  width: min(420px, calc(100vw - 40px));
  height: min(520px, calc(100vh - 36px));
  min-width: 0;
  min-height: 0;
  pointer-events: auto;
}

.pet-window > .pet-window__chat-panel {
  pointer-events: auto;
}

.pet-window__chat-panel :deep(.pet-chat) {
  width: 100%;
  height: 100%;
  max-width: none;
  max-height: none;
}

.pet-window__chat-close {
  position: absolute;
  top: 7px;
  right: 7px;
  z-index: 2;
  display: inline-flex;
  width: 24px;
  height: 24px;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--pet-line);
  border-radius: 50%;
  background: color-mix(in srgb, var(--mac-surface, #fff) 72%, transparent);
  color: var(--pet-muted);
  cursor: pointer;
  font: inherit;
  font-size: 16px;
  line-height: 1;
}

.pet-window__chat-close:hover,
.pet-window__chat-close:focus-visible {
  border-color: var(--mac-accent, #0a84ff);
  color: var(--pet-ink);
  outline: none;
}

@media (max-width: 520px) {
  .pet-window__chat-panel {
    right: 12px;
    bottom: 12px;
    width: calc(100vw - 24px);
    height: min(520px, calc(100vh - 24px));
  }
}
</style>
