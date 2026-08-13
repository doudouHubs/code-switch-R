import type {
  PetAtlasAsset,
  PetSkinRecord
} from './petTypes'
import type { PetAtlasFrame } from './petAtlas'

/** Studio 只接受业务元数据；路径字段属于后端内部存储，不能进入编辑器状态。 */
export type PetStudioSkinRecord = Omit<PetSkinRecord, 'path' | 'atlasPath'>

export const PET_STUDIO_BUILTIN_ACTIONS = [
  'idle',
  'walk',
  'sleep',
  'beg',
  'eat',
  'munch',
  'bathe',
  'soak',
  'swim',
  'zen',
  'play',
  'held',
  'report-time'
] as const

export const PET_STUDIO_BUILTIN_BEHAVIORS = [
  'feed',
  'idle',
  'walk',
  'sleep',
  'bathe',
  'soak',
  'swim',
  'zen',
  'play',
  'drag',
  'beg',
  'report-time'
] as const

export type PetStudioActionId = string
export type PetStudioBuiltinActionId = (typeof PET_STUDIO_BUILTIN_ACTIONS)[number]

export interface PetStudioBounds {
  x: number
  y: number
  width: number
  height: number
}

export interface PetStudioFrameGeometry {
  width: number
  height: number
  subjectBounds: PetStudioBounds
}

export type PetStudioFrameSource =
  | { kind: 'atlas'; pose: PetStudioActionId; frameIndex: number }
  // 生成帧只保留 data URL；本地文件路径不能进入 Vue 状态或保存请求。
  | { kind: 'file'; dataUrl: string }

export interface PetStudioFrame {
  id: string
  source: PetStudioFrameSource
  durationMs: number
  geometry: PetStudioFrameGeometry
}

export interface PetStudioAnimation {
  loop: boolean
  label?: string
  description?: string
  frames: PetStudioFrame[]
}

export interface PetStudioBehavior {
  label?: string
  actions: PetStudioActionId[]
}

export type PetStudioBehaviorMap = Record<string, PetStudioBehavior>

export interface PetStudioProjectSource {
  kind: 'new' | 'default' | 'skin'
  skinId?: string
  canUpdate: boolean
  atlas: PetAtlasAsset | null
}

export interface PetStudioProject {
  source: PetStudioProjectSource
  name: string
  subject: string
  modelId: string
  createdAt?: number
  updatedAt?: number
  builtin?: boolean
  assetVersion?: number
  spriteNormalizationVersion?: number
  animations: Partial<Record<PetStudioActionId, PetStudioAnimation>>
  behaviors: PetStudioBehaviorMap
}

export type PetStudioProjectAction =
  | { type: 'load'; project: PetStudioProject }
  | { type: 'set-name'; name: string }
  | { type: 'set-subject'; subject: string }
  | { type: 'set-model'; modelId: string }
  | { type: 'set-animation'; pose: PetStudioActionId; animation: PetStudioAnimation }
  | { type: 'add-action'; actionId: PetStudioActionId; animation: PetStudioAnimation }
  | { type: 'delete-action'; actionId: PetStudioActionId }
  | { type: 'set-animation-label'; pose: PetStudioActionId; label: string }
  | { type: 'set-animation-description'; pose: PetStudioActionId; description: string }
  | { type: 'append-frame'; pose: PetStudioActionId; frame: PetStudioFrame; afterId?: string }
  | { type: 'append-frames'; pose: PetStudioActionId; frames: PetStudioFrame[]; afterId?: string }
  | { type: 'replace-frame'; pose: PetStudioActionId; frameId: string; frame: PetStudioFrame }
  | { type: 'replace-animation-frames'; pose: PetStudioActionId; frames: PetStudioFrame[] }
  | { type: 'delete-frame'; pose: PetStudioActionId; frameId: string }
  | { type: 'move-frame'; pose: PetStudioActionId; frameId: string; direction: -1 | 1 }
  | { type: 'set-duration'; pose: PetStudioActionId; frameId: string; durationMs: number }
  | { type: 'set-behavior-label'; behaviorId: string; label: string }
  | { type: 'set-behavior-actions'; behaviorId: string; actions: PetStudioActionId[] }
  | { type: 'add-behavior'; behaviorId: string; behavior: PetStudioBehavior }
  | { type: 'delete-behavior'; behaviorId: string }

const MAX_ACTIONS = 64
const MAX_BEHAVIORS = 64
const MAX_FRAMES = 8
const MAX_ACTION_ID_LENGTH = 64
const MAX_BEHAVIOR_ID_LENGTH = 64
const MAX_LABEL_LENGTH = 80
const MAX_DESCRIPTION_LENGTH = 500
const DEFAULT_FRAME_DURATION = 240

export function isBuiltinPetStudioAction(actionId: string): boolean {
  return (PET_STUDIO_BUILTIN_ACTIONS as readonly string[]).includes(actionId)
}

export function getPetStudioActionIds(
  animations: Partial<Record<PetStudioActionId, unknown>>
): PetStudioActionId[] {
  const available = new Set(Object.keys(animations))
  const builtin = PET_STUDIO_BUILTIN_ACTIONS.filter((id) => available.has(id))
  const custom = Object.keys(animations)
    .filter((id) => !(PET_STUDIO_BUILTIN_ACTIONS as readonly string[]).includes(id))
    .sort()
  return [...builtin, ...custom]
}

export function getDefaultPetStudioBehaviors(): PetStudioBehaviorMap {
  return {
    feed: { actions: ['eat', 'munch'] },
    idle: { actions: ['idle'] },
    walk: { actions: ['walk'] },
    sleep: { actions: ['sleep'] },
    bathe: { actions: ['bathe'] },
    soak: { actions: ['soak'] },
    swim: { actions: ['swim'] },
    zen: { actions: ['zen'] },
    play: { actions: ['play'] },
    drag: { actions: ['held'] },
    beg: { actions: ['beg'] },
    'report-time': { actions: ['report-time'] }
  }
}

export function createBlankPetStudioProject(): PetStudioProject {
  return {
    source: { kind: 'new', canUpdate: false, atlas: null },
    name: '',
    subject: '',
    modelId: '',
    assetVersion: 1,
    spriteNormalizationVersion: 1,
    animations: {},
    behaviors: getDefaultPetStudioBehaviors()
  }
}

function cloneBehavior(value: PetStudioBehavior): PetStudioBehavior {
  return { ...(value.label ? { label: value.label } : {}), actions: [...value.actions] }
}

function mergeBehaviors(overrides: Record<string, PetStudioBehavior> | undefined): PetStudioBehaviorMap {
  const result: PetStudioBehaviorMap = {}
  for (const [id, behavior] of Object.entries(getDefaultPetStudioBehaviors())) {
    result[id] = cloneBehavior(behavior)
  }
  for (const [id, behavior] of Object.entries(overrides ?? {})) {
    result[id] = cloneBehavior(behavior)
  }
  return result
}

function readNumber(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

function readBounds(value: unknown, fallback: PetStudioBounds): PetStudioBounds {
  const source = value && typeof value === 'object' ? (value as Record<string, unknown>) : {}
  return {
    x: Math.max(0, Math.floor(readNumber(source.x, fallback.x))),
    y: Math.max(0, Math.floor(readNumber(source.y, fallback.y))),
    width: Math.max(1, Math.floor(readNumber(source.width, fallback.width))),
    height: Math.max(1, Math.floor(readNumber(source.height, fallback.height)))
  }
}

export function createImportedPetStudioProject(
  atlas: PetAtlasAsset,
  skin?: PetStudioSkinRecord
): PetStudioProject {
  const manifest = atlas.manifest as unknown as Record<string, unknown>
  const animationsRecord = (manifest.animations ?? {}) as Record<string, unknown>
  const animations: Partial<Record<PetStudioActionId, PetStudioAnimation>> = {}

  for (const pose of getPetStudioActionIds(animationsRecord)) {
    const raw = animationsRecord[pose]
    if (!raw || typeof raw !== 'object') continue
    const animation = raw as Record<string, unknown>
    const rawFrames = Array.isArray(animation.frames) ? animation.frames : []
    const frames = rawFrames.map((rawFrame, frameIndex) => {
      const frame = rawFrame && typeof rawFrame === 'object' ? (rawFrame as Record<string, unknown>) : {}
      const width = Math.max(1, Math.floor(readNumber(frame.width, 1)))
      const height = Math.max(1, Math.floor(readNumber(frame.height, 1)))
      const subjectBounds = readBounds(frame.subjectBounds, { x: 0, y: 0, width, height })
      return {
        id: `atlas:${pose}:${frameIndex}`,
        source: { kind: 'atlas' as const, pose, frameIndex },
        durationMs: Math.min(60_000, Math.max(16, Math.floor(readNumber(frame.durationMs, DEFAULT_FRAME_DURATION)))),
        geometry: { width, height, subjectBounds }
      }
    })
    if (!frames.length) continue
    animations[pose] = {
      loop: animation.loop !== false,
      ...(typeof animation.label === 'string' && animation.label.trim() ? { label: animation.label.trim() } : {}),
      ...(typeof animation.description === 'string' && animation.description.trim()
        ? { description: animation.description.trim() }
        : {}),
      frames
    }
  }

  return {
    source: skin
      ? { kind: 'skin', skinId: skin.skinId, canUpdate: skin.builtin !== true, atlas }
      : { kind: 'default', canUpdate: false, atlas },
    name: typeof manifest.name === 'string' ? manifest.name : skin?.name ?? 'Pet',
    subject: typeof manifest.subject === 'string' ? manifest.subject : skin?.subject ?? '',
    modelId: typeof manifest.modelId === 'string' ? manifest.modelId : skin?.modelId ?? '',
    createdAt: typeof manifest.createdAt === 'number' ? manifest.createdAt : skin?.createdAt,
    updatedAt: typeof manifest.updatedAt === 'number' ? manifest.updatedAt : skin?.updatedAt,
    builtin: typeof manifest.builtin === 'boolean' ? manifest.builtin : skin?.builtin,
    assetVersion: typeof manifest.assetVersion === 'number' ? manifest.assetVersion : skin?.assetVersion,
    spriteNormalizationVersion:
      typeof manifest.spriteNormalizationVersion === 'number'
        ? manifest.spriteNormalizationVersion
        : skin?.spriteNormalizationVersion,
    animations,
    behaviors: mergeBehaviors(
      manifest.behaviors && typeof manifest.behaviors === 'object'
        ? (manifest.behaviors as Record<string, PetStudioBehavior>)
        : undefined
    )
  }
}

export function getPetStudioAtlasFrame(
  project: PetStudioProject,
  pose: PetStudioActionId,
  frameIndex: number
): PetAtlasFrame | null {
  const atlas = project.source.atlas?.manifest
  const animation = atlas?.animations[pose]
  if (!animation || !Number.isInteger(frameIndex) || frameIndex < 0 || frameIndex >= animation.frames.length) {
    return null
  }
  return animation.frames[frameIndex]
}

function isValidActionId(value: string): boolean {
  return value.length > 0 && value.length <= MAX_ACTION_ID_LENGTH && /^[a-zA-Z][a-zA-Z0-9_-]*$/.test(value)
}

function isValidBehaviorId(value: string): boolean {
  return value.length > 0 && value.length <= MAX_BEHAVIOR_ID_LENGTH && /^[a-zA-Z][a-zA-Z0-9_-]*$/.test(value)
}

function isValidActionList(actions: string[]): boolean {
  return actions.length > 0 && actions.length <= MAX_ACTIONS && new Set(actions).size === actions.length && actions.every(isValidActionId)
}

function updateAnimation(
  project: PetStudioProject,
  pose: string,
  update: (animation: PetStudioAnimation | undefined) => PetStudioAnimation | undefined
): PetStudioProject {
  const animations = { ...project.animations }
  const next = update(animations[pose])
  if (next) animations[pose] = next
  else delete animations[pose]
  return { ...project, animations }
}

function updateBehavior(
  project: PetStudioProject,
  behaviorId: string,
  update: (behavior: PetStudioBehavior) => PetStudioBehavior | undefined
): PetStudioProject {
  const current = project.behaviors[behaviorId]
  if (!current) return project
  const next = update(current)
  const behaviors = { ...project.behaviors }
  if (next) behaviors[behaviorId] = cloneBehavior(next)
  else delete behaviors[behaviorId]
  return { ...project, behaviors }
}

export function petStudioProjectReducer(
  project: PetStudioProject,
  action: PetStudioProjectAction
): PetStudioProject {
  switch (action.type) {
    case 'load':
      return action.project
    case 'set-name':
      return { ...project, name: action.name }
    case 'set-subject':
      return { ...project, subject: action.subject }
    case 'set-model':
      return { ...project, modelId: action.modelId }
    case 'set-animation':
      if (!isValidActionId(action.pose) || action.animation.frames.length < 1 || action.animation.frames.length > MAX_FRAMES) return project
      return updateAnimation(project, action.pose, () => action.animation)
    case 'add-action':
      if (
        !isValidActionId(action.actionId) ||
        isBuiltinPetStudioAction(action.actionId) ||
        project.animations[action.actionId] ||
        Object.keys(project.animations).length >= MAX_ACTIONS ||
        action.animation.frames.length < 1 ||
        action.animation.frames.length > MAX_FRAMES
      ) return project
      return { ...project, animations: { ...project.animations, [action.actionId]: action.animation } }
    case 'delete-action':
      if (action.actionId === 'idle' || isBuiltinPetStudioAction(action.actionId) || !project.animations[action.actionId]) return project
      if (Object.values(project.behaviors).some((behavior) => behavior.actions.includes(action.actionId))) return project
      return { ...project, animations: Object.fromEntries(Object.entries(project.animations).filter(([id]) => id !== action.actionId)) }
    case 'set-animation-label':
      if (action.label.length > MAX_LABEL_LENGTH) return project
      return updateAnimation(project, action.pose, (animation) => {
        if (!animation) return animation
        const label = action.label.trim()
        return label ? { ...animation, label } : { loop: animation.loop, ...(animation.description ? { description: animation.description } : {}), frames: animation.frames }
      })
    case 'set-animation-description':
      if (action.description.length > MAX_DESCRIPTION_LENGTH) return project
      return updateAnimation(project, action.pose, (animation) => {
        if (!animation) return animation
        const description = action.description.trim()
        return description ? { ...animation, description } : { loop: animation.loop, ...(animation.label ? { label: animation.label } : {}), frames: animation.frames }
      })
    case 'append-frame':
      return updateAnimation(project, action.pose, (animation) => {
        const current = animation ?? { loop: true, frames: [] }
        if (current.frames.length >= MAX_FRAMES) return current
        const frames = [...current.frames]
        const index = action.afterId ? frames.findIndex((frame) => frame.id === action.afterId) : -1
        frames.splice(index >= 0 ? index + 1 : frames.length, 0, action.frame)
        return { ...current, frames }
      })
    case 'append-frames':
      return updateAnimation(project, action.pose, (animation) => {
        const current = animation ?? { loop: true, frames: [] }
        if (!action.frames.length || current.frames.length + action.frames.length > MAX_FRAMES) return current
        const existing = new Set(current.frames.map((frame) => frame.id))
        if (action.frames.some((frame) => existing.has(frame.id))) return current
        const frames = [...current.frames]
        const index = action.afterId ? frames.findIndex((frame) => frame.id === action.afterId) : -1
        frames.splice(index >= 0 ? index + 1 : frames.length, 0, ...action.frames)
        return { ...current, frames }
      })
    case 'replace-frame':
      return updateAnimation(project, action.pose, (animation) => {
        if (!animation) return animation
        const index = animation.frames.findIndex((frame) => frame.id === action.frameId)
        if (index < 0) return animation
        const frames = [...animation.frames]
        frames[index] = { ...action.frame, id: frames[index].id, durationMs: frames[index].durationMs }
        return { ...animation, frames }
      })
    case 'replace-animation-frames':
      return updateAnimation(project, action.pose, (animation) => {
        if (!animation || action.frames.length !== animation.frames.length) return animation
        return {
          ...animation,
          frames: action.frames.map((frame, index) => ({ ...frame, id: animation.frames[index].id, durationMs: animation.frames[index].durationMs }))
        }
      })
    case 'delete-frame':
      return updateAnimation(project, action.pose, (animation) => {
        if (!animation || (action.pose === 'idle' && animation.frames.length === 1)) return animation
        const frames = animation.frames.filter((frame) => frame.id !== action.frameId)
        return frames.length ? { ...animation, frames } : undefined
      })
    case 'move-frame':
      return updateAnimation(project, action.pose, (animation) => {
        if (!animation) return animation
        const from = animation.frames.findIndex((frame) => frame.id === action.frameId)
        const to = from + action.direction
        if (from < 0 || to < 0 || to >= animation.frames.length) return animation
        const frames = [...animation.frames]
        ;[frames[from], frames[to]] = [frames[to], frames[from]]
        return { ...animation, frames }
      })
    case 'set-duration':
      return updateAnimation(project, action.pose, (animation) => {
        if (!animation || !Number.isInteger(action.durationMs)) return animation
        const durationMs = Math.min(60_000, Math.max(16, action.durationMs))
        return { ...animation, frames: animation.frames.map((frame) => frame.id === action.frameId ? { ...frame, durationMs } : frame) }
      })
    case 'set-behavior-label':
      if (action.label.length > MAX_LABEL_LENGTH) return project
      return updateBehavior(project, action.behaviorId, (behavior) => {
        const label = action.label.trim()
        return label ? { ...behavior, label } : { actions: [...behavior.actions] }
      })
    case 'set-behavior-actions':
      return isValidActionList(action.actions)
        ? updateBehavior(project, action.behaviorId, (behavior) => ({ ...behavior, actions: [...action.actions] }))
        : project
    case 'add-behavior':
      if (!isValidBehaviorId(action.behaviorId) || project.behaviors[action.behaviorId] || Object.keys(project.behaviors).length >= MAX_BEHAVIORS || !isValidActionList(action.behavior.actions)) return project
      return { ...project, behaviors: { ...project.behaviors, [action.behaviorId]: cloneBehavior(action.behavior) } }
    case 'delete-behavior':
      if ((PET_STUDIO_BUILTIN_BEHAVIORS as readonly string[]).includes(action.behaviorId) || !project.behaviors[action.behaviorId]) return project
      return updateBehavior(project, action.behaviorId, () => undefined)
    default:
      return project
  }
}

export function createGeneratedPetStudioFrame(args: {
  id: string
  dataUrl: string
  geometry: PetStudioFrameGeometry
  durationMs?: number
}): PetStudioFrame {
  return {
    id: args.id,
    source: { kind: 'file', dataUrl: args.dataUrl },
    durationMs: args.durationMs ?? DEFAULT_FRAME_DURATION,
    geometry: args.geometry
  }
}

export function toPetStudioPackAnimations(project: PetStudioProject): Record<string, unknown> {
  const animations: Record<string, unknown> = {}
  for (const pose of getPetStudioActionIds(project.animations)) {
    const animation = project.animations[pose]
    if (!animation?.frames.length) continue
    animations[pose] = {
      loop: animation.loop,
      ...(animation.label?.trim() ? { label: animation.label.trim() } : {}),
      ...(animation.description?.trim() ? { description: animation.description.trim() } : {}),
      frames: animation.frames.map((frame) => ({
        kind: frame.source.kind,
        ...(frame.source.kind === 'file'
          ? { data: frame.source.dataUrl }
          : { pose: frame.source.pose, frameIndex: frame.source.frameIndex }),
        durationMs: frame.durationMs
      }))
    }
  }
  return animations
}

export function toPetStudioPackBehaviors(project: PetStudioProject): PetStudioBehaviorMap {
  return Object.fromEntries(Object.entries(project.behaviors).map(([id, behavior]) => [id, cloneBehavior(behavior)]))
}

export function fingerprintPetStudioProject(project: PetStudioProject): string {
  return JSON.stringify({
    name: project.name.trim(),
    subject: project.subject.trim(),
    modelId: project.modelId.trim(),
    createdAt: project.createdAt ?? null,
    updatedAt: project.updatedAt ?? null,
    builtin: project.builtin ?? false,
    assetVersion: project.assetVersion ?? null,
    spriteNormalizationVersion: project.spriteNormalizationVersion ?? null,
    behaviors: Object.keys(project.behaviors).sort().map((id) => [id, project.behaviors[id].label?.trim() ?? '', project.behaviors[id].actions]),
    animations: getPetStudioActionIds(project.animations).map((pose) => {
      const animation = project.animations[pose]
      return [
        pose,
        animation?.loop,
        animation?.label?.trim() ?? '',
        animation?.description?.trim() ?? '',
        animation?.frames.map((frame) => [
          frame.id,
          frame.durationMs,
          frame.source.kind,
          frame.source.kind === 'file'
            ? frame.source.dataUrl
            : `${frame.source.pose}:${frame.source.frameIndex}`,
          frame.geometry
        ])
      ]
    })
  })
}
