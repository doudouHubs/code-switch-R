<script setup lang="ts">
import { Check } from '@lucide/vue'
import { useI18n } from 'vue-i18n'
import ProjectManagerCardMenu from './ProjectManagerCardMenu.vue'
import ProjectManagerCodexStatusLight from './ProjectManagerCodexStatusLight.vue'
import ProjectManagerHighlightedText from './ProjectManagerHighlightedText.vue'
import ProjectManagerMasonryGrid from './ProjectManagerMasonryGrid.vue'
import type {
  CodexSessionRuntimeStatus,
  CodexStatusMonitorInfo,
  SessionSummary,
} from '../../services/projectManager'

type ProjectManagerCardMenuAction = {
  key: string
  label: string
  accent?: boolean
  danger?: boolean
}

const props = defineProps<{
  sessions: SessionSummary[]
  searchKeyword: string
  formatUpdatedAt: (timestamp: number) => string
  resolveSummary: (session: SessionSummary) => string
  showProjectNameTag: boolean
  selectionMode: boolean
  isSessionSelected: (sessionId: string) => boolean
  isSessionOpening: (sessionId: string) => boolean
  isSessionDeleting: (sessionId: string) => boolean
  codexMonitor: CodexStatusMonitorInfo
  resolveCodexSessionStatus: (sessionId: string) => CodexSessionRuntimeStatus | undefined
}>()

const emit = defineEmits<{
  rename: [session: SessionSummary]
  delete: [session: SessionSummary]
  'open-session': [session: SessionSummary]
  'open-detail': [session: SessionSummary]
  'toggle-selection': [session: SessionSummary]
}>()

const { t } = useI18n()

const resolveSessionActions = (session: SessionSummary): ProjectManagerCardMenuAction[] => [
  {
    key: `rename:${session.id}`,
    label: t('components.projectManager.card.rename'),
  },
  {
    key: `delete:${session.id}`,
    label: t('components.projectManager.card.deleteSession'),
    danger: true,
  },
]

const handleSessionAction = (session: SessionSummary, actionKey: string) => {
  if (props.selectionMode) {
    return
  }
  if (actionKey.startsWith('rename:')) {
    emit('rename', session)
    return
  }
  if (actionKey.startsWith('delete:')) {
    emit('delete', session)
    return
  }
}

const emitOpenSession = (session: SessionSummary) => {
  if (
    props.selectionMode ||
    props.isSessionOpening(session.id) ||
    props.isSessionDeleting(session.id)
  ) {
    return
  }
  emit('open-session', session)
}

const emitOpenDetail = (session: SessionSummary) => {
  if (props.selectionMode || props.isSessionDeleting(session.id)) {
    return
  }
  emit('open-detail', session)
}

const emitToggleSelection = (session: SessionSummary) => {
  if (props.isSessionDeleting(session.id)) {
    return
  }
  emit('toggle-selection', session)
}

const handleSessionCardClick = (session: SessionSummary) => {
  if (props.selectionMode) {
    emitToggleSelection(session)
    return
  }
  emitOpenDetail(session)
}
</script>

<template>
  <ProjectManagerMasonryGrid class="session-grid" :min-column-width="280" :gap="18">
    <article
      v-for="session in sessions"
      :key="session.id"
      :class="[
        'session-card',
        {
          'is-opening': isSessionOpening(session.id),
          'is-deleting': isSessionDeleting(session.id),
          'is-selection-mode': selectionMode,
          'is-selected': selectionMode && isSessionSelected(session.id),
        },
      ]"
      :role="selectionMode ? 'checkbox' : undefined"
      :tabindex="selectionMode ? 0 : undefined"
      :aria-checked="selectionMode ? isSessionSelected(session.id) : undefined"
      :aria-label="selectionMode ? session.display_name : undefined"
      @click="handleSessionCardClick(session)"
      @keydown.enter.self.prevent="emitToggleSelection(session)"
      @keydown.space.self.prevent="emitToggleSelection(session)"
    >
      <span
        v-if="selectionMode"
        class="session-selection-indicator"
        :class="{ 'is-checked': isSessionSelected(session.id) }"
        aria-hidden="true"
      >
        <Check v-if="isSessionSelected(session.id)" :size="14" :stroke-width="2.4" />
      </span>
      <div class="card-topline">
        <div class="card-title-row">
          <ProjectManagerCodexStatusLight
            :monitor="codexMonitor"
            :session-status="resolveCodexSessionStatus(session.id)"
          />
          <h3 class="card-title">
            <ProjectManagerHighlightedText
              :text="session.display_name"
              :keyword="searchKeyword"
            />
          </h3>
        </div>
        <ProjectManagerCardMenu
          :label="t('components.projectManager.card.moreActions')"
          :actions="resolveSessionActions(session)"
          :loading="isSessionDeleting(session.id)"
          :disabled="selectionMode || isSessionDeleting(session.id)"
          @select="handleSessionAction(session, $event)"
        />
      </div>
      <div class="card-copy">
        <p v-if="showProjectNameTag" class="card-eyebrow">
          <ProjectManagerHighlightedText
            :text="session.project_name"
            :keyword="searchKeyword"
          />
        </p>
        <p class="card-summary">
          <ProjectManagerHighlightedText
            :text="resolveSummary(session)"
            :keyword="searchKeyword"
          />
        </p>
        <p class="card-path small">
          <ProjectManagerHighlightedText
            :text="showProjectNameTag ? session.project_path : (session.cwd || session.project_path)"
            :keyword="searchKeyword"
          />
        </p>
      </div>
      <div class="card-footer">
        <span class="card-time">{{ formatUpdatedAt(session.updated_at) }}</span>
        <button
          :class="['card-footer-action', { 'is-loading': isSessionOpening(session.id) }]"
          type="button"
          :disabled="selectionMode || isSessionOpening(session.id) || isSessionDeleting(session.id)"
          @click.stop="emitOpenSession(session)"
        >
          <span
            v-if="isSessionOpening(session.id)"
            class="card-footer-spinner"
            aria-hidden="true"
          ></span>
          <span>{{ t('components.projectManager.card.openSession') }}</span>
        </button>
      </div>
    </article>
  </ProjectManagerMasonryGrid>
</template>
