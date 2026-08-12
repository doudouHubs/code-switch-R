export const PET_POSE_KEYS = [
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

export type PetPoseKey = (typeof PET_POSE_KEYS)[number]
export type PetActionId = string

export const PET_BEHAVIOR_KEYS = [
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

export type PetBehaviorKey = (typeof PET_BEHAVIOR_KEYS)[number]

export const PET_ATLAS_VERSION = 1
export const PET_ATLAS_IMAGE_NAME = 'atlas.png'
export const PET_ATLAS_ALTERNATE_IMAGE_NAME = 'atlas.next.png'
export type PetAtlasImageName =
  | typeof PET_ATLAS_IMAGE_NAME
  | typeof PET_ATLAS_ALTERNATE_IMAGE_NAME

export const PET_ATLAS_MAX_BEHAVIORS = 64
export const PET_ATLAS_MAX_BEHAVIOR_ID_LENGTH = 64
export const PET_ATLAS_MAX_BEHAVIOR_LABEL_LENGTH = 80
export const PET_ATLAS_MAX_ACTIONS = 64
export const PET_ATLAS_MAX_ACTION_ID_LENGTH = 64
export const PET_ATLAS_MAX_ACTION_LABEL_LENGTH = 80
export const PET_ATLAS_MAX_FRAMES_PER_ACTION = 8
export const PET_ATLAS_MAX_ACTION_DESCRIPTION_LENGTH = 500
export const PET_ATLAS_MAX_TEXTURE_SIZE = 8192

export interface PetAtlasBounds {
  x: number
  y: number
  width: number
  height: number
}

export interface PetAtlasFrame extends PetAtlasBounds {
  durationMs: number
  /** 相对于当前 atlas frame 的主体边界，不是整张 atlas 的坐标。 */
  subjectBounds: PetAtlasBounds
}

export interface PetAtlasAnimation {
  loop: boolean
  label?: string
  description?: string
  frames: PetAtlasFrame[]
}

export interface PetAtlasBehavior {
  label?: string
  actions: PetActionId[]
}

export type PetAtlasBehaviorMap = Record<string, PetAtlasBehavior>
export type PetAtlasAnimationMap = Partial<Record<PetActionId, PetAtlasAnimation>> & {
  idle: PetAtlasAnimation
}

export interface PetAtlasDescriptor {
  image: PetAtlasImageName
  width: number
  height: number
  anchor: 'bottom-center'
  layout: 'action-rows'
}

export interface PetAtlasDocument {
  name: string
  subject?: string
  modelId?: string
  createdAt?: number
  updatedAt?: number
  builtin?: boolean
  assetVersion?: number
  spriteNormalizationVersion?: number
  atlasVersion: typeof PET_ATLAS_VERSION
  atlas: PetAtlasDescriptor
  animations: PetAtlasAnimationMap
  /** 省略时由运行时与默认行为映射合并。 */
  behaviors?: PetAtlasBehaviorMap
}

export const DEFAULT_PET_ATLAS_BEHAVIORS: Record<PetBehaviorKey, PetAtlasBehavior> = {
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

export function isValidPetActionId(actionId: string): boolean {
  return (
    actionId.length > 0 &&
    actionId.length <= PET_ATLAS_MAX_ACTION_ID_LENGTH &&
    /^[a-zA-Z][a-zA-Z0-9_-]*$/.test(actionId)
  )
}

export function isValidPetBehaviorId(behaviorId: string): boolean {
  return (
    behaviorId.length > 0 &&
    behaviorId.length <= PET_ATLAS_MAX_BEHAVIOR_ID_LENGTH &&
    /^[a-zA-Z][a-zA-Z0-9_-]*$/.test(behaviorId)
  )
}

export function isBuiltinPetAction(actionId: string): actionId is PetPoseKey {
  return (PET_POSE_KEYS as readonly string[]).includes(actionId)
}

/** 内置动作优先、自定义动作按稳定字典序排列，保证同一 manifest 的行顺序不依赖对象插入顺序。 */
export function getPetAtlasActionKeys<T>(
  animations: Partial<Record<PetActionId, T>>
): PetActionId[] {
  const available = new Set(Object.keys(animations))
  const builtin = PET_POSE_KEYS.filter((actionId) => available.has(actionId))
  const custom = Object.keys(animations)
    .filter((actionId) => !(PET_POSE_KEYS as readonly string[]).includes(actionId))
    .sort()
  return [...builtin, ...custom]
}

export function getDefaultPetAtlasBehaviors(): PetAtlasBehaviorMap {
  return Object.fromEntries(
    Object.entries(DEFAULT_PET_ATLAS_BEHAVIORS).map(([id, behavior]) => [
      id,
      { ...behavior, actions: [...behavior.actions] }
    ])
  )
}

/** 返回副本，避免调用方改写默认行为或 manifest 内的数组。 */
export function getPetAtlasBehaviors(manifest: PetAtlasDocument): PetAtlasBehaviorMap {
  const merged = {
    ...getDefaultPetAtlasBehaviors(),
    ...(manifest.behaviors ?? {})
  }
  return Object.fromEntries(
    Object.entries(merged).map(([id, behavior]) => [
      id,
      { ...behavior, actions: [...behavior.actions] }
    ])
  )
}

/** 行为只暴露当前 atlas 中存在的动作；全失效时回到固定动作顺序中的首个动作。 */
export function getPetAtlasBehaviorActions(
  manifest: PetAtlasDocument,
  behaviorId: string
): PetActionId[] {
  const behavior = getPetAtlasBehaviors(manifest)[behaviorId]
  const configured = behavior?.actions ?? getDefaultPetAtlasBehaviors().idle.actions
  const available = configured.filter((actionId) => Boolean(manifest.animations[actionId]))
  if (available.length > 0) return [...available]
  const firstAvailable = getPetAtlasActionKeys(manifest.animations)[0]
  return firstAvailable ? [firstAvailable] : []
}

export function getPetAtlasAnimation(
  manifest: PetAtlasDocument,
  action: PetActionId = 'idle'
): PetAtlasAnimation {
  return manifest.animations[action] ?? manifest.animations.idle
}

export function normalizePetAtlasFrameIndex(
  animation: PetAtlasAnimation,
  frameIndex: number
): number {
  return Number.isInteger(frameIndex) && frameIndex >= 0 && frameIndex < animation.frames.length
    ? frameIndex
    : 0
}

export function getPetAtlasFrame(
  manifest: PetAtlasDocument,
  action: PetActionId = 'idle',
  frameIndex = 0
): PetAtlasFrame {
  const animation = getPetAtlasAnimation(manifest, action)
  return animation.frames[normalizePetAtlasFrameIndex(animation, frameIndex)]
}

function requireRecord(value: unknown, label: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`${label} must be an object.`)
  }
  return value as Record<string, unknown>
}

function requireInteger(value: unknown, label: string, min: number, max: number): number {
  if (!Number.isInteger(value) || (value as number) < min || (value as number) > max) {
    throw new Error(`${label} must be an integer between ${min} and ${max}.`)
  }
  return value as number
}

export function parsePetAtlasBounds(
  value: unknown,
  label: string,
  containerWidth: number,
  containerHeight: number
): PetAtlasBounds {
  const record = requireRecord(value, label)
  const x = requireInteger(record.x, `${label}.x`, 0, containerWidth)
  const y = requireInteger(record.y, `${label}.y`, 0, containerHeight)
  const width = requireInteger(record.width, `${label}.width`, 1, containerWidth)
  const height = requireInteger(record.height, `${label}.height`, 1, containerHeight)
  if (x + width > containerWidth || y + height > containerHeight) {
    throw new Error(`${label} exceeds its container bounds.`)
  }
  return { x, y, width, height }
}

export function parsePetAtlasFrame(
  value: unknown,
  label: string,
  atlasWidth: number,
  atlasHeight: number
): PetAtlasFrame {
  const record = requireRecord(value, label)
  const atlasBounds = parsePetAtlasBounds(record, label, atlasWidth, atlasHeight)
  const durationMs = requireInteger(record.durationMs, `${label}.durationMs`, 16, 60_000)
  const subjectBounds = parsePetAtlasBounds(
    record.subjectBounds,
    `${label}.subjectBounds`,
    atlasBounds.width,
    atlasBounds.height
  )
  return { ...atlasBounds, durationMs, subjectBounds }
}

export function parsePetAtlasBehaviorMap(value: unknown): PetAtlasBehaviorMap {
  const record = requireRecord(value, 'behaviors')
  const ids = Object.keys(record)
  if (ids.length > PET_ATLAS_MAX_BEHAVIORS) {
    throw new Error(`Pet atlas behaviors must contain at most ${PET_ATLAS_MAX_BEHAVIORS} entries.`)
  }

  const behaviors: PetAtlasBehaviorMap = {}
  for (const id of ids) {
    if (!isValidPetBehaviorId(id)) {
      throw new Error(
        `Invalid pet atlas behavior ID: ${id}. It must start with a letter and contain only letters, numbers, _ or - (max ${PET_ATLAS_MAX_BEHAVIOR_ID_LENGTH}).`
      )
    }
    const behavior = requireRecord(record[id], `behaviors.${id}`)
    const label = behavior.label
    if (
      label !== undefined &&
      (typeof label !== 'string' || label.trim().length > PET_ATLAS_MAX_BEHAVIOR_LABEL_LENGTH)
    ) {
      throw new Error(
        `behaviors.${id}.label must be a string up to ${PET_ATLAS_MAX_BEHAVIOR_LABEL_LENGTH} characters.`
      )
    }
    if (!Array.isArray(behavior.actions) || behavior.actions.length < 1) {
      throw new Error(`behaviors.${id}.actions must contain at least one action.`)
    }
    if (behavior.actions.length > PET_ATLAS_MAX_ACTIONS) {
      throw new Error(`behaviors.${id}.actions contains too many actions.`)
    }

    const actions: PetActionId[] = []
    for (const action of behavior.actions) {
      if (typeof action !== 'string' || !isValidPetActionId(action)) {
        throw new Error(`Invalid pet atlas action ID in behaviors.${id}: ${String(action)}`)
      }
      if (actions.includes(action)) {
        throw new Error(`behaviors.${id}.actions contains duplicate action: ${action}`)
      }
      actions.push(action)
    }

    const trimmedLabel = typeof label === 'string' ? label.trim() : ''
    behaviors[id] = {
      ...(trimmedLabel ? { label: trimmedLabel } : {}),
      actions
    }
  }
  return behaviors
}

function parseOptionalMetadata(root: Record<string, unknown>): Pick<
  PetAtlasDocument,
  | 'subject'
  | 'modelId'
  | 'createdAt'
  | 'updatedAt'
  | 'builtin'
  | 'assetVersion'
  | 'spriteNormalizationVersion'
> {
  const metadata: Pick<
    PetAtlasDocument,
    | 'subject'
    | 'modelId'
    | 'createdAt'
    | 'updatedAt'
    | 'builtin'
    | 'assetVersion'
    | 'spriteNormalizationVersion'
  > = {}

  for (const key of ['subject', 'modelId'] as const) {
    const value = root[key]
    if (value !== undefined) {
      if (typeof value !== 'string') {
        throw new Error(`Pet atlas manifest ${key} must be a string when provided.`)
      }
      metadata[key] = value
    }
  }
  for (const key of [
    'createdAt',
    'updatedAt',
    'assetVersion',
  'spriteNormalizationVersion'
  ] as const) {
    const value = root[key]
    if (value !== undefined) {
      if (typeof value !== 'number' || !Number.isFinite(value)) {
        throw new Error(`Pet atlas manifest ${key} must be a finite number when provided.`)
      }
      metadata[key] = value
    }
  }
  const builtin = root.builtin
  if (builtin !== undefined) {
    if (typeof builtin !== 'boolean') {
      throw new Error('Pet atlas manifest builtin must be a boolean when provided.')
    }
    metadata.builtin = builtin
  }
  return metadata
}

function rectanglesOverlap(left: PetAtlasBounds, right: PetAtlasBounds): boolean {
  return (
    left.x < right.x + right.width &&
    left.x + left.width > right.x &&
    left.y < right.y + right.height &&
    left.y + left.height > right.y
  )
}

/**
 * 所有渲染入口先经过同一个严格解析器，防止损坏坐标让 Canvas 读取 atlas 外部像素，
 * 同时把动作行顺序固定下来，避免不同 JSON 插入顺序产生不同资源布局。
 */
export function parsePetAtlasDocument(value: unknown): PetAtlasDocument {
  const root = requireRecord(value, 'Pet atlas manifest')
  if (root.atlasVersion !== PET_ATLAS_VERSION) {
    throw new Error(`Unsupported pet atlas version: ${String(root.atlasVersion)}`)
  }
  if (typeof root.name !== 'string' || !root.name.trim()) {
    throw new Error('Pet atlas manifest requires a non-empty name.')
  }

  const atlasRecord = requireRecord(root.atlas, 'atlas')
  const atlasImage = atlasRecord.image
  if (atlasImage !== PET_ATLAS_IMAGE_NAME && atlasImage !== PET_ATLAS_ALTERNATE_IMAGE_NAME) {
    throw new Error(
      `Pet atlas image must be ${PET_ATLAS_IMAGE_NAME} or ${PET_ATLAS_ALTERNATE_IMAGE_NAME}.`
    )
  }
  if (atlasRecord.anchor !== 'bottom-center') {
    throw new Error('Pet atlas anchor must be bottom-center.')
  }
  if (atlasRecord.layout !== 'action-rows') {
    throw new Error('Pet atlas layout must place each action on its own row.')
  }
  const atlasWidth = requireInteger(atlasRecord.width, 'atlas.width', 1, PET_ATLAS_MAX_TEXTURE_SIZE)
  const atlasHeight = requireInteger(
    atlasRecord.height,
    'atlas.height',
    1,
    PET_ATLAS_MAX_TEXTURE_SIZE
  )

  const animationRecord = requireRecord(root.animations, 'animations')
  const actionKeys = Object.keys(animationRecord)
  if (actionKeys.length > PET_ATLAS_MAX_ACTIONS) {
    throw new Error(`Pet atlas animations must contain at most ${PET_ATLAS_MAX_ACTIONS} actions.`)
  }
  for (const actionId of actionKeys) {
    if (!isValidPetActionId(actionId)) {
      throw new Error(
        `Pet atlas action ID must start with a letter and contain only letters, numbers, _ or - (max ${PET_ATLAS_MAX_ACTION_ID_LENGTH}): ${actionId}`
      )
    }
  }

  const animations: Partial<Record<PetActionId, PetAtlasAnimation>> = {}
  const occupiedFrames: Array<{ action: PetActionId; frame: PetAtlasFrame }> = []
  let previousActionRowBottom = -1

  for (const action of getPetAtlasActionKeys(animationRecord)) {
    const rawAnimation = animationRecord[action]
    if (rawAnimation === undefined) continue
    const animation = requireRecord(rawAnimation, `animations.${action}`)
    if (!Array.isArray(animation.frames)) {
      throw new Error(`animations.${action}.frames must be an array.`)
    }
    if (
      animation.frames.length < 1 ||
      animation.frames.length > PET_ATLAS_MAX_FRAMES_PER_ACTION
    ) {
      throw new Error(
        `animations.${action} must contain 1-${PET_ATLAS_MAX_FRAMES_PER_ACTION} frames.`
      )
    }
    if (typeof animation.loop !== 'boolean') {
      throw new Error(`animations.${action}.loop must be a boolean.`)
    }

    const label = animation.label
    if (
      label !== undefined &&
      (typeof label !== 'string' || label.trim().length > PET_ATLAS_MAX_ACTION_LABEL_LENGTH)
    ) {
      throw new Error(
        `animations.${action}.label must be a string up to ${PET_ATLAS_MAX_ACTION_LABEL_LENGTH} characters.`
      )
    }
    if (
      animation.description !== undefined &&
      (typeof animation.description !== 'string' ||
        animation.description.length > PET_ATLAS_MAX_ACTION_DESCRIPTION_LENGTH)
    ) {
      throw new Error(
        `animations.${action}.description must be a string up to ${PET_ATLAS_MAX_ACTION_DESCRIPTION_LENGTH} characters.`
      )
    }

    const frames = animation.frames.map((rawFrame, index) =>
      parsePetAtlasFrame(rawFrame, `animations.${action}.frames[${index}]`, atlasWidth, atlasHeight)
    )
    const rowBottom = frames[0].y + frames[0].height
    if (frames.some((frame) => frame.y + frame.height !== rowBottom)) {
      throw new Error(`animations.${action} frames must share one bottom-aligned atlas row.`)
    }
    const rowTop = Math.min(...frames.map((frame) => frame.y))
    if (rowTop <= previousActionRowBottom) {
      throw new Error(
        `Pet atlas action rows must follow the canonical action order; animations.${action} is out of order.`
      )
    }
    for (let index = 1; index < frames.length; index += 1) {
      if (frames[index].x <= frames[index - 1].x) {
        throw new Error(`animations.${action} frames must be ordered from left to right.`)
      }
    }

    previousActionRowBottom = rowBottom
    const trimmedLabel = typeof label === 'string' ? label.trim() : ''
    const description =
      typeof animation.description === 'string' ? animation.description.trim() : ''
    animations[action] = {
      loop: animation.loop,
      ...(trimmedLabel ? { label: trimmedLabel } : {}),
      ...(description ? { description } : {}),
      frames
    }
    for (const frame of frames) occupiedFrames.push({ action, frame })
  }

  if (!animations.idle) {
    throw new Error('Pet atlas manifest requires an idle animation.')
  }
  for (let left = 0; left < occupiedFrames.length; left += 1) {
    for (let right = left + 1; right < occupiedFrames.length; right += 1) {
      if (rectanglesOverlap(occupiedFrames[left].frame, occupiedFrames[right].frame)) {
        throw new Error(
          `Pet atlas frames overlap: ${occupiedFrames[left].action} and ${occupiedFrames[right].action}.`
        )
      }
    }
  }

  const behaviors =
    root.behaviors === undefined ? undefined : parsePetAtlasBehaviorMap(root.behaviors)
  const metadata = parseOptionalMetadata(root)

  return {
    name: root.name.trim(),
    ...metadata,
    atlasVersion: PET_ATLAS_VERSION,
    atlas: {
      image: atlasImage,
      width: atlasWidth,
      height: atlasHeight,
      anchor: 'bottom-center',
      layout: 'action-rows'
    },
    animations: animations as PetAtlasAnimationMap,
    ...(behaviors ? { behaviors } : {})
  }
}
