import { createApp } from 'vue'
import './style.css'
import { i18n, setupI18n } from './utils/i18n'
import { initTheme } from './utils/ThemeManager'

initTheme()
const isMac = navigator.userAgent.includes('Mac')
if (isMac) {
  document.documentElement.classList.add('mac')
}

// 桌宠是独立的透明 WebView 窗口，不能把主应用的渐变背景带进来；
// 这里在 Vue 挂载前清掉全局背景，避免首帧先闪出一整块不透明底色。
const isPetWindow = new URLSearchParams(window.location.search).get('appView') === 'pet'
if (isPetWindow) {
  document.documentElement.style.background = 'transparent'
  document.body.style.background = 'transparent'
  document.getElementById('app')?.setAttribute('style', 'background: transparent;')
}

async function bootstrap(){
    await setupI18n('zh')//默认语言或从后端读取
    if (isPetWindow) {
      const { default: PetWindowPage } = await import('./components/Pet/PetWindow.vue')
      createApp(PetWindowPage).use(i18n).mount('#app')
      return
    }

    const [{ default: App }, { default: router }] = await Promise.all([
      import('./App.vue'),
      import('./router/index')
    ])
    createApp(App).use(router).use(i18n).mount('#app')
}
bootstrap()
