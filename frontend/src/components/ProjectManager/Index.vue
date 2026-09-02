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
import { Events } from "@wailsio/runtime";
import BaseButton from "../common/BaseButton.vue";
import BaseModal from "../common/BaseModal.vue";
import ProjectManagerBreadcrumb from "./ProjectManagerBreadcrumb.vue";
import ProjectManagerHeroPanel from "./ProjectManagerHeroPanel.vue";
import ProjectManagerProjectGrid from "./ProjectManagerProjectGrid.vue";
import ProjectManagerRenameModal from "./ProjectManagerRenameModal.vue";
import ProjectManagerSessionGrid from "./ProjectManagerSessionGrid.vue";
import ProjectManagerSessionSelectionDock from "./ProjectManagerSessionSelectionDock.vue";
import ProjectManagerStatePanel from "./ProjectManagerStatePanel.vue";
import "./projectManager.css";
import {
  clearProjectCodexProvider,
  deleteProject,
  deleteSession,
  fetchCodexRuntimeStatusSnapshot,
  fetchProjectManagerSnapshot,
  openProjectFolder,
  openProjectTerminal,
  openSessionTerminal,
  runProjectAICommit,
  runProjectCommand,
  refreshProjectManagerSnapshot,
  renameProject,
  renameSession,
  pruneSessionConversationsByRange,
  searchProjectSessionConversations,
  saveProjectRunCommand,
  setProjectCodexProvider,
  type ProjectSummary,
  type ProjectManagerSnapshot,
  type ProjectSessionSearchResult,
  type SessionSummary,
  type CodexProjectRuntimeStatus,
  type CodexRuntimeState,
  type CodexRuntimeStatusSnapshot,
  type CodexSessionRuntimeStatus,
  type SessionDeletionRange,
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
const sessionSelectionMode = ref(false);
const selectedSessionIds = ref<string[]>([]);
const batchDeletingSessions = ref(false);
const sessionDeletionRange = ref<SessionDeletionRange>("all");
const snapshotProjects = ref<ProjectSummary[]>([]);
const snapshotSessions = ref<SessionSummary[]>([]);
const codexRuntimeSnapshot = ref<CodexRuntimeStatusSnapshot>({
  monitor: {
    installed: false,
    agent_hooks_supported: false,
  },
  sessions: [],
  projects: [],
  updated_at: 0,
});
const selectedProjectId = ref("");
const searchKeyword = ref("");
const projectSessionSearchLoading = ref(false);
const projectSessionSearchResults = ref<ProjectSessionSearchResult[]>([]);
const projectSessionSearchResolvedKey = ref("");
const projectSessionSearchErrorKey = ref("");
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
  targetType: "project" as
    | "project"
    | "session"
    | "session-batch"
    | "session-batch-prune",
  targetId: "",
  targetIds: [] as string[],
  targetName: "",
  sessionCount: 0,
  range: "all" as SessionDeletionRange,
});

const projectManagerOpenTimeoutMs = 5000;
const projectSessionSearchDelayMs = 300;
const openingSessionTimers = new Map<string, ReturnType<typeof setTimeout>>();
let projectSessionSearchTimer: ReturnType<typeof setTimeout> | null = null;
let projectSessionSearchRequestVersion = 0;
let stopCodexStatusEvents: (() => void) | null = null;

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

const normalizeCodexProjectPathKey = (path: string) => {
  let normalized = path.trim().replaceAll("/", "\\");
  while (normalized.length > 3 && normalized.endsWith("\\")) {
    normalized = normalized.slice(0, -1);
  }
  return normalized.toLowerCase();
};

const codexSessionStatusByID = computed(
  () =>
    new Map<string, CodexSessionRuntimeStatus>(
      codexRuntimeSnapshot.value.sessions.map((status) => [
        status.session_id,
        status,
      ]),
    ),
);

const codexProjectStatusByPath = computed(
  () =>
    new Map<string, CodexProjectRuntimeStatus>(
      codexRuntimeSnapshot.value.projects.map((status) => [
        normalizeCodexProjectPathKey(status.project_path),
        status,
      ]),
    ),
);

const resolveCodexSessionStatus = (sessionID: string) =>
  codexSessionStatusByID.value.get(sessionID);

const resolveCodexProjectStatus = (projectPath: string) =>
  codexProjectStatusByPath.value.get(
    normalizeCodexProjectPathKey(projectPath),
  );

const sessionSummaryByID = computed(
  () => new Map(snapshotSessions.value.map((session) => [session.id, session])),
);

const createRuntimeSessionSummary = (
  project: ProjectSummary,
  status: CodexSessionRuntimeStatus,
): SessionSummary => ({
  id: status.session_id,
  project_id: project.id,
  project_path: project.path,
  project_name: project.display_name,
  source_name: status.session_id,
  display_name: status.session_id,
  summary: "",
  latest_user_message: "",
  updated_at: status.updated_at,
  window_id: "",
  cwd: status.project_path || project.path,
  last_capture_path: "",
  project_source_hint: "runtime",
});

const loadedProjectSessionsByProjectID = computed(() => {
  const projectsByPath = new Map(
    snapshotProjects.value.map((project) => [
      normalizeCodexProjectPathKey(project.path),
      project,
    ]),
  );
  const sessionsByProjectID = new Map<string, SessionSummary[]>();
  codexRuntimeSnapshot.value.sessions.forEach((status) => {
    if (status.state === "not_loaded") {
      return;
    }
    const project = projectsByPath.get(
      normalizeCodexProjectPathKey(status.project_path),
    );
    if (!project) {
      return;
    }
    // Hook 状态先于项目扫描缓存到达时，也必须立即展示可恢复的会话标签。
    const session =
      sessionSummaryByID.value.get(status.session_id) ??
      createRuntimeSessionSummary(project, status);
    const sessions = sessionsByProjectID.get(project.id) ?? [];
    sessions.push(session);
    sessionsByProjectID.set(project.id, sessions);
  });
  return sessionsByProjectID;
});

const resolveProjectLoadedSessions = (project: ProjectSummary) => {
  // 运行态是会话是否仍加载的唯一事实来源；not_loaded 会在 CLI 或终端退出后被监控器写入。
  return loadedProjectSessionsByProjectID.value.get(project.id) ?? [];
};

type CodexCardStatusGroup = "running" | "idle" | "not_loaded";

const resolveCodexCardStatusGroup = (
  state: CodexRuntimeState | undefined,
): CodexCardStatusGroup => {
  if (
    state === "active" ||
    state === "waiting_approval" ||
    state === "waiting_user_input"
  ) {
    return "running";
  }
  if (state === "idle" || state === "system_error") {
    return "idle";
  }
  return "not_loaded";
};

const groupCardsByCodexStatus = <T>(
  cards: T[],
  resolveState: (card: T) => CodexRuntimeState | undefined,
) => {
  const cardsByGroup: Record<CodexCardStatusGroup, T[]> = {
    running: [],
    idle: [],
    not_loaded: [],
  };

  cards.forEach((card) => {
    cardsByGroup[resolveCodexCardStatusGroup(resolveState(card))].push(card);
  });

  // 桶分组仅决定三类卡片的前后关系，组内不按细分状态或更新时间重排，避免来回跳动。
  return [
    ...cardsByGroup.running,
    ...cardsByGroup.idle,
    ...cardsByGroup.not_loaded,
  ];
};

const selectedProject = computed(
  () =>
    snapshotProjects.value.find(
      (project) => project.id === selectedProjectId.value,
    ) ?? null,
);

const isProjectSessionSearchScope = computed(
  () => activeMode.value === "project" && !!selectedProjectId.value,
);

const projectSessionSearchKey = computed(() => {
  if (!isProjectSessionSearchScope.value || !normalizedKeyword.value) {
    return "";
  }
  return `${selectedProjectId.value}\n${normalizedKeyword.value}`;
});

const projectSessionSearchResultByID = computed(
  () =>
    new Map(
      projectSessionSearchResults.value.map((result) => [
        result.session_id,
        result,
      ]),
    ),
);

const projectSessionSearchResolved = computed(
  () =>
    !!projectSessionSearchKey.value &&
    projectSessionSearchResolvedKey.value === projectSessionSearchKey.value,
);

const projectSessionSearchFailed = computed(
  () =>
    !!projectSessionSearchKey.value &&
    projectSessionSearchErrorKey.value === projectSessionSearchKey.value,
);

const matchesKeyword = (fields: Array<string | undefined>, keyword: string) => {
  if (!keyword) {
    return true;
  }
  return fields.some((field) => (field ?? "").toLowerCase().includes(keyword));
};

const projectCards = computed(() => {
  const keyword = normalizedKeyword.value;
  const filteredProjects = snapshotProjects.value.filter((project) =>
    matchesKeyword(
      [project.display_name, project.source_name, project.path],
      keyword,
    ),
  );

  return groupCardsByCodexStatus(
    filteredProjects,
    (project) => resolveCodexProjectStatus(project.path)?.state,
  );
});

const currentProjectSessions = computed(() => {
  const projectId = selectedProjectId.value;
  const keyword = normalizedKeyword.value;
  const projectSessions = snapshotSessions.value.filter(
    (session) => !projectId || session.project_id === projectId,
  );
  const matchedSessions = projectSessionSearchResultByID.value;
  const filteredSessions =
    !keyword || !projectSessionSearchResolved.value
      ? projectSessions
      : projectSessions.filter((session) => matchedSessions.has(session.id));

  return groupCardsByCodexStatus(
    filteredSessions,
    (session) => resolveCodexSessionStatus(session.id)?.state,
  );
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
        session.latest_user_message,
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

const canSelectProjectSessions = computed(
  () => activeMode.value === "project" && !!selectedProjectId.value,
);

const selectedVisibleSessionIds = computed(() => {
  const selectedIDs = new Set(selectedSessionIds.value);
  return visibleSessions.value
    .filter((session) => selectedIDs.has(session.id))
    .map((session) => session.id);
});

const selectedSessionCount = computed(
  () => selectedVisibleSessionIds.value.length,
);

const isSessionSelected = (sessionID: string) =>
  selectedSessionIds.value.includes(sessionID);
const isSessionDeleting = (sessionID: string) =>
  deletingSessionIds.value.includes(sessionID);

const allVisibleSessionsSelected = computed(
  () =>
    visibleSessions.value.length > 0 &&
    selectedSessionCount.value === visibleSessions.value.length,
);

const sessionSelectionDockVisible = computed(
  () => sessionSelectionMode.value && canSelectProjectSessions.value,
);

const sessionDeletionRangeLabel = computed(() => {
  switch (sessionDeletionRange.value) {
    case "one_week":
      return t("components.projectManager.selection.rangeOneWeek");
    case "three_weeks":
      return t("components.projectManager.selection.rangeThreeWeeks");
    case "one_month":
      return t("components.projectManager.selection.rangeOneMonth");
    default:
      return t("components.projectManager.selection.rangeAll");
  }
});

const emptyStateMessage = computed(() => {
  if (loading.value) {
    return "";
  }
  if (showProjectGrid.value && projectCards.value.length === 0) {
    return t("components.projectManager.states.emptyProjects");
  }
  if (projectSessionSearchFailed.value) {
    return t("components.projectManager.states.sessionSearchFailed");
  }
  if (!showProjectGrid.value && visibleSessions.value.length === 0) {
    if (
      isProjectSessionSearchScope.value &&
      normalizedKeyword.value &&
      projectSessionSearchResolved.value
    ) {
      return t("components.projectManager.states.noSessionSearchResults");
    }
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

const resetSessionSelection = () => {
  sessionSelectionMode.value = false;
  selectedSessionIds.value = [];
  sessionDeletionRange.value = "all";
};

const reconcileSessionSelection = () => {
  if (!canSelectProjectSessions.value) {
    resetSessionSelection();
    return;
  }

  const visibleIDs = new Set(visibleSessions.value.map((session) => session.id));
  const nextSelectedIDs = selectedSessionIds.value.filter((id) =>
    visibleIDs.has(id),
  );
  if (nextSelectedIDs.length !== selectedSessionIds.value.length) {
    // 搜索结果和刷新快照会改变可见集合，选中状态不能继续指向用户已经看不见的会话。
    selectedSessionIds.value = nextSelectedIDs;
  }
};

watch(
  [activeMode, selectedProjectId],
  ([mode, projectID], previous) => {
    const previousProjectID = previous?.[1];
    if (mode !== "project" || !projectID || projectID !== previousProjectID) {
      resetSessionSelection();
      return;
    }
    reconcileSessionSelection();
  },
);
watch(visibleSessions, reconcileSessionSelection);

const toggleSessionSelectionMode = () => {
  if (!canSelectProjectSessions.value || batchDeletingSessions.value) {
    return;
  }
  if (sessionSelectionMode.value) {
    resetSessionSelection();
    return;
  }
  sessionSelectionMode.value = true;
  reconcileSessionSelection();
};

const exitSessionSelectionMode = () => {
  if (batchDeletingSessions.value) {
    return;
  }
  resetSessionSelection();
};

const toggleSessionSelection = (sessionID: string) => {
  if (
    !sessionSelectionMode.value ||
    batchDeletingSessions.value ||
    isSessionDeleting(sessionID)
  ) {
    return;
  }

  if (selectedSessionIds.value.includes(sessionID)) {
    selectedSessionIds.value = selectedSessionIds.value.filter(
      (id) => id !== sessionID,
    );
    return;
  }
  selectedSessionIds.value = [...selectedSessionIds.value, sessionID];
};

const selectAllVisibleSessions = () => {
  if (!sessionSelectionMode.value || batchDeletingSessions.value) {
    return;
  }
  selectedSessionIds.value = visibleSessions.value.map((session) => session.id);
};

const invertVisibleSessionSelection = () => {
  if (!sessionSelectionMode.value || batchDeletingSessions.value) {
    return;
  }
  const selectedIDs = new Set(selectedSessionIds.value);
  selectedSessionIds.value = visibleSessions.value
    .filter((session) => !selectedIDs.has(session.id))
    .map((session) => session.id);
};

const clearProjectSessionSearchTimer = () => {
  if (!projectSessionSearchTimer) {
    return;
  }
  clearTimeout(projectSessionSearchTimer);
  projectSessionSearchTimer = null;
};

const executeProjectSessionSearch = async (
  projectPath: string,
  query: string,
  searchKey: string,
  requestVersion: number,
) => {
  try {
    const results = await searchProjectSessionConversations(projectPath, query);
    if (requestVersion !== projectSessionSearchRequestVersion) {
      return;
    }
    projectSessionSearchResults.value = results;
    projectSessionSearchResolvedKey.value = searchKey;
    projectSessionSearchErrorKey.value = "";
  } catch (error) {
    if (requestVersion !== projectSessionSearchRequestVersion) {
      return;
    }
    console.error("failed to search project session conversations", error);
    projectSessionSearchResults.value = [];
    projectSessionSearchResolvedKey.value = searchKey;
    projectSessionSearchErrorKey.value = searchKey;
    showToast(extractErrorMessage(error), "error");
  } finally {
    if (requestVersion === projectSessionSearchRequestVersion) {
      projectSessionSearchLoading.value = false;
    }
  }
};

const scheduleProjectSessionSearch = () => {
  clearProjectSessionSearchTimer();
  const requestVersion = ++projectSessionSearchRequestVersion;
  const searchKey = projectSessionSearchKey.value;
  projectSessionSearchErrorKey.value = "";

  if (!searchKey) {
    projectSessionSearchLoading.value = false;
    projectSessionSearchResults.value = [];
    projectSessionSearchResolvedKey.value = "";
    return;
  }

  const projectPath = selectedProjectId.value;
  const query = searchKeyword.value.trim();
  projectSessionSearchLoading.value = true;
  projectSessionSearchTimer = setTimeout(() => {
    projectSessionSearchTimer = null;
    void executeProjectSessionSearch(
      projectPath,
      query,
      searchKey,
      requestVersion,
    );
  }, projectSessionSearchDelayMs);
};

watch(projectSessionSearchKey, scheduleProjectSessionSearch);

const loadSnapshot = async (isRefresh = false) => {
  const hasSnapshot =
    snapshotProjects.value.length > 0 || snapshotSessions.value.length > 0;
  if (isRefresh) {
    refreshing.value = true;
  }
  // 首次强制刷新没有可展示的旧数据，必须保留 loading 状态，
  // 否则空数组会先被误判成“没有项目”，造成首屏状态闪烁。
  if (!isRefresh || !hasSnapshot) {
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
  resetSessionSelection();
  selectedProjectId.value = project.id;
  activeMode.value = "project";
};

const handleBackToProjects = () => {
  if (batchDeletingSessions.value) {
    return;
  }
  resetSessionSelection();
  selectedProjectId.value = "";
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
  deleteState.targetIds = type === "session" ? [payload.id] : [];
  deleteState.targetName = payload.display_name;
  deleteState.sessionCount =
    type === "project"
      ? snapshotSessions.value.filter(
          (session) => session.project_id === payload.id,
        ).length
      : 0;
  deleteState.range = "all";
  deleteState.open = true;
};

const openBatchDeleteModal = () => {
  if (
    !sessionSelectionMode.value ||
    batchDeletingSessions.value ||
    selectedSessionCount.value === 0
  ) {
    return;
  }

  const range = sessionDeletionRange.value;
  deleteState.targetType = range === "all" ? "session-batch" : "session-batch-prune";
  deleteState.targetId = "";
  deleteState.targetIds = [...selectedVisibleSessionIds.value];
  deleteState.targetName = "";
  deleteState.sessionCount = deleteState.targetIds.length;
  deleteState.range = range;
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

  // 当前项目刷新后源文件和会话集合都可能变化；有效查询必须重跑，
  // 否则卡片会继续消费刷新前的会话 ID 和正文片段。
  if (projectSessionSearchKey.value) {
    scheduleProjectSessionSearch();
  }
};

const applyCodexRuntimeSnapshot = (snapshot: CodexRuntimeStatusSnapshot) => {
  // 状态灯只是增强层；即使旧后端或损坏事件返回了缺字段数据，
  // 也必须退回灰灯，不能让整个项目管理页面因为一次监控异常白屏。
  const updatedAt = snapshot?.updated_at ?? Date.now();
  if (updatedAt < codexRuntimeSnapshot.value.updated_at) {
    return;
  }
  codexRuntimeSnapshot.value = {
    monitor: snapshot?.monitor ?? {
      installed: false,
      agent_hooks_supported: false,
    },
    sessions: Array.isArray(snapshot?.sessions) ? snapshot.sessions : [],
    projects: Array.isArray(snapshot?.projects) ? snapshot.projects : [],
    updated_at: updatedAt,
  };
};

const resolveCodexRuntimeStatusEvent = (
  data: unknown,
): CodexRuntimeStatusSnapshot | null => {
  // Wails v3 的 Event.Emit(name, data...) 使用可变参数，单个快照也会以
  // event.data = [snapshot] 的形式到达。若直接把数组当快照，会把已加载的
  // 项目状态覆盖为空数组，所有活动灯随即退成灰色。
  if (!Array.isArray(data) || data.length !== 1) {
    return null;
  }

  const [snapshot] = data;
  if (
    !snapshot ||
    typeof snapshot !== "object" ||
    Array.isArray(snapshot) ||
    !Array.isArray((snapshot as Partial<CodexRuntimeStatusSnapshot>).sessions) ||
    !Array.isArray((snapshot as Partial<CodexRuntimeStatusSnapshot>).projects)
  ) {
    return null;
  }

  return snapshot as CodexRuntimeStatusSnapshot;
};

const loadCodexRuntimeSnapshot = async () => {
  try {
    applyCodexRuntimeSnapshot(await fetchCodexRuntimeStatusSnapshot());
  } catch (error) {
    // 兼容尚未提供状态接口的旧后端；项目与会话浏览功能不应被状态灯连坐。
    console.warn("failed to load Codex runtime status snapshot", error);
  }
};

const removeSessionFromSnapshot = (session: SessionSummary) => {
  snapshotSessions.value = snapshotSessions.value.filter(
    (item) => item.id !== session.id,
  );
  snapshotProjects.value = snapshotProjects.value.map((project) => {
    if (project.id !== session.project_id) {
      return project;
    }
    return {
      ...project,
      session_count: Math.max(0, project.session_count - 1),
    };
  });
  selectedSessionIds.value = selectedSessionIds.value.filter(
    (id) => id !== session.id,
  );
};

const confirmBatchSessionDelete = async () => {
  const targetIDs = [...deleteState.targetIds];
  const targetsByID = new Map(
    snapshotSessions.value.map((session) => [session.id, session]),
  );
  deleteState.open = false;

  if (targetIDs.length === 0) {
    return;
  }

  batchDeletingSessions.value = true;
  deletingSessionIds.value = Array.from(
    new Set([...deletingSessionIds.value, ...targetIDs]),
  );
  let deletedCount = 0;
  const failures: string[] = [];

  try {
    // Codex 索引和本地隐藏记录都属于共享文件状态，批量操作必须串行，避免并发写入互相覆盖。
    for (const sessionID of targetIDs) {
      const session = targetsByID.get(sessionID);
      if (!session) {
        failures.push(
          `${sessionID}: ${t("components.projectManager.errors.sessionNotFound")}`,
        );
        continue;
      }

      try {
        await deleteSession(session.id);
        removeSessionFromSnapshot(session);
        deletedCount += 1;
      } catch (error) {
        console.error("failed to delete session in batch", session.id, error);
        failures.push(
          `${session.display_name || session.id}: ${extractErrorMessage(error)}`,
        );
      }
    }

    if (failures.length === 0) {
      showToast(
        t("components.projectManager.delete.batchSessionDeleted", {
          count: deletedCount,
        }),
        "success",
      );
    } else {
      const summary = t(
        deletedCount > 0
          ? "components.projectManager.delete.batchSessionDeletePartial"
          : "components.projectManager.delete.batchSessionDeleteFailed",
        {
          deleted: deletedCount,
          failed: failures.length,
          count: targetIDs.length,
        },
      );
      const details = t(
        "components.projectManager.delete.batchSessionDeleteFailureDetails",
        { items: failures.join("; ") },
      );
      showToast(`${summary} ${details}`, deletedCount > 0 ? "warning" : "error");
    }
  } finally {
    deletingSessionIds.value = deletingSessionIds.value.filter(
      (id) => !targetIDs.includes(id),
    );
    batchDeletingSessions.value = false;
    deleteState.targetIds = [];
    reconcileSessionSelection();
  }
};

const confirmBatchSessionConversationPrune = async () => {
  const targetIDs = [...deleteState.targetIds];
  const range = deleteState.range;
  const targetsByID = new Map(
    snapshotSessions.value.map((session) => [session.id, session]),
  );
  deleteState.open = false;

  if (targetIDs.length === 0 || range === "all") {
    return;
  }

  batchDeletingSessions.value = true;
  deletingSessionIds.value = Array.from(
    new Set([...deletingSessionIds.value, ...targetIDs]),
  );

  try {
    const result = await pruneSessionConversationsByRange(targetIDs, range);
    const failures = result.results.filter((item) => !!item.error);
    const noMatchCount = result.results.filter(
      (item) => !item.error && item.deleted_items === 0,
    ).length;
    const failureDetails = failures.map((item) => {
      const session = targetsByID.get(item.session_id);
      return `${session?.display_name || item.session_id}: ${item.error}`;
    });

    if (result.total_deleted_items > 0) {
      // 详情剪枝不会移除会话卡片，但可能改变概要和更新时间，完成后重新读取快照保持列表一致。
      await loadSnapshot(true);
    }

    if (failures.length === 0) {
      if (result.total_deleted_items === 0) {
        showToast(
          t("components.projectManager.delete.batchSessionPruneNoMatch", {
            count: noMatchCount,
            range: sessionDeletionRangeLabel.value,
          }),
          "warning",
        );
      } else {
        showToast(
          t("components.projectManager.delete.batchSessionPruned", {
            turns: result.total_deleted_turns,
            items: result.total_deleted_items,
            range: sessionDeletionRangeLabel.value,
            empty: noMatchCount,
          }),
          "success",
        );
      }
    } else {
      const summary = t(
        result.total_deleted_items > 0
          ? "components.projectManager.delete.batchSessionPrunePartial"
          : "components.projectManager.delete.batchSessionPruneFailed",
        {
          turns: result.total_deleted_turns,
          items: result.total_deleted_items,
          failed: failures.length,
          count: targetIDs.length,
          range: sessionDeletionRangeLabel.value,
        },
      );
      const details = t(
        "components.projectManager.delete.batchSessionPruneFailureDetails",
        { items: failureDetails.join("; ") },
      );
      showToast(`${summary} ${details}`, result.total_deleted_items > 0 ? "warning" : "error");
    }
  } catch (error) {
    console.error("failed to prune session conversations in batch", error);
    showToast(extractErrorMessage(error), "error");
  } finally {
    deletingSessionIds.value = deletingSessionIds.value.filter(
      (id) => !targetIDs.includes(id),
    );
    batchDeletingSessions.value = false;
    deleteState.targetIds = [];
    reconcileSessionSelection();
  }
};

const confirmDelete = async () => {
  const targetType = deleteState.targetType;
  if (targetType === "session-batch") {
    await confirmBatchSessionDelete();
    return;
  }
  if (targetType === "session-batch-prune") {
    await confirmBatchSessionConversationPrune();
    return;
  }

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
      removeSessionFromSnapshot(sessionTarget);
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

const resolveSessionSummary = (session: SessionSummary) => {
  if (projectSessionSearchResolved.value) {
    const matchedContent = projectSessionSearchResultByID.value
      .get(session.id)
      ?.matched_content.trim();
    if (matchedContent) {
      return matchedContent;
    }
  }
  return (
    session.latest_user_message ||
    session.summary ||
    t("components.projectManager.common.emptySummary")
  );
};

onMounted(() => {
  // 先订阅增量事件再拉初始快照，避免 Codex 状态恰好在页面挂载窗口内更新却无人接收。
  stopCodexStatusEvents = Events.On(
    "project-manager:codex-status",
    (event) => {
      const snapshot = resolveCodexRuntimeStatusEvent(event.data);
      if (!snapshot) {
        console.warn("received malformed Codex runtime status event", event.data);
        return;
      }
      applyCodexRuntimeSnapshot(snapshot);
    },
  );
  void loadCodexRuntimeSnapshot();
  // 页面打开时直接走增量刷新，避免先展示旧快照后再悄悄补会话，导致用户误以为历史不全。
  void loadSnapshot(true);
});

onBeforeUnmount(() => {
  stopCodexStatusEvents?.();
  stopCodexStatusEvents = null;
  clearProjectSessionSearchTimer();
  projectSessionSearchRequestVersion += 1;
  openingSessionTimers.forEach((timeoutId) => {
    clearTimeout(timeoutId);
  });
  openingSessionTimers.clear();
});
</script>

<template>
  <div class="project-manager-page">
    <ProjectManagerHeroPanel
      v-model="searchKeyword"
      :active-mode="activeMode"
      :refreshing="refreshing"
      :searching="projectSessionSearchLoading"
      :conversation-search="isProjectSessionSearchScope"
      :can-select-sessions="canSelectProjectSessions"
      :selection-mode="sessionSelectionMode"
      :selection-busy="batchDeletingSessions"
      @change-mode="activeMode = $event"
      @clear="searchKeyword = ''"
      @refresh="loadSnapshot(true)"
      @toggle-selection="toggleSessionSelectionMode"
    />

    <ProjectManagerBreadcrumb
      v-if="selectedProject && activeMode === 'project'"
      :project="selectedProject"
      :opening-terminal="openingProjectTerminalId === selectedProject.id"
      :committing="committingProjectId === selectedProject.id"
      @back="handleBackToProjects"
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
      :search-keyword="searchKeyword"
      :format-updated-at="formatUpdatedAt"
      :is-project-deleting="isProjectDeleting"
      :is-project-committing="isProjectCommitting"
      :is-project-running="isProjectRunning"
      :codex-monitor="codexRuntimeSnapshot.monitor"
      :resolve-codex-project-status="resolveCodexProjectStatus"
      :resolve-codex-session-status="resolveCodexSessionStatus"
      :resolve-project-loaded-sessions="resolveProjectLoadedSessions"
      :is-session-opening="isSessionOpening"
      :is-session-deleting="isSessionDeleting"
      @enter="enterProject"
      @delete="openDeleteModal('project', $event)"
      @open-folder="handleOpenProjectFolder"
      @set-codex-provider="openProviderModal"
      @run-command="handleRunProjectCommand"
      @edit-run-command="openRunCommandModal"
      @commit="handleRunProjectAICommit"
      @open-session="handleOpenSession"
    />

    <ProjectManagerSessionGrid
      v-else
      :sessions="visibleSessions"
      :search-keyword="searchKeyword"
      :format-updated-at="formatUpdatedAt"
      :resolve-summary="resolveSessionSummary"
      :show-project-name-tag="activeMode === 'session'"
      :is-session-opening="isSessionOpening"
      :is-session-deleting="isSessionDeleting"
      :selection-mode="sessionSelectionMode"
      :is-session-selected="isSessionSelected"
      :codex-monitor="codexRuntimeSnapshot.monitor"
      :resolve-codex-session-status="resolveCodexSessionStatus"
      @delete="openDeleteModal('session', $event)"
      @rename="openRenameModal('session', $event)"
      @open-session="handleOpenSession"
      @open-detail="openSessionDetail"
      @toggle-selection="toggleSessionSelection($event.id)"
    />

    <ProjectManagerSessionSelectionDock
      v-if="sessionSelectionDockVisible"
      :selected-count="selectedSessionCount"
      :total-count="visibleSessions.length"
      :all-selected="allVisibleSessionsSelected"
      :busy="batchDeletingSessions"
      :range="sessionDeletionRange"
      @select-all="selectAllVisibleSessions"
      @invert-selection="invertVisibleSessionSelection"
      @update:range="sessionDeletionRange = $event"
      @delete="openBatchDeleteModal"
      @exit="exitSessionSelectionMode"
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
          : deleteState.targetType === 'session-batch'
            ? t('components.projectManager.delete.batchSessionTitle')
            : deleteState.targetType === 'session-batch-prune'
              ? t('components.projectManager.delete.batchSessionPruneTitle')
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
          <p v-else-if="deleteState.targetType === 'session-batch'">
            {{
              t("components.projectManager.delete.batchSessionConfirm", {
                count: deleteState.sessionCount,
              })
            }}
          </p>
          <p v-else-if="deleteState.targetType === 'session-batch-prune'">
            {{
              t("components.projectManager.delete.batchSessionPruneConfirm", {
                count: deleteState.sessionCount,
                range: sessionDeletionRangeLabel,
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
            {{
              deleteState.targetType === 'session-batch-prune'
                ? t("components.projectManager.delete.batchSessionPruneHint")
                : t("components.projectManager.delete.hint")
            }}
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
