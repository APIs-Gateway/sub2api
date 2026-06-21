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
        mono: ['ui-monospace', 'SFMono-Regular', 'Menlo', 'Monaco', 'Consolas', 'monospace']
      },
      boxShadow: {
        glass: '0 8px 32px rgba(0, 0, 0, 0.08)',
        'glass-sm': '0 4px 16px rgba(0, 0, 0, 0.06)',
        glow: '0 0 20px rgba(204, 120, 92, 0.25)',
        'glow-lg': '0 0 40px rgba(204, 120, 92, 0.35)',
        card: '0 1px 3px rgba(0, 0, 0, 0.04), 0 1px 2px rgba(0, 0, 0, 0.06)',
        'card-hover': '0 10px 40px rgba(0, 0, 0, 0.08)',
        'inner-glow': 'inset 0 1px 0 rgba(255, 255, 255, 0.1)'
      },
      backgroundImage: {
        'gradient-radial': 'radial-gradient(var(--tw-gradient-stops))',
        'gradient-primary': 'linear-gradient(135deg, #cc785c 0%, #b5634a 100%)',
        'gradient-dark': 'linear-gradient(135deg, #262420 0%, #1c1b18 100%)',
        'gradient-glass':
          'linear-gradient(135deg, rgba(255,255,255,0.1) 0%, rgba(255,255,255,0.05) 100%)',
        'mesh-gradient':
          'radial-gradient(at 40% 20%, rgba(204, 120, 92, 0.12) 0px, transparent 50%), radial-gradient(at 80% 0%, rgba(209, 141, 103, 0.08) 0px, transparent 50%), radial-gradient(at 0% 50%, rgba(204, 120, 92, 0.08) 0px, transparent 50%)'
      },
      animation: {
        'fade-in': 'fadeIn 0.3s ease-out',
        'slide-up': 'slideUp 0.3s ease-out',
        'slide-down': 'slideDown 0.3s ease-out',
        'slide-in-right': 'slideInRight 0.3s ease-out',
        'scale-in': 'scaleIn 0.2s ease-out',
        'pulse-slow': 'pulse 3s cubic-bezier(0.4, 0, 0.6, 1) infinite',
        shimmer: 'shimmer 2s linear infinite',
        glow: 'glow 2s ease-in-out infinite alternate'
      },
      keyframes: {
        fadeIn: {
          '0%': { opacity: '0' },
          '100%': { opacity: '1' }
        },
        slideUp: {
          '0%': { opacity: '0', transform: 'translateY(10px)' },
          '100%': { opacity: '1', transform: 'translateY(0)' }
        },
        slideDown: {
          '0%': { opacity: '0', transform: 'translateY(-10px)' },
          '100%': { opacity: '1', transform: 'translateY(0)' }
        },
        slideInRight: {
          '0%': { opacity: '0', transform: 'translateX(20px)' },
          '100%': { opacity: '1', transform: 'translateX(0)' }
        },
        scaleIn: {
          '0%': { opacity: '0', transform: 'scale(0.95)' },
          '100%': { opacity: '1', transform: 'scale(1)' }
        },
        shimmer: {
          '0%': { backgroundPosition: '-200% 0' },
          '100%': { backgroundPosition: '200% 0' }
        },
        glow: {
          '0%': { boxShadow: '0 0 20px rgba(204, 120, 92, 0.25)' },
          '100%': { boxShadow: '0 0 30px rgba(204, 120, 92, 0.4)' }
        }
      },
      backdropBlur: {
        xs: '2px'
      },
      borderRadius: {
        '4xl': '2rem'
      }
    }
  },
  plugins: []
}
