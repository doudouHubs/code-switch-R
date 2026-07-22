<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import ProjectManagerCardMenu from './ProjectManagerCardMenu.vue'
import ProjectManagerCodexStatusLight from './ProjectManagerCodexStatusLight.vue'
import ProjectManagerHighlightedText from './ProjectManagerHighlightedText.vue'
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
  if (props.isSessionOpening(session.id) || props.isSessionDeleting(session.id)) {
    return
  }
  emit('open-session', session)
}

const emitOpenDetail = (session: SessionSummary) => {
  if (props.isSessionDeleting(session.id)) {
    return
  }
  emit('open-detail', session)
}
</script>

<template>
  <section class="session-grid">
    <article
      v-for="session in sessions"
      :key="session.id"
      :class="['session-card', { 'is-opening': isSessionOpening(session.id), 'is-deleting': isSessionDeleting(session.id) }]"
      @click="emitOpenDetail(session)"
    >
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
          :disabled="isSessionDeleting(session.id)"
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
          :disabled="isSessionOpening(session.id) || isSessionDeleting(session.id)"
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
  </section>
</template>
