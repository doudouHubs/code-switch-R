<script setup lang="ts">
import { Call } from '../../wails-runtime-compat'
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { DEFAULT_PET_ID, type PetMemoryRecord } from './petTypes'

interface PetMemoryPanelProps {
  petId?: string
}

const props = withDefaults(defineProps<PetMemoryPanelProps>(), {
  petId: DEFAULT_PET_ID
})
const { t } = useI18n()

const PET_MEMORY_SERVICE = 'codeswitch/services.PetMemoryService'
const PET_MEMORY_METHODS = {
  list: `${PET_MEMORY_SERVICE}.List`,
  append: `${PET_MEMORY_SERVICE}.Append`,
  remove: `${PET_MEMORY_SERVICE}.Remove`,
  clear: `${PET_MEMORY_SERVICE}.Clear`
} as const

const entries = ref<PetMemoryRecord[]>([])
const loading = ref(false)
const actionLoading = ref(false)
const draft = ref('')
const confirmClear = ref(false)
const errorMessage = ref('')
let clearConfirmTimer: number | undefined

const isEditable = computed(() => props.petId === DEFAULT_PET_ID)

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function normalizeMemoryRecords(value: unknown, petId: string): PetMemoryRecord[] {
  if (!Array.isArray(value)) return []
  return value
    .map((entry, index) => {
      const item = isRecord(entry) ? entry : {}
      const text = typeof item.text === 'string' ? item.text.trim() : ''
      if (!text) return null
      return {
        petId: typeof item.petId === 'string' ? item.petId : petId,
        id: typeof item.id === 'string' && item.id ? item.id : `memory-${index}`,
        date: typeof item.date === 'string' ? item.date : '',
        text,
        createdAt: typeof item.createdAt === 'number' ? item.createdAt : 0,
        updatedAt: typeof item.updatedAt === 'number' ? item.updatedAt : 0
      }
    })
    .filter((entry): entry is PetMemoryRecord => entry !== null)
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function formatMemoryDate(date: string): string {
  return date || t('pet.common.unknownDate')
}

function clearConfirmState(): void {
  confirmClear.value = false
  if (clearConfirmTimer !== undefined) {
    window.clearTimeout(clearConfirmTimer)
    clearConfirmTimer = undefined
  }
}

async function refresh(): Promise<void> {
  if (!isEditable.value) {
    entries.value = []
    return
  }
  loading.value = true
  errorMessage.value = ''
  try {
    const raw = await Call.ByName(PET_MEMORY_METHODS.list)
    entries.value = normalizeMemoryRecords(raw, props.petId)
  } catch (error) {
    entries.value = []
    errorMessage.value = errorText(error)
  } finally {
    loading.value = false
  }
}

async function addMemory(): Promise<void> {
  if (!isEditable.value || actionLoading.value) return
  const text = draft.value.trim()
  if (!text) return
  actionLoading.value = true
  errorMessage.value = ''
  try {
    const raw = await Call.ByName(PET_MEMORY_METHODS.append, [text])
    entries.value = normalizeMemoryRecords(raw, props.petId)
    draft.value = ''
  } catch (error) {
    errorMessage.value = errorText(error)
  } finally {
    actionLoading.value = false
  }
}

async function removeMemory(id: string): Promise<void> {
  if (!isEditable.value || actionLoading.value || !id) return
  actionLoading.value = true
  errorMessage.value = ''
  try {
    await Call.ByName(PET_MEMORY_METHODS.remove, id)
    await refresh()
  } catch (error) {
    errorMessage.value = errorText(error)
  } finally {
    actionLoading.value = false
  }
}

async function clearAll(): Promise<void> {
  if (!isEditable.value || actionLoading.value) return
  if (!confirmClear.value) {
    confirmClear.value = true
    if (clearConfirmTimer !== undefined) window.clearTimeout(clearConfirmTimer)
    clearConfirmTimer = window.setTimeout(clearConfirmState, 3_000)
    return
  }

  clearConfirmState()
  actionLoading.value = true
  errorMessage.value = ''
  try {
    await Call.ByName(PET_MEMORY_METHODS.clear)
    entries.value = []
  } catch (error) {
    errorMessage.value = errorText(error)
  } finally {
    actionLoading.value = false
  }
}

watch(
  () => props.petId,
  () => {
    clearConfirmState()
    draft.value = ''
    errorMessage.value = ''
    entries.value = []
    if (isEditable.value) void refresh()
  },
  { immediate: true }
)

onUnmounted(() => {
  clearConfirmState()
})
</script>

<template>
  <section class="pet-memory-panel">
    <p class="pet-memory-panel__description">{{ t('pet.memory.subtitle') }}</p>

    <div v-if="!isEditable" class="pet-memory-panel__state">
      {{ t('pet.memory.unavailable', { petId: props.petId }) }}
    </div>

    <template v-else>
      <div class="pet-memory-panel__toolbar">
        <p class="pet-memory-panel__count">{{ t('pet.memory.count', { count: entries.length }) }}</p>
        <div class="pet-memory-panel__actions">
          <button
            type="button"
            class="pet-memory-panel__button"
            :disabled="loading || actionLoading"
            @click="refresh"
          >
            <span class="pet-memory-panel__button-icon is-refresh" :class="{ 'is-spinning': loading }" aria-hidden="true">↻</span>
            {{ loading ? t('pet.common.loading') : t('pet.common.refresh') }}
          </button>
          <!-- 记忆当前由 SQLite 统一管理，没有可安全暴露的独立文件路径。 -->
          <button
            type="button"
            class="pet-memory-panel__button"
            disabled
            :title="t('pet.memory.openFile')"
          >
            <span class="pet-memory-panel__button-icon is-file" aria-hidden="true">□</span>
            {{ t('pet.memory.openFile') }}
          </button>
          <button
            type="button"
            class="pet-memory-panel__button pet-memory-panel__button--clear"
            :class="{ 'is-confirming': confirmClear }"
            :disabled="entries.length === 0 || actionLoading"
            @click="clearAll"
          >
            <span class="pet-memory-panel__button-icon is-trash" aria-hidden="true">×</span>
            {{ confirmClear ? t('pet.memory.confirmClear') : t('pet.memory.clear') }}
          </button>
        </div>
      </div>

      <form class="pet-memory-panel__add" @submit.prevent="addMemory">
        <input
          v-model="draft"
          type="text"
          maxlength="120"
          :placeholder="t('pet.memory.inputPlaceholder')"
          :disabled="actionLoading"
        />
        <button type="submit" class="pet-memory-panel__button pet-memory-panel__button--add" :disabled="!draft.trim() || actionLoading">
          <span class="pet-memory-panel__button-icon is-plus" aria-hidden="true">+</span>
          {{ actionLoading ? t('pet.common.processing') : t('pet.memory.add') }}
        </button>
      </form>

      <div v-if="errorMessage" class="pet-memory-panel__error">
        <span>{{ t('pet.memory.operationFailed', { error: errorMessage }) }}</span>
        <button type="button" class="pet-memory-panel__button" @click="refresh">{{ t('pet.common.retry') }}</button>
      </div>

      <div v-if="loading" class="pet-memory-panel__state pet-memory-panel__state--loading">
        <span class="pet-memory-panel__state-icon is-spinner" aria-hidden="true"></span>
        <span>{{ t('pet.memory.loading') }}</span>
      </div>
      <div v-else-if="!errorMessage && entries.length === 0" class="pet-memory-panel__state pet-memory-panel__state--empty">
        <span class="pet-memory-panel__state-icon is-book" aria-hidden="true">▤</span>
        <span>{{ t('pet.memory.empty') }}</span>
      </div>
      <div v-else class="pet-memory-panel__list">
        <article v-for="entry in entries" :key="entry.id" class="pet-memory-panel__entry">
          <span class="pet-memory-panel__date">{{ formatMemoryDate(entry.date) }}</span>
          <p>{{ entry.text }}</p>
          <button
            type="button"
            class="pet-memory-panel__delete"
            :disabled="actionLoading"
            :title="t('pet.memory.deleteTitle')"
            @click="removeMemory(entry.id)"
          >
            <span aria-hidden="true">×</span>
          </button>
        </article>
      </div>
    </template>
  </section>
</template>

<style scoped>
.pet-memory-panel {
  --memory-ink: var(--settings-ink, var(--mac-text, #1d1d1f));
  --memory-muted: var(--settings-muted, var(--mac-text-secondary, #6e6e73));
  --memory-line: var(--settings-line, var(--mac-border, rgba(15, 23, 42, 0.12)));
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 16px;
  color: var(--memory-ink);
}

.pet-memory-panel__toolbar,
.pet-memory-panel__add,
.pet-memory-panel__error,
.pet-memory-panel__entry {
  display: flex;
  align-items: center;
}

.pet-memory-panel p {
  margin: 0;
}

.pet-memory-panel__toolbar {
  justify-content: space-between;
  gap: 12px;
}

.pet-memory-panel__description,
.pet-memory-panel__count,
.pet-memory-panel__state {
  color: var(--memory-muted);
  font-size: 12px;
  line-height: 1.55;
}

.pet-memory-panel__description {
  max-width: 58ch;
}

.pet-memory-panel__button,
.pet-memory-panel__delete {
  display: inline-flex;
  min-height: 28px;
  align-items: center;
  justify-content: center;
  gap: 5px;
  border: 1px solid var(--memory-line);
  border-radius: 6px;
  padding: 5px 9px;
  background: transparent;
  color: var(--memory-ink);
  cursor: pointer;
  font: inherit;
  font-size: 12px;
  line-height: 1;
  white-space: nowrap;
  transition: border-color 120ms ease, background-color 120ms ease, color 120ms ease;
}

.pet-memory-panel__button:hover:not(:disabled) {
  border-color: color-mix(in srgb, var(--mac-accent, #0a84ff) 42%, var(--memory-line));
  background: color-mix(in srgb, var(--mac-accent, #0a84ff) 7%, transparent);
}

.pet-memory-panel__button:focus-visible,
.pet-memory-panel__delete:focus-visible {
  outline: 2px solid color-mix(in srgb, var(--mac-accent, #0a84ff) 55%, transparent);
  outline-offset: 1px;
}

.pet-memory-panel__button:disabled,
.pet-memory-panel__delete:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

.pet-memory-panel__button--clear.is-confirming {
  border-color: color-mix(in srgb, #bd4f4f 42%, var(--memory-line));
  background: color-mix(in srgb, #bd4f4f 9%, transparent);
  color: #bd4f4f;
}

.pet-memory-panel__button-icon {
  display: inline-grid;
  width: 13px;
  height: 13px;
  place-items: center;
  font-size: 15px;
  line-height: 1;
}

.pet-memory-panel__button-icon.is-refresh {
  font-size: 16px;
}

.pet-memory-panel__button-icon.is-trash {
  font-size: 17px;
  font-weight: 400;
}

.pet-memory-panel__button-icon.is-plus {
  font-size: 17px;
}

.pet-memory-panel__button-icon.is-spinning,
.pet-memory-panel__state-icon.is-spinner {
  animation: pet-memory-spin 800ms linear infinite;
}

.pet-memory-panel__actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
}

.pet-memory-panel__add {
  gap: 8px;
}

.pet-memory-panel__add input {
  box-sizing: border-box;
  min-width: 0;
  flex: 1 1 auto;
  border: 1px solid var(--memory-line);
  border-radius: 6px;
  padding: 7px 9px;
  background: color-mix(in srgb, var(--settings-strong-surface, #f5f5f7) 74%, transparent);
  color: var(--memory-ink);
  font: inherit;
  font-size: 13px;
  outline: none;
}

.pet-memory-panel__add input:focus {
  border-color: var(--mac-accent, #0a84ff);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--mac-accent, #0a84ff) 18%, transparent);
}

.pet-memory-panel__error {
  justify-content: space-between;
  gap: 10px;
  border: 1px solid color-mix(in srgb, #bd4f4f 40%, var(--memory-line));
  border-radius: 8px;
  padding: 9px 12px;
  color: #bd4f4f;
  font-size: 12px;
}

.pet-memory-panel__state {
  display: flex;
  min-height: 112px;
  align-items: center;
  justify-content: center;
  gap: 8px;
  border: 1px dashed var(--memory-line);
  border-radius: 8px;
  padding: 28px 16px;
  text-align: center;
}

.pet-memory-panel__state-icon {
  display: inline-grid;
  width: 20px;
  height: 20px;
  place-items: center;
  color: color-mix(in srgb, var(--memory-muted) 58%, transparent);
}

.pet-memory-panel__state-icon.is-book {
  font-size: 20px;
  line-height: 1;
}

.pet-memory-panel__state-icon.is-spinner {
  border: 2px solid color-mix(in srgb, var(--memory-muted) 22%, transparent);
  border-top-color: var(--mac-accent, #0a84ff);
  border-radius: 50%;
}

.pet-memory-panel__list {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.pet-memory-panel__entry {
  min-width: 0;
  align-items: flex-start;
  gap: 8px;
  border: 1px solid var(--memory-line);
  border-radius: 8px;
  padding: 8px 12px;
  background: color-mix(in srgb, var(--settings-strong-surface, #f5f5f7) 40%, transparent);
}

.pet-memory-panel__date {
  flex: 0 0 auto;
  border-radius: 4px;
  padding: 3px 6px;
  background: color-mix(in srgb, var(--memory-muted) 12%, transparent);
  color: var(--memory-muted);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}

.pet-memory-panel__entry p {
  min-width: 0;
  flex: 1 1 auto;
  color: var(--memory-ink);
  font-size: 13px;
  line-height: 1.55;
  overflow-wrap: anywhere;
}

.pet-memory-panel__delete {
  flex: 0 0 auto;
  border: 0;
  min-height: 22px;
  padding: 2px 5px;
  background: transparent;
  color: var(--memory-muted);
  font-size: 18px;
}

.pet-memory-panel__delete:hover {
  color: #bd4f4f;
}

@keyframes pet-memory-spin {
  to {
    transform: rotate(360deg);
  }
}

@media (max-width: 640px) {
  .pet-memory-panel__toolbar {
    align-items: flex-start;
    flex-direction: column;
  }

  .pet-memory-panel__actions {
    width: 100%;
    justify-content: flex-start;
  }

  .pet-memory-panel__add {
    align-items: stretch;
    flex-direction: column;
  }

  .pet-memory-panel__add .pet-memory-panel__button {
    width: 100%;
  }

  .pet-memory-panel__entry {
    flex-wrap: wrap;
  }

  .pet-memory-panel__entry p {
    flex-basis: calc(100% - 10px);
    order: 3;
  }
}
</style>
