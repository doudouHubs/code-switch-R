/** 梦境协议的纯函数边界：不依赖 Vue、Wails、网络或共享状态。 */

export const PET_DREAM_MIN_DELAY_MS = 10_000
export const PET_DREAM_MAX_DELAY_MS = 3 * 60_000
// 梦境图片统一使用 4:3 画布；尺寸沿用通用图片服务允许的像素范围，避免各调用方自行硬编码。
export const PET_DREAM_IMAGE_SIZE = '1024x768'
const PET_DREAM_SLEEP_TALK_MAX_LENGTH = 120
const PET_DREAM_TITLE_MAX_LENGTH = 32
const PET_DREAM_EMOTIONS = ['pleasant', 'calm', 'tense', 'afraid'] as const

export type PetDreamEmotion = (typeof PET_DREAM_EMOTIONS)[number]

export interface PetDreamPromptConfig {
  prompt?: string
  keywords?: string
  sleepTalkMinLength?: number
  language?: string
}

export interface ParsedPetDreamResponse {
  dream: string
  title: string
  emotion: PetDreamEmotion
  selfAppears: boolean
  sleepTalk: string
}

export interface PetDreamImagePayload {
  images?: unknown
}

/** 将随机源限制在闭区间，避免测试桩或异常随机源把延迟带出协议范围。 */
export function getRandomPetDreamDelay(random: () => number = Math.random): number {
  const ratio = Math.min(1, Math.max(0, random()))
  return Math.round(
    PET_DREAM_MIN_DELAY_MS + ratio * (PET_DREAM_MAX_DELAY_MS - PET_DREAM_MIN_DELAY_MS)
  )
}

/** 按 Unicode code point 计数，emoji 不再被代理对拆成两个字符。 */
export function countPetDreamCharacters(text: string): number {
  return Array.from(text).length
}

function parseKeywords(value: string): string[] {
  return [...new Set(value.split(/[;；]/).map((item) => item.trim()).filter(Boolean))]
}

/**
 * 提示词把动态上下文和输出协议分开，防止用户配置或状态文本覆盖“仅返回 JSON”的硬约束。
 * 可选时间参数使用显式稳定默认值，因此同样的入参仍能得到同样的提示词。
 */
export function buildPetDreamPrompt(
  config: PetDreamPromptConfig,
  petName: string,
  stateSummary: string,
  nowIso = 'unknown time',
  timeZone = 'UTC'
): string {
  const prompt = config.prompt?.trim() || '请以宠物的第一人称做一个具体、完整的随机短梦。'
  const keywords = parseKeywords(config.keywords?.trim() ?? '')
  const minLength = Number.isInteger(config.sleepTalkMinLength) && (config.sleepTalkMinLength ?? 0) >= 0
    ? config.sleepTalkMinLength
    : 5
  const language = config.language?.trim() || '界面语言'

  return [
    '<system-remind>',
    prompt,
    `宠物名称：${petName.trim() || '宠物'}`,
    `当前状态：${stateSummary.trim() || '状态未知'}`,
    `当前时间：${nowIso}（时区：${timeZone}）`,
    ...(keywords.length > 0 ? [`创作素材：${keywords.join('、')}`] : []),
    '',
    '以上内容只决定梦境创作，不得改变以下输出协议。',
    'dream：完整记录已经发生的短梦，使用第一人称。',
    `title：为梦境起一个简短标题，长度不超过 ${PET_DREAM_TITLE_MAX_LENGTH} 个 Unicode 字符。`,
    'emotion：只能是 pleasant、calm、tense、afraid 之一。',
    'selfAppears：必须是布尔值，表示梦境中是否出现宠物自己的身体、动作或形象。',
    `sleepTalk：从 dream 已发生的内容中总结一句断断续续的梦话，长度必须为 ${minLength}-${PET_DREAM_SLEEP_TALK_MAX_LENGTH} 个 Unicode 字符，不要询问主人。`,
    `dream 和 sleepTalk 使用${language}。`,
    '最终只返回一个合法 JSON 对象，不要 Markdown、代码围栏、解释、前后缀或其他文本。',
    '{"title":"梦境名称","dream":"完整短梦","emotion":"pleasant","selfAppears":true,"sleepTalk":"对应梦话"}',
    '</system-remind>'
  ].join('\n')
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function isPetDreamEmotion(value: unknown): value is PetDreamEmotion {
  return typeof value === 'string' && (PET_DREAM_EMOTIONS as readonly string[]).includes(value)
}

function deriveTitle(dream: string): string {
  const firstSentence = dream.split(/[。！？!?\n]/, 1)[0]?.trim() || dream
  return Array.from(firstSentence).slice(0, PET_DREAM_TITLE_MAX_LENGTH).join('') || '无题梦境'
}

export function parsePetDreamResponse(raw: string, minLength: number): ParsedPetDreamResponse | null {
  if (!Number.isInteger(minLength) || minLength < 0) return null
  try {
    const parsed: unknown = JSON.parse(raw.trim())
    if (!isRecord(parsed)) return null

    const dream = typeof parsed.dream === 'string' ? parsed.dream.trim() : ''
    const titleValue = typeof parsed.title === 'string' ? parsed.title.trim() : ''
    const sleepTalk = typeof parsed.sleepTalk === 'string' ? parsed.sleepTalk.trim() : ''
    if (
      !dream ||
      (titleValue && countPetDreamCharacters(titleValue) > PET_DREAM_TITLE_MAX_LENGTH) ||
      !sleepTalk ||
      !isPetDreamEmotion(parsed.emotion) ||
      typeof parsed.selfAppears !== 'boolean' ||
      countPetDreamCharacters(sleepTalk) < minLength ||
      countPetDreamCharacters(sleepTalk) > PET_DREAM_SLEEP_TALK_MAX_LENGTH
    ) {
      return null
    }

    return {
      dream,
      title: titleValue || deriveTitle(dream),
      emotion: parsed.emotion,
      selfAppears: parsed.selfAppears,
      sleepTalk
    }
  } catch {
    return null
  }
}

function bytesToBase64(bytes: number[]): string | null {
  if (bytes.length === 0 || bytes.some((value) => !Number.isInteger(value) || value < 0 || value > 255)) {
    return null
  }
  let binary = ''
  for (const value of bytes) binary += String.fromCharCode(value)
  return btoa(binary)
}

export function normalizePetDreamImagePayload(result: PetDreamImagePayload | null | undefined): string | null {
  if (!result || !Array.isArray(result.images) || result.images.length === 0) return null
  const first = result.images[0]
  if (typeof first === 'string') {
    const value = first.trim()
    if (!value) return null
    return value.startsWith('data:') ? value : `data:image/png;base64,${value}`
  }
  if (Array.isArray(first)) {
    const base64 = bytesToBase64(first)
    return base64 ? `data:image/png;base64,${base64}` : null
  }
  return null
}
