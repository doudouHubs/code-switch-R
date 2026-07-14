<script setup lang="ts">
import {
  computed,
  onBeforeUnmount,
  onMounted,
  reactive,
  ref,
  watch,
} from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import BaseButton from "../common/BaseButton.vue";
import BaseModal from "../common/BaseModal.vue";
import ProjectManagerBreadcrumb from "./ProjectManagerBreadcrumb.vue";
import ProjectManagerHeroPanel from "./ProjectManagerHeroPanel.vue";
import ProjectManagerProjectGrid from "./ProjectManagerProjectGrid.vue";
import ProjectManagerRenameModal from "./ProjectManagerRenameModal.vue";
import ProjectManagerSessionGrid from "./ProjectManagerSessionGrid.vue";
import ProjectManagerStatePanel from "./ProjectManagerStatePanel.vue";
import "./projectManager.css";
import {
  clearProjectCodexProvider,
  deleteProject,
  deleteSession,
  fetchProjectManagerSnapshot,
  openProjectFolder,
  openProjectTerminal,
  openSessionTerminal,
  runProjectAICommit,
  runProjectCommand,
  refreshProjectManagerSnapshot,
  renameProject,
  renameSession,
  saveProjectRunCommand,
  setProjectCodexProvider,
  type ProjectSummary,
  type ProjectManagerSnapshot,
  type SessionSummary,
} from "../../services/projectManager";
import { LoadProviders } from "../../../bindings/codeswitch/services/providerservice";
import type { Provider } from "../../../bindings/codeswitch/services/models";
import { extractErrorMessage } from "../../utils/error";
import { showToast } from "../../utils/toast";
import type {
  ProjectManagerRenameTarget,
  ProjectManagerViewMode,
} from "./types";

const { t, locale } = useI18n();
const router = useRouter();

const loading = ref(false);
const refreshing = ref(false);
const renameSaving = ref(false);
const providerSaving = ref(false);
const providerLoading = ref(false);
const openingSessionIds = ref<string[]>([]);
const openingProjectTerminalId = ref("");
const committingProjectId = ref("");
const runningProjectCommandId = ref("");
const deletingProjectIds = ref<string[]>([]);
const deletingSessionIds = ref<string[]>([]);
const snapshotProjects = ref<ProjectSummary[]>([]);
const snapshotSessions = ref<SessionSummary[]>([]);
const selectedProjectId = ref("");
const searchKeyword = ref("");
const activeMode = ref<ProjectManagerViewMode>("project");
const renameModalOpen = ref(false);
const renameTargetType = ref<ProjectManagerRenameTarget>("project");
const renameTargetId = ref("");
const renameValue = ref("");
const codexProviders = ref<Provider[]>([]);
const providerModalState = reactive({
  open: false,
  projectId: "",
  projectPath: "",
  projectName: "",
  selectedProviderId: 0,
  autoFallback: true,
});
const runCommandSaving = ref(false);
const runCommandModalState = reactive({
  open: false,
  projectId: "",
  projectPath: "",
  projectName: "",
  command: "",
  originalCommand: "",
});
const deleteState = reactive({
  open: false,
  targetType: "project" as "project" | "session",
  targetId: "",
  targetName: "",
  sessionCount: 0,
});

const projectManagerOpenTimeoutMs = 5000;
const projectManagerWarmRefreshDelayMs = 800;
const openingSessionTimers = new Map<string, ReturnType<typeof setTimeout>>();
let projectManagerWarmRefreshTimer: ReturnType<typeof setTimeout> | null = null;

const dateFormatter = computed(
  () =>
    new Intl.DateTimeFormat(locale.value === "zh" ? "zh-CN" : "en-US", {
      month: "short",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
    }),
);

const normalizedKeyword = computed(() =>
  searchKeyword.value.trim().toLowerCase(),
);

const selectedProject = computed(
  () =>
    snapshotProjects.value.find(
      (project) => project.id === selectedProjectId.value,
    ) ?? null,
);

const matchesKeyword = (fields: Array<string | undefined>, keyword: string) => {
  if (!keyword) {
    return true;
  }
  return fields.some((field) => (field ?? "").toLowerCase().includes(keyword));
};

const projectCards = computed(() => {
  const keyword = normalizedKeyword.value;
  return snapshotProjects.value.filter((project) =>
    matchesKeyword(
      [project.display_name, project.source_name, project.path],
      keyword,
    ),
  );
});

const currentProjectSessions = computed(() => {
  const projectId = selectedProjectId.value;
  const keyword = normalizedKeyword.value;
  return snapshotSessions.value.filter((session) => {
    if (projectId && session.project_id !== projectId) {
      return false;
    }
    return matchesKeyword(
      [
        session.display_name,
        session.source_name,
        session.project_name,
        session.project_path,
        session.summary,
      ],
      keyword,
    );
  });
});

const flatSessionCards = computed(() => {
  const keyword = normalizedKeyword.value;
  return snapshotSessions.value.filter((session) =>
    matchesKeyword(
      [
        session.display_name,
        session.source_name,
        session.project_name,
        session.project_path,
        session.summary,
      ],
      keyword,
    ),
  );
});

const showProjectGrid = computed(
  () => activeMode.value === "project" && !selectedProjectId.value,
);

const visibleSessions = computed(() =>
  activeMode.value === "session"
    ? flatSessionCards.value
    : currentProjectSessions.value,
);

const emptyStateMessage = computed(() => {
  if (loading.value) {
    return "";
  }
  if (showProjectGrid.value && projectCards.value.length === 0) {
    return t("components.projectManager.states.emptyProjects");
  }
  if (!showProjectGrid.value && visibleSessions.value.length === 0) {
    return t("components.projectManager.states.emptySessions");
  }
  return "";
});

watch(activeMode, (mode) => {
  // 会话总览和项目钻取是两套视角；切到会话模式时必须丢掉旧项目上下文，避免 breadcrumb 挂残影。
  if (mode === "session") {
    selectedProjectId.value = "";
  }
});

const loadSnapshot = async (isRefresh = false) => {
  if (isRefresh) {
    refreshing.value = true;
  } else {
    loading.value = true;
  }

  try {
    const snapshot = isRefresh
      ? await refreshProjectManagerSnapshot()
      : await fetchProjectManagerSnapshot();
    applySnapshot(snapshot);
  } catch (error) {
    console.error("failed to load project manager snapshot", error);
    showToast(extractErrorMessage(error), "error");
  } finally {
    loading.value = false;
    refreshing.value = false;
  }
};

const enterProject = (project: ProjectSummary) => {
  selectedProjectId.value = project.id;
  activeMode.value = "project";
};

const formatUpdatedAt = (timestamp: number) => {
  if (!timestamp) {
    return t("components.projectManager.common.unknownTime");
  }
  return dateFormatter.value.format(new Date(timestamp));
};

const openRenameModal = (
  type: ProjectManagerRenameTarget,
  payload: ProjectSummary | SessionSummary,
) => {
  renameTargetType.value = type;
  renameTargetId.value = payload.id;
  renameValue.value = payload.display_name;
  renameModalOpen.value = true;
};

const closeRenameModal = () => {
  if (renameSaving.value) {
    return;
  }
  renameModalOpen.value = false;
};

const openProviderModal = async (project: ProjectSummary) => {
  if (!project?.path || providerSaving.value) {
    return;
  }
  providerModalState.projectId = project.id;
  providerModalState.projectPath = project.path;
  providerModalState.projectName = project.display_name;
  providerModalState.selectedProviderId = project.codex_provider_id ?? 0;
  providerModalState.autoFallback = project.codex_provider_auto !== false;
  providerModalState.open = true;

  if (codexProviders.value.length > 0) {
    return;
  }

  providerLoading.value = true;
  try {
    const providers = await LoadProviders("codex");
    codexProviders.value = providers ?? [];
  } catch (error) {
    console.error("failed to load codex providers", error);
    showToast(extractErrorMessage(error), "error");
  } finally {
    providerLoading.value = false;
  }
};

const closeProviderModal = () => {
  if (providerSaving.value) {
    return;
  }
  providerModalState.open = false;
};

const openRunCommandModal = (project: ProjectSummary) => {
  if (!project?.path || runCommandSaving.value) {
    return;
  }
  const command = project.run_command ?? "";
  runCommandModalState.projectId = project.id;
  runCommandModalState.projectPath = project.path;
  runCommandModalState.projectName = project.display_name;
  runCommandModalState.command = command;
  runCommandModalState.originalCommand = command;
  runCommandModalState.open = true;
};

const closeRunCommandModal = () => {
  if (runCommandSaving.value) {
    return;
  }
  runCommandModalState.open = false;
};

const openDeleteModal = (
  type: "project" | "session",
  payload: ProjectSummary | SessionSummary,
) => {
  if (type === "project" && deletingProjectIds.value.includes(payload.id)) {
    return;
  }
  if (type === "session" && deletingSessionIds.value.includes(payload.id)) {
    return;
  }
  deleteState.targetType = type;
  deleteState.targetId = payload.id;
  deleteState.targetName = payload.display_name;
  deleteState.sessionCount =
    type === "project"
      ? snapshotSessions.value.filter(
          (session) => session.project_id === payload.id,
        ).length
      : 0;
  deleteState.open = true;
};

const closeDeleteModal = () => {
  deleteState.open = false;
};

const saveRename = async () => {
  const value = renameValue.value.trim();
  if (!value) {
    showToast(t("components.projectManager.rename.emptyName"), "warning");
    return;
  }

  renameSaving.value = true;
  try {
    if (renameTargetType.value === "project") {
      const target = snapshotProjects.value.find(
        (project) => project.id === renameTargetId.value,
      );
      if (!target) {
        throw new Error(t("components.projectManager.errors.projectNotFound"));
      }
      await renameProject(target.path, value);
    } else {
      await renameSession(renameTargetId.value, value);
    }
    await loadSnapshot(true);
    renameModalOpen.value = false;
    showToast(t("components.projectManager.rename.saved"), "success");
  } catch (error) {
    console.error("failed to rename entity", error);
    showToast(extractErrorMessage(error), "error");
  } finally {
    renameSaving.value = false;
  }
};

const saveProjectCodexProvider = async () => {
  const projectPath = providerModalState.projectPath;
  const providerId = providerModalState.selectedProviderId;
  if (!projectPath) {
    showToast(t("components.projectManager.errors.projectNotFound"), "error");
    return;
  }

  providerSaving.value = true;
  try {
    if (providerId > 0) {
      await setProjectCodexProvider(
        projectPath,
        providerId,
        providerModalState.autoFallback,
      );
    } else {
      await clearProjectCodexProvider(projectPath);
    }

    const providerName =
      codexProviders.value.find((provider) => provider.id === providerId)?.name ??
      "";
    snapshotProjects.value = snapshotProjects.value.map((project) => {
      if (project.path !== projectPath) {
        return project;
      }
      return {
        ...project,
        codex_provider_id: providerId || undefined,
        codex_provider_name: providerName || undefined,
        codex_provider_auto: providerId > 0 ? providerModalState.autoFallback : true,
      };
    });
    providerModalState.open = false;
    showToast(t("components.projectManager.provider.saved"), "success");
    await loadSnapshot(true);
  } catch (error) {
    console.error("failed to save project codex provider", error);
    showToast(extractErrorMessage(error), "error");
  } finally {
    providerSaving.value = false;
  }
};

const saveProjectRunCommandSetting = async () => {
  const projectPath = runCommandModalState.projectPath;
  const command = runCommandModalState.command.trim();
  const hadCommand = !!runCommandModalState.originalCommand.trim();
  if (!projectPath) {
    showToast(t("components.projectManager.errors.projectNotFound"), "error");
    return;
  }
  if (!command && !hadCommand) {
    showToast(t("components.projectManager.runCommand.emptyCommand"), "warning");
    return;
  }

  runCommandSaving.value = true;
  try {
    await saveProjectRunCommand(projectPath, command);
    snapshotProjects.value = snapshotProjects.value.map((project) => {
      if (project.path !== projectPath) {
        return project;
      }
      return {
        ...project,
        run_command: command || undefined,
      };
    });
    runCommandModalState.open = false;
    showToast(
      command
        ? t("components.projectManager.runCommand.saved")
        : t("components.projectManager.runCommand.cleared"),
      "success",
    );
    await loadSnapshot(true);
  } catch (error) {
    console.error("failed to save project run command", error);
    showToast(extractErrorMessage(error), "error");
  } finally {
    runCommandSaving.value = false;
  }
};

const applySnapshot = (snapshot: ProjectManagerSnapshot) => {
  snapshotProjects.value = snapshot.projects;
  snapshotSessions.value = snapshot.sessions;

  // 选中的项目如果已经被删除或重建，必须同步退出详情视角；
  // 否则界面会挂着一个失效 breadcrumb，看着就跟坏了一样。
  if (
    selectedProjectId.value &&
    !snapshot.projects.some((project) => project.id === selectedProjectId.value)
  ) {
    selectedProjectId.value = "";
  }
};

const scheduleSilentWarmRefresh = () => {
  if (projectManagerWarmRefreshTimer) {
    clearTimeout(projectManagerWarmRefreshTimer);
  }

  // 首屏先吃缓存，随后静默强制走一次增量刷新；
  // 这里不能再调 GetSnapshot，不然很可能第二次还是吃到旧缓存，等于刷新了个寂寞。
  projectManagerWarmRefreshTimer = setTimeout(async () => {
    try {
      const snapshot = await refreshProjectManagerSnapshot();
      applySnapshot(snapshot);
    } catch (error) {
      console.error(
        "failed to perform silent project manager warm refresh",
        error,
      );
    } finally {
      projectManagerWarmRefreshTimer = null;
    }
  }, projectManagerWarmRefreshDelayMs);
};

const confirmDelete = async () => {
  const targetType = deleteState.targetType;
  const targetId = deleteState.targetId;
  const targetName = deleteState.targetName;
  const projectTarget =
    targetType === "project"
      ? (snapshotProjects.value.find((project) => project.id === targetId) ??
        null)
      : null;
  const sessionTarget =
    targetType === "session"
      ? (snapshotSessions.value.find((session) => session.id === targetId) ??
        null)
      : null;

  deleteState.open = false;

  if (targetType === "project") {
    if (!projectTarget) {
      showToast(t("components.projectManager.errors.projectNotFound"), "error");
      return;
    }
    deletingProjectIds.value = [...deletingProjectIds.value, targetId];
  } else {
    if (!sessionTarget) {
      showToast(t("components.projectManager.errors.sessionNotFound"), "error");
      return;
    }
    deletingSessionIds.value = [...deletingSessionIds.value, targetId];
  }

  try {
    if (targetType === "project" && projectTarget) {
      await deleteProject(projectTarget.path);
      snapshotProjects.value = snapshotProjects.value.filter(
        (project) => project.id !== projectTarget.id,
      );
      snapshotSessions.value = snapshotSessions.value.filter(
        (session) => session.project_id !== projectTarget.id,
      );
      if (selectedProjectId.value === projectTarget.id) {
        selectedProjectId.value = "";
      }
      showToast(
        t("components.projectManager.delete.projectDeleted"),
        "success",
      );
    } else if (sessionTarget) {
      await deleteSession(sessionTarget.id);
      snapshotSessions.value = snapshotSessions.value.filter(
        (session) => session.id !== sessionTarget.id,
      );
      snapshotProjects.value = snapshotProjects.value.map((project) => {
        if (project.id !== sessionTarget.project_id) {
          return project;
        }
        return {
          ...project,
          session_count: Math.max(0, project.session_count - 1),
        };
      });
      showToast(
        t("components.projectManager.delete.sessionDeleted"),
        "success",
      );
    }
  } catch (error) {
    console.error("failed to delete entity", error);
    if (targetType === "project") {
      showToast(
        targetName
          ? `${targetName}: ${extractErrorMessage(error)}`
          : extractErrorMessage(error),
        "error",
      );
    } else {
      showToast(
        targetName
          ? `${targetName}: ${extractErrorMessage(error)}`
          : extractErrorMessage(error),
        "error",
      );
    }
  } finally {
    if (targetType === "project") {
      deletingProjectIds.value = deletingProjectIds.value.filter(
        (id) => id !== targetId,
      );
    } else {
      deletingSessionIds.value = deletingSessionIds.value.filter(
        (id) => id !== targetId,
      );
    }
  }
};

const handleOpenProjectFolder = async (project: ProjectSummary) => {
  try {
    await openProjectFolder(project.path);
  } catch (error) {
    console.error("failed to open project folder", error);
    showToast(extractErrorMessage(error), "error");
  }
};

const handleOpenProjectTerminal = async (project: ProjectSummary) => {
  if (!project?.path || openingProjectTerminalId.value === project.id) {
    return;
  }

  // 这里单独走“项目级新终端”链路，明确只负责在项目目录新开 codex，
  // 不复用会话 resume 逻辑，避免头部按钮把用户带回某个旧会话。
  openingProjectTerminalId.value = project.id;
  const timeoutId = setTimeout(() => {
    if (openingProjectTerminalId.value === project.id) {
      openingProjectTerminalId.value = "";
    }
  }, projectManagerOpenTimeoutMs);

  try {
    await openProjectTerminal(project.path);
  } catch (error) {
    console.error("failed to open project terminal", error);
    showToast(extractErrorMessage(error), "error");
  } finally {
    clearTimeout(timeoutId);
    if (openingProjectTerminalId.value === project.id) {
      openingProjectTerminalId.value = "";
    }
  }
};

const handleRunProjectCommand = async (project: ProjectSummary) => {
  if (!project?.path || runningProjectCommandId.value === project.id) {
    return;
  }

  if (!project.run_command?.trim()) {
    openRunCommandModal(project);
    return;
  }

  runningProjectCommandId.value = project.id;
  try {
    await runProjectCommand(project.path);
    showToast(t("components.projectManager.runCommand.started"), "success");
  } catch (error) {
    console.error("failed to run project command", error);
    showToast(extractErrorMessage(error), "error");
  } finally {
    runningProjectCommandId.value = "";
  }
};

const handleRunProjectAICommit = async (project: ProjectSummary) => {
  if (!project?.path || committingProjectId.value === project.id) {
    return;
  }

  committingProjectId.value = project.id;
  try {
    await runProjectAICommit(project.path);
    showToast(t("components.projectManager.commit.started"), "success");
  } catch (error) {
    console.error("failed to run project ai commit", error);
    showToast(extractErrorMessage(error), "error");
  } finally {
    committingProjectId.value = "";
  }
};

const handleOpenSession = async (session: SessionSummary) => {
  if (openingSessionIds.value.includes(session.id)) {
    return;
  }

  // 点击打开终端必须立刻给出反馈，不然用户只会觉得按钮死了。
  // 这里把 loading 做成会话级别，避免一个请求把所有卡片都锁住。
  openingSessionIds.value = [...openingSessionIds.value, session.id];
  const timeoutId = setTimeout(() => {
    clearOpeningSession(session.id);
  }, projectManagerOpenTimeoutMs);
  openingSessionTimers.set(session.id, timeoutId);

  try {
    await openSessionTerminal(session);
  } catch (error) {
    console.error("failed to open session terminal", error);
    showToast(extractErrorMessage(error), "error");
  } finally {
    clearOpeningSession(session.id);
  }
};

const openSessionDetail = (session: SessionSummary) => {
  router.push(`/projects/sessions/${encodeURIComponent(session.id)}`);
};

const clearOpeningSession = (sessionID: string) => {
  const timeoutId = openingSessionTimers.get(sessionID);
  if (timeoutId) {
    clearTimeout(timeoutId);
    openingSessionTimers.delete(sessionID);
  }
  if (!openingSessionIds.value.includes(sessionID)) {
    return;
  }
  openingSessionIds.value = openingSessionIds.value.filter(
    (id) => id !== sessionID,
  );
};

const isSessionOpening = (sessionID: string) =>
  openingSessionIds.value.includes(sessionID);
const isProjectDeleting = (projectID: string) =>
  deletingProjectIds.value.includes(projectID);
const isProjectCommitting = (projectID: string) =>
  committingProjectId.value === projectID;
const isProjectRunning = (projectID: string) =>
  runningProjectCommandId.value === projectID;
const isSessionDeleting = (sessionID: string) =>
  deletingSessionIds.value.includes(sessionID);

const resolveSessionSummary = (session: SessionSummary) =>
  session.summary || t("components.projectManager.common.emptySummary");

onMounted(() => {
  loadSnapshot();
  scheduleSilentWarmRefresh();
});

onBeforeUnmount(() => {
  openingSessionTimers.forEach((timeoutId) => {
    clearTimeout(timeoutId);
  });
  openingSessionTimers.clear();
  if (projectManagerWarmRefreshTimer) {
    clearTimeout(projectManagerWarmRefreshTimer);
    projectManagerWarmRefreshTimer = null;
  }
});
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
      :opening-terminal="openingProjectTerminalId === selectedProject.id"
      :committing="committingProjectId === selectedProject.id"
      @back="selectedProjectId = ''"
      @open-terminal="handleOpenProjectTerminal(selectedProject)"
      @commit="handleRunProjectAICommit(selectedProject)"
    />

    <ProjectManagerStatePanel
      v-if="loading || emptyStateMessage"
      :loading="loading"
      :message="
        loading
          ? t('components.projectManager.states.loading')
          : emptyStateMessage
      "
    />

    <ProjectManagerProjectGrid
      v-else-if="showProjectGrid"
      :projects="projectCards"
      :format-updated-at="formatUpdatedAt"
      :is-project-deleting="isProjectDeleting"
      :is-project-committing="isProjectCommitting"
      :is-project-running="isProjectRunning"
      @enter="enterProject"
      @delete="openDeleteModal('project', $event)"
      @open-folder="handleOpenProjectFolder"
      @set-codex-provider="openProviderModal"
      @run-command="handleRunProjectCommand"
      @edit-run-command="openRunCommandModal"
      @commit="handleRunProjectAICommit"
    />

    <ProjectManagerSessionGrid
      v-else
      :sessions="visibleSessions"
      :format-updated-at="formatUpdatedAt"
      :resolve-summary="resolveSessionSummary"
      :show-project-name-tag="activeMode === 'session'"
      :is-session-opening="isSessionOpening"
      :is-session-deleting="isSessionDeleting"
      @delete="openDeleteModal('session', $event)"
      @rename="openRenameModal('session', $event)"
      @open-session="handleOpenSession"
      @open-detail="openSessionDetail"
    />

    <ProjectManagerRenameModal
      v-model="renameValue"
      :open="renameModalOpen"
      :target-type="renameTargetType"
      :saving="renameSaving"
      @close="closeRenameModal"
      @save="saveRename"
    />

    <BaseModal
      :open="providerModalState.open"
      :title="t('components.projectManager.provider.title')"
      @close="closeProviderModal"
    >
      <div class="project-provider-modal-body">
        <p class="rename-hint">
          {{
            t("components.projectManager.provider.hint", {
              name: providerModalState.projectName,
            })
          }}
        </p>

        <div v-if="providerLoading" class="provider-picker-state">
          {{ t("components.projectManager.provider.loading") }}
        </div>
        <div v-else class="provider-picker-list">
          <label class="provider-picker-option">
            <input
              v-model.number="providerModalState.selectedProviderId"
              type="radio"
              :value="0"
            />
            <span>
              <strong>{{ t("components.projectManager.provider.defaultOption") }}</strong>
              <small>{{ t("components.projectManager.provider.defaultHint") }}</small>
            </span>
          </label>

          <label
            v-for="provider in codexProviders"
            :key="provider.id"
            class="provider-picker-option"
          >
            <input
              v-model.number="providerModalState.selectedProviderId"
              type="radio"
              :value="provider.id"
            />
            <span>
              <strong>
                {{ provider.name }}
                <em v-if="!provider.enabled" class="provider-picker-disabled-badge">
                  {{ t("components.projectManager.provider.disabledBadge") }}
                </em>
              </strong>
              <small>{{ provider.apiUrl }}</small>
            </span>
          </label>

          <p v-if="codexProviders.length === 0" class="provider-picker-empty">
            {{ t("components.projectManager.provider.empty") }}
          </p>
        </div>

        <label
          class="provider-auto-switch"
          :class="{ 'is-disabled': providerModalState.selectedProviderId <= 0 }"
        >
          <span>
            <strong>{{ t("components.projectManager.provider.autoLabel") }}</strong>
            <small>{{ t("components.projectManager.provider.autoHint") }}</small>
          </span>
          <input
            v-model="providerModalState.autoFallback"
            type="checkbox"
            :disabled="providerModalState.selectedProviderId <= 0"
          />
          <i aria-hidden="true"></i>
        </label>

        <footer class="form-actions rename-actions">
          <BaseButton variant="outline" type="button" @click="closeProviderModal">
            {{ t("components.projectManager.rename.cancel") }}
          </BaseButton>
          <BaseButton
            type="button"
            :disabled="providerLoading || providerSaving"
            :loading="providerSaving"
            @click="saveProjectCodexProvider"
          >
            {{ t("components.projectManager.provider.save") }}
          </BaseButton>
        </footer>
      </div>
    </BaseModal>

    <BaseModal
      :open="runCommandModalState.open"
      :title="t('components.projectManager.runCommand.title')"
      @close="closeRunCommandModal"
    >
      <div class="project-run-command-modal-body">
        <p class="rename-hint">
          {{
            t("components.projectManager.runCommand.hint", {
              name: runCommandModalState.projectName,
            })
          }}
        </p>

        <label class="project-run-command-field">
          <span>{{ t("components.projectManager.runCommand.commandLabel") }}</span>
          <textarea
            v-model="runCommandModalState.command"
            :placeholder="t('components.projectManager.runCommand.placeholder')"
            rows="7"
            @keydown.stop
          ></textarea>
        </label>

        <p class="project-run-command-footnote">
          {{ t("components.projectManager.runCommand.footnote") }}
        </p>

        <footer class="form-actions rename-actions">
          <BaseButton variant="outline" type="button" @click="closeRunCommandModal">
            {{ t("components.projectManager.rename.cancel") }}
          </BaseButton>
          <BaseButton
            type="button"
            :disabled="runCommandSaving"
            :loading="runCommandSaving"
            @click="saveProjectRunCommandSetting"
          >
            {{ t("components.projectManager.runCommand.save") }}
          </BaseButton>
        </footer>
      </div>
    </BaseModal>

    <BaseModal
      :open="deleteState.open"
      :title="
        deleteState.targetType === 'project'
          ? t('components.projectManager.delete.projectTitle')
          : t('components.projectManager.delete.sessionTitle')
      "
      variant="confirm"
      @close="closeDeleteModal"
    >
      <div class="rename-body">
        <div class="confirm-body">
          <p v-if="deleteState.targetType === 'project'">
            {{
              t("components.projectManager.delete.projectConfirm", {
                name: deleteState.targetName,
                count: deleteState.sessionCount,
              })
            }}
          </p>
          <p v-else>
            {{
              t("components.projectManager.delete.sessionConfirm", {
                name: deleteState.targetName,
              })
            }}
          </p>
          <p class="detail-delete-hint">
            {{ t("components.projectManager.delete.hint") }}
          </p>
        </div>
        <footer class="form-actions confirm-actions">
          <BaseButton variant="outline" type="button" @click="closeDeleteModal">
            {{ t("components.projectManager.rename.cancel") }}
          </BaseButton>
          <BaseButton variant="danger" type="button" @click="confirmDelete">
            {{ t("components.projectManager.delete.confirmAction") }}
          </BaseButton>
        </footer>
      </div>
    </BaseModal>
  </div>
</template>
