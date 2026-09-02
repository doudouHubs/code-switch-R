import { Call } from '../../wails-runtime-compat'
import {
  type AgentCommandRequest,
  type AgentCommandResult,
  type AgentInteraction,
  type AgentModel,
  type AgentModelListResult,
  type AgentSkill,
  type AgentSkillError,
  type AgentSkillListResult,
  type ResolveInteractionRequest
} from './agentTypes'

const PET_AI_SERVICE = 'codeswitch/services.PetAIAPIService'

export const AGENT_API_METHODS = {
  listSkills: PET_AI_SERVICE + '.ListSkills',
  listModels: PET_AI_SERVICE + '.ListModels',
  executeCommand: PET_AI_SERVICE + '.ExecuteCommand',
  resolveInteraction: PET_AI_SERVICE + '.ResolveInteraction'
} as const

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function arrayValue(value: unknown): unknown[] {
  return Array.isArray(value) ? value : []
}

function normalizeSkill(value: unknown): AgentSkill | null {
  if (!isRecord(value)) return null
  const name = stringValue(value.name).trim()
  const path = stringValue(value.path).trim()
  if (!name || !path) return null
  return {
    name,
    path,
    description: stringValue(value.description).trim(),
    shortDescription: stringValue(value.shortDescription).trim(),
    scope: stringValue(value.scope).trim(),
    enabled: value.enabled !== false
  }
}

function normalizeSkillError(value: unknown): AgentSkillError | null {
  if (!isRecord(value)) return null
  const message = stringValue(value.message).trim()
  const path = stringValue(value.path).trim()
  return message || path ? { message, path } : null
}

function normalizeModel(value: unknown): AgentModel | null {
  if (!isRecord(value)) return null
  const id = stringValue(value.id).trim()
  if (!id) return null
  return {
    id,
    model: stringValue(value.model).trim(),
    displayName: stringValue(value.displayName).trim() || stringValue(value.model).trim() || id,
    description: stringValue(value.description).trim(),
    hidden: value.hidden === true,
    isDefault: value.isDefault === true,
    inputModalities: arrayValue(value.inputModalities).flatMap((item) => {
      const modality = stringValue(item).trim()
      return modality ? [modality] : []
    }),
    defaultReasoningEffort: stringValue(value.defaultReasoningEffort).trim()
  }
}

function normalizeResult(value: unknown): Record<string, unknown> {
  if (isRecord(value)) return value
  return {}
}

function normalizeSkillsResult(value: unknown): AgentSkillListResult {
  const source = normalizeResult(value)
  return {
    projectId: stringValue(source.projectId).trim(),
    workspace: stringValue(source.workspace).trim(),
    skills: arrayValue(source.skills).flatMap((skill) => {
      const normalized = normalizeSkill(skill)
      return normalized ? [normalized] : []
    }),
    errors: arrayValue(source.errors).flatMap((error) => {
      const normalized = normalizeSkillError(error)
      return normalized ? [normalized] : []
    })
  }
}

function normalizeModelsResult(value: unknown): AgentModelListResult {
  const source = normalizeResult(value)
  return {
    projectId: stringValue(source.projectId).trim(),
    workspace: stringValue(source.workspace).trim(),
    models: arrayValue(source.models).flatMap((model) => {
      const normalized = normalizeModel(model)
      return normalized ? [normalized] : []
    }),
    nextCursor: stringValue(source.nextCursor).trim()
  }
}

function normalizeCommandResult(value: unknown): AgentCommandResult {
  const source = normalizeResult(value)
  return {
    command: stringValue(source.command).trim(),
    accepted: source.accepted === true,
    requestId: stringValue(source.requestId).trim(),
    text: stringValue(source.text),
    threadId: stringValue(source.threadId).trim(),
    turnId: stringValue(source.turnId).trim(),
    skills: arrayValue(source.skills).flatMap((skill) => {
      const normalized = normalizeSkill(skill)
      return normalized ? [normalized] : []
    }),
    models: arrayValue(source.models).flatMap((model) => {
      const normalized = normalizeModel(model)
      return normalized ? [normalized] : []
    }),
    raw: source.raw
  }
}

function call(method: string, request: unknown): Promise<unknown> {
  return Promise.resolve(Call.ByName(method, request))
}

export const agentApi = {
  listSkills(request: AgentCommandRequest): Promise<AgentSkillListResult> {
    return call(AGENT_API_METHODS.listSkills, request).then(normalizeSkillsResult)
  },

  listModels(request: AgentCommandRequest): Promise<AgentModelListResult> {
    return call(AGENT_API_METHODS.listModels, request).then(normalizeModelsResult)
  },

  executeCommand(request: AgentCommandRequest): Promise<AgentCommandResult> {
    return call(AGENT_API_METHODS.executeCommand, request).then(normalizeCommandResult)
  },

  resolveInteraction(request: ResolveInteractionRequest): Promise<void> {
    return call(AGENT_API_METHODS.resolveInteraction, request).then(() => undefined)
  }
}

export type { AgentInteraction }
