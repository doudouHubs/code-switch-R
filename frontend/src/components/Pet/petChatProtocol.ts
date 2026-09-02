import { buildPetPlanInstructions } from './petPlan'
import type { AgentInteraction } from '../Agent/agentTypes'

export type PetChatLifecycleType = 'queued' | 'started' | 'progress' | 'delta' | 'interaction' | 'completed' | 'failed' | 'cancelled'

export interface PetChatOutgoingImage {
  id: string
  data: string
  mediaType: string
  previewUrl: string
}

export interface PetChatOutgoingMessage {
  requestId: string
  text: string
  images: PetChatOutgoingImage[]
  createdAt: number
}

export interface PetChatLifecycleEvent {
  requestId: string
  type: PetChatLifecycleType
  // text 始终是已经清洗过的可见文本；历史页不需要重复解析 Codex 内部协议。
  text: string
  errorCode: string
  interaction?: AgentInteraction
}

export function buildPetChatPersona(systemPrompt: string | null | undefined, petName: string | null | undefined): string {
  const configured = systemPrompt?.trim() ?? ''
  const normalizedName = petName?.trim() || 'Kapi'
  const base = configured || '你是' + normalizedName + '，一个简短、友善、会记得当前对话的桌面宠物。'

  // 计划协议必须和聊天请求共用同一个 persona；否则设置页读取历史时可能命中
  // 不同的 persona fingerprint，导致 Codex thread 被错误地视为另一条会话。
  return `${base}\n\n${buildPetPlanInstructions()}`
}

export function cleanPetChatHistoryText(value: string): string {
  return value
    // runtime context 是宿主注入的环境信息，不属于用户可见的聊天正文。
    .replace(/<pet-runtime-context\b[^>]*>[\s\S]*?<\/pet-runtime-context\s*>/gi, '')
    // 用户消息 envelope 只用于区分协议边界，历史展示保留其中的实际内容。
    .replace(/<\/?pet-user-message\b[^>]*>/gi, '')
    .trim()
}

export function normalizePetChatTimestamp(value: unknown): number {
  let timestamp = 0
  if (typeof value === 'number' && Number.isFinite(value)) {
    timestamp = value
  } else if (typeof value === 'string') {
    const normalized = value.trim()
    if (!normalized) return 0
    const numeric = Number(normalized)
    timestamp = Number.isFinite(numeric) ? numeric : Date.parse(normalized)
  }

  if (!Number.isFinite(timestamp) || timestamp <= 0) return 0

  // Codex/旧 Wails 数据可能混用秒、毫秒和 ISO 字符串；统一成 Date 可用的毫秒，
  // 否则秒级时间会被当成 1970 年的日期，字符串时间又会直接被前端判为缺失。
  if (timestamp < 100_000_000_000) return Math.round(timestamp * 1000)
  if (timestamp > 100_000_000_000_000) return Math.round(timestamp / 1000)
  return Math.round(timestamp)
}
