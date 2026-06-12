import { resolve } from 'node:path'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// https://vitejs.dev/config/
export default defineConfig({
  resolve: {
    alias: {
      '@wailsio/runtime': resolve(__dirname, 'src/wails-runtime-compat/index.ts'),
    },
  },
  plugins: [vue()],
})
