import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import i18n, { initI18n } from './i18n'
import { useAppStore } from '@/stores/app'
import { installChunkLoadRecovery } from '@/utils/chunkLoadRecovery'
// Self-hosted brand fonts (offline / China-safe, no Google CDN)
import '@fontsource-variable/fraunces' // 衬线标题 Latin (近 Tiempos/Anthropic 编辑感)
import '@fontsource-variable/space-grotesk' // 无衬线正文/UI Latin (方头怪趣，近 Styrene)
import '@fontsource-variable/jetbrains-mono' // 等宽数据/账本 Latin (tabular + slashed-zero，承载全站数字)
// 中文字体 (按需权重；CJK 按 unicode-range 分片，浏览器只取用到的子集)
import '@fontsource/noto-sans-sc/400.css' // 正文中文
import '@fontsource/noto-sans-sc/500.css'
import '@fontsource/noto-sans-sc/700.css'
import '@fontsource/noto-serif-sc/600.css' // 标题中文 (宋体衬线)
import '@fontsource/noto-serif-sc/700.css'
import './style.css'

installChunkLoadRecovery()

function initThemeClass() {
  const savedTheme = localStorage.getItem('theme')
  const shouldUseDark =
    savedTheme === 'dark' ||
    (!savedTheme && window.matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.classList.toggle('dark', shouldUseDark)
}

async function bootstrap() {
  // Apply theme class globally before app mount to keep all routes consistent.
  initThemeClass()

  const app = createApp(App)
  const pinia = createPinia()
  app.use(pinia)

  // Initialize settings from injected config BEFORE mounting (prevents flash)
  // This must happen after pinia is installed but before router and i18n
  const appStore = useAppStore()
  appStore.initFromInjectedConfig()

  // Set document title immediately after config is loaded
  if (appStore.siteName && appStore.siteName !== 'Sub2API') {
    document.title = `${appStore.siteName} - AI API Gateway`
  }

  await initI18n()

  app.use(router)
  app.use(i18n)

  // 等待路由器完成初始导航后再挂载，避免竞态条件导致的空白渲染
  await router.isReady()
  app.mount('#app')
}

bootstrap()
