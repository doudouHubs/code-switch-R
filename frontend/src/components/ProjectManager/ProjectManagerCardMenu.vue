<script setup lang="ts">
import { Menu, MenuButton, MenuItem, MenuItems } from '@headlessui/vue'

interface ProjectManagerCardMenuAction {
  key: string
  label: string
  accent?: boolean
  danger?: boolean
}

defineProps<{
  label: string
  actions: ProjectManagerCardMenuAction[]
  loading?: boolean
  disabled?: boolean
}>()

const emit = defineEmits<{
  select: [key: string]
}>()
</script>

<template>
  <Menu as="div" class="card-action-menu" @click.stop>
    <MenuButton class="card-menu-trigger" :class="{ 'is-loading': loading }" type="button" :aria-label="label" :title="label" :disabled="disabled || loading">
      <span
        v-if="loading"
        class="card-menu-spinner"
        aria-hidden="true"
      ></span>
      <svg v-else viewBox="0 0 24 24" aria-hidden="true">
        <circle cx="12" cy="5" r="1.8" fill="currentColor" />
        <circle cx="12" cy="12" r="1.8" fill="currentColor" />
        <circle cx="12" cy="19" r="1.8" fill="currentColor" />
      </svg>
    </MenuButton>

    <transition
      enter-active-class="card-menu-enter"
      enter-from-class="card-menu-enter-from"
      enter-to-class="card-menu-enter-to"
      leave-active-class="card-menu-leave"
      leave-from-class="card-menu-leave-from"
      leave-to-class="card-menu-leave-to"
    >
      <MenuItems v-if="!loading" class="card-menu-popover">
        <MenuItem
          v-for="action in actions"
          :key="action.key"
          v-slot="{ active, close }"
        >
          <button
            type="button"
            :class="['card-menu-item', { active, accent: action.accent, danger: action.danger }]"
            @click.stop="close(); emit('select', action.key)"
          >
            {{ action.label }}
          </button>
        </MenuItem>
      </MenuItems>
    </transition>
  </Menu>
</template>
