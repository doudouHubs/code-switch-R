<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowDown, RefreshCw } from '@lucide/vue'
import { Call, Events } from '../../wails-runtime-compat'
import PetChat from './PetChat.vue'
import {
  buildPetChatPersona,
  cleanPetChatHistoryText,
  normalizePetChatTimestamp,
  type PetChatLifecycleEvent,
  type PetChatOutgoingMessage
} from './petChatProtocol'
import type { AgentSkillReference } from '../Agent/agentTypes'
import type { PetAgentConfig } from './petTypes'

interface PetChatHistoryPanelProps {
  petId?: string
  petName?: string
  agent?: PetAgentConfig | null
  projectName?: string
  sessionName?: string
  skills?: AgentSkillReference[]
}

interface PetChatHistoryImage {
  id: string
  previewUrl: string
}

interface PetChatHistoryMessage {
  id: string
  role: 'user' | 'assistant'
  content: string
  images: PetChatHistoryImage[]
  createdAt: number
  status: string
}

interface LoadHistoryOptions {
  showLoading?: boolean
}

const props = withDefaults(defineProps<PetChatHistoryPanelProps>(), {
  petId: 'default',
  petName: 'Kapi',
  agent: null,
  projectName: '',
  sessionName: '',
  skills: () => []
})
const { t, locale } = useI18n()

const PET_AI_METHOD = 'codeswitch/services.PetAIAPIService.GetChatHistory'
const PET_AI_EVENT = 'pet.ai'
const PET_CHAT_IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'] as const
const PET_HISTORY_REFRESH_RETRIES = 3

const messages = ref<PetChatHistoryMessage[]>([])
const loading = ref(false)
const errorMessage = ref('')
const messagesRef = ref<HTMLElement | null>(null)
const showJumpToLatest = ref(false)

let requestGeneration = 0
let activeCall: ReturnType<typeof Call.ByName> | null = null
let activeRequestId = ''
  let activeAssistantMessageId = ''
  let lastHistoryErrorWasInFlight = false
  let lastLocalTerminalRequestId = ''
  let stopChatEvent: (() => void) | null = null

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function errorText(error: unknown): string {
  if (error instanceof Error) return error.message
  if (typeof error === 'string') return error
  try {
    return JSON.stringify(error)
  } catch {
    return String(error)
  }
}

function isRequestInFlightError(error: unknown): boolean {
  return /PET_AI_REQUEST_IN_FLIGHT|request\s+in\s+flight|请求.*进行中/i.test(errorText(error))
}

function eventPayload(value: unknown): Record<string, unknown> {
  let sourceValue: unknown = value
  if (Array.isArray(sourceValue) && sourceValue.length === 1) sourceValue = sourceValue[0]
  if (typeof sourceValue === 'string' && sourceValue.trim()) {
    try {
      sourceValue = JSON.parse(sourceValue)
    } catch {
      return {}
    }
  }
  const source = isRecord(sourceValue) ? sourceValue : {}
  const data = source.data
  if (Array.isArray(data) && data.length === 1 && isRecord(data[0])) return data[0]
  if (typeof data === 'string' && data.trim()) {
    try {
      const parsed = JSON.parse(data)
      if (isRecord(parsed)) return parsed
    } catch {
      return source
    }
  }
  return isRecord(data) ? data : source
}

function normalizeImages(value: unknown, messageIndex: number): PetChatHistoryImage[] {
  if (!Array.isArray(value)) return []
  return value.flatMap((candidate, imageIndex) => {
    if (!isRecord(candidate)) return []
    const data = stringValue(candidate.data).trim()
    const mediaType = stringValue(candidate.mediaType).trim().toLowerCase()
    if (!data || !(PET_CHAT_IMAGE_TYPES as readonly string[]).includes(mediaType)) return []
    return [{
      id: `history-${messageIndex}-${imageIndex}`,
      previewUrl: `data:${mediaType};base64,${data}`
    }]
  })
}

function normalizeMessage(value: unknown, index: number): PetChatHistoryMessage | null {
  if (!isRecord(value)) return null
  const role = stringValue(value.role).trim().toLowerCase()
  if (role !== 'user' && role !== 'assistant') return null
  const content = cleanPetChatHistoryText(stringValue(value.content))
  const images = normalizeImages(value.images, index)
  if (!content && images.length === 0) return null
  const createdAt = normalizePetChatTimestamp(value.createdAt)
  return {
    id: stringValue(value.id).trim() || `history-${index}-${createdAt}`,
    role,
    content,
    images,
    createdAt,
    status: stringValue(value.status).trim().toLowerCase()
  }
}

function formatDate(timestamp: number): string {
  if (!timestamp) return t('pet.common.unknownDate')
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(timestamp))
}

function roleLabel(role: PetChatHistoryMessage['role']): string {
  return role === 'user' ? t('pet.chatHistory.you') : t('pet.chatHistory.pet')
}

function statusLabel(status: string, role: PetChatHistoryMessage['role']): string {
  if (role === 'assistant' && status === 'queued') return t('pet.chat.status.queued')
  if (role === 'assistant' && (status === 'streaming' || status === 'pending')) return t('pet.chat.status.replying')
  if (role === 'assistant' && status === 'interaction') return t('pet.chat.status.waitingForInput')
  if (status === 'interrupted' || status === 'cancelled') return t('pet.chatHistory.cancelled')
  if (status === 'failed' || status === 'error') return t('pet.chatHistory.failed')
  return ''
}

function cancelActiveCall(): void {
  const call = activeCall
  activeCall = null
  if (!call) return
  try {
    // 切换宠物或卸载页签时取消慢速 thread/read，避免旧响应覆盖新宠物的历史。
    void call.cancel().catch(() => undefined)
  } catch {
    // bridge 已关闭时取消可能抛错，但不能阻塞设置页卸载。
  }
}

function isNearBottom(): boolean {
  const element = messagesRef.value
  if (!element) return true
  return element.scrollHeight - element.scrollTop - element.clientHeight <= 72
}

function scheduleScroll(shouldScroll: boolean): void {
  void nextTick(() => {
    const element = messagesRef.value
    if (!element || !shouldScroll) {
      showJumpToLatest.value = !isNearBottom()
      return
    }
    element.scrollTop = element.scrollHeight
    showJumpToLatest.value = false
  })
}

function replaceMessages(nextMessages: PetChatHistoryMessage[], forceScroll = false): void {
  const shouldScroll = forceScroll || isNearBottom()
  messages.value = nextMessages
  scheduleScroll(shouldScroll)
}

function appendMessage(message: PetChatHistoryMessage, forceScroll = false): void {
  const shouldScroll = forceScroll || isNearBottom()
  messages.value = [...messages.value, message]
  scheduleScroll(shouldScroll)
}

function patchMessage(messageId: string, patch: Partial<PetChatHistoryMessage>): void {
  const index = messages.value.findIndex((message) => message.id === messageId)
  if (index < 0) return
  const shouldScroll = isNearBottom()
  const nextMessages = [...messages.value]
  nextMessages[index] = { ...nextMessages[index], ...patch }
  messages.value = nextMessages
  scheduleScroll(shouldScroll)
}

function createAssistantMessage(requestId: string, text: string, status: string): PetChatHistoryMessage {
  return {
    id: `${requestId}:assistant`,
    role: 'assistant',
    content: text || t('pet.chat.status.replying'),
    images: [],
    createdAt: Date.now(),
    status
  }
}

function ensureAssistantMessage(event: PetChatLifecycleEvent): string {
  if (activeAssistantMessageId) return activeAssistantMessageId
  const initialStatus = event.type === 'queued' ? 'queued' : 'streaming'
  const message = createAssistantMessage(
    event.requestId,
    event.text || '...',
    initialStatus
  )
  activeAssistantMessageId = message.id
  appendMessage(message, true)
  return message.id
}

async function loadHistory(options: LoadHistoryOptions = {}): Promise<boolean> {
  const generation = ++requestGeneration
  const requestIdAtStart = activeRequestId
  cancelActiveCall()
  const showLoading = options.showLoading !== false
  if (showLoading) loading.value = true
  errorMessage.value = ''
  lastHistoryErrorWasInFlight = false

  const call = Call.ByName(PET_AI_METHOD, {
    petId: props.petId,
    projectId: props.agent?.projectId?.trim() ?? '',
    persona: buildPetChatPersona(props.agent?.systemPrompt, props.petName)
  })
  activeCall = call
  try {
    const rawResult = await call
    // 历史读取是慢速边界；如果期间已经开始新请求，旧结果不能覆盖刚追加的乐观消息。
    if (generation !== requestGeneration || activeRequestId !== requestIdAtStart) return false
    const result = isRecord(rawResult) ? rawResult : {}
    const rawMessages = Array.isArray(result.messages) ? result.messages : []
    const normalized = rawMessages
      .map((message, index) => normalizeMessage(message, index))
      .filter((message): message is PetChatHistoryMessage => message !== null)
    replaceMessages(normalized, true)
    return true
  } catch (error) {
    if (generation !== requestGeneration || activeRequestId !== requestIdAtStart) return false
    lastHistoryErrorWasInFlight = isRequestInFlightError(error)
    console.warn('[Pet] chat history read failed:', error)
    errorMessage.value = t('pet.chatHistory.loadFailed')
    // 已有对话不能因为一次刷新失败被清空；保留当前 transcript，等待用户重试。
    return false
  } finally {
    if (activeCall === call) activeCall = null
    if (generation === requestGeneration && showLoading) loading.value = false
  }
}

async function refreshHistoryAfterCompletion(requestId: string): Promise<void> {
  for (let attempt = 0; attempt < PET_HISTORY_REFRESH_RETRIES; attempt += 1) {
    if (attempt > 0) {
      await new Promise<void>((resolve) => window.setTimeout(resolve, 150 * attempt))
    }
    const loaded = await loadHistory({ showLoading: false })
    if (loaded || !lastHistoryErrorWasInFlight) return
    // requestId 只用于避免旧请求的重试在新请求开始后继续改写页面。
    if (activeRequestId && activeRequestId !== requestId) return
  }
}

function handleOutgoingMessage(payload: PetChatOutgoingMessage): void {
  activeRequestId = payload.requestId
  activeAssistantMessageId = ''
  lastLocalTerminalRequestId = ''
  appendMessage({
    id: `${payload.requestId}:user`,
    role: 'user',
    content: payload.text,
    images: payload.images.map((image) => ({ id: image.id, previewUrl: image.previewUrl })),
    createdAt: payload.createdAt,
    status: 'pending'
  }, true)
  // 这个占位只存在于当前页面内，表示消息已经被共享 Agent 接收；真实模型
  // 文本到达后会覆盖它，刷新历史时不会把“...”误当成持久化回复。
  const assistant = createAssistantMessage(payload.requestId, '...', 'streaming')
  activeAssistantMessageId = assistant.id
  appendMessage(assistant, true)
}

function handleLifecycle(event: PetChatLifecycleEvent): void {
  if (!activeRequestId || event.requestId !== activeRequestId) return

  if (event.type === 'queued' || event.type === 'started' || event.type === 'progress' || event.type === 'delta') {
    const messageId = ensureAssistantMessage(event)
    patchMessage(messageId, {
      content: event.text || '...',
      // 排队不是流式处理中；保留这个状态，历史气泡才能准确表达当前项目 FIFO 的位置。
      status: event.type === 'queued' ? 'queued' : 'streaming'
    })
    return
  }

  if (event.type === 'interaction') {
    const messageId = ensureAssistantMessage(event)
    patchMessage(messageId, {
      content: event.text || t('pet.chat.status.waitingForInput'),
      status: 'interaction'
    })
    return
  }

  if (event.type === 'completed') {
    const messageId = ensureAssistantMessage(event)
    patchMessage(messageId, {
      content: event.text || t('pet.chat.message.noText'),
      status: ''
    })
    const requestId = activeRequestId
    lastLocalTerminalRequestId = requestId
    activeRequestId = ''
    activeAssistantMessageId = ''
    void refreshHistoryAfterCompletion(requestId)
    return
  }

  const messageId = activeAssistantMessageId
  if (messageId) {
    patchMessage(messageId, {
      content: event.text,
      status: event.type === 'cancelled' ? 'cancelled' : 'failed'
    })
  } else {
    patchMessage(`${event.requestId}:user`, {
      status: event.type === 'cancelled' ? 'cancelled' : 'failed'
    })
  }
  const requestId = activeRequestId
  lastLocalTerminalRequestId = requestId
  activeRequestId = ''
  activeAssistantMessageId = ''
  void refreshHistoryAfterCompletion(requestId)
}

function handleAgentEvent(value: unknown): void {
  const payload = eventPayload(value)
  if (!['completed', 'failed', 'cancelled'].includes(String(payload.type ?? '').toLowerCase())) return
  const source = String(payload.source ?? '').toLowerCase()
  if (source !== 'channel' && source !== 'manager') return
  const projectId = props.agent?.projectId?.trim() ?? ''
  if (!projectId || String(payload.projectId ?? '').trim() !== projectId) return
  const requestId = String(payload.requestId ?? '').trim()
  // 本地 PetChat 已经在 lifecycle 回调中刷新过自己的 turn；只为 review 等由
  // Agent 控制栏直接启动的 manager turn 补一次历史读取，避免每条普通消息重复打 IPC。
  if (source === 'manager' && requestId && requestId === lastLocalTerminalRequestId) return
  // 频道和管家共用同一 Codex thread；频道完成事件到达后只重新读取这个 thread，
  // 不在页面侧拼接第二份消息，避免事件合并、重连或重复广播造成重复气泡。
  void refreshHistoryAfterCompletion(requestId)
}

function handleHistoryScroll(): void {
  showJumpToLatest.value = !isNearBottom()
}

function scrollToLatest(): void {
  scheduleScroll(true)
}

watch(
  () => [props.petId, props.petName, props.agent?.projectId, props.agent?.projectFolder, props.agent?.systemPrompt],
  () => {
    activeRequestId = ''
    activeAssistantMessageId = ''
    void loadHistory()
  },
  { immediate: true }
)

onBeforeUnmount(() => {
  requestGeneration += 1
  activeRequestId = ''
  activeAssistantMessageId = ''
  cancelActiveCall()
  stopChatEvent?.()
  stopChatEvent = null
})

onMounted(() => {
  stopChatEvent = Events.On(PET_AI_EVENT, (event) => handleAgentEvent(event.data))
})
</script>

<template>
  <section class="pet-chat-history-panel" :aria-label="t('pet.chatHistory.title')">
    <div class="pet-chat-history-panel__conversation">
      <button
        type="button"
        class="pet-chat-history-panel__refresh"
        :aria-label="t('pet.common.refresh')"
        :title="t('pet.common.refresh')"
        :disabled="loading || Boolean(activeRequestId)"
        @click="void loadHistory()"
      >
        <RefreshCw :size="15" :class="{ 'is-spinning': loading }" aria-hidden="true" />
      </button>
      <div
        ref="messagesRef"
        class="pet-chat-history-panel__messages"
        role="log"
        :aria-label="t('pet.chat.aria.messages')"
        :aria-busy="loading"
        @scroll="handleHistoryScroll"
      >
        <div v-if="loading && messages.length === 0" class="pet-chat-history-panel__state">
          {{ t('pet.chatHistory.loading') }}
        </div>
        <div v-else-if="errorMessage && messages.length === 0" class="pet-chat-history-panel__state is-error">
          <span>{{ errorMessage }}</span>
          <button type="button" class="pet-chat-history-panel__retry" @click="void loadHistory()">
            {{ t('pet.common.retry') }}
          </button>
        </div>
        <div v-else-if="messages.length === 0" class="pet-chat-history-panel__state">
          {{ t('pet.chatHistory.empty') }}
        </div>
        <template v-else>
          <article
            v-for="message in messages"
            :key="message.id"
            :class="[
              'pet-chat-history-panel__message',
              `is-${message.role}`,
              { 'is-live': message.status === 'queued' || message.status === 'streaming' || message.status === 'interaction' || message.status === 'pending' }
            ]"
          >
            <div class="pet-chat-history-panel__message-meta">
              <strong>{{ roleLabel(message.role) }}</strong>
              <time v-if="message.createdAt" :datetime="new Date(message.createdAt).toISOString()">
                {{ formatDate(message.createdAt) }}
              </time>
              <span v-if="statusLabel(message.status, message.role)" class="pet-chat-history-panel__status">
                {{ statusLabel(message.status, message.role) }}
              </span>
            </div>
            <p v-if="message.content" class="pet-chat-history-panel__content">{{ message.content }}</p>
            <div v-if="message.images.length > 0" class="pet-chat-history-panel__images">
              <img
                v-for="image in message.images"
                :key="image.id"
                :src="image.previewUrl"
                :alt="t('pet.chat.attachments.imageAlt')"
                class="pet-chat-history-panel__image"
              />
            </div>
          </article>
        </template>
      </div>

      <button
        v-if="showJumpToLatest"
        type="button"
        class="pet-chat-history-panel__jump"
        :aria-label="t('pet.chatHistory.jumpToLatest')"
        :title="t('pet.chatHistory.jumpToLatest')"
        @click="scrollToLatest"
      >
        <ArrowDown :size="15" :stroke-width="2" aria-hidden="true" />
      </button>
    </div>

    <div v-if="errorMessage && messages.length > 0" class="pet-chat-history-panel__inline-error" role="status">
      <span>{{ errorMessage }}</span>
      <button type="button" class="pet-chat-history-panel__retry" @click="void loadHistory()">
        {{ t('pet.common.retry') }}
      </button>
    </div>

      <PetChat
        variant="history"
        :show-close="false"
        :pet-id="props.petId"
        :pet-name="props.petName"
        :agent="props.agent"
        :project-name="props.projectName"
        :session-name="props.sessionName"
        :skills="props.skills"
      @message-sent="handleOutgoingMessage"
      @lifecycle="handleLifecycle"
    />
  </section>
</template>

<style scoped>
.pet-chat-history-panel {
  --history-ink: var(--settings-ink, var(--mac-text, #1d1d1f));
  --history-muted: var(--settings-muted, var(--mac-text-secondary, #6e6e73));
  --history-line: var(--settings-line, var(--mac-border, rgba(15, 23, 42, 0.12)));
  --history-surface: var(--settings-surface, var(--mac-surface, #fff));
  --history-accent: var(--mac-accent, #0a84ff);
  display: flex;
  width: 100%;
  height: 100%;
  min-height: 0;
  min-width: 0;
  flex-direction: column;
  overflow: hidden;
  color: var(--history-ink);
}

.pet-chat-history-panel__refresh {
  position: absolute;
  top: 16px;
  right: 22px;
  display: inline-flex;
  width: 30px;
  height: 30px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--history-line);
  border-radius: 7px;
  background: var(--history-surface);
  color: var(--history-muted);
  cursor: pointer;
  z-index: 2;
}

.pet-chat-history-panel__refresh:hover:not(:disabled),
.pet-chat-history-panel__jump:hover {
  border-color: color-mix(in srgb, var(--history-accent) 45%, var(--history-line));
  color: var(--history-accent);
}

.pet-chat-history-panel__refresh:disabled {
  cursor: wait;
  opacity: 0.55;
}

.pet-chat-history-panel__refresh .is-spinning {
  animation: pet-chat-history-spin 0.8s linear infinite;
}

.pet-chat-history-panel__conversation {
  position: relative;
  display: flex;
  width: 100%;
  min-height: 0;
  flex: 1 1 0;
  flex-direction: column;
  overflow: hidden;
  background: color-mix(in srgb, var(--history-surface) 34%, transparent);
}

.pet-chat-history-panel__messages {
  display: flex;
  width: 100%;
  min-height: 0;
  flex: 1 1 0;
  flex-direction: column;
  align-items: stretch;
  gap: 14px;
  overflow-y: auto;
  padding: 24px clamp(20px, 4vw, 64px) 28px;
  scrollbar-width: thin;
}

.pet-chat-history-panel__state {
  display: flex;
  min-height: 180px;
  flex: 1 1 auto;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 16px;
  color: var(--history-muted);
  font-size: 12px;
  line-height: 17px;
  text-align: center;
}

.pet-chat-history-panel__state.is-error {
  color: #b23d3d;
}

.pet-chat-history-panel__message {
  display: flex;
  width: min(100%, 720px);
  min-width: 0;
  max-width: 100%;
  flex: 0 0 auto;
  flex-direction: column;
  align-self: flex-start;
  align-items: flex-start;
  gap: 5px;
}

.pet-chat-history-panel__message.is-user {
  margin-left: auto;
  margin-right: 0;
  align-self: flex-end;
  align-items: flex-end;
}

.pet-chat-history-panel__message-meta {
  display: flex;
  width: 100%;
  box-sizing: border-box;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 5px 8px;
  padding: 0 5px;
  color: var(--history-muted);
  font-size: 10px;
  line-height: 14px;
}

.pet-chat-history-panel__message.is-user .pet-chat-history-panel__message-meta {
  justify-content: flex-end;
}

.pet-chat-history-panel__message-meta strong {
  color: var(--history-ink);
  font-size: 11px;
}

.pet-chat-history-panel__status {
  color: var(--history-accent);
}

.pet-chat-history-panel__message.is-user .pet-chat-history-panel__status {
  color: #a24141;
}

.pet-chat-history-panel__content {
  width: fit-content;
  max-width: 100%;
  box-sizing: border-box;
  margin: 0;
  border: 1px solid var(--history-line);
  border-radius: 14px 14px 14px 4px;
  padding: 10px 13px;
  background: var(--history-surface);
  color: var(--history-ink);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-size: 12px;
  line-height: 18px;
}

.pet-chat-history-panel__message.is-user .pet-chat-history-panel__content {
  border-color: color-mix(in srgb, var(--history-accent) 26%, var(--history-line));
  border-radius: 14px 14px 4px 14px;
  background: color-mix(in srgb, var(--history-accent) 9%, var(--history-surface));
}

.pet-chat-history-panel__message.is-live .pet-chat-history-panel__content {
  border-color: color-mix(in srgb, var(--history-accent) 34%, var(--history-line));
}

.pet-chat-history-panel__images {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 2px;
}

.pet-chat-history-panel__message.is-user .pet-chat-history-panel__images {
  justify-content: flex-end;
}

.pet-chat-history-panel__image {
  display: block;
  width: 112px;
  height: 112px;
  border: 1px solid var(--history-line);
  border-radius: 9px;
  object-fit: cover;
}

.pet-chat-history-panel__jump {
  position: absolute;
  right: 16px;
  bottom: 14px;
  display: inline-flex;
  width: 30px;
  height: 30px;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--history-line);
  border-radius: 50%;
  background: color-mix(in srgb, var(--history-surface) 92%, transparent);
  color: var(--history-muted);
  cursor: pointer;
  box-shadow: 0 4px 14px color-mix(in srgb, #243247 14%, transparent);
}

.pet-chat-history-panel__inline-error {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  color: #a24141;
  font-size: 11px;
  line-height: 16px;
}

.pet-chat-history-panel :deep(.pet-chat.is-history) {
  flex: 0 0 auto;
  border-top: 1px solid var(--history-line);
  background: color-mix(in srgb, var(--history-surface) 62%, transparent);
}

.pet-chat-history-panel :deep(.pet-chat.is-history .pet-chat__composer) {
  padding: 14px clamp(20px, 4vw, 64px) 18px;
}

.pet-chat-history-panel__retry {
  flex: 0 0 auto;
  border: 0;
  border-radius: 5px;
  padding: 5px 9px;
  background: color-mix(in srgb, #b23d3d 10%, var(--history-surface));
  color: #9b3333;
  cursor: pointer;
  font-size: 11px;
}

@keyframes pet-chat-history-spin {
  to { transform: rotate(360deg); }
}

@media (max-width: 720px) {
  .pet-chat-history-panel {
    height: 100%;
  }

  .pet-chat-history-panel__messages {
    padding: 20px 14px 22px;
  }

  .pet-chat-history-panel__refresh {
    top: 10px;
    right: 14px;
  }

  .pet-chat-history-panel :deep(.pet-chat.is-history .pet-chat__composer) {
    padding: 12px 14px 14px;
  }

  .pet-chat-history-panel__state {
    flex-direction: column;
  }

  .pet-chat-history-panel__state span {
    max-width: 100%;
    overflow-wrap: normal;
    word-break: keep-all;
  }

  .pet-chat-history-panel__message {
    width: min(100%, 720px);
  }

  .pet-chat-history-panel__inline-error {
    align-items: flex-start;
    flex-direction: column;
  }
}

@media (prefers-reduced-motion: reduce) {
  .pet-chat-history-panel__refresh .is-spinning {
    animation: none;
  }
}
</style>
