import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      // 业务代码和自动生成 bindings 必须共享同一份 Wails 回调表，避免两个 runtime
      // 互相覆盖 window._wails.callResultHandler，导致原生调用 Promise 永久悬挂。
      '@wailsio/runtime': fileURLToPath(new URL('./src/wails-runtime-compat/index.ts', import.meta.url)),
    },
  },
})
