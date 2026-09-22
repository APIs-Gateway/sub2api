<template>
  <section class="admin-task-page">
    <header class="admin-task-page__header">
      <div class="max-w-2xl">
        <p v-if="eyebrow" class="admin-task-page__eyebrow">{{ eyebrow }}</p>
        <h1 class="admin-task-page__title">{{ title }}</h1>
        <p v-if="description" class="admin-task-page__description">{{ description }}</p>
      </div>

      <div v-if="$slots.actions" class="admin-task-page__actions">
        <slot name="actions" />
      </div>
    </header>

    <div v-if="$slots.filters" class="admin-task-page__filters">
      <slot name="filters" />
    </div>

    <div class="admin-task-page__worklist">
      <slot name="worklist" />
    </div>

    <footer v-if="$slots.footer" class="admin-task-page__footer">
      <slot name="footer" />
    </footer>
  </section>
</template>

<script setup lang="ts">
withDefaults(defineProps<{
  title: string
  description?: string
  eyebrow?: string
}>(), {
  description: '',
  eyebrow: ''
})
</script>

<style scoped>
.admin-task-page {
  @apply mx-auto flex w-full max-w-[1440px] flex-col gap-5 pb-4;
}

.admin-task-page__header {
  @apply grid gap-5 border-b border-gray-200 pb-5 dark:border-dark-700 lg:grid-cols-[minmax(0,1fr)_auto] lg:items-end;
}

.admin-task-page__eyebrow {
  @apply mb-2 text-xs font-semibold uppercase tracking-[0.16em] text-primary-700 dark:text-primary-300;
}

.admin-task-page__title {
  @apply font-serif text-3xl font-semibold tracking-tight text-gray-950 dark:text-white sm:text-4xl;
}

.admin-task-page__description {
  @apply mt-2 max-w-xl text-sm leading-6 text-gray-600 dark:text-dark-300;
}

.admin-task-page__actions {
  @apply flex flex-wrap items-center gap-2 lg:justify-end;
}

.admin-task-page__filters {
  @apply border-y border-gray-200 py-3 dark:border-dark-700;
}

.admin-task-page__worklist {
  @apply min-h-0;
}

.admin-task-page__footer {
  @apply border-t border-gray-200 pt-4 dark:border-dark-700;
}
</style>
