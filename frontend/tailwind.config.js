import plugin from 'tailwindcss/plugin'

/**
 * 浅色模式下 text-gray-400 / text-gray-500 的文字色。
 *
 * 色板里的 gray-400 (#b8b1a1) 在白底只有 2.13:1、gray-500 (#918a7c) 约 3.4:1，达不到
 * WCAG AA 的 4.5:1。这两档在全站被大量用作次要文字（模板里约 2200 处 + 深色模式的
 * dark:text-gray-400 约 1500 处），所以不改色板，也不逐处替换，而是只覆盖「文字色」这一个
 * 出口（见 theme.extend.textColor）：浅色取下面的值，深色仍取色板原值。
 *
 * 对比度（白 / gray-50 / gray-100 底）：
 *   400 -> #736c5f  5.20 / 4.93 / 4.60
 *   500 -> #6f685b  5.52 / 5.24 / 4.88
 * 修改这两个值时请同步更新 src/__tests__/lightGrayTextContrast.spec.ts 的校验。
 */
export const lightGrayText = { 400: '#736c5f', 500: '#6f685b' }

const channels = (hex) =>
  [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16)).join(' ')

/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{vue,js,ts,jsx,tsx}'],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        // 主色调 - Anthropic 陶土色 (Book Cloth #cc785c，降饱和、更柔和)
        primary: {
          50: '#fbf5f1',
          100: '#f5e7dd',
          200: '#ecccb9',
          300: '#dcab8e',
          400: '#d18d67',
          500: '#cc785c',
          600: '#b5634a',
          700: '#964f3b',
          800: '#793f30',
          900: '#5f3326',
          950: '#361a13'
        },
        // 辅助色 - 暖棕褐 (与主色搭配用于渐变文字等)
        accent: {
          50: '#f8f5f0',
          100: '#efe8dd',
          200: '#ddcfba',
          300: '#c6b08f',
          400: '#ad8e69',
          500: '#94714f',
          600: '#7a5b40',
          700: '#624735',
          800: '#4c382d',
          900: '#3c2d25',
          950: '#241813'
        },
        // 暖中性灰 - 替换 Tailwind 默认冷灰 (Anthropic Ivory/Stone)
        gray: {
          50: '#faf9f5',
          100: '#f3f1ea',
          200: '#e9e5db',
          300: '#d9d3c6',
          400: '#b8b1a1',
          500: '#918a7c',
          600: '#6d6659',
          700: '#524c42',
          800: '#3a352e',
          900: '#262420',
          950: '#1a1814'
        },
        // 深色模式背景 - 暖炭灰 (Warm Charcoal)
        dark: {
          50: '#f5f3ee',
          100: '#e9e5db',
          200: '#d3ccbe',
          300: '#b4ab9a',
          400: '#8c8475',
          500: '#6b6357',
          600: '#4f4940',
          700: '#34302a',
          800: '#262420',
          900: '#1c1b18',
          950: '#141310'
        }
      },
      // 只覆盖文字色（text-gray-400/500，含 placeholder:/hover: 等变体和 @apply），
      // bg-/border-/ring-/fill- 等仍用上面的 gray 色板。值走 CSS 变量，见底部 plugin。
      textColor: {
        gray: {
          400: 'rgb(var(--text-gray-400) / <alpha-value>)',
          500: 'rgb(var(--text-gray-500) / <alpha-value>)'
        }
      },
      fontFamily: {
        // 无衬线（UI/正文）：Latin 走 Space Grotesk，中文走 Noto Sans SC
        sans: [
          'Space Grotesk Variable',
          'Space Grotesk',
          'Noto Sans SC',
          'system-ui',
          '-apple-system',
          'BlinkMacSystemFont',
          'Segoe UI',
          'Roboto',
          'Helvetica Neue',
          'Arial',
          'PingFang SC',
          'Hiragino Sans GB',
          'Microsoft YaHei',
          'sans-serif'
        ],
        // 衬线（标题/编辑感）：Latin 走 Fraunces，中文走 Noto Serif SC
        serif: [
          'Fraunces Variable',
          'Fraunces',
          'Noto Serif SC',
          'Songti SC',
          'STSong',
          'Georgia',
          'Times New Roman',
          'serif'
        ],
        // 等宽（机器之声）：所有数据/费用/token/ID/时间戳走 JetBrains Mono，CJK 落 Noto Sans SC
        mono: [
          'JetBrains Mono Variable',
          'JetBrains Mono',
          'ui-monospace',
          'SFMono-Regular',
          'Menlo',
          'Monaco',
          'Consolas',
          'Noto Sans SC',
          'monospace'
        ]
      },
      boxShadow: {
        // 仅保留：发丝级"纸面"分层 + 唯一真浮层(模态)的极轻投影。玻璃/发光大投影已删除。
        hairline: '0 1px 0 rgba(38, 36, 32, 0.06)',
        overlay: '0 8px 30px rgba(38, 36, 32, 0.12)',
        // 兼容残留引用（MonitorCard 等长尾，待 step 6 清扫）
        card: '0 1px 3px rgba(38, 36, 32, 0.04), 0 1px 2px rgba(38, 36, 32, 0.06)',
        'card-hover': '0 4px 16px rgba(38, 36, 32, 0.06)'
      },
      backgroundImage: {
        'gradient-radial': 'radial-gradient(var(--tw-gradient-stops))'
      },
      animation: {
        // 克制：仅淡入 + 一段短滑动；发光/微光/脉冲/缩放/右滑已删除。星标呼吸单独在组件内实现。
        'fade-in': 'fadeIn 0.3s ease-out',
        'slide-up': 'slideUp 0.3s ease-out',
        'slide-down': 'slideDown 0.3s ease-out'
      },
      keyframes: {
        fadeIn: {
          '0%': { opacity: '0' },
          '100%': { opacity: '1' }
        },
        slideUp: {
          '0%': { opacity: '0', transform: 'translateY(8px)' },
          '100%': { opacity: '1', transform: 'translateY(0)' }
        },
        slideDown: {
          '0%': { opacity: '0', transform: 'translateY(-8px)' },
          '100%': { opacity: '1', transform: 'translateY(0)' }
        }
      }
    }
  },
  plugins: [
    plugin(({ addBase, theme }) => {
      // 默认（浅色）取加深后的值；以下场景沿用色板原值，保持「本来就该弱」的层级：
      //  - .dark：深色模式不动；
      //  - input/textarea/select：占位符（placeholder:text-gray-400 继承自输入框）；
      //  - 禁用控件：按钮/输入框等 :disabled 与 aria-disabled；
      //  - 浅色模式下固定深底的 tooltip / 终端块（bg-gray-800/900/950，以及终端上的半透明复制按钮
      //    bg-gray-800/80），原值在深底上对比度更高。
      //    注意：bg-black/50、bg-gray-950/60 这类半透明遮罩下面是浅色卡片，不能放进这个列表。
      addBase({
        ':root:not(.dark)': {
          '--text-gray-400': channels(lightGrayText[400]),
          '--text-gray-500': channels(lightGrayText[500])
        },
        [[
          '.dark',
          'input',
          'textarea',
          'select',
          ':disabled',
          "[aria-disabled='true']",
          '.bg-gray-800',
          '.bg-gray-900',
          '.bg-gray-950',
          "[class~='bg-gray-800/80']"
        ].join(', ')]: {
          '--text-gray-400': channels(theme('colors.gray.400')),
          '--text-gray-500': channels(theme('colors.gray.500'))
        }
      })
    })
  ]
}
