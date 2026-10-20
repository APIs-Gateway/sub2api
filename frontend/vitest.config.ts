import { defineConfig } from 'vitest/config'
import { resolve } from 'path'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': resolve(__dirname, 'src'),
      'vue-i18n': 'vue-i18n/dist/vue-i18n.runtime.esm-bundler.js'
    }
  },
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./src/__tests__/setup.ts'],
    include: ['src/**/*.{test,spec}.{js,ts,jsx,tsx}'],
    exclude: ['node_modules', 'dist'],
    coverage: {
      provider: 'v8',
      // 全量跑存在历史未铺满/失败的用例,vitest 默认失败就不出报告;开 reportOnFailure
      // 让覆盖率仍然生成,交给 Codecov(CI 里该 job 配 continue-on-error,失败不挡合并)。
      reportOnFailure: true,
      // lcov 供 Codecov 上传;覆盖率门槛统一改由 Codecov patch(新 PR 改动行 ≥80%)管,
      // 这里不再设本地 threshold,以免全量跑覆盖时因存量低覆盖直接 exit 1。
      reporter: ['text', 'lcov', 'html'],
      include: ['src/**/*.{js,ts,vue}'],
      exclude: [
        'node_modules',
        'src/**/*.d.ts',
        'src/**/*.spec.ts',
        'src/**/*.test.ts',
        'src/main.ts'
      ]
    }
  }
})
