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
  forkSessionConversation,
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
const forking = ref(false)
const openingTerminal = ref(false)
const selecting = ref(false)
const userOnlyMode = ref(false)
const detail = ref<SessionConversationDetail | null>(null)
const expandedIDs = ref<string[]>([])
const selectedIDs = ref<string[]>([])
const conversationViewport = ref<HTMLElement | null>(null)

const deleteState = reactive({
  open: false,
  count: 0,
})

const messageRowElements = new Map<string, HTMLElement>()

type ConversationDisplayEntry = {
  id: string
  role: SessionConversationItem['role']
  item: SessionConversationItem
  items: SessionConversationItem[]
  agentGroup: boolean
}

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
const itemByID = computed(() => new Map(items.value.map(item => [item.id, item])))
const selectedSet = computed(() => new Set(selectedIDs.value))
const expandedSet = computed(() => new Set(expandedIDs.value))
const selectedItems = computed(() =>
  selectedIDs.value
    .map(itemID => itemByID.value.get(itemID))
    .filter((item): item is SessionConversationItem => !!item),
)
const selectedCount = computed(() => selectedIDs.value.length)
const hasSelection = computed(() => selectedCount.value > 0)
const canForkSelection = computed(() =>
  hasSelection.value &&
  selectedItems.value.length === selectedIDs.value.length &&
  selectedItems.value.every(item => !!item.turn_id),
)
const showSelectionToolbar = computed(() => selecting.value && !!detail.value)
const selectionToggleLabel = computed(() =>
  selecting.value
    ? t('components.projectManager.detail.exitSelection')
    : t('components.projectManager.detail.enterSelection'),
)
const headerTitle = computed(() =>
  detail.value?.session.display_name || t('components.projectManager.detail.loadingTitle'),
)
const userOnlyModeLabel = computed(() =>
  userOnlyMode.value
    ? t('components.projectManager.detail.showFullConversation')
    : t('components.projectManager.detail.showUserOnly'),
)
const getConversationContent = (item: SessionConversationItem) => {
  if (userOnlyMode.value && item.role === 'agent') {
    return t('components.projectManager.detail.agentCollapsed')
  }
  return item.content
}

const displayEntries = computed<ConversationDisplayEntry[]>(() => {
  if (!userOnlyMode.value) {
    return items.value.map(item => ({
      id: item.id,
      role: item.role,
      item,
      items: [item],
      agentGroup: false,
    }))
  }

  const repliesByUser = new Map<string, SessionConversationItem[]>()
  const emittedReplyGroups = new Set<string>()
  for (const item of items.value) {
    if (item.role !== 'agent' || !item.reply_for) {
      continue
    }
    const replies = repliesByUser.get(item.reply_for) ?? []
    replies.push(item)
    repliesByUser.set(item.reply_for, replies)
  }

  const result: ConversationDisplayEntry[] = []
  for (const item of items.value) {
    if (item.role === 'user') {
      result.push({
        id: item.id,
        role: item.role,
        item,
        items: [item],
        agentGroup: false,
      })

      const replies = repliesByUser.get(item.id) ?? []
      if (replies.length > 0) {
        // 一轮用户问题可能产生多段 Agent 回复。仅用户模式下必须按轮次聚合，
        // 否则多个 Agent 气泡仍然刷屏，用户扫问题脉络时还是一脑袋包。
        result.push({
          id: `agent-group-${item.id}`,
          role: 'agent',
          item: replies[0],
          items: replies,
          agentGroup: true,
        })
        emittedReplyGroups.add(item.id)
      }
      continue
    }

    if (item.role === 'agent' && item.reply_for) {
      continue
    }

    result.push({
      id: item.id,
      role: item.role,
      item,
      items: [item],
      agentGroup: item.role === 'agent',
    })
  }

  for (const [userID, replies] of repliesByUser) {
    if (emittedReplyGroups.has(userID) || replies.length === 0) {
      continue
    }
    result.push({
      id: `agent-group-${userID}`,
      role: 'agent',
      item: replies[0],
      items: replies,
      agentGroup: true,
    })
  }

  return result
})

const formatUpdatedAt = (timestamp: number) => {
  if (!timestamp) {
    return t('components.projectManager.common.unknownTime')
  }
  return dateFormatter.value.format(new Date(timestamp))
}

const findRepliesForUser = (userID: string) =>
  items.value.filter(item => item.reply_for === userID)

const shouldShowExpand = (item: SessionConversationItem) => {
  if (userOnlyMode.value) {
    return false
  }
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
  const entry = displayEntries.value[index]
  if (!entry) {
    return 120
  }

  const item = entry.item
  const expandable = shouldShowExpand(item)
  const compactAgent = entry.agentGroup || (userOnlyMode.value && item.role === 'agent')
  const visibleUnits = compactAgent
    ? 1
    : item.role === 'agent' && expandable && !isExpanded(item.id)
    ? Math.min(estimateMessageUnits(item.content), 3)
    : estimateMessageUnits(item.content)

  const baseHeight = compactAgent ? 72 : item.role === 'user' ? 92 : 104
  return baseHeight + visibleUnits * 24 + (expandable && !compactAgent ? 24 : 0)
}

const virtualizer = useVirtualizer<HTMLElement, HTMLElement>(computed(() => ({
  count: displayEntries.value.length,
  getScrollElement: () => conversationViewport.value,
  estimateSize: estimateMessageSize,
  overscan: 10,
  getItemKey: index => displayEntries.value[index]?.id ?? index,
})))

const virtualMessages = computed(() => {
  const next: Array<{ row: VirtualItem; entry: ConversationDisplayEntry }> = []
  for (const row of virtualizer.value.getVirtualItems()) {
    const entry = displayEntries.value[row.index]
    if (entry) {
      next.push({ row, entry })
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

const hasActiveTextSelection = () => {
  if (typeof window === 'undefined') {
    return false
  }
  const selection = window.getSelection()
  return !!selection && !selection.isCollapsed && selection.toString().trim().length > 0
}

const scrollConversationToLatest = async () => {
  const latestIndex = displayEntries.value.length - 1
  if (latestIndex < 0) {
    return
  }

  // TanStack Virtual 的滚动元素必须是真正拥有滚动条的容器。
  // 这里连续等两帧，是为了等 Vue 渲染、虚拟列表测量和浏览器布局全部落定；
  // 否则首次进入详情时容易只滚到估算位置，右侧看起来还停在半路。
  for (let attempt = 0; attempt < 3; attempt += 1) {
    virtualizer.value.scrollToIndex(latestIndex, { align: 'end' })
    await waitForNextLayout()
    conversationViewport.value?.scrollTo({
      top: conversationViewport.value.scrollHeight,
      behavior: 'auto',
    })
    await waitForNextLayout()
  }
}

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

const syncVirtualConversationLayout = async (options: { anchorItemID?: string; scrollToLatest?: boolean } = {}) => {
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

  if (options.scrollToLatest && scrollElement) {
    await scrollConversationToLatest()
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

const handleBubbleClick = (entry: ConversationDisplayEntry) => {
  if (selecting.value || userOnlyMode.value || entry.agentGroup || entry.role !== 'agent') {
    return
  }
  // 鼠标拖拽选中文字后也会触发 click；这里必须让文本选择优先，
  // 否则用户复制长回复时会顺手把气泡展开/收起，体验跟被门夹了一样。
  if (hasActiveTextSelection()) {
    return
  }
  void toggleExpanded(entry.item)
}

const handleBubbleKeydown = (item: SessionConversationItem, event: KeyboardEvent) => {
  if (userOnlyMode.value) {
    return
  }
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
const isEntrySelected = (entry: ConversationDisplayEntry) =>
  entry.items.every(item => selectedSet.value.has(item.id))

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

const toggleSelectedEntry = (entry: ConversationDisplayEntry, checked: boolean) => {
  if (entry.agentGroup) {
    for (const item of entry.items) {
      setSelected(item.id, checked)
    }
    return
  }
  toggleSelected(entry.item, checked)
}

const handleCheckboxChange = (entry: ConversationDisplayEntry, event: Event) => {
  const target = event.target as HTMLInputElement
  toggleSelectedEntry(entry, target.checked)
}

const handleMessageEntryClick = (entry: ConversationDisplayEntry) => {
  if (!selecting.value) {
    return
  }
  if (hasActiveTextSelection()) {
    return
  }
  toggleSelectedEntry(entry, !isEntrySelected(entry))
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

const toggleSelectionMode = () => {
  if (selecting.value) {
    closeSelectionMode()
    return
  }
  openSelectionMode()
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
    await syncVirtualConversationLayout({ scrollToLatest: true })
  }
}

const toggleUserOnlyMode = async () => {
  userOnlyMode.value = !userOnlyMode.value
  if (userOnlyMode.value) {
    // 仅用户模式的业务目标是快速扫用户问题脉络，Agent 回答统一压成一行；
    // 已展开状态需要清空，否则回到普通模式时会出现“模式切换前的旧展开状态”干扰阅读。
    expandedIDs.value = []
  }
  await syncVirtualConversationLayout({ scrollToLatest: true })
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
    await openSessionTerminal(detail.value.session)
  } catch (error) {
    console.error('failed to open session terminal from detail', error)
    showToast(extractErrorMessage(error), 'error')
  } finally {
    openingTerminal.value = false
  }
}

const forkConversationFromMessages = async (messageIDs: string[]) => {
  if (!detail.value || forking.value) {
    return
  }
  const normalizedIDs = Array.from(new Set(messageIDs.map(id => id.trim()).filter(Boolean)))
  if (!normalizedIDs.length) {
    return
  }

  forking.value = true
  try {
    await forkSessionConversation(detail.value.session.id, normalizedIDs)
    closeSelectionMode()
    showToast(t('components.projectManager.detail.forkSuccess'), 'success')
  } catch (error) {
    console.error('failed to fork session conversation', error)
    showToast(extractErrorMessage(error), 'error')
  } finally {
    forking.value = false
  }
}

const handleForkFromUser = (item: SessionConversationItem) => {
  if (item.role !== 'user' || forking.value) {
    return
  }
  if (!item.turn_id) {
    showToast(t('components.projectManager.detail.forkUnavailable'), 'error')
    return
  }
  void forkConversationFromMessages([item.id])
}

const forkSelectedConversation = () => {
  if (!canForkSelection.value) {
    showToast(t('components.projectManager.detail.forkUnavailable'), 'error')
    return
  }
  void forkConversationFromMessages(selectedIDs.value)
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
  <div class="project-manager-page project-manager-detail-page" :class="{ 'is-selecting': showSelectionToolbar }">
    <header class="detail-app-header">
      <div class="detail-app-header-side">
        <button
          class="detail-nav-button"
          type="button"
          :title="t('components.projectManager.detail.back')"
          :aria-label="t('components.projectManager.detail.back')"
          @click="goBack"
        >
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <path d="M14.5 5.5 8 12l6.5 6.5" />
          </svg>
        </button>
        <div class="detail-app-header-copy">
          <strong>{{ headerTitle }}</strong>
        </div>
      </div>

      <div class="detail-app-header-actions">
        <button
          class="detail-header-text-button accent"
          type="button"
          :disabled="openingTerminal || forking || loading || !detail"
          @click="handleOpenTerminal"
        >
          <span v-if="openingTerminal" class="detail-header-spinner" aria-hidden="true"></span>
          <span>{{ t('components.projectManager.card.openSession') }}</span>
        </button>
        <button
          class="detail-header-text-button"
          type="button"
          :class="{ active: userOnlyMode }"
          :disabled="loading || forking || !detail"
          @click="toggleUserOnlyMode"
        >
          {{ userOnlyModeLabel }}
        </button>
        <button
          class="detail-header-text-button"
          type="button"
          :class="{ active: selecting }"
          :disabled="loading || !detail || pruning || forking"
          @click="toggleSelectionMode"
        >
          {{ selectionToggleLabel }}
        </button>
      </div>
    </header>

    <section v-if="loading" class="detail-screen state-panel">
      <div class="state-orb"></div>
      <p>{{ t('components.projectManager.detail.loading') }}</p>
    </section>

    <section v-else-if="!items.length" class="detail-screen state-panel empty">
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
          :ref="element => setMessageRowRef(entry.entry.id, element)"
          class="conversation-virtual-row"
          :data-index="entry.row.index"
          :style="{ transform: `translateY(${entry.row.start}px)` }"
        >
          <div
            :class="['message-entry', `is-${entry.entry.role}`, { selected: isEntrySelected(entry.entry), 'is-selecting': selecting }]"
            :role="selecting ? 'checkbox' : undefined"
            :aria-checked="selecting ? isEntrySelected(entry.entry) : undefined"
            :tabindex="selecting ? 0 : undefined"
            @click="handleMessageEntryClick(entry.entry)"
            @keydown.enter.prevent="handleMessageEntryClick(entry.entry)"
            @keydown.space.prevent="handleMessageEntryClick(entry.entry)"
          >
            <label v-if="selecting" class="message-select-slot" @click.stop>
              <input
                type="checkbox"
                :checked="isEntrySelected(entry.entry)"
                @change="handleCheckboxChange(entry.entry, $event)"
              />
            </label>

            <div class="message-stack">
              <div
                :class="[
                  'conversation-bubble',
                  `is-${entry.entry.role}`,
                  {
                    clickable: entry.entry.role === 'agent' && !userOnlyMode && !entry.entry.agentGroup,
                    selected: isEntrySelected(entry.entry),
                    compacted: entry.entry.agentGroup || (userOnlyMode && entry.entry.role === 'agent'),
                  },
                ]"
                :role="!selecting && !userOnlyMode && !entry.entry.agentGroup && entry.entry.role === 'agent' ? 'button' : undefined"
                :tabindex="!selecting && !userOnlyMode && !entry.entry.agentGroup && entry.entry.role === 'agent' ? 0 : undefined"
                @click="handleBubbleClick(entry.entry)"
                @keydown="!selecting && !userOnlyMode && !entry.entry.agentGroup && handleBubbleKeydown(entry.entry.item, $event)"
              >
                <span
                  :class="[
                    'conversation-content',
                    {
                      clamped: entry.entry.role === 'agent' && !userOnlyMode && !entry.entry.agentGroup && !isExpanded(entry.entry.item.id),
                      compacted: entry.entry.agentGroup || (userOnlyMode && entry.entry.role === 'agent'),
                    },
                  ]"
                >
                  {{ entry.entry.agentGroup ? t('components.projectManager.detail.agentCollapsedCount', { count: entry.entry.items.length }) : getConversationContent(entry.entry.item) }}
                </span>
                <span
                  v-if="!entry.entry.agentGroup && shouldShowExpand(entry.entry.item)"
                  class="conversation-toggle"
                >
                  {{ isExpanded(entry.entry.item.id) ? t('components.projectManager.detail.showLess') : t('components.projectManager.detail.showMore') }}
                </span>
              </div>

              <div :class="['message-meta', `is-${entry.entry.role}`]">
                <span>{{ entry.entry.role === 'user' ? t('components.projectManager.detail.userLabel') : t('components.projectManager.detail.agentLabel') }}</span>
                <span>{{ formatUpdatedAt(entry.entry.item.timestamp) }}</span>
                <button
                  v-if="entry.entry.role === 'user' && !selecting"
                  class="message-meta-action fork"
                  type="button"
                  :disabled="forking || !entry.entry.item.turn_id"
                  :title="entry.entry.item.turn_id ? t('components.projectManager.detail.forkFromHere') : t('components.projectManager.detail.forkUnavailable')"
                  @click.stop="handleForkFromUser(entry.entry.item)"
                >
                  <svg viewBox="0 0 24 24" aria-hidden="true">
                    <path d="M7 5.75v5.5a4 4 0 0 0 4 4h6" />
                    <path d="M14.25 11.5 18 15.25 14.25 19" />
                    <path d="M7 5.75a2 2 0 1 0 0-4 2 2 0 0 0 0 4Z" />
                  </svg>
                  <span>{{ t('components.projectManager.detail.forkFromHere') }}</span>
                </button>
              </div>
            </div>
          </div>
        </article>
      </div>
    </section>

    <footer v-if="showSelectionToolbar" class="selection-dock">
      <div class="selection-dock-summary">
        <strong>{{ t('components.projectManager.detail.selectedCount', { count: selectedCount }) }}</strong>
        <span>{{ t('components.projectManager.detail.totalCount', { count: items.length }) }}</span>
      </div>

      <div class="selection-dock-actions">
        <button class="selection-dock-action" type="button" @click="selectPrimaryConversation">
          <span class="selection-dock-icon" aria-hidden="true">
            <svg viewBox="0 0 24 24">
              <path d="M6 6.75h9" />
              <path d="M6 12h12" />
              <path d="M6 17.25h7.5" />
            </svg>
          </span>
          <span>{{ t('components.projectManager.detail.selectPrimary') }}</span>
        </button>

        <button class="selection-dock-action" type="button" @click="selectAllMessages">
          <span class="selection-dock-icon" aria-hidden="true">
            <svg viewBox="0 0 24 24">
              <rect x="5.25" y="5.25" width="13.5" height="13.5" rx="2.5" />
              <path d="m8.5 12 2.2 2.2L15.5 9.5" />
            </svg>
          </span>
          <span>{{ t('components.projectManager.detail.selectLoaded') }}</span>
        </button>

        <button class="selection-dock-action" type="button" :disabled="!hasSelection" @click="clearSelection">
          <span class="selection-dock-icon" aria-hidden="true">
            <svg viewBox="0 0 24 24">
              <path d="M6.75 6.75 17.25 17.25" />
              <path d="M17.25 6.75 6.75 17.25" />
            </svg>
          </span>
          <span>{{ t('components.projectManager.detail.clearSelection') }}</span>
        </button>

        <button
          class="selection-dock-action accent"
          type="button"
          :disabled="!canForkSelection || forking || pruning || loading"
          @click="forkSelectedConversation"
        >
          <span class="selection-dock-icon" aria-hidden="true">
            <svg viewBox="0 0 24 24">
              <path d="M7 5.75v5.5a4 4 0 0 0 4 4h6" />
              <path d="M14.25 11.5 18 15.25 14.25 19" />
              <path d="M7 5.75a2 2 0 1 0 0-4 2 2 0 0 0 0 4Z" />
            </svg>
          </span>
          <span>{{ forking ? t('components.projectManager.detail.forking') : t('components.projectManager.detail.forkSelected') }}</span>
        </button>

        <button
          class="selection-dock-action danger"
          type="button"
          :disabled="!hasSelection || pruning || forking || loading"
          @click="openDeleteConfirm"
        >
          <span class="selection-dock-icon" aria-hidden="true">
            <svg viewBox="0 0 24 24">
              <path d="M9.25 5.75h5.5" />
              <path d="M4.75 7.25h14.5" />
              <path d="M8.25 7.25v9.25" />
              <path d="M15.75 7.25v9.25" />
              <path d="M6.75 7.25l.55 10.05a1.5 1.5 0 0 0 1.5 1.42h6.4a1.5 1.5 0 0 0 1.5-1.42l.55-10.05" />
            </svg>
          </span>
          <span>{{ t('components.projectManager.detail.deleteAction') }}</span>
        </button>

        <button class="selection-dock-close" type="button" :disabled="pruning || forking" @click="closeSelectionMode">
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <path d="M7 7 17 17" />
            <path d="M17 7 7 17" />
          </svg>
        </button>
      </div>
    </footer>

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
