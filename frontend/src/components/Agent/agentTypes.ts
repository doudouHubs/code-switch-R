export type AgentCommandName = 'skills' | 'models' | 'review' | 'compact' | 'steer' | 'interrupt'

export type AgentEventType =
  | 'queued'
  | 'started'
  | 'progress'
  | 'delta'
  | 'interaction'
  | 'completed'
  | 'failed'
  | 'cancelled'

export interface AgentSkill {
  name: string
  description: string
  shortDescription: string
  path: string
  scope: string
  enabled: boolean
}

export interface AgentSkillReference {
  name: string
  path: string
}

export interface AgentSkillError {
  message: string
  path: string
}

export interface AgentSkillListResult {
  projectId: string
  workspace: string
  skills: AgentSkill[]
  errors: AgentSkillError[]
}

export interface AgentModel {
  id: string
  model: string
  displayName: string
  description: string
  hidden: boolean
  isDefault: boolean
  inputModalities: string[]
  defaultReasoningEffort: string
}

export interface AgentModelListResult {
  projectId: string
  workspace: string
  models: AgentModel[]
  nextCursor: string
}

export interface AgentCommandRequest {
  projectId: string
  projectName?: string
  petId: string
  requestId?: string
  source?: string
  sessionName?: string
  command: AgentCommandName | string
  args?: string[]
  forceReload?: boolean
  cursor?: string
  includeHidden?: boolean
  limit?: number
  delivery?: string
  expectedTurnId?: string
  input?: string
}

export interface AgentCommandResult {
  command: string
  accepted: boolean
  requestId: string
  text: string
  threadId: string
  turnId: string
  skills: AgentSkill[]
  models: AgentModel[]
  raw?: unknown
}

export type AgentInteractionKind = 'approval' | 'permission' | 'user_input' | 'mcp_form'

export interface AgentInteractionOption {
  label: string
  description: string
}

export interface AgentInteractionQuestion {
  id: string
  header: string
  question: string
  secret: boolean
  other: boolean
  options: AgentInteractionOption[]
}

export interface AgentInteraction {
  id: string
  kind: AgentInteractionKind
  method: string
  threadId: string
  turnId: string
  itemId: string
  callId: string
  title: string
  reason: string
  command: string
  cwd: string
  serverName: string
  message: string
  availableDecisions: string[]
  questions: AgentInteractionQuestion[]
  requestedSchema: Record<string, unknown> | null
}

export interface ResolveInteractionRequest {
  interactionId: string
  decision?: string
  action?: string
  scope?: string
  permissions?: Record<string, unknown>
  answers?: Record<string, string[]>
  content?: Record<string, unknown>
}

export interface AgentConversationEvent {
  type: AgentEventType
  petId: string
  requestId: string
  sequence: number
  projectId: string
  source: string
  delta: string
  text: string
  errorCode: string
  interaction?: AgentInteraction
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function booleanValue(value: unknown): boolean {
  return value === true
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function recordFrom(value: unknown): Record<string, unknown> {
  let current = value
  for (let depth = 0; depth < 4; depth += 1) {
    if (Array.isArray(current) && current.length === 1) {
      current = current[0]
      continue
    }
    if (typeof current === 'string' && current.trim()) {
      try {
        current = JSON.parse(current)
        continue
      } catch {
        return {}
      }
    }
    return isRecord(current) ? current : {}
  }
  return isRecord(current) ? current : {}
}

function arrayValue(value: unknown): unknown[] {
  return Array.isArray(value) ? value : []
}

export function normalizeAgentInteraction(value: unknown): AgentInteraction | null {
  const source = recordFrom(value)
  const id = stringValue(source.id).trim()
  const kind = stringValue(source.kind).trim().toLowerCase() as AgentInteractionKind
  if (!id || !['approval', 'permission', 'user_input', 'mcp_form'].includes(kind)) return null

  const questions = arrayValue(source.questions).flatMap((candidate) => {
    const question = recordFrom(candidate)
    const questionId = stringValue(question.id).trim()
    const text = stringValue(question.question).trim()
    if (!questionId || !text) return []
    const options = arrayValue(question.options).flatMap((optionCandidate) => {
      const option = recordFrom(optionCandidate)
      const label = stringValue(option.label).trim()
      return label ? [{
        label,
        description: stringValue(option.description).trim()
      }] : []
    })
    return [{
      id: questionId,
      header: stringValue(question.header).trim(),
      question: text,
      secret: booleanValue(question.isSecret ?? question.secret),
      other: booleanValue(question.isOther ?? question.other),
      options
    }]
  })

  const schema = recordFrom(source.requestedSchema)
  return {
    id,
    kind,
    method: stringValue(source.method).trim(),
    threadId: stringValue(source.threadId).trim(),
    turnId: stringValue(source.turnId).trim(),
    itemId: stringValue(source.itemId).trim(),
    callId: stringValue(source.callId).trim(),
    title: stringValue(source.title).trim(),
    reason: stringValue(source.reason).trim(),
    command: stringValue(source.command).trim(),
    cwd: stringValue(source.cwd).trim(),
    serverName: stringValue(source.serverName).trim(),
    message: stringValue(source.message).trim(),
    availableDecisions: arrayValue(source.availableDecisions).flatMap((decision) => {
      const normalized = stringValue(decision).trim()
      return normalized ? [normalized] : []
    }),
    questions,
    requestedSchema: Object.keys(schema).length > 0 ? schema : null
  }
}

export function normalizeAgentConversationEvent(value: unknown): AgentConversationEvent | null {
  const source = recordFrom(value)
  const data = recordFrom(source.data)
  const field = (name: string, alternate = ''): unknown => source[name] ?? data[name] ?? source[alternate] ?? data[alternate]
  const rawType = stringValue(field('type')).trim().toLowerCase()
  const typeMap: Record<string, AgentEventType | undefined> = {
    queued: 'queued',
    start: 'started',
    started: 'started',
    progress: 'progress',
    delta: 'delta',
    interaction: 'interaction',
    usage: 'progress',
    completed: 'completed',
    done: 'completed',
    failed: 'failed',
    error: 'failed',
    cancelled: 'cancelled',
    canceled: 'cancelled'
  }
  const type = typeMap[rawType]
  const requestId = stringValue(field('requestId', 'request_id')).trim()
  if (!type || !requestId) return null

  const errorValue = source.error ?? data.error
  const errorRecord = isRecord(errorValue) ? errorValue : null
  const interaction = normalizeAgentInteraction(source.interaction ?? data.interaction)
  return {
    type,
    petId: stringValue(field('petId', 'pet_id')).trim(),
    requestId,
    sequence: numberValue(field('sequence')),
    projectId: stringValue(field('projectId', 'project_id')).trim(),
    source: stringValue(field('source')).trim().toLowerCase(),
    delta: stringValue(field('delta')),
    text: stringValue(field('text')) || stringValue(data.content),
    errorCode: stringValue(errorRecord?.code) || stringValue(source.code) || stringValue(data.code) || stringValue(errorValue),
    interaction: interaction ?? undefined
  }
}
