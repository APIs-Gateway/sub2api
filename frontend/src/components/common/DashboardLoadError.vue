<template>
  <div
    class="flex flex-col gap-3 rounded-2xl border border-red-200 bg-red-50 p-5 text-red-700 dark:border-red-900/60 dark:bg-red-950/30 dark:text-red-300 sm:flex-row sm:items-center sm:justify-between"
    role="alert"
  >
    <div>
      <p class="text-sm font-semibold">{{ title }}</p>
      <p v-if="description" class="mt-1 text-sm opacity-80">{{ description }}</p>
    </div>
    <button type="button" class="btn btn-secondary shrink-0" :disabled="loading" @click="$emit('retry')">
      {{ retryLabel }}
    </button>
  </div>
</template>

<script setup lang="ts">
defineProps<{
  title: string
  description?: string
  retryLabel: string
  loading?: boolean
}>()

defineEmits<{
  retry: []
}>()
</script>

