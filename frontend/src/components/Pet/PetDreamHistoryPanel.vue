<script setup lang="ts">
import { Call } from '../../wails-runtime-compat'
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { DEFAULT_PET_ID, type PetDreamHistoryRecord } from './petTypes'

interface PetDreamHistoryPanelProps {
  petId?: string
}

const props = withDefaults(defineProps<PetDreamHistoryPanelProps>(), {
  petId: DEFAULT_PET_ID
})
const { t, locale } = useI18n()

const PET_DREAM_SERVICE = 'codeswitch/services.PetDreamAPIService'
const PET_DREAM_METHODS = {
  listHistory: `${PET_DREAM_SERVICE}.ListHistory`,
  readImage: `${PET_DREAM_SERVICE}.ReadImage`
} as const
const PET_DREAM_PAGE_SIZE = 20
const PET_DREAM_EMOTIONS = ['pleasant', 'calm', 'tense', 'afraid'] as const

interface PetDreamHistoryPage {
  records: PetDreamHistoryRecord[]
  page: number
  pageSize: number
  total: number
  totalPages: number
  hasNext: boolean
  hasPrevious: boolean
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function finiteNumber(value: unknown, fallback = 0): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

function normalizeDreamRecord(value: unknown, petId: string, index: number): PetDreamHistoryRecord | null {
  const item = isRecord(value) ? value : {}
  const dream = typeof item.dream === 'string' ? item.dream.trim() : ''
  if (!dream) return null
  const emotion = typeof item.emotion === 'string' && PET_DREAM_EMOTIONS.includes(item.emotion as (typeof PET_DREAM_EMOTIONS)[number])
    ? item.emotion as PetDreamHistoryRecord['emotion']
    : null
  return {
    petId: typeof item.petId === 'string' ? item.petId : petId,
    id: typeof item.id === 'string' && item.id ? item.id : `dream-${index}`,
    createdAt: finiteNumber(item.createdAt),
    title: typeof item.title === 'string' ? item.title.trim() : '',
    creativePrompt: typeof item.creativePrompt === 'string' ? item.creativePrompt : '',
    effectivePrompt: typeof item.effectivePrompt === 'string' ? item.effectivePrompt : '',
    keywords: Array.isArray(item.keywords)
      ? item.keywords.filter((keyword): keyword is string => typeof keyword === 'string').map((keyword) => keyword.trim()).filter(Boolean)
      : [],
    themeId: typeof item.themeId === 'string' && item.themeId ? item.themeId : null,
    themeLabel: typeof item.themeLabel === 'string' && item.themeLabel ? item.themeLabel : null,
    dream,
    sleepTalk: typeof item.sleepTalk === 'string' ? item.sleepTalk.trim() : '',
    emotion,
    selfAppears: typeof item.selfAppears === 'boolean' ? item.selfAppears : null,
    imagePath: typeof item.imagePath === 'string' && item.imagePath.trim() ? item.imagePath.trim() : null
  }
}

function normalizeHistoryPage(value: unknown, petId: string): PetDreamHistoryPage {
  const root = isRecord(value) ? value : {}
  const rawRecords = Array.isArray(root.records) ? root.records : []
  const records = rawRecords
    .map((record, index) => normalizeDreamRecord(record, petId, index))
    .filter((record): record is PetDreamHistoryRecord => record !== null)
  const totalPages = Math.max(0, Math.floor(finiteNumber(root.totalPages)))
  const page = Math.max(1, Math.floor(finiteNumber(root.page, 1)))
  const pageSize = Math.max(1, Math.floor(finiteNumber(root.pageSize, PET_DREAM_PAGE_SIZE)))
  const total = Math.max(0, Math.floor(finiteNumber(root.total, records.length)))
  return {
    records,
    page,
    pageSize,
    total,
    totalPages,
    hasNext: root.hasNext === true,
    hasPrevious: root.hasPrevious === true
  }
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function formatDreamDate(timestamp: number): string {
  if (!Number.isFinite(timestamp) || timestamp <= 0) return t('pet.common.unknownDate')
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(timestamp))
}

function isDataImageUrl(value: unknown): value is string {
  return typeof value === 'string' && /^data:image\/(?:png|jpeg|jpg|webp|gif);base64,[A-Za-z0-9+/]+=*$/i.test(value)
}

const records = ref<PetDreamHistoryRecord[]>([])
const selectedId = ref<string | null>(null)
const page = ref(1)
const totalPages = ref(0)
const total = ref(0)
const loading = ref(false)
const errorMessage = ref('')
const pageError = ref('')
const imageUrls = ref<Record<string, string>>({})
const imageLoading = ref<Record<string, boolean>>({})
const imageErrors = ref<Record<string, string>>({})
const imageRequestTokens = ref<Record<string, number>>({})
let requestGeneration = 0
let imageRequestSequence = 0

const selectedRecord = computed(() => records.value.find((record) => record.id === selectedId.value) ?? records.value[0] ?? null)
const selectedImageUrl = computed(() => (selectedRecord.value ? imageUrls.value[selectedRecord.value.id] ?? '' : ''))
const selectedImageLoading = computed(() => (selectedRecord.value ? imageLoading.value[selectedRecord.value.id] === true : false))
const selectedImageError = computed(() => (selectedRecord.value ? imageErrors.value[selectedRecord.value.id] ?? '' : ''))

function resetImageStates(): void {
  imageUrls.value = {}
  imageLoading.value = {}
  imageErrors.value = {}
  imageRequestTokens.value = {}
}

async function loadImage(record: PetDreamHistoryRecord, generation = requestGeneration): Promise<void> {
  if (!record.imagePath || imageUrls.value[record.id] || imageLoading.value[record.id]) return
  const token = ++imageRequestSequence
  imageRequestTokens.value = { ...imageRequestTokens.value, [record.id]: token }
  imageLoading.value = { ...imageLoading.value, [record.id]: true }
  imageErrors.value = { ...imageErrors.value, [record.id]: '' }
  try {
    const raw = await Call.ByName(PET_DREAM_METHODS.readImage, props.petId, record.imagePath)
    if (generation !== requestGeneration || imageRequestTokens.value[record.id] !== token) return
    if (!isDataImageUrl(raw)) throw new Error(t('pet.dreamHistory.imageInvalid'))
    imageUrls.value = { ...imageUrls.value, [record.id]: raw }
  } catch (error) {
    if (generation !== requestGeneration || imageRequestTokens.value[record.id] !== token) return
    imageErrors.value = { ...imageErrors.value, [record.id]: errorText(error) }
  } finally {
    if (imageRequestTokens.value[record.id] === token) {
      imageLoading.value = { ...imageLoading.value, [record.id]: false }
    }
  }
}

async function loadPage(targetPage: number, replace = false): Promise<void> {
  const generation = ++requestGeneration
  loading.value = true
  if (replace) errorMessage.value = ''
  else pageError.value = ''
  try {
    const raw = await Call.ByName(
      PET_DREAM_METHODS.listHistory,
      props.petId,
      Math.max(1, targetPage),
      PET_DREAM_PAGE_SIZE
    )
    if (generation !== requestGeneration) return
    const result = normalizeHistoryPage(raw, props.petId)
    records.value = result.records
    page.value = result.page
    totalPages.value = result.totalPages
    total.value = result.total
    selectedId.value = result.records[0]?.id ?? null
    resetImageStates()
    // 图片按当前选中项读取，避免分页时一次性把整页大图编码进 renderer 内存。
    if (result.records[0]?.imagePath) void loadImage(result.records[0], generation)
  } catch (error) {
    if (generation !== requestGeneration) return
    const message = errorText(error)
    if (replace) errorMessage.value = message
    else pageError.value = message
  } finally {
    if (generation === requestGeneration) loading.value = false
  }
}

async function refresh(): Promise<void> {
  // 刷新会让旧请求失效，防止切页或重试时旧响应覆盖新列表。
  requestGeneration += 1
  records.value = []
  selectedId.value = null
  page.value = 1
  totalPages.value = 0
  total.value = 0
  errorMessage.value = ''
  pageError.value = ''
  resetImageStates()
  await loadPage(1, true)
}

function selectRecord(record: PetDreamHistoryRecord): void {
  selectedId.value = record.id
  if (record.imagePath) void loadImage(record)
}

function retryCurrentPage(): void {
  if (records.value.length === 0) void refresh()
  else void loadPage(page.value)
}

function retrySelectedImage(): void {
  if (selectedRecord.value?.imagePath) void loadImage(selectedRecord.value)
}

watch(selectedRecord, (record) => {
  if (record?.imagePath) void loadImage(record)
})

watch(
  () => props.petId,
  () => {
    void refresh()
  },
  { immediate: true }
)

onUnmounted(() => {
  requestGeneration += 1
})
</script>

<template>
  <section class="pet-dream-history-panel">
    <div class="pet-dream-history-panel__header">
      <div>
        <h3>{{ t('pet.dreamHistory.title') }}</h3>
        <p>{{ t('pet.dreamHistory.subtitle') }}</p>
      </div>
      <button
        type="button"
        class="pet-dream-history-panel__button"
        :disabled="loading"
        @click="refresh"
      >
        {{ loading ? t('pet.common.loading') : t('pet.common.refresh') }}
      </button>
    </div>

    <div v-if="loading && records.length === 0" class="pet-dream-history-panel__state">{{ t('pet.dreamHistory.loading') }}</div>
    <div v-else-if="errorMessage && records.length === 0" class="pet-dream-history-panel__error-state">
      <span>{{ t('pet.dreamHistory.loadFailed', { error: errorMessage }) }}</span>
      <button type="button" class="pet-dream-history-panel__button" @click="retryCurrentPage">{{ t('pet.common.retry') }}</button>
    </div>
    <div v-else-if="records.length === 0" class="pet-dream-history-panel__state">{{ t('pet.dreamHistory.empty') }}</div>
    <div v-else class="pet-dream-history-panel__layout">
      <div class="pet-dream-history-panel__list-wrap">
        <div class="pet-dream-history-panel__list">
          <button
            v-for="record in records"
            :key="record.id"
            type="button"
            :class="['pet-dream-history-panel__item', { 'is-selected': selectedRecord?.id === record.id }]"
            @click="selectRecord(record)"
          >
            <span class="pet-dream-history-panel__thumb">
              <img v-if="imageUrls[record.id]" :src="imageUrls[record.id]" alt="" draggable="false" />
              <span v-else-if="imageLoading[record.id]">{{ t('pet.common.loadingShort') }}</span>
              <span v-else>{{ record.imagePath ? t('pet.dreamHistory.image') : t('pet.dreamHistory.noImage') }}</span>
            </span>
            <span class="pet-dream-history-panel__item-copy">
              <strong>{{ record.title || t('pet.dreamHistory.untitled') }}</strong>
              <small>{{ formatDreamDate(record.createdAt) }}</small>
            </span>
          </button>
        </div>
        <div v-if="pageError" class="pet-dream-history-panel__page-error">
          <span>{{ pageError }}</span>
          <button type="button" class="pet-dream-history-panel__text-button" @click="retryCurrentPage">{{ t('pet.dreamHistory.retryPage') }}</button>
        </div>
        <div v-if="totalPages > 1" class="pet-dream-history-panel__pagination">
          <button type="button" :disabled="loading || page <= 1" @click="loadPage(page - 1)">{{ t('pet.dreamHistory.previousPage') }}</button>
          <span>{{ t('pet.dreamHistory.pagination', { page, totalPages, total }) }}</span>
          <button type="button" :disabled="loading || page >= totalPages" @click="loadPage(page + 1)">{{ t('pet.dreamHistory.nextPage') }}</button>
        </div>
      </div>

      <article v-if="selectedRecord" class="pet-dream-history-panel__detail">
        <div class="pet-dream-history-panel__detail-header">
          <div>
            <h4>{{ selectedRecord.title || t('pet.dreamHistory.untitled') }}</h4>
            <small>{{ formatDreamDate(selectedRecord.createdAt) }}</small>
          </div>
          <span>{{ selectedRecord.emotion ? t(`pet.dreamHistory.emotions.${selectedRecord.emotion}`) : t('pet.dreamHistory.unknownEmotion') }}</span>
        </div>

        <div class="pet-dream-history-panel__image">
          <img v-if="selectedImageUrl" :src="selectedImageUrl" :alt="t('pet.dreamHistory.imageAlt')" draggable="false" />
          <span v-else-if="selectedImageLoading">{{ t('pet.dreamHistory.imageLoading') }}</span>
          <div v-else-if="selectedImageError" class="pet-dream-history-panel__image-error">
            <span>{{ t('pet.dreamHistory.imageFailed', { error: selectedImageError }) }}</span>
            <button type="button" class="pet-dream-history-panel__text-button" @click="retrySelectedImage">{{ t('pet.common.retry') }}</button>
          </div>
          <span v-else>{{ selectedRecord.imagePath ? t('pet.dreamHistory.imageUnavailable') : t('pet.dreamHistory.noRecordImage') }}</span>
        </div>

        <dl class="pet-dream-history-panel__fields">
          <div>
            <dt>{{ t('pet.dreamHistory.dream') }}</dt>
            <dd>{{ selectedRecord.dream }}</dd>
          </div>
          <div>
            <dt>{{ t('pet.dreamHistory.sleepTalk') }}</dt>
            <dd>{{ selectedRecord.sleepTalk || t('pet.dreamHistory.noSleepTalk') }}</dd>
          </div>
          <div v-if="selectedRecord.themeLabel || selectedRecord.themeId">
            <dt>{{ t('pet.dreamHistory.theme') }}</dt>
            <dd>{{ selectedRecord.themeLabel || selectedRecord.themeId }}</dd>
          </div>
          <div v-if="selectedRecord.keywords.length > 0">
            <dt>{{ t('pet.dreamHistory.keywords') }}</dt>
            <dd>{{ selectedRecord.keywords.join(' · ') }}</dd>
          </div>
          <div>
            <dt>{{ t('pet.dreamHistory.creativePrompt') }}</dt>
            <dd>{{ selectedRecord.creativePrompt || t('pet.dreamHistory.notRecorded') }}</dd>
          </div>
          <div>
            <dt>{{ t('pet.dreamHistory.effectivePrompt') }}</dt>
            <dd>{{ selectedRecord.effectivePrompt || t('pet.dreamHistory.notRecorded') }}</dd>
          </div>
        </dl>
      </article>
    </div>
  </section>
</template>

<style scoped>
.pet-dream-history-panel {
  --dream-ink: var(--settings-ink, var(--mac-text, #1d1d1f));
  --dream-muted: var(--settings-muted, var(--mac-text-secondary, #6e6e73));
  --dream-line: var(--settings-line, var(--mac-border, rgba(15, 23, 42, 0.12)));
  --dream-surface: var(--settings-surface, var(--mac-surface, #fff));
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 14px;
  border: 1px solid var(--dream-line);
  border-radius: 12px;
  padding: 16px;
  background: color-mix(in srgb, var(--dream-surface) 80%, transparent);
  color: var(--dream-ink);
}

.pet-dream-history-panel__header,
.pet-dream-history-panel__detail-header,
.pet-dream-history-panel__pagination,
.pet-dream-history-panel__page-error,
.pet-dream-history-panel__error-state {
  display: flex;
  align-items: center;
}

.pet-dream-history-panel h3,
.pet-dream-history-panel h4,
.pet-dream-history-panel p,
.pet-dream-history-panel dl,
.pet-dream-history-panel dd,
.pet-dream-history-panel dt {
  margin: 0;
}

.pet-dream-history-panel__header,
.pet-dream-history-panel__detail-header,
.pet-dream-history-panel__pagination,
.pet-dream-history-panel__page-error,
.pet-dream-history-panel__error-state {
  justify-content: space-between;
  gap: 12px;
}

.pet-dream-history-panel h3 {
  font-size: 14px;
}

.pet-dream-history-panel__header p,
.pet-dream-history-panel__state,
.pet-dream-history-panel__pagination,
.pet-dream-history-panel__page-error,
.pet-dream-history-panel__error-state,
.pet-dream-history-panel__detail-header small,
.pet-dream-history-panel__item-copy small {
  color: var(--dream-muted);
  font-size: 11px;
  line-height: 1.55;
}

.pet-dream-history-panel__header p {
  margin-top: 3px;
}

.pet-dream-history-panel__button,
.pet-dream-history-panel__pagination button,
.pet-dream-history-panel__text-button {
  border: 1px solid var(--dream-line);
  border-radius: 7px;
  padding: 7px 10px;
  background: color-mix(in srgb, var(--mac-accent, #0a84ff) 10%, transparent);
  color: var(--mac-accent, #0a84ff);
  cursor: pointer;
  font: inherit;
  font-size: 11px;
}

.pet-dream-history-panel__button:disabled,
.pet-dream-history-panel__pagination button:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

.pet-dream-history-panel__text-button {
  border: 0;
  padding: 2px 5px;
  background: transparent;
}

.pet-dream-history-panel__state,
.pet-dream-history-panel__error-state {
  min-height: 96px;
  border: 1px dashed var(--dream-line);
  border-radius: 9px;
  padding: 18px 12px;
  text-align: center;
}

.pet-dream-history-panel__error-state,
.pet-dream-history-panel__page-error,
.pet-dream-history-panel__image-error {
  color: #bd4f4f;
}

.pet-dream-history-panel__layout {
  display: grid;
  min-width: 0;
  grid-template-columns: minmax(190px, 0.4fr) minmax(0, 1fr);
  gap: 12px;
}

.pet-dream-history-panel__list-wrap,
.pet-dream-history-panel__detail {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 9px;
  border: 1px solid var(--dream-line);
  border-radius: 9px;
  padding: 9px;
  background: color-mix(in srgb, var(--settings-strong-surface, #f5f5f7) 45%, transparent);
}

.pet-dream-history-panel__list-wrap {
  min-height: 360px;
}

.pet-dream-history-panel__list {
  display: flex;
  min-height: 0;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 5px;
  overflow-y: auto;
}

.pet-dream-history-panel__item {
  display: flex;
  width: 100%;
  min-width: 0;
  align-items: center;
  gap: 8px;
  border: 1px solid transparent;
  border-radius: 7px;
  padding: 6px;
  background: transparent;
  color: var(--dream-ink);
  cursor: pointer;
  font: inherit;
  text-align: left;
}

.pet-dream-history-panel__item:hover,
.pet-dream-history-panel__item.is-selected {
  border-color: color-mix(in srgb, var(--mac-accent, #0a84ff) 42%, var(--dream-line));
  background: color-mix(in srgb, var(--mac-accent, #0a84ff) 10%, transparent);
}

.pet-dream-history-panel__thumb {
  display: flex;
  width: 48px;
  height: 48px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  border-radius: 6px;
  background: color-mix(in srgb, var(--dream-muted) 12%, transparent);
  color: var(--dream-muted);
  font-size: 10px;
}

.pet-dream-history-panel__thumb img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.pet-dream-history-panel__item-copy {
  display: flex;
  min-width: 0;
  flex: 1 1 auto;
  flex-direction: column;
  gap: 3px;
}

.pet-dream-history-panel__item-copy strong {
  overflow: hidden;
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-dream-history-panel__item-copy small {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pet-dream-history-panel__pagination {
  flex-wrap: wrap;
  border-top: 1px solid var(--dream-line);
  padding-top: 8px;
}

.pet-dream-history-panel__pagination span {
  flex: 1 1 auto;
  text-align: center;
}

.pet-dream-history-panel__page-error {
  align-items: flex-start;
  border-top: 1px solid var(--dream-line);
  padding-top: 8px;
}

.pet-dream-history-panel__detail {
  min-height: 360px;
  overflow-y: auto;
}

.pet-dream-history-panel__detail-header h4 {
  font-size: 13px;
}

.pet-dream-history-panel__detail-header small {
  display: block;
  margin-top: 3px;
}

.pet-dream-history-panel__detail-header > span {
  flex: 0 0 auto;
  border-radius: 999px;
  padding: 3px 7px;
  background: color-mix(in srgb, var(--dream-muted) 12%, transparent);
  color: var(--dream-muted);
  font-size: 10px;
}

.pet-dream-history-panel__image {
  display: flex;
  min-height: 150px;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  border: 1px solid var(--dream-line);
  border-radius: 8px;
  background: color-mix(in srgb, var(--settings-strong-surface, #f5f5f7) 62%, transparent);
  color: var(--dream-muted);
  font-size: 11px;
  text-align: center;
}

.pet-dream-history-panel__image img {
  display: block;
  max-width: 100%;
  max-height: 280px;
  object-fit: contain;
}

.pet-dream-history-panel__image-error {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px;
}

.pet-dream-history-panel__fields {
  display: flex;
  flex-direction: column;
  gap: 10px;
  font-size: 11px;
}

.pet-dream-history-panel__fields > div {
  min-width: 0;
}

.pet-dream-history-panel__fields dt {
  margin-bottom: 4px;
  color: var(--dream-muted);
  font-size: 10px;
}

.pet-dream-history-panel__fields dd {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  line-height: 1.6;
}

@media (max-width: 680px) {
  .pet-dream-history-panel__layout {
    grid-template-columns: minmax(0, 1fr);
  }

  .pet-dream-history-panel__list-wrap,
  .pet-dream-history-panel__detail {
    min-height: 0;
  }

  .pet-dream-history-panel__list {
    max-height: 300px;
  }
}
</style>
