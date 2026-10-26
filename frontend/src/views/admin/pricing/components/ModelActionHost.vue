<template>
  <LaunchDialog :model="action?.kind === 'launch' ? action.model : null" @close="emit('close')" />
  <TransitionDialog
    :model="action && action.kind !== 'launch' ? action.model : null"
    :kind="action?.kind === 'reactivate' ? 'reactivate' : 'retire'"
    @close="emit('close')"
  />
</template>

<script setup lang="ts">
import LaunchDialog from './LaunchDialog.vue'
import TransitionDialog from './TransitionDialog.vue'
import type { ModelAction } from './modelAction'

defineProps<{ action: ModelAction | null }>()
const emit = defineEmits<{ (e: 'close'): void }>()
</script>
