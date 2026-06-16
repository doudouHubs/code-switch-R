<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { ProjectSummary } from '../../services/projectManager'

defineProps<{
  projects: ProjectSummary[]
  formatUpdatedAt: (timestamp: number) => string
}>()

const emit = defineEmits<{
  enter: [project: ProjectSummary]
  rename: [project: ProjectSummary]
  'open-folder': [project: ProjectSummary]
  'view-path': [project: ProjectSummary]
}>()

const { t } = useI18n()
</script>

<template>
  <section class="project-grid">
    <article
      v-for="project in projects"
      :key="project.id"
      class="project-card"
      @click="emit('enter', project)"
    >
      <div class="card-topline">
        <span class="card-tag">{{ t('components.projectManager.card.projectTag') }}</span>
        <span class="card-time">{{ formatUpdatedAt(project.updated_at) }}</span>
      </div>
      <h3 class="card-title">{{ project.display_name }}</h3>
      <p class="card-path">{{ project.path }}</p>
      <div class="card-metrics">
        <div class="metric-actions" @click.stop>
          <button class="mini-action" type="button" @click="emit('rename', project)">
            {{ t('components.projectManager.card.rename') }}
          </button>
          <button class="mini-action" type="button" @click="emit('open-folder', project)">
            {{ t('components.projectManager.card.openFolder') }}
          </button>
          <button class="mini-action" type="button" @click="emit('view-path', project)">
            {{ t('components.projectManager.card.pathDetail') }}
          </button>
        </div>
      </div>
    </article>
  </section>
</template>
