import { Call } from '../wails-runtime-compat'

export type AppSettings = {
  show_heatmap: boolean
  show_home_title: boolean
  budget_total: number
  budget_used_adjustment: number
  budget_cycle_enabled: boolean
  budget_cycle_mode: string
  budget_refresh_time: string
  budget_refresh_day: number
  budget_show_countdown: boolean
  budget_show_forecast: boolean
  budget_forecast_method: string
  budget_total_codex: number
  budget_used_adjustment_codex: number
  budget_cycle_enabled_codex: boolean
  budget_cycle_mode_codex: string
  budget_refresh_time_codex: string
  budget_refresh_day_codex: number
  budget_show_countdown_codex: boolean
  budget_show_forecast_codex: boolean
  budget_forecast_method_codex: string
  auto_start: boolean
  auto_update: boolean
  enable_switch_notify: boolean // 供应商切换通知开关
  enable_round_robin: boolean   // 同 Level 轮询负载均衡开关
  enable_request_capture: boolean
  request_capture_dir: string
  speech_provider_platform: string | null
  speech_provider_id: string | null
  speech_model_id: string
}

/** 应用级语音识别选择；空值表示未配置，调用方不得回退到聊天或 TTS 配置。 */
export type SpeechProviderSelection = Pick<
  AppSettings,
  'speech_provider_platform' | 'speech_provider_id' | 'speech_model_id'
>

const DEFAULT_SETTINGS: AppSettings = {
  show_heatmap: true,
  show_home_title: true,
  budget_total: 0,
  budget_used_adjustment: 0,
  budget_cycle_enabled: false,
  budget_cycle_mode: 'daily',
  budget_refresh_time: '00:00',
  budget_refresh_day: 1,
  budget_show_countdown: false,
  budget_show_forecast: false,
  budget_forecast_method: 'cycle',
  budget_total_codex: 0,
  budget_used_adjustment_codex: 0,
  budget_cycle_enabled_codex: false,
  budget_cycle_mode_codex: 'daily',
  budget_refresh_time_codex: '00:00',
  budget_refresh_day_codex: 1,
  budget_show_countdown_codex: false,
  budget_show_forecast_codex: false,
  budget_forecast_method_codex: 'cycle',
  auto_start: false,
  auto_update: true,
  enable_switch_notify: true,  // 默认开启
  enable_round_robin: false,   // 默认关闭轮询
  enable_request_capture: true,
  request_capture_dir: '',
  speech_provider_platform: null,
  speech_provider_id: null,
  speech_model_id: '',
}

function normalizeOptionalReference(value: unknown): string | null {
  if (typeof value !== 'string') return null
  const normalized = value.trim()
  return normalized || null
}

function normalizeModelReference(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function normalizeAppSettings(value: unknown): AppSettings {
  // 后端配置文件可能来自旧版本，只返回已存在的字段；合并默认值可以保留旧配置兼容性，
  // 同时为新增的 speech_* 字段提供明确的“未配置”状态，而不是误用其他模型引用。
  const settings = value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Partial<AppSettings>)
    : {}
  return {
    ...DEFAULT_SETTINGS,
    ...settings,
    speech_provider_platform: normalizeOptionalReference(settings.speech_provider_platform),
    speech_provider_id: normalizeOptionalReference(settings.speech_provider_id),
    speech_model_id: normalizeModelReference(settings.speech_model_id)
  }
}

export function getSpeechProviderSelection(settings: AppSettings): SpeechProviderSelection {
  return {
    speech_provider_platform: normalizeOptionalReference(settings.speech_provider_platform),
    speech_provider_id: normalizeOptionalReference(settings.speech_provider_id),
    speech_model_id: normalizeModelReference(settings.speech_model_id)
  }
}

export function saveSpeechProviderSelection(
  settings: AppSettings,
  selection: SpeechProviderSelection
): AppSettings {
  return {
    ...settings,
    ...getSpeechProviderSelection({ ...settings, ...selection })
  }
}

export const fetchAppSettings = async (): Promise<AppSettings> => {
  const data = await Call.ByName('codeswitch/services.AppSettingsService.GetAppSettings')
  return normalizeAppSettings(data)
}

export const saveAppSettings = async (settings: AppSettings): Promise<AppSettings> => {
  const data = await Call.ByName('codeswitch/services.AppSettingsService.SaveAppSettings', settings)
  return normalizeAppSettings(data ?? settings)
}
