<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import ProjectManagerCardMenu from './ProjectManagerCardMenu.vue'
import type { ProjectSummary } from '../../services/projectManager'

type ProjectManagerCardMenuAction = {
  key: string
  label: string
  accent?: boolean
  danger?: boolean
}

defineProps<{
  projects: ProjectSummary[]
  formatUpdatedAt: (timestamp: number) => string
  isProjectDeleting: (projectId: string) => boolean
}>()

const emit = defineEmits<{
  enter: [project: ProjectSummary]
  delete: [project: ProjectSummary]
  'open-folder': [project: ProjectSummary]
}>()

const { t } = useI18n()

const resolveProjectActions = (project: ProjectSummary): ProjectManagerCardMenuAction[] => [
  {
    key: `delete:${project.id}`,
    label: t('components.projectManager.card.deleteProject'),
    danger: true,
  },
]

const handleProjectAction = (project: ProjectSummary, actionKey: string) => {
  if (actionKey.startsWith('open-folder:')) {
    emit('open-folder', project)
    return
  }
  if (actionKey.startsWith('delete:')) {
    emit('delete', project)
  }
}
</script>

<template>
  <section class="project-grid">
    <article
      v-for="project in projects"
      :key="project.id"
      :class="['project-card', { 'is-deleting': isProjectDeleting(project.id) }]"
      @click="!isProjectDeleting(project.id) && emit('enter', project)"
    >
      <div class="card-topline">
        <h3 class="card-title">{{ project.display_name }}</h3>
        <ProjectManagerCardMenu
          :label="t('components.projectManager.card.moreActions')"
          :actions="resolveProjectActions(project)"
          :loading="isProjectDeleting(project.id)"
          :disabled="isProjectDeleting(project.id)"
          @select="handleProjectAction(project, $event)"
        />
      </div>
      <div class="card-copy">
        <p class="card-path">{{ project.path }}</p>
      </div>
      <div class="card-footer">
        <span class="card-time">{{ formatUpdatedAt(project.updated_at) }}</span>
        <button
          class="card-footer-action"
          type="button"
          :disabled="isProjectDeleting(project.id)"
          @click.stop="emit('open-folder', project)"
        >
          {{ t('components.projectManager.card.openFolder') }}
        </button>
      </div>
    </article>
  </section>
</template>
