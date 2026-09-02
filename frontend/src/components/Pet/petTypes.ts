import type { PetAtlasDocument } from './petAtlas'

export const DEFAULT_PET_ID = 'default'

export type PetAction = 'feed' | 'bathe' | 'soak' | 'play' | 'sleep' | 'work' | 'study'
export type PetInteractionAction = PetAction | 'petted'
export type PetActionFailureReason =
  | 'coins'
  | 'full'
  | 'clean'
  | 'hungry'
  | 'level'
  | 'busy'
  | 'sleeping'

export type PetAwayKind = 'work' | 'study'
export type PetReasoningEffort = 'none' | 'minimal' | 'low' | 'medium' | 'high'
export type PetProactiveFrequency = 'low' | 'medium' | 'high'
export type PetVoiceMode = 'auto' | 'speech' | 'chat'
export type PetDreamEmotion = 'pleasant' | 'calm' | 'tense' | 'afraid'

export interface PetAwayTask {
  kind: PetAwayKind
  startedAt: number
  endsAt: number
}

export interface PetAwayReward {
  kind: PetAwayKind
  coins: number
  growth: number
}

/** 与 services/pet_contract.go 的 PetState JSON 字段保持一一对应。 */
export interface PetState {
  id: string
  name: string
  hunger: number
  cleanliness: number
  mood: number
  growth: number
  coins: number
  sleeping: boolean
  sleepEndsAt: number
  awayTask: PetAwayTask | null
  lastTickAt: number
  adoptedAt: number
  lastMilestoneDays: number
  proactiveDate: string
  proactiveCount: number
  lastProactiveAt: number
  coinCreditedExp: number
  lastDailyBonusDate: string
}

export interface PetExperience {
  petId: string
  totalExp: number
  totalTokens: number
}

export interface PetCareConfig {
  petId: string
  autoCareEnabled: boolean
  autoCareThreshold: number
}

/** 只保存 provider platform/provider/model 引用，不在前端状态中复制 API Key 或 provider 实体。 */
export interface PetAgentConfig {
  petId: string
  providerPlatform: string | null
  providerId: string | null
  modelId: string | null
  reasoningEffort: PetReasoningEffort | null
  systemPrompt: string
  projectId: string | null
  projectName: string | null
  projectFolder: string | null
  proactive: boolean
  proactiveFreq: PetProactiveFrequency
  quietStart: number
  quietEnd: number
  voiceEnabled: boolean
  voiceProviderId: string | null
  voiceModelId: string | null
  voice: string
  voiceMode: PetVoiceMode
  voiceInstruction: string
  voiceTag: string
}

export interface PetDreamConfig {
  petId: string
  dreamEnabled: boolean
  prompt: string
  keywords: string
  sleepTalkMinLength: number
  bubbleMinDurationSeconds: number
  imageProviderPlatform: string | null
  imageProviderId: string | null
  imageModelId: string | null
}

export type PetPlanScheduleKind = 'now' | 'delay' | 'at' | 'every' | 'cron'
export type PetPlanStepKind = 'action' | 'reminder'

export interface PetPlanSchedule {
  kind: PetPlanScheduleKind
  delaySeconds?: number
  at?: unknown
  everyMs?: number
  expr?: string
  tz?: string
}

export interface PetPlanStep {
  kind: PetPlanStepKind
  action?: PetAction
  schedule?: PetPlanSchedule
  label?: string
  text?: string
}

export interface PetPlanScript {
  version: number
  title?: string
  steps: PetPlanStep[]
}

export interface PetPlanRecord {
  petId: string
  planId: string
  version: number
  title: string
  script: PetPlanScript
  createdAt: number
  updatedAt: number
}

export interface PetDreamHistoryRecord {
  petId: string
  id: string
  createdAt: number
  title: string
  creativePrompt: string
  effectivePrompt: string
  keywords: string[]
  themeId: string | null
  themeLabel: string | null
  dream: string
  sleepTalk: string
  emotion: PetDreamEmotion | null
  selfAppears: boolean | null
  imagePath: string | null
}

export interface PetMemoryRecord {
  petId: string
  id: string
  date: string
  text: string
  createdAt: number
  updatedAt: number
}

export interface PetWindowConfig {
  petId: string
  enabled: boolean
}

/** 与 services/pet_heartbeat.go 的调度状态保持同一组稳定 wire value。 */
export type PetHeartbeatPhase = 'disabled' | 'waiting' | 'waiting_for_idle' | 'running'
export type PetHeartbeatRunStatus = 'none' | 'completed' | 'failed' | 'cancelled' | 'interrupted'

export interface PetHeartbeatConfig {
  petId: string
  enabled: boolean
  intervalMinutes: number
  prompt: string
}

export interface PetHeartbeatRuntime {
  phase: PetHeartbeatPhase
  nextRunAt: number
  currentRequestId: string
  lastStartedAt: number
  lastFinishedAt: number
  lastStatus: PetHeartbeatRunStatus
  lastErrorCode: string
}

export interface PetHeartbeatSnapshot {
  config: PetHeartbeatConfig
  runtime: PetHeartbeatRuntime
}

export interface PetHeartbeatEvent {
  type: string
  snapshot: PetHeartbeatSnapshot
}

export interface PetAtlasMetadata {
  atlasVersion: number
  image: string
  width: number
  height: number
  anchor: string
  layout: string
}

export interface PetSkinRecord {
  petId: string
  skinId: string
  name: string
  path: string
  atlasPath: string
  subject?: string
  modelId?: string
  createdAt?: number
  updatedAt?: number
  builtin: boolean
  assetVersion?: number
  spriteNormalizationVersion?: number
  atlas: PetAtlasMetadata
  manifestJson: unknown
}

export interface PetSkinSelection {
  petId: string
  activeSkinId: string | null
}

/** Wails 后端可以附带已经可读的 data URL，避免 Vue 直接碰文件系统路径。 */
export interface PetAtlasAsset {
  src: string
  manifest: PetAtlasDocument
}

export interface PetSettingsInput {
  window: PetWindowConfig
  care: PetCareConfig
  agent: PetAgentConfig
  dream: PetDreamConfig
  skinSelection: PetSkinSelection
}

export type PetSettingsForm = PetSettingsInput

export interface PetSnapshot extends PetSettingsInput {
  state: PetState
  experience: PetExperience
  plans: PetPlanRecord[]
  dreams: PetDreamHistoryRecord[]
  memories: PetMemoryRecord[]
  skins: PetSkinRecord[]
  atlas: PetAtlasAsset | null
}

/** 桌宠运行时 hydration 的轻量契约，不包含历史记录、皮肤列表或 atlas 二进制。 */
export interface PetRuntimeSnapshot {
  state: PetState
  experience: PetExperience
  window: PetWindowConfig
  care: PetCareConfig
  agent: PetAgentConfig
  dream: PetDreamConfig
  skinSelection: PetSkinSelection
}

/** 设置页首屏只需要配置和皮肤元数据，不应携带历史记录或 atlas 二进制。 */
export interface PetSettingsSnapshot extends PetRuntimeSnapshot {
  skins: PetSkinRecord[]
}

export interface PetActionResult {
  ok: boolean
  reason?: PetActionFailureReason
  reward?: PetAwayReward
  state?: PetState
  snapshot?: PetSnapshot
}

/** 宠物经验等级曲线与 OpenCowork pet-store 保持一致。 */
export function getGrowthForLevel(level: number): number {
  return 5000 * Math.max(0, level - 1) ** 2
}

export function getPetLevel(growth: number): number {
  return Math.floor(Math.sqrt(Math.max(0, growth) / 5000)) + 1
}

export function getLevelProgress(growth: number): number {
  const level = getPetLevel(growth)
  const current = getGrowthForLevel(level)
  const next = getGrowthForLevel(level + 1)
  return Math.min(1, Math.max(0, (growth - current) / (next - current)))
}

export const PET_AUTO_CARE_MIN_THRESHOLD = 5
export const PET_AUTO_CARE_MAX_THRESHOLD = 50
export const PET_AUTO_CARE_THRESHOLD_STEP = 5
export const PET_AUTO_CARE_DEFAULT_THRESHOLD = 20

export const PET_DREAM_MIN_SLEEP_TALK_LENGTH = 5
export const PET_DREAM_MAX_SLEEP_TALK_LENGTH = 100
export const PET_DREAM_DEFAULT_SLEEP_TALK_LENGTH = 12
export const PET_DREAM_MIN_BUBBLE_DURATION_SECONDS = 5
export const PET_DREAM_MAX_BUBBLE_DURATION_SECONDS = 60
export const PET_DREAM_DEFAULT_BUBBLE_DURATION_SECONDS = 12

export function normalizePetAutoCareThreshold(value: number): number {
  if (!Number.isFinite(value)) return PET_AUTO_CARE_DEFAULT_THRESHOLD
  const clamped = Math.min(
    PET_AUTO_CARE_MAX_THRESHOLD,
    Math.max(PET_AUTO_CARE_MIN_THRESHOLD, value)
  )
  return Math.round(clamped / PET_AUTO_CARE_THRESHOLD_STEP) * PET_AUTO_CARE_THRESHOLD_STEP
}

export function normalizePetDreamLength(value: number, fallback: number, min: number, max: number): number {
  if (!Number.isFinite(value)) return fallback
  return Math.min(max, Math.max(min, Math.round(value)))
}
