// 本地 Gemini 类型定义，避免 CI 生成绑定缺失类型导致编译失败

export type GeminiAuthType = 'oauth-personal' | 'gemini-api-key' | 'packycode' | 'generic'

export interface GeminiProvider {
  id: string
  name: string
  websiteUrl?: string
  apiKeyUrl?: string
  baseUrl?: string
  apiKey?: string
  model?: string
  modelCategory?: string
  description?: string
  category?: string // official, third_party, custom
  partnerPromotionKey?: string
  enabled: boolean
  // Go map[string]string 的值始终是 string，需与 Wails 生成的 binding 保持一致。
  envConfig?: Record<string, string | undefined>
  settingsConfig?: Record<string, any>
}

export interface GeminiPreset {
  name: string
  websiteUrl: string
  apiKeyUrl?: string
  baseUrl?: string
  model?: string
  modelCategory?: string
  description?: string
  category: string
  partnerPromotionKey?: string
  envConfig?: Record<string, string | undefined>
}

export interface GeminiStatus {
  enabled: boolean
  currentProvider?: string
  authType: GeminiAuthType
  hasApiKey: boolean
  hasBaseUrl: boolean
  model?: string
}
