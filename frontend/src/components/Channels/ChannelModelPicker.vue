<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { Bot, Check, ChevronDown } from '@lucide/vue'

export interface ChannelModelOption {
  platform: string
  providerId: string
  providerName: string
  modelId: string
  modelName: string
}

const props = withDefaults(
  defineProps<{
    model: string | null | undefined
    providerId: string | null | undefined
    options: ChannelModelOption[]
    defaultLabel: string
    defaultDescription?: string
    disabled?: boolean
  }>(),
  { defaultDescription: '', disabled: false }
)

const emit = defineEmits<{
  (event: 'select', option: ChannelModelOption | null): void
}>()

const root = ref<HTMLElement | null>(null)
const open = ref(false)

const selectedOption = computed(() => props.options.find(
  (option) => option.modelId === props.model && option.providerId === props.providerId,
))

const displayLabel = computed(() => selectedOption.value?.modelName || props.model || props.defaultLabel)

const groups = computed(() => {
  const grouped = new Map<string, { key: string; label: string; options: ChannelModelOption[] }>()
  for (const option of props.options) {
    const key = `${option.platform}:${option.providerId}`
    const group = grouped.get(key) ?? {
      key,
      label: `${option.platform} · ${option.providerName}`,
      options: [],
    }
    group.options.push(option)
    grouped.set(key, group)
  }
  return [...grouped.values()]
})

function select(option: ChannelModelOption | null): void {
  emit('select', option)
  open.value = false
}

function handleDocumentPointerDown(event: PointerEvent): void {
  const target = event.target
  if (!(target instanceof Node) || !root.value?.contains(target)) {
    open.value = false
  }
}

function handleKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') open.value = false
}

onMounted(() => {
  document.addEventListener('pointerdown', handleDocumentPointerDown)
})

onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', handleDocumentPointerDown)
})
</script>

<template>
  <div ref="root" class="channel-model-picker" @keydown="handleKeydown">
    <button
      class="model-trigger"
      type="button"
      :disabled="props.disabled"
      :aria-expanded="open"
      @click="open = !open"
    >
      <Bot :size="13" class="model-trigger-icon" />
      <span class="model-trigger-label" :class="{ placeholder: !selectedOption && !props.model }">{{ displayLabel }}</span>
      <ChevronDown :size="13" class="model-trigger-chevron" />
    </button>

    <div v-if="open" class="model-popover" role="listbox">
      <button
        class="model-option"
        :class="{ selected: !props.model }"
        type="button"
        role="option"
        :aria-selected="!props.model"
        @click="select(null)"
      >
        <Check v-if="!props.model" :size="13" class="model-check" />
        <span v-else class="model-check-spacer" />
        <span class="model-option-copy">
          <span>{{ props.defaultLabel }}</span>
          <small>{{ props.defaultDescription }}</small>
        </span>
      </button>

      <div v-if="groups.length" class="model-separator" />
      <section v-for="group in groups" :key="group.key" class="model-group">
        <div class="model-group-label">{{ group.label }}</div>
        <button
          v-for="option in group.options"
          :key="`${option.providerId}:${option.modelId}`"
          class="model-option"
          :class="{ selected: option.modelId === props.model && option.providerId === props.providerId }"
          type="button"
          role="option"
          :aria-selected="option.modelId === props.model && option.providerId === props.providerId"
          @click="select(option)"
        >
          <Check v-if="option.modelId === props.model && option.providerId === props.providerId" :size="13" class="model-check" />
          <span v-else class="model-check-spacer" />
          <span class="model-option-name">{{ option.modelName || option.modelId }}</span>
        </button>
      </section>

      <div v-if="!groups.length" class="model-empty">No configured chat models</div>
    </div>
  </div>
</template>

<style scoped>
.channel-model-picker {
  position: relative;
  width: 100%;
}

.model-trigger {
  display: flex;
  width: 100%;
  height: 32px;
  align-items: center;
  gap: 8px;
  padding: 0 10px;
  border: 1px solid var(--mac-border);
  border-radius: 6px;
  background: var(--mac-surface);
  color: var(--mac-text);
  font: inherit;
  font-size: 12px;
  text-align: left;
  cursor: pointer;
  transition: background 0.15s ease, border-color 0.15s ease;
}

.model-trigger:hover:not(:disabled) {
  border-color: color-mix(in srgb, var(--mac-accent) 42%, var(--mac-border));
  background: var(--mac-surface-strong);
}

.model-trigger:disabled {
  cursor: not-allowed;
  opacity: 0.58;
}

.model-trigger-icon,
.model-trigger-chevron {
  flex: 0 0 auto;
  color: var(--mac-text-secondary);
}

.model-trigger-label {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.model-trigger-label.placeholder {
  color: var(--mac-text-secondary);
}

.model-popover {
  position: absolute;
  z-index: 30;
  top: calc(100% + 4px);
  left: 0;
  width: min(320px, calc(100vw - 32px));
  max-height: 288px;
  overflow-y: auto;
  padding: 4px;
  border: 1px solid var(--mac-border);
  border-radius: 7px;
  background: var(--mac-surface);
  box-shadow: 0 14px 36px rgba(15, 23, 42, 0.18);
}

.model-option {
  display: flex;
  width: 100%;
  align-items: center;
  gap: 7px;
  min-height: 28px;
  padding: 6px 8px;
  border: 0;
  border-radius: 4px;
  background: transparent;
  color: var(--mac-text);
  font: inherit;
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}

.model-option:hover,
.model-option.selected {
  background: var(--mac-surface-strong);
}

.model-check,
.model-check-spacer {
  width: 13px;
  flex: 0 0 13px;
}

.model-check {
  color: var(--mac-accent);
}

.model-option-copy,
.model-option-name {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.model-option-copy {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.model-option-copy small {
  overflow: hidden;
  color: var(--mac-text-secondary);
  font-size: 10px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.model-group-label {
  padding: 7px 8px 4px;
  color: var(--mac-text-secondary);
  font-size: 10px;
  letter-spacing: 0.05em;
  text-transform: uppercase;
}

.model-separator {
  height: 1px;
  margin: 4px 0;
  background: var(--mac-divider);
}

.model-empty {
  padding: 14px 8px;
  color: var(--mac-text-secondary);
  font-size: 11px;
  text-align: center;
}
</style>
