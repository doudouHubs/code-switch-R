<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { petApi } from './petApi'
import {
  deletePetStudioSkin,
  generatePetStudioImage,
  listPetStudioSkins,
  packPetStudioAtlas,
  processPetStudioFrame,
  savePetStudioSkin,
  splitPetStudioActionSheet,
  toDataUrl,
  type PetStudioActionId,
  type PetStudioFrameInput
} from './petStudioApi'

interface Props { petId?: string }
const props = withDefaults(defineProps<Props>(), { petId: 'default' })
const { t } = useI18n()
const text = (key: string, params?: Record<string, unknown>): string => params ? t(key, params) : t(key)

// 必须覆盖源项目的内置动作集合；漏掉 bathe/soak 等动作会让生成出的 atlas
// 无法被现有 PetAtlas 行为映射选中，Studio 看似保存成功但运行时永远不会播放。
const actions: PetStudioActionId[] = [
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
]
const form = reactive({
  name: 'My Pet',
  subject: '',
  platform: '',
  providerId: '',
  modelId: '',
  prompt: '',
  frameCount: 1,
  keyColor: '#00ff00',
  targetHeight: 256,
  chromaKey: true,
  bind: true
})
const selectedAction = ref<PetStudioActionId>('idle')
const generatedPreview = ref('')
const frames = reactive<Record<string, PetStudioFrameInput[]>>({})
const phase = reactive({ loading: false, generating: false, processing: false, packing: false, saving: false, deleting: false })
const step = ref<'idle' | 'generate' | 'split' | 'chroma' | 'normalize' | 'pack' | 'saved'>('idle')
const completedSteps = ref(new Set<string>())
const errorMessage = ref('')
const notice = ref('')
const atlasUrl = ref('')
const manifest = ref<Record<string, unknown> | null>(null)
const skins = ref<Array<{ skinId: string; name: string; builtin?: boolean }>>([])

const busy = computed(() => Object.values(phase).some(Boolean))
const currentFrames = computed(() => frames[selectedAction.value] ?? [])

function imageDataUrl(data: string): string { return toDataUrl(data) }
function resetMessage(): void { errorMessage.value = ''; notice.value = '' }
function errorOf(error: unknown): string { return error instanceof Error ? error.message : String(error) }
function actionLabel(action: PetStudioActionId): string { return t(`pet.studio.actions.${action}`) }
function stepLabel(stepName: string): string { return t(`pet.studio.steps.${stepName}`) }

async function loadSkins(): Promise<void> {
  phase.loading = true
  try {
    const value = await listPetStudioSkins(props.petId)
    const list = Array.isArray(value) ? value : []
    skins.value = list.filter((item): item is Record<string, unknown> => Boolean(item) && typeof item === 'object')
      .map((item) => ({ skinId: String(item.skinId ?? ''), name: String(item.name ?? item.skinId ?? ''), builtin: item.builtin === true }))
      .filter((item) => item.skinId)
  } catch (error) {
    // 列表失败不阻断新工程，保存时后端仍会做最终校验。
    errorMessage.value = errorOf(error)
  } finally { phase.loading = false }
}

async function generate(): Promise<void> {
  resetMessage()
  if (!form.subject.trim() || !form.providerId.trim() || !form.modelId.trim()) {
    errorMessage.value = text('pet.studio.validation')
    return
  }
  phase.generating = true
  step.value = 'generate'
  try {
    const frameCount = Math.min(8, Math.max(1, Math.floor(Number(form.frameCount) || 1)))
    form.frameCount = frameCount
    // 生成结果只进入内存草稿；后续媒体处理成功前，不把半成品加入 atlas 帧集合。
    const prompt = [
      form.subject.trim(),
      form.prompt.trim(),
      frameCount > 1
        ? `${frameCount}-frame ${selectedAction.value} animation sprite sheet, invisible grid, left to right then top to bottom`
        : `single ${selectedAction.value} pose`,
      frameCount > 1
        ? 'same full body pet in every cell, equal cells, no labels or borders, clean background, consistent character design'
        : 'full body pet sprite, centered, clean background, consistent character design'
    ].filter(Boolean).join(', ')
    const generated = await generatePetStudioImage(props.petId, {
      platform: form.platform.trim(), providerId: form.providerId.trim(), modelId: form.modelId.trim()
    }, prompt)
    generatedPreview.value = imageDataUrl(generated.data)
    completedSteps.value.add('generate')
    phase.generating = false

    phase.processing = true
    const sources = frameCount > 1
      ? (step.value = 'split', await splitPetStudioActionSheet(generated.data, frameCount)).frames.map((frame) => frame.data)
      : [generated.data]
    if (frameCount > 1) completedSteps.value.add('split')
    step.value = form.chromaKey ? 'chroma' : 'normalize'
    const normalizedFrames: PetStudioFrameInput[] = []
    for (const source of sources) {
      const normalized = await processPetStudioFrame(source, {
        chromaKey: form.chromaKey,
        keyColor: form.keyColor,
        targetHeight: Math.max(32, Math.floor(form.targetHeight))
      })
      normalizedFrames.push({ data: normalized, durationMs: 500 })
    }
    if (form.chromaKey) completedSteps.value.add('chroma')
    completedSteps.value.add('normalize')
    frames[selectedAction.value] = [...(frames[selectedAction.value] ?? []), ...normalizedFrames]
    notice.value = text('pet.studio.generated')
    step.value = 'idle'
  } catch (error) {
    errorMessage.value = errorOf(error)
    step.value = 'idle'
  } finally { phase.generating = false; phase.processing = false }
}

async function pack(): Promise<void> {
  resetMessage()
  phase.packing = true
  step.value = 'pack'
  try {
    const result = await packPetStudioAtlas(form.name.trim() || 'Pet', frames)
    atlasUrl.value = result.data ? imageDataUrl(result.data) : ''
    manifest.value = result.manifest ?? null
    completedSteps.value.add('pack')
    notice.value = text('pet.studio.packed')
    step.value = 'idle'
  } catch (error) { errorMessage.value = errorOf(error); step.value = 'idle' }
  finally { phase.packing = false }
}

async function save(): Promise<void> {
  resetMessage()
  if (!atlasUrl.value || !manifest.value) { errorMessage.value = text('pet.studio.packFirst'); return }
  phase.saving = true
  try {
    // 保存边界只提交 base64 atlas 和 manifest，路径由 Studio 后端按受控 skinId 创建。
    const atlasBase64 = atlasUrl.value.split(',')[1] ?? ''
    await savePetStudioSkin(props.petId, {
      skinId: `studio-${Date.now().toString(36)}`,
      name: form.name.trim() || 'Pet Studio',
      subject: form.subject.trim(),
      modelId: form.modelId.trim(),
      atlasBase64,
      manifestJson: manifest.value,
      bind: form.bind
    })
    step.value = 'saved'
    notice.value = text(form.bind ? 'pet.studio.savedBound' : 'pet.studio.saved')
    await loadSkins()
  } catch (error) { errorMessage.value = errorOf(error) }
  finally { phase.saving = false }
}

async function removeSkin(skin: { skinId: string; builtin?: boolean }): Promise<void> {
  if (busy.value || skin.builtin || !window.confirm(text('pet.studio.deleteConfirm'))) return
  resetMessage(); phase.deleting = true
  try { await deletePetStudioSkin(props.petId, skin.skinId); notice.value = text('pet.studio.deleted'); await loadSkins() }
  catch (error) { errorMessage.value = errorOf(error) }
  finally { phase.deleting = false }
}

onMounted(() => { void loadSkins() })
</script>

<template>
  <section class="pet-studio">
    <header class="pet-studio__header">
      <div><span class="pet-studio__eyebrow">{{ text('pet.studio.eyebrow') }}</span><h2>{{ text('pet.studio.title') }}</h2><p>{{ text('pet.studio.subtitle') }}</p></div>
      <span class="pet-studio__status" :class="{ 'is-busy': busy }">{{ busy ? text('pet.studio.busy') : text('pet.studio.ready') }}</span>
    </header>
    <div v-if="errorMessage" class="pet-studio__message is-error" role="alert">{{ errorMessage }}</div>
    <div v-if="notice" class="pet-studio__message">{{ notice }}</div>

    <div class="pet-studio__grid">
      <div class="pet-studio__controls">
        <label><span>{{ text('pet.studio.name') }}</span><input v-model="form.name" :disabled="busy" /></label>
        <label><span>{{ text('pet.studio.subject') }}</span><textarea v-model="form.subject" :disabled="busy" rows="3" :placeholder="text('pet.studio.subjectPlaceholder')" /></label>
        <div class="pet-studio__two-col"><label><span>{{ text('pet.studio.platform') }}</span><input v-model="form.platform" :disabled="busy" :placeholder="text('pet.studio.platformPlaceholder')" /></label><label><span>{{ text('pet.studio.provider') }}</span><input v-model="form.providerId" :disabled="busy" /></label></div>
        <label><span>{{ text('pet.studio.model') }}</span><input v-model="form.modelId" :disabled="busy" /></label>
        <label><span>{{ text('pet.studio.prompt') }}</span><textarea v-model="form.prompt" :disabled="busy" rows="2" /></label>
        <div class="pet-studio__two-col"><label><span>{{ text('pet.studio.action') }}</span><select v-model="selectedAction" :disabled="busy"><option v-for="action in actions" :key="action" :value="action">{{ actionLabel(action) }}</option></select></label><label><span>{{ text('pet.studio.frameCount') }}</span><input v-model.number="form.frameCount" type="number" min="1" max="8" :disabled="busy" /></label></div>
        <label><span>{{ text('pet.studio.targetHeight') }}</span><input v-model.number="form.targetHeight" type="number" min="32" max="1024" :disabled="busy" /></label>
        <div class="pet-studio__options"><label><input v-model="form.chromaKey" type="checkbox" :disabled="busy" /> {{ text('pet.studio.chromaKey') }}</label><input v-model="form.keyColor" type="color" :disabled="busy || !form.chromaKey" :title="text('pet.studio.keyColor')" /><label><input v-model="form.bind" type="checkbox" :disabled="busy" /> {{ text('pet.studio.bind') }}</label></div>
        <div class="pet-studio__actions"><button type="button" :disabled="busy" @click="generate">{{ text('pet.studio.generate') }}</button><button type="button" class="is-secondary" :disabled="busy || !Object.keys(frames).length" @click="pack">{{ text('pet.studio.pack') }}</button><button type="button" class="is-primary" :disabled="busy || !atlasUrl" @click="save">{{ text('pet.studio.save') }}</button></div>
      </div>

      <div class="pet-studio__preview">
        <div class="pet-studio__steps"><span v-for="item in ['generate', 'split', 'chroma', 'normalize', 'pack']" :key="item" :class="{ active: step === item, done: completedSteps.has(item) }">{{ stepLabel(item) }}</span></div>
        <div class="pet-studio__canvas"><img v-if="generatedPreview" :src="generatedPreview" :alt="text('pet.studio.generatedPreviewAlt')" /><span v-else>{{ text('pet.studio.emptyPreview') }}</span></div>
        <div class="pet-studio__frames"><div v-for="(frame, index) in currentFrames" :key="`${selectedAction}-${index}`"><img :src="imageDataUrl(frame.data)" :alt="text('pet.studio.frameAlt', { action: actionLabel(selectedAction), index: index + 1 })" /></div></div>
        <div v-if="atlasUrl" class="pet-studio__atlas"><strong>{{ text('pet.studio.atlasPreview') }}</strong><img :src="atlasUrl" :alt="text('pet.studio.atlasAlt')" /></div>
      </div>
    </div>

    <div class="pet-studio__skins"><div class="pet-studio__section-heading"><h3>{{ text('pet.studio.savedSkins') }}</h3><button type="button" class="is-link" :disabled="busy" @click="loadSkins">{{ text('pet.common.refresh') }}</button></div><div v-if="!skins.length" class="pet-studio__empty">{{ text('pet.studio.noSkins') }}</div><div v-for="skin in skins" :key="skin.skinId" class="pet-studio__skin"><span>{{ skin.name }}</span><small>{{ skin.skinId }}</small><button type="button" :disabled="busy || skin.builtin" @click="removeSkin(skin)">{{ text('pet.common.delete') }}</button></div></div>
  </section>
</template>

<style scoped>
.pet-studio { display: flex; flex-direction: column; gap: 14px; padding: 22px; color: var(--settings-ink, #243042); }
.pet-studio__header, .pet-studio__section-heading, .pet-studio__skin { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.pet-studio__eyebrow { color: var(--mac-accent, #0a84ff); font-size: 10px; letter-spacing: .12em; }
.pet-studio h2, .pet-studio h3, .pet-studio p { margin: 0; }.pet-studio h2 { margin-top: 5px; font-size: 22px; }.pet-studio p, .pet-studio small { color: var(--settings-muted, #7c8798); font-size: 11px; }
.pet-studio__status { border: 1px solid var(--settings-line); border-radius: 999px; padding: 5px 9px; font-size: 11px; }.pet-studio__status.is-busy { color: #b26b21; }
.pet-studio__message { border: 1px solid color-mix(in srgb, #2d9d69 35%, var(--settings-line)); border-radius: 8px; padding: 9px 10px; color: #287950; font-size: 12px; }.pet-studio__message.is-error { border-color: #d98b8b; color: #b44242; }
.pet-studio__grid { display: grid; grid-template-columns: minmax(260px, .9fr) minmax(300px, 1.1fr); gap: 14px; }.pet-studio__controls, .pet-studio__preview, .pet-studio__skins { border: 1px solid var(--settings-line); border-radius: 10px; padding: 14px; background: color-mix(in srgb, var(--settings-strong-surface) 70%, transparent); }.pet-studio__controls { display: flex; flex-direction: column; gap: 11px; }
.pet-studio label { display: flex; flex-direction: column; gap: 5px; color: var(--settings-muted); font-size: 11px; }.pet-studio input, .pet-studio textarea, .pet-studio select { box-sizing: border-box; width: 100%; border: 1px solid var(--settings-line); border-radius: 7px; padding: 8px; background: var(--settings-surface); color: var(--settings-ink); font: inherit; font-size: 12px; }.pet-studio textarea { resize: vertical; }.pet-studio__two-col { display: grid; grid-template-columns: 1fr 1fr; gap: 9px; }.pet-studio__options { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; }.pet-studio__options label { flex-direction: row; align-items: center; }.pet-studio__options input[type='color'] { width: 28px; height: 24px; padding: 2px; }
.pet-studio__actions { display: flex; flex-wrap: wrap; gap: 7px; }.pet-studio button { border: 1px solid var(--settings-line); border-radius: 7px; padding: 8px 11px; background: var(--mac-accent, #0a84ff); color: white; font: inherit; font-size: 11px; cursor: pointer; }.pet-studio button.is-secondary { background: transparent; color: var(--settings-ink); }.pet-studio button.is-primary { background: #2c996a; }.pet-studio button.is-link { border: 0; padding: 2px; background: transparent; color: var(--mac-accent); }.pet-studio button:disabled { cursor: not-allowed; opacity: .45; }
.pet-studio__preview { display: flex; min-height: 330px; flex-direction: column; gap: 10px; }.pet-studio__steps { display: flex; gap: 5px; }.pet-studio__steps span { border-radius: 5px; padding: 4px 6px; background: color-mix(in srgb, var(--settings-line) 45%, transparent); color: var(--settings-muted); font-size: 10px; }.pet-studio__steps span.active { background: color-mix(in srgb, var(--mac-accent) 20%, transparent); color: var(--mac-accent); }.pet-studio__steps span.done { color: #2c996a; }.pet-studio__canvas { display: grid; min-height: 230px; place-items: center; overflow: hidden; border-radius: 8px; background-color: #eef1f3; background-image: linear-gradient(45deg, #dfe4e7 25%, transparent 25%), linear-gradient(-45deg, #dfe4e7 25%, transparent 25%), linear-gradient(45deg, transparent 75%, #dfe4e7 75%), linear-gradient(-45deg, transparent 75%, #dfe4e7 75%); background-size: 20px 20px; background-position: 0 0, 0 10px, 10px -10px, -10px 0; }.pet-studio__canvas img { max-width: 100%; max-height: 230px; object-fit: contain; }.pet-studio__canvas span, .pet-studio__empty { color: var(--settings-muted); font-size: 12px; }.pet-studio__frames { display: flex; gap: 6px; overflow-x: auto; }.pet-studio__frames div { width: 48px; height: 48px; flex: 0 0 48px; border: 1px solid var(--settings-line); border-radius: 5px; background: #eef1f3; }.pet-studio__frames img { width: 100%; height: 100%; object-fit: contain; }.pet-studio__atlas { display: flex; align-items: center; gap: 8px; font-size: 11px; }.pet-studio__atlas img { max-width: 180px; max-height: 70px; object-fit: contain; }.pet-studio__skins { display: flex; flex-direction: column; gap: 8px; }.pet-studio__skin { justify-content: flex-start; border-top: 1px solid var(--settings-line); padding-top: 8px; }.pet-studio__skin small { flex: 1; }.pet-studio__skin button { margin-left: auto; padding: 5px 8px; background: transparent; color: #b44242; }
@media (max-width: 700px) { .pet-studio { padding: 14px 10px; }.pet-studio__grid { grid-template-columns: 1fr; } }
</style>
