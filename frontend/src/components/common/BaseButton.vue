<template>
  <button :type="type" :class="['btn', variantClass, { 'is-loading': loading }]" :disabled="disabled || loading" v-bind="$attrs">
    <span
      v-if="loading"
      class="btn-spinner"
      aria-hidden="true"
    ></span>
    <span class="btn-label">
      <slot />
    </span>
  </button>
</template>

<script setup lang="ts">
import { computed, useAttrs } from 'vue'

const props = withDefaults(
  defineProps<{
    variant?: 'primary' | 'outline' | 'danger'
    type?: 'button' | 'submit' | 'reset'
    disabled?: boolean
    loading?: boolean
  }>(),
  {
    variant: 'primary',
    type: 'button',
    disabled: false,
    loading: false,
  },
)

useAttrs()

const variantClass = computed(() => `btn-${props.variant}`)
</script>
