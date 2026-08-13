<script setup lang="ts">
import { Call } from '../../wails-runtime-compat'
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import type { GeminiProvider, Provider } from '../../../bindings/codeswitch/services/models'
import { fetchProjectManagerSnapshot, refreshProjectManagerSnapshot, type ProjectSummary } from '../../services/projectManager'
import { petApi } from './petApi'
import PetAtlasFrame from './PetAtlasFrame.vue'
import PetDreamHistoryPanel from './PetDreamHistoryPanel.vue'
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

type PetTab = 'overview' | 'stats' | 'agent' | 'sleep' | 'skins' | 'memory' | 'dream-history' | 'studio'

const PET_TABS: ReadonlyArray<{ id: PetTab }> = [
  { id: 'overview' },
  { id: 'stats' },
  { id: 'studio' },
  { id: 'skins' },
  { id: 'memory' },
  { id: 'agent' },
  { id: 'sleep' },
  { id: 'dream-history' }
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
}

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
      bubbleMinDurationSeconds: 12
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
const snapshot = ref<PetSnapshot | null>(null)
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

const providerPlatformOptions = computed(() => {
  const current = form.agent.providerPlatform?.trim() ?? ''
  const standard = ['claude', 'codex', 'gemini']
  return current && !standard.includes(current) ? [current, ...standard] : standard
})

function providerModelOptions(platform: string, providers: Provider[]): PetProviderModelOption[] {
  return providers.flatMap((provider) => {
    const providerId = String(provider.id)
    if (!provider.enabled || !providerId.trim()) return []
    const models = Object.entries(provider.supportedModels ?? {})
      .filter(([modelId, enabled]) => Boolean(modelId.trim()) && enabled !== false)
    return models.map(([modelId]) => ({
      platform,
      providerId,
      providerName: provider.name.trim() || providerId,
      modelId,
      modelCategory: provider.modelCategories?.[modelId]?.trim() ?? ''
    }))
  })
}

function modelCategory(option: PetProviderModelOption): string {
  return option.modelCategory.trim().toLowerCase() || 'chat'
}

function geminiModelOptions(providers: GeminiProvider[]): PetProviderModelOption[] {
  return providers.flatMap((provider) => {
    const modelId = provider.model?.trim() ?? ''
    if (!provider.enabled || !provider.id.trim() || !modelId) return []
    return [{
      platform: 'gemini',
      providerId: provider.id.trim(),
      providerName: provider.name.trim() || provider.id.trim(),
      modelId,
      modelCategory: provider.modelCategory?.trim() ?? ''
    }]
  })
}

function skinPoseCount(skin: PetSkinRecord): number {
  const manifest = isRecord(skin.manifestJson) ? skin.manifestJson : {}
  return isRecord(manifest.animations) ? Object.keys(manifest.animations).length : 0
}

async function loadProviderOptions(platform: string | null = form.agent.providerPlatform): Promise<void> {
  const normalized = platform?.trim().toLowerCase() ?? ''
  const generation = ++providerRequestGeneration
  providerLoading.value = true
  providerError.value = ''
  try {
    if (!normalized) {
      providerOptions.value = []
      return
    }
    const options = normalized === 'gemini'
      ? geminiModelOptions(await Call.ByName('codeswitch/services.GeminiService.GetProviders') as GeminiProvider[])
      : providerModelOptions(
          normalized,
          await Call.ByName('codeswitch/services.ProviderService.LoadProviders', normalized) as Provider[]
        )
    if (generation !== providerRequestGeneration) return
    providerOptions.value = options
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

async function loadSkinPreviews(records = skins.value): Promise<void> {
  const generation = ++skinPreviewGeneration
  const next: Record<string, PetAtlasAsset> = {}
  skinPreviewLoading.value = {}
  try {
    const result = await readPetStudioAtlas(props.petId, 'default')
    if (generation !== skinPreviewGeneration) return
    defaultAtlas.value = result.atlas
  } catch {
    if (generation !== skinPreviewGeneration) return
    defaultAtlas.value = null
  }
  if (snapshot.value?.atlas && snapshot.value.skinSelection.activeSkinId) {
    next[snapshot.value.skinSelection.activeSkinId] = snapshot.value.atlas
  }
  skinPreviews.value = next
  for (const skin of records.filter((item) => item.skinId !== 'capybara')) {
    if (generation !== skinPreviewGeneration) return
    if (next[skin.skinId]) continue
    skinPreviewLoading.value = { ...skinPreviewLoading.value, [skin.skinId]: true }
    try {
      const result = await readPetStudioAtlas(props.petId, { skinId: skin.skinId })
      if (generation !== skinPreviewGeneration) return
      skinPreviews.value = { ...skinPreviews.value, [skin.skinId]: result.atlas }
    } catch {
      // 单个皮肤资源损坏时保留列表和绑定能力，缩略图退回占位，不阻断整个设置页。
    } finally {
      if (generation === skinPreviewGeneration) {
        const state = { ...skinPreviewLoading.value }
        delete state[skin.skinId]
        skinPreviewLoading.value = state
      }
    }
  }
}

async function loadSkinRoot(): Promise<void> {
  try {
    skinRoot.value = await getPetStudioRoot()
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
    const next = await petApi.getSnapshot(props.petId)
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
    const next = await petApi.getSnapshot(props.petId)
    if (generation !== statsRequestGeneration) return
    snapshot.value = next
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

function toForm(snapshot: PetSnapshot): PetSettingsForm {
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

function normalizeAgentConfig(agent: PetAgentConfig, petId: string): PetAgentConfig {
  return {
    ...agent,
    petId,
    providerPlatform: normalizeNullable(agent.providerPlatform),
    providerId: normalizeNullable(agent.providerId),
    modelId: normalizeNullable(agent.modelId),
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
      )
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
    const next = await petApi.getSnapshot(props.petId)
    snapshot.value = next
    statsNow.value = Date.now()
    skins.value = next.skins
    nameDraft.value = next.state.name
    // 外部 v-model 是受控值；没有受控值时才用后端快照初始化表单。
    if (!props.modelValue) assignForm(toForm(next))
    await Promise.all([
      loadProviderOptions(next.agent.providerPlatform),
      loadProjectOptions(),
      loadSkinPreviews(next.skins),
      loadSkinRoot()
    ])
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

function consumeStudioQuery(): void {
  if (route.query.studio !== '1') return

  // 设置页被 keep-alive 缓存时不会重新触发 onMounted；把 query 当作入口意图监听，
  // 才能保证桌宠菜单从已打开的设置页再次进入 Studio 仍然生效。
  selectTab('studio')
  const query = { ...route.query }
  delete query.studio
  void router.replace({ path: route.path, query })
}

watch(() => route.query.studio, consumeStudioQuery)

onMounted(() => {
  consumeStudioQuery()
  void loadSettings()
})

const agentPlatformModel = computed({
  get: () => form.agent.providerPlatform ?? '',
  set: (value: string) => {
    const platform = value.trim() || null
    if (platform === form.agent.providerPlatform) return
    form.agent.providerPlatform = platform
    form.agent.providerId = null
    form.agent.modelId = null
    void loadProviderOptions(platform)
  }
})

const agentProviderModel = computed({
  // 原生 select 需要一个真正的空值；不能让 JSON 空对象成为 sentinel，
  // 否则没有模型引用时下拉框不会命中任何 option，界面会像丢失配置。
  get: () => form.agent.providerId && form.agent.modelId
    ? JSON.stringify({ providerId: form.agent.providerId, modelId: form.agent.modelId })
    : '',
  set: (value: string) => {
    if (!value) {
      form.agent.providerId = null
      form.agent.modelId = null
      return
    }
    try {
      const selected = JSON.parse(value) as { providerId?: unknown; modelId?: unknown }
      form.agent.providerId = typeof selected.providerId === 'string' && selected.providerId ? selected.providerId : null
      form.agent.modelId = typeof selected.modelId === 'string' && selected.modelId ? selected.modelId : null
    } catch {
      form.agent.providerId = null
      form.agent.modelId = null
    }
  }
})

const visibleProviderOptions = computed(() => {
  const currentProviderId = form.agent.providerId ?? ''
  const currentModelId = form.agent.modelId ?? ''
  const chatOptions = providerOptions.value.filter((item) => modelCategory(item) === 'chat')
  if (!currentProviderId || !currentModelId || !form.agent.providerPlatform) return chatOptions
  const currentKey = JSON.stringify({ providerId: currentProviderId, modelId: currentModelId })
  if (chatOptions.some((item) => JSON.stringify({ providerId: item.providerId, modelId: item.modelId }) === currentKey)) {
    return chatOptions
  }
  return [
    {
      platform: form.agent.providerPlatform,
      providerId: currentProviderId,
      providerName: currentProviderId,
      modelId: currentModelId,
      modelCategory: ''
    },
    ...chatOptions
  ]
})

const visibleVoiceOptions = computed(() => {
  const currentProviderId = form.agent.voiceProviderId ?? ''
  const currentModelId = form.agent.voiceModelId ?? ''
  const options = providerOptions.value.filter((item) => {
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
    modelCategory: 'speech'
  }, ...options]
})

const voiceProviderModel = computed({
  get: () => form.agent.voiceProviderId && form.agent.voiceModelId
    ? JSON.stringify({ providerId: form.agent.voiceProviderId, modelId: form.agent.voiceModelId })
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
  const platform = form.agent.providerPlatform?.trim() ?? ''
  const providerId = form.agent.voiceProviderId?.trim() ?? ''
  const model = form.agent.voiceModelId?.trim() ?? ''
  if (!platform || !providerId || !model || voiceTesting.value) return
  voiceTesting.value = true
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
    errorMessage.value = error instanceof Error ? error.message : String(error)
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

onUnmounted(() => {
  stopStatsRefreshTimer()
  statsRequestGeneration += 1
  voicePreviewPlayer.dispose()
})
</script>

<template>
  <div class="pet-settings">
    <header class="pet-settings__header">
      <div class="pet-settings__heading">
        <h2>{{ t('pet.settings.title') }}</h2>
        <p>{{ t('pet.settings.subtitle') }}</p>
      </div>
    </header>

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
        {{ t(`pet.settings.tabs.${tab.id}`) }}
      </button>
    </nav>

    <div v-if="loading" class="pet-settings__state">{{ t('pet.settings.loading') }}</div>
    <div v-else-if="errorMessage" class="pet-settings__state is-error">
      <span>{{ t('pet.settings.operationFailed', { error: errorMessage }) }}</span>
      <button type="button" class="pet-settings__retry" @click="loadSettings">{{ t('pet.common.retry') }}</button>
    </div>

    <div v-else class="pet-settings__content">
      <div v-show="activeTab === 'studio'" class="pet-settings__wide-content pet-settings__studio">
        <PetStudio :pet-id="props.petId" />
      </div>

      <div class="pet-settings__narrow-content">
        <section v-show="activeTab === 'overview'" class="pet-settings__section">
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

          <div class="pet-settings__setting-row is-stacked">
            <div class="pet-settings__setting-copy">
              <strong>{{ t('pet.settings.overview.autoCare') }}</strong>
              <span>{{ t('pet.settings.overview.autoCareHint') }}</span>
            </div>
            <div class="pet-settings__inline-controls">
              <label class="pet-settings__switch">
                <input v-model="form.care.autoCareEnabled" type="checkbox" @change="void saveSettings()" />
                <span aria-hidden="true"></span>
              </label>
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
            <div class="pet-settings__range-hint"><span>{{ t('pet.settings.overview.insensitive') }}</span><span>{{ t('pet.settings.overview.attentive') }}</span></div>
          </div>

          <section class="pet-settings__overview-profile">
            <div class="pet-settings__section-title">
              <div>
                <h3>{{ t('pet.settings.overview.basicTitle') }}</h3>
                <p>{{ t('pet.settings.overview.basicSubtitle') }}</p>
              </div>
            </div>
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
        </section>

        <section v-show="activeTab === 'agent'" class="pet-settings__section">
          <div class="pet-settings__section-title">
            <div>
              <h3>{{ t('pet.settings.agent.title') }}</h3>
              <p>{{ t('pet.settings.agent.subtitle') }}</p>
            </div>
          </div>

          <div class="pet-settings__field-grid">
            <label class="pet-settings__field">
              <span>{{ t('pet.settings.agent.providerPlatform') }}</span>
              <select v-model="agentPlatformModel">
                <option value="">{{ t('pet.settings.agent.noPlatform') }}</option>
                <option v-for="platform in providerPlatformOptions" :key="platform" :value="platform">{{ platform }}</option>
              </select>
            </label>
            <label class="pet-settings__field pet-settings__field--wide">
              <span>{{ t('pet.settings.agent.modelReference') }}</span>
              <select v-model="agentProviderModel" :disabled="providerLoading || !form.agent.providerPlatform">
                <option value="">{{ providerLoading ? t('pet.settings.agent.loadingModels') : t('pet.settings.agent.modelPlaceholder') }}</option>
                <option
                  v-for="option in visibleProviderOptions"
                  :key="`${option.platform}:${option.providerId}:${option.modelId}`"
                  :value="JSON.stringify({ providerId: option.providerId, modelId: option.modelId })"
                >
                  {{ option.providerName }} · {{ option.modelId }}
                </option>
              </select>
            </label>
            <label class="pet-settings__field">
              <span>{{ t('pet.settings.agent.reasoningEffort') }}</span>
              <select v-model="reasoningEffortModel">
                <option value="">{{ t('pet.settings.agent.followModel') }}</option>
                <option value="none">{{ t('pet.settings.reasoning.none') }}</option>
                <option value="minimal">{{ t('pet.settings.reasoning.minimal') }}</option>
                <option value="low">{{ t('pet.settings.reasoning.low') }}</option>
                <option value="medium">{{ t('pet.settings.reasoning.medium') }}</option>
                <option value="high">{{ t('pet.settings.reasoning.high') }}</option>
              </select>
            </label>
          </div>

          <p v-if="providerError" class="pet-settings__field-error">{{ t('pet.settings.agent.providerLoadFailed', { error: providerError }) }}</p>
          <p v-else-if="form.agent.providerPlatform && !providerLoading && visibleProviderOptions.length === 0" class="pet-settings__hint">
            {{ t('pet.settings.agent.noModels') }}
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
            <textarea v-model="form.agent.systemPrompt" rows="10" :placeholder="t('pet.settings.agent.systemPromptPlaceholder')"></textarea>
          </label>
          <p class="pet-settings__hint">{{ t('pet.settings.agent.promptHint') }}</p>
        </section>

        <section v-show="activeTab === 'agent'" class="pet-settings__section">
          <div class="pet-settings__section-title">
            <div>
              <h3>{{ t('pet.settings.agent.project') }}</h3>
              <p>{{ t('pet.settings.agent.projectHint') }}</p>
            </div>
          </div>
          <label class="pet-settings__field">
            <span>{{ t('pet.settings.agent.project') }}</span>
            <select v-model="agentProjectModel" :disabled="projectLoading">
              <option value="">{{ projectLoading ? t('pet.settings.agent.loadingProjects') : t('pet.settings.agent.projectNone') }}</option>
              <option v-for="project in visibleProjectOptions" :key="project.id" :value="project.id">
                {{ project.display_name }}
              </option>
            </select>
          </label>
          <p v-if="projectError" class="pet-settings__field-error">{{ t('pet.settings.agent.projectLoadFailed', { error: projectError }) }}</p>
        </section>

        <section v-show="activeTab === 'agent'" class="pet-settings__section">
          <div class="pet-settings__section-title">
            <div>
              <h3>{{ t('pet.settings.agent.proactive') }}</h3>
              <p>{{ t('pet.settings.agent.proactiveHint') }}</p>
            </div>
          </div>
          <div class="pet-settings__setting-row">
            <div class="pet-settings__setting-copy">
              <strong>{{ t('pet.settings.agent.proactive') }}</strong>
              <span>{{ t('pet.settings.agent.proactiveHint') }}</span>
            </div>
            <label class="pet-settings__switch">
              <input v-model="form.agent.proactive" type="checkbox" />
              <span aria-hidden="true"></span>
            </label>
          </div>
          <div class="pet-settings__field-grid">
            <label class="pet-settings__field">
              <span>{{ t('pet.settings.agent.proactiveFrequency') }}</span>
              <select v-model="form.agent.proactiveFreq">
                <option value="low">{{ t('pet.settings.frequency.low') }}</option>
                <option value="medium">{{ t('pet.settings.frequency.medium') }}</option>
                <option value="high">{{ t('pet.settings.frequency.high') }}</option>
              </select>
            </label>
            <label class="pet-settings__number-field">
              <span>{{ t('pet.settings.agent.quietStart') }}</span>
              <input v-model.number="form.agent.quietStart" type="number" min="0" max="23" />
              <em>{{ t('pet.settings.agent.hour') }}</em>
            </label>
            <label class="pet-settings__number-field">
              <span>{{ t('pet.settings.agent.quietEnd') }}</span>
              <input v-model.number="form.agent.quietEnd" type="number" min="0" max="23" />
              <em>{{ t('pet.settings.agent.hour') }}</em>
            </label>
          </div>
        </section>

        <section v-show="activeTab === 'agent'" class="pet-settings__section">
        <div class="pet-settings__section-title">
          <div>
            <h3>{{ t('pet.settings.voice.title') }}</h3>
            <p>{{ t('pet.settings.voice.subtitle') }}</p>
          </div>
        </div>

        <div class="pet-settings__setting-row">
          <div>
            <strong>{{ t('pet.settings.voice.enabled') }}</strong>
            <span>{{ t('pet.settings.voice.enabledHint') }}</span>
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
              <option
                v-for="option in visibleVoiceOptions"
                :key="`voice:${option.platform}:${option.providerId}:${option.modelId}`"
                :value="JSON.stringify({ providerId: option.providerId, modelId: option.modelId })"
              >
                {{ option.providerName }} · {{ option.modelId }}
              </option>
            </select>
          </label>
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
              <input v-model="form.agent.voice" type="text" :placeholder="t('pet.settings.voice.voicePlaceholder')" />
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
            <input v-model="form.agent.voiceTag" type="text" :placeholder="t('pet.settings.voice.tagPlaceholder')" />
          </label>
          <label class="pet-settings__field">
            <span>{{ t('pet.settings.voice.instruction') }}</span>
            <input v-model="form.agent.voiceInstruction" type="text" :placeholder="t('pet.settings.voice.instructionPlaceholder')" />
          </label>
          <p class="pet-settings__hint">{{ t('pet.settings.voice.hint') }}</p>
        </template>
        </section>

        <div v-show="activeTab === 'agent'" class="pet-settings__save-row">
          <button type="button" class="pet-settings__save" :disabled="loading || saving" @click="saveSettings">
            {{ saving ? t('pet.common.saving') : t('pet.settings.save') }}
          </button>
        </div>

        <section v-show="activeTab === 'sleep'" class="pet-settings__section">
          <div class="pet-settings__section-title">
            <div>
              <h3>{{ t('pet.settings.dream.title') }}</h3>
              <p>{{ t('pet.settings.dream.subtitle') }}</p>
            </div>
          </div>

          <div class="pet-settings__setting-row">
            <div>
              <strong>{{ t('pet.settings.dream.enabled') }}</strong>
              <span>{{ t('pet.settings.dream.enabledHint') }}</span>
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
            <textarea v-model="form.dream.prompt" rows="9" :placeholder="t('pet.settings.dream.promptPlaceholder')"></textarea>
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
          <span class="pet-settings__managed-value">{{ t('pet.settings.dream.imageModelManaged') }}</span>
        </section>

        <div v-show="activeTab === 'sleep'" class="pet-settings__save-row">
          <button type="button" class="pet-settings__save" :disabled="loading || saving" @click="saveSettings">
            {{ saving ? t('pet.common.saving') : t('pet.settings.save') }}
          </button>
        </div>

        <div v-show="activeTab === 'skins'" class="pet-settings__skin-content">
          <section class="pet-settings__skin-window">
            <div class="pet-settings__skin-window-copy">
              <span class="pet-settings__monitor-icon" aria-hidden="true"></span>
              <div>
                <strong>{{ t('pet.settings.overview.windowEnabled') }}</strong>
                <span>{{ t(form.window.enabled ? 'pet.settings.skins.displayOn' : 'pet.settings.skins.displayOff') }}</span>
              </div>
            </div>
            <label class="pet-settings__switch">
              <input v-model="form.window.enabled" type="checkbox" @change="void saveSettings()" />
              <span aria-hidden="true"></span>
            </label>
          </section>

          <div class="pet-settings__section-title">
            <div>
              <h3>{{ t('pet.settings.skins.title') }}</h3>
              <p>{{ t('pet.settings.skins.subtitle') }}</p>
            </div>
          </div>

          <div class="pet-settings__skin-directory-row">
            <span class="pet-settings__managed-directory">{{ skinRoot || t('pet.settings.skins.directoryUnavailable') }}</span>
            <div class="pet-settings__field-actions">
              <button type="button" class="pet-settings__secondary-button" :disabled="skinRootLoading" @click="openSkinRoot">
                {{ t('pet.settings.skins.openFolder') }}
              </button>
              <button type="button" class="pet-settings__secondary-button" :disabled="skinRefreshing" @click="refreshSkins">
                {{ skinRefreshing ? t('pet.common.refreshing') : t('pet.settings.skins.refresh') }}
              </button>
            </div>
          </div>
          <p class="pet-settings__drop-hint">{{ t('pet.settings.skins.dropHint') }}</p>

          <div class="pet-settings__skin-list">
            <div class="pet-settings__skin-row">
              <div class="pet-settings__skin-thumb">
                <PetAtlasFrame
                  v-if="defaultAtlas"
                  :image-url="defaultAtlas.src"
                  :manifest="defaultAtlas.manifest"
                  action="idle"
                  :display-height="46"
                />
                <span v-else aria-hidden="true">·</span>
              </div>
              <div class="pet-settings__skin-copy">
                <strong>{{ t('pet.settings.skins.default') }}</strong>
                <span>{{ t('pet.settings.skins.builtinHint') }}</span>
              </div>
              <span v-if="defaultSkinActive" class="pet-settings__skin-status is-active">{{ t('pet.settings.skins.inUse') }}</span>
              <button v-else type="button" class="pet-settings__secondary-button" :disabled="saving" @click="void bindSkin(null)">{{ t('pet.settings.skins.use') }}</button>
            </div>

            <div v-for="skin in selectableSkinRecords" :key="skin.skinId" class="pet-settings__skin-row">
              <div class="pet-settings__skin-thumb">
                <PetAtlasFrame
                  v-if="skinPreviews[skin.skinId]"
                  :image-url="skinPreviews[skin.skinId].src"
                  :manifest="skinPreviews[skin.skinId].manifest"
                  action="idle"
                  :display-height="46"
                />
                <span v-else aria-hidden="true">{{ skinPreviewLoading[skin.skinId] ? '…' : '·' }}</span>
              </div>
              <div class="pet-settings__skin-copy">
                <strong>{{ skin.name }}</strong>
                <span>{{ skin.skinId }}{{ skin.modelId ? ` · ${skin.modelId}` : '' }} · {{ t('pet.settings.skins.poseCount', { count: skinPoseCount(skin) }) }}</span>
              </div>
              <span v-if="form.skinSelection.activeSkinId === skin.skinId" class="pet-settings__skin-status is-active">{{ t('pet.settings.skins.inUse') }}</span>
              <button v-else type="button" class="pet-settings__secondary-button" :disabled="saving" @click="void bindSkin(skin.skinId)">{{ t('pet.settings.skins.use') }}</button>
              <button
                v-if="!skin.builtin"
                type="button"
                class="pet-settings__icon-button"
                :disabled="deletingSkinId !== null || skinRefreshing"
                :title="t('pet.settings.skins.delete')"
                @click="deleteSkin(skin)"
              >
                ×
              </button>
            </div>
          </div>
          <p v-if="selectableSkinRecords.length === 0" class="pet-settings__hint">{{ t('pet.settings.skins.emptyHint') }}</p>
        </div>

        <div v-show="activeTab === 'stats'" class="pet-settings__stats-content">
        <div class="pet-settings__stats-heading">
          <div>
            <h3>{{ t('pet.settings.stats.title') }}</h3>
            <p>{{ t('pet.settings.stats.subtitle') }}</p>
          </div>
          <button
            type="button"
            class="pet-settings__secondary-button"
            :disabled="statsRefreshing"
            @click="refreshStats"
          >
            {{ statsRefreshing ? t('pet.common.refreshing') : t('pet.settings.stats.refresh') }}
          </button>
        </div>

        <div v-if="statsErrorMessage" class="pet-settings__stats-error">
          <span>{{ t('pet.settings.stats.loadFailed', { error: statsErrorMessage }) }}</span>
          <button type="button" class="pet-settings__secondary-button" @click="refreshStats">{{ t('pet.common.retry') }}</button>
        </div>

        <template v-if="snapshot">
          <section class="pet-settings__stats-block">
            <div class="pet-settings__stats-block-title">
              <h4>{{ t('pet.settings.stats.needs') }}</h4>
              <span>{{ t('pet.settings.stats.range') }}</span>
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
              <span>{{ t('pet.settings.stats.coins') }}</span>
              <strong>{{ formatInteger(snapshot.state.coins) }}</strong>
            </div>
            <div class="pet-settings__stats-card">
              <span class="pet-settings__stats-card-icon is-token" aria-hidden="true"></span>
              <span>{{ t('pet.settings.stats.token') }}</span>
              <strong>{{ formatInteger(snapshot.experience.totalTokens) }}</strong>
            </div>
            <div class="pet-settings__stats-card">
              <span class="pet-settings__stats-card-icon is-days" aria-hidden="true"></span>
              <span>{{ t('pet.settings.stats.companionDays') }}</span>
              <strong>{{ companionDays > 0 ? t('pet.settings.stats.days', { count: companionDays }) : t('pet.settings.stats.noData') }}</strong>
            </div>
            <div class="pet-settings__stats-card">
              <span class="pet-settings__stats-card-icon is-status" aria-hidden="true"></span>
              <span>{{ t('pet.settings.stats.currentStatus') }}</span>
              <strong>{{ petStatusLabel }}</strong>
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
  width: 100%;
  max-width: none;
  min-width: 0;
  box-sizing: border-box;
  margin: 0;
  padding: 32px 48px 48px;
  color: var(--settings-ink);
  font-family: var(--mac-font, system-ui, sans-serif);
}

.pet-settings__header,
.pet-settings__header-actions,
.pet-settings__setting-row,
.pet-settings__inline-controls,
.pet-settings__quiet-hours,
.pet-settings__range-hint {
  display: flex;
  align-items: center;
}

.pet-settings__header {
  justify-content: space-between;
  gap: 20px;
  margin-bottom: 18px;
}

.pet-settings__heading {
  min-width: 0;
}

.pet-settings h2,
.pet-settings h3,
.pet-settings p {
  margin: 0;
}

.pet-settings h2 {
  font-size: 18px;
  font-weight: 650;
  letter-spacing: 0;
}

.pet-settings__heading p,
.pet-settings__section-title p,
.pet-settings__setting-row span,
.pet-settings__hint {
  color: var(--settings-muted);
  font-size: 12px;
  line-height: 1.55;
}

.pet-settings__heading p {
  margin-top: 4px;
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

.pet-settings__content {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

/* 普通设置在桌面端放宽到主窗口可用空间，避免表单挤在左侧；Studio 和梦境历史继续使用完整画布。 */
.pet-settings__narrow-content {
  width: 100%;
  max-width: 1024px;
}

.pet-settings__wide-content {
  width: 100%;
  min-width: 0;
}

.pet-settings__tabs {
  display: flex;
  gap: 6px;
  margin-bottom: 14px;
  overflow-x: auto;
  padding-bottom: 2px;
}

.pet-settings__tab {
  flex: 0 0 auto;
  border: 1px solid var(--settings-line);
  border-radius: 6px;
  padding: 6px 10px;
  background: color-mix(in srgb, var(--settings-strong-surface) 72%, transparent);
  color: var(--settings-muted);
  cursor: pointer;
  font: inherit;
  font-size: 12px;
  transition: border-color 0.18s ease, background 0.18s ease, color 0.18s ease;
}

.pet-settings__tab:hover,
.pet-settings__tab.is-active {
  border-color: color-mix(in srgb, var(--mac-accent, #0a84ff) 48%, var(--settings-line));
  background: color-mix(in srgb, var(--mac-accent, #0a84ff) 12%, var(--settings-surface));
  color: var(--mac-accent, #0a84ff);
}

.pet-settings__overview-preview {
  display: flex;
  min-height: 190px;
  align-items: flex-end;
  justify-content: center;
  overflow: hidden;
  border: 1px solid color-mix(in srgb, var(--settings-line) 84%, transparent);
  border-radius: 10px;
  padding: 18px 16px 14px;
  background:
    linear-gradient(180deg, color-mix(in srgb, var(--mac-accent, #0a84ff) 7%, var(--settings-surface)), var(--settings-strong-surface));
}

.pet-settings__overview-placeholder {
  align-self: center;
  color: var(--settings-muted);
  font-size: 11px;
}

.pet-settings__overview-profile {
  display: flex;
  flex-direction: column;
  gap: 10px;
  border-top: 1px solid color-mix(in srgb, var(--settings-line) 72%, transparent);
  padding-top: 14px;
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
  gap: 14px;
}

.pet-settings__stats-heading,
.pet-settings__stats-block-title,
.pet-settings__growth-row,
.pet-settings__stat-row,
.pet-settings__experience-entry,
.pet-settings__unlock-row {
  display: flex;
  align-items: center;
}

.pet-settings__stats-heading {
  justify-content: space-between;
  gap: 14px;
}

.pet-settings__stats-heading h3,
.pet-settings__stats-block-title h4 {
  margin: 0;
}

.pet-settings__stats-heading h3 {
  font-size: 15px;
}

.pet-settings__stats-heading p,
.pet-settings__stats-block-title > span,
.pet-settings__stats-note,
.pet-settings__stats-empty {
  color: var(--settings-muted);
  font-size: 11px;
  line-height: 1.55;
}

.pet-settings__stats-heading p {
  margin-top: 3px;
}

.pet-settings__secondary-button {
  flex: 0 0 auto;
  border: 1px solid var(--settings-line);
  border-radius: 8px;
  padding: 7px 10px;
  background: color-mix(in srgb, var(--settings-strong-surface) 72%, transparent);
  color: var(--settings-ink);
  cursor: pointer;
  font: inherit;
  font-size: 11px;
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
  background: transparent;
  color: var(--settings-muted);
  cursor: pointer;
  font: inherit;
  font-size: 16px;
  line-height: 1;
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
  border-radius: 12px;
  padding: 14px;
  background: color-mix(in srgb, var(--settings-surface) 80%, transparent);
}

.pet-settings__stats-block-title {
  justify-content: space-between;
  gap: 12px;
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
  min-height: 62px;
  flex-direction: row;
  align-items: center;
  justify-content: center;
  gap: 8px;
  border: 1px solid var(--settings-line);
  border-radius: 10px;
  padding: 10px;
  background: color-mix(in srgb, var(--settings-surface) 80%, transparent);
}

.pet-settings__stats-card strong {
  overflow: hidden;
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
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.pet-settings__skin-row {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 10px;
  border: 1px solid color-mix(in srgb, var(--settings-line) 88%, transparent);
  border-radius: 9px;
  padding: 8px;
  background: color-mix(in srgb, var(--settings-strong-surface) 40%, transparent);
}

.pet-settings__skin-thumb {
  display: flex;
  width: 58px;
  height: 58px;
  flex: 0 0 58px;
  align-items: flex-end;
  justify-content: center;
  overflow: hidden;
  border-radius: 8px;
  background: color-mix(in srgb, var(--settings-strong-surface) 78%, transparent);
  color: var(--settings-muted);
  font-size: 18px;
}

.pet-settings__skin-copy {
  display: flex;
  min-width: 0;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 4px;
}

.pet-settings__skin-copy strong,
.pet-settings__skin-copy span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-settings__stats-card > span:nth-child(2) {
  min-width: 0;
  flex: 1 1 auto;
}

.pet-settings__skin-copy strong {
  color: var(--settings-ink);
  font-size: 12px;
  font-weight: 600;
}

.pet-settings__skin-copy span {
  color: var(--settings-muted);
  font-size: 10px;
}

.pet-settings__skin-status {
  flex: 0 0 auto;
  color: var(--settings-muted);
  font-size: 10px;
  white-space: nowrap;
}

.pet-settings__skin-status.is-active {
  color: #328c5d;
}

.pet-settings__skin-content {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 12px;
}

.pet-settings__skin-window {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  border: 1px solid var(--settings-line);
  border-radius: 8px;
  padding: 12px;
  background: color-mix(in srgb, var(--settings-surface) 80%, transparent);
}

.pet-settings__skin-window-copy {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 9px;
}

.pet-settings__skin-window-copy > div {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 3px;
}

.pet-settings__skin-window-copy strong {
  font-size: 12px;
}

.pet-settings__skin-window-copy span:not(.pet-settings__monitor-icon) {
  color: var(--settings-muted);
  font-size: 10px;
}

.pet-settings__monitor-icon {
  position: relative;
  display: inline-block;
  width: 16px;
  height: 12px;
  flex: 0 0 16px;
  border: 1.5px solid var(--settings-muted);
  border-radius: 2px;
}

.pet-settings__monitor-icon::after {
  position: absolute;
  bottom: -5px;
  left: 4px;
  width: 6px;
  height: 1.5px;
  background: var(--settings-muted);
  box-shadow: 2px 2px 0 -0.25px var(--settings-muted);
  content: '';
}

.pet-settings__skin-directory-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.pet-settings__skin-directory-row .pet-settings__managed-directory {
  min-width: 0;
  flex: 1 1 auto;
  margin-top: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-settings__skin-directory-row .pet-settings__field-actions {
  flex: 0 0 auto;
  margin-top: 0;
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

@media (max-width: 640px) {
  .pet-settings {
    padding: 16px 12px;
  }

  .pet-settings__header {
    align-items: flex-start;
    flex-direction: column;
    gap: 12px;
  }

  .pet-settings__header-actions {
    width: 100%;
    justify-content: space-between;
  }

  .pet-settings__stats-heading {
    align-items: flex-start;
    flex-direction: column;
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

  .pet-settings__skin-row {
    align-items: flex-start;
    flex-wrap: wrap;
  }

  .pet-settings__skin-copy {
    flex-basis: calc(100% - 68px);
  }

  .pet-settings__skin-status,
  .pet-settings__skin-row > .pet-settings__secondary-button,
  .pet-settings__skin-row > .pet-settings__icon-button {
    margin-left: 68px;
  }

  .pet-settings__stats-summary {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .pet-settings__skin-directory-row {
    align-items: flex-start;
    flex-direction: column;
  }

  .pet-settings__skin-directory-row .pet-settings__field-actions {
    width: 100%;
  }

  .pet-settings__skin-directory-row .pet-settings__secondary-button {
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
