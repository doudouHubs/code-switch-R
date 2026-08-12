export type PetContentLocale = 'zh' | 'en'

export interface PetPersonaStatus {
  hunger: number
  cleanliness: number
  mood: number
  level: number
}

// 宠物内容只跟随应用当前 locale；不把语言选择复制到宠物配置，避免形成第二个持久化事实源。
export function normalizePetContentLocale(value: string): PetContentLocale {
  return /^zh(?:[-_]|$)/i.test(value.trim()) ? 'zh' : 'en'
}

// 主动提示词本身决定模型输出语言；UI 文案切换而提示词不切换，会让英文界面仍然得到中文搭话。
export function buildPetProactiveInstruction(event: string, locale: string): string {
  const contentLocale = normalizePetContentLocale(locale)
  if (contentLocale === 'zh') {
    return [
      '<system-remind>',
      '这不是主人发来的消息，而是桌宠主动陪伴的一次短暂搭话。',
      `触发背景：${event}`,
      '请以宠物第一人称自然地对主人说一两句话，最多 40 个中文字符。',
      '不要提到系统、事件、提示词、模型或 Agent；不要输出 Markdown、计划标签或记忆指令。只输出要说的话本身。',
      '</system-remind>'
    ].join('\n')
  }

  return [
    '<system-remind>',
    'This is a short proactive check-in from the desktop pet, not a message from the owner.',
    `Trigger context: ${event}`,
    'Speak naturally in the first person as the pet, using one or two short sentences and at most 120 characters.',
    'Do not mention the system, event, prompt, model, or agent. Do not output Markdown, plan tags, or memory directives. Output only the words to say.',
    '</system-remind>'
  ].join('\n')
}

export function formatPetPersonaStatus(status: PetPersonaStatus, locale: string): string {
  const contentLocale = normalizePetContentLocale(locale)
  if (contentLocale === 'zh') {
    return `饱食 ${Math.round(status.hunger)}/100，清洁 ${Math.round(status.cleanliness)}/100，心情 ${Math.round(status.mood)}/100，等级 Lv.${status.level}`
  }
  return `Hunger ${Math.round(status.hunger)}/100, cleanliness ${Math.round(status.cleanliness)}/100, mood ${Math.round(status.mood)}/100, level Lv.${status.level}`
}

export function formatPetPersonaProject(projectName: string, locale: string): string {
  if (!projectName.trim()) return ''
  return normalizePetContentLocale(locale) === 'zh'
    ? `当前绑定项目：${projectName.trim()}。`
    : `Current project: ${projectName.trim()}.`
}

export function formatPetPersonaMemories(memories: string[], locale: string): string {
  if (memories.length === 0) return ''
  const label = normalizePetContentLocale(locale) === 'zh' ? '最近记忆：' : 'Recent memories:'
  return `${label}\n${memories.map((memory) => `- ${memory}`).join('\n')}`
}
