<template>
  <BaseDialog :show="show" :title="t('admin.pricingConfig.write.extra.title')" width="narrow" @close="emit('close')">
    <div class="space-y-4">
      <p class="text-sm text-gray-700 dark:text-gray-300">{{ t('admin.pricingConfig.write.extra.intro', { n: count }) }}</p>
      <p v-if="skipped > 0" class="text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingConfig.write.extra.skipped', { n: skipped }) }}</p>
      <div>
        <label class="input-label" for="extra-input">{{ t('admin.pricingConfig.write.extra.label') }}</label>
        <input
          id="extra-input"
          v-model="text"
          type="number"
          step="0.05"
          min="0.01"
          class="input num"
          :class="{ 'input-error': invalid }"
          :placeholder="t('admin.pricingConfig.write.extra.placeholder')"
          data-test="extra-input"
          @keydown.enter="submit"
        />
        <p v-if="invalid" class="input-error-text">{{ t('admin.pricingConfig.write.extra.invalid') }}</p>
        <p v-else class="input-hint">{{ t('admin.pricingConfig.write.extra.hint') }}</p>
      </div>
      <div class="flex flex-wrap gap-2">
        <button v-for="n in quick" :key="n" type="button" class="btn btn-secondary btn-sm num" @click="text = String(n)">×{{ n }}</button>
      </div>
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button type="button" class="btn btn-primary" :disabled="invalid || String(text ?? '') === ''" data-test="extra-confirm" @click="submit">
        {{ t('admin.pricingConfig.write.toPreview') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { parseExtra } from '../pricingWrite'

const props = defineProps<{ show: boolean; count: number; skipped: number }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'confirm', value: number): void }>()

const { t } = useI18n()
const quick = [1, 1.1, 1.2, 1.5, 2]
const text = ref('')
watch(
  () => props.show,
  (v) => {
    if (v) text.value = ''
  }
)

const invalid = computed(() => String(text.value ?? '') !== '' && parseExtra(text.value) === null)

function submit() {
  const v = parseExtra(text.value)
  if (typeof v !== 'number') return
  emit('confirm', v)
}
</script>
