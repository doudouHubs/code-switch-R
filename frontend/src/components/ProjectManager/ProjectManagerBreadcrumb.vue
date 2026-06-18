<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { ProjectSummary } from '../../services/projectManager'

defineProps<{
  project: ProjectSummary
  committing?: boolean
}>()

const emit = defineEmits<{
  back: []
  commit: []
}>()

const { t } = useI18n()
</script>

<template>
  <section class="project-breadcrumb">
    <div class="project-breadcrumb-main">
      <button class="back-chip" type="button" @click="emit('back')">
        ← {{ t('components.projectManager.toolbar.backToProjects') }}
      </button>
      <div class="breadcrumb-copy">
        <h2>{{ project.display_name }}</h2>
        <p>{{ project.path }}</p>
      </div>
    </div>

    <button
      class="breadcrumb-action-button"
      type="button"
      :disabled="committing"
      @click="emit('commit')"
    >
      <span v-if="committing" class="breadcrumb-action-spinner" aria-hidden="true"></span>
      <span>{{ t('components.projectManager.toolbar.aiCommit') }}</span>
    </button>
  </section>
</template>
