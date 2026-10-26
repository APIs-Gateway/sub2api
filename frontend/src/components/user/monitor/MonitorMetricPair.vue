<template>
  <div class="mt-5 grid gap-2" :class="hasSecondary ? 'grid-cols-2' : 'grid-cols-1'">
    <div
      class="rounded-xl p-3 bg-gray-50/80 dark:bg-dark-900/40 border border-gray-100 dark:border-dark-700/50"
    >
      <div
        class="flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wider text-gray-400"
      >
        <Icon :name="primaryIcon" size="xs" />
        <span>{{ primaryLabel }}</span>
      </div>
      <NumText
        tier="secondary"
        class="mt-1.5 block text-lg text-gray-900 dark:text-gray-100"
        :text="primaryUnit ? `${primaryValue} ${primaryUnit}` : primaryValue"
      />
    </div>
    <div
      v-if="hasSecondary"
      class="rounded-xl p-3 bg-gray-50/80 dark:bg-dark-900/40 border border-gray-100 dark:border-dark-700/50"
    >
      <div
        class="flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wider text-gray-400"
      >
        <Icon :name="secondaryIcon!" size="xs" />
        <span>{{ secondaryLabel }}</span>
      </div>
      <NumText
        tier="secondary"
        class="mt-1.5 block text-lg text-gray-900 dark:text-gray-100"
        :text="secondaryUnit ? `${secondaryValue} ${secondaryUnit}` : (secondaryValue ?? '')"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import NumText from '@/components/common/NumText.vue'

// 第二个指标是可选的：不传 secondaryLabel 时只渲染第一个，并让它占满整行。
const props = defineProps<{
  primaryLabel: string
  primaryValue: string
  primaryUnit: string
  primaryIcon: 'bolt' | 'globe' | 'clock' | 'link'
  secondaryLabel?: string
  secondaryValue?: string
  secondaryUnit?: string
  secondaryIcon?: 'bolt' | 'globe' | 'clock' | 'link'
}>()

const hasSecondary = computed(() => props.secondaryLabel !== undefined)
</script>
