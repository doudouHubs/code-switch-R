<template>
  <div class="model-reasoning-editor">
    <div class="editor-header">
      <label class="editor-label">
        <span>{{ $t('components.provider.modelReasoning.label') }}</span>
        <button
          type="button"
          class="help-icon"
          :data-tooltip="$t('components.provider.modelReasoning.tooltip')"
          :aria-label="$t('components.provider.modelReasoning.tooltip')"
        >
          <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
            <path
              d="M8 1a7 7 0 100 14A7 7 0 008 1zm0 13A6 6 0 118 2a6 6 0 010 12zm0-9.5a.75.75 0 01.75.75v4a.75.75 0 01-1.5 0v-4A.75.75 0 018 4.5zm0 7.5a1 1 0 100-2 1 1 0 000 2z"
              fill="currentColor"
            />
          </svg>
        </button>
      </label>
    </div>

    <div v-if="reasoningList.length > 0" class="reasoning-list">
      <div v-for="item in reasoningList" :key="item.model" class="reasoning-row">
        <code class="reasoning-model" :class="{ wildcard: item.model.includes('*') }">{{ item.model }}</code>
        <div class="reasoning-levels">
          <label v-for="level in REASONING_LEVELS" :key="`${item.model}:${level}`" class="reasoning-level">
            <input
              type="checkbox"
              :checked="item.levels.includes(level)"
              :aria-label="$t('components.provider.modelReasoning.select', { model: item.model, level: $t(`components.provider.modelReasoning.options.${level}`) })"
              @change="updateLevel(item.model, level, ($event.target as HTMLInputElement).checked)"
            />
            <span>{{ $t(`components.provider.modelReasoning.options.${level}`) }}</span>
          </label>
        </div>
        <button
          type="button"
          class="reasoning-remove"
          :aria-label="$t('components.provider.modelReasoning.remove')"
          @click="removeRule(item.model)"
        >
          <svg viewBox="0 0 12 12" width="10" height="10" aria-hidden="true">
            <path
              d="M3 3l6 6M9 3l-6 6"
              stroke="currentColor"
              stroke-width="1.5"
              stroke-linecap="round"
            />
          </svg>
        </button>
      </div>
    </div>

    <div class="reasoning-input-row">
      <BaseInput
        v-model="newModel"
        type="text"
        :placeholder="$t('components.provider.modelReasoning.modelPlaceholder')"
        @keydown.enter.prevent="addRule"
      />
      <div class="new-levels">
        <label v-for="level in REASONING_LEVELS" :key="`new:${level}`" class="reasoning-level">
          <input v-model="newLevels" type="checkbox" :value="level" />
          <span>{{ $t(`components.provider.modelReasoning.options.${level}`) }}</span>
        </label>
      </div>
      <BaseButton
        type="button"
        variant="outline"
        :disabled="!newModel.trim() || newLevels.length === 0"
        @click="addRule"
      >
        {{ $t('components.provider.modelReasoning.add') }}
      </BaseButton>
    </div>

    <div class="help-text">
      {{ $t('components.provider.modelReasoning.hint') }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import BaseInput from './BaseInput.vue'
import BaseButton from './BaseButton.vue'

const REASONING_LEVELS = ['none', 'minimal', 'low', 'medium', 'high'] as const
type ReasoningLevel = (typeof REASONING_LEVELS)[number]

interface Props {
  modelValue?: Record<string, string[]>
}

interface Emits {
  (event: 'update:modelValue', value: Record<string, string[]>): void
}

const props = defineProps<Props>()
const emit = defineEmits<Emits>()

function normalizeLevels(value: unknown): ReasoningLevel[] {
  if (!Array.isArray(value)) return []
  return REASONING_LEVELS.filter((level) => value.includes(level))
}

const reasoningList = computed(() => Object.entries(props.modelValue ?? {})
  .map(([model, levels]) => ({ model, levels: normalizeLevels(levels) }))
  .filter((item) => item.model.trim() && item.levels.length > 0))

const newModel = ref('')
const newLevels = ref<ReasoningLevel[]>([])

function emitRules(next: Record<string, string[]>): void {
  emit('update:modelValue', next)
}

function addRule(): void {
  const model = newModel.value.trim()
  const levels = normalizeLevels(newLevels.value)
  if (!model || levels.length === 0) return

  // 能力声明必须显式来自用户配置；不根据模型名猜测，避免 Agent 把不支持的参数发给上游。
  emitRules({ ...(props.modelValue ?? {}), [model]: levels })
  newModel.value = ''
  newLevels.value = []
}

function updateLevel(model: string, level: ReasoningLevel, checked: boolean): void {
  const current = normalizeLevels(props.modelValue?.[model])
  const nextLevels = checked
    ? [...current, level]
    : current.filter((item) => item !== level)
  const next = { ...(props.modelValue ?? {}) }

  // 没有任何等级的规则对 Agent 没有意义，直接移除而不是持久化空能力声明。
  if (nextLevels.length === 0) delete next[model]
  else next[model] = normalizeLevels(nextLevels)
  emitRules(next)
}

function removeRule(model: string): void {
  const next = { ...(props.modelValue ?? {}) }
  delete next[model]
  emitRules(next)
}

watch(
  () => props.modelValue,
  (value) => {
    if (value === undefined) emitRules({})
  },
  { immediate: true }
)
</script>

<style scoped>
.model-reasoning-editor { display: flex; flex-direction: column; gap: 12px; }
.editor-header { display: flex; align-items: center; justify-content: space-between; }
.editor-label { display: flex; align-items: center; gap: 6px; font-weight: 500; font-size: 0.875rem; color: var(--foreground); }
.help-icon, .reasoning-remove { display: inline-flex; align-items: center; justify-content: center; border: none; background: none; color: var(--foreground-muted); cursor: pointer; border-radius: 4px; }
.help-icon { padding: 2px; cursor: help; }
.help-icon:hover { color: var(--foreground); background-color: var(--background-hover); }
.reasoning-remove { padding: 4px; flex: 0 0 auto; }
.reasoning-remove:hover { color: var(--error); background-color: var(--error-bg); }
.reasoning-list { display: flex; flex-direction: column; gap: 8px; padding: 10px; background-color: var(--background-secondary); border-radius: 8px; }
.reasoning-row { display: flex; align-items: center; gap: 10px; padding: 8px 10px; background-color: var(--background); border: 1px solid var(--border); border-radius: 6px; }
.reasoning-model { flex: 1 1 170px; min-width: 120px; color: var(--foreground); overflow-wrap: anywhere; }
.reasoning-model.wildcard { color: var(--accent); }
.reasoning-levels, .new-levels { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.reasoning-level { display: inline-flex; align-items: center; gap: 4px; color: var(--foreground-muted); font-size: 0.75rem; white-space: nowrap; }
.reasoning-level input { accent-color: var(--accent); }
.reasoning-input-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.reasoning-input-row :deep(input[type='text']) { flex: 1 1 220px; font-family: 'SF Mono', 'Menlo', 'Monaco', 'Courier New', monospace; }
.new-levels { flex: 1 1 300px; }
.help-text { padding: 12px; background-color: var(--background-secondary); border-radius: 8px; font-size: 0.8125rem; line-height: 1.5; color: var(--foreground-muted); }
@media (max-width: 720px) {
  .reasoning-row { align-items: flex-start; flex-wrap: wrap; }
  .reasoning-levels { flex-basis: 100%; }
}
</style>
