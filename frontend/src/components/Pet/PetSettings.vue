<script setup lang="ts">
import { Call, Events } from '../../wails-runtime-compat'
import { computed, onMounted, onUnmounted, reactive, ref, watch, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import {
  BarChart3,
  Bot,
  Brain,
  FolderOpen,
  HeartPulse,
  Images,
  LayoutDashboard,
  Monitor,
  Moon,
  PawPrint,
  RefreshCw,
  Trash2,
  Wand2
} from '@lucide/vue'
import type { GeminiProvider, Provider } from '../../../bindings/codeswitch/services/models'
import { fetchProjectManagerSnapshot, refreshProjectManagerSnapshot, type ProjectSummary } from '../../services/projectManager'
import { petApi } from './petApi'
import PetAtlasFrame from './PetAtlasFrame.vue'
import PetDreamHistoryPanel from './PetDreamHistoryPanel.vue'
import PetHeartbeat from './PetHeartbeat.vue'
import PetMemoryPanel from './PetMemoryPanel.vue'
import PetStudio from './PetStudio.vue'
import { PetAudioPlayer } from './petAudio'
import {
  deletePetStudioSkin,
  getPetStudioRoot,
  openPetStudioRoot,
  readPetStudioAtlas
} from './petStudioApi'
import {
  DEFAULT_PET_ID,
  PET_AUTO_CARE_MAX_THRESHOLD,
  PET_AUTO_CARE_MIN_THRESHOLD,
  PET_AUTO_CARE_THRESHOLD_STEP,
  PET_DREAM_MAX_BUBBLE_DURATION_SECONDS,
  PET_DREAM_MAX_SLEEP_TALK_LENGTH,
  PET_DREAM_MIN_BUBBLE_DURATION_SECONDS,
  PET_DREAM_MIN_SLEEP_TALK_LENGTH,
  getGrowthForLevel,
  getLevelProgress,
  getPetLevel,
  normalizePetAutoCareThreshold,
  normalizePetDreamLength,
  type PetAgentConfig,
  type PetAtlasAsset,
  type PetReasoningEffort,
  type PetSettingsSnapshot,
  type PetSettingsForm,
  type PetSkinRecord,
  type PetSnapshot
} from './petTypes'

const PET_STATS_SERVICE = 'codeswitch/services.PetService'
const PET_STATS_METHODS = {
  listExperienceLog: `${PET_STATS_SERVICE}.ListExperienceLog`
} as const
const PET_EXPERIENCE_LOG_PAGE_SIZE = 50

// 后端规则层当前的解锁门槛没有进入共享前端类型；这里仅复制只读展示所需的稳定门槛，
// 不参与动作判定，真正的权限仍由 services/pet_rules.go 负责。
const PET_UNLOCKS = [
  { id: 'soak', level: 2 },
  { id: 'work', level: 4 },
  { id: 'study', level: 6 }
] as const

// 语音预设只负责填入 provider 约定的 voice 标识；真正的 provider、模型和密钥
// 仍由主配置页维护，设置页不能复制第二份凭据或自行推断 provider 配置。
const PET_VOICE_PRESETS = {
  openai: ['alloy', 'ash', 'ballad', 'coral', 'echo', 'fable', 'onyx', 'nova', 'sage', 'shimmer', 'verse'],
  mimo: ['mimo_default', '冰糖', '茉莉', '苏打', '白桦', 'Mia', 'Chloe', 'Milo', 'Dean']
} as const
const PET_VOICE_DEFAULT = '__default__'
const PET_VOICE_CUSTOM = '__custom__'
const PET_PROACTIVE_DAILY_CAP = { low: 1, medium: 2, high: 4 } as const
const PET_PROVIDER_PLATFORMS = ['claude', 'codex', 'gemini'] as const
const PET_HOURS = Array.from({ length: 24 }, (_, hour) => hour)

// 空字符串是后端默认值的持久化协议；设置页只把内置文本展示出来，保存时仍还原为空字符串，
// 这样用户能看到实际生效的 prompt，又不会把一份容易漂移的默认文本写进用户配置。
const PET_BUILTIN_AGENT_PROMPT = `你是 {{name}}，一只住在用户电脑桌面上的卡皮巴拉桌宠。你的性格：淡定、温暖、有点贪吃，喜欢泡温泉和发呆。

规则：
- 用用户的语言回复（用户说中文就用中文，说英文就用英文）。
- 回复要非常简短（一两句话，不超过 60 字），像宠物气泡对话，可以用一点可爱的语气词，但不要过度卖萌。
- 你不是全能助手：可以陪聊、安慰、提醒休息、聊聊今天的状态；专业问题可以简单回答，太复杂的就建议主人去主界面找 AI 同事。
- 永远不要输出 Markdown、代码块或列表，只输出纯文本。

你的当前状态：{{status}}
{{project}}`

// 与 PetWindow 的默认梦境入口保持同一文案，用户编辑页展示的是空配置实际会使用的内容。
const PET_BUILTIN_DREAM_PROMPT = '你正在睡觉并处于梦境中，这不是主人发来的消息。请以宠物的第一人称做一个具体、完整的随机短梦。梦境可以温暖、有趣、荒诞、紧张或偶尔令人害怕，但不要每次都做噩梦。'

type PetTab = 'overview' | 'stats' | 'agent' | 'heartbeat' | 'sleep' | 'skins' | 'memory' | 'dream-history' | 'studio'

const PET_TABS: ReadonlyArray<{ id: PetTab; icon: Component }> = [
  { id: 'overview', icon: LayoutDashboard },
  { id: 'stats', icon: BarChart3 },
  { id: 'studio', icon: Wand2 },
  { id: 'skins', icon: PawPrint },
  { id: 'memory', icon: Brain },
  { id: 'agent', icon: Bot },
  { id: 'heartbeat', icon: HeartPulse },
  { id: 'sleep', icon: Moon },
  { id: 'dream-history', icon: Images },
]

interface PetExperienceLogEntry {
  id: string
  at: number
  model: string
  tokens: number
  premium: boolean
  exp: number
}

interface PetProviderModelOption {
  platform: string
  providerId: string
  providerName: string
  modelId: string
  modelCategory: string
  reasoningEffortLevels: PetReasoningEffort[]
}

interface PetProviderModelGroup {
  key: string
  label: string
  options: PetProviderModelOption[]
}

interface PetProviderModelCatalogEntry {
  id?: unknown
  name?: unknown
  modelCategory?: unknown
  category?: unknown
  type?: unknown
  supportsImageGeneration?: unknown
  supports_image_generation?: unknown
}

interface PetProviderModelLoadResult {
  options: PetProviderModelOption[]
  errors: string[]
}

const PET_PROVIDER_OPTIONS_CACHE_TTL_MS = 2 * 60_000
const petProviderOptionsCache = new Map<string, { result: PetProviderModelLoadResult; expiresAt: number }>()
const petProviderOptionsRequests = new Map<string, Promise<PetProviderModelLoadResult>>()
const petSkinPreviewCache = new Map<string, PetAtlasAsset>()
const petSkinPreviewRequests = new Map<string, Promise<PetAtlasAsset>>()
let petStudioRootCache: string | null = null

type PetProjectOption = ProjectSummary

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function finiteNumber(value: unknown, fallback = 0): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

function normalizeExperienceLog(value: unknown, unknownModel: string): PetExperienceLogEntry[] {
  const root = isRecord(value) ? value : {}
  const entries = Array.isArray(root.entries) ? root.entries : []
  return entries.map((entry, index) => {
    const item = isRecord(entry) ? entry : {}
    return {
      id: typeof item.id === 'string' ? item.id : `experience-${index}`,
      at: finiteNumber(item.at),
      model: typeof item.model === 'string' && item.model.trim() ? item.model : unknownModel,
      tokens: Math.max(0, Math.floor(finiteNumber(item.tokens))),
      premium: item.premium === true,
      exp: finiteNumber(item.exp)
    }
  })
}

interface PetSettingsProps {
  petId?: string
  modelValue?: PetSettingsForm | null
}

const props = withDefaults(defineProps<PetSettingsProps>(), {
  petId: DEFAULT_PET_ID,
  modelValue: null
})

const emit = defineEmits<{
  (event: 'update:modelValue', value: PetSettingsForm): void
  (event: 'saved', value: PetSnapshot): void
  (event: 'error', value: unknown): void
}>()
const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()

function createDefaultForm(petId: string): PetSettingsForm {
  return {
    window: { petId, enabled: true },
    care: { petId, autoCareEnabled: false, autoCareThreshold: 20 },
    agent: {
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
    },
    dream: {
      petId,
      dreamEnabled: true,
      prompt: '',
      keywords: '',
      sleepTalkMinLength: 12,
      bubbleMinDurationSeconds: 12,
      imageProviderPlatform: null,
      imageProviderId: null,
      imageModelId: null
    },
    skinSelection: { petId, activeSkinId: null }
  }
}

function cloneForm(value: PetSettingsForm): PetSettingsForm {
  return {
    window: { ...value.window },
    care: { ...value.care },
    agent: { ...value.agent },
    dream: { ...value.dream },
    skinSelection: { ...value.skinSelection }
  }
}

const form = reactive<PetSettingsForm>(createDefaultForm(props.petId))
const skins = ref<PetSkinRecord[]>([])
const defaultAtlas = ref<PetAtlasAsset | null>(null)
const skinPreviews = ref<Record<string, PetAtlasAsset>>({})
const skinPreviewLoading = ref<Record<string, boolean>>({})
const skinRefreshing = ref(false)
const deletingSkinId = ref<string | null>(null)
const skinRoot = ref('')
const skinRootLoading = ref(false)
const loading = ref(true)
const saving = ref(false)
const nameDraft = ref('')
const renaming = ref(false)
const errorMessage = ref('')
const activeTab = ref<PetTab>('overview')
const snapshot = ref<PetSettingsSnapshot | null>(null)
const providerOptions = ref<PetProviderModelOption[]>([])
const providerLoading = ref(false)
const providerError = ref('')
const projectOptions = ref<PetProjectOption[]>([])
const projectLoading = ref(false)
const projectError = ref('')
const statsNow = ref(Date.now())
const statsRefreshing = ref(false)
const statsErrorMessage = ref('')
const experienceLog = ref<PetExperienceLogEntry[]>([])
const experienceLogLoading = ref(false)
const experienceLogError = ref('')
const voiceTesting = ref(false)
const voiceErrorMessage = ref('')
let statsRefreshTimer: number | undefined
let statsRequestGeneration = 0
let providerRequestGeneration = 0
let skinPreviewGeneration = 0

const voicePreviewPlayer = new PetAudioPlayer({
  startSpeechStream: (request) => Call.ByName('codeswitch/services.PetAIAPIService.StartSpeechStream', request),
  synthesizeSpeech: (request) => Call.ByName('codeswitch/services.PetAIAPIService.SynthesizeSpeech', request),
  cancelSpeech: (requestId) => Call.ByName('codeswitch/services.PetAIAPIService.CancelSpeech', requestId)
})

const selectableSkinRecords = computed(() => skins.value.filter((skin) => skin.skinId !== 'capybara'))
const defaultSkinActive = computed(() => {
  const activeSkinId = form.skinSelection.activeSkinId
  return !activeSkinId || activeSkinId === 'capybara'
})

const reasoningEffortModel = computed({
  get: () => form.agent.reasoningEffort ?? '',
  set: (value: string) => {
    form.agent.reasoningEffort = (value || null) as PetReasoningEffort | null
  }
})

const combinedGrowth = computed(() => {
  const current = snapshot.value
  return (current?.state.growth ?? 0) + (current?.experience.totalExp ?? 0)
})

const petLevel = computed(() => getPetLevel(combinedGrowth.value))
const nextLevelGrowth = computed(() => getGrowthForLevel(petLevel.value + 1))
const levelProgressPercent = computed(() => Math.round(getLevelProgress(combinedGrowth.value) * 100))

const companionDays = computed(() => {
  const adoptedAt = snapshot.value?.state.adoptedAt ?? 0
  if (!Number.isFinite(adoptedAt) || adoptedAt <= 0) return 0
  return Math.max(1, Math.ceil((statsNow.value - adoptedAt) / 86_400_000))
})

const awayRemainingMinutes = computed(() => {
  const endsAt = snapshot.value?.state.awayTask?.endsAt ?? 0
  return Math.max(0, Math.ceil((endsAt - statsNow.value) / 60_000))
})

const sleepRemainingMinutes = computed(() => {
  const endsAt = snapshot.value?.state.sleepEndsAt ?? 0
  return Math.max(0, Math.ceil((endsAt - statsNow.value) / 60_000))
})

const petStatusLabel = computed(() => {
  const current = snapshot.value
  if (!current) return t('pet.settings.status.none')
  if (current.state.awayTask) {
    const label = current.state.awayTask.kind === 'work'
      ? t('pet.settings.status.working')
      : t('pet.settings.status.studying')
    return awayRemainingMinutes.value > 0
      ? t('pet.settings.status.awayRemaining', { label, minutes: awayRemainingMinutes.value })
      : t('pet.settings.status.awayEnding', { label })
  }
  if (current.state.sleeping) {
    return sleepRemainingMinutes.value > 0
      ? t('pet.settings.status.sleepingRemaining', { minutes: sleepRemainingMinutes.value })
      : t('pet.settings.status.sleeping')
  }
  return t('pet.settings.status.awake')
})

const agentPromptModel = computed({
  get: () => form.agent.systemPrompt.trim() || PET_BUILTIN_AGENT_PROMPT,
  set: (value: string) => {
    form.agent.systemPrompt = value.trim() === PET_BUILTIN_AGENT_PROMPT.trim() ? '' : value
  }
})

const dreamPromptModel = computed({
  get: () => form.dream.prompt.trim() || PET_BUILTIN_DREAM_PROMPT,
  set: (value: string) => {
    form.dream.prompt = value.trim() === PET_BUILTIN_DREAM_PROMPT.trim() ? '' : value
  }
})

function modelRuleMatches(rule: string, modelId: string): boolean {
  const normalizedRule = rule.trim()
  const normalizedModelId = modelId.trim()
  if (!normalizedRule || !normalizedModelId) return false
  if (!normalizedRule.includes('*')) return normalizedRule === normalizedModelId
  // 后端 provider 规则当前只承诺一个“前缀*后缀”通配符；前端不扩展语义，
  // 避免设置页显示了能力，但请求校验和实际路由却无法命中同一条规则。
  if (normalizedRule.indexOf('*') !== normalizedRule.lastIndexOf('*')) return false

  // 配置里的星号只代表任意字符；其余字符按字面量匹配，避免模型名中的句点、加号等被当成正则语法。
  const pattern = normalizedRule
    .split('*')
    .map((part) => part.replace(/[\\^$.*+?()[\]{}|]/g, '\\$&'))
    .join('.*')
  return new RegExp(`^${pattern}$`).test(normalizedModelId)
}

function modelRuleSpecificity(rule: string): [number, number, number, number] {
  const normalizedRule = rule.trim()
  const wildcardCount = (normalizedRule.match(/\*/g) ?? []).length
  const literalLength = normalizedRule.replace(/\*/g, '').length
  // 精确规则优先；通配符规则按字面量长度、通配符数量和总长度稳定排序。
  return [normalizedRule.includes('*') ? 0 : 1, literalLength, -wildcardCount, normalizedRule.length]
}

function compareModelRuleSpecificity(left: string, right: string): number {
  const leftScore = modelRuleSpecificity(left)
  const rightScore = modelRuleSpecificity(right)
  for (let index = 0; index < leftScore.length; index += 1) {
    if (leftScore[index] === rightScore[index]) continue
    return leftScore[index] > rightScore[index] ? 1 : -1
  }
  // Go map 遍历顺序不稳定；相同具体度时用规则文本打破平局，确保能力展示稳定。
  return left === right ? 0 : (left < right ? 1 : -1)
}

function resolveModelRule<T>(rules: Record<string, T> | undefined, modelId: string): T | undefined {
  let matchedKey: string | undefined
  for (const key of Object.keys(rules ?? {})) {
    if (!modelRuleMatches(key, modelId)) continue
    if (!matchedKey || compareModelRuleSpecificity(key, matchedKey) > 0) matchedKey = key
  }
  return matchedKey === undefined ? undefined : rules?.[matchedKey]
}

function providerRuleAllowsModel(provider: Provider, modelId: string): boolean {
  const rules = Object.entries(provider.supportedModels ?? {}).filter(([rule]) => rule.trim())
  if (rules.length === 0) return true
  // false 只表示该条规则未启用；与 Go 端 IsModelSupported 保持一致，命中的任一 true 规则即可通过。
  return rules.some(([rule, enabled]) => enabled !== false && modelRuleMatches(rule, modelId))
}

function providerConfiguredModelIds(provider: Provider): string[] {
  const ids = new Set<string>()
  for (const [rawModelId, enabled] of Object.entries(provider.supportedModels ?? {})) {
    const modelId = rawModelId.trim()
    if (enabled !== false && modelId && !modelId.includes('*')) ids.add(modelId)
  }
  // 仅配置 modelMapping 的旧 provider 也要保留其外部模型名作为可选项。
  for (const rawModelId of Object.keys(provider.modelMapping ?? {})) {
    const modelId = rawModelId.trim()
    if (modelId && !modelId.includes('*')) ids.add(modelId)
  }
  return [...ids]
}

function providerModelOption(
  platform: string,
  provider: Provider,
  modelId: string,
  remoteModel?: PetProviderModelCatalogEntry
): PetProviderModelOption {
  const providerId = String(provider.id)
  const capabilities = provider as Provider & {
    modelReasoningEffortLevels?: Record<string, unknown>
  }
  const configuredCategory = normalizePetModelCategory(resolveModelRule(provider.modelCategories, modelId))
  return {
    platform,
    providerId,
    providerName: provider.name.trim() || providerId,
    modelId,
    // provider 配置是本地显式事实源；远端元数据和名称识别只负责补足没有配置的模型。
    modelCategory: configuredCategory || remoteModelCategory(remoteModel, modelId),
    reasoningEffortLevels: normalizeReasoningEffortLevels(
      resolveModelRule(capabilities.modelReasoningEffortLevels, modelId)
    )
  }
}

async function providerModelOptions(platform: string, providers: Provider[]): Promise<PetProviderModelLoadResult> {
  const results = await Promise.all(providers.map(async (provider): Promise<PetProviderModelLoadResult> => {
    const providerId = String(provider.id)
    if (!provider.enabled || !providerId.trim()) return { options: [], errors: [] }

    const configuredModelIds = providerConfiguredModelIds(provider)
    const rules = Object.keys(provider.supportedModels ?? {}).filter((rule) => rule.trim())
    const shouldFetchRemoteModels = rules.length === 0 || rules.some((rule) => rule.includes('*'))
    const modelIds = new Set(configuredModelIds)
    const remoteModels = new Map<string, PetProviderModelCatalogEntry>()
    const errors: string[] = []

    if (shouldFetchRemoteModels) {
      try {
        const discovered = await Call.ByName(
          'codeswitch/services.ProviderService.FetchModels',
          platform,
          provider.id
        ) as PetProviderModelCatalogEntry[]
        for (const model of discovered ?? []) {
          const modelId = typeof model?.id === 'string' ? model.id.trim() : ''
          if (modelId && providerRuleAllowsModel(provider, modelId)) {
            modelIds.add(modelId)
            remoteModels.set(modelId, model)
          }
        }
      } catch (error) {
        // 远端目录失败时保留已配置的精确模型；这样网络抖动不会清空当前宠物引用。
        errors.push(`${platform}/${provider.name.trim() || providerId}: ${error instanceof Error ? error.message : String(error)}`)
      }
    }

    return {
      options: [...modelIds]
        .filter((modelId) => providerRuleAllowsModel(provider, modelId))
        .sort((left, right) => left.localeCompare(right))
        .map((modelId) => providerModelOption(platform, provider, modelId, remoteModels.get(modelId))),
      errors
    }
  }))

  return {
    options: results.flatMap((result) => result.options),
    errors: results.flatMap((result) => result.errors)
  }
}

function normalizeReasoningEffortLevels(value: unknown): PetReasoningEffort[] {
  if (!Array.isArray(value)) return []
  return value.filter((item): item is PetReasoningEffort =>
    item === 'none' || item === 'minimal' || item === 'low' || item === 'medium' || item === 'high'
  )
}

function normalizePetModelCategory(value: unknown): string {
  const normalized = String(value ?? '').trim().toLowerCase().replace(/[\s-]+/g, '_')
  switch (normalized) {
    case 'chat':
    case 'text':
    case 'language':
    case 'llm':
    case 'completion':
    case 'completions':
      return 'chat'
    case 'speech':
    case 'audio':
    case 'tts':
    case 'text_to_speech':
    case 'texttospeech':
      return 'speech'
    case 'embedding':
    case 'embeddings':
      return 'embedding'
    case 'image':
    case 'images':
    case 'image_generation':
    case 'imagegeneration':
    case 'text_to_image':
    case 'text2image':
      return 'image'
    case 'video':
    case 'video_generation':
    case 'videogeneration':
    case 'text_to_video':
    case 'text2video':
      return 'video'
    default:
      return ''
  }
}

function inferPetModelCategory(modelId: string): string {
  const normalized = modelId.trim().toLowerCase()
  if (!normalized) return ''
  // 这里只兜底识别明确的图片模型家族，避免“包含 image”这种宽规则误伤聊天模型。
  return [
    'gpt-image',
    'dall-e',
    'imagen',
    'flux',
    'stable-diffusion',
    'stable_diffusion',
    'sdxl',
    'seedream',
    'qwen-image',
    'qwen_image',
    'image-gen',
    'image_generation',
    'imagegen'
  ].some((marker) => normalized.includes(marker)) ? 'image' : ''
}

function remoteModelCategory(model: PetProviderModelCatalogEntry | undefined, modelId: string): string {
  const explicit = [model?.modelCategory, model?.category, model?.type]
    .map(normalizePetModelCategory)
    .find(Boolean)
  if (explicit) return explicit
  if (model?.supportsImageGeneration === true || model?.supports_image_generation === true) return 'image'
  return inferPetModelCategory(modelId)
}

function modelCategory(option: PetProviderModelOption): string {
  return normalizePetModelCategory(option.modelCategory) || 'chat'
}

function geminiModelOptions(providers: GeminiProvider[]): PetProviderModelOption[] {
  return providers.flatMap((provider) => {
    const modelId = provider.model?.trim() ?? ''
    if (!provider.enabled || !provider.id.trim() || !modelId) return []
    const capabilities = provider as GeminiProvider & { reasoningEffortLevels?: unknown }
    return [{
      platform: 'gemini',
      providerId: provider.id.trim(),
      providerName: provider.name.trim() || provider.id.trim(),
      modelId,
      modelCategory: provider.modelCategory?.trim() ?? '',
      reasoningEffortLevels: normalizeReasoningEffortLevels(capabilities.reasoningEffortLevels)
    }]
  })
}

function skinPoseCount(skin: PetSkinRecord): number {
  const manifest = isRecord(skin.manifestJson) ? skin.manifestJson : {}
  return isRecord(manifest.animations) ? Object.keys(manifest.animations).length : 0
}

async function loadProviderOptions(): Promise<void> {
  const generation = ++providerRequestGeneration
  providerLoading.value = true
  providerError.value = ''
  try {
    const platforms = Array.from(new Set([
      ...PET_PROVIDER_PLATFORMS,
      form.agent.providerPlatform?.trim().toLowerCase() ?? '',
      form.dream.imageProviderPlatform?.trim().toLowerCase() ?? ''
    ].filter(Boolean)))
    const key = platforms.join(',')
    const cached = petProviderOptionsCache.get(key)
    const result = cached && cached.expiresAt > Date.now()
      ? cached.result
      : await (async () => {
        const existing = petProviderOptionsRequests.get(key)
        if (existing) return existing
        const request = (async (): Promise<PetProviderModelLoadResult> => {
          const loaded = await Promise.all(platforms.map(async (platform): Promise<PetProviderModelLoadResult> => {
            try {
              if (platform === 'gemini') {
                return {
                  options: geminiModelOptions(await Call.ByName('codeswitch/services.GeminiService.GetProviders') as GeminiProvider[]),
                  errors: []
                }
              }
              return providerModelOptions(
                platform,
                await Call.ByName('codeswitch/services.ProviderService.LoadProviders', platform) as Provider[]
              )
            } catch (error) {
              return {
                options: [],
                errors: [`${platform}: ${error instanceof Error ? error.message : String(error)}`]
              }
            }
          }))
          return {
            options: loaded.flatMap((item) => item.options),
            errors: loaded.flatMap((item) => item.errors)
          }
        })()
        petProviderOptionsRequests.set(key, request)
        try {
          const loaded = await request
          petProviderOptionsCache.set(key, {
            result: loaded,
            expiresAt: Date.now() + PET_PROVIDER_OPTIONS_CACHE_TTL_MS
          })
          return loaded
        } finally {
          if (petProviderOptionsRequests.get(key) === request) petProviderOptionsRequests.delete(key)
        }
      })()
    if (generation !== providerRequestGeneration) return
    providerOptions.value = result.options
    providerError.value = result.errors.length > 0 ? result.errors.join('; ') : ''
  } catch (error) {
    if (generation !== providerRequestGeneration) return
    providerOptions.value = []
    providerError.value = error instanceof Error ? error.message : String(error)
  } finally {
    if (generation === providerRequestGeneration) providerLoading.value = false
  }
}

async function loadProjectOptions(refresh = false): Promise<void> {
  projectLoading.value = true
  projectError.value = ''
  try {
    const result = refresh
      ? await refreshProjectManagerSnapshot()
      : await fetchProjectManagerSnapshot()
    projectOptions.value = Array.isArray(result?.projects) ? result.projects : []
  } catch (error) {
    projectOptions.value = []
    projectError.value = error instanceof Error ? error.message : String(error)
  } finally {
    projectLoading.value = false
  }
}

function petSkinPreviewCacheKey(petId: string, source: 'default' | { skinId: string }): string {
  const skinId = typeof source === 'string' ? source : source.skinId
  return `${petId}:${skinId}`
}

async function loadCachedPetSkinPreview(
  petId: string,
  source: 'default' | { skinId: string }
): Promise<PetAtlasAsset> {
  const key = petSkinPreviewCacheKey(petId, source)
  const cached = petSkinPreviewCache.get(key)
  if (cached) return cached
  const existing = petSkinPreviewRequests.get(key)
  if (existing) return existing

  const request = readPetStudioAtlas(petId, source).then((result) => result.atlas)
  petSkinPreviewRequests.set(key, request)
  try {
    const atlas = await request
    petSkinPreviewCache.set(key, atlas)
    return atlas
  } finally {
    if (petSkinPreviewRequests.get(key) === request) petSkinPreviewRequests.delete(key)
  }
}

function invalidatePetSkinPreviewCache(petId: string): void {
  const prefix = `${petId}:`
  for (const key of petSkinPreviewCache.keys()) {
    if (key.startsWith(prefix)) petSkinPreviewCache.delete(key)
  }
  for (const key of petSkinPreviewRequests.keys()) {
    if (key.startsWith(prefix)) petSkinPreviewRequests.delete(key)
  }
}

async function loadSkinPreviews(records = skins.value): Promise<void> {
  const generation = ++skinPreviewGeneration
  const customSkins = records.filter((item) => item.skinId !== 'capybara')
  skinPreviewLoading.value = Object.fromEntries(customSkins.map((skin) => [skin.skinId, true]))
  skinPreviews.value = {}

  // 预览图是可选资源，必须和首屏配置解耦；同一批皮肤并行读取，避免一个坏文件
  // 或慢磁盘把后面的缩略图排成长队。单项失败只保留占位，不影响设置操作。
  const defaultTask = loadCachedPetSkinPreview(props.petId, 'default')
    .then((atlas) => {
      if (generation === skinPreviewGeneration) defaultAtlas.value = atlas
    })
    .catch(() => {
      if (generation === skinPreviewGeneration) defaultAtlas.value = null
    })
  const skinTasks = customSkins.map(async (skin) => {
    try {
      const atlas = await loadCachedPetSkinPreview(props.petId, { skinId: skin.skinId })
      if (generation === skinPreviewGeneration) {
        skinPreviews.value = { ...skinPreviews.value, [skin.skinId]: atlas }
      }
    } catch {
      // 单个皮肤资源损坏时保留列表和绑定能力，缩略图退回占位，不阻断整个设置页。
    } finally {
      if (generation === skinPreviewGeneration) {
        const state = { ...skinPreviewLoading.value }
        delete state[skin.skinId]
        skinPreviewLoading.value = state
      }
    }
  })
  await Promise.all([defaultTask, ...skinTasks])
}

async function loadSkinRoot(): Promise<void> {
  if (petStudioRootCache) {
    skinRoot.value = petStudioRootCache
    return
  }
  try {
    const root = await getPetStudioRoot()
    petStudioRootCache = root
    skinRoot.value = root
  } catch {
    // 目录展示不是设置页的硬依赖；服务不可用时保留受控目录文案，避免阻断其他配置加载。
    skinRoot.value = ''
  }
}

async function openSkinRoot(): Promise<void> {
  if (skinRootLoading.value) return
  skinRootLoading.value = true
  errorMessage.value = ''
  try {
    await openPetStudioRoot()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : String(error)
    emit('error', error)
  } finally {
    skinRootLoading.value = false
  }
}

async function bindSkin(skinId: string | null): Promise<void> {
  if (saving.value || loading.value) return
  form.skinSelection.activeSkinId = skinId
  // 皮肤绑定是独立操作，和参考版一样点击后立即落盘；失败时保留表单值，
  // 让用户能看到待保存选择，但不把一次失败伪装成已经生效。
  await saveSettings()
}

async function refreshSkins(): Promise<void> {
  if (skinRefreshing.value) return
  skinRefreshing.value = true
  try {
    invalidatePetSkinPreviewCache(props.petId)
    const next = await petApi.getSettingsSnapshot(props.petId)
    snapshot.value = next
    skins.value = next.skins
    statsNow.value = Date.now()
    await loadSkinPreviews(next.skins)
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : String(error)
  } finally {
    skinRefreshing.value = false
  }
}

async function renamePet(): Promise<void> {
  const name = nameDraft.value.trim()
  if (!name || renaming.value || !snapshot.value || name === snapshot.value.state.name) return
  renaming.value = true
  errorMessage.value = ''
  try {
    const next = await petApi.updateName(props.petId, name)
    snapshot.value = next
    nameDraft.value = next.state.name
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : String(error)
    emit('error', error)
  } finally {
    renaming.value = false
  }
}

async function deleteSkin(skin: PetSkinRecord): Promise<void> {
  if (skin.builtin || deletingSkinId.value || skinRefreshing.value) return
  if (typeof window !== 'undefined' && !window.confirm(t('pet.settings.skins.deleteConfirm', { name: skin.name }))) return
  deletingSkinId.value = skin.skinId
  errorMessage.value = ''
  try {
    if (form.skinSelection.activeSkinId === skin.skinId) {
      form.skinSelection.activeSkinId = null
      if (!await saveSettings()) return
    }
    await deletePetStudioSkin(props.petId, skin.skinId)
    await refreshSkins()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : String(error)
    emit('error', error)
  } finally {
    deletingSkinId.value = null
  }
}

function selectTab(tab: PetTab): void {
  activeTab.value = tab
}

function formatInteger(value: number): string {
  return Math.floor(value).toLocaleString(locale.value)
}

function formatStatPercent(value: number): number {
  return Math.min(100, Math.max(0, Math.round(value)))
}

function formatExperienceTime(timestamp: number): string {
  if (!Number.isFinite(timestamp) || timestamp <= 0) return t('pet.common.unknownDate')
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(timestamp))
}

async function loadExperienceLog(generation: number): Promise<void> {
  experienceLogLoading.value = true
  experienceLogError.value = ''
  if (props.petId !== DEFAULT_PET_ID) {
    // PetService 当前注册的是 default 实例，非默认宠物不能读取它，否则会把 default 的日志串到当前宠物。
    experienceLog.value = []
    experienceLogError.value = t('pet.settings.stats.defaultOnly')
    experienceLogLoading.value = false
    return
  }
  try {
    const raw = await Call.ByName(
      PET_STATS_METHODS.listExperienceLog,
      1,
      PET_EXPERIENCE_LOG_PAGE_SIZE
    )
    if (generation !== statsRequestGeneration) return
    experienceLog.value = normalizeExperienceLog(raw, t('pet.settings.stats.unknownModel'))
  } catch (error) {
    if (generation !== statsRequestGeneration) return
    experienceLog.value = []
    experienceLogError.value = error instanceof Error ? error.message : String(error)
  } finally {
    if (generation === statsRequestGeneration) experienceLogLoading.value = false
  }
}

async function refreshStats(): Promise<void> {
  const generation = ++statsRequestGeneration
  statsRefreshing.value = true
  statsErrorMessage.value = ''
  statsNow.value = Date.now()
  try {
    // 统计刷新只更新独立快照，不回写 form，避免用户编辑未保存表单时被后台心跳覆盖。
    const next = await petApi.getRuntimeSnapshot(props.petId)
    if (generation !== statsRequestGeneration) return
    snapshot.value = {
      ...next,
      skins: snapshot.value?.skins ?? skins.value
    }
    await loadExperienceLog(generation)
  } catch (error) {
    if (generation !== statsRequestGeneration) return
    statsErrorMessage.value = error instanceof Error ? error.message : String(error)
  } finally {
    if (generation === statsRequestGeneration) statsRefreshing.value = false
  }
}

function stopStatsRefreshTimer(): void {
  if (statsRefreshTimer === undefined) return
  window.clearInterval(statsRefreshTimer)
  statsRefreshTimer = undefined
}

watch(activeTab, (tab) => {
  stopStatsRefreshTimer()
  if (tab !== 'stats') return
  void refreshStats()
  statsRefreshTimer = window.setInterval(() => {
    void refreshStats()
  }, 5_000)
})

function assignForm(value: PetSettingsForm): void {
  Object.assign(form.window, value.window)
  Object.assign(form.care, value.care)
  Object.assign(form.agent, value.agent)
  Object.assign(form.dream, value.dream)
  Object.assign(form.skinSelection, value.skinSelection)
}

function toForm(snapshot: PetSettingsSnapshot): PetSettingsForm {
  return {
    window: { ...snapshot.window },
    care: { ...snapshot.care },
    agent: { ...snapshot.agent },
    dream: { ...snapshot.dream },
    skinSelection: { ...snapshot.skinSelection }
  }
}

function normalizeNullable(value: string | null): string | null {
  const trimmed = value?.trim() ?? ''
  return trimmed ? trimmed : null
}

function normalizeConcreteModelId(value: string | null): string | null {
  const normalized = normalizeNullable(value)
  // supportedModels 的通配符只用于生成候选能力，不能作为 Agent 实际请求的 modelId。
  return normalized && !normalized.includes('*') ? normalized : null
}

function normalizeAgentConfig(agent: PetAgentConfig, petId: string): PetAgentConfig {
  return {
    ...agent,
    petId,
    providerPlatform: normalizeNullable(agent.providerPlatform),
    providerId: normalizeNullable(agent.providerId),
    modelId: normalizeConcreteModelId(agent.modelId),
    voiceProviderId: normalizeNullable(agent.voiceProviderId),
    voiceModelId: normalizeNullable(agent.voiceModelId),
    quietStart: Math.min(23, Math.max(0, Math.floor(Number(agent.quietStart) || 0))),
    quietEnd: Math.min(23, Math.max(0, Math.floor(Number(agent.quietEnd) || 0))),
    systemPrompt: agent.systemPrompt.trim(),
    voice: agent.voice.trim(),
    voiceInstruction: agent.voiceInstruction.trim(),
    voiceTag: agent.voiceTag.trim()
  }
}

function normalizeForm(value: PetSettingsForm): PetSettingsForm {
  return {
    window: { petId: props.petId, enabled: value.window.enabled },
    care: {
      petId: props.petId,
      autoCareEnabled: value.care.autoCareEnabled,
      autoCareThreshold: normalizePetAutoCareThreshold(value.care.autoCareThreshold)
    },
    agent: normalizeAgentConfig(value.agent, props.petId),
    dream: {
      petId: props.petId,
      dreamEnabled: value.dream.dreamEnabled,
      prompt: value.dream.prompt.trim(),
      keywords: value.dream.keywords.trim(),
      sleepTalkMinLength: normalizePetDreamLength(
        value.dream.sleepTalkMinLength,
        12,
        PET_DREAM_MIN_SLEEP_TALK_LENGTH,
        PET_DREAM_MAX_SLEEP_TALK_LENGTH
      ),
      bubbleMinDurationSeconds: normalizePetDreamLength(
        value.dream.bubbleMinDurationSeconds,
        12,
        PET_DREAM_MIN_BUBBLE_DURATION_SECONDS,
        PET_DREAM_MAX_BUBBLE_DURATION_SECONDS
      ),
      // 图片 provider/model 与梦境配置属于同一持久化提交；三元组缺一时保留 null，
      // 由 Go 端的原子归一化决定是否清空，避免前端拼出不可解析的半引用。
      imageProviderPlatform: normalizeNullable(value.dream.imageProviderPlatform),
      imageProviderId: normalizeNullable(value.dream.imageProviderId),
      imageModelId: normalizeNullable(value.dream.imageModelId)
    },
    skinSelection: {
      petId: props.petId,
      activeSkinId: value.skinSelection.activeSkinId && value.skinSelection.activeSkinId !== 'capybara'
        ? value.skinSelection.activeSkinId
        : null
    }
  }
}

async function loadSettings(): Promise<void> {
  loading.value = true
  errorMessage.value = ''
  try {
    const next = await petApi.getSettingsSnapshot(props.petId)
    snapshot.value = next
    statsNow.value = Date.now()
    skins.value = next.skins
    nameDraft.value = next.state.name
    // 外部 v-model 是受控值；没有受控值时才用后端快照初始化表单。
    if (!props.modelValue) assignForm(toForm(next))
    // 模型目录、项目索引和皮肤资源都不是编辑配置的必要条件；首屏拿到轻量快照
    // 后立即解除 loading，后台任务各自维护自己的 loading/error 状态。
    void loadProviderOptions()
    void loadProjectOptions()
    void loadSkinPreviews(next.skins)
    void loadSkinRoot()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : String(error)
    emit('error', error)
  } finally {
    loading.value = false
  }
}

async function saveSettings(): Promise<boolean> {
  if (saving.value || loading.value) return false
  saving.value = true
  errorMessage.value = ''
  const normalized = normalizeForm(cloneForm(form))
  assignForm(normalized)
  try {
    const next = await petApi.saveSettings(props.petId, normalized)
    // 配置已经落盘后才驱动原生窗口；副作用失败必须进入 catch，不能把部分成功伪装成完整成功。
    await petApi.setWindowEnabled(next.window.enabled)
    skins.value = next.skins
    snapshot.value = next
    nameDraft.value = next.state.name
    statsNow.value = Date.now()
    assignForm(toForm(next))
    // 配置保存是低频边界；通知独立桌宠窗口重新 hydration，避免再依赖高频
    // 完整快照轮询同步 Agent、梦境配置和当前皮肤。
    petApi.invalidateAtlas(props.petId)
    void Events.Emit('pet.settings.updated', { petId: props.petId })
    emit('saved', next)
    return true
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : String(error)
    emit('error', error)
    return false
  } finally {
    saving.value = false
  }
}

watch(
  () => props.modelValue,
  (value) => {
    if (value) assignForm(value)
  },
  { deep: true }
)

watch(
  form,
  (value) => {
    emit('update:modelValue', cloneForm(value))
  },
  { deep: true }
)

function consumeSettingsQuery(): void {
  let targetTab: PetTab | null = null
  const query = { ...route.query }
  if (route.query.studio === '1') {
    targetTab = 'studio'
    delete query.studio
  } else if (route.query.tab === 'heartbeat') {
    // 兼容历史心跳直达链接，同时把心跳的唯一用户入口收口到宠物设置页签。
    targetTab = 'heartbeat'
    delete query.tab
  }
  if (!targetTab) return

  // 设置页被 keep-alive 缓存时不会重新触发 onMounted；把 query 当作入口意图监听，
  // 才能保证桌宠菜单或历史链接再次进入设置页时仍然切到目标页签。
  selectTab(targetTab)
  void router.replace({ path: route.path, query })
}

watch(() => [route.query.studio, route.query.tab], consumeSettingsQuery)

onMounted(() => {
  consumeSettingsQuery()
  void loadSettings()
})

const agentProviderModel = computed({
  // 原生 select 需要一个真正的空值；不能让 JSON 空对象成为 sentinel，
  // 否则没有模型引用时下拉框不会命中任何 option，界面会像丢失配置。
  get: () => form.agent.providerPlatform && form.agent.providerId && form.agent.modelId
    ? JSON.stringify({
        platform: form.agent.providerPlatform,
        providerId: form.agent.providerId,
        modelId: form.agent.modelId
      })
    : '',
  set: (value: string) => {
    if (!value) {
      form.agent.providerPlatform = null
      form.agent.providerId = null
      form.agent.modelId = null
      form.agent.reasoningEffort = null
      return
    }
    try {
      const selected = JSON.parse(value) as { platform?: unknown; providerId?: unknown; modelId?: unknown }
      form.agent.providerPlatform = typeof selected.platform === 'string' && selected.platform ? selected.platform : null
      form.agent.providerId = typeof selected.providerId === 'string' && selected.providerId ? selected.providerId : null
      form.agent.modelId = typeof selected.modelId === 'string' && selected.modelId ? selected.modelId : null
      const selectedOption = visibleProviderOptions.value.find((option) =>
        option.platform === form.agent.providerPlatform &&
        option.providerId === form.agent.providerId &&
        option.modelId === form.agent.modelId
      )
      // 切换模型后，旧模型的 reasoning 等级即使新模型没有任何能力声明也必须清空，
      // 否则保存时会把上一模型的参数带到当前模型，造成“界面已切换、请求仍沿用旧能力”的隐性错配。
      if (form.agent.reasoningEffort &&
        (!selectedOption || !selectedOption.reasoningEffortLevels.includes(form.agent.reasoningEffort))) {
        form.agent.reasoningEffort = null
      }
    } catch {
      form.agent.providerPlatform = null
      form.agent.providerId = null
      form.agent.modelId = null
      form.agent.reasoningEffort = null
    }
  }
})

const visibleProviderOptions = computed(() => {
  const currentProviderId = form.agent.providerId ?? ''
  const currentModelId = form.agent.modelId ?? ''
  const chatOptions = providerOptions.value.filter((item) => modelCategory(item) === 'chat')
  if (!currentProviderId || !currentModelId || !form.agent.providerPlatform) return chatOptions
  const currentKey = JSON.stringify({
    platform: form.agent.providerPlatform,
    providerId: currentProviderId,
    modelId: currentModelId
  })
  if (chatOptions.some((item) => JSON.stringify({
    platform: item.platform,
    providerId: item.providerId,
    modelId: item.modelId
  }) === currentKey)) {
    return chatOptions
  }
  return [
    {
      platform: form.agent.providerPlatform,
      providerId: currentProviderId,
      providerName: currentProviderId,
      modelId: currentModelId,
      modelCategory: 'chat',
      reasoningEffortLevels: []
    },
    ...chatOptions
  ]
})

const agentModelGroups = computed<PetProviderModelGroup[]>(() => {
  const groups = new Map<string, PetProviderModelGroup>()
  for (const option of visibleProviderOptions.value) {
    const key = `${option.platform}:${option.providerId}`
    const group = groups.get(key) ?? {
      key,
      label: `${option.platform} · ${option.providerName}`,
      options: []
    }
    group.options.push(option)
    groups.set(key, group)
  }
  return [...groups.values()]
})

const selectedAgentModel = computed(() => visibleProviderOptions.value.find((option) =>
  option.platform === form.agent.providerPlatform &&
  option.providerId === form.agent.providerId &&
  option.modelId === form.agent.modelId
))

const visibleReasoningLevels = computed(() => selectedAgentModel.value?.reasoningEffortLevels ?? [])

watch(selectedAgentModel, (model) => {
  // provider 列表异步加载完成后，如果已保存的等级不在模型声明能力中，
  // 必须清掉无效值；不能把旧模型的 reasoning 参数偷偷带给新模型。
  if (providerLoading.value || providerOptions.value.length === 0) return
  if (form.agent.reasoningEffort &&
    (!model || !model.reasoningEffortLevels.includes(form.agent.reasoningEffort))) {
    form.agent.reasoningEffort = null
  }
})

const visibleVoiceOptions = computed(() => {
  const currentProviderId = form.agent.voiceProviderId ?? ''
  const currentModelId = form.agent.voiceModelId ?? ''
  if (!form.agent.providerPlatform) return []
  const options = providerOptions.value.filter((item) => {
    if (item.platform !== form.agent.providerPlatform) return false
    const category = modelCategory(item)
    return category === 'speech' || /tts|audio/i.test(item.modelId)
  })
  if (!currentProviderId || !currentModelId) return options
  const currentKey = JSON.stringify({ providerId: currentProviderId, modelId: currentModelId })
  if (options.some((item) => JSON.stringify({ providerId: item.providerId, modelId: item.modelId }) === currentKey)) {
    return options
  }
  return [{
    platform: form.agent.providerPlatform ?? '',
    providerId: currentProviderId,
    providerName: currentProviderId,
    modelId: currentModelId,
    modelCategory: 'speech',
    reasoningEffortLevels: []
  }, ...options]
})

const voiceModelGroups = computed<PetProviderModelGroup[]>(() => {
  const groups = new Map<string, PetProviderModelGroup>()
  for (const option of visibleVoiceOptions.value) {
    const key = `${option.platform}:${option.providerId}`
    const group = groups.get(key) ?? {
      key,
      label: `${option.platform} · ${option.providerName}`,
      options: []
    }
    group.options.push(option)
    groups.set(key, group)
  }
  return [...groups.values()]
})

const imageModelValue = computed({
  get: () => form.dream.imageProviderPlatform && form.dream.imageProviderId && form.dream.imageModelId
    ? JSON.stringify({
        platform: form.dream.imageProviderPlatform,
        providerId: form.dream.imageProviderId,
        modelId: form.dream.imageModelId
      })
    : '',
  set: (value: string) => {
    if (!value) {
      form.dream.imageProviderPlatform = null
      form.dream.imageProviderId = null
      form.dream.imageModelId = null
      return
    }
    try {
      const selected = JSON.parse(value) as { platform?: unknown; providerId?: unknown; modelId?: unknown }
      form.dream.imageProviderPlatform = typeof selected.platform === 'string' && selected.platform ? selected.platform : null
      form.dream.imageProviderId = typeof selected.providerId === 'string' && selected.providerId ? selected.providerId : null
      form.dream.imageModelId = typeof selected.modelId === 'string' && selected.modelId ? selected.modelId : null
    } catch {
      form.dream.imageProviderPlatform = null
      form.dream.imageProviderId = null
      form.dream.imageModelId = null
    }
  }
})

const visibleImageOptions = computed(() => {
  const imageOptions = providerOptions.value.filter((item) => modelCategory(item) === 'image')
  const currentPlatform = form.dream.imageProviderPlatform ?? ''
  const currentProviderId = form.dream.imageProviderId ?? ''
  const currentModelId = form.dream.imageModelId ?? ''
  if (!currentPlatform || !currentProviderId || !currentModelId) return imageOptions
  if (imageOptions.some((item) => item.platform === currentPlatform && item.providerId === currentProviderId && item.modelId === currentModelId)) {
    return imageOptions
  }
  return [{
    platform: currentPlatform,
    providerId: currentProviderId,
    providerName: currentProviderId,
    modelId: currentModelId,
    modelCategory: 'image',
    reasoningEffortLevels: []
  }, ...imageOptions]
})

const imageModelGroups = computed<PetProviderModelGroup[]>(() => {
  const groups = new Map<string, PetProviderModelGroup>()
  for (const option of visibleImageOptions.value) {
    const key = `${option.platform}:${option.providerId}`
    const group = groups.get(key) ?? {
      key,
      label: `${option.platform} · ${option.providerName}`,
      options: []
    }
    group.options.push(option)
    groups.set(key, group)
  }
  return [...groups.values()]
})

const voiceProviderModel = computed({
  get: () => form.agent.voiceProviderId && form.agent.voiceModelId
    ? JSON.stringify({
        platform: visibleVoiceOptions.value.find((option) =>
          option.providerId === form.agent.voiceProviderId && option.modelId === form.agent.voiceModelId
        )?.platform ?? form.agent.providerPlatform ?? '',
        providerId: form.agent.voiceProviderId,
        modelId: form.agent.voiceModelId
      })
    : '',
  set: (value: string) => {
    if (!value) {
      form.agent.voiceProviderId = null
      form.agent.voiceModelId = null
      return
    }
    try {
      const selected = JSON.parse(value) as { providerId?: unknown; modelId?: unknown }
      form.agent.voiceProviderId = typeof selected.providerId === 'string' && selected.providerId ? selected.providerId : null
      form.agent.voiceModelId = typeof selected.modelId === 'string' && selected.modelId ? selected.modelId : null
    } catch {
      form.agent.voiceProviderId = null
      form.agent.voiceModelId = null
    }
  }
})

const voicePresetModel = computed({
  get: () => {
    const voice = form.agent.voice.trim()
    if (!voice) return PET_VOICE_DEFAULT
    return [...PET_VOICE_PRESETS.openai, ...PET_VOICE_PRESETS.mimo].some((preset) => preset === voice)
      ? voice
      : PET_VOICE_CUSTOM
  },
  set: (value: string) => {
    if (value === PET_VOICE_DEFAULT) form.agent.voice = ''
    else if (value !== PET_VOICE_CUSTOM) form.agent.voice = value
  }
})

function resetSystemPrompt(): void {
  // 后端把空字符串解释为内置 prompt；保持这个 source-of-truth，避免前端复制一份易漂移的长文本。
  form.agent.systemPrompt = ''
}

function resetDreamPrompt(): void {
  // 梦境 prompt 同样以空值表示使用服务端默认模板，重置不会引入第二份默认配置。
  form.dream.prompt = ''
}

async function testVoice(): Promise<void> {
  const selectedVoice = visibleVoiceOptions.value.find((option) =>
    option.providerId === form.agent.voiceProviderId && option.modelId === form.agent.voiceModelId
  )
  const platform = selectedVoice?.platform?.trim() || form.agent.providerPlatform?.trim() || ''
  const providerId = form.agent.voiceProviderId?.trim() ?? ''
  const model = form.agent.voiceModelId?.trim() ?? ''
  if (!platform || !providerId || !model || voiceTesting.value) return
  voiceTesting.value = true
  voiceErrorMessage.value = ''
  try {
    await voicePreviewPlayer.playSentences([{
      petId: props.petId,
      requestId: `pet-settings-voice:${Date.now()}`,
      provider: { platform, providerId, model, capability: 'tts', autoFallback: false },
      text: t('pet.settings.voice.testSample', { name: overviewName.value }),
      voice: form.agent.voice,
      instruction: form.agent.voiceInstruction,
      voiceMode: form.agent.voiceMode,
      voiceTag: form.agent.voiceTag
    }], { preferStream: form.agent.voiceMode !== 'speech' })
  } catch (error) {
    // 试听失败只影响试听动作，不能把整个设置页切换到错误态，导致用户正在编辑的配置消失。
    voiceErrorMessage.value = error instanceof Error ? error.message : String(error)
  } finally {
    voiceTesting.value = false
  }
}

const agentProjectModel = computed({
  get: () => form.agent.projectId ?? '',
  set: (value: string) => {
    const project = projectOptions.value.find((item) => item.id === value)
    if (!project) {
      form.agent.projectId = null
      form.agent.projectName = null
      form.agent.projectFolder = null
      return
    }
    // 项目 ID、显示名和工作目录必须同源更新，否则后端解析到的项目与 UI 显示会错位。
    form.agent.projectId = project.id
    form.agent.projectName = project.display_name
    form.agent.projectFolder = project.path
  }
})

const visibleProjectOptions = computed(() => {
  const currentId = form.agent.projectId
  if (!currentId || projectOptions.value.some((project) => project.id === currentId)) return projectOptions.value
  return [{
    id: currentId,
    path: form.agent.projectFolder ?? '',
    source_name: form.agent.projectName ?? currentId,
    display_name: form.agent.projectName ?? currentId,
    updated_at: 0,
    session_count: 0,
    codex_provider_auto: true
  }, ...projectOptions.value]
})

const companionDateLabel = computed(() => {
  const adoptedAt = snapshot.value?.state.adoptedAt ?? 0
  if (!Number.isFinite(adoptedAt) || adoptedAt <= 0) return t('pet.settings.overview.noDate')
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium' }).format(new Date(adoptedAt))
})

const overviewName = computed(() => snapshot.value?.state.name ?? nameDraft.value)
const proactiveDailyCap = computed(() => PET_PROACTIVE_DAILY_CAP[form.agent.proactiveFreq])

onUnmounted(() => {
  stopStatsRefreshTimer()
  statsRequestGeneration += 1
  providerRequestGeneration += 1
  skinPreviewGeneration += 1
  voicePreviewPlayer.dispose()
})
</script>

<template>
  <div class="pet-settings">
    <header class="pet-settings__header">
      <nav class="pet-settings__tabs" role="tablist" :aria-label="t('pet.settings.tabsAria')">
        <button
          v-for="tab in PET_TABS"
          :key="tab.id"
          type="button"
          role="tab"
          :aria-selected="activeTab === tab.id"
          :class="['pet-settings__tab', { 'is-active': activeTab === tab.id }]"
          @click="selectTab(tab.id)"
        >
          <component :is="tab.icon" class="pet-settings__tab-icon" :size="14" :stroke-width="1.9" aria-hidden="true" />
          <span>{{ t(`pet.settings.tabs.${tab.id}`) }}</span>
        </button>
      </nav>
    </header>

    <div v-if="loading" class="pet-settings__state">{{ t('pet.settings.loading') }}</div>
    <div v-else-if="errorMessage" class="pet-settings__state is-error">
      <span>{{ t('pet.settings.operationFailed', { error: errorMessage }) }}</span>
      <button type="button" class="pet-settings__retry" @click="loadSettings">{{ t('pet.common.retry') }}</button>
    </div>

    <div v-else class="pet-settings__content">
      <div v-show="activeTab === 'studio'" class="pet-settings__wide-content pet-settings__studio">
        <PetStudio :pet-id="props.petId" />
      </div>

      <div class="pet-settings__main-content">
        <div v-show="activeTab === 'overview'" class="pet-settings__overview-content">
          <div class="pet-settings__overview-preview">
            <PetAtlasFrame
              v-if="defaultAtlas"
              :image-url="defaultAtlas.src"
              :manifest="defaultAtlas.manifest"
              action="idle"
              :display-height="150"
            />
            <div v-else class="pet-settings__overview-placeholder">{{ t('pet.settings.overview.previewUnavailable') }}</div>
          </div>

          <section class="pet-settings__section">
            <div class="pet-settings__setting-row">
              <div>
                <strong>{{ t('pet.settings.overview.windowEnabled') }}</strong>
                <span>{{ t('pet.settings.overview.windowEnabledHint') }}</span>
              </div>
              <label class="pet-settings__switch">
                <input v-model="form.window.enabled" type="checkbox" @change="void saveSettings()" />
                <span aria-hidden="true"></span>
              </label>
            </div>
          </section>

          <section class="pet-settings__section pet-settings__auto-care">
            <div class="pet-settings__setting-row">
              <div class="pet-settings__setting-copy">
                <strong>{{ t('pet.settings.overview.autoCare') }}</strong>
                <span>{{ t('pet.settings.overview.autoCareHint') }}</span>
              </div>
              <label class="pet-settings__switch">
                <input v-model="form.care.autoCareEnabled" type="checkbox" @change="void saveSettings()" />
                <span aria-hidden="true"></span>
              </label>
            </div>
            <div class="pet-settings__auto-care-control">
              <div class="pet-settings__auto-care-label">
                <span>{{ t('pet.settings.overview.autoCareThreshold') }}</span>
                <output>{{ form.care.autoCareThreshold }}%</output>
              </div>
              <input
                v-model.number="form.care.autoCareThreshold"
                class="pet-settings__range"
                type="range"
                :min="PET_AUTO_CARE_MIN_THRESHOLD"
                :max="PET_AUTO_CARE_MAX_THRESHOLD"
                :step="PET_AUTO_CARE_THRESHOLD_STEP"
                :disabled="!form.care.autoCareEnabled"
                :aria-label="t('pet.settings.overview.autoCareThreshold')"
                @change="void saveSettings()"
              />
              <div class="pet-settings__range-hint"><span>{{ PET_AUTO_CARE_MIN_THRESHOLD }}%</span><span>{{ PET_AUTO_CARE_MAX_THRESHOLD }}%</span></div>
            </div>
          </section>

          <section class="pet-settings__section pet-settings__overview-profile">
            <p class="pet-settings__overview-profile-title">{{ t('pet.settings.overview.basicTitle') }}</p>
            <div class="pet-settings__name-row">
              <input
                v-model="nameDraft"
                class="pet-settings__name-input"
                type="text"
                maxlength="20"
                :placeholder="t('pet.settings.overview.namePlaceholder')"
                :aria-label="t('pet.settings.overview.name')"
                @keyup.enter="renamePet"
              />
              <button
                type="button"
                class="pet-settings__secondary-button"
                :disabled="renaming || !nameDraft.trim() || nameDraft.trim() === overviewName"
                @click="renamePet"
              >
                {{ renaming ? t('pet.common.saving') : t('pet.settings.overview.rename') }}
              </button>
            </div>
            <p v-if="snapshot" class="pet-settings__overview-summary">
              {{ t('pet.settings.overview.summary', { level: petLevel, coins: formatInteger(snapshot.state.coins), date: companionDateLabel }) }}
            </p>
          </section>

          <p class="pet-settings__hint">{{ t('pet.settings.overview.hint') }}</p>
        </div>

        <p v-show="activeTab === 'agent'" class="pet-settings__tab-description">
          {{ t('pet.settings.agent.description') }}
        </p>
        <p v-show="activeTab === 'agent'" class="pet-settings__hint">
          {{ t('pet.settings.agent.codexDefaultHint') }}
        </p>
        <section v-show="activeTab === 'agent'" class="pet-settings__section">
          <div class="pet-settings__section-title">
            <div>
              <h3>{{ t('pet.settings.agent.model') }}</h3>
            </div>
          </div>

          <div class="pet-settings__field-grid">
            <label class="pet-settings__field pet-settings__field--wide">
              <span>{{ t('pet.settings.agent.modelReference') }}</span>
              <select v-model="agentProviderModel" :disabled="providerLoading">
                <option value="">{{ providerLoading ? t('pet.settings.agent.loadingModels') : t('pet.settings.agent.modelPlaceholder') }}</option>
                <optgroup v-for="group in agentModelGroups" :key="group.key" :label="group.label">
                  <option
                    v-for="option in group.options"
                    :key="`${option.platform}:${option.providerId}:${option.modelId}`"
                    :value="JSON.stringify({ platform: option.platform, providerId: option.providerId, modelId: option.modelId })"
                  >
                    {{ option.modelId }}
                  </option>
                </optgroup>
              </select>
            </label>
            <label class="pet-settings__field">
              <span>{{ t('pet.settings.agent.reasoningEffort') }}</span>
              <select v-model="reasoningEffortModel" :disabled="!selectedAgentModel">
                <option value="">{{ t('pet.settings.agent.followModel') }}</option>
                <option v-for="level in visibleReasoningLevels" :key="level" :value="level">
                  {{ t(`pet.settings.reasoning.${level}`) }}
                </option>
              </select>
            </label>
          </div>

          <p v-if="providerError" class="pet-settings__field-error">{{ t('pet.settings.agent.providerLoadFailed', { error: providerError }) }}</p>
          <p v-else-if="!providerLoading && agentModelGroups.length === 0" class="pet-settings__hint">
            {{ t('pet.settings.agent.noModels') }}
          </p>
          <p v-else-if="selectedAgentModel && visibleReasoningLevels.length === 0" class="pet-settings__hint">
            {{ t('pet.settings.agent.reasoningUnavailable') }}
          </p>
        </section>

        <section v-show="activeTab === 'agent'" class="pet-settings__section">
          <div class="pet-settings__field-heading">
            <span>{{ t('pet.settings.agent.systemPrompt') }}</span>
            <button type="button" class="pet-settings__text-button" @click="resetSystemPrompt">
              {{ t('pet.settings.agent.resetPrompt') }}
            </button>
          </div>
          <label class="pet-settings__field">
            <textarea v-model="agentPromptModel" rows="10" :placeholder="t('pet.settings.agent.systemPromptPlaceholder')"></textarea>
          </label>
          <p class="pet-settings__hint">{{ t('pet.settings.agent.promptHint') }}</p>
        </section>

        <section v-show="activeTab === 'agent'" class="pet-settings__section">
          <div class="pet-settings__section-title">
            <div>
              <h3>{{ t('pet.settings.agent.project') }}</h3>
            </div>
          </div>
          <label class="pet-settings__field">
            <span class="pet-settings__visually-hidden">{{ t('pet.settings.agent.project') }}</span>
            <select v-model="agentProjectModel" :disabled="projectLoading">
              <option value="">{{ projectLoading ? t('pet.settings.agent.loadingProjects') : t('pet.settings.agent.projectNone') }}</option>
              <option v-for="project in visibleProjectOptions" :key="project.id" :value="project.id">
                {{ project.display_name }}
              </option>
            </select>
          </label>
          <p class="pet-settings__hint">{{ t('pet.settings.agent.projectHint') }}</p>
          <p v-if="projectError" class="pet-settings__field-error">{{ t('pet.settings.agent.projectLoadFailed', { error: projectError }) }}</p>
        </section>

        <section v-show="activeTab === 'agent'" class="pet-settings__section">
          <div class="pet-settings__section-title pet-settings__section-title--setting">
            <div>
              <h3>{{ t('pet.settings.agent.proactive') }}</h3>
              <p>{{ t('pet.settings.agent.proactiveDesc') }}</p>
            </div>
            <label class="pet-settings__switch">
              <input v-model="form.agent.proactive" type="checkbox" />
              <span aria-hidden="true"></span>
            </label>
          </div>
          <div v-if="form.agent.proactive" class="pet-settings__field-grid">
            <label class="pet-settings__field">
              <span>{{ t('pet.settings.agent.proactiveFrequency') }}</span>
              <select v-model="form.agent.proactiveFreq">
                <option value="low">{{ t('pet.settings.frequency.low') }}</option>
                <option value="medium">{{ t('pet.settings.frequency.medium') }}</option>
                <option value="high">{{ t('pet.settings.frequency.high') }}</option>
              </select>
            </label>
            <label class="pet-settings__field">
              <span>{{ t('pet.settings.agent.quietStart') }}</span>
              <select v-model.number="form.agent.quietStart">
                <option v-for="hour in PET_HOURS" :key="`start:${hour}`" :value="hour">{{ String(hour).padStart(2, '0') }}:00</option>
              </select>
            </label>
            <label class="pet-settings__field">
              <span>{{ t('pet.settings.agent.quietEnd') }}</span>
              <select v-model.number="form.agent.quietEnd">
                <option v-for="hour in PET_HOURS" :key="`end:${hour}`" :value="hour">{{ String(hour).padStart(2, '0') }}:00</option>
              </select>
            </label>
          </div>
          <p class="pet-settings__hint">{{ t('pet.settings.agent.proactiveQuota', { count: proactiveDailyCap }) }}</p>
        </section>

        <section v-show="activeTab === 'agent'" class="pet-settings__section">
        <div class="pet-settings__section-title pet-settings__section-title--setting">
          <div>
            <h3>{{ t('pet.settings.voice.title') }}</h3>
            <p>{{ t('pet.settings.voice.subtitle') }}</p>
          </div>
          <label class="pet-settings__switch">
            <input v-model="form.agent.voiceEnabled" type="checkbox" />
            <span aria-hidden="true"></span>
          </label>
        </div>
        <template v-if="form.agent.voiceEnabled">
          <label class="pet-settings__field pet-settings__field--wide">
            <span>{{ t('pet.settings.voice.modelReference') }}</span>
            <select v-model="voiceProviderModel" :disabled="providerLoading || !form.agent.providerPlatform">
              <option value="">{{ providerLoading ? t('pet.settings.agent.loadingModels') : t('pet.settings.voice.modelPlaceholder') }}</option>
              <optgroup v-for="group in voiceModelGroups" :key="`voice:${group.key}`" :label="group.label">
                <option
                  v-for="option in group.options"
                  :key="`voice:${option.platform}:${option.providerId}:${option.modelId}`"
                  :value="JSON.stringify({ platform: option.platform, providerId: option.providerId, modelId: option.modelId })"
                >
                  {{ option.modelId }}
                </option>
              </optgroup>
            </select>
          </label>
          <p v-if="voiceModelGroups.length === 0" class="pet-settings__hint">{{ t('pet.settings.voice.noModels') }}</p>
          <div class="pet-settings__field-grid">
            <label class="pet-settings__field">
              <span>{{ t('pet.settings.voice.voice') }}</span>
              <select v-model="voicePresetModel">
                <option :value="PET_VOICE_DEFAULT">{{ t('pet.settings.voice.default') }}</option>
                <optgroup label="OpenAI">
                  <option v-for="voice in PET_VOICE_PRESETS.openai" :key="`openai:${voice}`" :value="voice">{{ voice }}</option>
                </optgroup>
                <optgroup label="MiMo">
                  <option v-for="voice in PET_VOICE_PRESETS.mimo" :key="`mimo:${voice}`" :value="voice">{{ voice }}</option>
                </optgroup>
                <option :value="PET_VOICE_CUSTOM">{{ t('pet.settings.voice.custom') }}</option>
              </select>
            </label>
            <label v-if="voicePresetModel === PET_VOICE_CUSTOM" class="pet-settings__field">
              <span>{{ t('pet.settings.voice.voice') }}</span>
            <input v-model="form.agent.voice" type="text" maxlength="80" :placeholder="t('pet.settings.voice.voicePlaceholder')" />
            </label>
            <label class="pet-settings__field">
              <span>{{ t('pet.settings.voice.mode') }}</span>
              <select v-model="form.agent.voiceMode">
                <option value="auto">{{ t('pet.settings.voice.auto') }}</option>
                <option value="speech">{{ t('pet.settings.voice.speech') }}</option>
                <option value="chat">{{ t('pet.settings.voice.chat') }}</option>
              </select>
            </label>
          </div>
          <div class="pet-settings__field-actions">
            <button type="button" class="pet-settings__secondary-button" :disabled="voiceTesting || !form.agent.voiceProviderId || !form.agent.voiceModelId" @click="testVoice">
              {{ voiceTesting ? t('pet.settings.voice.testing') : t('pet.settings.voice.test') }}
            </button>
          </div>
           <label class="pet-settings__field">
             <span>{{ t('pet.settings.voice.tag') }}</span>
             <input v-model="form.agent.voiceTag" type="text" maxlength="40" :placeholder="t('pet.settings.voice.tagPlaceholder')" />
           </label>
           <label class="pet-settings__field">
             <span>{{ t('pet.settings.voice.instruction') }}</span>
             <input v-model="form.agent.voiceInstruction" type="text" maxlength="200" :placeholder="t('pet.settings.voice.instructionPlaceholder')" />
           </label>
           <p v-if="voiceErrorMessage" class="pet-settings__field-error">{{ t('pet.settings.voice.testFailed', { error: voiceErrorMessage }) }}</p>
           <p class="pet-settings__hint">{{ t('pet.settings.voice.hint') }}</p>
        </template>
        </section>

        <div v-show="activeTab === 'agent'" class="pet-settings__save-row">
          <button type="button" class="pet-settings__save" :disabled="loading || saving" @click="saveSettings">
            {{ saving ? t('pet.common.saving') : t('pet.settings.save') }}
          </button>
        </div>

        <p v-show="activeTab === 'sleep'" class="pet-settings__tab-description">
          {{ t('pet.settings.dream.description') }}
        </p>
        <section v-show="activeTab === 'sleep'" class="pet-settings__section">
          <div class="pet-settings__section-title pet-settings__section-title--setting">
            <div>
              <h3>{{ t('pet.settings.dream.dreamTalk') }}</h3>
              <p>{{ t('pet.settings.dream.dreamTalkHint') }}</p>
            </div>
            <label class="pet-settings__switch">
              <input v-model="form.dream.dreamEnabled" type="checkbox" />
              <span aria-hidden="true"></span>
            </label>
          </div>

          <div class="pet-settings__field-grid pet-settings__dream-ranges">
            <label class="pet-settings__field">
              <span>{{ t('pet.settings.dream.sleepTalkMinLength') }} <output>{{ form.dream.sleepTalkMinLength }}</output></span>
              <input
                v-model.number="form.dream.sleepTalkMinLength"
                class="pet-settings__range"
                type="range"
                :min="PET_DREAM_MIN_SLEEP_TALK_LENGTH"
                :max="PET_DREAM_MAX_SLEEP_TALK_LENGTH"
                step="1"
              />
            </label>
            <label class="pet-settings__field">
              <span>{{ t('pet.settings.dream.bubbleDuration') }} <output>{{ t('pet.settings.dream.seconds', { count: form.dream.bubbleMinDurationSeconds }) }}</output></span>
              <input
                v-model.number="form.dream.bubbleMinDurationSeconds"
                class="pet-settings__range"
                type="range"
                :min="PET_DREAM_MIN_BUBBLE_DURATION_SECONDS"
                :max="PET_DREAM_MAX_BUBBLE_DURATION_SECONDS"
                step="1"
              />
            </label>
          </div>
        </section>

        <section v-show="activeTab === 'sleep'" class="pet-settings__section">
          <div class="pet-settings__field-heading">
            <span>{{ t('pet.settings.dream.prompt') }}</span>
            <button type="button" class="pet-settings__text-button" @click="resetDreamPrompt">
              {{ t('pet.settings.dream.resetPrompt') }}
            </button>
          </div>
          <label class="pet-settings__field">
            <textarea v-model="dreamPromptModel" rows="9" :placeholder="t('pet.settings.dream.promptPlaceholder')"></textarea>
          </label>
          <p class="pet-settings__hint">{{ t('pet.settings.dream.promptHint') }}</p>
          <label class="pet-settings__field">
            <span>{{ t('pet.settings.dream.keywords') }}</span>
            <input v-model="form.dream.keywords" type="text" :placeholder="t('pet.settings.dream.keywordsPlaceholder')" />
          </label>
          <p class="pet-settings__hint">{{ t('pet.settings.dream.keywordsHint') }}</p>
        </section>

        <section v-show="activeTab === 'sleep'" class="pet-settings__section">
          <div class="pet-settings__section-title">
            <div>
              <h3>{{ t('pet.settings.dream.imageModel') }}</h3>
              <p>{{ t('pet.settings.dream.imageModelHint') }}</p>
            </div>
          </div>
          <select v-model="imageModelValue" :disabled="providerLoading">
            <option value="">{{ providerLoading ? t('pet.settings.agent.loadingModels') : t('pet.settings.dream.imageModelPlaceholder') }}</option>
            <optgroup v-for="group in imageModelGroups" :key="`image:${group.key}`" :label="group.label">
              <option
                v-for="option in group.options"
                :key="`image:${option.platform}:${option.providerId}:${option.modelId}`"
                :value="JSON.stringify({ platform: option.platform, providerId: option.providerId, modelId: option.modelId })"
              >
                {{ option.modelId }}
              </option>
            </optgroup>
          </select>
          <p v-if="!providerLoading && imageModelGroups.length === 0" class="pet-settings__hint">
            {{ t('pet.settings.dream.noImageModels') }}
          </p>
        </section>

        <div v-show="activeTab === 'sleep'" class="pet-settings__save-row">
          <button type="button" class="pet-settings__save" :disabled="loading || saving" @click="saveSettings">
            {{ saving ? t('pet.common.saving') : t('pet.settings.save') }}
          </button>
        </div>

        <div v-show="activeTab === 'skins'" class="pet-settings__skin-content">
          <section class="pet-settings__skin-toolbar" aria-labelledby="pet-settings-skins-title">
            <div class="pet-settings__skin-toolbar-top">
              <div class="pet-settings__skin-heading">
                <h3 id="pet-settings-skins-title">{{ t('pet.settings.skins.title') }}</h3>
                <p>{{ t('pet.settings.skins.subtitle') }}</p>
              </div>

              <div class="pet-settings__skin-window-control">
                <Monitor class="pet-settings__skin-toolbar-icon" :size="17" :stroke-width="1.8" aria-hidden="true" />
                <div class="pet-settings__skin-window-copy">
                  <strong>{{ t('pet.settings.overview.windowEnabled') }}</strong>
                  <span>{{ t(form.window.enabled ? 'pet.settings.skins.displayOn' : 'pet.settings.skins.displayOff') }}</span>
                </div>
                <label class="pet-settings__switch">
                  <input
                    v-model="form.window.enabled"
                    type="checkbox"
                    :aria-label="t('pet.settings.overview.windowEnabled')"
                    @change="void saveSettings()"
                  />
                  <span aria-hidden="true"></span>
                </label>
              </div>
            </div>

            <div class="pet-settings__skin-directory-row">
              <span
                class="pet-settings__managed-directory"
                :title="skinRoot || t('pet.settings.skins.directoryUnavailable')"
              >
                {{ skinRoot || t('pet.settings.skins.directoryUnavailable') }}
              </span>
              <div class="pet-settings__skin-actions">
                <button type="button" class="pet-settings__secondary-button" :disabled="skinRootLoading" @click="openSkinRoot">
                  <FolderOpen class="pet-settings__button-icon" :size="14" :stroke-width="1.9" aria-hidden="true" />
                  <span>{{ t('pet.settings.skins.openFolder') }}</span>
                </button>
                <button type="button" class="pet-settings__secondary-button" :disabled="skinRefreshing" @click="refreshSkins">
                  <RefreshCw :class="['pet-settings__button-icon', { 'is-spinning': skinRefreshing }]" :size="14" :stroke-width="1.9" aria-hidden="true" />
                  <span>{{ skinRefreshing ? t('pet.common.refreshing') : t('pet.settings.skins.refresh') }}</span>
                </button>
              </div>
            </div>
            <p class="pet-settings__drop-hint">{{ t('pet.settings.skins.dropHint') }}</p>
          </section>

          <div class="pet-settings__skin-list">
            <article class="pet-settings__skin-card" :class="{ 'is-active': defaultSkinActive }">
              <div class="pet-settings__skin-thumb">
                <PetAtlasFrame
                  v-if="defaultAtlas"
                  :image-url="defaultAtlas.src"
                  :manifest="defaultAtlas.manifest"
                  action="idle"
                  :display-height="96"
                />
                <span v-else class="pet-settings__skin-preview-placeholder" aria-hidden="true">·</span>
              </div>
              <div class="pet-settings__skin-copy">
                <strong>{{ t('pet.settings.skins.default') }}</strong>
                <span>{{ t('pet.settings.skins.builtinHint') }}</span>
              </div>
              <div class="pet-settings__skin-card-footer">
                <span v-if="defaultSkinActive" class="pet-settings__skin-status is-active">{{ t('pet.settings.skins.inUse') }}</span>
                <button v-else type="button" class="pet-settings__secondary-button" :disabled="saving" @click="void bindSkin(null)">
                  {{ t('pet.settings.skins.use') }}
                </button>
              </div>
            </article>

            <article
              v-for="skin in selectableSkinRecords"
              :key="skin.skinId"
              class="pet-settings__skin-card"
              :class="{ 'is-active': form.skinSelection.activeSkinId === skin.skinId }"
            >
              <div class="pet-settings__skin-thumb">
                <PetAtlasFrame
                  v-if="skinPreviews[skin.skinId]"
                  :image-url="skinPreviews[skin.skinId].src"
                  :manifest="skinPreviews[skin.skinId].manifest"
                  action="idle"
                  :display-height="96"
                />
                <span v-else class="pet-settings__skin-preview-placeholder" aria-hidden="true">
                  {{ skinPreviewLoading[skin.skinId] ? '…' : '·' }}
                </span>
              </div>
              <div class="pet-settings__skin-copy">
                <strong>{{ skin.name }}</strong>
                <span>{{ skin.skinId }}{{ skin.modelId ? ` · ${skin.modelId}` : '' }} · {{ t('pet.settings.skins.poseCount', { count: skinPoseCount(skin) }) }}</span>
              </div>
              <div class="pet-settings__skin-card-footer">
                <span v-if="form.skinSelection.activeSkinId === skin.skinId" class="pet-settings__skin-status is-active">{{ t('pet.settings.skins.inUse') }}</span>
                <button v-else type="button" class="pet-settings__secondary-button" :disabled="saving" @click="void bindSkin(skin.skinId)">
                  {{ t('pet.settings.skins.use') }}
                </button>
              </div>
              <button
                v-if="!skin.builtin"
                type="button"
                class="pet-settings__icon-button pet-settings__skin-delete"
                :disabled="deletingSkinId !== null || skinRefreshing"
                :title="t('pet.settings.skins.delete')"
                :aria-label="t('pet.settings.skins.delete')"
                @click="deleteSkin(skin)"
              >
                <Trash2 :size="14" :stroke-width="1.9" aria-hidden="true" />
              </button>
            </article>
          </div>
          <p v-if="selectableSkinRecords.length === 0" class="pet-settings__skin-empty-hint">{{ t('pet.settings.skins.emptyHint') }}</p>
        </div>

        <div v-show="activeTab === 'stats'" class="pet-settings__stats-content">
        <div v-if="statsErrorMessage" class="pet-settings__stats-error">
          <span>{{ t('pet.settings.stats.loadFailed', { error: statsErrorMessage }) }}</span>
          <button type="button" class="pet-settings__secondary-button" @click="refreshStats">{{ t('pet.common.retry') }}</button>
        </div>

        <template v-if="snapshot">
          <section class="pet-settings__stats-block">
            <div class="pet-settings__stats-block-title">
              <h4>{{ t('pet.settings.stats.needs') }}</h4>
              <div class="pet-settings__stats-block-actions">
                <span>{{ t('pet.settings.stats.range') }}</span>
                <button
                  type="button"
                  class="pet-settings__icon-button"
                  :disabled="statsRefreshing"
                  :title="t('pet.settings.stats.refresh')"
                  :aria-label="t('pet.settings.stats.refresh')"
                  @click="refreshStats"
                >
                  {{ statsRefreshing ? '…' : '↻' }}
                </button>
              </div>
            </div>
            <div class="pet-settings__stat-row">
              <span class="pet-settings__stat-label"><span class="pet-settings__stat-icon is-hunger" aria-hidden="true"></span>{{ t('pet.settings.stats.hunger') }}</span>
              <div class="pet-settings__stat-track">
                <span :class="['pet-settings__stat-fill', 'is-hunger', { 'is-low': formatStatPercent(snapshot.state.hunger) < 30 }]" :style="{ width: `${formatStatPercent(snapshot.state.hunger)}%` }"></span>
              </div>
              <span class="pet-settings__stat-value">{{ formatStatPercent(snapshot.state.hunger) }} / 100</span>
            </div>
            <div class="pet-settings__stat-row">
              <span class="pet-settings__stat-label"><span class="pet-settings__stat-icon is-cleanliness" aria-hidden="true"></span>{{ t('pet.settings.stats.cleanliness') }}</span>
              <div class="pet-settings__stat-track">
                <span :class="['pet-settings__stat-fill', 'is-cleanliness', { 'is-low': formatStatPercent(snapshot.state.cleanliness) < 30 }]" :style="{ width: `${formatStatPercent(snapshot.state.cleanliness)}%` }"></span>
              </div>
              <span class="pet-settings__stat-value">{{ formatStatPercent(snapshot.state.cleanliness) }} / 100</span>
            </div>
            <div class="pet-settings__stat-row">
              <span class="pet-settings__stat-label"><span class="pet-settings__stat-icon is-mood" aria-hidden="true"></span>{{ t('pet.settings.stats.mood') }}</span>
              <div class="pet-settings__stat-track">
                <span :class="['pet-settings__stat-fill', 'is-mood', { 'is-low': formatStatPercent(snapshot.state.mood) < 30 }]" :style="{ width: `${formatStatPercent(snapshot.state.mood)}%` }"></span>
              </div>
              <span class="pet-settings__stat-value">{{ formatStatPercent(snapshot.state.mood) }} / 100</span>
            </div>
          </section>

          <section class="pet-settings__stats-block">
            <div class="pet-settings__stats-block-title">
              <h4>{{ t('pet.settings.stats.levelGrowth') }}</h4>
              <span>{{ t('pet.settings.stats.growthProgress', { current: formatInteger(combinedGrowth), next: formatInteger(nextLevelGrowth) }) }}</span>
            </div>
            <div class="pet-settings__growth-row">
              <strong>Lv.{{ petLevel }}</strong>
              <div class="pet-settings__stat-track">
                <span class="pet-settings__stat-fill is-growth" :style="{ width: `${levelProgressPercent}%` }"></span>
              </div>
              <span>Lv.{{ petLevel + 1 }}</span>
            </div>
            <p class="pet-settings__stats-note">{{ t('pet.settings.stats.growthNote', { progress: levelProgressPercent }) }}</p>
          </section>

          <section class="pet-settings__stats-block">
            <div class="pet-settings__stats-block-title">
              <h4>{{ t('pet.settings.stats.experienceTokens') }}</h4>
              <span v-if="experienceLogLoading">{{ t('pet.settings.stats.logLoading') }}</span>
              <span v-else-if="experienceLogError" class="is-error-text">{{ t('pet.settings.stats.logUnavailable') }}</span>
              <span v-else>{{ t('pet.settings.stats.logCount', { count: formatInteger(experienceLog.length) }) }}</span>
            </div>
            <div class="pet-settings__stats-inline-values">
              <div>
                <span>{{ t('pet.settings.stats.totalExperience') }}</span>
                <strong>{{ formatInteger(snapshot.experience.totalExp) }} EXP</strong>
              </div>
              <div>
                <span>{{ t('pet.settings.stats.totalTokens') }}</span>
                <strong>{{ formatInteger(snapshot.experience.totalTokens) }}</strong>
              </div>
            </div>
            <p v-if="experienceLogLoading" class="pet-settings__stats-empty">{{ t('pet.settings.stats.logLoadingDetail') }}</p>
            <p v-else-if="experienceLogError" class="pet-settings__stats-empty is-error-text">
              {{ t('pet.settings.stats.logError', { error: experienceLogError }) }}
            </p>
            <p v-else-if="experienceLog.length === 0" class="pet-settings__stats-empty">
              {{ t('pet.settings.stats.logEmpty') }}
            </p>
            <div v-else class="pet-settings__experience-log">
              <div v-for="(entry, index) in experienceLog" :key="`${entry.id}-${index}`" class="pet-settings__experience-entry">
                <span class="pet-settings__experience-time">{{ formatExperienceTime(entry.at) }}</span>
                <span class="pet-settings__experience-model">{{ entry.model }}</span>
                <span :class="['pet-settings__experience-badge', { 'is-premium': entry.premium }]">
                  {{ entry.premium ? t('pet.settings.stats.premium') : t('pet.settings.stats.base') }}
                </span>
                <span class="pet-settings__experience-tokens">{{ t('pet.settings.stats.tokenEntry', { count: formatInteger(entry.tokens) }) }}</span>
                <strong>{{ t('pet.settings.stats.experienceEntry', { count: formatInteger(entry.exp) }) }}</strong>
              </div>
            </div>
          </section>

          <div class="pet-settings__stats-summary">
            <div class="pet-settings__stats-card">
              <span class="pet-settings__stats-card-icon is-coins" aria-hidden="true"></span>
              <span class="pet-settings__stats-card-copy">
                <span>{{ t('pet.settings.stats.coins') }}</span>
                <strong>{{ formatInteger(snapshot.state.coins) }}</strong>
              </span>
            </div>
            <div class="pet-settings__stats-card">
              <span class="pet-settings__stats-card-icon is-token" aria-hidden="true"></span>
              <span class="pet-settings__stats-card-copy">
                <span>{{ t('pet.settings.stats.token') }}</span>
                <strong>{{ formatInteger(snapshot.experience.totalTokens) }}</strong>
              </span>
            </div>
            <div class="pet-settings__stats-card">
              <span class="pet-settings__stats-card-icon is-days" aria-hidden="true"></span>
              <span class="pet-settings__stats-card-copy">
                <span>{{ t('pet.settings.stats.companionDays') }}</span>
                <strong>{{ companionDays > 0 ? t('pet.settings.stats.days', { count: companionDays }) : t('pet.settings.stats.noData') }}</strong>
              </span>
            </div>
            <div class="pet-settings__stats-card">
              <span class="pet-settings__stats-card-icon is-status" aria-hidden="true"></span>
              <span class="pet-settings__stats-card-copy">
                <span>{{ t('pet.settings.stats.currentStatus') }}</span>
                <strong>{{ petStatusLabel }}</strong>
              </span>
            </div>
          </div>

          <section class="pet-settings__stats-block">
            <div class="pet-settings__stats-block-title">
              <h4>{{ t('pet.settings.stats.unlocks') }}</h4>
              <span>{{ t('pet.settings.stats.currentLevel', { level: petLevel }) }}</span>
            </div>
            <div class="pet-settings__unlock-list">
              <div v-for="unlock in PET_UNLOCKS" :key="unlock.id" class="pet-settings__unlock-row">
                <span>{{ t(`pet.settings.unlocks.${unlock.id}`) }}</span>
                <span v-if="petLevel >= unlock.level" class="is-unlocked"><span class="pet-settings__unlock-icon is-check" aria-hidden="true"></span>{{ t('pet.settings.stats.unlocked') }}</span>
                <span v-else class="is-locked"><span class="pet-settings__unlock-icon is-lock" aria-hidden="true"></span>{{ t('pet.settings.stats.unlockAt', { level: unlock.level }) }}</span>
              </div>
            </div>
          </section>
        </template>
        <div v-else class="pet-settings__stats-empty">{{ t('pet.settings.stats.noSnapshot') }}</div>
        </div>

        <div v-show="activeTab === 'memory'">
          <PetMemoryPanel :pet-id="props.petId" />
        </div>
      </div>

      <div v-show="activeTab === 'dream-history'" class="pet-settings__wide-content">
        <PetDreamHistoryPanel :pet-id="props.petId" />
      </div>

      <div v-show="activeTab === 'heartbeat'" class="pet-settings__wide-content pet-settings__heartbeat">
        <PetHeartbeat embedded />
      </div>

    </div>
  </div>
</template>

<style scoped>
.pet-settings {
  --settings-ink: var(--mac-text, #1d1d1f);
  --settings-muted: var(--mac-text-secondary, #6e6e73);
  --settings-line: var(--mac-border, rgba(15, 23, 42, 0.12));
  --settings-surface: var(--mac-surface, #ffffff);
  --settings-strong-surface: var(--mac-surface-strong, #f5f5f7);
  display: flex;
  flex-direction: column;
  gap: 18px;
  width: 100%;
  max-width: none;
  min-width: 0;
  box-sizing: border-box;
  margin: 0;
  padding: 32px 48px 48px;
  color: var(--settings-ink);
  font-family: var(--mac-font, system-ui, sans-serif);
}

.pet-settings__header-actions,
.pet-settings__setting-row,
.pet-settings__inline-controls,
.pet-settings__quiet-hours,
.pet-settings__range-hint {
  display: flex;
  align-items: center;
}

.pet-settings__header {
  display: block;
  width: 100%;
  min-width: 0;
}

.pet-settings h2,
.pet-settings h3,
.pet-settings p {
  margin: 0;
}

.pet-settings h2 {
  font-size: 18px;
  font-weight: 600;
  letter-spacing: 0;
}

.pet-settings__section-title p,
.pet-settings__setting-row span,
.pet-settings__hint {
  color: var(--settings-muted);
  font-size: 12px;
  line-height: 1.55;
}

.pet-settings__header-actions {
  flex: 0 0 auto;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 10px;
}

.pet-settings__save-row {
  display: flex;
  justify-content: flex-end;
  padding-top: 2px;
}

.pet-settings__save,
.pet-settings__retry {
  border: 1px solid color-mix(in srgb, var(--mac-accent, #0a84ff) 45%, var(--settings-line));
  border-radius: 8px;
  padding: 8px 12px;
  background: color-mix(in srgb, var(--mac-accent, #0a84ff) 10%, transparent);
  color: var(--mac-accent, #0a84ff);
  cursor: pointer;
  font: inherit;
  font-size: 12px;
  font-weight: 650;
}

.pet-settings__save:disabled {
  cursor: wait;
  opacity: 0.55;
}

/* 旧版 Wails 模板会给所有 button 注入固定尺寸；设置页按钮必须由自身内容和状态决定尺寸。 */
.pet-settings__save,
.pet-settings__retry,
.pet-settings__text-button {
  width: auto;
  min-width: 0;
  height: auto;
  margin: 0;
  line-height: 1.25;
  box-sizing: border-box;
  white-space: nowrap;
}

.pet-settings__content {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.pet-settings__main-content {
  display: flex;
  flex-direction: column;
  gap: 20px;
  width: 100%;
  min-width: 0;
}

.pet-settings__wide-content {
  width: 100%;
  min-width: 0;
}

.pet-settings__tabs {
  display: flex;
  width: 100%;
  min-width: 0;
  align-items: center;
  justify-content: flex-start;
  flex-wrap: wrap;
  gap: 6px;
}

/* 旧版 Wails 模板的 public/style.css 会给所有 button 注入固定宽度和左边距；设置页页签必须由内容决定宽度。 */
.pet-settings__tab {
  display: inline-flex;
  flex: 0 0 auto;
  width: auto;
  min-width: 0;
  height: 34px;
  align-items: center;
  gap: 7px;
  white-space: nowrap;
  border: 1px solid var(--settings-line);
  border-radius: 8px;
  margin: 0;
  padding: 0 11px;
  background: color-mix(in srgb, var(--settings-surface) 88%, transparent);
  color: var(--settings-ink);
  cursor: pointer;
  font: inherit;
  font-size: 12px;
  font-weight: 500;
  line-height: 20px;
  box-shadow: 0 1px 2px rgba(15, 23, 42, 0.035);
  transition: border-color 0.15s ease, background 0.15s ease, color 0.15s ease, box-shadow 0.15s ease, transform 0.15s ease;
}

.pet-settings__tab-icon {
  flex: 0 0 auto;
  color: var(--settings-muted);
}

.pet-settings__tab:not(.is-active):hover {
  border-color: var(--settings-line);
  background: var(--settings-strong-surface);
  color: var(--settings-ink);
  transform: translateY(-1px);
}

.pet-settings__tab.is-active {
  border-color: var(--mac-accent, #0a84ff);
  background: var(--mac-accent, #0a84ff);
  color: #fff;
  box-shadow: 0 5px 14px color-mix(in srgb, var(--mac-accent, #0a84ff) 20%, transparent);
}

.pet-settings__tab.is-active .pet-settings__tab-icon {
  color: currentColor;
}

.pet-settings__tab.is-active:hover {
  border-color: color-mix(in srgb, var(--mac-accent, #0a84ff) 90%, #000);
  background: color-mix(in srgb, var(--mac-accent, #0a84ff) 90%, #000);
  color: #fff;
}

.pet-settings__tab:focus-visible {
  outline: 2px solid color-mix(in srgb, var(--mac-accent, #0a84ff) 55%, transparent);
  outline-offset: 2px;
}

.pet-settings__overview-preview {
  display: flex;
  box-sizing: border-box;
  height: 198px;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  border: 1px solid color-mix(in srgb, var(--settings-line) 84%, transparent);
  border-radius: 16px;
  padding: 24px 16px;
  background: color-mix(in srgb, var(--settings-strong-surface) 32%, transparent);
}

.pet-settings__overview-placeholder {
  align-self: center;
  color: var(--settings-muted);
  font-size: 11px;
}

.pet-settings__overview-content {
  display: flex;
  flex-direction: column;
  gap: 20px;
}

.pet-settings__auto-care {
  gap: 16px;
}

.pet-settings__auto-care-control {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.pet-settings__auto-care-label {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  color: var(--settings-muted);
  font-size: 11px;
}

.pet-settings__overview-profile {
  gap: 12px;
}

.pet-settings__overview-profile-title {
  font-size: 14px;
  font-weight: 500;
}

.pet-settings__name-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.pet-settings__name-input {
  box-sizing: border-box;
  min-width: 0;
  flex: 1 1 auto;
  border: 1px solid var(--settings-line);
  border-radius: 8px;
  padding: 8px 9px;
  background: color-mix(in srgb, var(--settings-strong-surface) 74%, transparent);
  color: var(--settings-ink);
  font: inherit;
  font-size: 12px;
  outline: none;
  transition: border-color 0.18s ease, box-shadow 0.18s ease, background 0.18s ease;
}

.pet-settings__name-input:focus {
  border-color: var(--mac-accent, #0a84ff);
  background: var(--settings-surface);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--mac-accent, #0a84ff) 18%, transparent);
}

.pet-settings__overview-summary {
  color: var(--settings-muted);
  font-size: 11px;
  line-height: 1.55;
}

.pet-settings__hint {
  margin-top: -2px;
}

.pet-settings__studio {
  min-width: 0;
}

.pet-settings__stats-content {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 20px;
}

.pet-settings__stats-block-title,
.pet-settings__growth-row,
.pet-settings__stat-row,
.pet-settings__experience-entry,
.pet-settings__unlock-row {
  display: flex;
  align-items: center;
}

.pet-settings__stats-block-title h4 {
  margin: 0;
}

.pet-settings__stats-block-title > span,
.pet-settings__stats-note,
.pet-settings__stats-empty {
  color: var(--settings-muted);
  font-size: 11px;
  line-height: 1.55;
}

.pet-settings__secondary-button {
  display: inline-flex;
  width: auto;
  min-width: 0;
  height: auto;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  gap: 6px;
  border: 1px solid var(--settings-line);
  border-radius: 8px;
  margin: 0;
  padding: 7px 10px;
  background: color-mix(in srgb, var(--settings-strong-surface) 72%, transparent);
  color: var(--settings-ink);
  cursor: pointer;
  font: inherit;
  font-size: 11px;
  line-height: 1.25;
  box-sizing: border-box;
  white-space: nowrap;
}

.pet-settings__secondary-button:disabled {
  cursor: wait;
  opacity: 0.55;
}

.pet-settings__icon-button {
  display: inline-flex;
  width: 26px;
  height: 26px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  border: 1px solid transparent;
  border-radius: 7px;
  margin: 0;
  background: transparent;
  color: var(--settings-muted);
  cursor: pointer;
  font: inherit;
  font-size: 16px;
  line-height: 1;
  box-sizing: border-box;
}

.pet-settings__icon-button:hover {
  border-color: color-mix(in srgb, #bd4f4f 38%, var(--settings-line));
  background: color-mix(in srgb, #bd4f4f 10%, transparent);
  color: #bd4f4f;
}

.pet-settings__icon-button:disabled {
  cursor: wait;
  opacity: 0.45;
}

.pet-settings__stats-error {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  border: 1px solid color-mix(in srgb, #bd4f4f 40%, var(--settings-line));
  border-radius: 10px;
  padding: 10px 12px;
  color: #bd4f4f;
  font-size: 11px;
}

.pet-settings__stats-block {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 12px;
  border: 1px solid var(--settings-line);
  border-radius: 8px;
  padding: 16px;
  background: color-mix(in srgb, var(--settings-surface) 80%, transparent);
}

.pet-settings__stats-block-title {
  justify-content: space-between;
  gap: 12px;
}

.pet-settings__stats-block-actions {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.pet-settings__visually-hidden {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0 0 0 0);
  white-space: nowrap;
}

.pet-settings__stats-block-title h4 {
  font-size: 13px;
}

.pet-settings__stat-row {
  gap: 10px;
}

.pet-settings__stat-label {
  display: inline-flex;
  width: 88px;
  flex: 0 0 auto;
  align-items: center;
  gap: 6px;
  color: var(--settings-muted);
  font-size: 11px;
}

.pet-settings__stat-icon,
.pet-settings__stats-card-icon {
  display: inline-block;
  width: 12px;
  height: 12px;
  flex: 0 0 12px;
  border-radius: 50%;
  background: currentColor;
  opacity: 0.78;
}

.pet-settings__stat-icon.is-hunger,
.pet-settings__stats-card-icon.is-coins {
  color: #e3a72f;
}

.pet-settings__stat-icon.is-cleanliness,
.pet-settings__stats-card-icon.is-status {
  color: #3ba6d8;
}

.pet-settings__stat-icon.is-mood,
.pet-settings__stats-card-icon.is-days {
  color: #db7093;
}

.pet-settings__stats-card-icon.is-token {
  color: #d7833f;
}

.pet-settings__stat-track {
  height: 8px;
  min-width: 0;
  flex: 1 1 auto;
  overflow: hidden;
  border-radius: 999px;
  background: color-mix(in srgb, var(--settings-muted) 18%, transparent);
}

.pet-settings__stat-fill {
  display: block;
  height: 100%;
  border-radius: inherit;
  transition: width 0.2s ease;
}

.pet-settings__stat-fill.is-hunger {
  background: #e3a72f;
}

.pet-settings__stat-fill.is-cleanliness {
  background: #3ba6d8;
}

.pet-settings__stat-fill.is-mood {
  background: #db7093;
}

.pet-settings__stat-fill.is-growth {
  background: #8d77d9;
}

.pet-settings__stat-fill.is-low {
  background: #d95757;
}

.pet-settings__stat-value {
  width: 64px;
  flex: 0 0 auto;
  color: var(--settings-muted);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  text-align: right;
}

.pet-settings__growth-row {
  gap: 10px;
  color: var(--settings-muted);
  font-size: 11px;
}

.pet-settings__growth-row strong {
  color: #8068ca;
  font-size: 14px;
}

.pet-settings__growth-row > span:last-child {
  width: 38px;
  flex: 0 0 auto;
  text-align: right;
}

.pet-settings__stats-note {
  margin: 0;
}

.pet-settings__stats-inline-values {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

.pet-settings__stats-inline-values > div {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
  border-radius: 8px;
  padding: 9px 10px;
  background: color-mix(in srgb, var(--settings-strong-surface) 66%, transparent);
}

.pet-settings__stats-inline-values span,
.pet-settings__stats-card span {
  color: var(--settings-muted);
  font-size: 10px;
}

.pet-settings__stats-inline-values strong {
  overflow: hidden;
  color: var(--settings-ink);
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-settings__stats-empty {
  margin: 0;
  border: 1px dashed color-mix(in srgb, var(--settings-line) 90%, transparent);
  border-radius: 8px;
  padding: 10px;
}

.is-error-text {
  color: #bd4f4f !important;
}

.pet-settings__experience-log {
  display: flex;
  max-height: 260px;
  flex-direction: column;
  gap: 5px;
  overflow-y: auto;
  padding-right: 2px;
}

.pet-settings__experience-entry {
  min-width: 0;
  gap: 8px;
  border-radius: 7px;
  padding: 7px 8px;
  background: color-mix(in srgb, var(--settings-strong-surface) 65%, transparent);
  font-size: 10px;
}

.pet-settings__experience-time,
.pet-settings__experience-tokens {
  flex: 0 0 auto;
  color: var(--settings-muted);
  font-variant-numeric: tabular-nums;
}

.pet-settings__experience-model {
  min-width: 0;
  flex: 1 1 auto;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-settings__experience-badge {
  flex: 0 0 auto;
  border-radius: 999px;
  padding: 2px 6px;
  background: color-mix(in srgb, var(--settings-muted) 12%, transparent);
  color: var(--settings-muted);
}

.pet-settings__experience-badge.is-premium {
  background: color-mix(in srgb, #8068ca 16%, transparent);
  color: #8068ca;
}

.pet-settings__experience-entry strong {
  flex: 0 0 auto;
  color: #328c5d;
  font-variant-numeric: tabular-nums;
}

.pet-settings__stats-summary {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

.pet-settings__stats-card {
  display: flex;
  min-width: 0;
  min-height: 60px;
  flex-direction: row;
  align-items: center;
  justify-content: flex-start;
  gap: 10px;
  border: 1px solid var(--settings-line);
  border-radius: 8px;
  padding: 10px 12px;
  background: color-mix(in srgb, var(--settings-surface) 80%, transparent);
}

.pet-settings__stats-card-copy {
  display: flex;
  min-width: 0;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 3px;
  overflow: hidden;
}

.pet-settings__stats-card-copy > span {
  overflow: hidden;
  color: var(--settings-muted);
  font-size: 10px;
  line-height: 1.35;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-settings__stats-card-copy strong {
  overflow: hidden;
  display: block;
  color: var(--settings-ink);
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-settings__unlock-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.pet-settings__unlock-row {
  justify-content: space-between;
  gap: 12px;
  color: var(--settings-ink);
  font-size: 11px;
}

.pet-settings__unlock-row .is-unlocked {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: #328c5d;
}

.pet-settings__unlock-row .is-locked {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: var(--settings-muted);
}

.pet-settings__unlock-icon {
  position: relative;
  display: inline-block;
  width: 13px;
  height: 13px;
  flex: 0 0 13px;
}

.pet-settings__unlock-icon.is-check::before {
  position: absolute;
  top: 1px;
  left: 3px;
  width: 5px;
  height: 9px;
  border-right: 2px solid currentColor;
  border-bottom: 2px solid currentColor;
  content: '';
  transform: rotate(45deg);
}

.pet-settings__unlock-icon.is-lock {
  height: 9px;
  margin-top: 4px;
  border: 2px solid currentColor;
  border-radius: 3px;
  box-sizing: border-box;
}

.pet-settings__unlock-icon.is-lock::before {
  position: absolute;
  top: -7px;
  left: 2px;
  width: 5px;
  height: 7px;
  border: 2px solid currentColor;
  border-bottom: 0;
  border-radius: 5px 5px 0 0;
  content: '';
}

.pet-settings__skin-list {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 220px), 1fr));
  align-items: stretch;
  gap: 12px;
}

.pet-settings__skin-card {
  position: relative;
  display: flex;
  min-width: 0;
  flex-direction: column;
  overflow: hidden;
  border: 1px solid color-mix(in srgb, var(--settings-line) 88%, transparent);
  border-radius: 10px;
  background: color-mix(in srgb, var(--settings-surface) 82%, transparent);
  transition: border-color 0.16s ease, background 0.16s ease, box-shadow 0.16s ease;
}

.pet-settings__skin-card:hover {
  border-color: color-mix(in srgb, var(--mac-accent, #0a84ff) 30%, var(--settings-line));
  background: color-mix(in srgb, var(--settings-surface) 94%, var(--settings-strong-surface));
}

.pet-settings__skin-card.is-active {
  border-color: color-mix(in srgb, var(--mac-accent, #0a84ff) 65%, var(--settings-line));
  box-shadow: 0 0 0 2px color-mix(in srgb, var(--mac-accent, #0a84ff) 12%, transparent);
}

.pet-settings__skin-thumb {
  display: flex;
  position: relative;
  width: 100%;
  height: 136px;
  flex: 0 0 136px;
  align-items: flex-end;
  justify-content: center;
  box-sizing: border-box;
  overflow: hidden;
  padding: 10px;
  background: color-mix(in srgb, var(--settings-strong-surface) 78%, transparent);
  color: var(--settings-muted);
  font-size: 18px;
}

.pet-settings__skin-preview-placeholder {
  display: inline-flex;
  min-height: 96px;
  align-items: flex-end;
  justify-content: center;
}

.pet-settings__skin-copy {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
  padding: 12px 12px 0;
}

.pet-settings__skin-copy strong,
.pet-settings__skin-copy span {
  min-width: 0;
  overflow-wrap: anywhere;
  word-break: break-word;
}

.pet-settings__skin-copy strong {
  display: block;
  min-height: 32px;
  color: var(--settings-ink);
  line-height: 1.35;
  font-size: 12px;
  font-weight: 600;
}

.pet-settings__skin-copy span {
  display: -webkit-box;
  min-height: 29px;
  color: var(--settings-muted);
  font-size: 10px;
  line-height: 1.45;
  overflow: hidden;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.pet-settings__skin-status {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  flex: 0 0 auto;
  border-radius: 999px;
  padding: 4px 7px;
  background: color-mix(in srgb, #328c5d 12%, transparent);
  color: var(--settings-muted);
  font-size: 10px;
  font-weight: 600;
  white-space: nowrap;
}

.pet-settings__skin-status::before {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: currentColor;
  content: '';
}

.pet-settings__skin-status.is-active {
  color: #328c5d;
}

.pet-settings__skin-card-footer {
  display: flex;
  min-height: 48px;
  align-items: center;
  gap: 8px;
  margin-top: auto;
  padding: 10px 12px 12px;
}

.pet-settings__skin-card-footer .pet-settings__secondary-button {
  padding: 6px 10px;
}

.pet-settings__skin-content {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 14px;
}

.pet-settings__skin-toolbar {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 12px;
  border: 1px solid var(--settings-line);
  border-radius: 10px;
  padding: 14px;
  background: color-mix(in srgb, var(--settings-surface) 80%, transparent);
}

.pet-settings__skin-toolbar-top {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: 16px;
}

.pet-settings__skin-heading {
  min-width: 0;
}

.pet-settings__skin-heading h3 {
  margin: 0;
  font-size: 14px;
  font-weight: 650;
}

.pet-settings__skin-heading p {
  margin: 3px 0 0;
  color: var(--settings-muted);
  font-size: 11px;
  line-height: 1.5;
}

.pet-settings__skin-window-control {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.pet-settings__skin-toolbar-icon {
  flex: 0 0 auto;
  color: var(--mac-accent, #0a84ff);
}

.pet-settings__skin-window-copy {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 3px;
}

.pet-settings__skin-window-copy strong {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 12px;
}

.pet-settings__skin-window-copy span {
  overflow: hidden;
  color: var(--settings-muted);
  font-size: 10px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-settings__skin-directory-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: center;
  gap: 12px;
  padding-top: 12px;
  border-top: 1px solid color-mix(in srgb, var(--settings-line) 72%, transparent);
}

.pet-settings__skin-directory-row .pet-settings__managed-directory {
  min-width: 0;
  margin-top: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-settings__skin-actions {
  display: flex;
  flex: 0 0 auto;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 8px;
}

.pet-settings__skin-actions .pet-settings__secondary-button {
  min-height: 30px;
}

.pet-settings__button-icon {
  flex: 0 0 auto;
}

.pet-settings__button-icon.is-spinning {
  animation: pet-settings-spin 0.9s linear infinite;
}

.pet-settings__skin-toolbar .pet-settings__drop-hint {
  margin: 0;
  border: 0;
  border-top: 1px solid color-mix(in srgb, var(--settings-line) 72%, transparent);
  border-radius: 0;
  padding: 10px 0 0;
  font-size: 10px;
}

.pet-settings__skin-delete {
  position: absolute;
  top: 8px;
  right: 8px;
  z-index: 1;
  background: color-mix(in srgb, var(--settings-surface) 78%, transparent);
}

.pet-settings__skin-empty-hint {
  margin: 0;
  color: var(--settings-muted);
  font-size: 11px;
  line-height: 1.5;
}

@keyframes pet-settings-spin {
  to {
    transform: rotate(360deg);
  }
}

.pet-settings__dream-ranges {
  padding-top: 2px;
}

.pet-settings__section {
  display: flex;
  flex-direction: column;
  gap: 12px;
  border: 1px solid var(--settings-line);
  border-radius: 8px;
  padding: 16px;
  background: color-mix(in srgb, var(--settings-surface) 80%, transparent);
}

.pet-settings__stats-card-icon {
  width: 14px;
  height: 14px;
  flex-basis: 14px;
  margin-bottom: 1px;
}

.pet-settings__section-title {
  display: flex;
  justify-content: space-between;
  gap: 14px;
  padding-bottom: 2px;
}

.pet-settings__section-title h3 {
  font-size: 14px;
}

.pet-settings__section-title p {
  margin-top: 3px;
}

.pet-settings__tab-description {
  color: var(--settings-muted);
  font-size: 12px;
  line-height: 1.55;
}

.pet-settings__field-heading,
.pet-settings__field-actions,
.pet-settings__managed-directory,
.pet-settings__managed-value,
.pet-settings__drop-hint {
  display: flex;
  align-items: center;
}

.pet-settings__field-heading {
  justify-content: space-between;
  gap: 12px;
  color: var(--settings-muted);
  font-size: 11px;
}

.pet-settings__field-actions {
  justify-content: flex-end;
  gap: 8px;
  margin-top: -5px;
}

.pet-settings__text-button {
  border: 0;
  padding: 2px 0;
  background: transparent;
  color: var(--mac-accent, #0a84ff);
  cursor: pointer;
  font: inherit;
  font-size: 11px;
}

.pet-settings__text-button:hover {
  text-decoration: underline;
}

.pet-settings__managed-directory,
.pet-settings__managed-value,
.pet-settings__drop-hint {
  color: var(--settings-muted);
  font-size: 11px;
  line-height: 1.5;
}

.pet-settings__managed-directory {
  margin-top: -3px;
  overflow-wrap: anywhere;
  word-break: break-word;
}

.pet-settings__drop-hint {
  border: 1px dashed color-mix(in srgb, var(--settings-line) 92%, transparent);
  border-radius: 8px;
  padding: 9px 10px;
}

.pet-settings__setting-row {
  justify-content: space-between;
  gap: 16px;
  min-width: 0;
  padding-top: 2px;
}

.pet-settings__setting-row + .pet-settings__setting-row,
.pet-settings__setting-row.is-stacked {
  border-top: 1px solid color-mix(in srgb, var(--settings-line) 72%, transparent);
  padding-top: 14px;
}

.pet-settings__setting-row > div:first-child {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 3px;
}

.pet-settings__setting-row strong {
  font-size: 12px;
}

.pet-settings__setting-copy {
  flex: 1 1 260px;
}

.pet-settings__inline-controls {
  flex: 0 0 auto;
  gap: 10px;
}

.pet-settings__inline-controls output,
.pet-settings__field output {
  color: var(--settings-muted);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}

.pet-settings__switch {
  position: relative;
  display: inline-flex;
  width: 40px;
  height: 22px;
  flex: 0 0 auto;
  cursor: pointer;
}

.pet-settings__switch input {
  position: absolute;
  width: 1px;
  height: 1px;
  opacity: 0;
}

.pet-settings__switch span {
  position: relative;
  display: block;
  width: 100%;
  height: 100%;
  border-radius: 999px;
  background: color-mix(in srgb, var(--settings-muted) 30%, transparent);
  transition: background 0.18s ease;
}

.pet-settings__switch span::after {
  position: absolute;
  top: 3px;
  left: 3px;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: #fff;
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.2);
  content: '';
  transition: transform 0.18s ease;
}

.pet-settings__switch input:checked + span {
  background: var(--mac-accent, #0a84ff);
}

.pet-settings__switch input:checked + span::after {
  transform: translateX(18px);
}

.pet-settings__range {
  width: 100%;
  min-width: 0;
  accent-color: var(--mac-accent, #0a84ff);
  cursor: pointer;
}

.pet-settings__range:disabled {
  cursor: not-allowed;
  opacity: 0.45;
}

.pet-settings__range-hint {
  justify-content: space-between;
  color: var(--settings-muted);
  font-size: 10px;
  opacity: 0.72;
}

.pet-settings__field-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

.pet-settings__field,
.pet-settings__number-field {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 6px;
  color: var(--settings-muted);
  font-size: 11px;
}

.pet-settings__field--wide {
  grid-column: span 2;
}

.pet-settings__field-error {
  margin: -4px 0 0;
  color: #bd4f4f;
  font-size: 11px;
  line-height: 1.5;
}

.pet-settings__field input,
.pet-settings__field textarea,
.pet-settings__field select,
.pet-settings__number-field input,
.pet-settings__inline-controls select {
  box-sizing: border-box;
  width: 100%;
  min-width: 0;
  border: 1px solid var(--settings-line);
  border-radius: 8px;
  padding: 8px 9px;
  background: color-mix(in srgb, var(--settings-strong-surface) 74%, transparent);
  color: var(--settings-ink);
  font: inherit;
  font-size: 12px;
  outline: none;
  transition: border-color 0.18s ease, box-shadow 0.18s ease, background 0.18s ease;
}

.pet-settings__field textarea {
  min-height: 70px;
  resize: vertical;
  line-height: 1.5;
}

.pet-settings__field input:focus,
.pet-settings__field textarea:focus,
.pet-settings__field select:focus,
.pet-settings__number-field input:focus,
.pet-settings__inline-controls select:focus {
  border-color: var(--mac-accent, #0a84ff);
  background: var(--settings-surface);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--mac-accent, #0a84ff) 18%, transparent);
}

.pet-settings__quiet-hours {
  flex: 1 1 280px;
  flex-wrap: wrap;
  gap: 8px;
}

.pet-settings__number-field {
  flex: 1 1 120px;
  position: relative;
}

.pet-settings__number-field input {
  padding-right: 27px;
}

.pet-settings__number-field em {
  position: absolute;
  right: 9px;
  bottom: 8px;
  color: var(--settings-muted);
  font-size: 11px;
  font-style: normal;
}

.pet-settings__quiet-separator {
  align-self: flex-end;
  padding-bottom: 9px;
  color: var(--settings-muted);
  font-size: 11px;
}

.pet-settings__state {
  display: flex;
  min-height: 100px;
  align-items: center;
  justify-content: center;
  gap: 10px;
  border: 1px dashed var(--settings-line);
  border-radius: 12px;
  padding: 16px;
  color: var(--settings-muted);
  font-size: 12px;
  text-align: center;
}

.pet-settings__state.is-error {
  color: #bd4f4f;
}

.pet-settings__retry {
  border-color: color-mix(in srgb, #bd4f4f 45%, var(--settings-line));
  background: color-mix(in srgb, #bd4f4f 10%, transparent);
  color: #bd4f4f;
}

.pet-settings__hint {
  margin-top: -4px;
}

@media (max-width: 1100px) {
  .pet-settings__tabs {
    justify-content: flex-start;
  }
}

@media (max-width: 760px) {
  .pet-settings__skin-toolbar-top {
    grid-template-columns: minmax(0, 1fr);
    align-items: flex-start;
  }

  .pet-settings__skin-window-control {
    align-self: stretch;
  }
}

@media (max-width: 640px) {
  .pet-settings {
    padding: 16px 12px;
  }

  .pet-settings__tabs {
    width: 100%;
    justify-content: flex-start;
    flex-wrap: nowrap;
    overflow-x: auto;
    padding: 2px 2px 5px;
    scrollbar-width: thin;
  }

  .pet-settings__header-actions {
    width: 100%;
    justify-content: space-between;
  }

  .pet-settings__setting-row {
    align-items: flex-start;
    flex-wrap: wrap;
  }

  .pet-settings__setting-row > div:first-child {
    flex: 1 1 210px;
  }

  .pet-settings__field-grid {
    grid-template-columns: minmax(0, 1fr);
  }

  .pet-settings__field--wide {
    grid-column: auto;
  }

  .pet-settings__name-row {
    align-items: stretch;
    flex-direction: column;
  }

  .pet-settings__stats-summary {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .pet-settings__skin-directory-row {
    align-items: flex-start;
    grid-template-columns: minmax(0, 1fr);
  }

  .pet-settings__skin-actions {
    width: 100%;
    justify-content: stretch;
  }

  .pet-settings__skin-actions .pet-settings__secondary-button {
    flex: 1 1 auto;
  }

  .pet-settings__experience-time {
    display: none;
  }

  .pet-settings__quiet-hours {
    flex-basis: 100%;
  }
}

@media (min-width: 641px) {
  .pet-settings__stats-summary {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }
}
</style>
