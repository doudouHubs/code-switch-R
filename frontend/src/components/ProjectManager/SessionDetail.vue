<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
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
const renderedCount = ref(60)
const loadMoreAnchor = ref<HTMLElement | null>(null)

const deleteState = reactive({
  open: false,
  count: 0,
})

const conversationRenderBatchSize = 60
let loadMoreObserver: IntersectionObserver | null = null

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
const visibleItems = computed(() => items.value.slice(0, renderedCount.value))
const selectedSet = computed(() => new Set(selectedIDs.value))
const expandedSet = computed(() => new Set(expandedIDs.value))
const selectedCount = computed(() => selectedIDs.value.length)
const hasSelection = computed(() => selectedCount.value > 0)
const hasMoreItems = computed(() => visibleItems.value.length < items.value.length)
const showSelectionToolbar = computed(() => selecting.value && !!detail.value)
const conversationGroups = computed(() => {
  const groups: Array<{ id: string; role: SessionConversationItem['role']; items: SessionConversationItem[] }> = []

  for (const item of visibleItems.value) {
    const previousGroup = groups[groups.length - 1]
    if (previousGroup && previousGroup.role === item.role) {
      previousGroup.items.push(item)
      continue
    }

    groups.push({
      id: item.id,
      role: item.role,
      items: [item],
    })
  }

  return groups
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

const toggleExpanded = (item: SessionConversationItem) => {
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
}

const handleBubbleKeydown = (item: SessionConversationItem, event: KeyboardEvent) => {
  if (item.role !== 'agent') {
    return
  }
  if (event.key !== 'Enter' && event.key !== ' ') {
    return
  }
  event.preventDefault()
  toggleExpanded(item)
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

const resetRenderedWindow = () => {
  renderedCount.value = Math.min(items.value.length || conversationRenderBatchSize, conversationRenderBatchSize)
}

const loadMoreItems = () => {
  renderedCount.value = Math.min(renderedCount.value + conversationRenderBatchSize, items.value.length)
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

const selectLoadedItems = () => {
  selectedIDs.value = Array.from(new Set(visibleItems.value.map(item => item.id)))
}

const teardownLoadMoreObserver = () => {
  if (!loadMoreObserver) {
    return
  }
  loadMoreObserver.disconnect()
  loadMoreObserver = null
}

const setupLoadMoreObserver = async () => {
  teardownLoadMoreObserver()
  if (!hasMoreItems.value) {
    return
  }
  await nextTick()
  if (!loadMoreAnchor.value) {
    return
  }

  // 这里用“接近底部自动续一批”的渐进渲染，先把首屏和常用滚动做轻，
  // 避免超长会话一上来就把整页 DOM 全摊开。
  loadMoreObserver = new IntersectionObserver(entries => {
    if (!entries.some(entry => entry.isIntersecting)) {
      return
    }
    loadMoreItems()
  }, {
    rootMargin: '240px 0px 240px 0px',
  })
  loadMoreObserver.observe(loadMoreAnchor.value)
}

const loadDetail = async () => {
  if (!sessionID.value) {
    showToast(t('components.projectManager.detail.errors.sessionNotFound'), 'error')
    router.push('/projects')
    return
  }

  loading.value = true
  try {
    detail.value = await fetchSessionConversationDetail(sessionID.value)
    resetSelectionState()
    resetRenderedWindow()
  } catch (error) {
    console.error('failed to load session conversation detail', error)
    showToast(extractErrorMessage(error), 'error')
  } finally {
    loading.value = false
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
    resetRenderedWindow()
    deleteState.open = false
    showToast(t('components.projectManager.detail.deleteSuccess'), 'success')
  } catch (error) {
    console.error('failed to prune session conversation', error)
    showToast(extractErrorMessage(error), 'error')
  } finally {
    pruning.value = false
  }
}

watch(sessionID, () => {
  loadDetail()
})

watch([items, hasMoreItems], () => {
  void setupLoadMoreObserver()
})

watch(loadMoreAnchor, () => {
  void setupLoadMoreObserver()
})

onMounted(() => {
  loadDetail()
})

onBeforeUnmount(() => {
  teardownLoadMoreObserver()
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
          <strong>{{ t('components.projectManager.detail.renderedCount', { visible: visibleItems.length, total: items.length }) }}</strong>
          <span>{{ t('components.projectManager.detail.selectedCount', { count: selectedCount }) }}</span>
        </div>
        <div class="conversation-toolbar-actions">
          <button class="toolbar-chip" type="button" @click="selectPrimaryConversation">
            {{ t('components.projectManager.detail.selectPrimary') }}
          </button>
          <button class="toolbar-chip" type="button" @click="selectLoadedItems">
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

    <section v-else class="conversation-list chat-thread">
      <article
        v-for="group in conversationGroups"
        :key="group.id"
        :class="['conversation-group', `is-${group.role}`]"
      >
        <div
          v-for="item in group.items"
          :key="item.id"
          :class="['message-entry', `is-${item.role}`, { selected: isSelected(item.id), 'is-selecting': selecting }]"
          :role="selecting ? 'checkbox' : undefined"
          :aria-checked="selecting ? isSelected(item.id) : undefined"
          :tabindex="selecting ? 0 : undefined"
          @click="handleMessageEntryClick(item)"
          @keydown.enter.prevent="handleMessageEntryClick(item)"
          @keydown.space.prevent="handleMessageEntryClick(item)"
        >
          <label v-if="selecting" class="message-select-slot" @click.stop>
            <input
              type="checkbox"
              :checked="isSelected(item.id)"
              @change="handleCheckboxChange(item, $event)"
            />
          </label>

          <div class="message-stack">
            <div
              :class="['conversation-bubble', `is-${item.role}`, { clickable: item.role === 'agent', selected: isSelected(item.id) }]"
              :role="!selecting && item.role === 'agent' ? 'button' : undefined"
              :tabindex="!selecting && item.role === 'agent' ? 0 : undefined"
              @click="!selecting && toggleExpanded(item)"
              @keydown="!selecting && handleBubbleKeydown(item, $event)"
            >
              <span :class="['conversation-content', { clamped: item.role === 'agent' && !isExpanded(item.id) }]">
                {{ item.content }}
              </span>
              <span
                v-if="shouldShowExpand(item)"
                class="conversation-toggle"
              >
                {{ isExpanded(item.id) ? t('components.projectManager.detail.showLess') : t('components.projectManager.detail.showMore') }}
              </span>
            </div>

            <div :class="['message-meta', `is-${item.role}`]">
              <span>{{ item.role === 'user' ? t('components.projectManager.detail.userLabel') : t('components.projectManager.detail.agentLabel') }}</span>
              <span>{{ formatUpdatedAt(item.timestamp) }}</span>
            </div>
          </div>
        </div>
      </article>

      <div v-if="hasMoreItems" ref="loadMoreAnchor" class="conversation-load-more">
        <span>{{ t('components.projectManager.detail.loadMoreHint') }}</span>
        <button class="toolbar-chip" type="button" @click="loadMoreItems">
          {{ t('components.projectManager.detail.loadMore') }}
        </button>
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
