<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Bot, Check, CircleAlert, GitCompare, Minimize2, RefreshCw, SendHorizontal, Shield, Square, X } from '@lucide/vue'
import { Events } from '../../wails-runtime-compat'
import { extractErrorMessage } from '../../utils/error'
import PetChatHistoryPanel from '../Pet/PetChatHistoryPanel.vue'
import { petApi } from '../Pet/petApi'
import { DEFAULT_PET_ID, type PetSettingsSnapshot } from '../Pet/petTypes'
import { agentApi } from './agentApi'
import {
  normalizeAgentConversationEvent,
  type AgentCommandName,
  type AgentCommandRequest,
  type AgentConversationEvent,
  type AgentInteraction,
  type AgentSkill,
  type AgentSkillError,
  type ResolveInteractionRequest
} from './agentTypes'

const { t } = useI18n()

type ReviewTarget = 'uncommitted' | 'base' | 'commit' | 'custom'
type MCPFieldType = 'string' | 'number' | 'integer' | 'boolean' | 'object' | 'array'

interface MCPFieldView {
  name: string
  title: string
  description: string
  type: MCPFieldType
  required: boolean
  options: string[]
}

const loading = ref(true)
const errorMessage = ref('')
const snapshot = ref<PetSettingsSnapshot | null>(null)
const skills = ref<AgentSkill[]>([])
const skillErrors = ref<AgentSkillError[]>([])
const skillsLoading = ref(false)
const skillsError = ref('')
const selectedSkillKeys = ref<string[]>([])
const commandBusy = ref(false)
const commandError = ref('')
const commandNotice = ref('')
const activeRequestId = ref('')
const activeTurnId = ref('')
const activeEventType = ref<AgentConversationEvent['type'] | ''>('')
const activeSource = ref('')
const pendingInteraction = ref<AgentInteraction | null>(null)
const interactionBusy = ref(false)
const interactionError = ref('')
const reviewTarget = ref<ReviewTarget>('uncommitted')
const reviewReference = ref('')
const steerInput = ref('')
const permissionScope = ref<'turn' | 'session'>('turn')
const answerDrafts = ref<Record<string, string[]>>({})
const otherAnswerDrafts = ref<Record<string, string>>({})
const mcpFormValues = ref<Record<string, unknown>>({})
const mcpJsonDraft = ref('{}')

let loadGeneration = 0
let capabilityGeneration = 0
let capabilityProjectID = ''
let capabilitiesLoaded = false
let stopAgentEvent: (() => void) | null = null
const eventSequences = new Map<string, number>()

const projectID = computed(() => snapshot.value?.agent?.projectId?.trim() ?? '')
const projectName = computed(() => snapshot.value?.agent?.projectName?.trim() || projectID.value)
const projectReady = computed(() => Boolean(projectID.value))
const selectedSkillReferences = computed(() => skills.value
  .filter((skill) => skill.enabled && selectedSkillKeys.value.includes(skill.path))
  .map(({ name, path }) => ({ name, path })))
const filteredSkills = computed(() => skills.value)
const configuredModelID = computed(() => snapshot.value?.agent?.modelId?.trim() ?? '')
const configuredPlatform = computed(() => snapshot.value?.agent?.providerPlatform?.trim().toLowerCase() ?? '')
const configuredReasoningEffort = computed(() => snapshot.value?.agent?.reasoningEffort?.trim() ?? '')
const reviewNeedsReference = computed(() => reviewTarget.value !== 'uncommitted')
const reviewReferencePlaceholder = computed(() => {
  switch (reviewTarget.value) {
    case 'base': return t('pet.agentManager.reviewBasePlaceholder')
    case 'commit': return t('pet.agentManager.reviewCommitPlaceholder')
    case 'custom': return t('pet.agentManager.reviewCustomPlaceholder')
    default: return ''
  }
})
const liveStatusText = computed(() => {
  if (!activeRequestId.value) return ''
  switch (activeEventType.value) {
    case 'queued': return t('pet.agentManager.status.queued')
    case 'started': return t('pet.agentManager.status.started')
    case 'interaction': return t('pet.agentManager.status.interaction')
    case 'completed': return t('pet.agentManager.status.completed')
    case 'failed': return t('pet.agentManager.status.failed')
    case 'cancelled': return t('pet.agentManager.status.cancelled')
    default: return t('pet.agentManager.status.running')
  }
})
const interactionDecisionChoices = computed(() => {
  const interaction = pendingInteraction.value
  if (!interaction) return []
  if (interaction.availableDecisions.length > 0) return interaction.availableDecisions
  if (interaction.kind === 'approval') return ['accept', 'decline']
  if (interaction.kind === 'permission') return ['accept', 'decline']
  return []
})
const mcpFields = computed<MCPFieldView[]>(() => {
  const schema = pendingInteraction.value?.requestedSchema
  const properties = isRecord(schema?.properties) ? schema.properties : {}
  const required = new Set(Array.isArray(schema?.required)
    ? schema.required.flatMap((value) => typeof value === 'string' ? [value] : [])
    : [])
  return Object.entries(properties).flatMap(([name, value]) => {
    const property = isRecord(value) ? value : {}
    const rawType = Array.isArray(property.type) ? property.type.find((item) => typeof item === 'string' && item !== 'null') : property.type
    const type: MCPFieldType = rawType === 'number' || rawType === 'integer' || rawType === 'boolean' || rawType === 'object' || rawType === 'array'
      ? rawType
      : 'string'
    const options = Array.isArray(property.enum)
      ? property.enum.flatMap((item) => typeof item === 'string' ? [item] : [])
      : []
    return [{
      name,
      title: typeof property.title === 'string' && property.title.trim() ? property.title.trim() : name,
      description: typeof property.description === 'string' ? property.description.trim() : '',
      type,
      required: required.has(name),
      options
    }]
  })
})

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function stringifyError(error: unknown, fallback: string): string {
  return extractErrorMessage(error, fallback)
}

function resetActiveRequest(): void {
  activeRequestId.value = ''
  activeTurnId.value = ''
  activeEventType.value = ''
  activeSource.value = ''
}

function resetInteractionDrafts(interaction: AgentInteraction): void {
  const answers: Record<string, string[]> = {}
  for (const question of interaction.questions) answers[question.id] = []
  answerDrafts.value = answers
  otherAnswerDrafts.value = {}
  permissionScope.value = 'turn'
  mcpFormValues.value = {}
  mcpJsonDraft.value = '{}'
  interactionError.value = ''
}

function handleAgentEvent(value: unknown): void {
  const event = normalizeAgentConversationEvent(value)
  if (!event || !projectID.value || event.projectId !== projectID.value) return
  if (event.source && event.source !== 'manager' && event.source !== 'channel') return

  // Wails 重连或频道转发可能重复投递同一条事件；每个 request 的 sequence 只允许单调前进，
  // 否则旧 interaction 会覆盖新决定，页面看起来像“点了按钮但仍在等待”。
  if (event.sequence > 0) {
    const previous = eventSequences.get(event.requestId) ?? 0
    if (event.sequence <= previous) return
    eventSequences.set(event.requestId, event.sequence)
  }

  if (event.type === 'interaction' && event.interaction) {
    if (pendingInteraction.value?.id !== event.interaction.id) resetInteractionDrafts(event.interaction)
    pendingInteraction.value = event.interaction
    activeRequestId.value = event.requestId
    activeTurnId.value = event.interaction.turnId
    activeEventType.value = event.type
    activeSource.value = event.source
    return
  }

  if (event.type === 'queued' || event.type === 'started' || event.type === 'progress' || event.type === 'delta') {
    activeRequestId.value = event.requestId
    activeEventType.value = event.type
    activeSource.value = event.source
    return
  }

  if (event.type === 'completed' || event.type === 'failed' || event.type === 'cancelled') {
    if (event.requestId === activeRequestId.value) {
      resetActiveRequest()
      if (pendingInteraction.value) pendingInteraction.value = null
    }
  }
}

function commandRequest(command: AgentCommandName, extra: Partial<AgentCommandRequest> = {}): AgentCommandRequest {
  return {
    ...extra,
    projectId: projectID.value,
    projectName: projectName.value,
    petId: DEFAULT_PET_ID,
    source: 'manager',
    sessionName: t('pet.agentManager.title'),
    command
  }
}

function commandLabel(command: AgentCommandName): string {
  const labels: Record<AgentCommandName, string> = {
    skills: t('pet.agentManager.commands.skills'),
    models: t('pet.agentManager.commands.models'),
    review: t('pet.agentManager.commands.review'),
    compact: t('pet.agentManager.commands.compact'),
    steer: t('pet.agentManager.commands.steer'),
    interrupt: t('pet.agentManager.commands.interrupt')
  }
  return labels[command]
}

async function executeManagerCommand(command: AgentCommandName, extra: Partial<AgentCommandRequest> = {}): Promise<boolean> {
  if (!projectReady.value) {
    commandError.value = t('pet.agentManager.projectRequired')
    return false
  }
  if (commandBusy.value) return false
  commandBusy.value = true
  commandError.value = ''
  commandNotice.value = ''
  try {
    const result = await agentApi.executeCommand(commandRequest(command, extra))
    if (!result.accepted) throw new Error(t('pet.agentManager.commandRejected'))
    if (result.requestId) activeRequestId.value = result.requestId
    if (result.turnId) activeTurnId.value = result.turnId
    commandNotice.value = t('pet.agentManager.commandAccepted', { command: commandLabel(command) })
    return true
  } catch (error) {
    commandError.value = stringifyError(error, t('pet.agentManager.commandFailed'))
    return false
  } finally {
    commandBusy.value = false
  }
}

async function runReview(): Promise<void> {
  const reference = reviewReference.value.trim()
  if (reviewNeedsReference.value && !reference) {
    commandError.value = t('pet.agentManager.reviewReferenceRequired')
    return
  }
  const args = reviewTarget.value === 'uncommitted' ? [] : [reviewTarget.value, reference]
  const accepted = await executeManagerCommand('review', { args, delivery: 'inline' })
  if (accepted) reviewReference.value = ''
}

async function runCompact(): Promise<void> {
  await executeManagerCommand('compact')
}

async function runSteer(): Promise<void> {
  const input = steerInput.value.trim()
  if (!input) {
    commandError.value = t('pet.agentManager.steerRequired')
    return
  }
  const accepted = await executeManagerCommand('steer', {
    input,
    expectedTurnId: activeTurnId.value
  })
  if (accepted) steerInput.value = ''
}

async function runInterrupt(): Promise<void> {
  await executeManagerCommand('interrupt', { expectedTurnId: activeTurnId.value })
}

async function loadCapabilities(nextProjectID: string, forceReload = false): Promise<void> {
  const normalizedProjectID = nextProjectID.trim()
  if (!forceReload && capabilitiesLoaded && capabilityProjectID === normalizedProjectID) return
  const generation = ++capabilityGeneration
  capabilityProjectID = normalizedProjectID
  capabilitiesLoaded = false
  skills.value = []
  selectedSkillKeys.value = []
  skillErrors.value = []
  skillsError.value = ''
  if (!normalizedProjectID) {
    skillsError.value = t('pet.agentManager.projectRequired')
    capabilitiesLoaded = true
    return
  }

  skillsLoading.value = true
  try {
    const result = await agentApi.listSkills(commandRequest('skills', { forceReload }))
    if (generation !== capabilityGeneration) return
    skills.value = result.skills
    selectedSkillKeys.value = result.skills.filter((skill) => skill.enabled).map((skill) => skill.path)
    skillErrors.value = result.errors
  } catch (error) {
    if (generation !== capabilityGeneration) return
    skillsError.value = stringifyError(error, t('pet.agentManager.skillsLoadFailed'))
  } finally {
    if (generation !== capabilityGeneration) return
    skillsLoading.value = false
    capabilitiesLoaded = true
  }
}

async function loadSnapshot(forceCapabilities = false): Promise<void> {
  const generation = ++loadGeneration
  const previousProjectID = projectID.value
  if (!snapshot.value) loading.value = true
  errorMessage.value = ''
  try {
    // Agent 首屏只读取名称和 Agent 配置；完整快照中的 atlas、计划、梦境和记忆不参与聊天挂载，
    // 不应因为打开聊天页而增加 Wails 序列化和 WebView 解码成本。
    const next = await petApi.getSettingsSnapshot(DEFAULT_PET_ID)
    if (generation !== loadGeneration) return
    snapshot.value = next
    const nextProjectID = next.agent?.projectId?.trim() ?? ''
    if (previousProjectID && previousProjectID !== nextProjectID) {
      resetActiveRequest()
      pendingInteraction.value = null
      eventSequences.clear()
    }
    void loadCapabilities(nextProjectID, forceCapabilities)
  } catch (error) {
    if (generation !== loadGeneration) return
    errorMessage.value = stringifyError(error, t('pet.agentManager.snapshotLoadFailed'))
  } finally {
    if (generation === loadGeneration) loading.value = false
  }
}

async function refreshAll(): Promise<void> {
  await loadSnapshot(true)
}

function retrySkills(): void {
  void loadCapabilities(projectID.value, true)
}

function formatDecision(decision: string): string {
  const labels: Record<string, string> = {
    accept: t('pet.agentManager.interaction.accept'),
    acceptForSession: t('pet.agentManager.interaction.acceptForSession'),
    decline: t('pet.agentManager.interaction.decline'),
    cancel: t('pet.agentManager.interaction.cancel')
  }
  return labels[decision] ?? decision
}

function interactionTitle(interaction: AgentInteraction): string {
  return interaction.title || interaction.method || t('pet.agentManager.interaction.title')
}

function answerValues(questionID: string): string[] {
  return answerDrafts.value[questionID] ?? []
}

function setFreeAnswer(questionID: string, event: Event): void {
  const target = event.target
  if (!(target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement)) return
  answerDrafts.value[questionID] = target.value ? [target.value] : []
}

function toggleAnswer(questionID: string, value: string, event: Event): void {
  const target = event.target
  if (!(target instanceof HTMLInputElement)) return
  const values = new Set(answerValues(questionID))
  if (target.checked) values.add(value)
  else values.delete(value)
  answerDrafts.value[questionID] = [...values]
}

function setOtherAnswer(questionID: string, event: Event): void {
  const target = event.target
  if (!(target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement)) return
  otherAnswerDrafts.value[questionID] = target.value
}

function mcpFieldValue(name: string): unknown {
  return mcpFormValues.value[name]
}

function mcpFieldText(name: string): string {
  const value = mcpFieldValue(name)
  if (typeof value === 'string') return value
  if (typeof value === 'number' && Number.isFinite(value)) return String(value)
  return ''
}

function setMcpFieldValue(name: string, event: Event): void {
  const target = event.target
  if (target instanceof HTMLInputElement) {
    mcpFormValues.value[name] = target.type === 'checkbox' ? target.checked : target.value
  } else if (target instanceof HTMLSelectElement || target instanceof HTMLTextAreaElement) {
    mcpFormValues.value[name] = target.value
  }
}

async function resolveInteraction(payload: Omit<ResolveInteractionRequest, 'interactionId'>): Promise<void> {
  const interaction = pendingInteraction.value
  if (!interaction || interactionBusy.value) return
  interactionBusy.value = true
  interactionError.value = ''
  try {
    await agentApi.resolveInteraction({ interactionId: interaction.id, ...payload })
    if (pendingInteraction.value?.id === interaction.id) pendingInteraction.value = null
    commandNotice.value = t('pet.agentManager.interactionResolved')
  } catch (error) {
    interactionError.value = stringifyError(error, t('pet.agentManager.interactionResolveFailed'))
  } finally {
    interactionBusy.value = false
  }
}

function resolveApproval(decision: string): Promise<void> {
  return resolveInteraction({ decision })
}

function resolvePermission(decision: string): Promise<void> {
  return resolveInteraction({
    decision,
    scope: permissionScope.value,
    permissions: {}
  })
}

function resolveUserInput(): Promise<void> {
  const interaction = pendingInteraction.value
  if (!interaction) return Promise.resolve()
  const answers: Record<string, string[]> = {}
  for (const question of interaction.questions) {
    const values = [...answerValues(question.id)]
    const other = (otherAnswerDrafts.value[question.id] ?? '').trim()
    if (other && !values.includes(other)) values.push(other)
    answers[question.id] = values
  }
  return resolveInteraction({ answers })
}

function buildMcpContent(): Record<string, unknown> {
  if (mcpFields.value.length === 0) {
    let parsed: unknown
    try {
      parsed = JSON.parse(mcpJsonDraft.value)
    } catch {
      throw new Error(t('pet.agentManager.interaction.invalidJson'))
    }
    if (!isRecord(parsed)) throw new Error(t('pet.agentManager.interaction.invalidJson'))
    return parsed
  }

  const content: Record<string, unknown> = {}
  for (const field of mcpFields.value) {
    const raw = mcpFieldValue(field.name)
    if (field.type === 'boolean') {
      if (raw !== undefined) content[field.name] = raw === true
      continue
    }
    const text = typeof raw === 'string' ? raw.trim() : ''
    if (!text) {
      if (field.required) throw new Error(t('pet.agentManager.interaction.required', { field: field.title }))
      continue
    }
    if (field.type === 'number' || field.type === 'integer') {
      const number = Number(text)
      if (!Number.isFinite(number) || (field.type === 'integer' && !Number.isInteger(number))) {
        throw new Error(t('pet.agentManager.interaction.invalidNumber', { field: field.title }))
      }
      content[field.name] = number
    } else if (field.type === 'object' || field.type === 'array') {
      try {
        content[field.name] = JSON.parse(text)
      } catch {
        throw new Error(t('pet.agentManager.interaction.invalidJson'))
      }
    } else {
      content[field.name] = text
    }
  }
  return content
}

async function resolveMcp(action: 'accept' | 'decline' | 'cancel'): Promise<void> {
  if (action !== 'accept') {
    await resolveInteraction({ action })
    return
  }
  try {
    await resolveInteraction({ action, content: buildMcpContent() })
  } catch (error) {
    interactionError.value = stringifyError(error, t('pet.agentManager.interaction.invalidJson'))
  }
}

onMounted(() => {
  stopAgentEvent = Events.On('pet.ai', (event) => handleAgentEvent(event.data))
})

// 应用路由使用 keep-alive；每次回到 Agent 管家都重新读取快照，避免宠物设置刚保存的
// project/persona 仍被缓存页面拿来发送下一条长会话消息。
onActivated(() => {
  void loadSnapshot()
})

onBeforeUnmount(() => {
  loadGeneration += 1
  capabilityGeneration += 1
  stopAgentEvent?.()
  stopAgentEvent = null
})
</script>

<template>
  <div class="agent-manager" :aria-busy="loading">
    <div v-if="loading && !snapshot" class="agent-manager__state">
      {{ t('pet.settings.loading') }}
    </div>
    <div v-else-if="errorMessage && !snapshot" class="agent-manager__state is-error">
      <CircleAlert :size="17" aria-hidden="true" />
      <span>{{ errorMessage }}</span>
      <button type="button" class="agent-manager__retry" @click="void loadSnapshot()">
        {{ t('pet.common.retry') }}
      </button>
    </div>
    <div v-else-if="snapshot" class="agent-manager__workspace">
      <section class="agent-manager__conversation">
        <PetChatHistoryPanel
          :pet-id="DEFAULT_PET_ID"
          :pet-name="snapshot.state.name || 'Kapi'"
          :agent="snapshot.agent"
          :project-name="projectName"
          :session-name="t('pet.agentManager.title')"
          :skills="selectedSkillReferences"
        />
      </section>

      <aside class="agent-manager__rail" :aria-label="t('pet.agentManager.controlsLabel')">
        <div class="agent-manager__identity">
          <div class="agent-manager__identity-mark"><Bot :size="17" aria-hidden="true" /></div>
          <div class="agent-manager__identity-copy">
            <strong>{{ t('pet.agentManager.title') }}</strong>
            <span :title="projectName || projectID">{{ projectName || t('pet.agentManager.projectRequired') }}</span>
          </div>
          <button
            type="button"
            class="agent-manager__icon-button"
            :aria-label="t('pet.agentManager.refreshAll')"
            :title="t('pet.agentManager.refreshAll')"
            :disabled="loading || commandBusy"
            @click="void refreshAll()"
          >
            <RefreshCw :size="15" :class="{ 'is-spinning': loading }" aria-hidden="true" />
          </button>
        </div>

        <div v-if="errorMessage" class="agent-manager__notice is-error" role="status">
          <CircleAlert :size="14" aria-hidden="true" />
          <span>{{ errorMessage }}</span>
        </div>

        <section v-if="pendingInteraction" class="agent-manager__section agent-manager__interaction" aria-live="polite">
          <div class="agent-manager__section-head">
            <h2><Shield :size="15" aria-hidden="true" />{{ t('pet.agentManager.interaction.title') }}</h2>
            <span class="agent-manager__live-dot" aria-hidden="true"></span>
          </div>
          <strong class="agent-manager__interaction-title">{{ interactionTitle(pendingInteraction) }}</strong>
          <p v-if="pendingInteraction.reason" class="agent-manager__muted">{{ pendingInteraction.reason }}</p>
          <code v-if="pendingInteraction.command" class="agent-manager__code">{{ pendingInteraction.command }}</code>
          <code v-if="pendingInteraction.cwd" class="agent-manager__code">{{ pendingInteraction.cwd }}</code>

          <div v-if="pendingInteraction.kind === 'approval'" class="agent-manager__button-stack">
            <button
              v-for="decision in interactionDecisionChoices"
              :key="decision"
              type="button"
              class="agent-manager__action"
              :class="{ 'is-primary': decision === 'accept' || decision === 'acceptForSession' }"
              :disabled="interactionBusy"
              @click="void resolveApproval(decision)"
            >
              <Check v-if="decision === 'accept' || decision === 'acceptForSession'" :size="14" aria-hidden="true" />
              <X v-else :size="14" aria-hidden="true" />
              {{ formatDecision(decision) }}
            </button>
          </div>

          <div v-else-if="pendingInteraction.kind === 'permission'" class="agent-manager__interaction-form">
            <label class="agent-manager__field">
              <span>{{ t('pet.agentManager.interaction.scope') }}</span>
              <select v-model="permissionScope" :disabled="interactionBusy">
                <option value="turn">{{ t('pet.agentManager.interaction.turn') }}</option>
                <option value="session">{{ t('pet.agentManager.interaction.session') }}</option>
              </select>
            </label>
            <div class="agent-manager__button-stack">
              <button
                v-for="decision in interactionDecisionChoices"
                :key="decision"
                type="button"
                class="agent-manager__action"
                :class="{ 'is-primary': decision === 'accept' }"
                :disabled="interactionBusy"
                @click="void resolvePermission(decision)"
              >
                <Check v-if="decision === 'accept'" :size="14" aria-hidden="true" />
                <X v-else :size="14" aria-hidden="true" />
                {{ formatDecision(decision) }}
              </button>
            </div>
          </div>

          <div v-else-if="pendingInteraction.kind === 'user_input'" class="agent-manager__interaction-form">
            <div v-for="question in pendingInteraction.questions" :key="question.id" class="agent-manager__question">
              <strong>{{ question.header || t('pet.agentManager.interaction.answer') }}</strong>
              <p>{{ question.question }}</p>
              <div v-if="question.options.length > 0" class="agent-manager__option-list">
                <label v-for="option in question.options" :key="option.label" class="agent-manager__option">
                  <input
                    type="checkbox"
                    :checked="answerValues(question.id).includes(option.label)"
                    :disabled="interactionBusy"
                    @change="toggleAnswer(question.id, option.label, $event)"
                  />
                  <span>{{ option.label }}<small v-if="option.description">{{ option.description }}</small></span>
                </label>
              </div>
              <input
                v-else
                :type="question.secret ? 'password' : 'text'"
                :value="answerValues(question.id)[0] ?? ''"
                :disabled="interactionBusy"
                @input="setFreeAnswer(question.id, $event)"
              />
              <input
                v-if="question.other"
                type="text"
                :placeholder="t('pet.agentManager.interaction.other')"
                :value="otherAnswerDrafts[question.id] ?? ''"
                :disabled="interactionBusy"
                @input="setOtherAnswer(question.id, $event)"
              />
            </div>
            <button type="button" class="agent-manager__action is-primary" :disabled="interactionBusy" @click="void resolveUserInput()">
              <SendHorizontal :size="14" aria-hidden="true" />
              {{ t('pet.agentManager.interaction.submit') }}
            </button>
          </div>

          <div v-else-if="pendingInteraction.kind === 'mcp_form'" class="agent-manager__interaction-form">
            <p v-if="pendingInteraction.message" class="agent-manager__muted">{{ pendingInteraction.message }}</p>
            <template v-if="mcpFields.length > 0">
              <label v-for="field in mcpFields" :key="field.name" class="agent-manager__field">
                <span>{{ field.title }}<em v-if="field.required">*</em></span>
                <small v-if="field.description">{{ field.description }}</small>
                <input
                  v-if="field.type === 'boolean'"
                  type="checkbox"
                  :checked="mcpFieldValue(field.name) === true"
                  :disabled="interactionBusy"
                  @change="setMcpFieldValue(field.name, $event)"
                />
                <select
                  v-else-if="field.options.length > 0"
                  :value="mcpFieldText(field.name)"
                  :disabled="interactionBusy"
                  @change="setMcpFieldValue(field.name, $event)"
                >
                  <option value="">{{ t('pet.agentManager.interaction.select') }}</option>
                  <option v-for="option in field.options" :key="option" :value="option">{{ option }}</option>
                </select>
                <textarea
                  v-else-if="field.type === 'object' || field.type === 'array'"
                  rows="2"
                  :placeholder="field.type === 'array' ? '[]' : '{}'"
                  :value="mcpFieldText(field.name)"
                  :disabled="interactionBusy"
                  @input="setMcpFieldValue(field.name, $event)"
                ></textarea>
                <input
                  v-else
                  :type="field.type === 'number' || field.type === 'integer' ? 'number' : 'text'"
                  :value="mcpFieldText(field.name)"
                  :disabled="interactionBusy"
                  @input="setMcpFieldValue(field.name, $event)"
                />
              </label>
            </template>
            <label v-else class="agent-manager__field">
              <span>{{ t('pet.agentManager.interaction.json') }}</span>
              <textarea v-model="mcpJsonDraft" rows="4" :disabled="interactionBusy"></textarea>
            </label>
            <div class="agent-manager__button-row">
              <button type="button" class="agent-manager__action is-primary" :disabled="interactionBusy" @click="void resolveMcp('accept')">
                <Check :size="14" aria-hidden="true" />{{ t('pet.agentManager.interaction.accept') }}
              </button>
              <button type="button" class="agent-manager__action" :disabled="interactionBusy" @click="void resolveMcp('decline')">
                <X :size="14" aria-hidden="true" />{{ t('pet.agentManager.interaction.decline') }}
              </button>
            </div>
          </div>
          <div v-if="interactionError" class="agent-manager__inline-error" role="alert">{{ interactionError }}</div>
        </section>

        <section class="agent-manager__section">
          <div class="agent-manager__section-head">
            <h2><GitCompare :size="15" aria-hidden="true" />{{ t('pet.agentManager.commands.title') }}</h2>
            <span v-if="activeRequestId" class="agent-manager__active-label">{{ liveStatusText }}</span>
          </div>
          <label class="agent-manager__field">
            <span>{{ t('pet.agentManager.commands.reviewTarget') }}</span>
            <select v-model="reviewTarget" :disabled="commandBusy || !projectReady">
              <option value="uncommitted">{{ t('pet.agentManager.commands.uncommitted') }}</option>
              <option value="base">{{ t('pet.agentManager.commands.base') }}</option>
              <option value="commit">{{ t('pet.agentManager.commands.commit') }}</option>
              <option value="custom">{{ t('pet.agentManager.commands.custom') }}</option>
            </select>
          </label>
          <input
            v-if="reviewNeedsReference"
            v-model="reviewReference"
            class="agent-manager__text-input"
            type="text"
            :placeholder="reviewReferencePlaceholder"
            :disabled="commandBusy || !projectReady"
          />
          <button type="button" class="agent-manager__action is-primary" :disabled="commandBusy || !projectReady || (reviewNeedsReference && !reviewReference.trim())" @click="void runReview()">
            <GitCompare :size="14" aria-hidden="true" />{{ t('pet.agentManager.commands.runReview') }}
          </button>
          <div class="agent-manager__button-row">
            <button type="button" class="agent-manager__action" :disabled="commandBusy || !projectReady || Boolean(activeRequestId)" @click="void runCompact()">
              <Minimize2 :size="14" aria-hidden="true" />{{ t('pet.agentManager.commands.compact') }}
            </button>
            <button type="button" class="agent-manager__action is-danger" :disabled="commandBusy || !projectReady || !activeRequestId" @click="void runInterrupt()">
              <Square :size="13" aria-hidden="true" />{{ t('pet.agentManager.commands.interrupt') }}
            </button>
          </div>
          <div class="agent-manager__steer-row">
            <input v-model="steerInput" class="agent-manager__text-input" type="text" :placeholder="t('pet.agentManager.commands.steerPlaceholder')" :disabled="commandBusy || !projectReady || !activeRequestId" />
            <button type="button" class="agent-manager__icon-button is-accent" :aria-label="t('pet.agentManager.commands.steer')" :title="t('pet.agentManager.commands.steer')" :disabled="commandBusy || !projectReady || !activeRequestId || !steerInput.trim()" @click="void runSteer()">
              <SendHorizontal :size="15" aria-hidden="true" />
            </button>
          </div>
          <div v-if="commandNotice" class="agent-manager__notice" role="status">{{ commandNotice }}</div>
          <div v-if="commandError" class="agent-manager__inline-error" role="alert">{{ commandError }}</div>
        </section>

        <section class="agent-manager__section">
          <div class="agent-manager__section-head">
            <h2><Check :size="15" aria-hidden="true" />{{ t('pet.agentManager.skills.title') }}</h2>
            <button type="button" class="agent-manager__icon-button" :aria-label="t('pet.agentManager.skills.refresh')" :title="t('pet.agentManager.skills.refresh')" :disabled="skillsLoading || !projectReady" @click="retrySkills">
              <RefreshCw :size="14" :class="{ 'is-spinning': skillsLoading }" aria-hidden="true" />
            </button>
          </div>
          <div class="agent-manager__section-meta">
            <span>{{ t('pet.agentManager.skills.selected', { count: selectedSkillReferences.length }) }}</span>
            <span v-if="skillsLoading">{{ t('pet.agentManager.skills.loading') }}</span>
          </div>
          <div v-if="skillsError" class="agent-manager__inline-error" role="alert">{{ skillsError }}</div>
          <div v-else-if="filteredSkills.length === 0 && !skillsLoading" class="agent-manager__muted">{{ t('pet.agentManager.skills.empty') }}</div>
          <label v-for="(skill, index) in filteredSkills" :key="skill.path" class="agent-manager__skill">
            <input v-model="selectedSkillKeys" type="checkbox" :value="skill.path" :disabled="!skill.enabled" :id="`agent-skill-${index}`" />
            <span :title="skill.description || skill.shortDescription || skill.path">
              <strong>{{ skill.name }}</strong>
              <small>{{ skill.shortDescription || skill.description || skill.path }}</small>
            </span>
          </label>
          <div v-if="skillErrors.length > 0" class="agent-manager__muted">{{ t('pet.agentManager.skills.partial', { count: skillErrors.length }) }}</div>
        </section>

        <section class="agent-manager__section">
          <div class="agent-manager__section-head">
            <h2><Bot :size="15" aria-hidden="true" />{{ t('pet.agentManager.models.title') }}</h2>
          </div>
          <div v-if="configuredModelID" class="agent-manager__model">
            <strong>{{ configuredModelID }}</strong>
            <span v-if="configuredReasoningEffort">{{ t('pet.agentManager.models.reasoning', { value: configuredReasoningEffort }) }}</span>
            <small>{{ t('pet.agentManager.models.configuredReadonly') }}</small>
            <small v-if="configuredPlatform !== 'codex'">{{ t('pet.agentManager.models.platformUnsupported', { platform: configuredPlatform || 'unknown' }) }}</small>
          </div>
          <div v-else class="agent-manager__muted">{{ t('pet.agentManager.models.notConfigured') }}</div>
        </section>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.agent-manager {
  --agent-ink: var(--mac-text, #1d1d1f);
  --agent-muted: var(--mac-text-secondary, #6e6e73);
  --agent-line: var(--mac-border, rgba(15, 23, 42, 0.12));
  --agent-surface: var(--mac-surface, #fff);
  --agent-accent: var(--mac-accent, #0a84ff);
  display: flex;
  width: 100%;
  height: 100%;
  min-width: 0;
  min-height: 0;
  flex: 1 1 auto;
  box-sizing: border-box;
  overflow: hidden;
  color: var(--agent-ink);
  font-family: var(--mac-font, system-ui, sans-serif);
}

.agent-manager__state {
  display: flex;
  width: 100%;
  height: 100%;
  min-height: 0;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 16px;
  color: var(--agent-muted);
  font-size: 12px;
  line-height: 1.5;
  text-align: center;
}

.agent-manager__state.is-error,
.agent-manager__inline-error {
  color: #b23d3d;
}

.agent-manager__retry,
.agent-manager__action,
.agent-manager__icon-button,
.agent-manager__rail select,
.agent-manager__rail input,
.agent-manager__rail textarea {
  font: inherit;
}

.agent-manager__retry {
  border: 1px solid color-mix(in srgb, #b23d3d 45%, var(--agent-line));
  border-radius: 7px;
  margin: 0;
  padding: 6px 10px;
  background: color-mix(in srgb, #b23d3d 10%, transparent);
  color: #b23d3d;
  cursor: pointer;
  font-size: 11px;
}

.agent-manager__workspace {
  display: grid;
  width: 100%;
  height: 100%;
  min-width: 0;
  min-height: 0;
  grid-template-columns: minmax(0, 1fr) 304px;
}

.agent-manager__conversation {
  display: flex;
  min-width: 0;
  min-height: 0;
  background: color-mix(in srgb, var(--agent-surface) 42%, transparent);
}

.agent-manager__conversation > * {
  min-width: 0;
  min-height: 0;
  flex: 1 1 auto;
}

.agent-manager__rail {
  min-width: 0;
  min-height: 0;
  overflow-y: auto;
  border-left: 1px solid var(--agent-line);
  background: color-mix(in srgb, var(--agent-surface) 76%, transparent);
  scrollbar-width: thin;
}

.agent-manager__identity {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 9px;
  padding: 14px 14px 12px;
  border-bottom: 1px solid var(--agent-line);
}

.agent-manager__identity-mark {
  display: grid;
  width: 30px;
  height: 30px;
  flex: 0 0 30px;
  place-items: center;
  border: 1px solid color-mix(in srgb, var(--agent-accent) 28%, var(--agent-line));
  border-radius: 9px;
  background: color-mix(in srgb, var(--agent-accent) 10%, transparent);
  color: var(--agent-accent);
}

.agent-manager__identity-copy {
  display: flex;
  min-width: 0;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 2px;
}

.agent-manager__identity-copy strong {
  font-size: 13px;
  line-height: 18px;
}

.agent-manager__identity-copy span {
  overflow: hidden;
  color: var(--agent-muted);
  font-size: 10px;
  line-height: 14px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.agent-manager__icon-button {
  display: inline-flex;
  width: 30px;
  height: 30px;
  min-width: 30px;
  min-height: 30px;
  flex: 0 0 30px;
  align-items: center;
  justify-content: center;
  box-sizing: border-box;
  border: 1px solid var(--agent-line);
  border-radius: 7px;
  margin: 0;
  padding: 0;
  background: transparent;
  color: var(--agent-muted);
  cursor: pointer;
}

.agent-manager__icon-button:hover:not(:disabled),
.agent-manager__icon-button.is-accent {
  border-color: color-mix(in srgb, var(--agent-accent) 45%, var(--agent-line));
  color: var(--agent-accent);
}

.agent-manager__icon-button:disabled,
.agent-manager__action:disabled,
.agent-manager__retry:disabled {
  cursor: not-allowed;
  opacity: 0.45;
}

.agent-manager__section {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 8px;
  padding: 14px;
  border-bottom: 1px solid var(--agent-line);
}

.agent-manager__section-head,
.agent-manager__section-meta,
.agent-manager__button-row,
.agent-manager__steer-row {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 7px;
}

.agent-manager__section-head {
  justify-content: space-between;
}

.agent-manager__section-head h2 {
  display: inline-flex;
  min-width: 0;
  align-items: center;
  gap: 6px;
  margin: 0;
  font-size: 12px;
  line-height: 17px;
}

.agent-manager__section-meta {
  justify-content: space-between;
  color: var(--agent-muted);
  font-size: 10px;
  line-height: 14px;
}

.agent-manager__active-label {
  min-width: 0;
  overflow: hidden;
  color: var(--agent-accent);
  font-size: 10px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.agent-manager__field {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
  color: var(--agent-muted);
  font-size: 10px;
  line-height: 14px;
}

.agent-manager__field em {
  margin-left: 2px;
  color: #b23d3d;
  font-style: normal;
}

.agent-manager__field small {
  color: var(--agent-muted);
  font-size: 10px;
  line-height: 14px;
  overflow-wrap: anywhere;
}

.agent-manager__rail select,
.agent-manager__text-input,
.agent-manager__field input:not([type='checkbox']),
.agent-manager__field textarea {
  width: 100%;
  min-width: 0;
  box-sizing: border-box;
  border: 1px solid var(--agent-line);
  border-radius: 7px;
  padding: 7px 8px;
  outline: none;
  background: color-mix(in srgb, var(--agent-surface) 72%, transparent);
  color: var(--agent-ink);
  font-size: 11px;
  line-height: 15px;
}

.agent-manager__field textarea {
  min-height: 55px;
  resize: vertical;
  white-space: pre-wrap;
}

.agent-manager__rail select:focus,
.agent-manager__text-input:focus,
.agent-manager__field input:focus,
.agent-manager__field textarea:focus {
  border-color: color-mix(in srgb, var(--agent-accent) 54%, var(--agent-line));
}

.agent-manager__action {
  display: inline-flex;
  min-width: 0;
  min-height: 30px;
  flex: 1 1 auto;
  align-items: center;
  justify-content: center;
  gap: 6px;
  box-sizing: border-box;
  border: 1px solid var(--agent-line);
  border-radius: 7px;
  margin: 0;
  padding: 6px 9px;
  background: transparent;
  color: var(--agent-ink);
  cursor: pointer;
  font-size: 11px;
  line-height: 15px;
  text-align: center;
}

.agent-manager__action:hover:not(:disabled) {
  border-color: color-mix(in srgb, var(--agent-accent) 48%, var(--agent-line));
}

.agent-manager__action.is-primary {
  border-color: color-mix(in srgb, var(--agent-accent) 55%, var(--agent-line));
  background: color-mix(in srgb, var(--agent-accent) 12%, transparent);
  color: color-mix(in srgb, var(--agent-accent) 72%, var(--agent-ink));
}

.agent-manager__action.is-danger {
  border-color: color-mix(in srgb, #b23d3d 40%, var(--agent-line));
  color: #a33c3c;
}

.agent-manager__button-stack {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 6px;
}

.agent-manager__button-row > .agent-manager__action {
  min-width: 0;
}

.agent-manager__steer-row .agent-manager__text-input {
  flex: 1 1 auto;
}

.agent-manager__text-input {
  height: 30px;
}

.agent-manager__notice,
.agent-manager__inline-error {
  display: flex;
  min-width: 0;
  align-items: flex-start;
  gap: 6px;
  font-size: 10px;
  line-height: 14px;
  overflow-wrap: anywhere;
}

.agent-manager__notice {
  color: #2f8a58;
}

.agent-manager__notice.is-error {
  padding: 8px 12px;
  border-bottom: 1px solid var(--agent-line);
  color: #b23d3d;
}

.agent-manager__muted {
  margin: 0;
  color: var(--agent-muted);
  font-size: 10px;
  line-height: 15px;
  overflow-wrap: anywhere;
}

.agent-manager__skill {
  display: flex;
  min-width: 0;
  align-items: flex-start;
  gap: 8px;
  padding: 3px 0;
  cursor: pointer;
}

.agent-manager__skill > span {
  display: flex;
  min-width: 0;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 2px;
}

.agent-manager__skill strong {
  overflow: hidden;
  font-size: 11px;
  line-height: 15px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.agent-manager__skill small {
  overflow: hidden;
  color: var(--agent-muted);
  font-size: 10px;
  line-height: 14px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.agent-manager__model {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 3px;
  padding: 2px 0;
}

.agent-manager__model strong,
.agent-manager__interaction-title {
  font-size: 11px;
  line-height: 15px;
  overflow-wrap: anywhere;
}

.agent-manager__model code,
.agent-manager__code {
  display: block;
  min-width: 0;
  overflow: hidden;
  color: var(--agent-muted);
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
  font-size: 10px;
  line-height: 14px;
  text-overflow: ellipsis;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.agent-manager__model span,
.agent-manager__model small {
  color: var(--agent-muted);
  font-size: 10px;
  line-height: 14px;
}

.agent-manager__interaction {
  border-left: 3px solid var(--agent-accent);
  background: color-mix(in srgb, var(--agent-accent) 5%, transparent);
}

.agent-manager__interaction-title {
  color: var(--agent-ink);
}

.agent-manager__interaction-form,
.agent-manager__question,
.agent-manager__option-list {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 7px;
}

.agent-manager__question {
  padding-bottom: 4px;
  border-bottom: 1px solid color-mix(in srgb, var(--agent-line) 70%, transparent);
}

.agent-manager__question:last-of-type {
  border-bottom: 0;
}

.agent-manager__question strong {
  font-size: 10px;
  line-height: 14px;
}

.agent-manager__question p {
  margin: 0;
  color: var(--agent-muted);
  font-size: 10px;
  line-height: 15px;
  overflow-wrap: anywhere;
}

.agent-manager__option {
  display: flex;
  min-width: 0;
  align-items: flex-start;
  gap: 7px;
  color: var(--agent-ink);
  cursor: pointer;
  font-size: 10px;
  line-height: 14px;
}

.agent-manager__option span {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
  overflow-wrap: anywhere;
}

.agent-manager__option small {
  color: var(--agent-muted);
  font-size: 9px;
  line-height: 13px;
}

.agent-manager__live-dot {
  width: 6px;
  height: 6px;
  flex: 0 0 6px;
  border-radius: 50%;
  background: var(--agent-accent);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--agent-accent) 14%, transparent);
}

.agent-manager__icon-button .is-spinning,
.agent-manager__rail .is-spinning {
  animation: agent-manager-spin 0.8s linear infinite;
}

@keyframes agent-manager-spin {
  to { transform: rotate(360deg); }
}

@media (max-width: 900px) {
  .agent-manager {
    overflow: auto;
  }

  .agent-manager__workspace {
    display: flex;
    height: auto;
    min-height: 100%;
    flex-direction: column;
  }

  .agent-manager__conversation {
    height: min(72vh, 760px);
    min-height: 540px;
    flex: 0 0 auto;
  }

  .agent-manager__rail {
    overflow: visible;
    border-top: 1px solid var(--agent-line);
    border-left: 0;
  }
}

@media (prefers-reduced-motion: reduce) {
  .agent-manager__icon-button .is-spinning,
  .agent-manager__rail .is-spinning {
    animation: none;
  }
}
</style>
