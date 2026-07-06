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
  isProjectCommitting: (projectId: string) => boolean
}>()

const emit = defineEmits<{
  enter: [project: ProjectSummary]
  delete: [project: ProjectSummary]
  'open-folder': [project: ProjectSummary]
  'set-codex-provider': [project: ProjectSummary]
  commit: [project: ProjectSummary]
}>()

const { t } = useI18n()

const resolveProjectActions = (project: ProjectSummary): ProjectManagerCardMenuAction[] => [
  {
    key: `set-codex-provider:${project.id}`,
    label: t('components.projectManager.card.setCodexProvider'),
    accent: true,
  },
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
  if (actionKey.startsWith('set-codex-provider:')) {
    emit('set-codex-provider', project)
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
        <p class="card-provider">
          <span>{{ t('components.projectManager.card.codexProvider') }}</span>
          <strong>
            {{
              project.codex_provider_name ||
              t('components.projectManager.card.codexProviderDefault')
            }}
          </strong>
        </p>
      </div>
      <div class="card-footer">
        <span class="card-time">{{ formatUpdatedAt(project.updated_at) }}</span>
        <div class="card-footer-actions">
          <button
            class="card-footer-action"
            type="button"
            :disabled="isProjectDeleting(project.id)"
            @click.stop="emit('open-folder', project)"
          >
            {{ t('components.projectManager.card.openFolder') }}
          </button>
          <button
            :class="['card-footer-action', { 'is-loading': isProjectCommitting(project.id) }]"
            type="button"
            :disabled="isProjectDeleting(project.id) || isProjectCommitting(project.id)"
            @click.stop="emit('commit', project)"
          >
            <span
              v-if="isProjectCommitting(project.id)"
              class="card-footer-spinner"
              aria-hidden="true"
            ></span>
            <span>{{ t('components.projectManager.toolbar.aiCommit') }}</span>
          </button>
        </div>
      </div>
    </article>
  </section>
</template>
