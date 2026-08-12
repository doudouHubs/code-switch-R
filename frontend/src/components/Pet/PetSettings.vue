<script setup lang="ts">
import { Call } from '../../wails-runtime-compat'
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { petApi, type PetRuntimeMode } from './petApi'
import PetDreamHistoryPanel from './PetDreamHistoryPanel.vue'
import PetMemoryPanel from './PetMemoryPanel.vue'
import PetStudio from './PetStudio.vue'
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

type PetTab = 'overview' | 'stats' | 'agent' | 'sleep' | 'skins' | 'memory' | 'dream-history' | 'studio'

const PET_TABS: ReadonlyArray<{ id: PetTab }> = [
  { id: 'overview' },
  { id: 'stats' },
  { id: 'agent' },
  { id: 'sleep' },
  { id: 'skins' },
  { id: 'memory' },
  { id: 'dream-history' },
  { id: 'studio' }
]

interface PetExperienceLogEntry {
  id: string
  at: number
  model: string
  tokens: number
  premium: boolean
  exp: number
}

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
const loading = ref(true)
const saving = ref(false)
const errorMessage = ref('')
const runtimeMode = ref<PetRuntimeMode>('unknown')
const studioOpen = ref(false)
const activeTab = ref<PetTab>('overview')
const snapshot = ref<PetSnapshot | null>(null)
const statsNow = ref(Date.now())
const statsRefreshing = ref(false)
const statsErrorMessage = ref('')
const experienceLog = ref<PetExperienceLogEntry[]>([])
const experienceLogLoading = ref(false)
const experienceLogError = ref('')
let statsRefreshTimer: number | undefined
let statsRequestGeneration = 0

const activeSkinIdModel = computed({
  get: () => form.skinSelection.activeSkinId ?? '',
  set: (value: string) => {
    form.skinSelection.activeSkinId = value || null
  }
})

const reasoningEffortModel = computed({
  get: () => form.agent.reasoningEffort ?? '',
  set: (value: string) => {
    form.agent.reasoningEffort = (value || null) as PetReasoningEffort | null
  }
})

const displayModeLabel = computed(() => {
  if (runtimeMode.value === 'fallback') return t('pet.settings.runtime.fallback')
  if (runtimeMode.value === 'backend') return t('pet.settings.runtime.backend')
  return t('pet.settings.runtime.waiting')
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

function selectTab(tab: PetTab): void {
  activeTab.value = tab
  studioOpen.value = tab === 'studio'
}

function toggleStudio(): void {
  selectTab(studioOpen.value ? 'overview' : 'studio')
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
    runtimeMode.value = petApi.getRuntimeMode()
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
      activeSkinId: value.skinSelection.activeSkinId || null
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
    runtimeMode.value = petApi.getRuntimeMode()
    // 外部 v-model 是受控值；没有受控值时才用后端快照初始化表单。
    if (!props.modelValue) assignForm(toForm(next))
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : String(error)
    emit('error', error)
  } finally {
    loading.value = false
  }
}

async function saveSettings(): Promise<void> {
  if (saving.value || loading.value) return
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
    statsNow.value = Date.now()
    runtimeMode.value = petApi.getRuntimeMode()
    assignForm(toForm(next))
    emit('saved', next)
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : String(error)
    emit('error', error)
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

onMounted(() => {
  if (route.query.studio === '1') {
    // 跨窗口事件通过 query 传递一次性打开 Studio 的意图；消费后清掉 query，
    // 防止旧意图在返回设置后再次触发。
    selectTab('studio')
    void router.replace({ path: route.path, query: {} })
  }
  void loadSettings()
})

onUnmounted(() => {
  stopStatsRefreshTimer()
  statsRequestGeneration += 1
})
</script>

<template>
  <div class="pet-settings">
    <header class="pet-settings__header">
      <div class="pet-settings__heading">
        <span class="pet-settings__eyebrow">{{ t('pet.settings.eyebrow') }}</span>
        <h2>{{ t('pet.settings.title') }}</h2>
        <p>{{ t('pet.settings.subtitle') }}</p>
      </div>
      <div class="pet-settings__header-actions">
        <span class="pet-settings__connection">{{ displayModeLabel }}</span>
        <button type="button" class="pet-settings__retry" @click="toggleStudio">
          {{ studioOpen ? t('pet.settings.backToSettings') : t('pet.settings.openStudio') }}
        </button>
        <button type="button" class="pet-settings__save" :disabled="loading || saving" @click="saveSettings">
          {{ saving ? t('pet.common.saving') : t('pet.settings.save') }}
        </button>
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
      <div v-if="studioOpen" class="pet-settings__studio">
        <PetStudio :pet-id="props.petId" />
      </div>

      <section v-show="activeTab === 'overview'" class="pet-settings__section">
        <div class="pet-settings__section-title">
          <div>
            <h3>{{ t('pet.settings.overview.title') }}</h3>
            <p>{{ t('pet.settings.overview.subtitle') }}</p>
          </div>
        </div>

        <div class="pet-settings__setting-row">
          <div>
            <strong>{{ t('pet.settings.overview.windowEnabled') }}</strong>
            <span>{{ t('pet.settings.overview.windowEnabledHint') }}</span>
          </div>
          <label class="pet-settings__switch">
            <input v-model="form.window.enabled" type="checkbox" />
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
              <input v-model="form.care.autoCareEnabled" type="checkbox" />
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
          />
          <div class="pet-settings__range-hint"><span>{{ t('pet.settings.overview.insensitive') }}</span><span>{{ t('pet.settings.overview.attentive') }}</span></div>
        </div>
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
            <input v-model="form.agent.providerPlatform" type="text" :placeholder="t('pet.settings.agent.platformPlaceholder')" />
          </label>
          <label class="pet-settings__field">
            <span>{{ t('pet.settings.agent.providerId') }}</span>
            <input v-model="form.agent.providerId" type="text" :placeholder="t('pet.settings.agent.providerPlaceholder')" />
          </label>
          <label class="pet-settings__field">
            <span>{{ t('pet.settings.agent.modelId') }}</span>
            <input v-model="form.agent.modelId" type="text" :placeholder="t('pet.settings.agent.modelPlaceholder')" />
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
          <label class="pet-settings__field">
            <span>{{ t('pet.settings.agent.projectId') }}</span>
            <input v-model="form.agent.projectId" type="text" :placeholder="t('pet.settings.agent.projectPlaceholder')" />
          </label>
        </div>

        <label class="pet-settings__field">
          <span>{{ t('pet.settings.agent.systemPrompt') }}</span>
          <textarea v-model="form.agent.systemPrompt" rows="3" :placeholder="t('pet.settings.agent.systemPromptPlaceholder')"></textarea>
        </label>

        <div class="pet-settings__setting-row is-stacked">
          <div class="pet-settings__setting-copy">
            <strong>{{ t('pet.settings.agent.proactive') }}</strong>
            <span>{{ t('pet.settings.agent.proactiveHint') }}</span>
          </div>
          <div class="pet-settings__inline-controls">
            <label class="pet-settings__switch">
              <input v-model="form.agent.proactive" type="checkbox" />
              <span aria-hidden="true"></span>
            </label>
            <select v-model="form.agent.proactiveFreq" :aria-label="t('pet.settings.agent.proactiveFrequency')">
              <option value="low">{{ t('pet.settings.frequency.low') }}</option>
              <option value="medium">{{ t('pet.settings.frequency.medium') }}</option>
              <option value="high">{{ t('pet.settings.frequency.high') }}</option>
            </select>
          </div>
          <div class="pet-settings__quiet-hours">
            <label class="pet-settings__number-field">
              <span>{{ t('pet.settings.agent.quietStart') }}</span>
              <input v-model.number="form.agent.quietStart" type="number" min="0" max="23" />
              <em>{{ t('pet.settings.agent.hour') }}</em>
            </label>
            <span class="pet-settings__quiet-separator">{{ t('pet.settings.agent.to') }}</span>
            <label class="pet-settings__number-field">
              <span>{{ t('pet.settings.agent.quietEnd') }}</span>
              <input v-model.number="form.agent.quietEnd" type="number" min="0" max="23" />
              <em>{{ t('pet.settings.agent.hour') }}</em>
            </label>
          </div>
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
        <div class="pet-settings__field-grid">
          <label class="pet-settings__field">
            <span>{{ t('pet.settings.voice.providerId') }}</span>
            <input v-model="form.agent.voiceProviderId" type="text" :placeholder="t('pet.settings.voice.providerPlaceholder')" />
          </label>
          <label class="pet-settings__field">
            <span>{{ t('pet.settings.voice.modelId') }}</span>
            <input v-model="form.agent.voiceModelId" type="text" :placeholder="t('pet.settings.voice.modelPlaceholder')" />
          </label>
          <label class="pet-settings__field">
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
        <label class="pet-settings__field">
          <span>{{ t('pet.settings.voice.instruction') }}</span>
          <input v-model="form.agent.voiceInstruction" type="text" :placeholder="t('pet.settings.voice.instructionPlaceholder')" />
        </label>
        <label class="pet-settings__field">
          <span>{{ t('pet.settings.voice.tag') }}</span>
          <input v-model="form.agent.voiceTag" type="text" :placeholder="t('pet.settings.voice.tagPlaceholder')" />
        </label>
      </section>

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
        <label class="pet-settings__field">
          <span>{{ t('pet.settings.dream.prompt') }}</span>
          <textarea v-model="form.dream.prompt" rows="3" :placeholder="t('pet.settings.dream.promptPlaceholder')"></textarea>
        </label>
        <label class="pet-settings__field">
          <span>{{ t('pet.settings.dream.keywords') }}</span>
          <input v-model="form.dream.keywords" type="text" :placeholder="t('pet.settings.dream.keywordsPlaceholder')" />
        </label>
        <div class="pet-settings__field-grid">
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

      <section v-show="activeTab === 'skins'" class="pet-settings__section">
        <div class="pet-settings__section-title">
          <div>
            <h3>{{ t('pet.settings.skins.title') }}</h3>
            <p>{{ t('pet.settings.skins.subtitle') }}</p>
          </div>
        </div>
        <label class="pet-settings__field">
          <span>{{ t('pet.settings.skins.current') }}</span>
          <select v-model="activeSkinIdModel">
            <option value="">{{ t('pet.settings.skins.default') }}</option>
            <option v-for="skin in skins" :key="skin.skinId" :value="skin.skinId">
              {{ skin.name }}{{ skin.modelId ? ` · ${skin.modelId}` : '' }}
            </option>
          </select>
        </label>
        <p v-if="skins.length === 0" class="pet-settings__hint">
          {{ t('pet.settings.skins.emptyHint') }}
        </p>
      </section>

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
              <span class="pet-settings__stat-label">{{ t('pet.settings.stats.hunger') }}</span>
              <div class="pet-settings__stat-track">
                <span class="pet-settings__stat-fill is-hunger" :style="{ width: `${formatStatPercent(snapshot.state.hunger)}%` }"></span>
              </div>
              <span class="pet-settings__stat-value">{{ formatStatPercent(snapshot.state.hunger) }} / 100</span>
            </div>
            <div class="pet-settings__stat-row">
              <span class="pet-settings__stat-label">{{ t('pet.settings.stats.cleanliness') }}</span>
              <div class="pet-settings__stat-track">
                <span class="pet-settings__stat-fill is-cleanliness" :style="{ width: `${formatStatPercent(snapshot.state.cleanliness)}%` }"></span>
              </div>
              <span class="pet-settings__stat-value">{{ formatStatPercent(snapshot.state.cleanliness) }} / 100</span>
            </div>
            <div class="pet-settings__stat-row">
              <span class="pet-settings__stat-label">{{ t('pet.settings.stats.mood') }}</span>
              <div class="pet-settings__stat-track">
                <span class="pet-settings__stat-fill is-mood" :style="{ width: `${formatStatPercent(snapshot.state.mood)}%` }"></span>
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
              <span>{{ t('pet.settings.stats.coins') }}</span>
              <strong>{{ formatInteger(snapshot.state.coins) }}</strong>
            </div>
            <div class="pet-settings__stats-card">
              <span>{{ t('pet.settings.stats.token') }}</span>
              <strong>{{ formatInteger(snapshot.experience.totalTokens) }}</strong>
            </div>
            <div class="pet-settings__stats-card">
              <span>{{ t('pet.settings.stats.companionDays') }}</span>
              <strong>{{ companionDays > 0 ? t('pet.settings.stats.days', { count: companionDays }) : t('pet.settings.stats.noData') }}</strong>
            </div>
            <div class="pet-settings__stats-card">
              <span>{{ t('pet.settings.stats.currentStatus') }}</span>
              <strong>{{ petStatusLabel }}</strong>
            </div>
            <div class="pet-settings__stats-card">
              <span>{{ t('pet.settings.stats.plans') }}</span>
              <strong>{{ t('pet.settings.stats.count', { count: snapshot.plans.length }) }}</strong>
            </div>
            <div class="pet-settings__stats-card">
              <span>{{ t('pet.settings.stats.memories') }}</span>
              <strong>{{ t('pet.settings.stats.count', { count: snapshot.memories.length }) }}</strong>
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
                <span v-if="petLevel >= unlock.level" class="is-unlocked">{{ t('pet.settings.stats.unlocked') }}</span>
                <span v-else class="is-locked">{{ t('pet.settings.stats.unlockAt', { level: unlock.level }) }}</span>
              </div>
            </div>
          </section>
        </template>
        <div v-else class="pet-settings__stats-empty">{{ t('pet.settings.stats.noSnapshot') }}</div>
      </div>

      <PetMemoryPanel v-if="activeTab === 'memory'" :pet-id="props.petId" />
      <PetDreamHistoryPanel v-if="activeTab === 'dream-history'" :pet-id="props.petId" />
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
  max-width: 820px;
  min-width: 0;
  margin: 0 auto;
  padding: 24px;
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

.pet-settings__eyebrow {
  color: var(--mac-accent, #0a84ff);
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.12em;
}

.pet-settings h2,
.pet-settings h3,
.pet-settings p {
  margin: 0;
}

.pet-settings h2 {
  margin-top: 3px;
  font-size: 22px;
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
  margin-top: 5px;
}

.pet-settings__header-actions {
  flex: 0 0 auto;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 10px;
}

.pet-settings__connection {
  color: var(--settings-muted);
  font-size: 11px;
  white-space: nowrap;
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
  border-radius: 8px;
  padding: 7px 10px;
  background: color-mix(in srgb, var(--settings-strong-surface) 72%, transparent);
  color: var(--settings-muted);
  cursor: pointer;
  font: inherit;
  font-size: 11px;
  transition: border-color 0.18s ease, background 0.18s ease, color 0.18s ease;
}

.pet-settings__tab:hover,
.pet-settings__tab.is-active {
  border-color: color-mix(in srgb, var(--mac-accent, #0a84ff) 48%, var(--settings-line));
  background: color-mix(in srgb, var(--mac-accent, #0a84ff) 12%, var(--settings-surface));
  color: var(--mac-accent, #0a84ff);
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
  width: 42px;
  flex: 0 0 auto;
  color: var(--settings-muted);
  font-size: 11px;
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
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px;
}

.pet-settings__stats-card {
  display: flex;
  min-width: 0;
  min-height: 62px;
  flex-direction: column;
  justify-content: center;
  gap: 5px;
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
  color: #328c5d;
}

.pet-settings__unlock-row .is-locked {
  color: var(--settings-muted);
}

.pet-settings__section {
  display: flex;
  flex-direction: column;
  gap: 14px;
  border: 1px solid var(--settings-line);
  border-radius: 12px;
  padding: 16px;
  background: color-mix(in srgb, var(--settings-surface) 80%, transparent);
  box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.18);
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

  .pet-settings__stats-summary {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .pet-settings__experience-time {
    display: none;
  }

  .pet-settings__quiet-hours {
    flex-basis: 100%;
  }
}
</style>
