<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import {
  getPetAtlasBehaviorActions,
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
  mood?: number
  cleanliness?: number
}

interface PetAtlasMetrics {
  /** 所有动作共用的稳定命中范围，用于屏幕边界和拖拽。 */
  width: number
  height: number
  /** 当前帧主体的实际可见范围，用于气泡和附属特效定位。 */
  visibleWidth: number
  visibleHeight: number
}

const props = withDefaults(defineProps<PetAtlasFrameProps>(), {
  action: 'idle',
  behavior: '',
  frameIndex: 0,
  scale: 1,
  playing: false,
  flipX: false,
  mood: 100,
  cleanliness: 100
})

const emit = defineEmits<{
  (event: 'metricsChange', metrics: PetAtlasMetrics): void
  (event: 'assetError'): void
  (event: 'assetReady'): void
}>()

const canvasRef = ref<HTMLCanvasElement | null>(null)
const loadedImage = shallowRef<HTMLImageElement | null>(null)
const internalFrameIndex = ref(0)
const selectedAction = ref<PetActionId>(props.action)
let imageRequestId = 0
let playbackTimer: number | undefined
let canvasContext: CanvasRenderingContext2D | null = null
let canvasContextElement: HTMLCanvasElement | null = null
let canvasBackingWidth = 0
let canvasBackingHeight = 0

const safeScale = computed(() => {
  return Number.isFinite(props.scale) && props.scale > 0 ? props.scale : 1
})

const targetDisplayHeight = computed<number | null>(() => {
  const displayHeight = props.displayHeight
  // displayHeight 是可选尺寸；先完成类型和数值收窄，再参与主体高度缩放，
  // 未传入或非法时保留原有 safeScale 兜底，不改变桌宠默认比例。
  return typeof displayHeight === 'number' && Number.isFinite(displayHeight) && displayHeight > 0
    ? displayHeight
    : null
})

// 原版按“行为”给主体设定不同的视觉高度；当前帧和稳定命中盒都必须使用这套
// 行为高度，而不是直接拿 atlas 行名的像素高度，否则睡眠、游泳等姿势会忽大忽小。
const BEHAVIOR_DISPLAY_HEIGHTS: Record<string, number> = {
  idle: 100,
  walk: 98,
  sleep: 74,
  feed: 102,
  bathe: 108,
  soak: 110,
  swim: 80,
  zen: 112,
  play: 110,
  drag: 126,
  beg: 118,
  'report-time': 100
}

const STABLE_BEHAVIORS = Object.keys(BEHAVIOR_DISPLAY_HEIGHTS)

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

const visualActivity = computed(() => {
  const behavior = props.behavior.trim().toLowerCase()
  if (behavior === 'feed') return 'eat'
  if (behavior === 'drag' || activeAction.value === 'held') return 'drag'
  if (behavior === 'munch') return 'eat'
  return behavior || activeAction.value
})

const isDirty = computed(() => props.cleanliness < 30)
const isGloomy = computed(() => props.mood < 30)
const hasFrameSequence = computed(() => animation.value.frames.length > 1)

function getFrameScale(candidate: PetAtlasFrame): number {
  // 原版按动作约束“主体可见高度”，而不是把不同姿势共用一个 atlas 缩放值；
  // 这样睡眠、游泳等横向姿势不会因为透明画布或宽高比差异显得过小或过大。
  return targetDisplayHeight.value === null
    ? safeScale.value
    : targetDisplayHeight.value / candidate.subjectBounds.height
}

function getActiveDisplayScale(): number {
  if (targetDisplayHeight.value === null) return safeScale.value

  const behavior = props.behavior.trim().toLowerCase()
  const baseHeight = BEHAVIOR_DISPLAY_HEIGHTS[behavior] ?? 100
  // 父组件传入的是已经按全局 scale 换算后的当前行为高度；把同一倍率
  // 应用于所有行为，才能让稳定命中盒和可见主体在切换动作时保持同一坐标系。
  return targetDisplayHeight.value / baseHeight
}

function getBehaviorDisplayHeight(behavior: string): number {
  return (BEHAVIOR_DISPLAY_HEIGHTS[behavior] ?? 100) * getActiveDisplayScale()
}

const visibleSize = computed(() => ({
  width: Math.max(1, frame.value.subjectBounds.width * getFrameScale(frame.value)),
  height: Math.max(1, frame.value.subjectBounds.height * getFrameScale(frame.value))
}))

const containerSize = computed(() => {
  let maxWidth = 1
  const behaviorIds = [...STABLE_BEHAVIORS]
  const currentBehavior = props.behavior.trim().toLowerCase()
  if (currentBehavior && !behaviorIds.includes(currentBehavior)) behaviorIds.push(currentBehavior)

  // 原版只遍历“行为 -> 动作”的绑定，而不是扫描 manifest 全部动作。
  // 自定义动作可能是编辑器预览或未绑定动作，混入这里会放大透明命中盒，
  // 让边界、拖拽和气泡锚点都偏离可见主体。
  for (const behavior of behaviorIds) {
    const actionDisplayHeight = getBehaviorDisplayHeight(behavior)
    const actions = getPetAtlasBehaviorActions(props.manifest, behavior)
    for (const action of actions) {
      const candidate = props.manifest.animations[action] ?? props.manifest.animations.idle
      if (!candidate) continue
      for (const candidateFrame of candidate.frames) {
        const candidateScale = actionDisplayHeight / candidateFrame.subjectBounds.height
        maxWidth = Math.max(maxWidth, candidateFrame.subjectBounds.width * candidateScale)
      }
    }
  }

  // 原版的交互高度固定为最大动作高度（drag=126px），而不是由当前帧决定；
  // 这样动作切换不会让屏幕边界和拖拽区域上下跳动。自定义动作仍需兜底包住主体。
  const maxHeight = Math.max(126 * getActiveDisplayScale(), visibleSize.value.height)
  maxWidth = Math.max(maxWidth, visibleSize.value.width)
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

const frameFilter = computed(() => {
  const filters: string[] = []
  if (isDirty.value) filters.push('saturate(0.65)', 'brightness(0.92)')
  if (isGloomy.value) filters.push('grayscale(0.3)')
  return filters.length > 0 ? filters.join(' ') : undefined
})

const sleepMarkStyle = computed(() => ({
  right: `${Math.max(4, (containerSize.value.width - visibleSize.value.width) / 2 + 8)}px`,
  bottom: `${Math.max(8, visibleSize.value.height - 8)}px`
}))

watch(
  [
    () => Math.ceil(containerSize.value.width),
    () => Math.ceil(containerSize.value.height),
    () => Math.ceil(visibleSize.value.width),
    () => Math.ceil(visibleSize.value.height)
  ],
  ([width, height, visibleWidth, visibleHeight]) => {
    // 父窗口不能读取 canvas 的透明像素；由 atlas 组件统一发布几何事实，
    // 避免窗口层重新猜测不同动作和自定义皮肤的实际尺寸。
    emit('metricsChange', {
      width,
      height,
      visibleWidth,
      visibleHeight
    })
  },
  { immediate: true }
)

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

  if (canvasContextElement !== canvas) {
    canvasContextElement = canvas
    canvasContext = null
    canvasBackingWidth = 0
    canvasBackingHeight = 0
  }

  const nextWidth = canvasSize.value.width
  const nextHeight = canvasSize.value.height
  const backingStoreChanged = canvasBackingWidth !== nextWidth || canvasBackingHeight !== nextHeight
  if (backingStoreChanged) {
    // 每帧重设 width/height 会清空位图并重建 backing store；动画帧只应更新
    // 绘制内容，只有设备像素比或主体尺寸变化时才需要重建画布。
    canvas.width = nextWidth
    canvas.height = nextHeight
    canvasBackingWidth = nextWidth
    canvasBackingHeight = nextHeight
    canvasContext = null
  }

  const needsContextSetup = backingStoreChanged || canvasContext === null
  const context = canvasContext ?? (canvasContext = canvas.getContext('2d'))
  if (!context) return

  context.clearRect(0, 0, canvas.width, canvas.height)
  if (!image) return

  // 只裁主体而不是整块 frame，透明留白不会改变可见尺寸，底部中心锚点也能跨动作保持不动。
  if (needsContextSetup) {
    context.imageSmoothingEnabled = true
    context.imageSmoothingQuality = 'high'
  }
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
  if (!url || typeof Image === 'undefined') {
    emit('assetError')
    return
  }

  const image = new Image()
  image.decoding = 'async'
  image.onload = () => {
    if (requestId !== imageRequestId) return
    loadedImage.value = image
    emit('assetReady')
  }
  image.onerror = () => {
    if (requestId !== imageRequestId) return
    loadedImage.value = null
    emit('assetError')
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
  canvasContext = null
  canvasContextElement = null
})
</script>

<template>
  <div
    class="pet-atlas-frame"
    :style="{
      width: `${containerSize.width}px`,
      height: `${containerSize.height}px`,
      transform: props.flipX ? 'scaleX(-1)' : undefined
    }"
    aria-hidden="true"
  >
    <div
      class="pet-atlas-frame__body"
      :class="[
        `is-${visualActivity}`,
        {
          'is-frame-sequence': hasFrameSequence,
          'is-dirty': isDirty,
          'is-gloomy': isGloomy
        }
      ]"
    >
      <canvas
        ref="canvasRef"
        class="pet-atlas-frame__canvas"
        :style="{
          width: `${visibleSize.width}px`,
          height: `${visibleSize.height}px`,
          marginLeft: `${-visibleSize.width / 2}px`,
          filter: frameFilter
        }"
      />

      <!-- 脏污和低落状态是角色本身的视觉反馈，不应该依赖状态栏才能被发现。 -->
      <template v-if="isDirty && visualActivity !== 'bathe'">
        <span class="pet-atlas-frame__dirt pet-atlas-frame__dirt--one"></span>
        <span class="pet-atlas-frame__dirt pet-atlas-frame__dirt--two"></span>
      </template>
    </div>

    <!-- 原版把照料动作的附属动画挂在精灵容器上，气泡和主体因此保持同一锚点。 -->
    <template v-if="visualActivity === 'soak'">
      <span
        v-for="index in 3"
        :key="`steam-${index}`"
        class="pet-atlas-frame__steam"
        :style="{
          left: `${25 + (index - 1) * 22}%`,
          animationDelay: `${(index - 1) * 0.6}s`,
          animationDuration: `${2.2 + (index - 1) * 0.5}s`
        }"
      ></span>
    </template>

    <template v-if="visualActivity === 'bathe'">
      <span
        v-for="index in 3"
        :key="`bubble-${index}`"
        class="pet-atlas-frame__bath-bubble"
        :style="{
          left: `${22 + (index - 1) * 26}%`,
          width: `${8 + ((index - 1) % 2) * 5}px`,
          height: `${8 + ((index - 1) % 2) * 5}px`,
          animationDelay: `${(index - 1) * 0.4}s`,
          animationDuration: `${1.7 + (index - 1) * 0.4}s`
        }"
      ></span>
    </template>

    <span
      v-if="visualActivity === 'sleep'"
      class="pet-atlas-frame__sleep-mark"
      :style="{
        ...sleepMarkStyle,
        transform: props.flipX ? 'scaleX(-1)' : undefined
      }"
    >Zzz</span>
  </div>
</template>

<style scoped>
.pet-atlas-frame {
  position: relative;
  flex: 0 0 auto;
  overflow: visible;
}

.pet-atlas-frame__body {
  position: relative;
  width: 100%;
  height: 100%;
  transform-origin: center bottom;
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

.pet-atlas-frame__body.is-frame-sequence {
  animation: none !important;
}

.pet-atlas-frame__body.is-idle { animation: pet-atlas-idle 1.5s ease-in-out infinite alternate; }
.pet-atlas-frame__body.is-walk { animation: pet-atlas-walk 0.34s ease-in-out infinite; }
.pet-atlas-frame__body.is-sleep { animation: pet-atlas-sleep 2s ease-in-out infinite alternate; }
.pet-atlas-frame__body.is-eat { animation: pet-atlas-eat 0.6s ease-in-out infinite; }
.pet-atlas-frame__body.is-bathe { animation: pet-atlas-bathe 1.2s ease-in-out infinite; }
.pet-atlas-frame__body.is-soak { animation: pet-atlas-soak 1.9s ease-in-out infinite alternate; }
.pet-atlas-frame__body.is-swim { animation: pet-atlas-swim 1.1s ease-in-out infinite; }
.pet-atlas-frame__body.is-zen { animation: pet-atlas-zen 2.6s ease-in-out infinite alternate; }
.pet-atlas-frame__body.is-play { animation: pet-atlas-play 0.55s ease-out infinite; }
.pet-atlas-frame__body.is-drag { animation: pet-atlas-drag 0.8s ease-in-out infinite alternate; }
.pet-atlas-frame__body.is-beg { animation: pet-atlas-beg 0.7s ease-in-out infinite; }
.pet-atlas-frame__body.is-report-time { animation: pet-atlas-report-time 0.8s ease-in-out infinite; }

.pet-atlas-frame__dirt,
.pet-atlas-frame__steam,
.pet-atlas-frame__bath-bubble,
.pet-atlas-frame__sleep-mark {
  position: absolute;
  pointer-events: none;
}

.pet-atlas-frame__dirt {
  border-radius: 50%;
  background: #6b4f33;
}

.pet-atlas-frame__dirt--one {
  left: 30%;
  top: 45%;
  width: 10px;
  height: 6px;
  opacity: 0.4;
}

.pet-atlas-frame__dirt--two {
  left: 52%;
  top: 62%;
  width: 8px;
  height: 5px;
  opacity: 0.35;
}

.pet-atlas-frame__steam {
  bottom: 70%;
  width: 12px;
  height: 20px;
  border-radius: 50%;
  background: rgba(255, 255, 255, 0.75);
  filter: blur(4px);
  animation-name: pet-atlas-steam;
  animation-timing-function: ease-out;
  animation-iteration-count: infinite;
}

.pet-atlas-frame__bath-bubble {
  bottom: 55%;
  border: 1.5px solid rgba(158, 201, 232, 0.9);
  border-radius: 50%;
  background: rgba(200, 228, 248, 0.4);
  animation-name: pet-atlas-bath-bubble;
  animation-timing-function: ease-out;
  animation-iteration-count: infinite;
}

.pet-atlas-frame__sleep-mark {
  color: #8a7358;
  font-size: 18px;
  font-weight: 700;
  line-height: 1;
  animation: pet-atlas-sleep-mark 2.2s ease-out infinite;
}

@keyframes pet-atlas-idle {
  from { transform: scaleY(1); }
  to { transform: scaleY(1.02); }
}

@keyframes pet-atlas-walk {
  0%, 100% { transform: translateY(0); }
  50% { transform: translateY(-3px); }
}

@keyframes pet-atlas-sleep {
  from { transform: scaleY(0.97); }
  to { transform: scaleY(1); }
}

@keyframes pet-atlas-eat {
  0%, 100% { transform: translateY(0) rotate(0deg); }
  50% { transform: translateY(1.5px) rotate(2.5deg); }
}

@keyframes pet-atlas-bathe {
  0%, 100% { transform: translateY(0); }
  50% { transform: translateY(-2px); }
}

@keyframes pet-atlas-soak {
  from { transform: scaleY(0.99); }
  to { transform: scaleY(1.015); }
}

@keyframes pet-atlas-swim {
  0%, 100% { transform: translateY(0) rotate(0deg); }
  50% { transform: translateY(-2.5px) rotate(1.5deg); }
}

@keyframes pet-atlas-zen {
  from { transform: scaleY(1); }
  to { transform: scaleY(1.012); }
}

@keyframes pet-atlas-play {
  0%, 100% { transform: translateY(0); }
  50% { transform: translateY(-16px); }
}

@keyframes pet-atlas-drag {
  from { transform: rotate(-3deg); }
  to { transform: rotate(3deg); }
}

@keyframes pet-atlas-beg {
  0%, 100% { transform: translateY(0); }
  50% { transform: translateY(-6px); }
}

@keyframes pet-atlas-report-time {
  0%, 100% { transform: translateY(0) rotate(0deg); }
  50% { transform: translateY(-2px) rotate(1.5deg); }
}

@keyframes pet-atlas-steam {
  from { opacity: 0.75; transform: translateY(0) scaleX(1); }
  to { opacity: 0; transform: translateY(-34px) scaleX(1.6); }
}

@keyframes pet-atlas-bath-bubble {
  from { opacity: 0.9; transform: translateY(-4px); }
  to { opacity: 0; transform: translateY(-46px); }
}

@keyframes pet-atlas-sleep-mark {
  0% { opacity: 0.75; transform: translateY(0); }
  100% { opacity: 0; transform: translateY(-14px); }
}

@media (prefers-reduced-motion: reduce) {
  .pet-atlas-frame__body,
  .pet-atlas-frame__steam,
  .pet-atlas-frame__bath-bubble,
  .pet-atlas-frame__sleep-mark {
    animation: none !important;
  }
}
</style>
