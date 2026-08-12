<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import {
  getPetAtlasBehaviorActions,
  getPetAtlasActionKeys,
  getPetAtlasAnimation,
  getPetAtlasFrame,
  normalizePetAtlasFrameIndex,
  type PetActionId,
  type PetAtlasDocument,
  type PetAtlasFrame
} from './petAtlas'

interface PetAtlasFrameProps {
  imageUrl: string
  manifest: PetAtlasDocument
  action?: PetActionId
  behavior?: string
  frameIndex?: number
  scale?: number
  displayHeight?: number
  playing?: boolean
  flipX?: boolean
}

const props = withDefaults(defineProps<PetAtlasFrameProps>(), {
  action: 'idle',
  behavior: '',
  frameIndex: 0,
  scale: 1,
  playing: false,
  flipX: false
})

const canvasRef = ref<HTMLCanvasElement | null>(null)
const loadedImage = shallowRef<HTMLImageElement | null>(null)
const internalFrameIndex = ref(0)
const selectedAction = ref<PetActionId>(props.action)
let imageRequestId = 0
let playbackTimer: number | undefined

const safeScale = computed(() => {
  return Number.isFinite(props.scale) && props.scale > 0 ? props.scale : 1
})

const targetDisplayHeight = computed(() => {
  return Number.isFinite(props.displayHeight) && props.displayHeight > 0
    ? props.displayHeight
    : null
})

const activeAction = computed<PetActionId>(() => {
  if (!props.behavior) return props.action
  const actions = getPetAtlasBehaviorActions(props.manifest, props.behavior)
  return actions.includes(selectedAction.value) ? selectedAction.value : actions[0] ?? props.action
})

const animation = computed(() => getPetAtlasAnimation(props.manifest, activeAction.value))

const selectedFrameIndex = computed(() => {
  if (props.playing) return normalizePetAtlasFrameIndex(animation.value, internalFrameIndex.value)
  return normalizePetAtlasFrameIndex(animation.value, props.frameIndex)
})

const frame = computed<PetAtlasFrame>(() => {
  return getPetAtlasFrame(props.manifest, activeAction.value, selectedFrameIndex.value)
})

function getFrameScale(candidate: PetAtlasFrame): number {
  // 原版按动作约束“主体可见高度”，而不是把不同姿势共用一个 atlas 缩放值；
  // 这样睡眠、游泳等横向姿势不会因为透明画布或宽高比差异显得过小或过大。
  return targetDisplayHeight.value === null
    ? safeScale.value
    : targetDisplayHeight.value / candidate.subjectBounds.height
}

const visibleSize = computed(() => ({
  width: Math.max(1, frame.value.subjectBounds.width * getFrameScale(frame.value)),
  height: Math.max(1, frame.value.subjectBounds.height * getFrameScale(frame.value))
}))

const containerSize = computed(() => {
  let maxWidth = 1
  let maxHeight = 1
  for (const action of getPetAtlasActionKeys(props.manifest.animations)) {
    const candidate = props.manifest.animations[action]
    if (!candidate) continue
    for (const candidateFrame of candidate.frames) {
      const candidateScale = getFrameScale(candidateFrame)
      maxWidth = Math.max(maxWidth, candidateFrame.subjectBounds.width * candidateScale)
      maxHeight = Math.max(maxHeight, candidateFrame.subjectBounds.height * candidateScale)
    }
  }
  return {
    width: maxWidth,
    height: maxHeight
  }
})

const canvasSize = computed(() => {
  const pixelRatio = typeof window === 'undefined' ? 1 : window.devicePixelRatio || 1
  return {
    width: Math.max(1, Math.round(visibleSize.value.width * pixelRatio)),
    height: Math.max(1, Math.round(visibleSize.value.height * pixelRatio))
  }
})

function clearPlaybackTimer() {
  if (playbackTimer === undefined) return
  window.clearTimeout(playbackTimer)
  playbackTimer = undefined
}

function scheduleNextFrame() {
  clearPlaybackTimer()
  if (!props.playing || animation.value.frames.length <= 1) return

  const currentIndex = selectedFrameIndex.value
  const lastIndex = animation.value.frames.length - 1
  if (!animation.value.loop && currentIndex >= lastIndex) return

  const delay = Math.max(16, frame.value.durationMs)
  playbackTimer = window.setTimeout(() => {
    const nextIndex = currentIndex >= lastIndex ? 0 : currentIndex + 1
    internalFrameIndex.value = nextIndex
    scheduleNextFrame()
  }, delay)
}

function resetPlayback() {
  internalFrameIndex.value = normalizePetAtlasFrameIndex(animation.value, props.frameIndex)
  scheduleNextFrame()
}

function chooseBehaviorAction(behavior: string, previousAction: PetActionId): PetActionId {
  const actions = getPetAtlasBehaviorActions(props.manifest, behavior)
  if (actions.length === 0) return props.action

  // 只有行为切换才重新抽签；同一行为的帧推进不会改变动作，避免播放中途跳到另一行。
  const candidates = actions.length > 1 ? actions.filter((action) => action !== previousAction) : actions
  return candidates[Math.floor(Math.random() * candidates.length)] ?? actions[0]
}

function renderCanvas() {
  const canvas = canvasRef.value
  const image = loadedImage.value
  if (!canvas) return

  canvas.width = canvasSize.value.width
  canvas.height = canvasSize.value.height
  const context = canvas.getContext('2d')
  if (!context) return

  context.clearRect(0, 0, canvas.width, canvas.height)
  if (!image) return

  // 只裁主体而不是整块 frame，透明留白不会改变可见尺寸，底部中心锚点也能跨动作保持不动。
  context.imageSmoothingEnabled = true
  context.imageSmoothingQuality = 'high'
  context.drawImage(
    image,
    frame.value.x + frame.value.subjectBounds.x,
    frame.value.y + frame.value.subjectBounds.y,
    frame.value.subjectBounds.width,
    frame.value.subjectBounds.height,
    0,
    0,
    canvas.width,
    canvas.height
  )
}

function loadImage(url: string) {
  const requestId = ++imageRequestId
  loadedImage.value = null
  if (!url || typeof Image === 'undefined') return

  const image = new Image()
  image.decoding = 'async'
  image.onload = () => {
    if (requestId === imageRequestId) loadedImage.value = image
  }
  image.onerror = () => {
    if (requestId === imageRequestId) loadedImage.value = null
  }
  image.src = url
}

watch(
  () => props.imageUrl,
  (url) => loadImage(url),
  { immediate: true }
)

watch(
  [() => props.behavior, () => props.action, () => props.manifest],
  ([behavior, action], previous) => {
    if (!behavior) {
      selectedAction.value = action
      return
    }

    const available = getPetAtlasBehaviorActions(props.manifest, behavior)
    const behaviorChanged = behavior !== previous?.[0]
    // 快照刷新可能产生新的 manifest 对象，但只要当前动作仍有效，就必须保持本轮播放不变。
    if (behaviorChanged || !available.includes(selectedAction.value)) {
      selectedAction.value = chooseBehaviorAction(behavior, selectedAction.value)
    }
  },
  { immediate: true }
)

watch(
  [() => props.behavior, () => props.action, () => props.frameIndex, () => props.manifest, () => props.playing, activeAction],
  () => resetPlayback()
)

watch(
  [loadedImage, frame, canvasSize, () => props.flipX],
  () => {
    void nextTick(renderCanvas)
  },
  { flush: 'post' }
)

onMounted(() => {
  resetPlayback()
  renderCanvas()
})

onBeforeUnmount(() => {
  clearPlaybackTimer()
  imageRequestId += 1
})
</script>

<template>
  <div
    class="pet-atlas-frame"
    :style="{
      width: `${containerSize.width}px`,
      height: `${containerSize.height}px`
    }"
    aria-hidden="true"
  >
    <canvas
      ref="canvasRef"
      class="pet-atlas-frame__canvas"
      :style="{
        width: `${visibleSize.width}px`,
        height: `${visibleSize.height}px`,
        marginLeft: `${-visibleSize.width / 2}px`,
        transform: props.flipX ? 'scaleX(-1)' : undefined
      }"
    />
  </div>
</template>

<style scoped>
.pet-atlas-frame {
  position: relative;
  flex: 0 0 auto;
  overflow: visible;
}

.pet-atlas-frame__canvas {
  position: absolute;
  left: 50%;
  bottom: 0;
  display: block;
  transform-origin: center bottom;
  pointer-events: none;
  user-select: none;
}
</style>
