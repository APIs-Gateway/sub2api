<template>
  <div class="flex items-center gap-1.5">
    <span
      :class="[
        'inline-block h-2 w-2 rounded-full',
        variantClass
      ]"
    ></span>
    <span class="text-sm text-gray-700 dark:text-gray-300">
      {{ label }}
    </span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  status: string
  label: string
}>()

// 反彩虹：状态靠"墨点(在)/淡点(关)/Signal(错)"承载，颜色不编码正常态——文字才是主语。
const variantClass = computed(() => {
  switch (props.status) {
    case 'active':
    case 'success':
      // 正常/在线：实心墨点
      return 'bg-gray-800 dark:bg-gray-200'
    case 'disabled':
    case 'inactive':
      // 停用/离线：淡点（以"弱"示意，不着色）
      return 'bg-gray-300 dark:bg-dark-600'
    case 'warning':
      return 'bg-gray-400 dark:bg-dark-500'
    case 'error':
    case 'danger':
      // 唯一语义色 Signal（黏土族）
      return 'bg-primary-600'
    default:
      return 'bg-gray-300 dark:bg-dark-600'
  }
})
</script>
