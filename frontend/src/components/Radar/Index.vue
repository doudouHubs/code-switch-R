<template>
  <main class="radar-page" aria-label="Codex Radar">
    <iframe
      class="radar-frame"
      src="https://codexradar.com/"
      title="Codex Radar"
      loading="eager"
      allow="fullscreen"
      referrerpolicy="strict-origin-when-cross-origin"
    ></iframe>
  </main>
</template>

<style scoped>
.radar-page {
  width: 100%;
  height: 100%;
  min-height: 0;
  overflow: hidden;
  background: #ffffff;
}

.radar-frame {
  display: block;
  width: 100%;
  height: 100%;
  min-height: 100%;
  border: 0;
}
</style>
