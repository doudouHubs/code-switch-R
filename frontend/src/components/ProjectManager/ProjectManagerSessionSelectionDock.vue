<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ArrowDownUp, Check, ChevronUp, Clock3, ListChecks, Trash2, X } from '@lucide/vue'
import { useI18n } from 'vue-i18n'
import type { SessionDeletionRange } from '../../services/projectManager'

const props = defineProps<{
  selectedCount: number
  totalCount: number
  allSelected: boolean
  busy: boolean
  range: SessionDeletionRange
}>()

const emit = defineEmits<{
  'select-all': []
  'invert-selection': []
  'update:range': [range: SessionDeletionRange]
  delete: []
  exit: []
}>()

const { t } = useI18n()
const rangeMenuOpen = ref(false)
const rangeMenuRef = ref<HTMLElement | null>(null)

const rangeOptions = computed(() => [
  {
    value: 'all' as const,
    label: t('components.projectManager.selection.rangeAll'),
  },
  {
    value: 'one_week' as const,
    label: t('components.projectManager.selection.rangeOneWeek'),
  },
  {
    value: 'three_weeks' as const,
    label: t('components.projectManager.selection.rangeThreeWeeks'),
  },
  {
    value: 'one_month' as const,
    label: t('components.projectManager.selection.rangeOneMonth'),
  },
])

const selectedRangeLabel = computed(
  () =>
    rangeOptions.value.find((option) => option.value === props.range)?.label ??
    t('components.projectManager.selection.rangeAll'),
)

const closeRangeMenu = () => {
  rangeMenuOpen.value = false
}

const toggleRangeMenu = () => {
  if (props.busy) {
    return
  }
  rangeMenuOpen.value = !rangeMenuOpen.value
}

const selectRange = (range: SessionDeletionRange) => {
  if (props.busy) {
    return
  }
  emit('update:range', range)
  closeRangeMenu()
}

const handleDocumentPointerDown = (event: PointerEvent) => {
  // 菜单从底部工具栏向上展开，点击页面其他区域时要主动收起，避免遮挡会话卡片。
  const target = event.target
  if (!(target instanceof Node) || !rangeMenuRef.value?.contains(target)) {
    closeRangeMenu()
  }
}

onMounted(() => {
  document.addEventListener('pointerdown', handleDocumentPointerDown)
})

onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', handleDocumentPointerDown)
})
</script>

<template>
  <footer class="session-selection-dock" aria-live="polite">
    <div class="session-selection-summary">
      <strong>
        {{ t('components.projectManager.selection.selectedCount', { count: selectedCount }) }}
      </strong>
      <span>
        {{ t('components.projectManager.selection.totalCount', { count: totalCount }) }}
      </span>
    </div>

    <div class="session-selection-actions">
      <div ref="rangeMenuRef" class="session-selection-range">
        <button
          class="session-selection-action session-selection-range-trigger"
          type="button"
          :aria-expanded="rangeMenuOpen"
          aria-haspopup="menu"
          aria-controls="project-manager-session-delete-range-menu"
          :disabled="busy"
          @click="toggleRangeMenu"
          @keydown.escape.stop="closeRangeMenu"
        >
          <Clock3 :size="20" aria-hidden="true" />
          <span>{{ selectedRangeLabel }}</span>
          <ChevronUp
            :size="16"
            aria-hidden="true"
            :class="{ 'is-open': rangeMenuOpen }"
          />
        </button>

        <div
          v-if="rangeMenuOpen"
          id="project-manager-session-delete-range-menu"
          class="session-selection-range-menu"
          role="menu"
          :aria-label="t('components.projectManager.selection.rangeLabel')"
          @keydown.escape.stop="closeRangeMenu"
        >
          <button
            v-for="option in rangeOptions"
            :key="option.value"
            class="session-selection-range-option"
            type="button"
            role="menuitemradio"
            :aria-checked="props.range === option.value"
            :disabled="busy"
            @click="selectRange(option.value)"
          >
            <Check
              v-if="props.range === option.value"
              :size="16"
              aria-hidden="true"
            />
            <span v-else class="session-selection-range-check" aria-hidden="true"></span>
            <span>{{ option.label }}</span>
          </button>
        </div>
      </div>

      <button
        class="session-selection-action"
        type="button"
        :aria-pressed="allSelected"
        :disabled="busy || totalCount === 0"
        @click="emit('select-all')"
      >
        <ListChecks :size="20" aria-hidden="true" />
        <span>{{ t('components.projectManager.selection.selectAll') }}</span>
      </button>

      <button
        class="session-selection-action"
        type="button"
        :disabled="busy || totalCount === 0"
        @click="emit('invert-selection')"
      >
        <ArrowDownUp :size="20" aria-hidden="true" />
        <span>{{ t('components.projectManager.selection.invert') }}</span>
      </button>

      <button
        class="session-selection-action danger"
        type="button"
        :disabled="busy || selectedCount === 0"
        @click="emit('delete')"
      >
        <Trash2 :size="20" aria-hidden="true" />
        <span>{{ t('components.projectManager.selection.delete') }}</span>
      </button>

      <button
        class="session-selection-action"
        type="button"
        :disabled="busy"
        @click="emit('exit')"
      >
        <X :size="20" aria-hidden="true" />
        <span>{{ t('components.projectManager.selection.exit') }}</span>
      </button>
    </div>
  </footer>
</template>
