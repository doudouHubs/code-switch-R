<script setup lang="ts">
import { Call, Events } from '../../wails-runtime-compat'
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Bath,
  Briefcase,
  Coins,
  EyeOff,
  Gamepad2,
  GraduationCap,
  Lock,
  MessageCircle,
  Moon,
  Sparkles,
  Sun,
  Utensils,
  Wand2,
  Waves
} from '@lucide/vue'
import PetAtlasFrame from './PetAtlasFrame.vue'
import PetChat from './PetChat.vue'
import { PetAudioPlayer, type PetAudioSpeechRequest } from './petAudio'
import {
  getPetAtlasFrame,
  parsePetAtlasDocument,
  type PetAtlasDocument
} from './petAtlas'
import {
  normalizePetRuntimeState,
  petApi,
  settlePetRuntimeState,
  type PetRuntimeMode
} from './petApi'
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
  type PetRuntimeSnapshot,
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
// 位置状态独立于动作状态：动作只描述 atlas 如何播放，petX 才决定宠物实际在屏幕哪一列。
// 下面的尺寸只是 atlas 尚未加载时的初始值；加载后由 PetAtlasFrame 发布真实 metrics。
const PET_WIDTH = 132
// 原版命中盒按最大动作高度固定为 126px；如果初始值偏小，atlas 尚未加载的
// 这段时间会把宠物/气泡错误地夹在中间，随后 metrics 更新还会造成一次跳位。
const PET_STAGE_HEIGHT = 126
// 宠物可视主体必须贴合透明窗口底边；菜单是独立面板，使用自己的安全间距。
const PET_GROUND_PADDING = 0
const PANEL_BOTTOM_PADDING = 12
const EDGE_MARGIN = 28
const WALK_SPEED = 55
const SWIM_SPEED = 38
const DASH_SPEED = 170
const PET_POINTER_DRAG_THRESHOLD = 6
const PET_CONTEXT_MENU_WIDTH = 236
const PET_CHAT_WIDTH = 292
const PET_REPORT_TIME_BUBBLE_MS = 5_200
// 这些数值只用于菜单展示，动作能否执行仍由 PetService 判定；菜单不能复制业务规则。
const PET_FEED_COST = 10
const PET_BATHE_COST = 6
const PET_SOAK_COST = 15
const PET_STUDY_COST = 20
const PET_SOAK_MIN_LEVEL = 2
const PET_WORK_MIN_LEVEL = 4
const PET_STUDY_MIN_LEVEL = 6
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

const { locale, t, tm } = useI18n()

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
type PetAmbientBubbleKey =
  | 'hungry'
  | 'dirty'
  | 'lateNight'
  | 'gloomy'
  | 'snack'
  | 'happyIdle'
  | 'zen'
type PetFeedbackKey = 'happy' | 'zenPetted'

// 语言包中的气泡文案是数组，缺失或热更新异常时也必须给 UI 可显示的中文，
// 不能把内部 i18n 路径泄漏到桌宠气泡里。
const PET_AMBIENT_FALLBACKS: Record<PetAmbientBubbleKey, string> = {
  hungry: '肚子好饿……',
  dirty: '是不是该洗澡啦？',
  lateNight: '夜深了，早点休息吧~',
  gloomy: '有点无聊……',
  snack: '偷偷吃个小零食~',
  happyIdle: '今天心情真好~',
  zen: '发呆中。'
}

const PET_FEEDBACK_FALLBACKS: Record<PetFeedbackKey, string> = {
  happy: '嘿嘿~',
  zenPetted: '好安静……'
}

interface PetPointerGesture {
  pointerId: number
  startClientX: number
  startClientY: number
  startPetX: number
  startLift: number
  moved: boolean
}

interface PetAtlasMetrics {
  width: number
  height: number
  visibleWidth: number
  visibleHeight: number
}

type PetBubbleSource = 'notice' | 'dream' | 'proactive' | 'ambient' | 'chat'

interface PetBubbleState {
  text: string
  tone: NoticeTone
  source: PetBubbleSource
  imageUrl?: string
  interactive?: boolean
  emphasizedText?: string
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
const PET_IDLE_DOZE_THRESHOLD_SECONDS = 300
const PET_IDLE_WAKE_THRESHOLD_SECONDS = 5
const PET_IDLE_CHECK_INTERVAL_MS = 5_000
const PET_RUNTIME_TICK_INTERVAL_MS = 30_000
const PET_SETTINGS_UPDATED_EVENT = 'pet.settings.updated'

// 反馈来源共享一个气泡槽位，但不是平级覆盖：用户当前动作和聊天反馈不能
// 被启动问候或低优先级环境提示瞬间抹掉；同一来源仍可流式更新并复用节点。
const PET_BUBBLE_PRIORITIES: Record<PetBubbleSource, number> = {
  ambient: 10,
  dream: 30,
  proactive: 40,
  notice: 60,
  chat: 70
}

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
const actionBusy = ref<PetInteractionAction | null>(null)
const transientAction = ref<PetInteractionAction | null>(null)
const now = ref(Date.now())
const runtimeMode = ref<PetRuntimeMode>('unknown')
const bundledAtlas = shallowRef<PetAtlasAsset | null>(null)
const remoteAtlas = shallowRef<PetAtlasAsset | null>(null)
const ambientBehavior = ref<PetAmbientBehavior | null>(null)
const petFacingLeft = ref(false)
const contextMenuOpen = ref(false)
const chatOpen = ref(false)
const hoveringPet = ref(false)
const dragging = ref(false)
const initialPetX =
  typeof window === 'undefined' ? EDGE_MARGIN : Math.max(EDGE_MARGIN, window.innerWidth / 2 - PET_WIDTH / 2)
const petX = ref(initialPetX)
const petLift = ref(0)
const petAnchorRef = ref<HTMLElement | null>(null)
// 位置动画是桌宠最频繁的更新源；保留在普通变量中，避免每个 requestAnimationFrame
// 都触发整个 PetWindow 的响应式渲染。业务边界变化时再同步回 petX/petLift。
let renderedPetX = initialPetX
let renderedPetLift = 0
const petMetrics = ref<PetAtlasMetrics>({
  width: PET_WIDTH,
  height: PET_STAGE_HEIGHT,
  visibleWidth: PET_WIDTH,
  visibleHeight: PET_STAGE_HEIGHT
})
const dragGestureMoved = ref(false)
const squashing = ref(false)
const petBubble = ref<PetBubbleState | null>(null)
const atlasImageFailed = ref(false)
// dozing 是系统空闲造成的前端表现，不写入 PetSnapshot.sleeping，避免误触发梦境和持久化睡眠规则。
const dozing = ref(false)
const dreamPaused = ref(false)

let clockTimer: number | undefined
let runtimeTickTimer: number | undefined
let proactiveTimer: number | undefined
let ambientTimer: number | undefined
let idleTimer: number | undefined
let reportTimer: number | undefined
let refreshInFlight = false
let refreshRequested = false
let snapshotGeneration = 0
let lifecycleInitializedPetId = ''
let readyPresentationPetId = ''
let transientToken = 0
let dreamTimer: number | undefined
let dreamSessionGeneration = 0
let dreamSessionActive = false
let dreamTextController: AbortController | null = null
let dreamTextRequestId: string | null = null
let dreamImageController: AbortController | null = null
let dreamSpeechRequestId: string | null = null
let proactiveGeneration = 0
let lastProactiveRemarkAt = 0
let proactiveSpeechRequestId: string | null = null
let ambientToken = 0
let ambientBehaviorTimer: number | undefined
let bubbleTimer: number | undefined
let bubbleToken = 0
let roamFrame: number | undefined
let roamToken = 0
let lastLateNightAt = 0
let lastCoinPickupAt = 0
let liftFrame: number | undefined
let menuCloseTimer: number | undefined
let squashTimer: number | undefined
let petPointerGesture: PetPointerGesture | null = null
let windowModeBridgeUnavailable = false
let requestedWindowMode: PetWindowMode = 'passive'
let appliedWindowMode: PetWindowMode = 'passive'
let windowModeQueue: Promise<void> = Promise.resolve()
let forceWindowModeSync = false
let petWindowUnmounted = false
let idleCheckInFlight = false
let idleBridgeUnavailable = false
let stopPetActionEvent: (() => void) | null = null
let stopPetRuntimeEvent: (() => void) | null = null
let stopPetReminderEvent: (() => void) | null = null
let stopPetAudioEvent: (() => void) | null = null
let stopPetPointerEvent: (() => void) | null = null
let stopPetSettingsEvent: (() => void) | null = null

const state = computed(() => snapshot.value?.state ?? null)
const experience = computed(() => snapshot.value?.experience.totalExp ?? 0)
const totalGrowth = computed(() => (state.value?.growth ?? 0) + experience.value)
const level = computed(() => getPetLevel(totalGrowth.value))
const levelProgress = computed(() => getLevelProgress(totalGrowth.value))
const levelProgressPercent = computed(() => Math.round(levelProgress.value * 100))
const petName = computed(() => state.value?.name || t('pet.window.defaultName'))
const coins = computed(() => Math.floor(state.value?.coins ?? 0))
const awayRemaining = computed(() => Math.max(0, (state.value?.awayTask?.endsAt ?? 0) - now.value))
const atlas = computed<PetAtlasAsset | null>(() => {
  if (props.atlasManifest && props.atlasImageUrl) {
    return { src: props.atlasImageUrl, manifest: props.atlasManifest }
  }
  return remoteAtlas.value ?? bundledAtlas.value ?? snapshot.value?.atlas ?? null
})

const atlasBehavior = computed(() => {
  if (dragging.value && dragGestureMoved.value) return 'drag'
  if (state.value?.sleeping) return 'sleep'
  if (dozing.value) return 'sleep'
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

function getViewportWidth(): number {
  // 透明 Wails 窗口的 documentElement 可能沿用宿主页面的布局尺寸；桌宠漫游
  // 的边界必须以原生 WebView viewport 为准，否则窗口虽然铺满 WorkArea，宠物
  // 仍会被错误的内容宽度锁在中间一小段区域。
  return typeof window === 'undefined' ? PET_WIDTH : Math.max(1, window.innerWidth)
}

function getViewportHeight(): number {
  return typeof window === 'undefined' ? PET_STAGE_HEIGHT : Math.max(1, window.innerHeight)
}

function getPetMaxX(): number {
  // 拖拽允许把宠物完整贴到屏幕边缘；漫游会在这个命中边界内再额外保留
  // EDGE_MARGIN，两个范围不能混用，否则拖拽到边缘后自动漫游会突然跳位。
  return Math.max(0, getViewportWidth() - petMetrics.value.width)
}

function getRoamMinX(): number {
  return Math.min(EDGE_MARGIN, getPetMaxX())
}

function getRoamMaxX(): number {
  return Math.max(getRoamMinX(), getPetMaxX() - EDGE_MARGIN)
}

function applyPetAnchorTransform(): void {
  const anchor = petAnchorRef.value
  if (!anchor) return
  // 和 OpenCowork 的 motion value 一样，只更新承载宠物、气泡和 HUD 的移动层，
  // 不让漫游位置变化重新执行整个 Vue 模板的依赖计算。
  anchor.style.transform = `translate3d(${renderedPetX}px, ${renderedPetLift}px, 0)`
}

function setRenderedPetPosition(x: number, lift: number): void {
  renderedPetX = x
  renderedPetLift = lift
  applyPetAnchorTransform()
}

function commitPetPosition(): void {
  // 动画期间只改 DOM；到动作边界再把最终值提交给响应式状态，供菜单定位和边界计算使用。
  if (petX.value !== renderedPetX) petX.value = renderedPetX
  if (petLift.value !== renderedPetLift) petLift.value = renderedPetLift
  applyPetAnchorTransform()
}

function clampPetX(value: number): number {
  return clamp(value, 0, getPetMaxX())
}

function handlePetMetricsChange(next: PetAtlasMetrics): void {
  const metrics: PetAtlasMetrics = {
    width: Math.max(1, Math.ceil(Number.isFinite(next.width) ? next.width : PET_WIDTH)),
    height: Math.max(1, Math.ceil(Number.isFinite(next.height) ? next.height : PET_STAGE_HEIGHT)),
    visibleWidth: Math.max(1, Math.ceil(Number.isFinite(next.visibleWidth) ? next.visibleWidth : PET_WIDTH)),
    visibleHeight: Math.max(1, Math.ceil(Number.isFinite(next.visibleHeight) ? next.visibleHeight : PET_STAGE_HEIGHT))
  }
  const current = petMetrics.value
  if (
    current.width === metrics.width &&
    current.height === metrics.height &&
    current.visibleWidth === metrics.visibleWidth &&
    current.visibleHeight === metrics.visibleHeight
  ) {
    return
  }
  petMetrics.value = metrics
  renderedPetX = clampPetX(renderedPetX)
  renderedPetLift = Math.max(getPetMinLift(metrics), Math.min(0, renderedPetLift))
  commitPetPosition()
}

function getPetMinLift(metrics = petMetrics.value): number {
  const viewportHeight = getViewportHeight()
  // 原版只按稳定命中盒限制垂直拖拽；气泡跟随同一个 anchor，不能为了
  // 防止裁切而偷偷缩小宠物可拖到的范围，否则拖拽手感会和原版不一致。
  return Math.min(0, -(viewportHeight - PET_GROUND_PADDING - metrics.height))
}

// OpenCowork 把气泡、HUD 和精灵放在同一个移动容器里；这里保持同一坐标树，
// 避免根场景再按屏幕坐标手算气泡位置，导致移动、DPI 或动作高度切换时出现漂移。
const petAnchorStyle = computed(() => ({
  left: '0px',
  bottom: `${PET_GROUND_PADDING}px`,
  width: `${petMetrics.value.width}px`,
  height: `${petMetrics.value.height}px`
}))

const petBubbleStyle = computed(() => ({
  left: '50%',
  // 只使用当前主体的可见高度；稳定命中盒只负责拖拽和边界，不参与气泡锚定。
  bottom: `${petMetrics.value.visibleHeight + 8}px`
}))

const petHudStyle = computed(() => ({
  left: '50%',
  bottom: `${petMetrics.value.visibleHeight + 8}px`,
  width: '160px'
}))

function getSidePanelLeft(panelWidth: number): number {
  const viewportWidth = getViewportWidth()
  const width = Math.min(panelWidth, Math.max(1, viewportWidth - 24))
  // 菜单/聊天和原版一样优先放在宠物右侧；两边都放不下时以 8px
  // 为屏幕内边距夹紧，避免透明窗口边缘把按钮截掉。
  const preferRight = renderedPetX + petMetrics.value.width + 16 + width <= viewportWidth
  const left = preferRight
    ? renderedPetX + petMetrics.value.width + 12
    : renderedPetX - width - 12
  return clamp(left, 8, Math.max(8, viewportWidth - width - 8))
}

function animatePetDrop(): void {
  if (liftFrame !== undefined) window.cancelAnimationFrame(liftFrame)
  const startedLift = renderedPetLift
  if (startedLift >= 0) {
    setRenderedPetPosition(renderedPetX, 0)
    commitPetPosition()
    return
  }
  const startedAt = typeof performance === 'undefined' ? Date.now() : performance.now()
  // 用与原版 Framer Motion 接近的欠阻尼弹簧解算落地，不用固定分段插值，
  // 这样不同拖起高度的回弹速度和过冲比例保持一致。
  const stiffness = 320
  const damping = 15
  const naturalFrequency = Math.sqrt(stiffness)
  const dampingRatio = damping / (2 * naturalFrequency)
  const dampedFrequency = naturalFrequency * Math.sqrt(1 - dampingRatio ** 2)
  const duration = 1_100
  const tick = (timestamp: number): void => {
    const elapsed = Math.min(duration, Math.max(0, timestamp - startedAt)) / 1_000
    const envelope = Math.exp(-dampingRatio * naturalFrequency * elapsed)
    const oscillation = Math.cos(dampedFrequency * elapsed)
      + (dampingRatio * naturalFrequency / dampedFrequency) * Math.sin(dampedFrequency * elapsed)
    const nextLift = startedLift * envelope * oscillation
    setRenderedPetPosition(renderedPetX, nextLift)
    if (elapsed >= duration / 1_000 || Math.abs(nextLift) < 0.25) {
      setRenderedPetPosition(renderedPetX, 0)
      commitPetPosition()
      liftFrame = undefined
      return
    }
    liftFrame = window.requestAnimationFrame(tick)
  }
  liftFrame = window.requestAnimationFrame(tick)
}

const petMenuStyle = computed(() => ({
  left: `${getSidePanelLeft(PET_CONTEXT_MENU_WIDTH)}px`,
  // 菜单是窗口级面板；原版拖起宠物时菜单仍贴地，不能跟着精灵一起升高。
  bottom: `${PANEL_BOTTOM_PADDING}px`,
  width: `${Math.min(PET_CONTEXT_MENU_WIDTH, Math.max(1, getViewportWidth() - 24))}px`
}))

const petChatStyle = computed(() => ({
  left: `${getSidePanelLeft(PET_CHAT_WIDTH)}px`,
  // 聊天面板同样独立贴底，避免拖拽宠物时输入区域发生位移并丢失焦点。
  bottom: `${PANEL_BOTTOM_PADDING}px`,
  width: `${Math.min(PET_CHAT_WIDTH, Math.max(1, getViewportWidth() - 24))}px`
}))

const awayPillStyle = computed(() => ({
  right: '20px',
  bottom: '16px'
}))

const statusText = computed(() => {
  // 睡眠和打盹本身已经由宠物动画表达；不再额外冒泡“点击唤醒”提示，
  // 避免遮挡宠物，同时保留右键菜单中的明确唤醒动作和真正的梦话气泡。
  // 动作进行中只保留菜单按钮的 busy 状态，不把任务过程文案冒泡到宠物头顶。
  if (runtimeMode.value === 'fallback') return t('pet.window.status.fallback')
  return ''
})

const activeBubble = computed<PetBubbleState | null>(() => {
  // away 的状态由右下角任务胶囊承载；不能再把同一倒计时复制到宠物头顶。
  if (state.value?.awayTask) return null
  if (petBubble.value) return petBubble.value
  const text = statusText.value.trim()
  return text ? { text, tone: 'muted', source: 'notice' } : null
})

const activeBubbleTextParts = computed(() => {
  const bubble = activeBubble.value
  if (!bubble?.emphasizedText) return { before: bubble?.text ?? '', emphasized: '', after: '' }
  const start = bubble.text.indexOf(bubble.emphasizedText)
  if (start < 0) return { before: bubble.text, emphasized: '', after: '' }
  const end = start + bubble.emphasizedText.length
  return {
    before: bubble.text.slice(0, start),
    emphasized: bubble.text.slice(start, end),
    after: bubble.text.slice(end)
  }
})

const statItems = computed(() => [
  { key: 'hunger', label: t('pet.window.stats.hunger'), value: state.value?.hunger ?? 0, tone: 'hunger' },
  { key: 'cleanliness', label: t('pet.window.stats.cleanliness'), value: state.value?.cleanliness ?? 0, tone: 'cleanliness' },
  { key: 'mood', label: t('pet.window.stats.mood'), value: state.value?.mood ?? 0, tone: 'mood' }
])

const actionButtons = computed<Array<{
  action: PetInteractionAction
  label: string
  icon: Component
  cost?: number
  lockedLevel?: number
}>>(() => [
  // 右键菜单的动作顺序、图标语义、花费和等级锁定与 OpenCowork PetView 对齐。
  { action: 'feed', label: t('pet.window.actions.feed'), icon: Utensils, cost: PET_FEED_COST },
  { action: 'bathe', label: t('pet.window.actions.bathe'), icon: Bath, cost: PET_BATHE_COST },
  {
    action: 'soak',
    label: t('pet.window.actions.soak'),
    icon: Waves,
    cost: PET_SOAK_COST,
    lockedLevel: level.value < PET_SOAK_MIN_LEVEL ? PET_SOAK_MIN_LEVEL : undefined
  },
  { action: 'play', label: t('pet.window.actions.play'), icon: Gamepad2 },
  {
    action: 'sleep',
    label: state.value?.sleeping ? t('pet.window.actions.wake') : t('pet.window.actions.sleep'),
    icon: state.value?.sleeping ? Sun : Moon
  },
  {
    action: 'work',
    label: t('pet.window.actions.work'),
    icon: Briefcase,
    lockedLevel: level.value < PET_WORK_MIN_LEVEL ? PET_WORK_MIN_LEVEL : undefined
  },
  {
    action: 'study',
    label: t('pet.window.actions.study'),
    icon: GraduationCap,
    cost: PET_STUDY_COST,
    lockedLevel: level.value < PET_STUDY_MIN_LEVEL ? PET_STUDY_MIN_LEVEL : undefined
  }
])

function formatCountdown(milliseconds: number): string {
  const totalSeconds = Math.max(0, Math.ceil(milliseconds / 1000))
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  return `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`
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
  if (force) forceWindowModeSync = true

  // SetMode 是增强能力；绑定缺失或平台不支持时只熔断这条桥，不阻断 Vue 内的聊天和动作按钮。
  windowModeQueue = windowModeQueue.then(async () => {
    // pointer/DOM 事件可能在同一帧内产生 passive -> interactive -> passive；
    // 每次原生调用完成后重新读取最新目标，直到 requested 与 applied 收敛，
    // 避免旧请求完成后把“最新 passive”误判成已应用，留下 interactive 拦截桌面。
    while (!windowModeBridgeUnavailable) {
      const targetMode = requestedWindowMode
      const shouldForce = forceWindowModeSync
      if (!shouldForce && targetMode === appliedWindowMode) return
      forceWindowModeSync = false

      try {
        await Call.ByName(PET_WINDOW_MODE_METHOD, targetMode)
        appliedWindowMode = targetMode
      } catch (error) {
        if (isMissingPetBindingError(error)) {
          windowModeBridgeUnavailable = true
          return
        }
        // 瞬时桥错误不能永久熔断交互；仅在失败目标仍是最新目标时回退，
        // 若期间已有新目标，则让本轮排空逻辑继续尝试新目标。
        console.warn('[Pet] window mode update failed:', error)
        if (targetMode === requestedWindowMode) {
          requestedWindowMode = appliedWindowMode
          return
        }
        continue
      }
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
    Boolean(value.closest('.pet-window__pet-stage, .pet-window__bubble, .pet-window__context-menu, .pet-chat, button, input, textarea, select'))
  )
}

function isPetStageTarget(value: EventTarget | null): boolean {
  return value instanceof Element && Boolean(value.closest('.pet-window__pet-stage'))
}

function syncPetHoverState(value: EventTarget | null): void {
  // 原版依赖 renderer 的 mouseenter；Wails 穿透态下没有 DOM pointer 事件，
  // 因此 native 轮询必须同步同一个 hoveringPet owner，否则 HUD 会在离开后残留，
  // 或者重新移入时只切了窗口模式却没有显示状态面板。
  hoveringPet.value = isPetStageTarget(value)
}

function syncPetWindowModeAtPointer(event: PointerEvent): void {
  const hovered = typeof document === 'undefined'
    ? null
    : document.elementFromPoint(event.clientX, event.clientY)
  syncPetHoverState(hovered)
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
    syncPetHoverState(null)
    if (!dragging.value) requestPetWindowMode(shouldRetainKeyboardMode() ? 'keyboard' : 'passive')
    return
  }
  const screenX = typeof source.screenX === 'number' ? source.screenX : NaN
  const screenY = typeof source.screenY === 'number' ? source.screenY : NaN
  const windowX = typeof source.windowX === 'number' ? source.windowX : NaN
  const windowY = typeof source.windowY === 'number' ? source.windowY : NaN
  if (![screenX, screenY, windowX, windowY].every(Number.isFinite)) {
    syncPetHoverState(null)
    return
  }

  // Wails 没有 Electron 的 forward mouse；原生层只提供屏幕坐标，DOM 命中仍由 renderer 判断，
  // 这样透明空白区域继续穿透，只有真正落在宠物/菜单/聊天控件上的光标才恢复交互。
  const windowWidth = typeof source.windowWidth === 'number' ? source.windowWidth : NaN
  const windowHeight = typeof source.windowHeight === 'number' ? source.windowHeight : NaN
  const viewportWidth = Math.max(1, window.innerWidth)
  const viewportHeight = Math.max(1, window.innerHeight)
  if (![windowWidth, windowHeight].every((item) => Number.isFinite(item) && item > 0)) {
    syncPetHoverState(null)
    return
  }
  // Native GetWindowRect 使用物理像素，WebView 的 DOM 使用 CSS/DIP；按窗口比例换算，
  // 才能在高 DPI 和负坐标副屏上保持 elementFromPoint 的命中位置一致。
  const localX = ((screenX - windowX) / windowWidth) * viewportWidth
  const localY = ((screenY - windowY) / windowHeight) * viewportHeight
  const hovered = document.elementFromPoint(localX, localY)
  syncPetHoverState(hovered)
  if (isInteractiveTarget(hovered)) {
    requestPetWindowMode(isKeyboardTarget(document.activeElement) ? 'keyboard' : 'interactive')
  } else if (!dragging.value) {
    // interactive 状态下原生窗口覆盖整个 work area，不会再触发 DOM pointerleave；
    // 轮询命中透明空白后主动恢复 WS_EX_TRANSPARENT，避免一次碰到宠物就永久挡住桌面。
    requestPetWindowMode(shouldRetainKeyboardMode() ? 'keyboard' : 'passive')
  }
}

function handleWindowPointerOver(event: PointerEvent): void {
  syncPetHoverState(event.target)
  if (isInteractiveTarget(event.target)) requestPetWindowMode('interactive')
}

function handleWindowPointerLeave(): void {
  syncPetHoverState(null)
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
  syncPetHoverState(null)
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
    showPetBubble(t('pet.window.ambient.welcome'), 'muted', 'ambient', 3_000)
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

function clearPetBubble(source?: PetBubbleSource): void {
  if (source && petBubble.value?.source !== source) return
  bubbleToken += 1
  if (bubbleTimer !== undefined) window.clearTimeout(bubbleTimer)
  bubbleTimer = undefined
  petBubble.value = null
  if (!chatOpen.value && !contextMenuOpen.value && !dragging.value) {
    requestPetWindowMode(shouldRetainKeyboardMode() ? 'keyboard' : 'passive')
  }
}

function getPetBubbleDuration(text: string, source: PetBubbleSource, requested?: number): number {
  if (requested !== undefined && Number.isFinite(requested)) return Math.max(1_000, requested)
  if (source === 'proactive' || source === 'chat') return Math.min(32_000, Math.max(12_000, 12_000 + Array.from(text).length * 60))
  if (source === 'dream') return Math.max(5_000, 5_000 + Array.from(text).length * PET_DREAM_READING_MS_PER_CHARACTER)
  return 3_800
}

function showPetBubble(
  text: string,
  tone: NoticeTone = 'muted',
  source: PetBubbleSource = 'notice',
  duration?: number,
  imageUrl?: string,
  interactive = false,
  emphasizedText?: string
): void {
  const normalized = text.trim()
  if (!normalized) return
  const current = petBubble.value
  if (
    current &&
    current.source !== source &&
    PET_BUBBLE_PRIORITIES[source] < PET_BUBBLE_PRIORITIES[current.source]
  ) {
    return
  }
  bubbleToken += 1
  const token = bubbleToken
  if (bubbleTimer !== undefined) window.clearTimeout(bubbleTimer)
  // 这是整个桌宠窗口唯一的反馈 owner：梦话、主动搭话、动作结果和错误互相覆盖，
  // 但复用同一个 DOM 节点，流式或连续更新不会反复播放气泡入场动画。
  petBubble.value = {
    text: normalized,
    tone,
    source,
    ...(imageUrl ? { imageUrl } : {}),
    ...(interactive ? { interactive: true } : {}),
    ...(emphasizedText && normalized.includes(emphasizedText) ? { emphasizedText } : {})
  }
  if (interactive || imageUrl) requestPetWindowMode('interactive')
  bubbleTimer = window.setTimeout(() => {
    if (token !== bubbleToken) return
    petBubble.value = null
    bubbleTimer = undefined
    if (!chatOpen.value && !contextMenuOpen.value && !dragging.value) {
      requestPetWindowMode(shouldRetainKeyboardMode() ? 'keyboard' : 'passive')
    }
  }, getPetBubbleDuration(normalized, source, duration))
}

function openPetBubbleImage(): void {
  const imageUrl = petBubble.value?.imageUrl
  if (!imageUrl) return
  // 原版点击梦境图片会打开原图；当前协议返回受控 data URL，因此直接交给
  // 浏览器新页打开，不暴露归档绝对路径，也不把图片点击误判成打开聊天。
  const opened = window.open(imageUrl, '_blank', 'noopener,noreferrer')
  if (!opened) showPetBubble(t('pet.window.feedback.imageOpenBlocked'), 'muted', 'notice')
}

function showAmbientPresentation(text: string, duration = 5_000): void {
  showPetBubble(text, 'muted', 'ambient', duration)
}

function resolvePetMessage(value: unknown, path: string, fallback: string): string {
  if (Array.isArray(value)) {
    const messages = value.filter(
      (item): item is string => typeof item === 'string' && item.trim().length > 0
    )
    if (messages.length > 0) {
      return messages[Math.floor(Math.random() * messages.length)]
    }
  }
  // tm() 对单条文案也会返回 string；只有真正的文案才能直接展示，不能把缺失路径当文案。
  if (typeof value === 'string' && value.trim() && value !== path) return value
  return fallback
}

function pickAmbientBubble(key: PetAmbientBubbleKey): string {
  const path = `pet.window.ambient.${key}`
  return resolvePetMessage(tm(path), path, PET_AMBIENT_FALLBACKS[key])
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
  if (ambientTimer !== undefined) window.clearTimeout(ambientTimer)
  ambientTimer = undefined
  stopPetRoaming()
  ambientBehavior.value = null
}

function stopPetRoaming(): void {
  roamToken += 1
  if (roamFrame !== undefined) window.cancelAnimationFrame(roamFrame)
  roamFrame = undefined
  // 停止可能由点击、菜单、聊天或业务动作触发；先提交当前 DOM 位置，避免
  // 后续菜单定位仍拿着上一次动画起点，导致面板突然跳回旧位置。
  commitPetPosition()
}

function startPetRoaming(behavior: 'walk' | 'swim', speed = behavior === 'swim' ? SWIM_SPEED : WALK_SPEED): number {
  if (dozing.value || contextMenuOpen.value || chatOpen.value || dragging.value || state.value?.awayTask) return 0
  const minX = getRoamMinX()
  const maxX = getRoamMaxX()
  const from = clampPetX(renderedPetX)
  let target = minX + Math.random() * Math.max(0, maxX - minX)
  // 原版把 48px 以内的随机目标视为“本轮不走”；强行改成远端会让宠物
  // 在屏幕边缘突然长距离折返，破坏原版的随机停留节奏。
  if (Math.abs(target - from) < 48) return 0

  stopPetRoaming()
  setRenderedPetPosition(from, renderedPetLift)
  commitPetPosition()
  petFacingLeft.value = target < from
  const token = ++roamToken
  const startedAt = typeof performance === 'undefined' ? Date.now() : performance.now()
  const distance = Math.abs(target - from)
  const isDash = speed >= DASH_SPEED && distance >= 160
  // 原版短 dash 会退回普通散步；保留同一目标即可避免递归抽样导致极端情况下
  // 连续命中近目标，同时仍保持“短距离不突然加速”的可感知语义。
  const effectiveSpeed = isDash ? speed : Math.min(speed, WALK_SPEED)
  const duration = Math.max(320, distance / Math.max(1, effectiveSpeed) * 1_000)

  const tick = (timestamp: number): void => {
    if (token !== roamToken || petWindowUnmounted) return
    const elapsed = timestamp - startedAt
    const progress = Math.min(1, elapsed / duration)
    // dash 使用与 Motion `easeOut` 接近的 cubic ease-out，普通行走保持线性，
    // 让“突然冲刺”和普通散步在窗口中有明确的动作差异。
    const easedProgress = isDash ? 1 - (1 - progress) ** 3 : progress
    setRenderedPetPosition(from + (target - from) * easedProgress, renderedPetLift)
    if (progress >= 1) {
      setRenderedPetPosition(target, renderedPetLift)
      commitPetPosition()
      roamFrame = undefined
      if (ambientBehavior.value === behavior) ambientBehavior.value = null
      if (ambientBehaviorTimer !== undefined) window.clearTimeout(ambientBehaviorTimer)
      ambientBehaviorTimer = undefined
      scheduleNextAmbientBehavior()
      return
    }
    roamFrame = window.requestAnimationFrame(tick)
  }

  roamFrame = window.requestAnimationFrame(tick)
  return duration
}

function setAmbientBehavior(behavior: PetAmbientBehavior, duration: number, speed?: number): void {
  if (dozing.value) return
  ambientToken += 1
  const token = ambientToken
  if (ambientBehaviorTimer !== undefined) window.clearTimeout(ambientBehaviorTimer)
  ambientBehaviorTimer = undefined
  ambientBehavior.value = behavior
  let roamingDuration = 0
  if (behavior === 'walk' || behavior === 'swim') {
    roamingDuration = startPetRoaming(behavior, speed)
  }
  const effectiveDuration = behavior === 'walk' || behavior === 'swim'
    ? Math.max(duration, roamingDuration)
    : duration
  ambientBehaviorTimer = window.setTimeout(() => {
    if (token !== ambientToken) return
    stopPetRoaming()
    ambientBehavior.value = null
    ambientBehaviorTimer = undefined
    scheduleNextAmbientBehavior()
  }, effectiveDuration)
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
      petBubble.value?.source !== 'dream'
  )
}

function runAmbientBehavior(): void {
  if (!canRunAmbientBehavior() || ambientBehavior.value) return
  const current = state.value
  if (!current) return

  const hour = new Date().getHours()
  // 原版调度先处理需要照顾的状态，再抽普通消遣；否则低饥饿/低清洁时
  // 仍然随机游泳，用户看不到状态提醒，桌宠就像坏了而不是在求关注。
  if (current.hunger < 30 && Math.random() < (current.hunger < 15 ? 0.75 : 0.55)) {
    setAmbientBehavior('beg', 3_000)
    showAmbientPresentation(pickAmbientBubble('hungry'))
    return
  }
  if (current.cleanliness < 30 && Math.random() < 0.45) {
    setAmbientBehavior('beg', 3_000)
    showAmbientPresentation(pickAmbientBubble('dirty'))
    return
  }
  if (
    (hour >= 23 || hour < 5) &&
    Date.now() - lastLateNightAt > 30 * 60_000 &&
    Math.random() < 0.35
  ) {
    lastLateNightAt = Date.now()
    setAmbientBehavior('sleep', 6_000)
    showAmbientPresentation(pickAmbientBubble('lateNight'))
    return
  }
  if (current.mood < 20 && Math.random() < 0.3) {
    setAmbientBehavior('zen', 6_000)
    showAmbientPresentation(pickAmbientBubble('gloomy'))
    return
  }

  const roll = Math.random()
  if (Date.now() - lastCoinPickupAt > 45 * 60_000 && Math.random() < 0.08) {
    // 当前 Go/PetService 没有环境捡金币的持久化入口；这里只占用原版的
    // 行为槽位，不在前端偷偷改快照，避免显示奖励却在刷新后回滚。
    lastCoinPickupAt = Date.now()
    setAmbientBehavior('play', 2_200)
    return
  }
  if (roll < 0.16) {
    setAmbientBehavior('zen', 8_000)
    if (Math.random() < 0.5) showAmbientPresentation(pickAmbientBubble('zen'))
  } else if (roll < 0.26) {
    setAmbientBehavior(Math.random() < 0.5 ? 'eat' : 'munch', 2_600)
    if (Math.random() < 0.6) showAmbientPresentation(pickAmbientBubble('snack'))
  } else if (roll < 0.34 && current.mood > 80) {
    setAmbientBehavior('play', 2_200)
    if (Math.random() < 0.4) showAmbientPresentation(pickAmbientBubble('happyIdle'))
  } else if (roll < 0.42) {
    // dash 只有目标足够远时才会真正加速；短目标由 startPetRoaming 回退为普通行走。
    setAmbientBehavior('walk', 8_000, DASH_SPEED)
  } else if (roll < 0.6) {
    setAmbientBehavior('swim', 8_000, SWIM_SPEED)
  } else {
    setAmbientBehavior('walk', 8_000, WALK_SPEED)
  }
}

function scheduleNextAmbientBehavior(): void {
  if (petWindowUnmounted) return
  if (ambientTimer !== undefined) window.clearTimeout(ambientTimer)
  if (ambientBehavior.value) return
  // OpenCowork 在宠物回到 idle 后用 2.5~6.5 秒随机延迟排下一次行为，
  // 不用固定轮询概率，否则长时间可能看起来像动画已经坏掉。
  ambientTimer = window.setTimeout(() => {
    ambientTimer = undefined
    runAmbientBehavior()
    // 被菜单、聊天、拖拽或业务动作挡住时仍需继续等待；真正开始动作后，
    // 下一次排期由动作完成回调负责，避免走路期间堆积多个空计时器。
    if (!ambientBehavior.value) scheduleNextAmbientBehavior()
  }, 2_500 + Math.random() * 4_000)
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
      const time = formatPetClockTime(boundary)
      showPetBubble(t('pet.window.ambient.time', { time }), 'muted', 'ambient', PET_REPORT_TIME_BUBBLE_MS, undefined, false, time)
    }
    schedulePetTimeReport()
  }, delay)
}

function cancelContextMenuClose(): void {
  if (menuCloseTimer === undefined) return
  window.clearTimeout(menuCloseTimer)
  menuCloseTimer = undefined
}

function scheduleContextMenuClose(): void {
  cancelContextMenuClose()
  menuCloseTimer = window.setTimeout(() => {
    menuCloseTimer = undefined
    contextMenuOpen.value = false
    requestPetWindowMode(shouldRetainKeyboardMode() ? 'keyboard' : 'passive')
    scheduleNextAmbientBehavior()
  }, 500)
}

function triggerPetSquash(): void {
  squashing.value = true
  if (squashTimer !== undefined) window.clearTimeout(squashTimer)
  squashTimer = window.setTimeout(() => {
    squashing.value = false
    squashTimer = undefined
  }, 450)
}

function queuePetting(): void {
  triggerPetSquash()
  const isZen = atlasBehavior.value === 'zen'
  // 原版普通点击只在当前没有其他气泡时显示反馈，避免一次轻点覆盖用户
  // 正在阅读的回复；Zen 状态则额外触发专属反馈并获得两次摸摸增益。
  if (isZen) {
    showPetBubble(pickPetFeedback('zenPetted'), 'success', 'notice')
  } else if (!petBubble.value) {
    showNotice(pickPetFeedback('happy'), 'success')
  }
  void persistPetting(isZen ? 2 : 1)
}

function pickPetFeedback(key: PetFeedbackKey): string {
  const path = `pet.window.feedback.success.${key}`
  return resolvePetMessage(tm(path), path, PET_FEEDBACK_FALLBACKS[key])
}

async function persistPetting(times = 1): Promise<void> {
  try {
    let result = await petApi.performAction(props.petId, 'petted')
    for (let index = 1; index < times && result.ok; index += 1) {
      // 顺序提交两次，避免并发请求在 SQLite/服务锁竞争下丢掉第二次增量。
      result = await petApi.performAction(props.petId, 'petted')
    }
    if (result.snapshot) snapshot.value = result.snapshot
    else if (result.state) applyRuntimeState(result.state)
    runtimeMode.value = petApi.getRuntimeMode()
    if (!result.ok) showNotice(getFailureMessage(result.reason), 'error')
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : String(error)
    showNotice(t('pet.window.feedback.actionFailed'), 'error')
  }
}

function openPetChat(): void {
  // 打开聊天后宠物进入交互模式；必须先终止漫游，否则主体会继续横向移动，
  // 气泡和输入面板会在用户打字时追着宠物跑，和原版交互语义相反。
  stopAmbientBehavior()
  cancelContextMenuClose()
  contextMenuOpen.value = false
  chatOpen.value = true
  requestPetWindowMode('keyboard')

  const focusInput = (): void => {
    const input = document.querySelector<HTMLTextAreaElement>('.pet-chat__composer textarea')
    input?.focus()
  }
  window.setTimeout(focusInput, 0)
}

function closePetChat(): void {
  chatOpen.value = false
  if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
  requestPetWindowMode('passive')
  scheduleNextAmbientBehavior()
}

function togglePetChat(): void {
  if (chatOpen.value) closePetChat()
  else {
    stopAmbientBehavior()
    openPetChat()
  }
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

type PetContextMenuAction = 'chat' | 'studio' | 'hide'

function runContextMenuAction(action: PetContextMenuAction): void {
  if (action === 'chat') openPetChat()
  else if (action === 'studio') openPetSettings(true)
  else hidePetWindow()
}

function toggleContextMenu(): void {
  cancelContextMenuClose()
  wakePetFromDozing()
  if (!contextMenuOpen.value) stopAmbientBehavior()
  contextMenuOpen.value = !contextMenuOpen.value
  requestPetWindowMode(contextMenuOpen.value ? 'interactive' : 'passive')
  if (!contextMenuOpen.value) scheduleNextAmbientBehavior()
}

function handlePetAtlasError(): void {
  // atlas 解码失败时保留固定命中盒和所有交互，不让透明画布伪装成“宠物
  // 消失”。这样用户仍能重试/打开菜单，错误也不会把整扇透明窗锁死。
  atlasImageFailed.value = true
}

function handlePetAtlasReady(): void {
  atlasImageFailed.value = false
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value))
}

function onPetPointerEnter(): void {
  hoveringPet.value = true
  requestPetWindowMode('interactive')
}

function onPetPointerLeave(): void {
  hoveringPet.value = false
  if (!dragging.value) {
    requestPetWindowMode(shouldRetainKeyboardMode() ? 'keyboard' : 'passive')
  }
}

function onPetPointerDown(event: PointerEvent): void {
  if (event.button !== 0) return
  const target = event.currentTarget
  if (!(target instanceof HTMLElement)) return

  // 触碰是最高优先级的唤醒信号；后续移动仍由同一个 pointer capture 手势分流。
  wakePetFromDozing()
  requestPetWindowMode('interactive')
  target.setPointerCapture(event.pointerId)
  petPointerGesture = {
    pointerId: event.pointerId,
    startClientX: event.clientX,
    startClientY: event.clientY,
    startPetX: renderedPetX,
    startLift: renderedPetLift,
    moved: false
  }
  dragging.value = true
  dragGestureMoved.value = false
  event.preventDefault()
}

function onPetPointerMove(event: PointerEvent): void {
  const gesture = petPointerGesture
  if (!gesture || gesture.pointerId !== event.pointerId) return

  const dx = event.clientX - gesture.startClientX
  const dy = event.clientY - gesture.startClientY
  // 原版使用曼哈顿距离；保持这个阈值能避免斜向轻点被过早判定为拖拽。
  if (!gesture.moved && Math.abs(dx) + Math.abs(dy) > PET_POINTER_DRAG_THRESHOLD) {
    gesture.moved = true
    stopAmbientBehavior()
    stopPetRoaming()
    dragGestureMoved.value = true
  }
  if (!gesture.moved) return

  const nextX = clampPetX(gesture.startPetX + dx)
  // 桌宠只能被拎离地面，向下拖不再扩大底部窗口或把主体压出可见区域。
  const nextLift = Math.min(
    0,
    Math.max(
      -(getViewportHeight() - PET_GROUND_PADDING - petMetrics.value.height),
      gesture.startLift + dy
    )
  )
  // 拖拽和漫游共用同一条非响应式位置通道；松手时再提交，避免 pointermove
  // 高频触发整个 Vue 组件树更新。
  setRenderedPetPosition(nextX, nextLift)
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
      setRenderedPetPosition(clampPetX(gesture.startPetX), gesture.startLift)
      commitPetPosition()
    } else {
      commitPetPosition()
    }
    if (!cancelled) animatePetDrop()
    dragGestureMoved.value = false
    syncPetWindowModeAtPointer(event)
    scheduleNextAmbientBehavior()
    return
  }
  dragGestureMoved.value = false
  queuePetting()
  syncPetWindowModeAtPointer(event)
}

function handlePetKeydown(event: KeyboardEvent): void {
  if (event.key !== 'Enter' && event.key !== ' ') return
  event.preventDefault()
  cancelContextMenuClose()
  hoveringPet.value = true
  triggerPetSquash()
  requestPetWindowMode('keyboard')
  queuePetting()
}

function handlePetViewportResize(): void {
  stopPetRoaming()
  renderedPetX = clampPetX(renderedPetX)
  // 窗口变矮时收回超出上边界的拖拽位置，避免宠物主体或气泡永久落在不可见区域。
  renderedPetLift = Math.max(getPetMinLift(), Math.min(0, renderedPetLift))
  commitPetPosition()
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

function buildDreamImageProviderReference(current: PetSnapshot): PetProviderReference | null {
  const platform = current.dream.imageProviderPlatform?.trim() ?? ''
  const providerId = current.dream.imageProviderId?.trim() ?? ''
  const model = current.dream.imageModelId?.trim() ?? ''
  // 梦境文字和梦境图片是两条独立能力链；图片模型的契约、路由和计费能力不能由 chat provider 代替。
  // 因此这里只使用 snapshot.dream 的完整三元引用，不读取或复制 API Key，也不隐式回退到聊天配置。
  if (!platform || !providerId || !model) return null
  return {
    platform,
    providerId,
    model,
    capability: 'image',
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
  clearPetBubble('dream')
  clearDreamAudio()
}

function clearProactivePresentation(): void {
  clearPetBubble('proactive')
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
      const recorded = await petApi.recordProactiveState(props.petId, timestamp)
      applyRuntimeState(recorded)
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
    showPetBubble(text, 'muted', 'proactive', undefined, undefined, true)
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
  const provider = buildDreamImageProviderReference(current)
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

    showPetBubble(
      dream.sleepTalk,
      'muted',
      'dream',
      Math.max(5_000, current.dream.bubbleMinDurationSeconds * 1_000 + countUnicodeCharacters(dream.sleepTalk) * PET_DREAM_READING_MS_PER_CHARACTER),
      image?.dataUrl
    )
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
      // 后端事件可能早于前端进入 dozing；此处不再播放动作反馈，但仍应用
      // 动作结果携带的轻量状态，不回读完整快照。
      applyRuntimeState(actionResult.state)
      return
    }
    const technicalError = typeof payload.error === 'string' ? payload.error.trim() : ''
    if (technicalError) {
      showNotice(t('pet.window.feedback.scheduledActionFailed'), 'error')
    } else if (actionResult.ok === false) {
      showNotice(getFailureMessage(typeof actionResult.reason === 'string' ? actionResult.reason as PetActionFailureReason : undefined), 'error')
    } else {
      // 调度动作成功由 transient 动画和轻量 state 表达；不再额外冒泡任务完成文案。
      triggerTransient(action as PetInteractionAction)
    }
    if (!applyRuntimeState(actionResult.state)) {
      // 旧宿主的 scheduler 事件没有 state 时才走兼容读取，正式链路不会命中。
      await loadSnapshot()
    }
    return
  }

  // 兼容只投递 payload、由 renderer 执行动作的旧宿主；带 result 的新宿主不会走到这里。
  void runAction(action as PetInteractionAction, true)
}

async function handlePetRuntimeEvent(value: unknown): Promise<void> {
  const payload = decodeEventPayload(value)
  if (!matchesPetEvent(payload)) return
  const previousLevel = level.value
  const applied = applyRuntimeState(payload.snapshot ?? payload.state)
  if (!applied) await loadSnapshot()
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

async function handlePetSettingsUpdated(value: unknown): Promise<void> {
  const payload = decodeEventPayload(value)
  if (!matchesPetEvent(payload)) return
  // 设置页保存的是低频配置；事件只负责通知当前桌宠重新 hydration，atlas
  // 缓存同步失效，避免继续使用旧皮肤或旧主动互动配置。
  petApi.invalidateAtlas(props.petId)
  runtimeAtlasKey = ''
  await loadSnapshot()
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
  showPetBubble(message, tone, 'notice')
}

function handlePetChatStatus(payload: { text: string; tone: 'muted' | 'error' }): void {
  if (!payload.text.trim()) {
    if (petBubble.value?.source === 'chat') clearPetBubble('chat')
    return
  }
  showPetBubble(payload.text, payload.tone, 'chat', payload.text === t('pet.chat.status.connecting') ? 60_000 : undefined)
}

function handlePetChatBubble(payload: { text: string; tone: 'muted' | 'error'; duration?: number }): void {
  showPetBubble(payload.text, payload.tone, 'chat', payload.duration)
}

async function endWorkEarly(): Promise<void> {
  const current = state.value?.awayTask
  if (!current || current.kind !== 'work' || actionBusy.value) return
  actionBusy.value = 'work'
  try {
    const result = await petApi.endWorkEarly(props.petId, Date.now())
    // 结束 work 会同步清掉 awayTask；动作结果直接携带轻量 state，不能再让
    // 右下角胶囊依赖完整快照轮询才能消失。
    if (result.snapshot) snapshot.value = result.snapshot
    else if (result.state) applyRuntimeState(result.state)
    else await loadSnapshot()
    runtimeMode.value = petApi.getRuntimeMode()
    // 提前结束成功通过 awayTask 清除和状态更新表达；失败仍需明确告诉用户。
    if (!result.ok) showNotice(getFailureMessage(result.reason), 'error')
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : String(error)
    showNotice(t('pet.window.feedback.actionFailed'), 'error')
  } finally {
    actionBusy.value = null
  }
}

let runtimeAtlasKey = ''

function mergeRuntimeSnapshot(runtime: PetRuntimeSnapshot): void {
  const current = snapshot.value
  snapshot.value = {
    state: runtime.state,
    experience: runtime.experience,
    window: runtime.window,
    care: runtime.care,
    agent: runtime.agent,
    dream: runtime.dream,
    plans: current?.plans ?? [],
    dreams: current?.dreams ?? [],
    memories: current?.memories ?? [],
    skinSelection: runtime.skinSelection,
    skins: current?.skins ?? [],
    atlas: remoteAtlas.value ?? current?.atlas ?? null
  }
}

function applyRuntimeState(value: unknown): boolean {
  if (!snapshot.value || !isRecord(value)) return false
  try {
    const nextState = normalizePetRuntimeState(value, props.petId)
    snapshot.value = { ...snapshot.value, state: nextState }
    return true
  } catch {
    return false
  }
}

async function refreshPetAtlas(runtime: PetRuntimeSnapshot, generation: number, force = false): Promise<void> {
  if (props.atlasManifest && props.atlasImageUrl) return
  const skinKey = runtime.skinSelection.activeSkinId?.trim() || 'default'
  if (!force && runtimeAtlasKey === skinKey && remoteAtlas.value) return
  runtimeAtlasKey = skinKey
  // 皮肤 ID 变化时先清掉旧资源，不能在新 atlas 到达前短暂显示上一只皮肤。
  remoteAtlas.value = null
  if (snapshot.value) snapshot.value = { ...snapshot.value, atlas: null }
  try {
    const asset = await petApi.getAtlas(props.petId, skinKey)
    if (generation !== snapshotGeneration || props.petId !== runtime.state.id) return
    remoteAtlas.value = asset
    atlasImageFailed.value = false
  } catch (error) {
    // atlas 是展示增强层；资源入口失败时保留 bundled atlas 或透明占位，
    // 不能把状态 hydration 误报为失败，也不能恢复高频完整快照轮询。
    console.warn('[Pet] atlas load failed:', error)
  }
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
    const next = await petApi.getRuntimeSnapshot(requestedPetId)
    if (generation !== snapshotGeneration || requestedPetId !== props.petId) return
    mergeRuntimeSnapshot(next)
    runtimeMode.value = petApi.getRuntimeMode()
    phase.value = 'ready'
    errorMessage.value = ''
    if (readyPresentationPetId !== requestedPetId) {
      readyPresentationPetId = requestedPetId
      // hydration 完成后先给一次问候，后续动作、梦话和聊天再覆盖同一个气泡 owner。
      showPetBubble(t('pet.window.ambient.welcome'), 'muted', 'ambient', 4_500)
    }
    if (lifecycleInitializedPetId !== requestedPetId && snapshot.value) {
      void settlePetLifecycleFeedback(snapshot.value, generation)
    }
    void refreshPetAtlas(next, generation)
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

function tickLocalRuntimeState(): void {
  if (phase.value !== 'ready' || !snapshot.value) return
  const current = snapshot.value.state
  const next = settlePetRuntimeState(current, Date.now())
  if (
    next.lastTickAt === current.lastTickAt &&
    next.hunger === current.hunger &&
    next.cleanliness === current.cleanliness &&
    next.mood === current.mood &&
    next.sleeping === current.sleeping &&
    next.sleepEndsAt === current.sleepEndsAt
  ) {
    return
  }
  snapshot.value = { ...snapshot.value, state: next }
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
  const durationByAction: Partial<Record<PetInteractionAction, number>> = {
    // OpenCowork 的照料表现不是统一短闪；时长差异是动作反馈的一部分。
    feed: 2_800,
    bathe: 2_800,
    soak: 6_000,
    play: 2_600
  }
  window.setTimeout(() => {
    if (token !== transientToken) return
    transientAction.value = null
    scheduleNextAmbientBehavior()
  }, durationByAction[action] ?? 1_500)
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
  const optimisticVisual = action === 'feed' || action === 'bathe' || action === 'soak' || action === 'play'
  if (optimisticVisual) triggerTransient(action)
  try {
    const result = await petApi.performAction(props.petId, action)
    if (result.snapshot) snapshot.value = result.snapshot
    else if (result.state) applyRuntimeState(result.state)
    runtimeMode.value = petApi.getRuntimeMode()
    if (!result.ok) {
      if (optimisticVisual) {
        // 业务拒绝时撤销尚未结束的乐观动作，避免“金币不够但还在吃饭”的状态欺骗用户。
        transientToken += 1
        transientAction.value = null
      }
      showNotice(getFailureMessage(result.reason), 'error')
    }
  } catch (error) {
    if (optimisticVisual) {
      transientToken += 1
      transientAction.value = null
    }
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
  () => Boolean(snapshot.value?.state.awayTask),
  (away, previousAway) => {
    if (!away) {
      // 进入 away 时会清掉所有旧计时器；只有任务真正结束后才恢复漫游，
      // 避免任务胶囊存在期间不断排队无效行为，也避免结束瞬间没有下一次排期。
      if (previousAway) scheduleNextAmbientBehavior()
      return
    }
    // 进入 away 后精灵会卸载；所有仍指向旧精灵的视觉和交互状态必须同时撤销，
    // 否则漫游帧、菜单焦点或透明窗口模式会在任务期间继续拦截用户桌面。
    stopAmbientBehavior()
    contextMenuOpen.value = false
    chatOpen.value = false
    hoveringPet.value = false
    dragging.value = false
    dragGestureMoved.value = false
    clearPetBubble()
    requestPetWindowMode('passive')
  }
)

watch(
  petAnchorRef,
  () => {
    // away 状态会卸载并重新创建锚点；模板 ref 恢复后补写当前的非响应式位置。
    applyPetAnchorTransform()
  },
  { flush: 'post' }
)

watch(
  () => props.petId,
  (petId, previousPetId) => {
    if (!previousPetId || petId === previousPetId) return
    // 宠物切换时让旧快照和旧生命周期请求失去写入资格；新的快照完成后再启动一次检查。
    snapshotGeneration += 1
    petAudioPlayer.stop()
    lifecycleInitializedPetId = ''
    readyPresentationPetId = ''
    remoteAtlas.value = null
    runtimeAtlasKey = ''
    snapshot.value = null
    phase.value = 'loading'
    refreshRequested = true
    void loadSnapshot(true)
  }
)

onMounted(async () => {
  petWindowUnmounted = false
  applyPetAnchorTransform()
  requestPetWindowMode('passive', true)
  stopPetActionEvent = Events.On('pet.action', (event) => {
    void handlePetActionEvent(event.data)
  })
  stopPetRuntimeEvent = Events.On('pet.runtime', (event) => {
    void handlePetRuntimeEvent(event.data)
  })
  stopPetSettingsEvent = Events.On(PET_SETTINGS_UPDATED_EVENT, (event) => {
    void handlePetSettingsUpdated(event.data)
  })
  stopPetReminderEvent = Events.On('pet.reminder', (event) => handlePetReminderEvent(event.data))
  stopPetAudioEvent = Events.On('pet.audio', (event) => {
    // Wails event 的业务 payload 位于 event.data；播放器内部再按 requestId/sequence 校验。
    petAudioPlayer.handleEvent(event.data, props.petId)
  })
  stopPetPointerEvent = Events.On(PET_WINDOW_POINTER_EVENT, (event) => handleNativePetPointer(event.data))
  await Promise.all([loadSnapshot(true), loadBundledAtlas()])
  clockTimer = window.setInterval(() => {
    now.value = Date.now()
  }, 500)
  window.addEventListener('resize', handlePetViewportResize)
  handlePetViewportResize()
  idleTimer = window.setInterval(() => {
    void pollPetWindowIdle()
  }, PET_IDLE_CHECK_INTERVAL_MS)
  void pollPetWindowIdle()
  runtimeTickTimer = window.setInterval(tickLocalRuntimeState, PET_RUNTIME_TICK_INTERVAL_MS)
  scheduleNextAmbientBehavior()
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
  if (runtimeTickTimer !== undefined) window.clearInterval(runtimeTickTimer)
  if (clockTimer !== undefined) window.clearInterval(clockTimer)
  if (ambientTimer !== undefined) window.clearTimeout(ambientTimer)
  if (idleTimer !== undefined) window.clearInterval(idleTimer)
  if (reportTimer !== undefined) window.clearTimeout(reportTimer)
  if (proactiveTimer !== undefined) window.clearInterval(proactiveTimer)
  stopAmbientBehavior()
  stopPetRoaming()
  cancelContextMenuClose()
  if (squashTimer !== undefined) window.clearTimeout(squashTimer)
  squashTimer = undefined
  if (liftFrame !== undefined) window.cancelAnimationFrame(liftFrame)
  liftFrame = undefined
  window.removeEventListener('resize', handlePetViewportResize)
  clearPetBubble()
  transientToken += 1
  stopPetActionEvent?.()
  stopPetRuntimeEvent?.()
  stopPetReminderEvent?.()
  stopPetAudioEvent?.()
  stopPetPointerEvent?.()
  stopPetSettingsEvent?.()
  stopPetActionEvent = null
  stopPetRuntimeEvent = null
  stopPetReminderEvent = null
  stopPetAudioEvent = null
  stopPetPointerEvent = null
  stopPetSettingsEvent = null
  stopProactiveSession()
  stopDreamSession()
  petAudioPlayer.dispose()
})
</script>

<template>
  <div
    class="pet-window"
    :class="{ 'is-error': phase === 'error', 'is-busy': Boolean(actionBusy || state?.awayTask) }"
    @pointerover="handleWindowPointerOver"
    @pointerleave="handleWindowPointerLeave"
    @focusin="handleWindowFocusIn"
    @focusout="handleWindowFocusOut"
    @keydown.esc="chatOpen ? closePetChat() : contextMenuOpen && toggleContextMenu()"
  >
    <main class="pet-window__scene">
      <div
        v-if="!state?.awayTask"
        ref="petAnchorRef"
        class="pet-window__pet-anchor"
        :style="petAnchorStyle"
      >
        <!-- OpenCowork 的气泡、HUD 与精灵共享这个移动容器，状态反馈不会脱离宠物。 -->
        <article
          v-if="activeBubble"
          class="pet-window__bubble"
          :class="[`is-${activeBubble.tone}`, { 'is-interactive': activeBubble.interactive || activeBubble.imageUrl, 'has-image': activeBubble.imageUrl }]"
          :style="petBubbleStyle"
          aria-live="polite"
          @click="activeBubble.interactive ? openPetChat() : undefined"
        >
          <img
            v-if="activeBubble.imageUrl"
            class="pet-window__dream-image"
            :src="activeBubble.imageUrl"
            :alt="t('pet.window.dreamImageAlt')"
            role="button"
            tabindex="0"
            :title="t('pet.window.dreamImageOpen')"
            @click.stop="openPetBubbleImage"
            @keydown.enter.stop.prevent="openPetBubbleImage"
            @keydown.space.stop.prevent="openPetBubbleImage"
          />
          <p>
            <template v-if="activeBubbleTextParts.emphasized">
              {{ activeBubbleTextParts.before }}<strong>{{ activeBubbleTextParts.emphasized }}</strong>{{ activeBubbleTextParts.after }}
            </template>
            <template v-else>{{ activeBubble.text }}</template>
          </p>
        </article>
        <article
          v-else-if="hoveringPet && !contextMenuOpen && phase === 'ready'"
          class="pet-window__hud"
          :style="petHudStyle"
          aria-hidden="true"
        >
          <header class="pet-window__hud-header">
            <span>{{ petName }} · Lv.{{ level }}</span>
            <span class="pet-window__hud-coins">◈ {{ coins }}</span>
          </header>
          <div v-for="item in statItems" :key="item.key" class="pet-window__hud-stat">
            <span>{{ item.label }}</span>
            <span class="pet-window__hud-track">
              <span
                class="pet-window__hud-fill"
                :class="[`is-${item.tone}`, { 'is-low': item.value < 30 }]"
                :style="{ width: `${Math.max(0, Math.min(100, item.value))}%` }"
              ></span>
            </span>
            <span>{{ Math.round(item.value) }}</span>
          </div>
        </article>

        <div
          class="pet-window__pet-stage"
          :class="{
            'is-sleeping': state?.sleeping,
            'is-dragging': dragging
          }"
          :style="{ width: `${petMetrics.width}px`, height: `${petMetrics.height}px` }"
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
          <div class="pet-window__pet-visual" :class="{ 'is-squashing': squashing }">
            <PetAtlasFrame
              v-if="atlas && !atlasImageFailed"
              :image-url="atlas.src"
              :manifest="atlas.manifest"
              :behavior="atlasBehavior"
              :scale="props.scale"
              :display-height="petDisplayHeight"
              :mood="state?.mood ?? 100"
              :cleanliness="state?.cleanliness ?? 100"
              :playing="true"
              :flip-x="petFacingLeft"
              @metrics-change="handlePetMetricsChange"
              @asset-error="handlePetAtlasError"
              @asset-ready="handlePetAtlasReady"
            />
            <div v-else class="pet-window__atlas-placeholder" :aria-label="t('pet.window.atlasWaiting')">
              <span class="pet-window__placeholder-glyph">✦</span>
              <span>{{ t('pet.window.atlasWaiting') }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- away 是窗口级状态：原版卸载精灵，右下角只保留可交互的任务胶囊。 -->
      <div
        v-if="state?.awayTask"
        class="pet-window__away-pill"
        :class="{ 'is-actionable': state.awayTask.kind === 'work' }"
        :style="awayPillStyle"
        @dblclick="state.awayTask.kind === 'work' ? endWorkEarly() : undefined"
        @pointerenter="requestPetWindowMode('interactive')"
        @pointerleave="handleWindowPointerLeave"
      >
        <span class="pet-window__away-icon" aria-hidden="true">
          <Briefcase v-if="state.awayTask.kind === 'work'" :size="14" :stroke-width="1.8" />
          <GraduationCap v-else :size="14" :stroke-width="1.8" />
        </span>
        <span>{{ state.awayTask.kind === 'work' ? t('pet.window.status.working', { time: formatCountdown(awayRemaining) }) : t('pet.window.status.studying', { time: formatCountdown(awayRemaining) }) }}</span>
      </div>

      <aside
        v-if="contextMenuOpen"
        class="pet-window__context-menu"
        :style="petMenuStyle"
        :aria-label="t('pet.window.contextMenu.label')"
        @pointerenter="cancelContextMenuClose(); requestPetWindowMode('interactive')"
        @pointerleave="scheduleContextMenuClose"
        @pointerdown.stop
      >
        <header class="pet-window__context-menu-header">
          <strong class="pet-window__menu-heading">{{ petName }} · Lv.{{ level }}</strong>
          <span class="pet-window__coins" :title="t('pet.window.coins')" :aria-label="t('pet.window.coins')">
            <Coins :size="12" :stroke-width="2" aria-hidden="true" />
            <span>{{ Math.floor(coins) }}</span>
          </span>
        </header>
        <div v-if="phase === 'ready'" class="pet-window__context-summary">
          <div class="pet-window__experience">
            <div class="pet-window__experience-row">
              <Sparkles :size="12" :stroke-width="2" class="pet-window__experience-icon" aria-hidden="true" />
              <div class="pet-window__progress-track" role="progressbar" :aria-valuenow="levelProgressPercent" aria-valuemin="0" aria-valuemax="100">
                <span class="pet-window__progress-fill is-experience" :style="{ width: `${levelProgressPercent}%` }"></span>
              </div>
            </div>
          </div>
          <div class="pet-window__stats">
            <div v-for="item in statItems" :key="item.key" class="pet-window__stat">
              <span class="pet-window__stat-label">{{ item.label }}</span>
              <div class="pet-window__progress-track" role="progressbar" :aria-valuenow="Math.round(item.value)" aria-valuemin="0" aria-valuemax="100">
                <span
                  class="pet-window__progress-fill"
                  :class="[`is-${item.tone}`, { 'is-low': item.value < 30 }]"
                  :style="{ width: `${Math.max(0, Math.min(100, item.value))}%` }"
                ></span>
              </div>
              <span class="pet-window__stat-value">{{ Math.round(item.value) }}</span>
            </div>
          </div>
        </div>
        <div class="pet-window__menu-grid" :aria-label="t('pet.window.actionsLabel')">
          <button
            v-for="button in actionButtons"
            :key="button.action"
            type="button"
            class="pet-window__menu-action"
            :class="{ 'is-active': actionBusy === button.action }"
            :disabled="Boolean(button.lockedLevel) || isActionDisabled(button.action)"
            :title="button.label"
            @click="runAction(button.action)"
          >
            <span class="pet-window__menu-icon" aria-hidden="true">
              <component :is="button.icon" :size="14" :stroke-width="2" />
            </span>
            <span class="pet-window__menu-label">{{ button.label }}</span>
            <span v-if="button.lockedLevel" class="pet-window__menu-meta">
              <Lock :size="10" :stroke-width="2" />
              <span>{{ t('pet.window.requiresLevel', { level: button.lockedLevel }) }}</span>
            </span>
            <span v-else-if="button.cost" class="pet-window__menu-meta">-{{ button.cost }}</span>
          </button>
          <button type="button" class="pet-window__menu-action" @click="runContextMenuAction('chat')">
            <span class="pet-window__menu-icon" aria-hidden="true">
              <MessageCircle :size="14" :stroke-width="2" />
            </span>
            <span class="pet-window__menu-label">{{ t('pet.window.contextMenu.chat') }}</span>
          </button>
          <button type="button" class="pet-window__menu-action is-muted" @click="runContextMenuAction('studio')">
            <span class="pet-window__menu-icon" aria-hidden="true">
              <Wand2 :size="14" :stroke-width="2" />
            </span>
            <span class="pet-window__menu-label">{{ t('pet.window.contextMenu.studio') }}</span>
          </button>
          <button type="button" class="pet-window__menu-action is-muted" @click="runContextMenuAction('hide')">
            <span class="pet-window__menu-icon" aria-hidden="true">
              <EyeOff :size="14" :stroke-width="2" />
            </span>
            <span class="pet-window__menu-label">{{ t('pet.window.contextMenu.hide') }}</span>
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
    </main>

    <section
      v-if="chatOpen"
      class="pet-window__chat-panel"
      :style="petChatStyle"
      :aria-label="t('pet.chat.aria.chatPanel')"
      @pointerenter="requestPetWindowMode('keyboard')"
      @pointerleave="handleWindowPointerLeave"
      @pointerdown.stop
    >
      <PetChat
        :pet-id="props.petId"
        :pet-name="petName"
        :agent="snapshot?.agent ?? null"
        :dreams="snapshot?.dreams ?? []"
        :provider-platform="snapshot?.agent.providerPlatform ?? props.providerPlatform"
        @status-change="handlePetChatStatus"
        @bubble="handlePetChatBubble"
        @close="closePetChat"
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
  --pet-menu-hover: #ececef;
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
  /* OpenCowork 的 PetView 继承应用级字体链；这里显式固定，避免宿主项目的 SF Pro 回退链改变中文宽度。 */
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
  user-select: none;
  pointer-events: none;
}

:global(html.dark) .pet-window {
  --pet-menu-hover: #303030;
}

.pet-window__context-menu-header {
  display: flex;
  min-width: 0;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 0 6px 4px;
}

.pet-window__menu-heading {
  min-width: 0;
  overflow: hidden;
  color: var(--pet-ink);
  font-size: 12px;
  font-weight: 600;
  line-height: 16px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-window__coins {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 4px;
  color: #f59e0b;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  line-height: 16px;
  white-space: nowrap;
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

.pet-window__pet-anchor {
  position: absolute;
  display: block;
  min-width: 1px;
  min-height: 1px;
  overflow: visible;
  pointer-events: none;
  /* 位置由动画循环直接写入 transform；不要让浏览器把每帧移动当成布局变化。 */
  will-change: transform;
}

.pet-window__bubble {
  position: absolute;
  left: 50%;
  bottom: 0;
  display: block;
  width: max-content;
  max-width: min(208px, calc(100vw - 24px));
  min-width: 0;
  margin: 0;
  box-sizing: border-box;
  transform: translateX(-50%);
  border: 1px solid var(--pet-line);
  border-radius: 16px;
  padding: 6px 12px;
  background: color-mix(in srgb, var(--mac-surface, #fff) 95%, transparent);
  box-shadow: 0 10px 15px -3px color-mix(in srgb, #000 10%, transparent),
    0 4px 6px -4px color-mix(in srgb, #000 10%, transparent);
  backdrop-filter: blur(12px);
  color: var(--pet-ink);
  font-size: 12px;
  line-height: 16px;
  text-align: center;
  pointer-events: none;
  animation: pet-window__bubble-in 0.24s cubic-bezier(0.22, 1, 0.36, 1);
}

.pet-window__hud {
  position: absolute;
  left: 50%;
  bottom: 0;
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
  box-sizing: border-box;
  transform: translateX(-50%);
  border: 1px solid var(--pet-line);
  border-radius: 12px;
  padding: 8px;
  background: color-mix(in srgb, var(--mac-surface, #fff) 95%, transparent);
  box-shadow: 0 10px 15px -3px color-mix(in srgb, #000 10%, transparent),
    0 4px 6px -4px color-mix(in srgb, #000 10%, transparent);
  backdrop-filter: blur(12px);
  color: var(--pet-ink);
  font-size: 11px;
  line-height: 16px;
  pointer-events: none;
}

.pet-window__hud-header,
.pet-window__hud-stat {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.pet-window__hud-header {
  justify-content: space-between;
  gap: 6px;
  color: var(--pet-ink);
  font-size: 11px;
  font-weight: 500;
}

.pet-window__hud-header > span:first-child,
.pet-window__hud-stat > span:first-child {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-window__hud-coins {
  flex: 0 0 auto;
  color: #b77a18;
  font-variant-numeric: tabular-nums;
}

.pet-window__hud-stat > span:first-child {
  width: 40px;
  flex: 0 0 auto;
  color: var(--pet-muted);
}

.pet-window__hud-stat > span:last-child {
  width: 28px;
  flex: 0 0 auto;
  color: var(--pet-muted);
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.pet-window__hud-track {
  display: block;
  min-width: 0;
  height: 6px;
  flex: 1 1 auto;
  overflow: hidden;
  border-radius: 999px;
  background: color-mix(in srgb, var(--pet-muted) 13%, transparent);
}

.pet-window__hud-fill {
  display: block;
  height: 100%;
  border-radius: inherit;
  background: #d99b43;
}

.pet-window__hud-fill.is-hunger { background: #d99b43; }
.pet-window__hud-fill.is-cleanliness { background: #5caaa2; }
.pet-window__hud-fill.is-mood { background: #c87e9c; }
.pet-window__hud-fill.is-low { background: #cb5a54; }

.pet-window__bubble.is-interactive {
  pointer-events: auto;
  cursor: pointer;
}

.pet-window__bubble p {
  margin: 0;
  overflow-wrap: break-word;
  white-space: pre-wrap;
}

.pet-window__bubble.has-image {
  width: 208px;
}

.pet-window__dream-image {
  display: block;
  width: 100%;
  max-height: 112px;
  margin-bottom: 6px;
  border-radius: 6px;
  background: color-mix(in srgb, var(--pet-muted) 8%, transparent);
  object-fit: contain;
}

.pet-window__pet-stage {
  position: absolute;
  left: 0;
  bottom: 0;
  display: flex;
  width: var(--pet-stage-width, 132px);
  height: var(--pet-stage-height, 120px);
  min-width: 0;
  min-height: 0;
  align-items: flex-end;
  justify-content: center;
  overflow: visible;
  pointer-events: auto;
  cursor: pointer;
  outline: none;
  touch-action: none;
  transition: bottom 0.18s ease;
  will-change: left, bottom;
}

/* 定位盒只负责命中、拖拽和屏幕边界；视觉层单独变换，避免挤压/回弹改变落地点。 */
.pet-window__pet-visual {
  position: relative;
  display: flex;
  width: 100%;
  height: 100%;
  align-items: flex-end;
  justify-content: center;
  transform-origin: center bottom;
  pointer-events: none;
}

.pet-window__pet-stage.is-dragging {
  cursor: grabbing;
  transition: none;
}

.pet-window__pet-stage:hover {
  filter: saturate(1.04) brightness(1.02);
}

.pet-window__pet-visual.is-squashing {
  animation: pet-window__pet-squash 0.45s ease-out;
}

@keyframes pet-window__bubble-in {
  from { opacity: 0; transform: translate(-50%, 8px) scale(0.9); }
  to { opacity: 1; transform: translate(-50%, 0) scale(1); }
}

@keyframes pet-window__pet-squash {
  0% { transform: scale(1, 1); }
  38% { transform: scale(1.08, 0.86); }
  72% { transform: scale(0.96, 1.06); }
  100% { transform: scale(1, 1); }
}

.pet-window__pet-stage:focus-visible {
  border-radius: 12px;
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--mac-accent, #0a84ff) 25%, transparent);
}

.pet-window__pet-stage.is-sleeping {
  filter: saturate(0.92);
}

.pet-window__away-pill {
  position: absolute;
  z-index: 5;
  display: inline-flex;
  max-width: min(280px, calc(100vw - 40px));
  min-height: 32px;
  align-items: center;
  gap: 8px;
  box-sizing: border-box;
  border: 1px solid var(--pet-line);
  border-radius: 999px;
  padding: 7px 12px;
  background: color-mix(in srgb, var(--mac-surface, #fff) 94%, transparent);
  box-shadow: 0 8px 22px color-mix(in srgb, #243247 16%, transparent);
  backdrop-filter: blur(14px);
  color: var(--pet-ink);
  font-size: 12px;
  line-height: 16px;
  pointer-events: auto;
  user-select: none;
  white-space: nowrap;
}

.pet-window__away-pill.is-actionable {
  cursor: pointer;
}

.pet-window__away-pill.is-actionable:hover {
  border-color: color-mix(in srgb, var(--mac-accent, #0a84ff) 42%, var(--pet-line));
}

.pet-window__away-icon {
  display: inline-flex;
  width: 18px;
  height: 18px;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
  background: color-mix(in srgb, var(--mac-accent, #0a84ff) 12%, transparent);
  color: var(--mac-accent, #0a84ff);
  font-size: 10px;
  font-weight: 700;
}

.pet-window__away-icon svg {
  display: block;
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

.pet-window__context-menu {
  display: flex;
  width: min(100%, var(--pet-context-menu-width, 236px));
  min-width: 0;
  flex-direction: column;
  gap: 0;
  margin: 0;
  box-sizing: border-box;
  border: 1px solid var(--pet-line);
  border-radius: 12px;
  padding: 8px;
  background: color-mix(in srgb, var(--mac-surface, #fff) 95%, transparent);
  box-shadow: 0 20px 25px -5px color-mix(in srgb, #000 10%, transparent),
    0 8px 10px -6px color-mix(in srgb, #000 10%, transparent);
  backdrop-filter: blur(14px);
}

/* OpenCowork 依赖 Tailwind preflight 的 border-box；独立宠物窗还会加载
   public/style.css 的全局 button 规则，因此必须在菜单边界内显式恢复同一盒模型，
   否则 padding 会额外撑高每一行，最终把五行菜单挤成滚动区域。 */
.pet-window__context-menu,
.pet-window__context-menu * {
  box-sizing: border-box;
}

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

.pet-window__context-summary {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
  padding: 0 6px 8px;
}

.pet-window__experience-row {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.pet-window__experience-icon {
  flex: 0 0 auto;
  color: #a78bfa;
}

.pet-window__stats {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.pet-window__stat {
  display: flex;
  align-items: center;
  gap: 8px;
}

.pet-window__stat-label {
  width: 40px;
  flex: 0 0 auto;
  overflow: hidden;
  color: var(--pet-muted);
  font-size: 11px;
  line-height: 16px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-window__stat-value {
  width: 28px;
  flex: 0 0 auto;
  color: var(--pet-muted);
  font-size: 11px;
  line-height: 16px;
  text-align: right;
  font-variant-numeric: tabular-nums;
}

.pet-window__progress-track {
  display: block;
  min-width: 0;
  height: 6px;
  flex: 1 1 auto;
  overflow: hidden;
  border-radius: 999px;
  background: color-mix(in srgb, var(--pet-muted) 13%, transparent);
}

.pet-window__progress-fill {
  display: block;
  height: 100%;
  border-radius: inherit;
  background: #a78bfa;
  transition: width 0.25s ease, background 0.25s ease;
}

.pet-window__progress-fill.is-experience {
  background: #a78bfa;
}

.pet-window__progress-fill.is-hunger {
  background: #fbbf24;
}

.pet-window__progress-fill.is-cleanliness {
  background: #38bdf8;
}

.pet-window__progress-fill.is-mood {
  background: #f472b6;
}

.pet-window__progress-fill.is-low {
  background: #f87171;
}

.pet-window__menu-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  grid-auto-rows: 28px;
  gap: 4px;
  border-top: 1px solid color-mix(in srgb, var(--pet-line) 60%, transparent);
  padding-top: 8px;
}

.pet-window__menu-action {
  display: flex;
  width: 100%;
  min-width: 0;
  height: 28px;
  min-height: 28px;
  align-items: center;
  gap: 6px;
  border: 0;
  border-radius: 8px;
  padding: 6px 8px;
  background: transparent;
  color: var(--pet-ink);
  cursor: pointer;
  font: inherit;
  font-size: 12px;
  line-height: 16px;
  margin: 0;
  text-align: left;
  transition: background 0.18s ease, color 0.18s ease;
}

.pet-window__menu-action:disabled {
  cursor: not-allowed;
  opacity: 0.45;
}

.pet-window__menu-action.is-muted {
  color: var(--pet-muted);
}

.pet-window__menu-action:hover:not(:disabled),
.pet-window__menu-action:focus-visible,
.pet-window__menu-action.is-active {
  background: var(--pet-menu-hover);
  color: var(--pet-ink);
  outline: none;
}

.pet-window__menu-icon {
  display: inline-flex;
  width: 14px;
  height: 14px;
  flex: 0 0 14px;
  align-items: center;
  justify-content: center;
  color: var(--pet-muted);
}

.pet-window__menu-label {
  min-width: 0;
  flex: 1 1 auto;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-window__menu-meta {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 2px;
  color: var(--pet-muted);
  font-size: 10px;
  line-height: 13px;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.pet-window__context-menu {
  position: absolute;
  left: 0;
  bottom: 0;
  z-index: 4;
  display: flex;
  width: min(var(--pet-context-menu-width, 236px), calc(100vw - 24px));
  /* 菜单固定为五行；不让窗口高度约束把完整动作列表压成内部滚动区域。 */
  max-height: none;
  margin: 0;
  overflow: visible;
  pointer-events: auto;
  transform: none;
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

@media (max-width: 520px) {
  .pet-window__bubble {
    max-width: calc(100vw - 24px);
  }
}

.pet-window__inline-state.is-error {
  pointer-events: auto;
}

.pet-window__chat-panel {
  position: absolute;
  left: 0;
  bottom: 0;
  z-index: 8;
  display: flex;
  width: min(292px, calc(100vw - 24px));
  min-width: 0;
  min-height: 0;
  box-sizing: border-box;
  pointer-events: auto;
}

.pet-window > .pet-window__chat-panel {
  pointer-events: auto;
}

.pet-window__chat-panel :deep(.pet-chat) {
  width: 100%;
  height: auto;
  max-width: none;
  max-height: none;
}

@media (max-width: 520px) {
  .pet-window__chat-panel {
    width: calc(100vw - 24px);
  }
}
</style>
