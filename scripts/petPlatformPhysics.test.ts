import assert from 'node:assert/strict'
import test from 'node:test'
import {
  findPetDropPlatform,
  getLowerPlatformLandingX,
  getPetPlatformBounds,
  isPetPlatformPlacementValid,
  normalizePetPlatformSnapshot,
  type PetLocalPlatform
} from '../frontend/src/components/Pet/petPlatformPhysics.ts'

const platform = (overrides: Partial<PetLocalPlatform> = {}): PetLocalPlatform => ({
  id: 'platform',
  left: 0,
  top: 500,
  right: 400,
  bottom: 900,
  zOrder: 2,
  ...overrides
})

test('遮挡窗口会把平台完整脚底落点切成两个区间', () => {
  const base = platform()
  const occluder = platform({ id: 'occluder', left: 120, right: 220, top: 100, bottom: 550, zOrder: 0 })
  const bounds = getPetPlatformBounds(base, 80, 500, 0, [base, occluder])

  assert.deepEqual(bounds.segments, [
    { minX: 0, maxX: 40 },
    { minX: 220, maxX: 320 }
  ])
  assert.equal(isPetPlatformPlacementValid(base, 100, 80, 500, [base, occluder]), false)
  assert.equal(isPetPlatformPlacementValid(base, 240, 80, 500, [base, occluder]), true)
})

test('下落平台只接受完整脚底落点，不再用窄重叠回退', () => {
  const current = platform({ id: 'current', left: 0, right: 300, top: 300, bottom: 600, zOrder: 0 })
  const narrow = platform({ id: 'narrow', left: 260, right: 320, top: 600, bottom: 900, zOrder: 1 })
  const wide = platform({ id: 'wide', left: 240, right: 500, top: 600, bottom: 900, zOrder: 1 })

  assert.equal(getLowerPlatformLandingX(current, narrow, 220, 80, 600, [current, narrow]), null)
  assert.equal(getLowerPlatformLandingX(current, wide, 220, 80, 600, [current, wide]), 240)
})

test('被遮挡的平台不能成为拖拽落点', () => {
  const base = platform()
  const occluder = platform({ id: 'occluder', left: 120, right: 220, top: 100, bottom: 550, zOrder: 0 })

  assert.equal(findPetDropPlatform([base], 100, 80, 480, 900, [base, occluder], 500), null)
  assert.equal(findPetDropPlatform([base], 240, 80, 480, 900, [base, occluder], 500)?.id, 'platform')
})

test('旧宿主缺少增强字段时仍能归一化为桌面状态', () => {
  const snapshot = normalizePetPlatformSnapshot({
    available: true,
    overlay: { left: 0, top: 0, right: 100, bottom: 100 },
    platforms: []
  })
  assert.deepEqual(snapshot.occluders, [])
  assert.equal(snapshot.movingWindowId, '')
})
