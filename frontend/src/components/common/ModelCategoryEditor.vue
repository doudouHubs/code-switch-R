<template>
  <div class="model-category-editor">
    <div class="editor-header">
      <label class="editor-label">
        <span>{{ $t('components.provider.modelCategory.label') }}</span>
        <button
          type="button"
          class="help-icon"
          :data-tooltip="$t('components.provider.modelCategory.tooltip')"
        >
          <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
            <path
              d="M8 1a7 7 0 100 14A7 7 0 018 1zm0 13A6 6 0 118 2a6 6 0 010 12zm0-9.5a.75.75 0 01.75.75v4a.75.75 0 01-1.5 0v-4A.75.75 0 018 4.5zm0 7.5a1 1 0 100-2 1 1 0 000 2z"
              fill="currentColor"
            />
          </svg>
        </button>
      </label>
    </div>

    <div v-if="categoryList.length > 0" class="category-list">
      <div v-for="(item, index) in categoryList" :key="index" class="category-row">
        <code class="category-model">{{ item.model }}</code>
        <select
          class="category-select"
          :value="item.category"
          :aria-label="$t('components.provider.modelCategory.select', { model: item.model })"
          @change="updateCategory(item.model, ($event.target as HTMLSelectElement).value)"
        >
          <option v-for="option in categoryOptions" :key="option" :value="option">
            {{ $t(`components.provider.modelCategory.options.${option}`) }}
          </option>
        </select>
        <button
          type="button"
          class="category-remove"
          :aria-label="$t('components.provider.modelCategory.remove')"
          @click="removeCategory(item.model)"
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

    <div class="category-input-row">
      <BaseInput
        v-model="newModel"
        type="text"
        :placeholder="$t('components.provider.modelCategory.modelPlaceholder')"
        @keydown.enter.prevent="addCategory"
      />
      <select v-model="newCategory" class="category-select category-input-select">
        <option v-for="option in categoryOptions" :key="option" :value="option">
          {{ $t(`components.provider.modelCategory.options.${option}`) }}
        </option>
      </select>
      <BaseButton type="button" variant="outline" @click="addCategory">
        {{ $t('components.provider.modelCategory.add') }}
      </BaseButton>
    </div>

    <div class="help-text">
      {{ $t('components.provider.modelCategory.hint') }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import BaseInput from './BaseInput.vue'
import BaseButton from './BaseButton.vue'

type ModelCategory = 'chat' | 'speech' | 'embedding' | 'image' | 'video'

interface Props {
  modelValue?: Record<string, string>
}

interface Emits {
  (event: 'update:modelValue', value: Record<string, string>): void
}

const props = defineProps<Props>()
const emit = defineEmits<Emits>()

const categoryOptions: ModelCategory[] = ['chat', 'speech', 'embedding', 'image', 'video']
const newModel = ref('')
// 普通模型默认按 chat 处理；speech/image 等高风险能力必须由用户明确选择，
// 否则随手添加一个模型就会改变 provider 能力路由。
const newCategory = ref<ModelCategory>('chat')

const categoryList = computed(() => Object.entries(props.modelValue ?? {}).map(([model, category]) => ({ model, category })))

const addCategory = () => {
  const model = newModel.value.trim()
  if (!model) return
  emit('update:modelValue', { ...(props.modelValue ?? {}), [model]: newCategory.value })
  newModel.value = ''
}

const updateCategory = (model: string, category: string) => {
  if (!categoryOptions.includes(category as ModelCategory)) return
  emit('update:modelValue', { ...(props.modelValue ?? {}), [model]: category })
}

const removeCategory = (model: string) => {
  const next = { ...(props.modelValue ?? {}) }
  delete next[model]
  emit('update:modelValue', next)
}

watch(
  () => props.modelValue,
  (value) => {
    if (value === undefined) emit('update:modelValue', {})
  },
  { immediate: true }
)
</script>

<style scoped>
.model-category-editor { display: flex; flex-direction: column; gap: 12px; }
.editor-header { display: flex; align-items: center; justify-content: space-between; }
.editor-label { display: flex; align-items: center; gap: 6px; font-weight: 500; font-size: 0.875rem; color: var(--foreground); }
.help-icon, .category-remove { display: inline-flex; align-items: center; justify-content: center; border: none; background: none; color: var(--foreground-muted); cursor: pointer; border-radius: 4px; }
.help-icon { padding: 2px; cursor: help; }
.help-icon:hover { color: var(--foreground); background-color: var(--background-hover); }
.category-remove { padding: 4px; flex: 0 0 auto; }
.category-remove:hover { color: var(--error); background-color: var(--error-bg); }
.category-list { display: flex; flex-direction: column; gap: 8px; padding: 10px; background-color: var(--background-secondary); border-radius: 8px; }
.category-row, .category-input-row { display: flex; align-items: center; gap: 8px; }
.category-row { padding: 8px 10px; background-color: var(--background); border: 1px solid var(--border); border-radius: 6px; }
.category-model { flex: 1; min-width: 0; color: var(--foreground); word-break: break-all; }
.category-select { min-height: 34px; padding: 0 28px 0 10px; border: 1px solid var(--border); border-radius: 6px; background: var(--background); color: var(--foreground); }
.category-input-row :deep(input) { flex: 1; font-family: 'SF Mono', 'Menlo', 'Monaco', 'Courier New', monospace; }
.category-input-select { flex: 0 0 130px; }
.help-text { padding: 12px; background-color: var(--background-secondary); border-radius: 8px; font-size: 0.8125rem; line-height: 1.5; color: var(--foreground-muted); }
</style>
