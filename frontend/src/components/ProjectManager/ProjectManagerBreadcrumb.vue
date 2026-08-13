<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { ProjectSummary } from '../../services/projectManager'

defineProps<{
  project: ProjectSummary
  openingTerminal?: boolean
  committing?: boolean
}>()

const emit = defineEmits<{
  back: []
  openTerminal: []
  commit: []
}>()

const { t } = useI18n()
</script>

<template>
  <section class="project-breadcrumb">
    <div class="project-breadcrumb-main">
      <button
        class="back-chip"
        type="button"
        :aria-label="t('components.projectManager.toolbar.backToProjects')"
        :title="t('components.projectManager.toolbar.backToProjects')"
        @click="emit('back')"
      >
        <svg viewBox="0 0 20 20" aria-hidden="true">
          <path
            d="M11.75 4.75L6.5 10l5.25 5.25"
            fill="none"
            stroke="currentColor"
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="1.9"
          />
        </svg>
      </button>
      <div class="breadcrumb-copy">
        <h2>{{ project.display_name }}</h2>
        <p>{{ project.path }}</p>
      </div>
    </div>

    <div class="breadcrumb-actions">
      <button
        class="breadcrumb-action-button"
        type="button"
        :disabled="openingTerminal"
        @click="emit('openTerminal')"
      >
        <span v-if="openingTerminal" class="breadcrumb-action-spinner" aria-hidden="true"></span>
        <span>{{ t('components.projectManager.toolbar.openTerminal') }}</span>
      </button>

      <button
        class="breadcrumb-action-button"
        type="button"
        :disabled="committing"
        @click="emit('commit')"
      >
        <span v-if="committing" class="breadcrumb-action-spinner" aria-hidden="true"></span>
        <span>{{ t('components.projectManager.toolbar.aiCommit') }}</span>
      </button>
    </div>
  </section>
</template>
