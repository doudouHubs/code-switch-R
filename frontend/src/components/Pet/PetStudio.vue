<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { petApi } from './petApi'
import {
  deletePetStudioSkin,
  generatePetStudioImage,
  listPetStudioSkins,
  packPetStudioAtlas,
  processPetStudioFrame,
  readPetStudioAtlas,
  savePetStudioSkin,
  splitPetStudioActionSheet,
  toDataUrl,
  type PetStudioActionInput,
  type PetStudioFrameInput,
  type PetStudioSkinRecord
} from './petStudioApi'
import {
  createBlankPetStudioProject,
  createGeneratedPetStudioFrame,
  createImportedPetStudioProject,
  getPetStudioActionIds,
  getPetStudioAtlasFrame,
  getDefaultPetStudioBehaviors,
  isBuiltinPetStudioAction,
  toPetStudioPackBehaviors,
  type PetStudioActionId,
  type PetStudioAnimation,
  type PetStudioFrame,
  type PetStudioFrameGeometry,
  type PetStudioProject
} from './petStudioModel'
import { createPetStudioSession, type PetStudioReferenceImage } from './petStudioSession'

interface Props { petId?: string }

const props = withDefaults(defineProps<Props>(), { petId: 'default' })
const { t } = useI18n()

type GenerationKind = 'append' | 'replace' | 'batch'
type GenerationStatus = 'idle' | 'running' | 'success' | 'error' | 'cancelled'
type FrameFileMode = 'append' | 'replace'
type SaveMode = 'update' | 'copy'

interface LocalReferenceImage extends PetStudioReferenceImage {}

interface GenerationRequest {
  pose: PetStudioActionId
  subject: string
  prompt: string
  platform: string
  providerId: string
  modelId: string
  frameCount: number
  keyColor: string
  targetHeight: number
  chromaKey: boolean
  references: LocalReferenceImage[]
}

interface GenerationJob {
  kind: GenerationKind
  request: GenerationRequest
  poses: PetStudioActionId[]
}

const studio = createPetStudioSession()
const { state } = studio

const phase = reactive({
  loading: false,
  generating: false,
  processing: false,
  packing: false,
  saving: false,
  deleting: false
})
const generationStatus = ref<GenerationStatus>('idle')
const generationStep = ref<'idle' | 'generate' | 'split' | 'chroma' | 'normalize' | 'pack'>('idle')
const generationToken = ref(0)
const frameSerial = ref(0)
const frameFileMode = ref<FrameFileMode>('append')
const actionIdInput = ref('')
const behaviorIdInput = ref('')
const saveBind = ref(true)
const saveMode = ref<SaveMode>('copy')
const sourceChoice = ref(state.sourceSelection)
const skins = ref<PetStudioSkinRecord[]>([])
const errorMessage = ref('')
const notice = ref('')
const generatedPreview = ref('')
const atlasPreview = ref('')
const packedManifest = ref<PetStudioProject['source']['atlas'] extends infer _ ? Record<string, unknown> | null : null>(null)
const packedData = ref('')
const restyleActions = ref<PetStudioActionId[]>([])
const lastGenerationJob = ref<GenerationJob | null>(null)
const framePreviews = reactive<Record<string, string>>({})
const frameFileInput = ref<HTMLInputElement | null>(null)
const referenceFileInput = ref<HTMLInputElement | null>(null)

const generation = reactive({
  platform: '',
  providerId: '',
  prompt: '',
  frameCount: 1,
  keyColor: '#00ff00',
  targetHeight: 256,
  chromaKey: true
})

const text = (key: string, params?: Record<string, unknown>, defaultValue?: string): string => {
  const values = { ...(params ?? {}), ...(defaultValue ? { defaultValue } : {}) }
  return t(key, values)
}

const actionLabel = (action: PetStudioActionId): string => text(`pet.studio.actions.${action}`, undefined, action)
const behaviorLabel = (behaviorId: string): string => text(`pet.studio.behaviors.${behaviorId}`, undefined, behaviorId)
const sourceLabel = (source: string): string => text(`pet.studio.source.${source}`, undefined, source)
const stepLabel = (step: string): string => text(`pet.studio.steps.${step}`, undefined, step)

const busy = computed(() => Object.values(phase).some(Boolean) || state.saving)
const generationRunning = computed(() => generationStatus.value === 'running' && (phase.generating || phase.processing))
const project = computed(() => state.project)
const referenceImages = computed(() => state.referenceImages)
const actionIds = computed<PetStudioActionId[]>(() => {
  const ids = getPetStudioActionIds(project.value.animations)
  // 新工程还没有动画时仍显示 idle，用户可以先生成第一帧建立动画。
  return ids.includes('idle') ? ids : ['idle', ...ids]
})
const existingActionIds = computed(() => getPetStudioActionIds(project.value.animations))
const currentAnimation = computed<PetStudioAnimation | null>(() => project.value.animations[state.selectedPose] ?? null)
const currentFrames = computed(() => currentAnimation.value?.frames ?? [])
const selectedFrame = computed(() => currentFrames.value.find((frame) => frame.id === state.selectedFrameId) ?? currentFrames.value[0] ?? null)
const selectedFrameIndex = computed(() => selectedFrame.value ? currentFrames.value.findIndex((frame) => frame.id === selectedFrame.value?.id) : -1)
const selectedFramePreview = computed(() => selectedFrame.value ? framePreview(selectedFrame.value) : '')
const canPack = computed(() => Boolean(project.value.animations.idle?.frames.length))
const canSave = computed(() => canPack.value && project.value.name.trim().length > 0 && !project.value.source.atlas?.src.includes('file://'))
const canGenerate = computed(() => Boolean(project.value.subject.trim() && project.value.modelId.trim() && generation.providerId.trim()))
const canUpdate = computed(() => project.value.source.kind === 'skin' && project.value.source.canUpdate && Boolean(project.value.source.skinId))
const selectedBehavior = computed(() => state.project.behaviors[state.selectedBehaviorId] ?? null)
const behaviorIds = computed(() => Object.keys(project.value.behaviors))
const availableBehaviorActions = computed(() => existingActionIds.value)
const selectedRestyleCount = computed(() => restyleActions.value.filter((action) => Boolean(project.value.animations[action]?.frames.length)).length)

const projectName = computed({
  get: () => project.value.name,
  set: (value: string) => dispatchProject({ type: 'set-name', name: value })
})
const projectSubject = computed({
  get: () => project.value.subject,
  set: (value: string) => dispatchProject({ type: 'set-subject', subject: value })
})
const projectModelId = computed({
  get: () => project.value.modelId,
  set: (value: string) => dispatchProject({ type: 'set-model', modelId: value })
})

function errorOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function clearMessages(): void {
  errorMessage.value = ''
  notice.value = ''
}

function resetPackedPreview(): void {
  packedData.value = ''
  atlasPreview.value = ''
  packedManifest.value = null
}

function ensureSelection(next: PetStudioProject): void {
  const ids = getPetStudioActionIds(next.animations)
  const pose = ids.includes(state.selectedPose) ? state.selectedPose : ids[0] ?? 'idle'
  const animation = next.animations[pose]
  const selectedId = animation?.frames.some((frame) => frame.id === state.selectedFrameId)
    ? state.selectedFrameId
    : animation?.frames[0]?.id ?? null
  studio.setSession({ selectedPose: pose, selectedFrameId: selectedId })
}

function dispatchProject(action: Parameters<typeof studio.dispatchProject>[0]): PetStudioProject {
  const next = studio.dispatchProject(action)
  resetPackedPreview()
  ensureSelection(next)
  return next
}

function selectPose(pose: PetStudioActionId): void {
  if (busy.value) return
  const animation = project.value.animations[pose]
  studio.setSession({ selectedPose: pose, selectedFrameId: animation?.frames[0]?.id ?? null })
}

function selectFrame(frameId: string): void {
  if (busy.value) return
  studio.setSession({ selectedFrameId: frameId })
}

function imageDataUrl(data: string, mediaType = 'image/png'): string {
  if (!data) return ''
  return toDataUrl(data, mediaType)
}

function nextFrameId(prefix: string): string {
  frameSerial.value += 1
  return `${prefix}:${Date.now().toString(36)}:${frameSerial.value}`
}

function isBuiltinBehavior(behaviorId: string): boolean {
  return Object.prototype.hasOwnProperty.call(getDefaultPetStudioBehaviors(), behaviorId)
}

function isActionReferenced(actionId: string): boolean {
  return Object.values(project.value.behaviors).some((behavior) => behavior.actions.includes(actionId))
}

function framePreview(frame: PetStudioFrame): string {
  if (frame.source.kind === 'file') return frame.source.dataUrl
  return framePreviews[frame.id] || project.value.source.atlas?.src || ''
}

function readFileAsDataUrl(file: File): Promise<{ dataUrl: string; mediaType: string }> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      const dataUrl = typeof reader.result === 'string' ? reader.result : ''
      if (!dataUrl) {
        reject(new Error(text('pet.studio.fileReadFailed', undefined, '无法读取图片文件。')))
        return
      }
      resolve({ dataUrl, mediaType: file.type || 'image/png' })
    }
    reader.onerror = () => reject(new Error(text('pet.studio.fileReadFailed', undefined, '无法读取图片文件。')))
    reader.readAsDataURL(file)
  })
}

function readImageGeometry(dataUrl: string, fallbackHeight: number): Promise<PetStudioFrameGeometry> {
  if (typeof Image === 'undefined') {
    const size = Math.max(32, Math.floor(fallbackHeight))
    return Promise.resolve({ width: size, height: size, subjectBounds: { x: 0, y: 0, width: size, height: size } })
  }
  return new Promise((resolve) => {
    const image = new Image()
    image.onload = () => {
      const width = Math.max(1, image.naturalWidth || Math.floor(fallbackHeight))
      const height = Math.max(1, image.naturalHeight || Math.floor(fallbackHeight))
      resolve({ width, height, subjectBounds: { x: 0, y: 0, width, height } })
    }
    image.onerror = () => {
      const size = Math.max(32, Math.floor(fallbackHeight))
      resolve({ width: size, height: size, subjectBounds: { x: 0, y: 0, width: size, height: size } })
    }
    image.src = dataUrl
  })
}

async function cropAtlasFrame(atlasSrc: string, crop: { x: number; y: number; width: number; height: number }): Promise<string> {
  if (typeof document === 'undefined' || typeof Image === 'undefined') return atlasSrc
  const image = new Image()
  const loaded = await new Promise<HTMLImageElement>((resolve, reject) => {
    image.onload = () => resolve(image)
    image.onerror = () => reject(new Error(text('pet.studio.atlasPreviewFailed', undefined, '无法读取 atlas 帧预览。')))
    image.src = atlasSrc
  })
  if (crop.x < 0 || crop.y < 0 || crop.width < 1 || crop.height < 1 || crop.x + crop.width > loaded.naturalWidth || crop.y + crop.height > loaded.naturalHeight) {
    throw new Error(text('pet.studio.atlasCropInvalid', undefined, 'atlas 帧裁剪区域越过图片边界。'))
  }
  const canvas = document.createElement('canvas')
  canvas.width = crop.width
  canvas.height = crop.height
  const context = canvas.getContext('2d')
  if (!context) return atlasSrc
  context.drawImage(loaded, crop.x, crop.y, crop.width, crop.height, 0, 0, crop.width, crop.height)
  return canvas.toDataURL('image/png')
}

async function refreshFramePreviews(): Promise<void> {
  const refreshToken = ++frameSerial.value
  const current = project.value
  const atlasSrc = current.source.atlas?.src
  for (const pose of getPetStudioActionIds(current.animations)) {
    const animation = current.animations[pose]
    if (!animation) continue
    for (const frame of animation.frames) {
      if (frame.source.kind === 'file') {
        framePreviews[frame.id] = frame.source.dataUrl
        continue
      }
      const atlasFrame = getPetStudioAtlasFrame(current, frame.source.pose, frame.source.frameIndex)
      if (!atlasSrc || !atlasFrame) continue
      try {
        const preview = await cropAtlasFrame(atlasSrc, atlasFrame)
        if (refreshToken !== frameSerial.value) return
        framePreviews[frame.id] = preview
      } catch {
        // canvas 读取失败时保留 atlas 本体，保存仍会由 API 再次校验 crop，避免伪造一张空白帧。
        if (refreshToken === frameSerial.value) framePreviews[frame.id] = atlasSrc
      }
    }
  }
}

function clearFramePreviews(): void {
  for (const key of Object.keys(framePreviews)) delete framePreviews[key]
}

async function loadSkins(showError = true): Promise<void> {
  phase.loading = true
  if (showError) errorMessage.value = ''
  try {
    skins.value = await listPetStudioSkins(props.petId)
  } catch (error) {
    if (showError) errorMessage.value = errorOf(error)
  } finally {
    phase.loading = false
  }
}

function sourceValue(event: Event): string {
  return (event.target as HTMLSelectElement).value
}

function confirmDiscard(): boolean {
  if (!studio.dirty.value) return true
  return typeof window === 'undefined' || window.confirm(text('pet.studio.discardConfirm', undefined, '当前有未保存修改，切换来源会放弃这些修改。继续吗？'))
}

async function readProjectFromSource(selection: string): Promise<PetStudioProject> {
  if (selection === 'new') return createBlankPetStudioProject()
  const result = await readPetStudioAtlas(props.petId, selection === 'default' ? 'default' : { skinId: selection })
  const imported = createImportedPetStudioProject(result.atlas, result.skin)
  // ReadSkin 对默认资源也会返回一条内置记录，但默认资源始终是只读来源，不能被误判为可更新皮肤。
  imported.source = selection === 'default'
    ? { kind: 'default', canUpdate: false, atlas: result.atlas }
    : { kind: 'skin', skinId: selection, canUpdate: imported.builtin !== true, atlas: result.atlas }
  return imported
}

async function changeSource(selection: string): Promise<void> {
  if (busy.value || selection === state.sourceSelection) {
    sourceChoice.value = state.sourceSelection
    return
  }
  if (!confirmDiscard()) {
    sourceChoice.value = state.sourceSelection
    return
  }
  clearMessages()
  phase.loading = true
  try {
    const next = await readProjectFromSource(selection)
    studio.loadProject(next, selection)
    const firstPose = getPetStudioActionIds(next.animations)[0] ?? 'idle'
    studio.setSession({
      selectedPose: firstPose,
      selectedFrameId: next.animations[firstPose]?.frames[0]?.id ?? null,
      referenceImages: []
    })
    sourceChoice.value = selection
    generatedPreview.value = ''
    lastGenerationJob.value = null
    generationStatus.value = 'idle'
    clearFramePreviews()
    resetPackedPreview()
    await refreshFramePreviews()
    notice.value = text('pet.studio.sourceLoaded', { source: sourceLabel(selection === 'new' ? 'new' : next.source.kind) }, '已加载 {source}。')
  } catch (error) {
    sourceChoice.value = state.sourceSelection
    errorMessage.value = errorOf(error)
  } finally {
    phase.loading = false
  }
}

function clampFrameCount(value: number): number {
  return Math.min(8, Math.max(1, Math.floor(Number(value) || 1)))
}

function buildGenerationRequest(pose: PetStudioActionId, frameCount = generation.frameCount): GenerationRequest {
  return {
    pose,
    subject: project.value.subject.trim(),
    prompt: generation.prompt.trim(),
    platform: generation.platform.trim(),
    providerId: generation.providerId.trim(),
    modelId: project.value.modelId.trim(),
    frameCount: clampFrameCount(frameCount),
    keyColor: generation.keyColor,
    targetHeight: Math.max(32, Math.floor(Number(generation.targetHeight) || 256)),
    chromaKey: generation.chromaKey,
    references: state.referenceImages.map((reference) => ({ ...reference }))
  }
}

function generationPrompt(request: GenerationRequest): string {
  return [
    request.subject,
    request.prompt,
    request.frameCount > 1
      ? `${request.frameCount}-frame ${request.pose} animation sprite sheet, invisible grid, left to right then top to bottom`
      : `single ${request.pose} pose`,
    request.frameCount > 1
      ? 'same full body pet in every cell, equal cells, no labels or borders, clean background, consistent character design'
      : 'full body pet sprite, centered, clean background, consistent character design'
  ].filter(Boolean).join(', ')
}

function generationActive(token: number): boolean {
  return token === generationToken.value
}

async function createGeneratedFrames(request: GenerationRequest, token: number): Promise<PetStudioFrame[] | null> {
  generationStep.value = 'generate'
  const generated = await generatePetStudioImage(
    props.petId,
    { platform: request.platform, providerId: request.providerId, modelId: request.modelId },
    generationPrompt(request),
    request.references
  )
  if (!generationActive(token)) return null
  generatedPreview.value = imageDataUrl(generated.data, generated.mediaType)

  phase.generating = false
  phase.processing = true
  const sources: Array<{ data: string; mediaType: string }> = request.frameCount > 1
    ? (generationStep.value = 'split', await splitPetStudioActionSheet(generated.data, request.frameCount)).frames.map((frame) => ({ data: frame.data, mediaType: 'image/png' }))
    : [{ data: generated.data, mediaType: generated.mediaType }]
  if (!generationActive(token)) return null

  const result: PetStudioFrame[] = []
  for (const source of sources) {
    if (!generationActive(token)) return null
    generationStep.value = request.chromaKey ? 'chroma' : 'normalize'
    const normalized = await processPetStudioFrame(source.data, {
      chromaKey: request.chromaKey,
      keyColor: request.keyColor,
      targetHeight: request.targetHeight
    })
    if (!generationActive(token)) return null
    generationStep.value = 'normalize'
    const dataUrl = imageDataUrl(normalized)
    result.push(createGeneratedPetStudioFrame({
      id: nextFrameId('generated'),
      dataUrl,
      durationMs: 240,
      geometry: await readImageGeometry(dataUrl, request.targetHeight)
    }))
  }
  return result
}

function applyGeneratedFrames(request: GenerationRequest, frames: PetStudioFrame[], kind: GenerationKind): void {
  if (kind === 'replace') {
    const firstId = project.value.animations[request.pose]?.frames[0]?.id ?? frames[0]?.id ?? null
    dispatchProject({ type: 'replace-animation-frames', pose: request.pose, frames })
    studio.setSession({ selectedPose: request.pose, selectedFrameId: firstId })
    return
  }
  const afterId = state.selectedPose === request.pose ? state.selectedFrameId ?? undefined : undefined
  dispatchProject({ type: 'append-frames', pose: request.pose, frames, afterId })
  studio.setSession({ selectedPose: request.pose, selectedFrameId: frames[0]?.id ?? null })
}

async function startGeneration(job: GenerationJob): Promise<void> {
  if (busy.value || !job.poses.length) return
  clearMessages()
  const token = ++generationToken.value
  lastGenerationJob.value = job
  generationStatus.value = 'running'
  generationStep.value = 'generate'
  phase.generating = true
  phase.processing = false
  try {
    for (const pose of job.poses) {
      if (!generationActive(token)) return
      const animation = project.value.animations[pose]
      const request = job.kind === 'batch'
        ? { ...job.request, pose, frameCount: clampFrameCount(animation?.frames.length ?? 1) }
        : { ...job.request, pose }
      const frames = await createGeneratedFrames(request, token)
      if (!frames || !generationActive(token)) return
      if (job.kind === 'replace' && frames.length !== (animation?.frames.length ?? 0)) {
        throw new Error(text('pet.studio.restyleFrameCountMismatch', undefined, '整动作重生成返回的帧数与原动作不一致。'))
      }
      applyGeneratedFrames(request, frames, job.kind === 'batch' ? 'replace' : job.kind)
    }
    if (!generationActive(token)) return
    generationStatus.value = 'success'
    generationStep.value = 'idle'
    notice.value = text('pet.studio.generated', undefined, '生成结果已加入当前草稿。')
  } catch (error) {
    if (!generationActive(token)) return
    generationStatus.value = 'error'
    generationStep.value = 'idle'
    errorMessage.value = errorOf(error)
  } finally {
    if (generationActive(token)) {
      phase.generating = false
      phase.processing = false
    }
  }
}

function validateGenerationRequest(request: GenerationRequest): boolean {
  if (!request.subject || !request.providerId || !request.modelId) {
    errorMessage.value = text('pet.studio.validation', undefined, '请输入 subject、provider 和 model。')
    return false
  }
  return true
}

function generate(): void {
  const request = buildGenerationRequest(state.selectedPose)
  if (!validateGenerationRequest(request)) return
  void startGeneration({ kind: 'append', request, poses: [request.pose] })
}

function regenerateAction(): void {
  const frames = currentFrames.value
  if (!frames.length) {
    errorMessage.value = text('pet.studio.noFrames', undefined, '当前动作还没有帧。')
    return
  }
  const request = buildGenerationRequest(state.selectedPose, frames.length)
  if (!validateGenerationRequest(request)) return
  void startGeneration({ kind: 'replace', request, poses: [request.pose] })
}

function batchRestyle(): void {
  const poses = restyleActions.value.filter((pose) => Boolean(project.value.animations[pose]?.frames.length))
  if (!poses.length) {
    errorMessage.value = text('pet.studio.selectRestyleActions', undefined, '请先选择至少一个有帧的动作。')
    return
  }
  const request = buildGenerationRequest(poses[0], project.value.animations[poses[0]]?.frames.length ?? 1)
  if (!validateGenerationRequest(request)) return
  void startGeneration({ kind: 'batch', request, poses })
}

function retryGeneration(): void {
  if (!lastGenerationJob.value) return
  void startGeneration(lastGenerationJob.value)
}

function cancelGeneration(): void {
  if (!generationRunning.value) return
  generationToken.value += 1
  phase.generating = false
  phase.processing = false
  generationStatus.value = 'cancelled'
  generationStep.value = 'idle'
  errorMessage.value = ''
  notice.value = text('pet.studio.generationCancelled', undefined, '已取消生成；迟到的 Wails 结果不会写入草稿。')
}

function openFramePicker(mode: FrameFileMode): void {
  if (busy.value) return
  frameFileMode.value = mode
  frameFileInput.value?.click()
}

async function onFrameFileSelected(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file || busy.value) return
  clearMessages()
  phase.processing = true
  try {
    const loaded = await readFileAsDataUrl(file)
    const geometry = await readImageGeometry(loaded.dataUrl, generation.targetHeight)
    const frame = createGeneratedPetStudioFrame({ id: nextFrameId('file'), dataUrl: loaded.dataUrl, geometry, durationMs: 240 })
    if (frameFileMode.value === 'replace' && selectedFrame.value) {
      dispatchProject({ type: 'replace-frame', pose: state.selectedPose, frameId: selectedFrame.value.id, frame })
      framePreviews[selectedFrame.value.id] = frame.source.kind === 'file' ? frame.source.dataUrl : loaded.dataUrl
    } else {
      dispatchProject({ type: 'append-frame', pose: state.selectedPose, frame, afterId: state.selectedFrameId ?? undefined })
      framePreviews[frame.id] = loaded.dataUrl
      studio.setSession({ selectedFrameId: frame.id })
    }
    notice.value = text('pet.studio.frameAdded', undefined, '图片帧已加入当前草稿。')
  } catch (error) {
    errorMessage.value = errorOf(error)
  } finally {
    phase.processing = false
  }
}

function openReferencePicker(): void {
  if (busy.value || referenceImages.value.length >= 3) return
  referenceFileInput.value?.click()
}

async function onReferenceFilesSelected(event: Event): Promise<void> {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  input.value = ''
  if (!files.length || busy.value) return
  phase.processing = true
  clearMessages()
  try {
    const remaining = 3 - referenceImages.value.length
    const loaded = await Promise.all(files.slice(0, remaining).map(async (file) => {
      const result = await readFileAsDataUrl(file)
      return { data: result.dataUrl, mediaType: result.mediaType, preview: result.dataUrl }
    }))
    studio.setReferenceImages([...state.referenceImages, ...loaded])
    notice.value = text('pet.studio.referenceAdded', { count: referenceImages.value.length }, '已添加 {count} 张参考图。')
  } catch (error) {
    errorMessage.value = errorOf(error)
  } finally {
    phase.processing = false
  }
}

function removeReference(index: number): void {
  if (busy.value) return
  studio.setReferenceImages(state.referenceImages.filter((_, currentIndex) => currentIndex !== index))
}

function setAnimationLabel(value: string): void {
  dispatchProject({ type: 'set-animation-label', pose: state.selectedPose, label: value })
}

function setAnimationDescription(value: string): void {
  dispatchProject({ type: 'set-animation-description', pose: state.selectedPose, description: value })
}

function setAnimationLoop(loop: boolean): void {
  if (!currentAnimation.value) return
  dispatchProject({ type: 'set-animation', pose: state.selectedPose, animation: { ...currentAnimation.value, loop } })
}

function setSelectedDuration(value: number): void {
  if (!selectedFrame.value) return
  dispatchProject({ type: 'set-duration', pose: state.selectedPose, frameId: selectedFrame.value.id, durationMs: Math.floor(Number(value) || 240) })
}

function moveSelectedFrame(direction: -1 | 1): void {
  if (!selectedFrame.value) return
  dispatchProject({ type: 'move-frame', pose: state.selectedPose, frameId: selectedFrame.value.id, direction })
}

function deleteSelectedFrame(): void {
  if (!selectedFrame.value) return
  if (state.selectedPose === 'idle' && currentFrames.value.length === 1) {
    errorMessage.value = text('pet.studio.idleMustKeepFrame', undefined, 'idle 至少需要保留一帧。')
    return
  }
  dispatchProject({ type: 'delete-frame', pose: state.selectedPose, frameId: selectedFrame.value.id })
}

function addAction(): void {
  const actionId = actionIdInput.value.trim()
  if (!/^[a-zA-Z][a-zA-Z0-9_-]*$/.test(actionId) || isBuiltinPetStudioAction(actionId) || actionIds.value.includes(actionId)) {
    errorMessage.value = text('pet.studio.invalidActionId', undefined, '动作 ID 必须以字母开头，且不能与已有或内置动作重复。')
    return
  }
  const seed = selectedFrame.value
  const frame = seed
    ? { ...seed, id: nextFrameId(`action-${actionId}`), source: { ...seed.source } }
    : createGeneratedPetStudioFrame({
        id: nextFrameId(`action-${actionId}`),
        dataUrl: 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=',
        geometry: { width: 1, height: 1, subjectBounds: { x: 0, y: 0, width: 1, height: 1 } },
        durationMs: 240
      })
  dispatchProject({ type: 'add-action', actionId, animation: { loop: true, frames: [frame] } })
  actionIdInput.value = ''
  selectPose(actionId)
  notice.value = text('pet.studio.actionAdded', undefined, '动作已加入草稿，请继续生成或替换它的帧。')
}

function deleteAction(actionId: PetStudioActionId): void {
  if (isBuiltinPetStudioAction(actionId) || actionId === 'idle') return
  if (isActionReferenced(actionId)) {
    errorMessage.value = text('pet.studio.actionReferenced', undefined, '该动作已被行为引用，先从行为集合中移除后才能删除。')
    return
  }
  if (typeof window !== 'undefined' && !window.confirm(text('pet.studio.deleteActionConfirm', undefined, '删除这个自定义动作？'))) return
  dispatchProject({ type: 'delete-action', actionId })
}

function toggleRestyleAction(actionId: PetStudioActionId, checked: boolean): void {
  if (checked && !restyleActions.value.includes(actionId)) restyleActions.value.push(actionId)
  if (!checked) restyleActions.value = restyleActions.value.filter((value) => value !== actionId)
}

function setBehaviorLabel(value: string): void {
  dispatchProject({ type: 'set-behavior-label', behaviorId: state.selectedBehaviorId, label: value })
}

function toggleBehaviorAction(actionId: PetStudioActionId, checked: boolean): void {
  const behavior = selectedBehavior.value
  if (!behavior) return
  const actions = checked
    ? [...behavior.actions, actionId]
    : behavior.actions.filter((action) => action !== actionId)
  if (!actions.length) {
    errorMessage.value = text('pet.studio.behaviorNeedsAction', undefined, '行为至少需要保留一个动作。')
    return
  }
  dispatchProject({ type: 'set-behavior-actions', behaviorId: state.selectedBehaviorId, actions: [...new Set(actions)] })
}

function moveBehaviorAction(index: number, direction: -1 | 1): void {
  const behavior = selectedBehavior.value
  if (!behavior) return
  const target = index + direction
  if (target < 0 || target >= behavior.actions.length) return
  const actions = [...behavior.actions]
  ;[actions[index], actions[target]] = [actions[target], actions[index]]
  dispatchProject({ type: 'set-behavior-actions', behaviorId: state.selectedBehaviorId, actions })
}

function addBehavior(): void {
  const behaviorId = behaviorIdInput.value.trim()
  const firstAction = existingActionIds.value[0]
  if (!firstAction || !/^[a-zA-Z][a-zA-Z0-9_-]*$/.test(behaviorId) || behaviorIds.value.includes(behaviorId)) {
    errorMessage.value = text('pet.studio.invalidBehaviorId', undefined, '请提供唯一的行为 ID，并先至少生成一个动作。')
    return
  }
  dispatchProject({ type: 'add-behavior', behaviorId, behavior: { actions: [firstAction] } })
  behaviorIdInput.value = ''
  studio.setSession({ selectedBehaviorId: behaviorId })
}

function deleteBehavior(): void {
  if (isBuiltinBehavior(state.selectedBehaviorId)) return
  dispatchProject({ type: 'delete-behavior', behaviorId: state.selectedBehaviorId })
  studio.setSession({ selectedBehaviorId: behaviorIds.value[0] ?? 'feed' })
}

function frameInputFromProject(projectToPack: PetStudioProject, pose: PetStudioActionId, frame: PetStudioFrame): PetStudioFrameInput {
  if (frame.source.kind === 'file') {
    return { data: frame.source.dataUrl, durationMs: frame.durationMs }
  }
  const atlasSrc = projectToPack.source.atlas?.src
  const atlasFrame = getPetStudioAtlasFrame(projectToPack, frame.source.pose, frame.source.frameIndex)
  if (!atlasSrc || !atlasFrame) throw new Error(text('pet.studio.atlasFrameMissing', undefined, '无法找到 atlas 帧数据，不能保存。'))
  return {
    data: atlasSrc,
    durationMs: frame.durationMs,
    crop: { x: atlasFrame.x, y: atlasFrame.y, width: atlasFrame.width, height: atlasFrame.height }
  }
}

function actionInputsFromProject(projectToPack: PetStudioProject): PetStudioActionInput[] {
  return getPetStudioActionIds(projectToPack.animations).flatMap((pose) => {
    const animation = projectToPack.animations[pose]
    if (!animation?.frames.length) return []
    return [{
      id: pose,
      loop: animation.loop,
      label: animation.label,
      description: animation.description,
      frames: animation.frames.map((frame) => frameInputFromProject(projectToPack, pose, frame))
    }]
  })
}

async function packProject(projectToPack: PetStudioProject): Promise<{ data: string; mediaType: string; manifest: Record<string, unknown> }> {
  if (!projectToPack.animations.idle?.frames.length) {
    throw new Error(text('pet.studio.packFirst', undefined, '请先生成并保留至少一帧 idle。'))
  }
  const metadata = {
    subject: projectToPack.subject.trim(),
    modelId: projectToPack.modelId.trim(),
    ...(projectToPack.createdAt ? { createdAt: projectToPack.createdAt } : {}),
    ...(projectToPack.updatedAt ? { updatedAt: projectToPack.updatedAt } : {}),
    ...(projectToPack.builtin !== undefined ? { builtin: projectToPack.builtin } : {}),
    ...(projectToPack.assetVersion !== undefined ? { assetVersion: projectToPack.assetVersion } : {}),
    ...(projectToPack.spriteNormalizationVersion !== undefined ? { spriteNormalizationVersion: projectToPack.spriteNormalizationVersion } : {})
  }
  const result = await packPetStudioAtlas(projectToPack.name.trim() || 'Pet', {}, {
    actions: actionInputsFromProject(projectToPack),
    behaviors: toPetStudioPackBehaviors(projectToPack),
    metadata,
    ...metadata
  })
  return { data: result.data, mediaType: result.mediaType, manifest: result.manifest as unknown as Record<string, unknown> }
}

async function packDraft(): Promise<void> {
  if (busy.value || !canPack.value) return
  clearMessages()
  phase.packing = true
  generationStep.value = 'pack'
  try {
    const result = await packProject(project.value)
    packedData.value = result.data
    atlasPreview.value = imageDataUrl(result.data, result.mediaType)
    packedManifest.value = result.manifest
    notice.value = text('pet.studio.packed', undefined, 'atlas 已生成，可保存皮肤。')
  } catch (error) {
    errorMessage.value = errorOf(error)
  } finally {
    phase.packing = false
    generationStep.value = 'idle'
  }
}

function safeCopySkinId(name: string): string {
  const slug = name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'skin'
  return `studio-${slug}-${Date.now().toString(36)}`
}

async function saveSkin(mode: SaveMode, bind: boolean): Promise<void> {
  if (busy.value || !canSave.value) return
  const skinId = mode === 'update' ? project.value.source.skinId?.trim() : safeCopySkinId(project.value.name)
  if (!skinId) {
    errorMessage.value = text('pet.studio.updateUnavailable', undefined, '当前来源不支持原地更新，请选择另存副本。')
    return
  }
  clearMessages()
  const revision = studio.beginSave()
  if (revision === null) return
  phase.saving = true
  saveMode.value = mode
  saveBind.value = bind
  const now = Date.now()
  const saveProjectDraft: PetStudioProject = {
    ...project.value,
    createdAt: project.value.createdAt ?? now,
    updatedAt: now,
    // 另存副本必须切断内置资源身份，否则新记录会被 UI 当成不可更新的内置皮肤。
    builtin: mode === 'copy' ? false : project.value.builtin
  }
  try {
    const packed = await packProject(saveProjectDraft)
    packedData.value = packed.data
    atlasPreview.value = imageDataUrl(packed.data, 'image/png')
    packedManifest.value = packed.manifest
    await savePetStudioSkin(props.petId, {
      skinId,
      name: saveProjectDraft.name.trim() || 'Pet Studio',
      subject: saveProjectDraft.subject.trim(),
      modelId: saveProjectDraft.modelId.trim(),
      atlas: packed.data,
      manifestJson: packed.manifest,
      bind
    })

    // 保存成功后必须重新 ReadSkin，确保 baseline 使用后端最终 manifest，而不是本地 pack 的猜测。
    const loaded = await readPetStudioAtlas(props.petId, { skinId })
    const imported = createImportedPetStudioProject(loaded.atlas, loaded.skin)
    if (imported.source.kind !== 'skin') {
      imported.source = { kind: 'skin', skinId, canUpdate: true, atlas: loaded.atlas }
    }
    if (studio.commitSavedProject(revision, imported, skinId)) {
      const firstPose = getPetStudioActionIds(imported.animations)[0] ?? 'idle'
      studio.setSession({ selectedPose: firstPose, selectedFrameId: imported.animations[firstPose]?.frames[0]?.id ?? null })
      sourceChoice.value = skinId
      clearFramePreviews()
      resetPackedPreview()
      await refreshFramePreviews()
    }
    await loadSkins(false)
    if (bind) {
      try {
        await petApi.getSnapshot(props.petId)
      } catch (error) {
        // 皮肤已经持久化；运行时刷新失败单独提示，不能把已保存结果说成失败。
        notice.value = text('pet.studio.savedRuntimeRefreshFailed', { error: errorOf(error) }, '皮肤已保存，但运行时刷新失败：{error}')
      }
    }
    if (!notice.value) notice.value = text(bind ? 'pet.studio.savedBound' : 'pet.studio.saved', undefined, bind ? '皮肤已保存并绑定。' : '皮肤已保存。')
  } catch (error) {
    errorMessage.value = errorOf(error)
    notice.value = text('pet.studio.savedButReloadFailed', undefined, '保存请求可能已经到达后端，但重新读取来源失败，请刷新皮肤列表确认。')
    studio.finishSave(revision)
  } finally {
    phase.saving = false
    studio.finishSave(revision)
  }
}

async function removeSkin(skin: PetStudioSkinRecord): Promise<void> {
  if (busy.value || skin.builtin) return
  if (skin.skinId === state.sourceSelection) {
    errorMessage.value = text('pet.studio.deleteCurrentSource', undefined, '当前正在编辑这套皮肤，请先切换来源再删除。')
    return
  }
  if (typeof window !== 'undefined' && !window.confirm(text('pet.studio.deleteConfirm', undefined, '删除这套皮肤？'))) return
  clearMessages()
  phase.deleting = true
  try {
    await deletePetStudioSkin(props.petId, skin.skinId)
    notice.value = text('pet.studio.deleted', undefined, '皮肤已删除。')
    await loadSkins(false)
  } catch (error) {
    errorMessage.value = errorOf(error)
  } finally {
    phase.deleting = false
  }
}

function formatDate(value: number | undefined): string {
  if (!value) return text('pet.studio.notAvailable', undefined, '未设置')
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(value)
}

watch(() => state.selectedPose, (pose) => {
  if (!restyleActions.value.includes(pose) && project.value.animations[pose]?.frames.length) restyleActions.value.push(pose)
})

watch(() => project.value.source.atlas?.src, () => {
  void refreshFramePreviews()
})

onMounted(() => {
  void (async () => {
    // 默认 atlas 是 Studio 的可视化基线；首次进入也必须走与手动切换相同的读取链路，
    // 否则用户看到的是空白“新建”项目，直到主动操作来源下拉框才会出现原版形象。
    await loadSkins()
    if (state.sourceSelection === 'new') await changeSource('default')
  })()
})
</script>

<template>
  <section class="pet-studio">
    <header class="studio-header">
      <div class="studio-header__identity">
        <span class="studio-eyebrow">{{ text('pet.studio.eyebrow', undefined, 'PET STUDIO') }}</span>
        <div class="studio-title-row">
          <h2>{{ text('pet.studio.title', undefined, '形象工坊') }}</h2>
          <span class="studio-status" :class="{ 'is-busy': busy, 'is-dirty': studio.dirty.value }">
            {{ busy ? text('pet.studio.busy', undefined, '处理中') : studio.dirty.value ? text('pet.studio.dirty', undefined, '未保存') : text('pet.studio.ready', undefined, '就绪') }}
          </span>
        </div>
        <p>{{ text('pet.studio.subtitle', undefined, '生成、处理并管理桌宠皮肤。') }}</p>
      </div>
      <div class="studio-header__source">
        <label class="field field--compact">
          <span>{{ text('pet.studio.sourceLabel', undefined, '来源') }}</span>
          <select :value="sourceChoice" :disabled="busy" @change="changeSource(sourceValue($event))">
            <option value="new">{{ sourceLabel('new') }}</option>
            <option value="default">{{ sourceLabel('default') }}</option>
            <option v-for="skin in skins" :key="skin.skinId" :value="skin.skinId">
              {{ skin.name || skin.skinId }}{{ skin.builtin ? ` · ${text('pet.studio.builtin', undefined, '内置')}` : '' }}
            </option>
          </select>
        </label>
        <button class="icon-button" type="button" :disabled="busy" :title="text('pet.common.refresh', undefined, '刷新')" @click="loadSkins()">↻</button>
      </div>
    </header>

    <div v-if="errorMessage" class="studio-message studio-message--error" role="alert">{{ errorMessage }}</div>
    <div v-if="notice" class="studio-message" role="status">{{ notice }}</div>

    <div class="studio-toolbar">
      <div class="studio-toolbar__source-copy">
        <span class="source-kicker">{{ sourceLabel(project.source.kind) }}</span>
        <strong>{{ project.name || text('pet.studio.untitled', undefined, '未命名皮肤') }}</strong>
        <span v-if="project.source.skinId" class="source-id">{{ project.source.skinId }}</span>
      </div>
      <div class="studio-toolbar__actions">
        <span v-if="generationRunning" class="operation-label">{{ stepLabel(generationStep) }}</span>
        <button v-if="generationRunning" class="button button--danger" type="button" @click="cancelGeneration">
          {{ text('pet.studio.cancel', undefined, '取消生成') }}
        </button>
        <button v-else-if="generationStatus === 'error' || generationStatus === 'cancelled'" class="button button--quiet" type="button" :disabled="busy || !lastGenerationJob" @click="retryGeneration">
          {{ text('pet.studio.retry', undefined, '重试') }}
        </button>
        <button class="button button--quiet" type="button" :disabled="busy || !canPack" @click="packDraft">
          {{ text('pet.studio.pack', undefined, '打包 Atlas') }}
        </button>
      </div>
    </div>

    <div class="studio-layout">
      <aside class="studio-sidebar">
        <div class="sidebar-heading">
          <div>
            <span class="section-kicker">{{ text('pet.studio.actionKicker', undefined, 'TIMELINE') }}</span>
            <h3>{{ text('pet.studio.actionsTitle', undefined, '动作') }}</h3>
          </div>
          <span class="count-badge">{{ existingActionIds.length }}</span>
        </div>

        <div class="action-list">
          <div v-for="action in actionIds" :key="action" class="action-row" :class="{ 'is-selected': state.selectedPose === action }">
            <button class="action-row__select" type="button" :disabled="busy" @click="selectPose(action)">
              <span class="action-row__dot" :class="{ 'is-empty': !project.animations[action]?.frames.length }"></span>
              <span class="action-row__name">{{ actionLabel(action) }}</span>
              <span class="action-row__count">{{ project.animations[action]?.frames.length ?? 0 }}</span>
            </button>
            <button v-if="!isBuiltinPetStudioAction(action) && action !== 'idle'" class="action-row__delete" type="button" :disabled="busy" :title="text('pet.common.delete', undefined, '删除')" @click="deleteAction(action)">×</button>
          </div>
          <div v-if="!existingActionIds.length" class="empty-hint">{{ text('pet.studio.newActionHint', undefined, '先为 idle 生成一帧，动作列表会在这里展开。') }}</div>
        </div>

        <div class="sidebar-add">
          <input v-model="actionIdInput" :disabled="busy" :placeholder="text('pet.studio.actionIdPlaceholder', undefined, 'custom-action')" @keyup.enter="addAction" />
          <button class="button button--small" type="button" :disabled="busy" @click="addAction">{{ text('pet.studio.addAction', undefined, '新增动作') }}</button>
        </div>

        <div class="sidebar-footnote">
          <span>{{ text('pet.studio.sourceNote', undefined, '只保存 data URL 与受控 skinId，不暴露本地路径。') }}</span>
        </div>
      </aside>

      <main class="studio-main">
        <nav class="studio-tabs" aria-label="Studio sections">
          <button type="button" :class="{ 'is-active': state.editorMode === 'actions' }" :disabled="busy" @click="studio.setSession({ editorMode: 'actions' })">{{ text('pet.studio.actionsTab', undefined, '动作编辑') }}</button>
          <button type="button" :class="{ 'is-active': state.editorMode === 'behaviors' }" :disabled="busy" @click="studio.setSession({ editorMode: 'behaviors' })">{{ text('pet.studio.behaviorsTab', undefined, '行为编排') }}</button>
        </nav>

        <template v-if="state.editorMode === 'actions'">
          <section class="preview-panel">
            <div class="panel-heading">
              <div>
                <span class="section-kicker">{{ actionLabel(state.selectedPose) }}</span>
                <h3>{{ text('pet.studio.previewTitle', undefined, '动作预览') }}</h3>
              </div>
              <span v-if="currentAnimation" class="muted-copy">{{ currentFrames.length }} / 8 {{ text('pet.studio.framesUnit', undefined, '帧') }}</span>
            </div>
            <div class="preview-stage">
              <div class="preview-stage__grid"></div>
              <img v-if="selectedFramePreview" :src="selectedFramePreview" :alt="text('pet.studio.frameAlt', { action: actionLabel(state.selectedPose), index: selectedFrameIndex + 1 }, '动作帧')" />
              <img v-else-if="generatedPreview" :src="generatedPreview" :alt="text('pet.studio.generatedPreviewAlt', undefined, '生成的宠物预览')" />
              <img v-else-if="project.source.atlas?.src" :src="project.source.atlas.src" :alt="text('pet.studio.atlasAlt', undefined, '宠物 atlas 预览')" />
              <div v-else class="preview-stage__empty">
                <span class="preview-stage__empty-mark">+</span>
                <strong>{{ text('pet.studio.emptyPreview', undefined, '生成图片后在此预览') }}</strong>
                <small>{{ text('pet.studio.emptyPreviewHint', undefined, '右侧生成设置会把结果写入当前草稿。') }}</small>
              </div>
              <span v-if="generationRunning" class="preview-stage__loading">{{ stepLabel(generationStep) }}</span>
            </div>
          </section>

          <section class="timeline-panel">
            <div class="panel-heading panel-heading--wrap">
              <div>
                <span class="section-kicker">{{ text('pet.studio.frameTimeline', undefined, 'FRAME TIMELINE') }}</span>
                <h3>{{ text('pet.studio.framesTitle', undefined, '帧序列') }}</h3>
              </div>
              <div class="timeline-actions">
                <button class="button button--small button--quiet" type="button" :disabled="busy || currentFrames.length >= 8" @click="openFramePicker('append')">＋ {{ text('pet.studio.appendFrame', undefined, '追加帧') }}</button>
                <button class="button button--small button--quiet" type="button" :disabled="busy || !selectedFrame" @click="openFramePicker('replace')">↺ {{ text('pet.studio.replaceFrame', undefined, '替换当前帧') }}</button>
              </div>
            </div>
            <div v-if="currentFrames.length" class="frame-strip">
              <button v-for="(frame, index) in currentFrames" :key="frame.id" class="frame-card" :class="{ 'is-selected': selectedFrame?.id === frame.id }" type="button" :disabled="busy" @click="selectFrame(frame.id)">
                <span class="frame-card__number">{{ String(index + 1).padStart(2, '0') }}</span>
                <img :src="framePreview(frame)" :alt="text('pet.studio.frameAlt', { action: actionLabel(state.selectedPose), index: index + 1 }, '动作帧')" />
                <span class="frame-card__duration">{{ frame.durationMs }}ms</span>
              </button>
              <button v-if="currentFrames.length < 8" class="frame-card frame-card--add" type="button" :disabled="busy" @click="openFramePicker('append')"><span>＋</span><small>{{ text('pet.studio.addFrame', undefined, '添加') }}</small></button>
            </div>
            <div v-else class="timeline-empty">{{ text('pet.studio.noFrames', undefined, '当前动作还没有帧。') }}</div>
            <input ref="frameFileInput" class="visually-hidden" type="file" accept="image/*" :disabled="busy" @change="onFrameFileSelected" />
          </section>

          <section class="action-detail-grid">
            <div class="detail-section">
              <div class="section-title-row"><h3>{{ text('pet.studio.actionMetadata', undefined, '动作元数据') }}</h3><span class="muted-copy">{{ state.selectedPose }}</span></div>
              <label class="field"><span>{{ text('pet.studio.animationLabel', undefined, '动作标签') }}</span><input :value="currentAnimation?.label ?? ''" :disabled="busy || !currentAnimation" :placeholder="actionLabel(state.selectedPose)" @input="setAnimationLabel(($event.target as HTMLInputElement).value)" /></label>
              <label class="field"><span>{{ text('pet.studio.animationDescription', undefined, '动作描述') }}</span><textarea :value="currentAnimation?.description ?? ''" :disabled="busy || !currentAnimation" rows="2" :placeholder="text('pet.studio.animationDescriptionPlaceholder', undefined, '这个动作在什么时候播放？')" @input="setAnimationDescription(($event.target as HTMLTextAreaElement).value)" /></label>
              <label class="toggle-line"><input type="checkbox" :checked="currentAnimation?.loop !== false" :disabled="busy || !currentAnimation" @change="setAnimationLoop(($event.target as HTMLInputElement).checked)" /><span>{{ text('pet.studio.loop', undefined, '循环播放') }}</span></label>
            </div>
            <div class="detail-section detail-section--selected">
              <div class="section-title-row"><h3>{{ text('pet.studio.selectedFrame', undefined, '选中帧') }}</h3><span v-if="selectedFrame" class="muted-copy">{{ selectedFrameIndex + 1 }} / {{ currentFrames.length }}</span></div>
              <div v-if="selectedFrame" class="selected-frame-controls">
                <div class="selected-frame-preview"><img :src="selectedFramePreview" :alt="text('pet.studio.selectedFrame', undefined, '选中帧')" /></div>
                <div class="selected-frame-fields">
                  <label class="field"><span>{{ text('pet.studio.duration', undefined, 'Duration (ms)') }}</span><input type="number" min="16" max="60000" step="16" :value="selectedFrame.durationMs" :disabled="busy" @change="setSelectedDuration(Number(($event.target as HTMLInputElement).value))" /></label>
                  <div class="frame-move-actions"><button class="icon-button" type="button" :disabled="busy || selectedFrameIndex <= 0" :title="text('pet.studio.moveLeft', undefined, '向前移动')" @click="moveSelectedFrame(-1)">←</button><button class="icon-button" type="button" :disabled="busy || selectedFrameIndex < 0 || selectedFrameIndex >= currentFrames.length - 1" :title="text('pet.studio.moveRight', undefined, '向后移动')" @click="moveSelectedFrame(1)">→</button><button class="icon-button icon-button--danger" type="button" :disabled="busy || (state.selectedPose === 'idle' && currentFrames.length === 1)" :title="text('pet.common.delete', undefined, '删除')" @click="deleteSelectedFrame">×</button></div>
                  <small class="frame-source">{{ selectedFrame.source.kind === 'atlas' ? text('pet.studio.atlasFrame', undefined, '来自已加载 atlas，保存时按 crop 处理') : text('pet.studio.generatedFrame', undefined, '草稿图片帧') }}</small>
                </div>
              </div>
              <div v-else class="empty-hint">{{ text('pet.studio.selectFrameHint', undefined, '生成或追加一帧后，可以在这里编辑时长和顺序。') }}</div>
            </div>
          </section>

          <section class="restyle-panel">
            <div class="panel-heading panel-heading--wrap">
              <div><span class="section-kicker">{{ text('pet.studio.restyleKicker', undefined, 'RESTYLE') }}</span><h3>{{ text('pet.studio.restyleTitle', undefined, '重生成') }}</h3></div>
              <div class="timeline-actions"><button class="button button--small" type="button" :disabled="busy || !currentFrames.length" @click="regenerateAction">{{ text('pet.studio.regenerateAction', undefined, '整动作重生成') }}</button><button class="button button--small button--quiet" type="button" :disabled="busy || !selectedRestyleCount" @click="batchRestyle">{{ text('pet.studio.batchRestyle', undefined, '批量 Restyle') }} · {{ selectedRestyleCount }}</button></div>
            </div>
            <div class="restyle-options"><label v-for="action in existingActionIds" :key="`restyle-${action}`" class="check-chip"><input type="checkbox" :checked="restyleActions.includes(action)" :disabled="busy" @change="toggleRestyleAction(action, ($event.target as HTMLInputElement).checked)" /><span>{{ actionLabel(action) }}</span></label></div>
          </section>
        </template>

        <section v-else class="behavior-editor">
          <div class="panel-heading panel-heading--wrap"><div><span class="section-kicker">{{ text('pet.studio.behaviorKicker', undefined, 'BEHAVIOR MAP') }}</span><h3>{{ text('pet.studio.behaviorsTitle', undefined, '行为动作集合') }}</h3></div><span class="muted-copy">{{ behaviorIds.length }} {{ text('pet.studio.behaviorsUnit', undefined, '组') }}</span></div>
          <div class="behavior-layout">
            <div class="behavior-list">
              <button v-for="behaviorId in behaviorIds" :key="behaviorId" type="button" :class="{ 'is-selected': state.selectedBehaviorId === behaviorId }" :disabled="busy" @click="studio.setSession({ selectedBehaviorId: behaviorId })"><span>{{ behaviorLabel(behaviorId) }}</span><small>{{ project.behaviors[behaviorId].actions.length }}</small></button>
              <div class="sidebar-add behavior-add"><input v-model="behaviorIdInput" :disabled="busy" :placeholder="text('pet.studio.behaviorIdPlaceholder', undefined, 'morning-routine')" @keyup.enter="addBehavior" /><button class="button button--small" type="button" :disabled="busy" @click="addBehavior">{{ text('pet.studio.addBehavior', undefined, '新增行为') }}</button></div>
            </div>
            <div v-if="selectedBehavior" class="behavior-detail">
              <div class="section-title-row"><div><h3>{{ behaviorLabel(state.selectedBehaviorId) }}</h3><span class="muted-copy">{{ state.selectedBehaviorId }}</span></div><button v-if="!isBuiltinBehavior(state.selectedBehaviorId)" class="button button--small button--danger" type="button" :disabled="busy" @click="deleteBehavior">{{ text('pet.common.delete', undefined, '删除') }}</button></div>
              <label class="field"><span>{{ text('pet.studio.behaviorLabel', undefined, '行为标签') }}</span><input :value="selectedBehavior.label ?? ''" :disabled="busy" :placeholder="behaviorLabel(state.selectedBehaviorId)" @input="setBehaviorLabel(($event.target as HTMLInputElement).value)" /></label>
              <div class="behavior-actions-block"><span class="field-label">{{ text('pet.studio.behaviorActions', undefined, '动作集合') }}</span><div class="behavior-action-checks"><label v-for="action in availableBehaviorActions" :key="`behavior-${action}`" class="check-chip"><input type="checkbox" :checked="selectedBehavior.actions.includes(action)" :disabled="busy" @change="toggleBehaviorAction(action, ($event.target as HTMLInputElement).checked)" /><span>{{ actionLabel(action) }}</span></label></div></div>
              <div class="behavior-order"><div class="section-title-row"><span class="field-label">{{ text('pet.studio.behaviorOrder', undefined, '播放顺序') }}</span><span class="muted-copy">{{ text('pet.studio.behaviorOrderHint', undefined, '按顺序尝试动作') }}</span></div><div v-for="(action, index) in selectedBehavior.actions" :key="`${state.selectedBehaviorId}-${action}`" class="behavior-order-row"><span class="order-index">{{ index + 1 }}</span><strong>{{ actionLabel(action) }}</strong><span class="behavior-order-spacer"></span><button class="icon-button" type="button" :disabled="busy || index === 0" @click="moveBehaviorAction(index, -1)">↑</button><button class="icon-button" type="button" :disabled="busy || index === selectedBehavior.actions.length - 1" @click="moveBehaviorAction(index, 1)">↓</button></div></div>
            </div>
            <div v-else class="empty-hint">{{ text('pet.studio.behaviorEmpty', undefined, '选择一组行为开始编排。') }}</div>
          </div>
        </section>
      </main>

      <aside class="studio-inspector">
        <section class="inspector-section">
          <div class="inspector-heading"><span class="section-kicker">{{ text('pet.studio.generationKicker', undefined, 'GENERATION') }}</span><h3>{{ text('pet.studio.generationSettings', undefined, '生成设置') }}</h3></div>
          <label class="field"><span>{{ text('pet.studio.platform', undefined, 'Platform') }}</span><input v-model="generation.platform" :disabled="busy" :placeholder="text('pet.studio.platformPlaceholder', undefined, 'openai')" /></label>
          <label class="field"><span>{{ text('pet.studio.provider', undefined, 'Provider') }}</span><input v-model="generation.providerId" :disabled="busy" :placeholder="text('pet.studio.providerPlaceholder', undefined, 'provider-id')" /></label>
          <label class="field"><span>{{ text('pet.studio.model', undefined, 'Model') }}</span><input v-model="projectModelId" :disabled="busy" :placeholder="text('pet.studio.modelPlaceholder', undefined, 'image-model')" /></label>
          <label class="field"><span>{{ text('pet.studio.prompt', undefined, '生成补充说明') }}</span><textarea v-model="generation.prompt" :disabled="busy" rows="3" :placeholder="text('pet.studio.promptPlaceholder', undefined, '保持角色外观一致，突出当前动作。')" /></label>
          <div class="field-grid">
            <label class="field"><span>{{ text('pet.studio.frameCount', undefined, '生成帧数') }}</span><input v-model.number="generation.frameCount" type="number" min="1" max="8" :disabled="busy" /></label>
            <label class="field"><span>{{ text('pet.studio.targetHeight', undefined, '主体高度') }}</span><input v-model.number="generation.targetHeight" type="number" min="32" max="1024" :disabled="busy" /></label>
          </div>
          <div class="color-row"><label class="toggle-line"><input v-model="generation.chromaKey" type="checkbox" :disabled="busy" /><span>{{ text('pet.studio.chromaKey', undefined, '移除色键背景') }}</span></label><label class="color-field" :title="text('pet.studio.keyColor', undefined, 'Key color')"><input v-model="generation.keyColor" type="color" :disabled="busy || !generation.chromaKey" /><span>{{ generation.keyColor }}</span></label></div>
          <div class="generation-actions"><button class="button button--primary" type="button" :disabled="busy || !canGenerate" @click="generate">{{ text('pet.studio.generate', undefined, '生成图片') }} · {{ actionLabel(state.selectedPose) }}</button><button v-if="generationRunning" class="button button--danger" type="button" @click="cancelGeneration">{{ text('pet.studio.cancel', undefined, '取消') }}</button></div>
          <div class="step-tracker"><span v-for="step in ['generate', 'split', 'chroma', 'normalize']" :key="step" :class="{ 'is-active': generationStep === step, 'is-done': generationStatus === 'success' && generationStep === 'idle' }">{{ stepLabel(step) }}</span></div>
        </section>

        <section class="inspector-section">
          <div class="inspector-heading"><span class="section-kicker">{{ text('pet.studio.referenceKicker', undefined, 'REFERENCES') }}</span><h3>{{ text('pet.studio.referenceImages', undefined, '参考图') }} <small>{{ referenceImages.length }}/3</small></h3></div>
          <div class="reference-grid"><div v-for="(reference, index) in referenceImages" :key="`${reference.data}-${index}`" class="reference-thumb"><img :src="reference.preview" :alt="text('pet.studio.referenceAlt', { index: index + 1 }, '参考图')" /><button type="button" :disabled="busy" :title="text('pet.common.delete', undefined, '删除')" @click="removeReference(index)">×</button></div><button v-if="referenceImages.length < 3" class="reference-add" type="button" :disabled="busy" @click="openReferencePicker">＋<span>{{ text('pet.studio.addReference', undefined, '添加参考图') }}</span></button></div>
          <input ref="referenceFileInput" class="visually-hidden" type="file" accept="image/*" multiple :disabled="busy" @change="onReferenceFilesSelected" />
        </section>

        <section class="inspector-section">
          <div class="inspector-heading"><span class="section-kicker">{{ text('pet.studio.metadataKicker', undefined, 'METADATA') }}</span><h3>{{ text('pet.studio.metadata', undefined, '皮肤信息') }}</h3></div>
          <label class="field"><span>{{ text('pet.studio.name', undefined, '皮肤名称') }}</span><input v-model="projectName" :disabled="busy" :placeholder="text('pet.studio.namePlaceholder', undefined, 'My Pet')" /></label>
          <label class="field"><span>{{ text('pet.studio.subject', undefined, 'Subject') }}</span><textarea v-model="projectSubject" :disabled="busy" rows="3" :placeholder="text('pet.studio.subjectPlaceholder', undefined, '描述宠物外观、材质和识别特征')" /></label>
          <dl class="metadata-list"><div><dt>{{ text('pet.studio.sourceKind', undefined, '来源类型') }}</dt><dd>{{ sourceLabel(project.source.kind) }}</dd></div><div><dt>{{ text('pet.studio.createdAt', undefined, '创建时间') }}</dt><dd>{{ formatDate(project.createdAt) }}</dd></div><div><dt>{{ text('pet.studio.updatedAt', undefined, '更新时间') }}</dt><dd>{{ formatDate(project.updatedAt) }}</dd></div></dl>
        </section>

        <section class="inspector-section inspector-section--save">
          <div class="inspector-heading"><span class="section-kicker">{{ text('pet.studio.saveKicker', undefined, 'PERSIST') }}</span><h3>{{ text('pet.studio.saveTitle', undefined, '保存草稿') }}</h3></div>
          <label class="toggle-line"><input v-model="saveBind" type="checkbox" :disabled="busy" /><span>{{ text('pet.studio.bind', undefined, '保存后绑定') }}</span></label>
          <div class="save-mode-note">{{ canUpdate ? text('pet.studio.updateAvailable', undefined, '当前来源支持原地更新。') : text('pet.studio.copyOnly', undefined, '新工程或内置来源只能另存副本。') }}</div>
          <div class="save-actions"><button v-if="canUpdate" class="button button--quiet" type="button" :disabled="busy || !canSave" @click="saveSkin('update', saveBind)">{{ text('pet.studio.saveUpdate', undefined, '原地更新') }}</button><button class="button button--quiet" type="button" :disabled="busy || !canSave" @click="saveSkin('copy', false)">{{ text('pet.studio.saveCopy', undefined, '另存副本') }}</button><button class="button button--primary" type="button" :disabled="busy || !canSave" @click="saveSkin('copy', true)">{{ text('pet.studio.saveAndBind', undefined, '保存并绑定') }}</button></div>
          <div v-if="atlasPreview" class="packed-preview"><div class="packed-preview__heading"><span>{{ text('pet.studio.atlasPreview', undefined, 'Atlas 预览') }}</span><small>{{ packedManifest ? text('pet.studio.packReady', undefined, '已打包') : '' }}</small></div><img :src="atlasPreview" :alt="text('pet.studio.atlasAlt', undefined, '宠物 atlas 预览')" /></div>
        </section>
      </aside>
    </div>

    <section class="skins-section">
      <div class="panel-heading panel-heading--wrap"><div><span class="section-kicker">{{ text('pet.studio.libraryKicker', undefined, 'LIBRARY') }}</span><h3>{{ text('pet.studio.savedSkins', undefined, '已保存皮肤') }}</h3></div><button class="button button--small button--quiet" type="button" :disabled="busy" @click="loadSkins()">{{ text('pet.common.refresh', undefined, '刷新列表') }}</button></div>
      <div v-if="!skins.length" class="timeline-empty">{{ text('pet.studio.noSkins', undefined, '暂无自定义皮肤') }}</div>
      <div v-else class="skin-list"><div v-for="skin in skins" :key="skin.skinId" class="skin-row"><div><strong>{{ skin.name || skin.skinId }}</strong><span>{{ skin.skinId }}</span></div><span v-if="skin.builtin" class="skin-badge">{{ text('pet.studio.builtin', undefined, '内置') }}</span><button class="button button--small button--danger" type="button" :disabled="busy || skin.builtin" @click="removeSkin(skin)">{{ text('pet.common.delete', undefined, '删除') }}</button></div></div>
    </section>
  </section>
</template>

<style scoped>
.pet-studio { --studio-ink: var(--settings-ink, #1f2933); --studio-muted: var(--settings-muted, #758091); --studio-line: color-mix(in srgb, var(--settings-line, #d8dee6) 82%, transparent); --studio-surface: var(--settings-surface, #ffffff); --studio-soft: color-mix(in srgb, var(--settings-strong-surface, #f4f6f8) 78%, transparent); --studio-accent: var(--mac-accent, #0a84ff); --studio-green: #2d966d; --studio-red: #b84d4d; display: flex; flex-direction: column; gap: 14px; padding: 22px; color: var(--studio-ink); background: linear-gradient(145deg, color-mix(in srgb, var(--studio-soft) 88%, transparent), transparent 42%); }
.pet-studio * { box-sizing: border-box; }
.studio-header, .studio-toolbar, .panel-heading, .section-title-row, .sidebar-heading, .inspector-heading, .studio-header__source, .studio-title-row, .studio-toolbar__actions, .timeline-actions, .generation-actions, .save-actions, .color-row, .toggle-line, .skin-row, .skin-row > div, .packed-preview__heading { display: flex; align-items: center; }
.studio-header, .studio-toolbar, .panel-heading, .section-title-row, .sidebar-heading, .studio-header__source, .studio-title-row, .studio-toolbar__actions, .skin-row, .packed-preview__heading { justify-content: space-between; gap: 12px; }
.studio-header { min-height: 58px; }
.studio-eyebrow, .section-kicker, .source-kicker { display: block; color: var(--studio-accent); font-size: 10px; font-weight: 750; letter-spacing: .14em; line-height: 1.2; text-transform: uppercase; }
.studio-title-row { justify-content: flex-start; margin-top: 4px; }
.studio-header h2, .studio-header p, .panel-heading h3, .sidebar-heading h3, .inspector-heading h3, .section-title-row h3, .behavior-detail h3 { margin: 0; }
.studio-header h2 { font-size: 23px; letter-spacing: -.02em; }
.studio-header p { margin-top: 4px; color: var(--studio-muted); font-size: 12px; }
.studio-status, .count-badge, .skin-badge { border: 1px solid var(--studio-line); border-radius: 999px; color: var(--studio-muted); font-size: 10px; font-weight: 700; letter-spacing: .04em; padding: 4px 8px; white-space: nowrap; }
.studio-status.is-busy { color: #ad762e; }.studio-status.is-dirty { border-color: color-mix(in srgb, #d28a40 45%, var(--studio-line)); color: #ad6725; }
.studio-header__source { min-width: 260px; justify-content: flex-end; }.field--compact { min-width: 210px; }
.studio-message { border: 1px solid color-mix(in srgb, var(--studio-green) 32%, var(--studio-line)); border-radius: 7px; padding: 9px 11px; color: var(--studio-green); font-size: 12px; }.studio-message--error { border-color: color-mix(in srgb, var(--studio-red) 40%, var(--studio-line)); color: var(--studio-red); }
.studio-toolbar { min-height: 46px; border-block: 1px solid var(--studio-line); padding: 8px 0; }.studio-toolbar__source-copy { display: flex; align-items: baseline; flex-wrap: wrap; gap: 8px; }.studio-toolbar__source-copy strong { font-size: 13px; }.source-id, .muted-copy, .frame-source, .save-mode-note, .sidebar-footnote, .empty-hint, .timeline-empty, .metadata-list dd, .skin-row span, .operation-label { color: var(--studio-muted); font-size: 11px; }.source-id { font-family: ui-monospace, SFMono-Regular, Consolas, monospace; }
.studio-toolbar__actions, .timeline-actions, .generation-actions, .save-actions { flex-wrap: wrap; justify-content: flex-end; gap: 7px; }
.studio-layout { display: grid; grid-template-columns: 190px minmax(420px, 1fr) 300px; align-items: start; gap: 14px; }.studio-sidebar, .studio-main, .studio-inspector, .skins-section { min-width: 0; }.studio-sidebar, .studio-inspector { display: flex; flex-direction: column; gap: 12px; }.studio-sidebar { padding-right: 2px; }.studio-main { display: flex; flex-direction: column; gap: 12px; }
.studio-sidebar, .preview-panel, .timeline-panel, .action-detail-grid, .restyle-panel, .behavior-editor, .inspector-section, .skins-section { border: 1px solid var(--studio-line); background: color-mix(in srgb, var(--studio-surface) 90%, transparent); box-shadow: 0 8px 30px color-mix(in srgb, #243042 7%, transparent); }
.studio-sidebar { border: 1px solid var(--studio-line); border-radius: 9px; padding: 12px 10px; background: color-mix(in srgb, var(--studio-soft) 82%, transparent); }.preview-panel, .timeline-panel, .restyle-panel, .behavior-editor, .inspector-section, .skins-section { border-radius: 9px; padding: 14px; }.action-detail-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 1px; overflow: hidden; border-radius: 9px; background: var(--studio-line); }.detail-section { min-width: 0; padding: 14px; background: var(--studio-surface); }
.panel-heading, .sidebar-heading, .inspector-heading { min-height: 34px; }.panel-heading--wrap { flex-wrap: wrap; }.panel-heading h3, .sidebar-heading h3, .inspector-heading h3, .section-title-row h3 { margin-top: 3px; font-size: 14px; letter-spacing: -.01em; }.inspector-heading h3 small { color: var(--studio-muted); font-size: 10px; font-weight: 600; }
.studio-tabs { display: flex; border-bottom: 1px solid var(--studio-line); gap: 18px; }.studio-tabs button { position: relative; border: 0; padding: 7px 0 10px; background: transparent; color: var(--studio-muted); cursor: pointer; font: inherit; font-size: 12px; font-weight: 700; }.studio-tabs button.is-active { color: var(--studio-ink); }.studio-tabs button.is-active::after { position: absolute; right: 0; bottom: -1px; left: 0; height: 2px; background: var(--studio-accent); content: ''; }
.action-list { display: flex; flex-direction: column; gap: 3px; margin-top: 9px; }.action-row { display: flex; align-items: center; min-height: 34px; border: 1px solid transparent; border-radius: 6px; }.action-row.is-selected { border-color: color-mix(in srgb, var(--studio-accent) 28%, var(--studio-line)); background: color-mix(in srgb, var(--studio-accent) 8%, transparent); }.action-row__select { display: flex; flex: 1; align-items: center; min-width: 0; border: 0; padding: 7px 6px; background: transparent; color: inherit; cursor: pointer; font: inherit; text-align: left; }.action-row__dot { width: 6px; height: 6px; flex: 0 0 6px; margin-right: 8px; border-radius: 50%; background: var(--studio-green); }.action-row__dot.is-empty { background: var(--studio-line); }.action-row__name { overflow: hidden; flex: 1; font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }.action-row__count { color: var(--studio-muted); font-size: 10px; }.action-row__delete { width: 25px; border: 0; background: transparent; color: var(--studio-muted); cursor: pointer; font-size: 16px; }.action-row__delete:hover { color: var(--studio-red); }
.sidebar-add { display: flex; flex-direction: column; gap: 6px; margin-top: 10px; }.sidebar-add input, .field input, .field textarea, .field select, .field--compact select { width: 100%; border: 1px solid var(--studio-line); border-radius: 5px; outline: none; background: var(--studio-surface); color: var(--studio-ink); font: inherit; font-size: 11px; }.sidebar-add input, .field input, .field textarea { padding: 8px 9px; }.field select, .field--compact select { padding: 7px 8px; }.field textarea { min-height: 58px; resize: vertical; }.sidebar-add input:focus, .field input:focus, .field textarea:focus, .field select:focus { border-color: color-mix(in srgb, var(--studio-accent) 60%, var(--studio-line)); }.sidebar-footnote { margin-top: auto; border-top: 1px solid var(--studio-line); padding-top: 10px; line-height: 1.5; }
.button, .icon-button { border: 1px solid transparent; border-radius: 5px; cursor: pointer; font: inherit; font-size: 11px; font-weight: 700; }.button { min-height: 30px; padding: 7px 10px; background: var(--studio-accent); color: #fff; }.button--small { min-height: 27px; padding: 5px 8px; font-size: 10px; }.button--primary { background: var(--studio-green); }.button--quiet { border-color: var(--studio-line); background: transparent; color: var(--studio-ink); }.button--danger { border-color: color-mix(in srgb, var(--studio-red) 32%, var(--studio-line)); background: color-mix(in srgb, var(--studio-red) 8%, transparent); color: var(--studio-red); }.icon-button { display: inline-grid; width: 29px; height: 29px; place-items: center; border-color: var(--studio-line); background: transparent; color: var(--studio-ink); }.icon-button--danger { color: var(--studio-red); }.button:disabled, .icon-button:disabled, .studio-tabs button:disabled, .action-row button:disabled, .behavior-list button:disabled, .reference-add:disabled { cursor: not-allowed; opacity: .42; }
.preview-stage { position: relative; display: grid; min-height: 285px; margin-top: 11px; place-items: center; overflow: hidden; border: 1px solid var(--studio-line); border-radius: 7px; background: #edf0ee; background-image: linear-gradient(45deg, #dfe5e1 25%, transparent 25%), linear-gradient(-45deg, #dfe5e1 25%, transparent 25%), linear-gradient(45deg, transparent 75%, #dfe5e1 75%), linear-gradient(-45deg, transparent 75%, #dfe5e1 75%); background-position: 0 0, 0 10px, 10px -10px, -10px 0; background-size: 20px 20px; }.preview-stage__grid { position: absolute; inset: 0; opacity: .25; background: linear-gradient(90deg, transparent 49.8%, #fff 50%, transparent 50.2%), linear-gradient(0deg, transparent 49.8%, #fff 50%, transparent 50.2%); background-size: 50% 50%; pointer-events: none; }.preview-stage img { position: relative; z-index: 1; max-width: 92%; max-height: 270px; object-fit: contain; }.preview-stage__empty { position: relative; z-index: 1; display: flex; flex-direction: column; align-items: center; gap: 6px; color: var(--studio-muted); text-align: center; }.preview-stage__empty-mark { display: grid; width: 38px; height: 38px; place-items: center; border: 1px dashed var(--studio-muted); border-radius: 50%; font-size: 21px; font-weight: 300; }.preview-stage__empty strong { font-size: 12px; }.preview-stage__empty small { font-size: 10px; }.preview-stage__loading { position: absolute; right: 10px; bottom: 9px; z-index: 2; border: 1px solid color-mix(in srgb, var(--studio-accent) 28%, var(--studio-line)); border-radius: 999px; padding: 5px 8px; background: color-mix(in srgb, var(--studio-surface) 86%, transparent); color: var(--studio-accent); font-size: 10px; }
.frame-strip { display: flex; gap: 8px; overflow-x: auto; margin-top: 11px; padding: 2px 1px 5px; }.frame-card { position: relative; display: flex; width: 78px; height: 94px; flex: 0 0 78px; flex-direction: column; align-items: stretch; overflow: hidden; border: 1px solid var(--studio-line); border-radius: 6px; background: var(--studio-surface); color: var(--studio-ink); cursor: pointer; font: inherit; }.frame-card.is-selected { border-color: var(--studio-accent); box-shadow: 0 0 0 2px color-mix(in srgb, var(--studio-accent) 16%, transparent); }.frame-card img { width: 100%; height: 68px; object-fit: contain; background: #eef1ef; background-image: linear-gradient(45deg, #dfe5e1 25%, transparent 25%), linear-gradient(-45deg, #dfe5e1 25%, transparent 25%), linear-gradient(45deg, transparent 75%, #dfe5e1 75%), linear-gradient(-45deg, transparent 75%, #dfe5e1 75%); background-position: 0 0, 0 7px, 7px -7px, -7px 0; background-size: 14px 14px; }.frame-card__number { position: absolute; top: 4px; left: 4px; z-index: 1; border-radius: 3px; padding: 2px 4px; background: color-mix(in srgb, #111 66%, transparent); color: #fff; font-size: 9px; }.frame-card__duration { overflow: hidden; padding: 5px; color: var(--studio-muted); font-size: 9px; text-overflow: ellipsis; white-space: nowrap; }.frame-card--add { align-items: center; justify-content: center; gap: 5px; border-style: dashed; color: var(--studio-muted); }.frame-card--add span { font-size: 22px; font-weight: 300; }.frame-card--add small { font-size: 10px; }.timeline-empty { margin-top: 11px; border: 1px dashed var(--studio-line); border-radius: 6px; padding: 21px; text-align: center; }
.field { display: flex; flex-direction: column; gap: 5px; min-width: 0; color: var(--studio-muted); font-size: 10px; font-weight: 700; }.field-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }.toggle-line { justify-content: flex-start; gap: 7px; min-width: 0; color: var(--studio-muted); cursor: pointer; font-size: 10px; }.toggle-line input, .check-chip input { accent-color: var(--studio-accent); }.color-row { justify-content: space-between; gap: 8px; }.color-field { display: flex; align-items: center; gap: 6px; color: var(--studio-muted); font-family: ui-monospace, SFMono-Regular, Consolas, monospace; font-size: 10px; }.color-field input { width: 25px; height: 22px; padding: 1px; border: 1px solid var(--studio-line); border-radius: 4px; background: transparent; }.generation-actions { margin-top: 2px; }.generation-actions .button--primary { flex: 1; }.step-tracker { display: flex; gap: 4px; margin-top: 2px; }.step-tracker span { border-radius: 3px; padding: 3px 5px; background: var(--studio-soft); color: var(--studio-muted); font-size: 9px; }.step-tracker span.is-active { background: color-mix(in srgb, var(--studio-accent) 14%, transparent); color: var(--studio-accent); }.step-tracker span.is-done { color: var(--studio-green); }
.action-detail-grid .detail-section { display: flex; flex-direction: column; gap: 10px; }.selected-frame-controls { display: grid; grid-template-columns: 76px 1fr; gap: 10px; }.selected-frame-preview { display: grid; min-height: 76px; place-items: center; overflow: hidden; border: 1px solid var(--studio-line); border-radius: 5px; background: #eef1ef; background-image: linear-gradient(45deg, #dfe5e1 25%, transparent 25%), linear-gradient(-45deg, #dfe5e1 25%, transparent 25%), linear-gradient(45deg, transparent 75%, #dfe5e1 75%), linear-gradient(-45deg, transparent 75%, #dfe5e1 75%); background-position: 0 0, 0 7px, 7px -7px, -7px 0; background-size: 14px 14px; }.selected-frame-preview img { max-width: 100%; max-height: 100%; object-fit: contain; }.selected-frame-fields { display: flex; flex-direction: column; gap: 8px; }.frame-move-actions { display: flex; gap: 5px; }.frame-source { display: block; line-height: 1.4; }
.restyle-panel { display: flex; flex-direction: column; gap: 9px; }.restyle-options, .behavior-action-checks { display: flex; flex-wrap: wrap; gap: 6px; }.check-chip { display: inline-flex; align-items: center; gap: 5px; border: 1px solid var(--studio-line); border-radius: 999px; padding: 5px 7px; color: var(--studio-muted); cursor: pointer; font-size: 10px; }.check-chip:has(input:checked) { border-color: color-mix(in srgb, var(--studio-accent) 42%, var(--studio-line)); background: color-mix(in srgb, var(--studio-accent) 9%, transparent); color: var(--studio-ink); }
.behavior-editor { min-height: 400px; }.behavior-layout { display: grid; grid-template-columns: 145px minmax(0, 1fr); gap: 16px; margin-top: 13px; }.behavior-list { display: flex; flex-direction: column; gap: 3px; }.behavior-list > button { display: flex; align-items: center; justify-content: space-between; border: 1px solid transparent; border-radius: 5px; padding: 8px; background: transparent; color: var(--studio-ink); cursor: pointer; font: inherit; font-size: 11px; text-align: left; }.behavior-list > button.is-selected { border-color: color-mix(in srgb, var(--studio-accent) 28%, var(--studio-line)); background: color-mix(in srgb, var(--studio-accent) 8%, transparent); }.behavior-list small { color: var(--studio-muted); }.behavior-add { margin-top: 8px; }.behavior-detail { display: flex; flex-direction: column; gap: 14px; }.behavior-actions-block, .behavior-order { display: flex; flex-direction: column; gap: 8px; }.field-label { color: var(--studio-muted); font-size: 10px; font-weight: 700; }.behavior-order { border-top: 1px solid var(--studio-line); padding-top: 12px; }.behavior-order-row { display: flex; align-items: center; gap: 8px; border-bottom: 1px solid var(--studio-line); padding: 7px 0; }.order-index { display: grid; width: 21px; height: 21px; place-items: center; border-radius: 4px; background: var(--studio-soft); color: var(--studio-muted); font-size: 10px; }.behavior-order-row strong { font-size: 11px; }.behavior-order-spacer { flex: 1; }
.reference-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 6px; }.reference-thumb, .reference-add { position: relative; display: grid; min-height: 56px; place-items: center; overflow: hidden; border: 1px dashed var(--studio-line); border-radius: 5px; background: var(--studio-soft); }.reference-thumb img { width: 100%; height: 68px; object-fit: cover; }.reference-thumb button { position: absolute; top: 2px; right: 2px; width: 18px; height: 18px; border: 0; border-radius: 50%; background: #111a; color: #fff; cursor: pointer; line-height: 15px; }.reference-add { min-height: 68px; border-style: dashed; background: transparent; color: var(--studio-muted); cursor: pointer; }.reference-add > span { font-size: 9px; }
.inspector-section { display: flex; flex-direction: column; gap: 10px; }.inspector-heading { align-items: flex-start; flex-direction: column; gap: 0; }.metadata-list { display: flex; flex-direction: column; gap: 6px; margin: 2px 0 0; }.metadata-list div { display: flex; justify-content: space-between; gap: 8px; border-top: 1px solid var(--studio-line); padding-top: 6px; }.metadata-list dt { color: var(--studio-muted); font-size: 10px; }.metadata-list dd { margin: 0; overflow: hidden; text-align: right; text-overflow: ellipsis; white-space: nowrap; }.inspector-section--save { border-color: color-mix(in srgb, var(--studio-green) 30%, var(--studio-line)); }.save-mode-note { line-height: 1.45; }.save-actions { display: grid; grid-template-columns: 1fr 1fr; }.save-actions .button--primary { grid-column: 1 / -1; }.packed-preview { border-top: 1px solid var(--studio-line); padding-top: 10px; }.packed-preview__heading span { color: var(--studio-muted); font-size: 10px; font-weight: 700; }.packed-preview__heading small { color: var(--studio-green); font-size: 9px; }.packed-preview img { display: block; width: 100%; max-height: 100px; margin-top: 7px; object-fit: contain; background: #eef1ef; }
.skins-section { display: flex; flex-direction: column; gap: 10px; }.skin-list { display: flex; flex-direction: column; gap: 6px; }.skin-row { border-top: 1px solid var(--studio-line); padding-top: 8px; }.skin-row > div { min-width: 0; flex: 1; align-items: baseline; justify-content: flex-start; gap: 8px; }.skin-row strong { overflow: hidden; font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }.skin-row span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.skin-badge { color: var(--studio-accent); }
.visually-hidden { position: absolute !important; width: 1px !important; height: 1px !important; overflow: hidden !important; clip: rect(1px, 1px, 1px, 1px) !important; white-space: nowrap !important; }
@media (max-width: 1120px) { .studio-layout { grid-template-columns: 170px minmax(380px, 1fr); }.studio-inspector { grid-column: 1 / -1; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); align-items: start; }.inspector-section--save { grid-column: 1 / -1; } }
@media (max-width: 760px) { .pet-studio { padding: 14px 10px; }.studio-header { align-items: flex-start; flex-direction: column; }.studio-header__source { width: 100%; justify-content: flex-start; }.studio-layout { grid-template-columns: 1fr; }.studio-sidebar { order: 0; }.studio-main { order: 1; }.studio-inspector { order: 2; display: flex; }.action-detail-grid { grid-template-columns: 1fr; }.behavior-layout { grid-template-columns: 1fr; }.studio-toolbar { align-items: flex-start; flex-direction: column; }.studio-toolbar__actions { justify-content: flex-start; }.preview-stage { min-height: 230px; }.save-actions { grid-template-columns: 1fr; }.save-actions .button--primary { grid-column: auto; } }
</style>
