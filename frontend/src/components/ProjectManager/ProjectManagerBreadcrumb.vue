<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { ProjectSummary } from '../../services/projectManager'

defineProps<{
  project: ProjectSummary
}>()

const emit = defineEmits<{
  back: []
}>()

const { t } = useI18n()
</script>

<template>
  <section class="project-breadcrumb">
    <button class="back-chip" type="button" @click="emit('back')">
      ← {{ t('components.projectManager.toolbar.backToProjects') }}
    </button>
    <div class="breadcrumb-copy">
      <h2>{{ project.display_name }}</h2>
      <p>{{ project.path }}</p>
    </div>
  </section>
</template>
