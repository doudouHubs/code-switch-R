import { Call } from '../../wails-runtime-compat'
import {
  PET_ATLAS_IMAGE_NAME,
  parsePetAtlasDocument,
  type PetAtlasBehaviorMap,
  type PetAtlasDocument,
  type PetAtlasFrame,
  type PetActionId
} from './petAtlas'
import type { PetAtlasAsset, PetSkinRecord } from './petTypes'

const PET_IMAGE_SERVICE = 'codeswitch/services.PetImageAPIService'
const PET_MEDIA_SERVICE = 'codeswitch/services.PetMediaAPIService'
const PET_STUDIO_SERVICE = 'codeswitch/services.PetStudioAPIService'
const MAX_REFERENCE_IMAGES = 3

/** Studio 动作 ID 直接复用 atlas 契约，避免页面和运行时各维护一份动作枚举。 */
export type PetStudioActionId = PetActionId

export interface PetStudioImageModel {
  platform?: string
  providerId: string
  modelId: string
}

export interface PetStudioReferenceImage {
  /** 可以是 bare base64，也可以是 data URL；发给 Go 前会统一成 bare base64。 */
  data: string
  mediaType?: string
  preview?: string
}

export interface PetStudioGeneratedImage {
  data: string
  mediaType: string
}

export interface PetStudioFrameCrop {
  x: number
  y: number
  width: number
  height: number
}

export interface PetStudioFrameInput {
  /** 真实图片数据。没有数据时 API 必须失败，不能用坐标或占位帧蒙混过关。 */
  data: string
  durationMs?: number
  mediaType?: string
  crop?: PetStudioFrameCrop
}

export interface PetStudioActionInput {
  id: string
  frames: PetStudioFrameInput[]
  loop?: boolean
  label?: string
  description?: string
}

export interface PetStudioManifestMetadata {
  subject?: string
  modelId?: string
  createdAt?: number
  updatedAt?: number
  builtin?: boolean
  assetVersion?: number
  spriteNormalizationVersion?: number
}

export interface PetStudioPackOptions extends PetStudioManifestMetadata {
  metadata?: PetStudioManifestMetadata
  behaviors?: PetAtlasBehaviorMap
  /** 新契约可以显式提供 action，旧页面继续使用 Record<string, FrameInput[]>。 */
  actions?: PetStudioActionInput[]
  padding?: number
  maxWidth?: number
  maxHeight?: number
}

export interface PetStudioPackResult {
  data: string
  mediaType: string
  manifest: PetAtlasDocument
  atlas: Record<string, unknown>
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

/** 后端记录中的路径字段永远不进入这个前端类型。 */
export type PetStudioSkinRecord = Omit<PetSkinRecord, 'path' | 'atlasPath'>

export interface PetStudioSaveRequest {
  skinId: string
  name: string
  subject?: string
  modelId?: string
  /** 新字段；必须提交 bare base64。 */
  atlas?: string
  /** 旧字段兼容别名，发送时会和 atlas 保持一致。 */
  atlasBase64?: string
  manifestJson: unknown
  bind?: boolean
}

export interface PetStudioAtlasReadResult {
  atlas: PetAtlasAsset
  skin?: PetStudioSkinRecord
}

export type PetStudioAtlasSource = 'default' | string | { skinId: string }

function call(service: string, method: string, ...args: unknown[]): Promise<unknown> {
  return Promise.resolve(Call.ByName(`${service}.${method}`, ...args))
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {}
}

function isByteArray(value: unknown): value is number[] {
  return Array.isArray(value) && value.every((item) => Number.isInteger(item) && item >= 0 && item <= 255)
}

function bytesToBase64(value: number[]): string {
  let binary = ''
  for (const item of value) binary += String.fromCharCode(item)
  return btoa(binary)
}

function parseDataUrl(value: string): { data: string; mediaType?: string } | null {
  const match = value.match(/^data:([^;,]+);base64,(.*)$/is)
  if (!match) return null
  return { data: match[2].trim(), mediaType: match[1].trim().toLowerCase() }
}

function normalizeMediaType(value: unknown, fallback = 'image/png'): string {
  if (typeof value !== 'string') return fallback
  const normalized = value.trim().toLowerCase()
  return normalized.startsWith('image/') ? normalized : fallback
}

function isBareBase64(value: string): boolean {
  return value.length > 0 && !/[\s,]/.test(value) && /^[A-Za-z0-9+/]+={0,2}$/.test(value)
}

/** Wails 对 []byte 的编码在不同 runtime 版本中可能是 base64 字符串或数字数组，统一在边界兼容。 */
export function readBase64Bytes(value: unknown): string {
  if (typeof value === 'string') {
    const dataUrl = parseDataUrl(value.trim())
    return (dataUrl?.data ?? value).trim()
  }
  if (isByteArray(value)) return bytesToBase64(value)
  if (typeof Uint8Array !== 'undefined' && value instanceof Uint8Array) {
    return bytesToBase64(Array.from(value))
  }
  if (value instanceof ArrayBuffer) return bytesToBase64(Array.from(new Uint8Array(value)))
  const record = asRecord(value)
  for (const key of ['data', 'base64', 'bytes', 'value']) {
    if (record[key] !== undefined) {
      const nested = readBase64Bytes(record[key])
      if (nested) return nested
    }
  }
  return ''
}

function readMediaType(value: unknown, fallback = 'image/png'): string {
  if (typeof value === 'string') return parseDataUrl(value)?.mediaType ?? fallback
  const record = asRecord(value)
  return normalizeMediaType(record.mediaType ?? record.mimeType ?? record.contentType, fallback)
}

function normalizeImageInput(value: unknown, mediaType?: string): { data: string; mediaType: string } {
  const record = asRecord(value)
  const raw = record.data ?? record.base64 ?? record.bytes ?? value
  const data = readBase64Bytes(raw)
  if (!data || !isBareBase64(data)) throw new Error('图片数据必须是有效的 bare base64 或 data URL。')
  return { data, mediaType: readMediaType(raw, normalizeMediaType(mediaType)) }
}

export function toDataUrl(data: string, mediaType = 'image/png'): string {
  const parsed = parseDataUrl(data.trim())
  return `data:${parsed?.mediaType ?? normalizeMediaType(mediaType)};base64,${parsed?.data ?? data.trim()}`
}

function decodeBase64Utf8(value: string): string {
  const binary = atob(value)
  const bytes = Uint8Array.from(binary, (char) => char.charCodeAt(0))
  return new TextDecoder().decode(bytes)
}

function parseManifestValue(value: unknown): unknown {
  if (value && typeof value === 'object' && !Array.isArray(value)) return value
  if (typeof value !== 'string' && !isByteArray(value)) return null
  const text = typeof value === 'string' ? value.trim() : decodeBase64Utf8(bytesToBase64(value))
  if (text.startsWith('{')) {
    try {
      return JSON.parse(text) as unknown
    } catch {
      return null
    }
  }
  const encoded = readBase64Bytes(text)
  if (!encoded) return null
  try {
    return JSON.parse(decodeBase64Utf8(encoded)) as unknown
  } catch {
    return null
  }
}

function findFirstValue(root: Record<string, unknown>, keys: string[]): unknown {
  for (const key of keys) {
    if (root[key] !== undefined && root[key] !== null) return root[key]
  }
  return undefined
}

function findAtlasPayload(root: Record<string, unknown>): unknown {
  const nested = [root.atlas, root.asset, root.atlasAsset, root.payload, root.skin]
    .map(asRecord)
    .find((value) => Object.keys(value).length > 0)
  return findFirstValue(root, ['src', 'data', 'atlasBase64', 'atlasData', 'atlasBytes', 'bytes', 'image']) ??
    (nested ? findFirstValue(nested, ['src', 'data', 'atlasBase64', 'atlasData', 'atlasBytes', 'bytes', 'image']) : undefined) ??
    (typeof root.atlas === 'string' || Array.isArray(root.atlas) ? root.atlas : undefined)
}

function stripSkinPaths(value: unknown): PetStudioSkinRecord | undefined {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
  const record = { ...asRecord(value) } as Record<string, unknown>
  // 即使后端未来误带路径，也在 Wails 返回边界再次剥离，避免把文件系统信息传给 Vue。
  delete record.path
  delete record.atlasPath
  return record as PetStudioSkinRecord
}

function parseReadResult(value: unknown): PetStudioAtlasReadResult {
  const root = asRecord(value)
  const nestedAtlas = asRecord(root.atlas)
  const nestedSkin = asRecord(root.skin)
  const payload = findAtlasPayload(root)
  if (payload === undefined) throw new Error('Studio 读取结果缺少 atlas 图片数据。')
  const image = normalizeImageInput(payload)
  const manifestValue = findFirstValue(root, ['manifest', 'manifestJson', 'petJson', 'metadata']) ??
    findFirstValue(nestedAtlas, ['manifest', 'manifestJson', 'petJson', 'metadata']) ??
    findFirstValue(nestedSkin, ['manifest', 'manifestJson', 'petJson'])
  const manifest = parseManifestValue(manifestValue)
  if (!manifest) throw new Error('Studio 读取结果缺少有效 manifest JSON。')
  const document = parsePetAtlasDocument(manifest)
  const skin = stripSkinPaths(root.skin ?? root.record ?? root.skinRecord)
  return {
    atlas: { src: toDataUrl(image.data, image.mediaType), manifest: document },
    ...(skin ? { skin } : {})
  }
}

/**
 * 读取方法只接受受控的 skinId，默认资源用空 ID 交给后端决定；前端不拼接、不读取任何本地路径。
 * ReadSkin 是 SaveSkin/ListSkins 所属 Studio service 的读取入口，返回 shape 同时兼容裸 asset 和包裹结果。
 */
export async function readPetStudioAtlas(
  petId: string,
  source: PetStudioAtlasSource = 'default'
): Promise<PetStudioAtlasReadResult> {
  const skinId = typeof source === 'string' ? (source === 'default' ? '' : source.trim()) : source.skinId.trim()
  if (source !== 'default' && !skinId) throw new Error('读取 Studio skin 时必须提供 skinId。')
  return parseReadResult(await call(PET_STUDIO_SERVICE, 'ReadSkin', petId, skinId))
}

export const loadPetStudioAtlas = readPetStudioAtlas

export async function generatePetStudioImage(
  petId: string,
  model: PetStudioImageModel,
  prompt: string,
  references: readonly PetStudioReferenceImage[] | { referenceImages?: readonly PetStudioReferenceImage[] } = []
): Promise<PetStudioGeneratedImage> {
  const referenceImages: readonly PetStudioReferenceImage[] = Array.isArray(references)
    ? references
    : ('referenceImages' in references ? references.referenceImages ?? [] : [])
  if (referenceImages.length > MAX_REFERENCE_IMAGES) {
    throw new Error(`最多只能提供 ${MAX_REFERENCE_IMAGES} 张参考图。`)
  }
  const normalizedReferences = referenceImages.map((reference) => normalizeImageInput(reference, reference.mediaType))
  const request: Record<string, unknown> = {
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
  }
  if (normalizedReferences.length > 0) {
    const payload = normalizedReferences.map((reference) => ({
      data: reference.data,
      mediaType: reference.mediaType,
      pose: 'idle',
      frameIndex: 0
    }))
    // referenceImages 是新协议；referenceImage 让尚未升级的 Go bridge 仍能消费第一张。
    request.referenceImages = payload
    request.referenceImage = payload[0]
  }
  const result = asRecord(await call(PET_IMAGE_SERVICE, 'GenerateImage', request))
  const images = Array.isArray(result.images) ? result.images : []
  const first = images[0]
  const data = readBase64Bytes(first)
  if (!data || !isBareBase64(data)) throw new Error('生成接口没有返回有效图片。')
  return { data, mediaType: readMediaType(first, normalizeMediaType(result.mediaType)) }
}

export async function processPetStudioFrame(
  data: string,
  options: { chromaKey: boolean; keyColor: string; targetHeight: number }
): Promise<string> {
  let current = normalizeImageInput(data).data
  if (options.chromaKey) {
    const result = asRecord(
      await call(PET_MEDIA_SERVICE, 'ApplyChromaKey', { data: current, keyColor: options.keyColor })
    )
    current = normalizeImageInput(result.data, String(result.mediaType ?? 'image/png')).data
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
  const output = normalizeImageInput(normalized.data, String(normalized.mediaType ?? 'image/png')).data
  if (!output) throw new Error('normalize 没有返回有效图片。')
  return output
}

function validateCrop(crop: PetStudioFrameCrop): PetStudioFrameCrop {
  const values = [crop.x, crop.y, crop.width, crop.height]
  if (!values.every((value) => Number.isInteger(value)) || crop.x < 0 || crop.y < 0 || crop.width < 1 || crop.height < 1) {
    throw new Error('atlas frame crop 必须是有效的正整数矩形。')
  }
  return { ...crop }
}

async function cropImageToPng(data: string, mediaType: string, crop: PetStudioFrameCrop): Promise<string> {
  if (typeof document === 'undefined' || typeof Image === 'undefined') {
    throw new Error('当前运行时无法读取 atlas frame 像素；请提供已经裁好的真实帧数据。')
  }
  const image = new Image()
  const imageUrl = toDataUrl(data, mediaType)
  const loaded = await new Promise<HTMLImageElement>((resolve, reject) => {
    image.onload = () => resolve(image)
    image.onerror = () => reject(new Error('无法解码 atlas frame 图片。'))
    image.src = imageUrl
  })
  const bounds = validateCrop(crop)
  if (bounds.x + bounds.width > loaded.naturalWidth || bounds.y + bounds.height > loaded.naturalHeight) {
    throw new Error('atlas frame crop 越过图片边界。')
  }
  const canvas = document.createElement('canvas')
  canvas.width = bounds.width
  canvas.height = bounds.height
  const context = canvas.getContext('2d')
  if (!context) throw new Error('无法创建 atlas frame 像素读取上下文。')
  context.drawImage(loaded, bounds.x, bounds.y, bounds.width, bounds.height, 0, 0, bounds.width, bounds.height)
  const result = canvas.toDataURL('image/png')
  return normalizeImageInput(result).data
}

async function materializeFrame(frame: PetStudioFrameInput): Promise<{ data: string; durationMs: number }> {
  const image = normalizeImageInput(frame.data, frame.mediaType)
  const data = frame.crop
    ? await cropImageToPng(image.data, image.mediaType, frame.crop)
    : image.data
  const durationMs = Number.isInteger(frame.durationMs)
    ? Math.min(60_000, Math.max(16, frame.durationMs as number))
    : 500
  return { data, durationMs }
}

function resolveMetadata(options: PetStudioPackOptions): PetStudioManifestMetadata {
  const metadata: PetStudioManifestMetadata = { ...(options.metadata ?? {}) }
  if (options.subject !== undefined) metadata.subject = options.subject
  if (options.modelId !== undefined) metadata.modelId = options.modelId
  if (options.createdAt !== undefined) metadata.createdAt = options.createdAt
  if (options.updatedAt !== undefined) metadata.updatedAt = options.updatedAt
  if (options.builtin !== undefined) metadata.builtin = options.builtin
  if (options.assetVersion !== undefined) metadata.assetVersion = options.assetVersion
  if (options.spriteNormalizationVersion !== undefined) metadata.spriteNormalizationVersion = options.spriteNormalizationVersion
  return metadata
}

function actionInputsFromFrames(
  frames: Record<string, PetStudioFrameInput[] | PetStudioActionInput>,
  options: PetStudioPackOptions
): PetStudioActionInput[] {
  if (options.actions) return options.actions.map((action) => ({ ...action, frames: [...action.frames] }))
  return Object.entries(frames).map(([id, value]) => {
    if (!Array.isArray(value)) return { ...value, id, frames: [...value.frames] }
    return { id, frames: [...(value as PetStudioFrameInput[])], loop: true }
  })
}

/**
 * PackAtlas 只接收真实帧像素。atlas 引用帧可以携带 crop，由这里在浏览器内裁成真实 PNG；
 * 没有数据或无法裁剪时直接失败，避免生成一个“看起来保存成功但永远播放不了”的假 atlas。
 */
export async function packPetStudioAtlas(
  name: string,
  frames: Record<string, PetStudioFrameInput[] | PetStudioActionInput>,
  options: PetStudioPackOptions = {}
): Promise<PetStudioPackResult> {
  const actions = actionInputsFromFrames(frames, options)
    .filter((action) => action.frames.length > 0)
    .map(async (action) => {
      const materialized = await Promise.all(action.frames.map(materializeFrame))
      return {
        id: action.id,
        frames: materialized.map((frame) => frame.data),
        durationsMs: materialized.map((frame) => frame.durationMs),
        loop: action.loop !== false,
        ...(action.label?.trim() ? { label: action.label.trim() } : {}),
        ...(action.description?.trim() ? { description: action.description.trim() } : {})
      }
    })
  const packedActions = await Promise.all(actions)
  if (!packedActions.some((action) => action.id === 'idle')) throw new Error('至少需要一个 idle 帧才能打包。')
  const metadata = resolveMetadata(options)
  const request: Record<string, unknown> = {
    name,
    actions: packedActions,
    ...metadata,
    ...(options.behaviors ? { behaviors: options.behaviors } : {}),
    metadata,
    ...(options.padding !== undefined ? { padding: options.padding } : {}),
    ...(options.maxWidth !== undefined ? { maxWidth: options.maxWidth } : {}),
    ...(options.maxHeight !== undefined ? { maxHeight: options.maxHeight } : {})
  }
  const result = asRecord(await call(PET_MEDIA_SERVICE, 'PackAtlas', request))
  const data = readBase64Bytes(result.data)
  if (!data || !isBareBase64(data) || result.manifest === undefined) {
    throw new Error('atlas 打包结果不完整。')
  }
  const rawManifest = asRecord(result.manifest)
  const mergedManifest = {
    ...rawManifest,
    name: typeof rawManifest.name === 'string' && rawManifest.name.trim() ? rawManifest.name : name,
    ...metadata,
    ...(options.behaviors ? { behaviors: options.behaviors } : {})
  }
  const manifest = parsePetAtlasDocument(mergedManifest)
  return {
    data,
    mediaType: normalizeMediaType(result.mediaType),
    manifest,
    atlas: asRecord(result.atlas)
  }
}

export async function splitPetStudioActionSheet(
  data: string,
  frameCount: number
): Promise<PetStudioActionSheetResult> {
  const result = asRecord(
    await call(PET_MEDIA_SERVICE, 'SplitActionSheet', { data: normalizeImageInput(data).data, frameCount })
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
    .filter((frame) => frame.data && isBareBase64(frame.data) && frame.width > 0 && frame.height > 0)
  const normalizedLayout = {
    columns: Number(layout.columns ?? 0),
    rows: Number(layout.rows ?? 0),
    frameCount: Number(layout.frameCount ?? 0)
  }
  if (!normalizedLayout.columns || !normalizedLayout.rows || normalizedLayout.frameCount !== frames.length) {
    throw new Error('action sheet 拆帧结果不完整。')
  }
  return { layout: normalizedLayout, frames }
}

function normalizeManifestForSave(value: unknown): unknown {
  if (value && typeof value === 'object' && !Array.isArray(value)) return value
  if (typeof value === 'string') {
    try {
      return JSON.parse(value) as unknown
    } catch {
      throw new Error('manifestJson 必须是有效 JSON。')
    }
  }
  throw new Error('manifestJson 必须是 JSON object。')
}

export async function savePetStudioSkin(petId: string, request: PetStudioSaveRequest): Promise<unknown> {
  const atlas = request.atlas?.trim() ?? ''
  const legacyAtlas = request.atlasBase64?.trim() ?? ''
  if (atlas && legacyAtlas && readBase64Bytes(atlas) !== readBase64Bytes(legacyAtlas)) {
    throw new Error('atlas 与 atlasBase64 不能同时提交不同内容。')
  }
  const bareAtlas = readBase64Bytes(atlas || legacyAtlas)
  if (!bareAtlas || !isBareBase64(bareAtlas)) throw new Error('保存请求缺少有效 atlas bare base64。')
  const manifestJson = normalizeManifestForSave(request.manifestJson)
  return call(PET_STUDIO_SERVICE, 'SaveSkin', petId, {
    skinId: request.skinId,
    name: request.name,
    subject: request.subject ?? '',
    modelId: request.modelId ?? '',
    atlas: bareAtlas,
    // 同时发旧字段，旧 bridge 可以继续解码；两者严格保持相同内容。
    atlasBase64: bareAtlas,
    manifestJson,
    bind: request.bind === true
  })
}

export async function listPetStudioSkins(petId: string): Promise<PetStudioSkinRecord[]> {
  const value = await call(PET_STUDIO_SERVICE, 'ListSkins', petId)
  return (Array.isArray(value) ? value : [])
    .map(stripSkinPaths)
    .filter((record): record is PetStudioSkinRecord => Boolean(record))
}

export function deletePetStudioSkin(petId: string, skinId: string): Promise<unknown> {
  return call(PET_STUDIO_SERVICE, 'DeleteSkin', petId, skinId)
}

export async function getPetStudioRoot(): Promise<string> {
  const value = await call(PET_STUDIO_SERVICE, 'GetRoot')
  if (typeof value !== 'string' || !value.trim()) throw new Error('Pet Studio 资源目录不可用。')
  return value.trim()
}

export function openPetStudioRoot(): Promise<unknown> {
  return call(PET_STUDIO_SERVICE, 'OpenRoot')
}

export { PET_ATLAS_IMAGE_NAME }
export type { PetAtlasAsset, PetAtlasFrame }
