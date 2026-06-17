<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useVirtualizer, type VirtualItem } from '@tanstack/vue-virtual'
import BaseButton from '../common/BaseButton.vue'
import BaseModal from '../common/BaseModal.vue'
import './projectManager.css'
import {
  fetchSessionConversationDetail,
  openSessionTerminal,
  pruneSessionConversation,
  type SessionConversationDetail,
  type SessionConversationItem,
} from '../../services/projectManager'
import { extractErrorMessage } from '../../utils/error'
import { showToast } from '../../utils/toast'

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()

const loading = ref(false)
const pruning = ref(false)
const openingTerminal = ref(false)
const selecting = ref(false)
const detail = ref<SessionConversationDetail | null>(null)
const expandedIDs = ref<string[]>([])
const selectedIDs = ref<string[]>([])
const conversationViewport = ref<HTMLElement | null>(null)

const deleteState = reactive({
  open: false,
  count: 0,
})

const messageRowElements = new Map<string, HTMLElement>()

const sessionID = computed(() => {
  const raw = route.params.sessionId
  return typeof raw === 'string' ? decodeURIComponent(raw) : ''
})

const dateFormatter = computed(() =>
  new Intl.DateTimeFormat(locale.value === 'zh' ? 'zh-CN' : 'en-US', {
    month: 'short',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }),
)

const items = computed(() => detail.value?.items ?? [])
const selectedSet = computed(() => new Set(selectedIDs.value))
const expandedSet = computed(() => new Set(expandedIDs.value))
const selectedCount = computed(() => selectedIDs.value.length)
const hasSelection = computed(() => selectedCount.value > 0)
const showSelectionToolbar = computed(() => selecting.value && !!detail.value)

const formatUpdatedAt = (timestamp: number) => {
  if (!timestamp) {
    return t('components.projectManager.common.unknownTime')
  }
  return dateFormatter.value.format(new Date(timestamp))
}

const findRepliesForUser = (userID: string) =>
  items.value.filter(item => item.reply_for === userID)

const shouldShowExpand = (item: SessionConversationItem) => {
  if (item.role !== 'agent') {
    return false
  }
  const normalized = item.content.replace(/\r\n/g, '\n')
  const nonEmptyLines = normalized
    .split('\n')
    .map(line => line.trim())
    .filter(Boolean)
  return nonEmptyLines.length > 3 || normalized.length > 180
}

const isExpanded = (itemID: string) => expandedSet.value.has(itemID)

const estimateMessageUnits = (content: string) => {
  const normalized = content.replace(/\r\n/g, '\n')
  return normalized
    .split('\n')
    .reduce((total, line) => total + Math.max(1, Math.ceil(Math.max(line.length, 1) / 42)), 0)
}

const estimateMessageSize = (index: number) => {
  const item = items.value[index]
  if (!item) {
    return 120
  }

  const expandable = shouldShowExpand(item)
  const visibleUnits = item.role === 'agent' && expandable && !isExpanded(item.id)
    ? Math.min(estimateMessageUnits(item.content), 3)
    : estimateMessageUnits(item.content)

  const baseHeight = item.role === 'user' ? 92 : 104
  return baseHeight + visibleUnits * 24 + (expandable ? 24 : 0)
}

const virtualizer = useVirtualizer<HTMLElement, HTMLElement>(computed(() => ({
  count: items.value.length,
  getScrollElement: () => conversationViewport.value,
  estimateSize: estimateMessageSize,
  overscan: 10,
  getItemKey: index => items.value[index]?.id ?? index,
})))

const virtualMessages = computed(() => {
  const next: Array<{ row: VirtualItem; item: SessionConversationItem }> = []
  for (const row of virtualizer.value.getVirtualItems()) {
    const item = items.value[row.index]
    if (item) {
      next.push({ row, item })
    }
  }
  return next
})

const virtualTotalSize = computed(() => virtualizer.value.getTotalSize())

const waitForNextLayout = () => new Promise<void>((resolve) => {
  if (typeof window === 'undefined') {
    resolve()
    return
  }
  window.requestAnimationFrame(() => resolve())
})

const setMessageRowRef = (messageID: string, element: unknown) => {
  if (!(element instanceof HTMLElement)) {
    messageRowElements.delete(messageID)
    return
  }

  messageRowElements.set(messageID, element)
  virtualizer.value.measureElement(element)
}

const getVirtualRowKey = (row: VirtualItem) =>
  typeof row.key === 'bigint' ? row.key.toString() : row.key

const syncVirtualConversationLayout = async (options: { anchorItemID?: string; resetScroll?: boolean } = {}) => {
  const scrollElement = conversationViewport.value
  const anchorElement = options.anchorItemID ? messageRowElements.get(options.anchorItemID) : null
  let anchorTop: number | null = null

  if (scrollElement && anchorElement) {
    const viewportRect = scrollElement.getBoundingClientRect()
    anchorTop = anchorElement.getBoundingClientRect().top - viewportRect.top
  }

  await nextTick()
  await waitForNextLayout()
  virtualizer.value.measure()
  await waitForNextLayout()

  if (options.resetScroll && scrollElement) {
    scrollElement.scrollTop = 0
    virtualizer.value.scrollToOffset(0)
  }

  if (anchorTop === null || !scrollElement || !options.anchorItemID) {
    return
  }

  const nextAnchorElement = messageRowElements.get(options.anchorItemID)
  if (!nextAnchorElement) {
    return
  }

  const viewportRect = scrollElement.getBoundingClientRect()
  const nextAnchorTop = nextAnchorElement.getBoundingClientRect().top - viewportRect.top
  const delta = nextAnchorTop - anchorTop

  // 这里按“点击哪条 agent，就尽量稳住哪条”的原则补偿滚动，
  // 否则展开/收起长回答时，视口会出现明显抖动，体验像没睡醒似的。
  if (Math.abs(delta) >= 1) {
    scrollElement.scrollTop += delta
  }
}

const toggleExpanded = async (item: SessionConversationItem) => {
  if (item.role !== 'agent' || !shouldShowExpand(item)) {
    return
  }
  const next = new Set(expandedIDs.value)
  if (next.has(item.id)) {
    next.delete(item.id)
  } else {
    next.add(item.id)
  }
  expandedIDs.value = Array.from(next)
  await syncVirtualConversationLayout({ anchorItemID: item.id })
}

const handleBubbleKeydown = (item: SessionConversationItem, event: KeyboardEvent) => {
  if (item.role !== 'agent') {
    return
  }
  if (event.key !== 'Enter' && event.key !== ' ') {
    return
  }
  event.preventDefault()
  void toggleExpanded(item)
}

const isSelected = (itemID: string) => selectedSet.value.has(itemID)

const setSelected = (itemID: string, checked: boolean) => {
  const next = new Set(selectedIDs.value)
  if (checked) {
    next.add(itemID)
  } else {
    next.delete(itemID)
  }
  selectedIDs.value = Array.from(next)
}

const toggleSelected = (item: SessionConversationItem, checked: boolean) => {
  if (item.role === 'user') {
    setSelected(item.id, checked)
    const replies = findRepliesForUser(item.id)
    for (const reply of replies) {
      setSelected(reply.id, checked)
    }
    return
  }
  setSelected(item.id, checked)
}

const handleCheckboxChange = (item: SessionConversationItem, event: Event) => {
  const target = event.target as HTMLInputElement
  toggleSelected(item, target.checked)
}

const handleMessageEntryClick = (item: SessionConversationItem) => {
  if (!selecting.value) {
    return
  }
  toggleSelected(item, !isSelected(item.id))
}

const resetSelectionState = () => {
  selectedIDs.value = []
  expandedIDs.value = []
  selecting.value = false
}

const clearSelection = () => {
  selectedIDs.value = []
}

const openSelectionMode = () => {
  selecting.value = true
}

const closeSelectionMode = () => {
  selectedIDs.value = []
  selecting.value = false
}

const selectPrimaryConversation = () => {
  const next = new Set<string>()
  // 这里按“用户消息 + 它后面整段 agent 回复”的归属规则做快捷选择。
  // 手动勾选和快捷选择必须是一套语义，不然用户看见的数量和最后删掉的内容会对不上。
  for (const item of items.value) {
    if (item.role === 'user' || item.reply_for) {
      next.add(item.id)
    }
  }
  selectedIDs.value = Array.from(next)
}

const selectAllMessages = () => {
  // 真虚拟列表只渲染视口附近的 DOM，但“全选”必须作用于完整数据集，
  // 否则用户眼里是全选，实际只删掉一截，那就纯扯犊子了。
  selectedIDs.value = Array.from(new Set(items.value.map(item => item.id)))
}

const loadDetail = async () => {
  if (!sessionID.value) {
    showToast(t('components.projectManager.detail.errors.sessionNotFound'), 'error')
    router.push('/projects')
    return
  }

  loading.value = true
  let loaded = false
  try {
    detail.value = await fetchSessionConversationDetail(sessionID.value)
    resetSelectionState()
    loaded = true
  } catch (error) {
    console.error('failed to load session conversation detail', error)
    showToast(extractErrorMessage(error), 'error')
  } finally {
    loading.value = false
  }

  if (loaded) {
    await syncVirtualConversationLayout({ resetScroll: true })
  }
}

const goBack = () => {
  router.push('/projects')
}

const handleOpenTerminal = async () => {
  if (!detail.value || openingTerminal.value) {
    return
  }
  openingTerminal.value = true
  try {
    await openSessionTerminal(detail.value.session.id)
  } catch (error) {
    console.error('failed to open session terminal from detail', error)
    showToast(extractErrorMessage(error), 'error')
  } finally {
    openingTerminal.value = false
  }
}

const openDeleteConfirm = () => {
  if (!selectedIDs.value.length) {
    return
  }
  deleteState.count = selectedIDs.value.length
  deleteState.open = true
}

const closeDeleteConfirm = () => {
  if (pruning.value) {
    return
  }
  deleteState.open = false
}

const confirmDelete = async () => {
  if (!detail.value || !selectedIDs.value.length) {
    return
  }

  pruning.value = true
  try {
    detail.value = await pruneSessionConversation(detail.value.session.id, selectedIDs.value)
    resetSelectionState()
    deleteState.open = false
    await syncVirtualConversationLayout()
    showToast(t('components.projectManager.detail.deleteSuccess'), 'success')
  } catch (error) {
    console.error('failed to prune session conversation', error)
    showToast(extractErrorMessage(error), 'error')
  } finally {
    pruning.value = false
  }
}

watch(sessionID, () => {
  void loadDetail()
})

watch(selecting, () => {
  if (loading.value) {
    return
  }
  void syncVirtualConversationLayout()
})

onMounted(() => {
  void loadDetail()
})
</script>

<template>
  <div class="project-manager-page project-manager-detail-page">
    <section class="project-detail-hero">
      <div class="project-detail-actions">
        <button class="back-chip" type="button" @click="goBack">
          ← {{ t('components.projectManager.detail.back') }}
        </button>
        <div class="project-detail-action-row">
          <BaseButton
            variant="outline"
            :disabled="openingTerminal || loading || !detail"
            :loading="openingTerminal"
            @click="handleOpenTerminal"
          >
            {{ t('components.projectManager.card.openSession') }}
          </BaseButton>
          <BaseButton
            v-if="!selecting"
            variant="outline"
            :disabled="loading || !detail"
            @click="openSelectionMode"
          >
            {{ t('components.projectManager.detail.enterSelection') }}
          </BaseButton>
          <BaseButton
            v-else
            variant="danger"
            :disabled="!hasSelection || pruning || loading"
            @click="openDeleteConfirm"
          >
            {{ t('components.projectManager.detail.deleteSelected', { count: selectedCount }) }}
          </BaseButton>
        </div>
      </div>

      <div v-if="showSelectionToolbar" class="conversation-toolbar">
        <div class="conversation-toolbar-status">
          <strong>{{ t('components.projectManager.detail.selectedCount', { count: selectedCount }) }}</strong>
          <span>{{ t('components.projectManager.detail.totalCount', { count: items.length }) }}</span>
        </div>
        <div class="conversation-toolbar-actions">
          <button class="toolbar-chip" type="button" @click="selectPrimaryConversation">
            {{ t('components.projectManager.detail.selectPrimary') }}
          </button>
          <button class="toolbar-chip" type="button" @click="selectAllMessages">
            {{ t('components.projectManager.detail.selectLoaded') }}
          </button>
          <button class="toolbar-chip ghost" type="button" :disabled="!hasSelection" @click="clearSelection">
            {{ t('components.projectManager.detail.clearSelection') }}
          </button>
          <button class="toolbar-chip ghost" type="button" @click="closeSelectionMode">
            {{ t('components.projectManager.detail.exitSelection') }}
          </button>
        </div>
      </div>

      <div class="project-detail-copy">
        <p class="detail-eyebrow">{{ t('components.projectManager.detail.eyebrow') }}</p>
        <h1>{{ detail?.session.display_name || t('components.projectManager.detail.loadingTitle') }}</h1>
        <p class="detail-lead">{{ detail?.session.summary || t('components.projectManager.common.emptySummary') }}</p>
      </div>

      <div v-if="detail" class="project-detail-meta">
        <div class="detail-meta-card">
          <span>{{ t('components.projectManager.detail.projectName') }}</span>
          <strong>{{ detail.session.project_name }}</strong>
        </div>
        <div class="detail-meta-card">
          <span>{{ t('components.projectManager.common.path') }}</span>
          <strong>{{ detail.session.cwd || detail.session.project_path }}</strong>
        </div>
        <div class="detail-meta-card">
          <span>{{ t('components.projectManager.detail.updatedAt') }}</span>
          <strong>{{ formatUpdatedAt(detail.session.updated_at) }}</strong>
        </div>
      </div>
    </section>

    <section v-if="loading" class="state-panel">
      <div class="state-orb"></div>
      <p>{{ t('components.projectManager.detail.loading') }}</p>
    </section>

    <section v-else-if="!items.length" class="state-panel empty">
      <p>{{ t('components.projectManager.detail.empty') }}</p>
    </section>

    <section
      v-else
      ref="conversationViewport"
      class="conversation-list chat-thread conversation-viewport"
    >
      <div class="conversation-virtualizer" :style="{ height: `${virtualTotalSize}px` }">
        <article
          v-for="entry in virtualMessages"
          :key="getVirtualRowKey(entry.row)"
          :ref="element => setMessageRowRef(entry.item.id, element)"
          class="conversation-virtual-row"
          :data-index="entry.row.index"
          :style="{ transform: `translateY(${entry.row.start}px)` }"
        >
          <div
            :class="['message-entry', `is-${entry.item.role}`, { selected: isSelected(entry.item.id), 'is-selecting': selecting }]"
            :role="selecting ? 'checkbox' : undefined"
            :aria-checked="selecting ? isSelected(entry.item.id) : undefined"
            :tabindex="selecting ? 0 : undefined"
            @click="handleMessageEntryClick(entry.item)"
            @keydown.enter.prevent="handleMessageEntryClick(entry.item)"
            @keydown.space.prevent="handleMessageEntryClick(entry.item)"
          >
            <label v-if="selecting" class="message-select-slot" @click.stop>
              <input
                type="checkbox"
                :checked="isSelected(entry.item.id)"
                @change="handleCheckboxChange(entry.item, $event)"
              />
            </label>

            <div class="message-stack">
              <div
                :class="['conversation-bubble', `is-${entry.item.role}`, { clickable: entry.item.role === 'agent', selected: isSelected(entry.item.id) }]"
                :role="!selecting && entry.item.role === 'agent' ? 'button' : undefined"
                :tabindex="!selecting && entry.item.role === 'agent' ? 0 : undefined"
                @click="!selecting && toggleExpanded(entry.item)"
                @keydown="!selecting && handleBubbleKeydown(entry.item, $event)"
              >
                <span :class="['conversation-content', { clamped: entry.item.role === 'agent' && !isExpanded(entry.item.id) }]">
                  {{ entry.item.content }}
                </span>
                <span
                  v-if="shouldShowExpand(entry.item)"
                  class="conversation-toggle"
                >
                  {{ isExpanded(entry.item.id) ? t('components.projectManager.detail.showLess') : t('components.projectManager.detail.showMore') }}
                </span>
              </div>

              <div :class="['message-meta', `is-${entry.item.role}`]">
                <span>{{ entry.item.role === 'user' ? t('components.projectManager.detail.userLabel') : t('components.projectManager.detail.agentLabel') }}</span>
                <span>{{ formatUpdatedAt(entry.item.timestamp) }}</span>
              </div>
            </div>
          </div>
        </article>
      </div>
    </section>

    <BaseModal
      :open="deleteState.open"
      :title="t('components.projectManager.detail.deleteTitle')"
      variant="confirm"
      @close="closeDeleteConfirm"
    >
      <div class="confirm-body">
        <p>{{ t('components.projectManager.detail.deleteConfirm', { count: deleteState.count }) }}</p>
        <p class="detail-delete-hint">{{ t('components.projectManager.detail.deleteHint') }}</p>
      </div>
      <footer class="form-actions confirm-actions">
        <BaseButton variant="outline" type="button" :disabled="pruning" @click="closeDeleteConfirm">
          {{ t('components.projectManager.rename.cancel') }}
        </BaseButton>
        <BaseButton variant="danger" type="button" :disabled="pruning" :loading="pruning" @click="confirmDelete">
          {{ t('components.projectManager.detail.confirmDeleteAction') }}
        </BaseButton>
      </footer>
    </BaseModal>
  </div>
</template>
