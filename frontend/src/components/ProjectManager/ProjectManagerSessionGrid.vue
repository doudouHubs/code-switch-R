<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { SessionSummary } from '../../services/projectManager'

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
        <span class="card-tag">
          {{ showProjectNameTag ? session.project_name : t('components.projectManager.card.sessionTag') }}
        </span>
        <span class="card-time">{{ formatUpdatedAt(session.updated_at) }}</span>
      </div>
      <h3 class="card-title">{{ session.display_name }}</h3>
      <p class="card-summary">{{ resolveSummary(session) }}</p>
      <p class="card-path small">{{ showProjectNameTag ? session.project_path : (session.cwd || session.project_path) }}</p>
      <div class="card-metrics" @click.stop>
        <div class="metric-actions">
          <button class="mini-action" type="button" @click="emit('rename', session)">
            {{ t('components.projectManager.card.rename') }}
          </button>
          <button class="mini-action accent" type="button" @click="emit('open-session', session)">
            {{ t('components.projectManager.card.openSession') }}
          </button>
        </div>
      </div>
    </article>
  </section>
</template>
