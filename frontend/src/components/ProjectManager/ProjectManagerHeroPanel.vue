<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseButton from '../common/BaseButton.vue'
import BaseInput from '../common/BaseInput.vue'
import type { ProjectManagerViewMode } from './types'

const props = defineProps<{
  activeMode: ProjectManagerViewMode
  modelValue: string
  refreshing: boolean
  searching: boolean
  conversationSearch: boolean
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
  'change-mode': [mode: ProjectManagerViewMode]
  refresh: []
  clear: []
}>()

const { t } = useI18n()

const keywordModel = computed({
  get: () => props.modelValue,
  set: (value: string) => emit('update:modelValue', value),
})

const searchPlaceholder = computed(() =>
  t(
    props.conversationSearch
      ? 'components.projectManager.toolbar.conversationSearchPlaceholder'
      : 'components.projectManager.toolbar.searchPlaceholder',
  ),
)
</script>

<template>
  <section class="toolbar">
    <div class="mode-switch tab-group" role="tablist" :aria-label="t('components.projectManager.toolbar.modeLabel')">
      <button
        class="mode-pill tab-pill"
        :class="{ active: activeMode === 'project' }"
        type="button"
        @click="emit('change-mode', 'project')"
      >
        {{ t('components.projectManager.toolbar.projectMode') }}
      </button>
      <button
        class="mode-pill tab-pill"
        :class="{ active: activeMode === 'session' }"
        type="button"
        @click="emit('change-mode', 'session')"
      >
        {{ t('components.projectManager.toolbar.sessionMode') }}
      </button>
    </div>

    <div class="toolbar-actions">
      <div class="search-shell">
        <span
          v-if="searching"
          class="toolbar-search-spinner"
          role="status"
          :aria-label="t('components.projectManager.toolbar.searchingConversations')"
        ></span>
        <svg v-else viewBox="0 0 24 24" aria-hidden="true">
          <circle cx="11" cy="11" r="7"></circle>
          <path d="M20 20l-3.6-3.6"></path>
        </svg>
        <BaseInput
          v-model="keywordModel"
          class="search-input"
          :placeholder="searchPlaceholder"
        />
        <button
          v-if="modelValue"
          class="clear-btn"
          type="button"
          @click="emit('clear')"
        >
          ×
        </button>
      </div>
      <BaseButton variant="outline" :disabled="refreshing" :loading="refreshing" @click="emit('refresh')">
        {{ t('components.projectManager.toolbar.refresh') }}
      </BaseButton>
    </div>
  </section>
</template>
