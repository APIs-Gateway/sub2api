<template>
  <BaseDialog :show="show" :title="t('admin.pricingConfig.write.custom.title')" width="narrow" @close="emit('close')">
    <div class="space-y-4">
      <p class="text-sm text-gray-700 dark:text-gray-300">{{ t('admin.pricingConfig.write.custom.intro', { n: count }) }}</p>
      <div class="grid grid-cols-2 gap-3">
        <div v-for="f in fields" :key="f.key">
          <label class="input-label" :for="`custom-${f.key}`">{{ t(`admin.pricingConfig.write.custom.${f.key}`) }}</label>
          <input
            :id="`custom-${f.key}`"
            v-model="f.text"
            type="number"
            step="0.01"
            min="0"
            class="input num"
            :class="{ 'input-error': isBad(f.text) }"
            :placeholder="t('admin.pricingConfig.write.custom.placeholder')"
            :data-test="`custom-${f.key}`"
          />
        </div>
      </div>
      <p class="input-hint">{{ t('admin.pricingConfig.write.custom.hint') }}</p>
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button type="button" class="btn btn-primary" :disabled="!valid" data-test="custom-confirm" @click="submit">
        {{ t('admin.pricingConfig.write.toPreview') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { CustomPrice } from '@/api/admin/pricing'
import { parsePrice } from '../pricingWrite'

const props = defineProps<{ show: boolean; count: number }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'confirm', value: CustomPrice): void }>()

const { t } = useI18n()
const fields = reactive([
  { key: 'input' as const, text: '' },
  { key: 'output' as const, text: '' }
])
watch(
  () => props.show,
  (v) => {
    if (v) fields.forEach((f) => (f.text = ''))
  }
)

const isBad = (text: string | number) => parsePrice(text) === null
// 至少填一项，否则这个自定义价没有任何内容
const valid = computed(() => fields.every((f) => !isBad(f.text)) && fields.some((f) => String(f.text ?? '').trim() !== ''))

function submit() {
  if (!valid.value) return
  const price: CustomPrice = { billing_mode: 'token' }
  const input = parsePrice(fields[0].text)
  const output = parsePrice(fields[1].text)
  price.input_price = input ?? null
  price.output_price = output ?? null
  emit('confirm', price)
}
</script>
