<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import ProjectManagerCardMenu from './ProjectManagerCardMenu.vue'
import type { SessionSummary } from '../../services/projectManager'

type ProjectManagerCardMenuAction = {
  key: string
  label: string
  accent?: boolean
}

defineProps<{
  sessions: SessionSummary[]
  formatUpdatedAt: (timestamp: number) => string
  resolveSummary: (session: SessionSummary) => string
  showProjectNameTag: boolean
}>()

const emit = defineEmits<{
  rename: [session: SessionSummary]
  'open-session': [session: SessionSummary]
}>()

const { t } = useI18n()

const resolveSessionActions = (session: SessionSummary): ProjectManagerCardMenuAction[] => [
  {
    key: `rename:${session.id}`,
    label: t('components.projectManager.card.rename'),
  },
]

const handleSessionAction = (session: SessionSummary, actionKey: string) => {
  if (actionKey.startsWith('rename:')) {
    emit('rename', session)
    return
  }
}
</script>

<template>
  <section class="session-grid">
    <article
      v-for="session in sessions"
      :key="session.id"
      class="session-card"
      @click="emit('open-session', session)"
    >
      <div class="card-topline">
        <h3 class="card-title">{{ session.display_name }}</h3>
        <ProjectManagerCardMenu
          :label="t('components.projectManager.card.moreActions')"
          :actions="resolveSessionActions(session)"
          @select="handleSessionAction(session, $event)"
        />
      </div>
      <div class="card-copy">
        <p v-if="showProjectNameTag" class="card-eyebrow">{{ session.project_name }}</p>
        <p class="card-summary">{{ resolveSummary(session) }}</p>
        <p class="card-path small">{{ showProjectNameTag ? session.project_path : (session.cwd || session.project_path) }}</p>
      </div>
      <div class="card-footer">
        <span class="card-time">{{ formatUpdatedAt(session.updated_at) }}</span>
        <button
          class="card-footer-action"
          type="button"
          @click.stop="emit('open-session', session)"
        >
          {{ t('components.projectManager.card.openSession') }}
        </button>
      </div>
    </article>
  </section>
</template>
