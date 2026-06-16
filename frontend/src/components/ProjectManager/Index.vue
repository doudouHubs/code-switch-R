<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { Dialogs } from '@wailsio/runtime'
import { useI18n } from 'vue-i18n'
import ProjectManagerBreadcrumb from './ProjectManagerBreadcrumb.vue'
import ProjectManagerHeroPanel from './ProjectManagerHeroPanel.vue'
import ProjectManagerProjectGrid from './ProjectManagerProjectGrid.vue'
import ProjectManagerRenameModal from './ProjectManagerRenameModal.vue'
import ProjectManagerSessionGrid from './ProjectManagerSessionGrid.vue'
import ProjectManagerStatePanel from './ProjectManagerStatePanel.vue'
import './projectManager.css'
import {
  fetchProjectManagerSnapshot,
  openProjectFolder,
  openSessionTerminal,
  refreshProjectManagerSnapshot,
  renameProject,
  renameSession,
  type ProjectSummary,
  type SessionSummary,
} from '../../services/projectManager'
import { extractErrorMessage } from '../../utils/error'
import { showToast } from '../../utils/toast'
import type { ProjectManagerRenameTarget, ProjectManagerViewMode } from './types'

const { t, locale } = useI18n()

const loading = ref(false)
const refreshing = ref(false)
const renameSaving = ref(false)
const snapshotProjects = ref<ProjectSummary[]>([])
const snapshotSessions = ref<SessionSummary[]>([])
const selectedProjectId = ref('')
const searchKeyword = ref('')
const activeMode = ref<ProjectManagerViewMode>('project')
const renameModalOpen = ref(false)
const renameTargetType = ref<ProjectManagerRenameTarget>('project')
const renameTargetId = ref('')
const renameValue = ref('')

const dateFormatter = computed(() =>
  new Intl.DateTimeFormat(locale.value === 'zh' ? 'zh-CN' : 'en-US', {
    month: 'short',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }),
)

const normalizedKeyword = computed(() => searchKeyword.value.trim().toLowerCase())

const selectedProject = computed(() =>
  snapshotProjects.value.find(project => project.id === selectedProjectId.value) ?? null,
)

const matchesKeyword = (fields: Array<string | undefined>, keyword: string) => {
  if (!keyword) {
    return true
  }
  return fields.some(field => (field ?? '').toLowerCase().includes(keyword))
}

const projectCards = computed(() => {
  const keyword = normalizedKeyword.value
  return snapshotProjects.value.filter(project => matchesKeyword([
    project.display_name,
    project.source_name,
    project.path,
  ], keyword))
})

const currentProjectSessions = computed(() => {
  const projectId = selectedProjectId.value
  const keyword = normalizedKeyword.value
  return snapshotSessions.value.filter(session => {
    if (projectId && session.project_id !== projectId) {
      return false
    }
    return matchesKeyword([
      session.display_name,
      session.source_name,
      session.project_name,
      session.project_path,
      session.summary,
    ], keyword)
  })
})

const flatSessionCards = computed(() => {
  const keyword = normalizedKeyword.value
  return snapshotSessions.value.filter(session => matchesKeyword([
    session.display_name,
    session.source_name,
    session.project_name,
    session.project_path,
    session.summary,
  ], keyword))
})

const showProjectGrid = computed(() =>
  activeMode.value === 'project' && !selectedProjectId.value,
)

const visibleSessions = computed(() =>
  activeMode.value === 'session' ? flatSessionCards.value : currentProjectSessions.value,
)

const emptyStateMessage = computed(() => {
  if (loading.value) {
    return ''
  }
  if (showProjectGrid.value && projectCards.value.length === 0) {
    return t('components.projectManager.states.emptyProjects')
  }
  if (!showProjectGrid.value && visibleSessions.value.length === 0) {
    return t('components.projectManager.states.emptySessions')
  }
  return ''
})

watch(activeMode, mode => {
  // 会话总览和项目钻取是两套视角；切到会话模式时必须丢掉旧项目上下文，避免 breadcrumb 挂残影。
  if (mode === 'session') {
    selectedProjectId.value = ''
  }
})

const loadSnapshot = async (isRefresh = false) => {
  if (isRefresh) {
    refreshing.value = true
  } else {
    loading.value = true
  }

  try {
    const snapshot = isRefresh
      ? await refreshProjectManagerSnapshot()
      : await fetchProjectManagerSnapshot()
    snapshotProjects.value = snapshot.projects
    snapshotSessions.value = snapshot.sessions

    if (selectedProjectId.value && !snapshot.projects.some(project => project.id === selectedProjectId.value)) {
      selectedProjectId.value = ''
    }
  } catch (error) {
    console.error('failed to load project manager snapshot', error)
    showToast(extractErrorMessage(error), 'error')
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

const enterProject = (project: ProjectSummary) => {
  selectedProjectId.value = project.id
  activeMode.value = 'project'
}

const formatUpdatedAt = (timestamp: number) => {
  if (!timestamp) {
    return t('components.projectManager.common.unknownTime')
  }
  return dateFormatter.value.format(new Date(timestamp))
}

const openRenameModal = (type: ProjectManagerRenameTarget, payload: ProjectSummary | SessionSummary) => {
  renameTargetType.value = type
  renameTargetId.value = payload.id
  renameValue.value = payload.display_name
  renameModalOpen.value = true
}

const closeRenameModal = () => {
  if (renameSaving.value) {
    return
  }
  renameModalOpen.value = false
}

const saveRename = async () => {
  const value = renameValue.value.trim()
  if (!value) {
    showToast(t('components.projectManager.rename.emptyName'), 'warning')
    return
  }

  renameSaving.value = true
  try {
    if (renameTargetType.value === 'project') {
      const target = snapshotProjects.value.find(project => project.id === renameTargetId.value)
      if (!target) {
        throw new Error(t('components.projectManager.errors.projectNotFound'))
      }
      await renameProject(target.path, value)
    } else {
      await renameSession(renameTargetId.value, value)
    }
    await loadSnapshot(true)
    renameModalOpen.value = false
    showToast(t('components.projectManager.rename.saved'), 'success')
  } catch (error) {
    console.error('failed to rename entity', error)
    showToast(extractErrorMessage(error), 'error')
  } finally {
    renameSaving.value = false
  }
}

const handleOpenProjectFolder = async (project: ProjectSummary) => {
  try {
    await openProjectFolder(project.path)
  } catch (error) {
    console.error('failed to open project folder', error)
    showToast(extractErrorMessage(error), 'error')
  }
}

const handleOpenSession = async (session: SessionSummary) => {
  try {
    await openSessionTerminal(session.id)
  } catch (error) {
    console.error('failed to open session terminal', error)
    showToast(extractErrorMessage(error), 'error')
  }
}

const resolveSessionSummary = (session: SessionSummary) =>
  session.summary || t('components.projectManager.common.emptySummary')

const viewProjectRawPath = async (project: ProjectSummary) => {
  await Dialogs.Info({
    Title: t('components.projectManager.common.path'),
    Message: project.path,
  })
}

onMounted(() => {
  loadSnapshot()
})
</script>

<template>
  <div class="project-manager-page">
    <ProjectManagerHeroPanel
      v-model="searchKeyword"
      :active-mode="activeMode"
      :refreshing="refreshing"
      @change-mode="activeMode = $event"
      @clear="searchKeyword = ''"
      @refresh="loadSnapshot(true)"
    />

    <ProjectManagerBreadcrumb
      v-if="selectedProject && activeMode === 'project'"
      :project="selectedProject"
      @back="selectedProjectId = ''"
    />

    <ProjectManagerStatePanel
      v-if="loading || emptyStateMessage"
      :loading="loading"
      :message="loading ? t('components.projectManager.states.loading') : emptyStateMessage"
    />

    <ProjectManagerProjectGrid
      v-else-if="showProjectGrid"
      :projects="projectCards"
      :format-updated-at="formatUpdatedAt"
      @enter="enterProject"
      @rename="openRenameModal('project', $event)"
      @open-folder="handleOpenProjectFolder"
      @view-path="viewProjectRawPath"
    />

    <ProjectManagerSessionGrid
      v-else
      :sessions="visibleSessions"
      :format-updated-at="formatUpdatedAt"
      :resolve-summary="resolveSessionSummary"
      :show-project-name-tag="activeMode === 'session'"
      @rename="openRenameModal('session', $event)"
      @open-session="handleOpenSession"
    />

    <ProjectManagerRenameModal
      v-model="renameValue"
      :open="renameModalOpen"
      :target-type="renameTargetType"
      :saving="renameSaving"
      @close="closeRenameModal"
      @save="saveRename"
    />
  </div>
</template>
