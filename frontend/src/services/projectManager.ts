import { Call } from "@wailsio/runtime";

const PROJECT_MANAGER_SERVICE = "codeswitch/services.ProjectManagerService";

export interface ProjectSummary {
  id: string;
  path: string;
  source_name: string;
  display_name: string;
  run_command?: string;
  updated_at: number;
  session_count: number;
  codex_provider_id?: number;
  codex_provider_name?: string;
  codex_provider_auto: boolean;
}

export interface SessionSummary {
  id: string;
  project_id: string;
  project_path: string;
  project_name: string;
  source_name: string;
  display_name: string;
  summary: string;
  latest_user_message: string;
  updated_at: number;
  window_id: string;
  cwd: string;
  last_capture_path: string;
  project_source_hint: string;
}

export interface SessionConversationItem {
  id: string;
  session_id: string;
  role: "user" | "agent";
  content: string;
  timestamp: number;
  reply_for: string;
  turn_id: string;
  turn_usage?: SessionConversationTurnUsage;
  source_file: string;
  source_line: number;
}

export interface SessionConversationTurnUsage {
  input_tokens: number;
  cached_input_tokens: number;
  output_tokens: number;
  reasoning_output_tokens: number;
  total_tokens: number;
  model_calls: number;
  duration_ms: number;
  complete: boolean;
}

export interface SessionConversationDetail {
  session: SessionSummary;
  items: SessionConversationItem[];
}

export interface ProjectSessionSearchResult {
  session_id: string;
  matched_content: string;
}

export interface ProjectManagerSnapshot {
  projects: ProjectSummary[];
  sessions: SessionSummary[];
  snapshot_updated_at?: number;
}

export type CodexRuntimeState =
  | "not_loaded"
  | "idle"
  | "active"
  | "waiting_approval"
  | "waiting_user_input"
  | "system_error";

export interface CodexStatusMonitorInfo {
  installed: boolean;
  codex_version?: string;
  agent_hooks_supported: boolean;
  error?: string;
}

export interface CodexSessionRuntimeStatus {
  session_id: string;
  turn_id?: string;
  project_path: string;
  state: CodexRuntimeState;
  turn_status?: "completed" | "interrupted" | "failed" | "in_progress";
  active_agents: number;
  agent_supported: boolean;
  monitored: boolean;
  updated_at: number;
  last_event?: string;
  last_error?: string;
}

export interface CodexProjectRuntimeStatus {
  project_path: string;
  state: CodexRuntimeState;
  active_sessions: number;
  waiting_sessions: number;
  error_sessions: number;
  latest_session_id?: string;
  latest_session_state?: CodexRuntimeState;
  updated_at: number;
}

export interface CodexRuntimeStatusSnapshot {
  monitor: CodexStatusMonitorInfo;
  sessions: CodexSessionRuntimeStatus[];
  projects: CodexProjectRuntimeStatus[];
  updated_at: number;
}

export const fetchProjectManagerSnapshot =
  async (): Promise<ProjectManagerSnapshot> => {
    const result = await Call.ByName(`${PROJECT_MANAGER_SERVICE}.GetSnapshot`);
    return result as ProjectManagerSnapshot;
  };

export const refreshProjectManagerSnapshot =
  async (): Promise<ProjectManagerSnapshot> => {
    const result = await Call.ByName(
      `${PROJECT_MANAGER_SERVICE}.RefreshProjectIndex`,
    );
    return result as ProjectManagerSnapshot;
  };

export const fetchCodexRuntimeStatusSnapshot =
  async (): Promise<CodexRuntimeStatusSnapshot> => {
    const result = await Call.ByName(
      `${PROJECT_MANAGER_SERVICE}.GetCodexRuntimeStatusSnapshot`,
    );
    return result as CodexRuntimeStatusSnapshot;
  };

export const searchProjectSessionConversations = async (
  projectPath: string,
  query: string,
): Promise<ProjectSessionSearchResult[]> => {
  const result = await Call.ByName(
    `${PROJECT_MANAGER_SERVICE}.SearchProjectSessionConversations`,
    projectPath,
    query,
  );
  return result as ProjectSessionSearchResult[];
};

export const renameProject = async (
  projectPath: string,
  displayName: string,
): Promise<void> => {
  await Call.ByName(
    `${PROJECT_MANAGER_SERVICE}.RenameProject`,
    projectPath,
    displayName,
  );
};

export const renameSession = async (
  sessionID: string,
  displayName: string,
): Promise<void> => {
  await Call.ByName(
    `${PROJECT_MANAGER_SERVICE}.RenameSession`,
    sessionID,
    displayName,
  );
};

export const setProjectCodexProvider = async (
  projectPath: string,
  providerID: number,
  autoFallback = true,
): Promise<void> => {
  await Call.ByName(
    `${PROJECT_MANAGER_SERVICE}.SetProjectCodexProviderRouting`,
    projectPath,
    providerID,
    autoFallback,
  );
};

export const clearProjectCodexProvider = async (
  projectPath: string,
): Promise<void> => {
  await Call.ByName(
    `${PROJECT_MANAGER_SERVICE}.ClearProjectCodexProvider`,
    projectPath,
  );
};

export const deleteProject = async (projectPath: string): Promise<void> => {
  await Call.ByName(`${PROJECT_MANAGER_SERVICE}.DeleteProject`, projectPath);
};

export const deleteSession = async (sessionID: string): Promise<void> => {
  await Call.ByName(`${PROJECT_MANAGER_SERVICE}.DeleteSession`, sessionID);
};

export const openSessionTerminal = async (
  session: SessionSummary | string,
): Promise<void> => {
  if (typeof session === "string") {
    await Call.ByName(
      `${PROJECT_MANAGER_SERVICE}.OpenSessionTerminal`,
      session,
    );
    return;
  }
  await Call.ByName(
    `${PROJECT_MANAGER_SERVICE}.OpenSessionTerminalWithSession`,
    session,
  );
};

export const openProjectFolder = async (projectPath: string): Promise<void> => {
  await Call.ByName(
    `${PROJECT_MANAGER_SERVICE}.OpenProjectFolder`,
    projectPath,
  );
};

export const openProjectTerminal = async (
  projectPath: string,
): Promise<void> => {
  await Call.ByName(
    `${PROJECT_MANAGER_SERVICE}.OpenProjectTerminal`,
    projectPath,
  );
};

export const saveProjectRunCommand = async (
  projectPath: string,
  command: string,
): Promise<void> => {
  await Call.ByName(
    `${PROJECT_MANAGER_SERVICE}.SaveProjectRunCommand`,
    projectPath,
    command,
  );
};

export const runProjectCommand = async (
  projectPath: string,
): Promise<void> => {
  await Call.ByName(
    `${PROJECT_MANAGER_SERVICE}.RunProjectCommand`,
    projectPath,
  );
};

export const runProjectAICommit = async (
  projectPath: string,
): Promise<void> => {
  await Call.ByName(
    `${PROJECT_MANAGER_SERVICE}.RunProjectAICommit`,
    projectPath,
  );
};

export const fetchSessionConversationDetail = async (
  sessionID: string,
): Promise<SessionConversationDetail> => {
  const result = await Call.ByName(
    `${PROJECT_MANAGER_SERVICE}.GetSessionConversationDetail`,
    sessionID,
  );
  return result as SessionConversationDetail;
};

export const pruneSessionConversation = async (
  sessionID: string,
  messageIDs: string[],
): Promise<SessionConversationDetail> => {
  const result = await Call.ByName(
    `${PROJECT_MANAGER_SERVICE}.PruneSessionConversation`,
    sessionID,
    messageIDs,
  );
  return result as SessionConversationDetail;
};

export const forkSessionConversation = async (
  sessionID: string,
  messageIDs: string[],
): Promise<SessionSummary> => {
  const result = await Call.ByName(
    `${PROJECT_MANAGER_SERVICE}.ForkSessionConversation`,
    sessionID,
    messageIDs,
  );
  return result as SessionSummary;
};
