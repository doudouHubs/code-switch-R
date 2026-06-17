<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseButton from '../common/BaseButton.vue'
import BaseInput from '../common/BaseInput.vue'
import BaseModal from '../common/BaseModal.vue'
import type { ProjectManagerRenameTarget } from './types'

const props = defineProps<{
  open: boolean
  targetType: ProjectManagerRenameTarget
  saving: boolean
  modelValue: string
}>()

const emit = defineEmits<{
  close: []
  save: []
  'update:modelValue': [value: string]
}>()

const { t } = useI18n()

const renameModel = computed({
  get: () => props.modelValue,
  set: (value: string) => emit('update:modelValue', value),
})
</script>

<template>
  <BaseModal
    :open="open"
    :title="targetType === 'project'
      ? t('components.projectManager.rename.projectTitle')
      : t('components.projectManager.rename.sessionTitle')"
    @close="emit('close')"
  >
    <div class="rename-body">
      <p class="rename-hint">{{ t('components.projectManager.rename.hint') }}</p>
      <BaseInput
        v-model="renameModel"
        :placeholder="t('components.projectManager.rename.placeholder')"
        @keydown.enter.prevent="emit('save')"
      />
      <div class="rename-actions">
        <BaseButton variant="outline" @click="emit('close')">
          {{ t('components.projectManager.rename.cancel') }}
        </BaseButton>
        <BaseButton :disabled="saving" :loading="saving" @click="emit('save')">
          {{ t('components.projectManager.rename.save') }}
        </BaseButton>
      </div>
    </div>
  </BaseModal>
</template>
