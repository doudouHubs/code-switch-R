import { Call } from '@wailsio/runtime'

const PROJECT_MANAGER_SERVICE = 'codeswitch/services.ProjectManagerService'

export interface ProjectSummary {
  id: string
  path: string
  source_name: string
  display_name: string
  updated_at: number
  session_count: number
}

export interface SessionSummary {
  id: string
  project_id: string
  project_path: string
  project_name: string
  source_name: string
  display_name: string
  summary: string
  updated_at: number
  window_id: string
  cwd: string
  last_capture_path: string
  project_source_hint: string
}

export interface ProjectManagerSnapshot {
  projects: ProjectSummary[]
  sessions: SessionSummary[]
}

export const fetchProjectManagerSnapshot = async (): Promise<ProjectManagerSnapshot> => {
  const result = await Call.ByName(`${PROJECT_MANAGER_SERVICE}.GetSnapshot`)
  return result as ProjectManagerSnapshot
}

export const refreshProjectManagerSnapshot = async (): Promise<ProjectManagerSnapshot> => {
  const result = await Call.ByName(`${PROJECT_MANAGER_SERVICE}.RefreshProjectIndex`)
  return result as ProjectManagerSnapshot
}

export const renameProject = async (projectPath: string, displayName: string): Promise<void> => {
  await Call.ByName(`${PROJECT_MANAGER_SERVICE}.RenameProject`, projectPath, displayName)
}

export const renameSession = async (sessionID: string, displayName: string): Promise<void> => {
  await Call.ByName(`${PROJECT_MANAGER_SERVICE}.RenameSession`, sessionID, displayName)
}

export const openSessionTerminal = async (sessionID: string): Promise<void> => {
  await Call.ByName(`${PROJECT_MANAGER_SERVICE}.OpenSessionTerminal`, sessionID)
}

export const openProjectFolder = async (projectPath: string): Promise<void> => {
  await Call.ByName(`${PROJECT_MANAGER_SERVICE}.OpenProjectFolder`, projectPath)
}
