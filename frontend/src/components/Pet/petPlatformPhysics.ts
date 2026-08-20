import type {
  PetWindowPlatform,
  PetWindowPlatformRect,
  PetWindowPlatformSnapshot
} from './petApi'

export interface PetLocalPlatform {
  id: string
  left: number
  top: number
  right: number
  bottom: number
  zOrder: number
}

export interface PetPlatformBounds {
  minX: number
  maxX: number
  // 一个窗口被高层窗口遮住后，可站立位置可能被切成多个区间；只返回
  // min/max 会让漫游穿过遮挡区，所以保留完整的宠物左边缘区间。
  segments: PetPlatformSegment[]
}

export interface PetPlatformSegment {
  minX: number
  maxX: number
}

export interface PetPlatformSupport {
  kind: 'ground' | 'platform'
  id?: string
  offsetX?: number
}

const PLATFORM_EDGE_EPSILON = 2

function finite(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

function normalizeRect(value: unknown): PetWindowPlatformRect | null {
  if (!value || typeof value !== 'object') return null
  const source = value as Record<string, unknown>
  const left = source.left
  const top = source.top
  const right = source.right
  const bottom = source.bottom
  if (!finite(left) || !finite(top) || !finite(right) || !finite(bottom) || right <= left || bottom <= top) return null
  return { left, top, right, bottom }
}

function normalizePlatform(value: unknown): PetWindowPlatform | null {
  if (!value || typeof value !== 'object') return null
  const source = value as Record<string, unknown>
  const id = typeof source.id === 'string' ? source.id.trim() : ''
  const rect = normalizeRect(source.rect)
  const rawZOrder = source.zOrder
  const zOrder = finite(rawZOrder) ? rawZOrder : 0
  if (!id || !rect) return null
  return { id, rect, zOrder }
}

export function normalizePetPlatformSnapshot(value: unknown): PetWindowPlatformSnapshot {
  if (!value || typeof value !== 'object') {
    return {
      available: false,
      overlay: { left: 0, top: 0, right: 0, bottom: 0 },
      platforms: [],
      occluders: [],
      movingWindowId: ''
    }
  }
  const source = value as Record<string, unknown>
  const overlay = normalizeRect(source.overlay)
  const platforms = Array.isArray(source.platforms)
    ? source.platforms.map(normalizePlatform).filter((item): item is PetWindowPlatform => item !== null)
    : []
  const occluders = Array.isArray(source.occluders)
    ? source.occluders.map(normalizePlatform).filter((item): item is PetWindowPlatform => item !== null)
    : []
  const movingWindowId = typeof source.movingWindowId === 'string' ? source.movingWindowId.trim() : ''
  if (source.available !== true || !overlay) {
    return {
      available: false,
      overlay: overlay ?? { left: 0, top: 0, right: 0, bottom: 0 },
      platforms: [],
      occluders: [],
      movingWindowId: ''
    }
  }
  return { available: true, overlay, platforms, occluders, movingWindowId }
}

function toLocalPlatformList(
  platforms: readonly PetWindowPlatform[],
  snapshot: PetWindowPlatformSnapshot,
  viewportWidth: number,
  viewportHeight: number
): PetLocalPlatform[] {
  const overlayWidth = Math.max(1, snapshot.overlay.right - snapshot.overlay.left)
  const overlayHeight = Math.max(1, snapshot.overlay.bottom - snapshot.overlay.top)
  const scaleX = Math.max(1, viewportWidth) / overlayWidth
  const scaleY = Math.max(1, viewportHeight) / overlayHeight
  return platforms.map((platform) => ({
    id: platform.id,
    left: (platform.rect.left - snapshot.overlay.left) * scaleX,
    top: (platform.rect.top - snapshot.overlay.top) * scaleY,
    right: (platform.rect.right - snapshot.overlay.left) * scaleX,
    bottom: (platform.rect.bottom - snapshot.overlay.top) * scaleY,
    zOrder: platform.zOrder
  }))
}

export function toLocalPetPlatforms(
  snapshot: PetWindowPlatformSnapshot,
  viewportWidth: number,
  viewportHeight: number
): PetLocalPlatform[] {
  if (!snapshot.available) return []
  return toLocalPlatformList(snapshot.platforms, snapshot, viewportWidth, viewportHeight)
}

export function toLocalPetOccluders(
  snapshot: PetWindowPlatformSnapshot,
  viewportWidth: number,
  viewportHeight: number
): PetLocalPlatform[] {
  if (!snapshot.available) return []
  return toLocalPlatformList(snapshot.occluders, snapshot, viewportWidth, viewportHeight)
}

export function getPetGroundY(viewportHeight: number, groundPadding: number): number {
  return Math.max(0, viewportHeight - groundPadding)
}

export function getPetPlatformBounds(
  platform: PetLocalPlatform,
  petWidth: number,
  viewportWidth: number,
  edgeMargin = 0,
  occluders: readonly PetLocalPlatform[] = []
): PetPlatformBounds {
  const segments = getPetPlatformPlacementSegments(platform, petWidth, viewportWidth, occluders)
    .map((segment) => ({
      minX: segment.minX + edgeMargin,
      maxX: segment.maxX - edgeMargin
    }))
    .filter((segment) => segment.maxX >= segment.minX)
  if (segments.length === 0) return { minX: 0, maxX: 0, segments: [] }
  return {
    minX: segments[0].minX,
    maxX: segments[segments.length - 1].maxX,
    segments
  }
}

function getPetPlatformPlacementSegments(
  platform: PetLocalPlatform,
  petWidth: number,
  viewportWidth: number,
  occluders: readonly PetLocalPlatform[]
): PetPlatformSegment[] {
  const maxPetX = Math.max(0, viewportWidth - petWidth)
  const platformMinX = Math.max(0, platform.left)
  const platformMaxX = Math.min(viewportWidth, platform.right)
  let segments: PetPlatformSegment[] = [{
    minX: Math.max(0, platformMinX),
    maxX: Math.min(maxPetX, platformMaxX - petWidth)
  }]
  if (segments[0].maxX < segments[0].minX) return []

  // Z 序数字越小表示窗口越靠前。只有真正压过当前平台顶部的高层窗口
  // 才会切掉落脚区，窗口只是从旁边经过或底边刚好贴住时不能误伤平台。
  const blockingRanges = occluders
    .filter((occluder) => (
      occluder.id !== platform.id &&
      occluder.zOrder < platform.zOrder &&
      occluder.top <= platform.top + PLATFORM_EDGE_EPSILON &&
      occluder.bottom > platform.top + PLATFORM_EDGE_EPSILON &&
      occluder.right > platform.left &&
      occluder.left < platform.right
    ))
    .map((occluder) => ({
      minX: Math.max(0, occluder.left - petWidth),
      maxX: Math.min(viewportWidth, occluder.right)
    }))
    .filter((range) => range.maxX > range.minX)
    .sort((left, right) => left.minX - right.minX)

  for (const range of blockingRanges) {
    const next: PetPlatformSegment[] = []
    for (const segment of segments) {
      if (range.maxX <= segment.minX || range.minX >= segment.maxX) {
        next.push(segment)
        continue
      }
      if (range.minX > segment.minX) {
        next.push({ minX: segment.minX, maxX: Math.min(segment.maxX, range.minX) })
      }
      if (range.maxX < segment.maxX) {
        next.push({ minX: Math.max(segment.minX, range.maxX), maxX: segment.maxX })
      }
    }
    segments = next.filter((segment) => segment.maxX >= segment.minX)
    if (segments.length === 0) break
  }
  return segments
}

export function isPetPlatformPlacementValid(
  platform: PetLocalPlatform,
  petX: number,
  petWidth: number,
  viewportWidth: number,
  occluders: readonly PetLocalPlatform[] = []
): boolean {
  return getPetPlatformPlacementSegments(platform, petWidth, viewportWidth, occluders)
    .some((segment) => petX >= segment.minX - PLATFORM_EDGE_EPSILON && petX <= segment.maxX + PLATFORM_EDGE_EPSILON)
}

export function platformOverlapsPet(
  platform: PetLocalPlatform,
  petX: number,
  petWidth: number,
  minimumOverlap = 1
): boolean {
  return Math.min(platform.right, petX + petWidth) - Math.max(platform.left, petX) >= minimumOverlap
}

export function platformRectsOverlap(left: PetLocalPlatform, right: PetLocalPlatform): boolean {
  return left.right > right.left && right.right > left.left
}

export function findPetDropPlatform(
  platforms: readonly PetLocalPlatform[],
  petX: number,
  petWidth: number,
  footY: number,
  groundY: number,
  occluders: readonly PetLocalPlatform[] = [],
  viewportWidth = Number.POSITIVE_INFINITY
): PetLocalPlatform | null {
  const candidates = platforms.filter((platform) => (
    platform.top >= footY - PLATFORM_EDGE_EPSILON &&
    platform.top <= groundY + PLATFORM_EDGE_EPSILON &&
    platformOverlapsPet(platform, petX, petWidth) &&
    (viewportWidth === Number.POSITIVE_INFINITY || isPetPlatformPlacementValid(platform, petX, petWidth, viewportWidth, occluders))
  ))
  candidates.sort((left, right) => left.top - right.top || left.zOrder - right.zOrder)
  return candidates[0] ?? null
}

export function findLowerPetPlatform(
  platforms: readonly PetLocalPlatform[],
  current: PetLocalPlatform,
  groundY: number,
  petWidth = 0,
  viewportWidth = Number.POSITIVE_INFINITY,
  occluders: readonly PetLocalPlatform[] = []
): PetLocalPlatform | null {
  const candidates = platforms.filter((platform) => (
    platform.id !== current.id &&
    platform.top > current.top + PLATFORM_EDGE_EPSILON &&
    platform.top <= groundY + PLATFORM_EDGE_EPSILON &&
    platformRectsOverlap(current, platform) &&
    (petWidth <= 0 || viewportWidth === Number.POSITIVE_INFINITY || getPetPlatformPlacementSegments(platform, petWidth, viewportWidth, occluders).length > 0)
  ))
  candidates.sort((left, right) => left.top - right.top || left.zOrder - right.zOrder)
  return candidates[0] ?? null
}

export function getPetPlatformLift(platform: PetLocalPlatform, groundY: number): number {
  return platform.top - groundY
}

export function getLowerPlatformLandingX(
  current: PetLocalPlatform,
  target: PetLocalPlatform,
  petX: number,
  petWidth: number,
  viewportWidth: number,
  occluders: readonly PetLocalPlatform[] = []
): number | null {
  if (!platformRectsOverlap(current, target)) return null
  const targetBounds = getPetPlatformBounds(target, petWidth, viewportWidth, 0, occluders)
  if (targetBounds.segments.length === 0) return null

  let bestX: number | null = null
  let bestDistance = Number.POSITIVE_INFINITY
  for (const segment of targetBounds.segments) {
    const candidate = Math.min(segment.maxX, Math.max(segment.minX, petX))
    const distance = Math.abs(candidate - petX)
    if (distance < bestDistance) {
      bestDistance = distance
      bestX = candidate
    }
  }
  return bestX
}
