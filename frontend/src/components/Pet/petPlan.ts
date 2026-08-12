import type {
  PetAction,
  PetPlanSchedule,
  PetPlanScript,
  PetPlanStep
} from './petTypes'

export const PET_PLAN_DIRECTIVE_START = '<pet-plan>'
export const PET_PLAN_DIRECTIVE_END = '</pet-plan>'

const PET_PLAN_VERSION = 1
const PET_PLAN_MAX_STEPS = 16
const PET_PLAN_MAX_TEXT_LENGTH = 240
const PET_PLAN_MAX_DELAY_SECONDS = 30 * 24 * 60 * 60
const PET_PLAN_MIN_INTERVAL_MS = 60 * 1000
const PET_PLAN_MAX_INTERVAL_MS = 365 * 24 * 60 * 60 * 1000
const PET_PLAN_ACTIONS: PetAction[] = [
  'feed',
  'bathe',
  'soak',
  'play',
  'sleep',
  'work',
  'study'
]

const PET_ACTION_LABELS: Record<PetAction, string> = {
  feed: '喂食',
  bathe: '洗澡',
  soak: '泡澡',
  play: '玩耍',
  sleep: '睡眠',
  work: '工作',
  study: '学习'
}

export interface PetPlanExtraction {
  reply: string
  plan: PetPlanScript | null
  error?: string
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

function isNonEmptyText(value: unknown, maxLength = PET_PLAN_MAX_TEXT_LENGTH): value is string {
  return typeof value === 'string' && value.trim().length > 0 && value.trim().length <= maxLength
}

function isPetAction(value: unknown): value is PetAction {
  return typeof value === 'string' && PET_PLAN_ACTIONS.includes(value as PetAction)
}

function normalizeSchedule(value: unknown): PetPlanSchedule | undefined {
  if (value === undefined) return undefined
  if (!isRecord(value) || typeof value.kind !== 'string') {
    throw new Error('schedule 必须是包含 kind 的对象')
  }

  switch (value.kind) {
    case 'now':
      return { kind: 'now' }
    case 'delay':
      if (
        !isFiniteNumber(value.delaySeconds) ||
        value.delaySeconds <= 0 ||
        value.delaySeconds > PET_PLAN_MAX_DELAY_SECONDS
      ) {
        throw new Error('delaySeconds 超出允许范围')
      }
      return { kind: 'delay', delaySeconds: value.delaySeconds }
    case 'at': {
      const at = value.at
      const validNumber = isFiniteNumber(at) && at > 0
      const validString = typeof at === 'string' && !Number.isNaN(new Date(at).getTime())
      if (!validNumber && !validString) throw new Error('at 不是有效时间')
      if (value.tz !== undefined && !isNonEmptyText(value.tz, 80)) {
        throw new Error('tz 不是有效时区')
      }
      return {
        kind: 'at',
        at,
        ...(typeof value.tz === 'string' ? { tz: value.tz.trim() } : {})
      }
    }
    case 'every':
      if (
        !isFiniteNumber(value.everyMs) ||
        value.everyMs < PET_PLAN_MIN_INTERVAL_MS ||
        value.everyMs > PET_PLAN_MAX_INTERVAL_MS
      ) {
        throw new Error('everyMs 超出允许范围')
      }
      return { kind: 'every', everyMs: Math.round(value.everyMs) }
    case 'cron':
      if (!isNonEmptyText(value.expr, 100)) throw new Error('cron 表达式不能为空')
      if (value.tz !== undefined && !isNonEmptyText(value.tz, 80)) {
        throw new Error('tz 不是有效时区')
      }
      return {
        kind: 'cron',
        expr: value.expr.trim(),
        ...(typeof value.tz === 'string' ? { tz: value.tz.trim() } : {})
      }
    default:
      throw new Error(`不支持的 schedule.kind：${value.kind}`)
  }
}

/**
 * 先做轻量结构归一化，再把完整规则交给后端 SchedulePlan 复核。
 * 这样 malformed JSON 只会变成计划错误，不会穿透到模板渲染或异步回调。
 */
export function validatePetPlan(value: unknown): { ok: true; value: PetPlanScript } | { ok: false; error: string } {
  if (!isRecord(value)) return { ok: false, error: '计划必须是对象' }
  if (value.version !== PET_PLAN_VERSION) return { ok: false, error: `不支持的计划版本：${String(value.version)}` }
  if (!Array.isArray(value.steps) || value.steps.length === 0) {
    return { ok: false, error: '计划至少需要一个步骤' }
  }
  if (value.steps.length > PET_PLAN_MAX_STEPS) {
    return { ok: false, error: `计划步骤不能超过 ${PET_PLAN_MAX_STEPS} 个` }
  }
  if (value.title !== undefined && !isNonEmptyText(value.title)) {
    return { ok: false, error: '计划标题无效' }
  }

  const steps: PetPlanStep[] = []
  try {
    for (let index = 0; index < value.steps.length; index += 1) {
      const raw = value.steps[index]
      if (!isRecord(raw) || (raw.kind !== 'action' && raw.kind !== 'reminder')) {
        return { ok: false, error: `第 ${index + 1} 步类型无效` }
      }

      const schedule = normalizeSchedule(raw.schedule)
      if (raw.kind === 'action') {
        if (!isPetAction(raw.action)) return { ok: false, error: `第 ${index + 1} 步动作无效` }
        if (raw.label !== undefined && !isNonEmptyText(raw.label)) {
          return { ok: false, error: `第 ${index + 1} 步标签无效` }
        }
        steps.push({
          kind: 'action',
          action: raw.action,
          ...(schedule ? { schedule } : {}),
          ...(typeof raw.label === 'string' ? { label: raw.label.trim() } : {})
        })
        continue
      }

      if (!isNonEmptyText(raw.text)) return { ok: false, error: `第 ${index + 1} 步提醒内容无效` }
      steps.push({
        kind: 'reminder',
        text: raw.text.trim(),
        ...(schedule ? { schedule } : {})
      })
    }
  } catch (error) {
    return { ok: false, error: error instanceof Error ? error.message : String(error) }
  }

  return {
    ok: true,
    value: {
      version: PET_PLAN_VERSION,
      ...(typeof value.title === 'string' ? { title: value.title.trim() } : {}),
      steps
    }
  }
}

/** 流式期间隐藏协议起点之后的半截内容；完整标签则只移除协议块本身。 */
export function stripPetPlanDirective(text: string): string {
  let visible = text
  while (true) {
    const start = visible.indexOf(PET_PLAN_DIRECTIVE_START)
    if (start < 0) return visible.trim()
    const contentStart = start + PET_PLAN_DIRECTIVE_START.length
    const end = visible.indexOf(PET_PLAN_DIRECTIVE_END, contentStart)
    if (end < 0) return visible.slice(0, start).trim()
    visible = `${visible.slice(0, start)}${visible.slice(end + PET_PLAN_DIRECTIVE_END.length)}`
  }
}

/** 与后端记忆提取规则保持同一标签形状，UI 只负责隐藏，落盘仍由后端负责。 */
export function stripPetMemoryDirectives(text: string): string {
  return text
    .replace(/\[\[\s*(?:记住|remember)\s*[:：]\s*([^\]]+)\]\]/gi, '')
    .replace(/\[\[(?:[^\]]|\][^\]])*$/, '')
    .trim()
}

export function cleanPetAssistantText(text: string): string {
  return stripPetPlanDirective(stripPetMemoryDirectives(text))
}

export function extractPetPlan(raw: string): PetPlanExtraction {
  const start = raw.indexOf(PET_PLAN_DIRECTIVE_START)
  if (start < 0) return { reply: cleanPetAssistantText(raw), plan: null }

  const contentStart = start + PET_PLAN_DIRECTIVE_START.length
  const end = raw.indexOf(PET_PLAN_DIRECTIVE_END, contentStart)
  if (end < 0) {
    return {
      reply: cleanPetAssistantText(raw),
      plan: null,
      error: '计划协议未闭合'
    }
  }

  const json = raw.slice(contentStart, end).trim()
  let value: unknown
  try {
    value = JSON.parse(json)
  } catch {
    return { reply: cleanPetAssistantText(raw), plan: null, error: '计划 JSON 无效' }
  }

  const validated = validatePetPlan(value)
  if (!validated.ok) return { reply: cleanPetAssistantText(raw), plan: null, error: validated.error }
  return { reply: cleanPetAssistantText(raw), plan: validated.value }
}

export function localPetTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
}

/** 规则必须随 persona 发送，否则模型没有生成隐藏协议的契约依据。 */
export function buildPetPlanInstructions(): string {
  const timeZone = localPetTimeZone()
  return `<pet-plan-rules>
计划能力只在主人明确要求现在做、稍后做、到点提醒、每天或每周重复时使用；普通聊天不要输出计划。
允许的宠物动作只有：${PET_PLAN_ACTIONS.join(', ')}。不能生成脚本、Shell、文件操作或其他工具调用。
需要安排时，在最终回复末尾追加且只追加一个隐藏标签：<pet-plan>{"version":1,"title":"可选计划名","steps":[{"kind":"action","action":"feed","schedule":{"kind":"now"}},{"kind":"reminder","text":"开会","schedule":{"kind":"at","at":"2026-01-01T09:00:00","tz":"${timeZone}"}}]}</pet-plan>
step.kind=action 时必须使用允许的 action；step.kind=reminder 时必须提供简短 text。schedule.kind 支持 now、delay（delaySeconds）、at（ISO 时间或毫秒时间戳）、every（everyMs）和 cron（标准 5/6 段表达式 + tz）。相对时间优先使用 delay；绝对时间使用当前本地时区 ${timeZone}。
当前本地时间：${new Date().toISOString()}；当前时区：${timeZone}。如果日期、时间或动作含糊，先用普通短句追问，不要输出计划标签。
</pet-plan-rules>`
}

export function formatPlanDate(value: number): string {
  if (!value) return '时间未知'
  try {
    return new Intl.DateTimeFormat('zh-CN', {
      month: 'numeric',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit'
    }).format(value)
  } catch {
    return '时间未知'
  }
}

export function formatPlanSchedule(schedule?: PetPlanSchedule): string {
  if (!schedule) return '立即'
  switch (schedule.kind) {
    case 'now':
      return '立即'
    case 'delay':
      return `${Math.round(schedule.delaySeconds ?? 0)} 秒后`
    case 'at': {
      const raw = schedule.at
      const timestamp = typeof raw === 'number' ? raw : new Date(String(raw)).getTime()
      return Number.isFinite(timestamp) ? formatPlanDate(timestamp) : '指定时间'
    }
    case 'every':
      return `每 ${Math.round((schedule.everyMs ?? 0) / 60000)} 分钟`
    case 'cron':
      return `定时 · ${schedule.expr || 'cron'}`
  }
}

export function formatPlanStep(step: PetPlanStep): string {
  const body = step.kind === 'action'
    ? (step.label?.trim() || PET_ACTION_LABELS[step.action as PetAction] || '宠物动作')
    : `提醒：${step.text?.trim() || '未填写内容'}`
  return `${body} · ${formatPlanSchedule(step.schedule)}`
}

export function formatPlanError(error: unknown): string {
  if (isRecord(error)) {
    const code = typeof error.code === 'string' ? error.code : ''
    const message = typeof error.message === 'string' ? error.message : ''
    const messages: Record<string, string> = {
      cancel_unavailable: '取消能力尚未接入',
      dependency_unavailable: '计划调度服务不可用',
      invalid_request: '计划请求无效',
      schedule_failed: '计划入队失败',
      plan_record_failed: '计划已入队，但记录保存失败'
    }
    return messages[code] || message || '计划操作失败'
  }
  if (error instanceof Error) return error.message || '计划操作失败'
  return String(error || '计划操作失败')
}
