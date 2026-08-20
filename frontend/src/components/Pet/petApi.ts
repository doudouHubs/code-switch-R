import { Call } from '../../wails-runtime-compat'
import { isWailsRuntimeAvailable } from '../../wails-runtime-compat/runtime'
import { parsePetAtlasDocument } from './petAtlas'
import {
  DEFAULT_PET_ID,
  PET_AUTO_CARE_DEFAULT_THRESHOLD,
  PET_DREAM_DEFAULT_BUBBLE_DURATION_SECONDS,
  PET_DREAM_DEFAULT_SLEEP_TALK_LENGTH,
  PET_DREAM_MAX_BUBBLE_DURATION_SECONDS,
  PET_DREAM_MAX_SLEEP_TALK_LENGTH,
  PET_DREAM_MIN_BUBBLE_DURATION_SECONDS,
  PET_DREAM_MIN_SLEEP_TALK_LENGTH,
  getPetLevel,
  normalizePetAutoCareThreshold,
  normalizePetDreamLength,
  type PetAction,
  type PetActionFailureReason,
  type PetActionResult,
  type PetAgentConfig,
  type PetAtlasAsset,
  type PetAwayReward,
  type PetAwayTask,
  type PetCareConfig,
  type PetDreamConfig,
  type PetExperience,
  type PetInteractionAction,
  type PetMemoryRecord,
  type PetPlanRecord,
  type PetPlanSchedule,
  type PetPlanScript,
  type PetPlanStep,
  type PetProactiveFrequency,
  type PetReasoningEffort,
  type PetSettingsInput,
  type PetDreamEmotion,
  type PetDreamHistoryRecord,
  type PetSkinRecord,
  type PetSkinSelection,
  type PetRuntimeSnapshot,
  type PetSnapshot,
  type PetState,
  type PetVoiceMode,
  type PetWindowConfig
} from './petTypes'

const PET_SERVICE = 'codeswitch/services.PetService'
const PET_WINDOW_SERVICE = 'codeswitch/services.PetWindowAPI'

/**
 * 只在这里定义 Wails 方法名。后端绑定生成或方法命名调整时替换这一层即可，Vue
 * 不需要知道 runtime 的调用形式，也不会散落 window 全局调用。
 */
export const PET_RUNTIME_METHODS = {
  getSnapshot: 'GetSnapshot',
  getRuntimeSnapshot: 'GetRuntimeSnapshot',
  getAtlas: 'GetAtlas',
  performAction: 'PerformAction',
  endWorkEarly: 'EndWorkEarlyForPet',
  petted: 'PettedForPet',
  saveSettings: 'SaveSettings',
  updateName: 'UpdateName',
  recordProactive: 'RecordProactive',
  recordProactiveState: 'RecordProactiveState'
} as const

export const PET_WINDOW_RUNTIME_METHODS = {
  open: 'Open',
  close: 'Close',
  toggle: 'Toggle',
  state: 'State',
  platforms: 'GetPlatforms',
  setPlatformLayer: 'SetPlatformLayer'
} as const

export interface PetRuntimeAdapter {
  call(method: string, args: readonly unknown[]): Promise<unknown>
}

export type PetRuntimeMode = 'unknown' | 'backend' | 'fallback'

export type PetWindowRuntimeMode = 'passive' | 'interactive' | 'keyboard'

export interface PetWindowRuntimeState {
  version: number
  open: boolean
  mode: PetWindowRuntimeMode
  clickThrough: boolean
  focused: boolean
  alwaysOnTop: boolean
}

export interface PetWindowPlatformRect {
  left: number
  top: number
  right: number
  bottom: number
}

export interface PetWindowPlatform {
  id: string
  rect: PetWindowPlatformRect
  zOrder: number
}

export interface PetWindowPlatformSnapshot {
  available: boolean
  overlay: PetWindowPlatformRect
  platforms: PetWindowPlatform[]
  occluders: PetWindowPlatform[]
  movingWindowId: string
}

export interface PetApi {
  getSnapshot(petId?: string): Promise<PetSnapshot>
  getRuntimeSnapshot(petId?: string): Promise<PetRuntimeSnapshot>
  getAtlas(petId?: string, cacheKey?: string): Promise<PetAtlasAsset | null>
  invalidateAtlas(petId?: string): void
  performAction(petId: string, action: PetInteractionAction): Promise<PetActionResult>
  endWorkEarly(petId: string, now?: number): Promise<PetActionResult>
  saveSettings(petId: string, settings: PetSettingsInput): Promise<PetSnapshot>
  updateName(petId: string, name: string): Promise<PetSnapshot>
  recordProactive(petId: string, now?: number): Promise<PetSnapshot>
  recordProactiveState(petId: string, now?: number): Promise<PetState>
  setWindowEnabled(enabled: boolean): Promise<PetWindowRuntimeState>
  getWindowState(): Promise<PetWindowRuntimeState>
  getRuntimeMode(): PetRuntimeMode
}

const wailsRuntimeAdapter: PetRuntimeAdapter = {
  call(method, args) {
    // Call.ByName 在桌面端仍走 Wails，在浏览器端由兼容层改走 loopback bridge；
    // 这里不能再提前拒绝浏览器，否则真实 SQLite 快照永远到不了页面。
    const target = method.includes('.') ? method : `${PET_SERVICE}.${method}`
    return Promise.resolve(Call.ByName(target, ...args))
  }
}

const fallbackSnapshots = new Map<string, PetSnapshot>()
// atlas 是展示资源而不是运行时状态；按宠物和皮肤选择缓存，避免每次状态事件都重新
// 读取 PNG、生成 data URL 或让 WebView 重新解码同一张图片。
const atlasCache = new Map<string, PetAtlasAsset | null>()
let fallbackWindowState = createFallbackWindowState(false)

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function asRecord(value: unknown): Record<string, unknown> {
  return isRecord(value) ? value : {}
}

function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

function asFiniteNumber(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

function asNonNegativeNumber(value: unknown, fallback: number): number {
  return Math.max(0, asFiniteNumber(value, fallback))
}

function asString(value: unknown, fallback: string): string {
  return typeof value === 'string' ? value : fallback
}

function asNullableString(value: unknown, fallback: string | null): string | null {
  return value === null ? null : typeof value === 'string' ? value : fallback
}

function normalizeProjectFolder(value: unknown, fallback: string | null, present: boolean): string | null {
  if (!present) return fallback
  if (typeof value !== 'string') return null
  const trimmed = value.trim()
  return trimmed || null
}

function clamp(value: number, min = 0, max = 100): number {
  return Math.min(max, Math.max(min, value))
}

function normalizeAwayTask(value: unknown): PetAwayTask | null {
  const source = asRecord(value)
  const kind = source.kind === 'work' || source.kind === 'study' ? source.kind : null
  const startedAt = asNonNegativeNumber(source.startedAt, 0)
  const endsAt = asNonNegativeNumber(source.endsAt, 0)
  if (!kind || startedAt <= 0 || endsAt < startedAt) return null
  return { kind, startedAt, endsAt }
}

function normalizeState(value: unknown, petId: string): PetState {
  const source = asRecord(value)
  const now = Date.now()
  return {
    id: asString(source.id ?? source.petId, petId),
    name: asString(source.name, 'Kapi').trim() || 'Kapi',
    hunger: clamp(asFiniteNumber(source.hunger, 80)),
    cleanliness: clamp(asFiniteNumber(source.cleanliness, 80)),
    mood: clamp(asFiniteNumber(source.mood, 70)),
    growth: asNonNegativeNumber(source.growth, 0),
    coins: Math.floor(asNonNegativeNumber(source.coins, 120)),
    sleeping: source.sleeping === true,
    sleepEndsAt: asNonNegativeNumber(source.sleepEndsAt, 0),
    awayTask: normalizeAwayTask(source.awayTask),
    lastTickAt: asNonNegativeNumber(source.lastTickAt, now) || now,
    adoptedAt: asNonNegativeNumber(source.adoptedAt, now) || now,
    lastMilestoneDays: Math.floor(asNonNegativeNumber(source.lastMilestoneDays, 0)),
    proactiveDate: asString(source.proactiveDate, ''),
    proactiveCount: Math.floor(asNonNegativeNumber(source.proactiveCount, 0)),
    lastProactiveAt: asNonNegativeNumber(source.lastProactiveAt, 0),
    coinCreditedExp: asNonNegativeNumber(source.coinCreditedExp, 0),
    lastDailyBonusDate: asString(source.lastDailyBonusDate, '')
  }
}

function normalizeExperience(value: unknown, petId: string): PetExperience {
  const source = asRecord(value)
  return {
    petId: asString(source.petId, petId),
    totalExp: asNonNegativeNumber(source.totalExp, 0),
    totalTokens: Math.floor(asNonNegativeNumber(source.totalTokens, 0))
  }
}

function defaultWindowConfig(petId: string): PetWindowConfig {
  return { petId, enabled: true }
}

function defaultCareConfig(petId: string): PetCareConfig {
  return {
    petId,
    autoCareEnabled: false,
    autoCareThreshold: PET_AUTO_CARE_DEFAULT_THRESHOLD
  }
}

function defaultAgentConfig(petId: string): PetAgentConfig {
  return {
    petId,
    providerPlatform: null,
    providerId: null,
    modelId: null,
    reasoningEffort: null,
    systemPrompt: '',
    projectId: null,
    projectName: null,
    projectFolder: null,
    proactive: false,
    proactiveFreq: 'low',
    quietStart: 22,
    quietEnd: 9,
    voiceEnabled: false,
    voiceProviderId: null,
    voiceModelId: null,
    voice: '',
    voiceMode: 'auto',
    voiceInstruction: '',
    voiceTag: ''
  }
}

function defaultDreamConfig(petId: string): PetDreamConfig {
  return {
    petId,
    dreamEnabled: true,
    prompt: '',
    keywords: '',
    sleepTalkMinLength: PET_DREAM_DEFAULT_SLEEP_TALK_LENGTH,
    bubbleMinDurationSeconds: PET_DREAM_DEFAULT_BUBBLE_DURATION_SECONDS,
    imageProviderPlatform: null,
    imageProviderId: null,
    imageModelId: null
  }
}

export function normalizePetRuntimeState(value: unknown, petId = DEFAULT_PET_ID): PetState {
  return normalizeState(value, petId)
}

function defaultSkinSelection(petId: string): PetSkinSelection {
  return { petId, activeSkinId: null }
}

function normalizeWindowConfig(value: unknown, petId: string, fallback = defaultWindowConfig(petId)) {
  const source = asRecord(value)
  return {
    petId: asString(source.petId, fallback.petId || petId),
    enabled: typeof source.enabled === 'boolean' ? source.enabled : fallback.enabled
  }
}

function createFallbackWindowState(open: boolean): PetWindowRuntimeState {
  return {
    version: 1,
    open,
    mode: 'passive',
    clickThrough: true,
    focused: false,
    // 桌宠默认使用普通层级；站到外部窗口后再由运行时同步目标窗口层级。
    alwaysOnTop: false
  }
}

function normalizeWindowState(value: unknown): PetWindowRuntimeState {
  const source = asRecord(value)
  if (typeof source.open !== 'boolean') throw new Error('Pet window state did not contain an open flag.')
  const mode = source.mode
  if (mode !== 'passive' && mode !== 'interactive' && mode !== 'keyboard') {
    throw new Error('Pet window state contained an unsupported mode.')
  }
  return {
    version: Math.floor(asFiniteNumber(source.version, 1)),
    open: source.open,
    mode,
    clickThrough: source.clickThrough === true,
    focused: source.focused === true,
    alwaysOnTop: source.alwaysOnTop === true
  }
}

function normalizeCareConfig(value: unknown, petId: string, fallback = defaultCareConfig(petId)) {
  const source = asRecord(value)
  return {
    petId: asString(source.petId, fallback.petId || petId),
    autoCareEnabled:
      typeof source.autoCareEnabled === 'boolean' ? source.autoCareEnabled : fallback.autoCareEnabled,
    autoCareThreshold: normalizePetAutoCareThreshold(
      asFiniteNumber(source.autoCareThreshold, fallback.autoCareThreshold)
    )
  }
}

function normalizeReasoningEffort(value: unknown, fallback: PetReasoningEffort | null): PetReasoningEffort | null {
  return value === null || value === 'none' || value === 'minimal' || value === 'low' || value === 'medium' || value === 'high'
    ? (value as PetReasoningEffort | null)
    : fallback
}

function normalizeProactiveFrequency(value: unknown, fallback: PetProactiveFrequency): PetProactiveFrequency {
  return value === 'low' || value === 'medium' || value === 'high' ? value : fallback
}

function normalizeVoiceMode(value: unknown, fallback: PetVoiceMode): PetVoiceMode {
  return value === 'auto' || value === 'speech' || value === 'chat' ? value : fallback
}

function normalizeAgentConfig(value: unknown, petId: string, fallback = defaultAgentConfig(petId)): PetAgentConfig {
  const source = asRecord(value)
  return {
    petId: asString(source.petId, fallback.petId || petId),
    providerPlatform: asNullableString(source.providerPlatform, fallback.providerPlatform),
    providerId: asNullableString(source.providerId, fallback.providerId),
    modelId: asNullableString(source.modelId, fallback.modelId),
    reasoningEffort: normalizeReasoningEffort(source.reasoningEffort, fallback.reasoningEffort),
    systemPrompt: asString(source.systemPrompt, fallback.systemPrompt),
    projectId: asNullableString(source.projectId, fallback.projectId),
    projectName: asNullableString(source.projectName, fallback.projectName),
    // 旧快照缺少 projectFolder 时沿用 fallback；字段一旦存在，只允许非空字符串作为路径引用。
    projectFolder: normalizeProjectFolder(
      source.projectFolder,
      fallback.projectFolder,
      Object.prototype.hasOwnProperty.call(source, 'projectFolder')
    ),
    proactive: typeof source.proactive === 'boolean' ? source.proactive : fallback.proactive,
    proactiveFreq: normalizeProactiveFrequency(source.proactiveFreq, fallback.proactiveFreq),
    quietStart: Math.min(23, Math.max(0, Math.floor(asFiniteNumber(source.quietStart, fallback.quietStart)))),
    quietEnd: Math.min(23, Math.max(0, Math.floor(asFiniteNumber(source.quietEnd, fallback.quietEnd)))),
    voiceEnabled: typeof source.voiceEnabled === 'boolean' ? source.voiceEnabled : fallback.voiceEnabled,
    voiceProviderId: asNullableString(source.voiceProviderId, fallback.voiceProviderId),
    voiceModelId: asNullableString(source.voiceModelId, fallback.voiceModelId),
    voice: asString(source.voice, fallback.voice),
    voiceMode: normalizeVoiceMode(source.voiceMode, fallback.voiceMode),
    voiceInstruction: asString(source.voiceInstruction, fallback.voiceInstruction),
    voiceTag: asString(source.voiceTag, fallback.voiceTag)
  }
}

function normalizeDreamConfig(value: unknown, petId: string, fallback = defaultDreamConfig(petId)): PetDreamConfig {
  const source = asRecord(value)
  return {
    petId: asString(source.petId, fallback.petId || petId),
    dreamEnabled: typeof source.dreamEnabled === 'boolean' ? source.dreamEnabled : fallback.dreamEnabled,
    prompt: asString(source.prompt, fallback.prompt),
    keywords: asString(source.keywords, fallback.keywords),
    sleepTalkMinLength: normalizePetDreamLength(
      asFiniteNumber(source.sleepTalkMinLength, fallback.sleepTalkMinLength),
      fallback.sleepTalkMinLength,
      PET_DREAM_MIN_SLEEP_TALK_LENGTH,
      PET_DREAM_MAX_SLEEP_TALK_LENGTH
    ),
    bubbleMinDurationSeconds: normalizePetDreamLength(
      asFiniteNumber(source.bubbleMinDurationSeconds, fallback.bubbleMinDurationSeconds),
      fallback.bubbleMinDurationSeconds,
      PET_DREAM_MIN_BUBBLE_DURATION_SECONDS,
      PET_DREAM_MAX_BUBBLE_DURATION_SECONDS
    ),
    imageProviderPlatform: asNullableString(source.imageProviderPlatform, fallback.imageProviderPlatform),
    imageProviderId: asNullableString(source.imageProviderId, fallback.imageProviderId),
    imageModelId: asNullableString(source.imageModelId, fallback.imageModelId)
  }
}

function normalizeSkinSelection(value: unknown, petId: string, fallback = defaultSkinSelection(petId)): PetSkinSelection {
  const source = asRecord(value)
  const activeSkinId = source.activeSkinId
  return {
    petId: asString(source.petId, fallback.petId || petId),
    activeSkinId: activeSkinId === null ? null : typeof activeSkinId === 'string' ? activeSkinId : fallback.activeSkinId
  }
}

function normalizeSkin(value: unknown, petId: string): PetSkinRecord | null {
  const source = asRecord(value)
  if (typeof source.skinId !== 'string' || typeof source.name !== 'string') return null
  const atlas = asRecord(source.atlas)
  return {
    petId: asString(source.petId, petId),
    skinId: source.skinId,
    name: source.name,
    // 皮肤路径只用于后端读取资源，前端使用 atlas data URL，旧响应里的路径也不能回流。
    path: '',
    atlasPath: '',
    ...(typeof source.subject === 'string' ? { subject: source.subject } : {}),
    ...(typeof source.modelId === 'string' ? { modelId: source.modelId } : {}),
    ...(typeof source.createdAt === 'number' ? { createdAt: source.createdAt } : {}),
    ...(typeof source.updatedAt === 'number' ? { updatedAt: source.updatedAt } : {}),
    builtin: source.builtin === true,
    ...(typeof source.assetVersion === 'number' ? { assetVersion: source.assetVersion } : {}),
    ...(typeof source.spriteNormalizationVersion === 'number'
      ? { spriteNormalizationVersion: source.spriteNormalizationVersion }
      : {}),
    atlas: {
      atlasVersion: Math.floor(asFiniteNumber(atlas.atlasVersion, 1)),
      image: asString(atlas.image, 'atlas.png'),
      width: Math.floor(asNonNegativeNumber(atlas.width, 1)),
      height: Math.floor(asNonNegativeNumber(atlas.height, 1)),
      anchor: asString(atlas.anchor, 'bottom-center'),
      layout: asString(atlas.layout, 'action-rows')
    },
    manifestJson: source.manifestJson ?? null
  }
}

function normalizePlanAction(value: unknown): PetAction | undefined {
  const actions: PetAction[] = ['feed', 'bathe', 'soak', 'play', 'sleep', 'work', 'study']
  return actions.includes(value as PetAction) ? (value as PetAction) : undefined
}

function normalizePlanSchedule(value: unknown): PetPlanSchedule | undefined {
  const source = asRecord(value)
  const kind = source.kind
  if (kind !== 'now' && kind !== 'delay' && kind !== 'at' && kind !== 'every' && kind !== 'cron') {
    return undefined
  }
  return {
    kind,
    ...(typeof source.delaySeconds === 'number' && Number.isFinite(source.delaySeconds)
      ? { delaySeconds: source.delaySeconds }
      : {}),
    ...('at' in source ? { at: source.at } : {}),
    ...(typeof source.everyMs === 'number' && Number.isFinite(source.everyMs)
      ? { everyMs: Math.max(0, Math.floor(source.everyMs)) }
      : {}),
    ...(typeof source.expr === 'string' ? { expr: source.expr } : {}),
    ...(typeof source.tz === 'string' ? { tz: source.tz } : {})
  }
}

function normalizePlanStep(value: unknown): PetPlanStep | null {
  const source = asRecord(value)
  const kind = source.kind === 'action' || source.kind === 'reminder' ? source.kind : null
  if (!kind) return null
  const action = normalizePlanAction(source.action)
  const schedule = normalizePlanSchedule(source.schedule)
  return {
    kind,
    ...(action ? { action } : {}),
    ...(schedule ? { schedule } : {}),
    ...(typeof source.label === 'string' ? { label: source.label } : {}),
    ...(typeof source.text === 'string' ? { text: source.text } : {})
  }
}

function normalizePlanScript(value: unknown): PetPlanScript {
  const source = asRecord(value)
  const steps = Array.isArray(source.steps)
    ? source.steps.map(normalizePlanStep).filter((step): step is PetPlanStep => Boolean(step))
    : []
  return {
    version: Math.floor(asFiniteNumber(source.version, 1)),
    ...(typeof source.title === 'string' ? { title: source.title } : {}),
    steps
  }
}

function normalizePlanRecords(value: unknown, petId: string): PetPlanRecord[] {
  if (!Array.isArray(value)) return []
  return value
    .filter(isRecord)
    .map((record) => ({
      petId: asString(record.petId, petId),
      planId: asString(record.planId, ''),
      version: Math.floor(asFiniteNumber(record.version, 1)),
      title: asString(record.title, ''),
      script: normalizePlanScript(record.script),
      createdAt: asNonNegativeNumber(record.createdAt, 0),
      updatedAt: asNonNegativeNumber(record.updatedAt, 0)
    }))
}

function normalizeDreamEmotion(value: unknown): PetDreamEmotion | null {
  return value === 'pleasant' || value === 'calm' || value === 'tense' || value === 'afraid'
    ? value
    : null
}

function normalizeDreamHistoryRecords(value: unknown, petId: string): PetDreamHistoryRecord[] {
  if (!Array.isArray(value)) return []
  return value
    .filter(isRecord)
    .map((record) => ({
      petId: asString(record.petId, petId),
      id: asString(record.id, ''),
      createdAt: asNonNegativeNumber(record.createdAt, 0),
      title: asString(record.title, ''),
      creativePrompt: asString(record.creativePrompt, ''),
      effectivePrompt: asString(record.effectivePrompt, ''),
      keywords: Array.isArray(record.keywords)
        ? record.keywords.filter((keyword): keyword is string => typeof keyword === 'string')
        : [],
      themeId: record.themeId === null || typeof record.themeId === 'string' ? record.themeId : null,
      themeLabel: record.themeLabel === null || typeof record.themeLabel === 'string' ? record.themeLabel : null,
      dream: asString(record.dream, ''),
      sleepTalk: asString(record.sleepTalk, ''),
      emotion: normalizeDreamEmotion(record.emotion),
      selfAppears: typeof record.selfAppears === 'boolean' ? record.selfAppears : null,
      // 后端会清洗本地归档路径；前端兼容旧响应时也必须丢弃旧路径，不能把它重新暴露出去。
      imagePath: null
    }))
}

function normalizeMemoryRecords(value: unknown, petId: string): PetMemoryRecord[] {
  if (!Array.isArray(value)) return []
  return value
    .filter(isRecord)
    .map((record) => ({
      petId: asString(record.petId, petId),
      id: asString(record.id, ''),
      date: asString(record.date, ''),
      text: asString(record.text, ''),
      createdAt: asNonNegativeNumber(record.createdAt, 0),
      updatedAt: asNonNegativeNumber(record.updatedAt, 0)
    }))
}

function normalizeAtlas(value: unknown): PetAtlasAsset | null {
  const source = asRecord(value)
  const src = asString(source.src ?? source.imageUrl ?? source.url, '')
  let manifest: unknown = source.manifest ?? source.manifestJson
  if (typeof manifest === 'string') {
    try {
      manifest = JSON.parse(manifest)
    } catch {
      return null
    }
  }
  if (!src || !manifest) return null
  try {
    return { src, manifest: parsePetAtlasDocument(manifest) }
  } catch {
    // 损坏的自定义 atlas 不应阻断状态窗口；调用方会回到默认资源或占位态。
    return null
  }
}

function extractSettingsRoot(root: Record<string, unknown>): Record<string, unknown> {
  return isRecord(root.settings) ? root.settings : root
}

function hasStatePayload(value: unknown): boolean {
  const root = asRecord(value)
  return isRecord(root.state) || 'hunger' in root || 'cleanliness' in root || 'awayTask' in root
}

function hasFullSnapshotPayload(value: unknown): boolean {
  const root = asRecord(value)
  return hasStatePayload(root) && isRecord(root.experience) && isRecord(root.agent)
}

function normalizeSnapshot(value: unknown, petId: string): PetSnapshot {
  const root = asRecord(value)
  if (!hasStatePayload(root)) throw new Error('Pet snapshot did not contain a PetState payload.')
  const settings = extractSettingsRoot(root)
  const skinsValue = root.skins ?? settings.skins
  const plansValue = root.plans ?? settings.plans
  const dreamsValue = root.dreams ?? settings.dreams
  const memoriesValue = root.memories ?? settings.memories
  const skins = Array.isArray(skinsValue)
    ? skinsValue.map((skin) => normalizeSkin(skin, petId)).filter((skin): skin is PetSkinRecord => Boolean(skin))
    : []
  return {
    state: normalizeState(root.state ?? root.petState ?? root, petId),
    experience: normalizeExperience(root.experience ?? root.exp, petId),
    window: normalizeWindowConfig(root.window ?? settings.window ?? root.windowConfig, petId),
    care: normalizeCareConfig(root.care ?? settings.care ?? root.careConfig, petId),
    agent: normalizeAgentConfig(root.agent ?? settings.agent ?? root.agentConfig, petId),
    dream: normalizeDreamConfig(root.dream ?? settings.dream ?? root.dreamConfig, petId),
    plans: normalizePlanRecords(plansValue, petId),
    dreams: normalizeDreamHistoryRecords(dreamsValue, petId),
    memories: normalizeMemoryRecords(memoriesValue, petId),
    skinSelection: normalizeSkinSelection(
      root.skinSelection ?? settings.skinSelection ?? root.skin,
      petId
    ),
    skins,
    atlas: normalizeAtlas(root.atlas ?? root.petAtlas)
  }
}

function normalizeRuntimeSnapshot(value: unknown, petId: string): PetRuntimeSnapshot {
  // 复用完整快照的字段归一化逻辑，保证旧宿主返回缺省字段时仍然沿用同一套默认值；
  // 这里只投影运行时所需字段，不把历史和资源重新带回 renderer 状态。
  const full = normalizeSnapshot(value, petId)
  return {
    state: full.state,
    experience: full.experience,
    window: full.window,
    care: full.care,
    agent: full.agent,
    dream: full.dream,
    skinSelection: full.skinSelection
  }
}

function createDefaultSnapshot(petId: string): PetSnapshot {
  const now = Date.now()
  return {
    state: {
      id: petId,
      name: 'Kapi',
      hunger: 80,
      cleanliness: 80,
      mood: 70,
      growth: 0,
      coins: 120,
      sleeping: false,
      sleepEndsAt: 0,
      awayTask: null,
      lastTickAt: now,
      adoptedAt: now,
      lastMilestoneDays: 0,
      proactiveDate: '',
      proactiveCount: 0,
      lastProactiveAt: 0,
      coinCreditedExp: 0,
      lastDailyBonusDate: ''
    },
    experience: { petId, totalExp: 0, totalTokens: 0 },
    window: defaultWindowConfig(petId),
    care: defaultCareConfig(petId),
    agent: defaultAgentConfig(petId),
    dream: defaultDreamConfig(petId),
    plans: [],
    dreams: [],
    memories: [],
    skinSelection: defaultSkinSelection(petId),
    skins: [],
    atlas: null
  }
}

function getFallbackSnapshot(petId: string): PetSnapshot {
  const cached = fallbackSnapshots.get(petId)
  if (cached) return cached
  // fallback 只服务当前 runtime 的预览生命周期，绝不写浏览器持久存储，避免形成第二个 durable owner。
  const snapshot = createDefaultSnapshot(petId)
  fallbackSnapshots.set(petId, snapshot)
  return snapshot
}

function applyDecay(state: PetState, elapsedMs: number): PetState {
  const minutes = Math.min(Math.max(0, elapsedMs), 24 * 60 * 60 * 1000) / 60_000
  if (minutes <= 0) return state
  const restFactor = state.sleeping ? 0.4 : state.awayTask ? 0.5 : 1
  const hunger = clamp(state.hunger - 0.8 * restFactor * minutes)
  const cleanliness = clamp(state.cleanliness - 0.5 * restFactor * minutes)
  const uncomfortable = hunger < 30 || cleanliness < 30
  const moodDelta = state.sleeping
    ? 2 * (uncomfortable ? 0.4 : 1) * minutes
    : uncomfortable
      ? -1.2 * minutes
      : 0.6 * minutes
  return {
    ...state,
    hunger,
    cleanliness,
    mood: clamp(state.mood + moodDelta)
  }
}

function localPetDateKey(timestamp: number): string {
  const date = new Date(timestamp)
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${date.getFullYear()}-${month}-${day}`
}

function settleFallback(snapshot: PetSnapshot, now = Date.now()): PetSnapshot {
  let state = clone(snapshot.state)
  const lastTickAt = state.lastTickAt > 0 ? state.lastTickAt : now
  if (state.sleeping && state.sleepEndsAt > 0 && now >= state.sleepEndsAt) {
    const slept = applyDecay({ ...state, lastTickAt }, state.sleepEndsAt - lastTickAt)
    state = applyDecay(
      { ...slept, sleeping: false, sleepEndsAt: 0, lastTickAt: state.sleepEndsAt },
      now - state.sleepEndsAt
    )
  } else {
    state = applyDecay({ ...state, lastTickAt }, now - lastTickAt)
  }
  state.lastTickAt = now

  // fallback 只模拟已有契约里的时间结算，后端注册后以 Go 的奖励和持久化规则为准。
  if (state.awayTask && now >= state.awayTask.endsAt) {
    const reward: PetAwayReward =
      state.awayTask.kind === 'work'
        ? { kind: 'work', coins: 60, growth: 30 }
        : { kind: 'study', coins: 0, growth: 240 }
    state = {
      ...state,
      awayTask: null,
      coins: state.coins + reward.coins,
      growth: state.growth + reward.growth,
      hunger: clamp(state.hunger - 10),
      cleanliness: clamp(state.cleanliness - 8)
    }
  }

  const next = { ...snapshot, state }
  fallbackSnapshots.set(state.id, next)
  return next
}

function recordFallbackProactive(snapshot: PetSnapshot, now = Date.now()): PetSnapshot {
  const state = clone(snapshot.state)
  const today = localPetDateKey(now)
  const sameDay = state.proactiveDate === today
  state.proactiveDate = today
  state.proactiveCount = sameDay ? state.proactiveCount + 1 : 1
  state.lastProactiveAt = now
  const next = { ...snapshot, state }
  fallbackSnapshots.set(state.id, next)
  return clone(next)
}

function failure(reason: PetActionFailureReason): PetActionResult {
  return { ok: false, reason }
}

function fallbackAction(snapshot: PetSnapshot, action: PetInteractionAction, now = Date.now()): PetActionResult {
  const settled = settleFallback(snapshot, now)
  const state = settled.state
  const totalGrowth = state.growth + settled.experience.totalExp
  let nextState = state

  if (action === 'petted') {
    if (state.sleeping || state.awayTask) return { ...failure(state.awayTask ? 'busy' : 'sleeping'), snapshot: settled }
    nextState = { ...state, mood: clamp(state.mood + 3) }
  } else if (state.awayTask) {
    return { ...failure('busy'), snapshot: settled }
  } else if (action !== 'sleep' && state.sleeping) {
    return { ...failure('sleeping'), snapshot: settled }
  } else if (action === 'feed') {
    if (state.hunger >= 95) return { ...failure('full'), snapshot: settled }
    if (state.coins < 10) return { ...failure('coins'), snapshot: settled }
    nextState = { ...state, coins: state.coins - 10, hunger: clamp(state.hunger + 35), mood: clamp(state.mood + 2) }
  } else if (action === 'bathe') {
    if (state.cleanliness >= 95) return { ...failure('clean'), snapshot: settled }
    if (state.coins < 6) return { ...failure('coins'), snapshot: settled }
    nextState = { ...state, coins: state.coins - 6, cleanliness: clamp(state.cleanliness + 45), mood: clamp(state.mood + 1) }
  } else if (action === 'soak') {
    if (getPetLevel(totalGrowth) < 2) return { ...failure('level'), snapshot: settled }
    if (state.coins < 15) return { ...failure('coins'), snapshot: settled }
    nextState = { ...state, coins: state.coins - 15, cleanliness: clamp(state.cleanliness + 30), mood: clamp(state.mood + 28) }
  } else if (action === 'play') {
    if (state.hunger < 10) return { ...failure('hungry'), snapshot: settled }
    nextState = { ...state, mood: clamp(state.mood + 18), hunger: clamp(state.hunger - 6), cleanliness: clamp(state.cleanliness - 4) }
  } else if (action === 'sleep') {
    nextState = state.sleeping
      ? { ...state, sleeping: false, sleepEndsAt: 0 }
      : { ...state, sleeping: true, sleepEndsAt: now + 20 * 60_000 }
  } else if (action === 'work') {
    if (getPetLevel(totalGrowth) < 4) return { ...failure('level'), snapshot: settled }
    if (state.hunger < 20) return { ...failure('hungry'), snapshot: settled }
    nextState = { ...state, sleeping: false, sleepEndsAt: 0, awayTask: { kind: 'work', startedAt: now, endsAt: now + 30 * 60_000 } }
  } else if (action === 'study') {
    if (getPetLevel(totalGrowth) < 6) return { ...failure('level'), snapshot: settled }
    if (state.coins < 20) return { ...failure('coins'), snapshot: settled }
    if (state.hunger < 20) return { ...failure('hungry'), snapshot: settled }
    nextState = { ...state, sleeping: false, sleepEndsAt: 0, coins: state.coins - 20, awayTask: { kind: 'study', startedAt: now, endsAt: now + 20 * 60_000 } }
  }

  const next = { ...settled, state: nextState }
  fallbackSnapshots.set(nextState.id, next)
  return { ok: true, snapshot: clone(next) }
}

function normalizeActionResult(value: unknown, petId = DEFAULT_PET_ID): PetActionResult {
  if (value === undefined || value === null || value === true) return { ok: true }
  const source = asRecord(value)
  const reason = source.reason
  const validReasons: PetActionFailureReason[] = ['coins', 'full', 'clean', 'hungry', 'level', 'busy', 'sleeping']
  const rewardSource = asRecord(source.reward)
  const reward: PetAwayReward | undefined =
    (rewardSource.kind === 'work' || rewardSource.kind === 'study') &&
    typeof rewardSource.coins === 'number' &&
    typeof rewardSource.growth === 'number'
      ? {
          kind: rewardSource.kind,
          coins: rewardSource.coins,
          growth: rewardSource.growth
        }
      : undefined
  const state = isRecord(source.state) ? normalizeState(source.state, petId) : undefined
  return {
    ok: source.ok !== false,
    ...(validReasons.includes(reason as PetActionFailureReason)
      ? { reason: reason as PetActionFailureReason }
      : {}),
    ...(reward ? { reward } : {}),
    ...(state ? { state } : {})
  }
}

export function settlePetRuntimeState(state: PetState, now = Date.now()): PetState {
  const current = clone(state)
  const lastTickAt = current.lastTickAt > 0 ? current.lastTickAt : now
  if (current.sleeping && current.sleepEndsAt > 0 && now >= current.sleepEndsAt) {
    // 睡眠跨过结束点时分两段结算，保证恢复心情不会延伸到醒来之后；这与
    // OpenCowork pet-store 的 tick 规则一致，renderer 可以在后端事件到达前保持稳定显示。
    const slept = applyDecay({ ...current, lastTickAt }, current.sleepEndsAt - lastTickAt)
    const awake = applyDecay(
      { ...slept, sleeping: false, sleepEndsAt: 0, lastTickAt: current.sleepEndsAt },
      now - current.sleepEndsAt
    )
    return { ...awake, sleeping: false, sleepEndsAt: 0, lastTickAt: now }
  }
  return { ...applyDecay({ ...current, lastTickAt }, now - lastTickAt), lastTickAt: now }
}

function isRuntimeUnavailable(error: unknown): boolean {
  if (error instanceof ReferenceError) return true
  const message = error instanceof Error ? error.message : String(error)
  return /failed to fetch|networkerror|unknown method|method .*not found|binding|wails runtime|404/i.test(message)
}

function fallbackEndWorkEarly(snapshot: PetSnapshot, now = Date.now()): PetActionResult {
  const settled = settleFallback(snapshot, now)
  const state = settled.state
  if (!state.awayTask || state.awayTask.kind !== 'work' || now >= state.awayTask.endsAt) {
    return { ...failure('busy'), snapshot: settled }
  }
  const next = {
    ...settled,
    state: { ...state, awayTask: null }
  }
  fallbackSnapshots.set(state.id, next)
  return { ok: true, snapshot: clone(next) }
}

function shouldUsePreviewFallback(error: unknown): boolean {
  // 浏览器页面现在有明确的 loopback bridge；bridge 不在线时必须把错误交给
  // 页面显示，否则默认宠物会伪装成 SQLite 数据，用户修改后还会在刷新时丢失。
  // Wails 桌面端仍保留旧 binding 缺失时的兼容 fallback。
  return isWailsRuntimeAvailable() && isRuntimeUnavailable(error)
}

function cloneFallbackWindowState(): PetWindowRuntimeState {
  return { ...fallbackWindowState }
}

function formatWindowEffectError(enabled: boolean, error: unknown): Error {
  const action = enabled ? '打开' : '关闭'
  const detail = error instanceof Error ? error.message : String(error)
  return new Error(`宠物设置已保存，但${action}桌宠窗口失败：${detail}`)
}

function mergeSettings(current: PetSnapshot, value: unknown, petId: string): PetSnapshot {
  const root = asRecord(value)
  const settings = extractSettingsRoot(root)
  return {
    ...current,
    window: normalizeWindowConfig(root.window ?? settings.window ?? root, petId, current.window),
    care: normalizeCareConfig(root.care ?? settings.care ?? root, petId, current.care),
    agent: normalizeAgentConfig(root.agent ?? settings.agent ?? root, petId, current.agent),
    dream: normalizeDreamConfig(root.dream ?? settings.dream ?? root, petId, current.dream),
    plans: normalizePlanRecords(root.plans ?? settings.plans ?? current.plans, petId),
    dreams: normalizeDreamHistoryRecords(root.dreams ?? settings.dreams ?? current.dreams, petId),
    memories: normalizeMemoryRecords(root.memories ?? settings.memories ?? current.memories, petId),
    skinSelection: normalizeSkinSelection(
      root.skinSelection ?? settings.skinSelection ?? root,
      petId,
      current.skinSelection
    )
  }
}

export function createPetApi(adapter: PetRuntimeAdapter = wailsRuntimeAdapter): PetApi {
  let mode: PetRuntimeMode = 'unknown'

  async function readRemoteSnapshot(petId: string): Promise<PetSnapshot> {
    const raw = await adapter.call(PET_RUNTIME_METHODS.getSnapshot, [petId])
    mode = 'backend'
    return normalizeSnapshot(raw, petId)
  }

  async function readRemoteRuntimeSnapshot(petId: string): Promise<PetRuntimeSnapshot> {
    try {
      const raw = await adapter.call(PET_RUNTIME_METHODS.getRuntimeSnapshot, [petId])
      mode = 'backend'
      return normalizeRuntimeSnapshot(raw, petId)
    } catch (error) {
      // 旧宿主尚未提供轻量入口时只在兼容路径回读一次完整快照；新宿主不会
      // 进入这里，因此不会把历史/atlas 重新带回 30 秒运行时链路。
      if (!isRuntimeUnavailable(error)) throw error
      const full = await readRemoteSnapshot(petId)
      return {
        state: full.state,
        experience: full.experience,
        window: full.window,
        care: full.care,
        agent: full.agent,
        dream: full.dream,
        skinSelection: full.skinSelection
      }
    }
  }

  async function getSnapshot(petId = DEFAULT_PET_ID): Promise<PetSnapshot> {
    if (mode === 'fallback') return settleFallback(getFallbackSnapshot(petId))
    try {
      return await readRemoteSnapshot(petId)
    } catch (error) {
      if (!shouldUsePreviewFallback(error)) throw error
      mode = 'fallback'
      return settleFallback(getFallbackSnapshot(petId))
    }
  }

  async function getRuntimeSnapshot(petId = DEFAULT_PET_ID): Promise<PetRuntimeSnapshot> {
    if (mode === 'fallback') {
      const full = settleFallback(getFallbackSnapshot(petId))
      return {
        state: full.state,
        experience: full.experience,
        window: full.window,
        care: full.care,
        agent: full.agent,
        dream: full.dream,
        skinSelection: full.skinSelection
      }
    }
    try {
      return await readRemoteRuntimeSnapshot(petId)
    } catch (error) {
      if (!shouldUsePreviewFallback(error)) throw error
      mode = 'fallback'
      return getRuntimeSnapshot(petId)
    }
  }

  async function getAtlas(petId = DEFAULT_PET_ID, cacheKey = 'default'): Promise<PetAtlasAsset | null> {
    const key = `${petId}:${cacheKey.trim() || 'default'}`
    if (atlasCache.has(key)) return atlasCache.get(key) ?? null

    if (mode === 'fallback') {
      const asset = getFallbackSnapshot(petId).atlas
      atlasCache.set(key, asset)
      return asset
    }

    try {
      const raw = await adapter.call(PET_RUNTIME_METHODS.getAtlas, [petId])
      const asset = normalizeAtlas(raw)
      mode = 'backend'
      atlasCache.set(key, asset)
      return asset
    } catch (error) {
      // 旧宿主兼容时只回读一次完整快照拿资源；正式实现的皮肤切换不会
      // 经过这个分支，atlas 仍由独立入口和缓存负责生命周期。
      if (isRuntimeUnavailable(error)) {
        const full = await readRemoteSnapshot(petId)
        atlasCache.set(key, full.atlas)
        return full.atlas
      }
      if (!shouldUsePreviewFallback(error)) throw error
      mode = 'fallback'
      const asset = getFallbackSnapshot(petId).atlas
      atlasCache.set(key, asset)
      return asset
    }
  }

  function invalidateAtlas(petId?: string): void {
    if (!petId) {
      atlasCache.clear()
      return
    }
    const prefix = `${petId}:`
    for (const key of atlasCache.keys()) {
      if (key.startsWith(prefix)) atlasCache.delete(key)
    }
  }

  async function performAction(petId: string, action: PetInteractionAction): Promise<PetActionResult> {
    if (mode === 'fallback') return fallbackAction(getFallbackSnapshot(petId), action)
    try {
      const raw = await adapter.call(
        action === 'petted' ? PET_RUNTIME_METHODS.petted : PET_RUNTIME_METHODS.performAction,
        action === 'petted' ? [petId] : [petId, action]
      )
      const result = normalizeActionResult(raw, petId)
      const snapshot = hasFullSnapshotPayload(raw)
        ? normalizeSnapshot(raw, petId)
        : undefined
      mode = 'backend'
      // 旧宿主的动作结果没有 state，保留一次性完整回读兼容；新宿主只返回
      // 几 KB 的 PetActionResult，renderer 直接用 state 更新本地运行时。
      if (!result.state && !snapshot) {
        return { ...result, snapshot: await readRemoteSnapshot(petId) }
      }
      return { ...result, ...(snapshot ? { snapshot } : {}) }
    } catch (error) {
      if (!shouldUsePreviewFallback(error)) throw error
      mode = 'fallback'
      return fallbackAction(getFallbackSnapshot(petId), action)
    }
  }

  async function recordProactive(petId: string, now = Date.now()): Promise<PetSnapshot> {
    if (mode === 'fallback') return recordFallbackProactive(settleFallback(getFallbackSnapshot(petId)), now)
    try {
      const raw = await adapter.call(PET_RUNTIME_METHODS.recordProactive, [petId, Math.round(now)])
      const snapshot = hasStatePayload(raw)
        ? normalizeSnapshot(raw, petId)
        : await readRemoteSnapshot(petId)
      mode = 'backend'
      return snapshot
    } catch (error) {
      if (!shouldUsePreviewFallback(error)) throw error
      mode = 'fallback'
      return recordFallbackProactive(settleFallback(getFallbackSnapshot(petId)), now)
    }
  }

  async function saveSettings(petId: string, settings: PetSettingsInput): Promise<PetSnapshot> {
    if (mode === 'fallback') {
      const current = settleFallback(getFallbackSnapshot(petId))
      const next = mergeSettings(current, settings, petId)
      fallbackSnapshots.set(petId, next)
      return clone(next)
    }
    try {
      const raw = await adapter.call(PET_RUNTIME_METHODS.saveSettings, [petId, settings])
      if (hasStatePayload(raw)) {
        mode = 'backend'
        return normalizeSnapshot(raw, petId)
      }
      const current = await readRemoteSnapshot(petId)
      mode = 'backend'
      return mergeSettings(current, raw ?? settings, petId)
    } catch (error) {
      if (!shouldUsePreviewFallback(error)) throw error
      mode = 'fallback'
      const current = settleFallback(getFallbackSnapshot(petId))
      const next = mergeSettings(current, settings, petId)
      fallbackSnapshots.set(petId, next)
      return clone(next)
    }
  }

  async function recordProactiveState(petId: string, now = Date.now()): Promise<PetState> {
    if (mode === 'fallback') {
      return recordFallbackProactive(settleFallback(getFallbackSnapshot(petId)), now).state
    }
    try {
      const raw = await adapter.call(PET_RUNTIME_METHODS.recordProactiveState, [petId, Math.round(now)])
      const state = normalizeState(raw, petId)
      mode = 'backend'
      return state
    } catch (error) {
      // 旧宿主没有轻量入口时只走一次兼容完整接口；正式链路不会触发大快照回读。
      if (isRuntimeUnavailable(error)) {
        return (await recordProactive(petId, now)).state
      }
      if (!shouldUsePreviewFallback(error)) throw error
      mode = 'fallback'
      return recordFallbackProactive(settleFallback(getFallbackSnapshot(petId)), now).state
    }
  }

  async function endWorkEarly(petId: string, now = Date.now()): Promise<PetActionResult> {
    if (mode === 'fallback') return fallbackEndWorkEarly(getFallbackSnapshot(petId), now)
    try {
      const raw = await adapter.call(`${PET_SERVICE}.${PET_RUNTIME_METHODS.endWorkEarly}`, [petId, Math.round(now)])
      const result = normalizeActionResult(raw, petId)
      const snapshot = hasFullSnapshotPayload(raw)
        ? normalizeSnapshot(raw, petId)
        : undefined
      mode = 'backend'
      if (!result.state && !snapshot) {
        return { ...result, snapshot: await readRemoteSnapshot(petId) }
      }
      return { ...result, ...(snapshot ? { snapshot } : {}) }
    } catch (error) {
      if (!shouldUsePreviewFallback(error)) throw error
      mode = 'fallback'
      return fallbackEndWorkEarly(getFallbackSnapshot(petId), now)
    }
  }

  async function updateName(petId: string, name: string): Promise<PetSnapshot> {
    const normalized = name.trim()
    if (!normalized) throw new Error('宠物名称不能为空。')
    // fallback 只用于预览设置和动作，不具备持久化能力；名称若在此处静默成功，
    // 用户重启应用后会丢失，反而比明确提示后端不可用更危险。
    if (mode === 'fallback') throw new Error('宠物改名需要已连接的后端。')
    const raw = await adapter.call(PET_RUNTIME_METHODS.updateName, [petId, normalized])
    mode = 'backend'
    return hasStatePayload(raw)
      ? normalizeSnapshot(raw, petId)
      : await readRemoteSnapshot(petId)
  }

  async function readRemoteWindowState(): Promise<PetWindowRuntimeState> {
    const raw = await adapter.call(`${PET_WINDOW_SERVICE}.${PET_WINDOW_RUNTIME_METHODS.state}`, [])
    const state = normalizeWindowState(raw)
    mode = 'backend'
    return state
  }

  async function getWindowState(): Promise<PetWindowRuntimeState> {
    if (mode === 'fallback') return cloneFallbackWindowState()
    try {
      return await readRemoteWindowState()
    } catch (error) {
      // PetService 已经在线时，窗口服务缺失是桥接集成错误，不能把后续保存静默降级到 fallback。
      if (!shouldUsePreviewFallback(error) || mode === 'backend') throw error
      mode = 'fallback'
      return cloneFallbackWindowState()
    }
  }

  async function setWindowEnabled(enabled: boolean): Promise<PetWindowRuntimeState> {
    if (!isWailsRuntimeAvailable()) {
      // 浏览器页面只持久化 enabled 配置，不执行 Open/Close 这类原生窗口副作用；
      // Chrome 没有透明桌面窗口可供控制，强行调用只会让保存结果被错误覆盖。
      const state = await getWindowState()
      return { ...state, open: enabled }
    }
    if (mode === 'fallback') {
      // fallback 只模拟当前预览进程的窗口状态，不引入 localStorage 这个第二持久化 owner。
      fallbackWindowState = { ...fallbackWindowState, open: enabled }
      return cloneFallbackWindowState()
    }

    try {
      const method = enabled ? PET_WINDOW_RUNTIME_METHODS.open : PET_WINDOW_RUNTIME_METHODS.close
      await adapter.call(`${PET_WINDOW_SERVICE}.${method}`, [])
      const state = await readRemoteWindowState()
      if (state.open !== enabled) {
        throw new Error(`窗口状态为 ${state.open ? 'open' : 'closed'}，与已保存的 enabled=${enabled} 不一致。`)
      }
      return state
    } catch (error) {
      // 这里不能像读取快照那样切 fallback：SaveSettings 已经在后端成功，静默降级会掩盖真实窗口未同步。
      throw formatWindowEffectError(enabled, error)
    }
  }

  return {
    getSnapshot,
    getRuntimeSnapshot,
    getAtlas,
    invalidateAtlas,
    performAction,
    endWorkEarly,
    saveSettings,
    updateName,
    recordProactive,
    recordProactiveState,
    setWindowEnabled,
    getWindowState,
    getRuntimeMode: () => mode
  }
}

export const petApi = createPetApi()
