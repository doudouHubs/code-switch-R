<script setup lang="ts">
defineProps<{
  loading: boolean
  message: string
}>()
</script>

<template>
  <section class="state-panel" :class="{ empty: !loading }">
    <div v-if="loading" class="state-orb"></div>
    <p>{{ message }}</p>
  </section>
</template>
