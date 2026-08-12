<script setup lang="ts">
import { RouterView, useRoute, useRouter } from 'vue-router'
import { computed, onBeforeUnmount, onMounted } from 'vue'
import Sidebar from './components/Sidebar.vue'
import UpdateNotification from './components/common/UpdateNotification.vue'
import { Call, Events } from './wails-runtime-compat'

const applyTheme = () => {
  const userTheme = localStorage.getItem('theme')
  const systemPrefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches

  const isDark = userTheme === 'dark' || (!userTheme && systemPrefersDark)

  document.documentElement.classList.toggle('dark', isDark)
}

onMounted(() => {
  applyTheme()

  // 可监听系统主题变化自动更新（可选）
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
    applyTheme()
  })
})

const route = useRoute()
const router = useRouter()
const isTray = computed(() => route.path === '/tray')

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

// 独立桌宠窗只负责交互，设置和 Studio 必须在主窗口展示；事件桥同时覆盖
// 主窗口被隐藏的情况，先恢复宿主窗口再切换路由，避免用户点击后“无反应”。
const stopPetSettingsRequest = Events.On('pet.window.open-settings', (event) => {
  const raw = Array.isArray(event.data) && event.data.length === 1 ? event.data[0] : event.data
  const openStudio = isRecord(raw) && raw.openStudio === true
  void (async () => {
    try {
      await Call.ByName('main.AppService.ShowMainWindow')
    } catch {
      // 普通 Vite 预览没有 Wails 宿主，仍允许路由切换用于页面级验收。
    }
    await router.push({
      path: '/pet/settings',
      query: openStudio ? { studio: '1' } : {}
    })
  })()
})

onBeforeUnmount(() => stopPetSettingsRequest())
</script>

<template>
  <div v-if="isTray" class="tray-layout">
    <RouterView v-slot="{ Component }">
      <component :is="Component" />
    </RouterView>
  </div>
  <div v-else class="app-layout">
    <Sidebar />
    <main class="main-content">
      <RouterView v-slot="{ Component }">
        <keep-alive>
          <component :is="Component" />
        </keep-alive>
      </RouterView>
    </main>
    <!-- 全局更新通知 -->
    <UpdateNotification />
  </div>
</template>

<style scoped>
.tray-layout {
  width: 100vw;
  height: 100vh;
  overflow: hidden;
}

.app-layout {
  display: flex;
  height: 100vh;
  width: 100vw;
  overflow: hidden;
}

.main-content {
  flex: 1;
  overflow-y: auto;
  background: var(--mac-bg);
}
</style>
