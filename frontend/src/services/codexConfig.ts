import { Call } from '@wailsio/runtime'

const CODEX_SETTINGS_SERVICE = 'codeswitch/services.CodexSettingsService'

export const fetchModelInstructionsFile = async (): Promise<string> => {
  const raw = await Call.ByName(`${CODEX_SETTINGS_SERVICE}.GetModelInstructionsFile`)
  return typeof raw === 'string' ? raw : ''
}

export const saveModelInstructionsFile = async (path: string): Promise<void> => {
  await Call.ByName(`${CODEX_SETTINGS_SERVICE}.SetModelInstructionsFile`, path)
}
