import { Call } from '../../wails-runtime-compat'

const PET_IMAGE_SERVICE = 'codeswitch/services.PetImageAPIService'
const PET_MEDIA_SERVICE = 'codeswitch/services.PetMediaAPIService'
const PET_STUDIO_SERVICE = 'codeswitch/services.PetStudioAPIService'

export type PetStudioActionId =
  | 'idle'
  | 'walk'
  | 'sleep'
  | 'beg'
  | 'eat'
  | 'munch'
  | 'bathe'
  | 'soak'
  | 'swim'
  | 'zen'
  | 'play'
  | 'held'
  | 'report-time'

export interface PetStudioImageModel {
  platform?: string
  providerId: string
  modelId: string
}

export interface PetStudioGeneratedImage {
  data: string
  mediaType: string
}

export interface PetStudioFrameInput {
  data: string
  durationMs?: number
}

export interface PetStudioPackResult {
  data?: string
  mediaType?: string
  manifest?: Record<string, unknown>
  atlas?: Record<string, unknown>
}

export interface PetStudioActionSheetFrame {
  data: string
  width: number
  height: number
}

export interface PetStudioActionSheetResult {
  layout: { columns: number; rows: number; frameCount: number }
  frames: PetStudioActionSheetFrame[]
}

export interface PetStudioSaveRequest {
  skinId: string
  name: string
  subject: string
  modelId: string
  atlasBase64: string
  manifestJson: unknown
  bind: boolean
}

function call(service: string, method: string, ...args: unknown[]): Promise<unknown> {
  return Promise.resolve(Call.ByName(`${service}.${method}`, ...args))
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {}
}

/** Wails 对 []byte 的编码在不同 runtime 版本中可能是 base64 字符串或数字数组，统一在边界兼容。 */
export function readBase64Bytes(value: unknown): string {
  if (typeof value === 'string') return value.replace(/^data:[^;,]+;base64,/, '').trim()
  if (Array.isArray(value) && value.every((item) => Number.isInteger(item))) {
    let binary = ''
    for (const item of value) binary += String.fromCharCode(Number(item) & 255)
    return btoa(binary)
  }
  return ''
}

export function toDataUrl(data: string, mediaType = 'image/png'): string {
  return `data:${mediaType};base64,${data}`
}

export async function generatePetStudioImage(
  petId: string,
  model: PetStudioImageModel,
  prompt: string
): Promise<PetStudioGeneratedImage> {
  const result = asRecord(
    await call(PET_IMAGE_SERVICE, 'GenerateImage', {
      petId,
      requestId: `pet-studio-${Date.now()}`,
      provider: {
        platform: model.platform ?? '',
        providerId: model.providerId,
        model: model.modelId,
        capability: 'image',
        autoFallback: false
      },
      prompt,
      size: '1024x1024',
      count: 1
    })
  )
  const images = Array.isArray(result.images) ? result.images : []
  const data = readBase64Bytes(images[0])
  if (!data) throw new Error('生成接口没有返回有效图片。')
  return { data, mediaType: typeof result.mediaType === 'string' ? result.mediaType : 'image/png' }
}

export async function processPetStudioFrame(
  data: string,
  options: { chromaKey: boolean; keyColor: string; targetHeight: number }
): Promise<string> {
  let current = data
  if (options.chromaKey) {
    const result = asRecord(
      await call(PET_MEDIA_SERVICE, 'ApplyChromaKey', {
        data: current,
        keyColor: options.keyColor
      })
    )
    current = readBase64Bytes(result.data)
    if (!current) throw new Error('chroma key 没有返回有效图片。')
  }
  const normalized = asRecord(
    await call(PET_MEDIA_SERVICE, 'NormalizeSprite', {
      data: current,
      targetHeight: options.targetHeight,
      alphaThreshold: 8,
      paddingX: 8,
      paddingY: 8
    })
  )
  const output = readBase64Bytes(normalized.data)
  if (!output) throw new Error('normalize 没有返回有效图片。')
  return output
}

export async function packPetStudioAtlas(
  name: string,
  frames: Record<string, PetStudioFrameInput[]>
): Promise<PetStudioPackResult> {
  const actions = Object.entries(frames)
    .filter(([, values]) => values.length > 0)
    .map(([id, values]) => ({
      id,
      frames: values.map((frame) => frame.data),
      durationsMs: values.map((frame) => frame.durationMs ?? 500),
      loop: id !== 'idle'
    }))
  if (!actions.some((action) => action.id === 'idle')) throw new Error('至少需要一个 idle 帧才能打包。')
  const result = asRecord(await call(PET_MEDIA_SERVICE, 'PackAtlas', { name, actions }))
  if (!readBase64Bytes(result.data) || !result.manifest) throw new Error('atlas 打包结果不完整。')
  return {
    data: readBase64Bytes(result.data),
    mediaType: typeof result.mediaType === 'string' ? result.mediaType : 'image/png',
    manifest: result.manifest as Record<string, unknown>,
    atlas: asRecord(result.atlas)
  }
}

export async function splitPetStudioActionSheet(
  data: string,
  frameCount: number
): Promise<PetStudioActionSheetResult> {
  const result = asRecord(
    await call(PET_MEDIA_SERVICE, 'SplitActionSheet', { data, frameCount })
  )
  const layout = asRecord(result.layout)
  const rawFrames = Array.isArray(result.frames) ? result.frames : []
  const frames = rawFrames
    .map((value) => {
      const item = asRecord(value)
      return {
        data: readBase64Bytes(item.data),
        width: Number(item.width ?? 0),
        height: Number(item.height ?? 0)
      }
    })
    .filter((frame) => frame.data && frame.width > 0 && frame.height > 0)
  const normalizedLayout = {
    columns: Number(layout.columns ?? 0),
    rows: Number(layout.rows ?? 0),
    frameCount: Number(layout.frameCount ?? 0)
  }
  if (
    !normalizedLayout.columns ||
    !normalizedLayout.rows ||
    normalizedLayout.frameCount !== frames.length
  ) {
    throw new Error('action sheet 拆帧结果不完整。')
  }
  return { layout: normalizedLayout, frames }
}

export function savePetStudioSkin(petId: string, request: PetStudioSaveRequest): Promise<unknown> {
  return call(PET_STUDIO_SERVICE, 'SaveSkin', petId, request)
}

export function listPetStudioSkins(petId: string): Promise<unknown> {
  return call(PET_STUDIO_SERVICE, 'ListSkins', petId)
}

export function deletePetStudioSkin(petId: string, skinId: string): Promise<unknown> {
  return call(PET_STUDIO_SERVICE, 'DeleteSkin', petId, skinId)
}
