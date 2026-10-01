<template>
  <div class="space-y-3">
    <div class="rounded-lg bg-blue-50 p-3 dark:bg-blue-900/20">
      <p class="text-xs text-blue-700 dark:text-blue-400">
        <Icon name="exclamationTriangle" size="sm" class="mr-1 inline" :stroke-width="2" />
        {{ t('admin.accounts.tempUnschedulable.notice') }}
      </p>
    </div>

    <div class="flex flex-wrap gap-2">
      <button
        v-for="(preset, presetIndex) in presets"
        :key="preset.label"
        type="button"
        :data-testid="`temp-unsched-preset-${presetIndex}`"
        class="rounded-lg bg-gray-100 px-3 py-1.5 text-xs font-medium text-gray-600 transition-colors hover:bg-gray-200 dark:bg-dark-600 dark:text-gray-300 dark:hover:bg-dark-500"
        @click="addRule(preset.rule)"
      >
        + {{ preset.label }}
      </button>
    </div>

    <div v-if="rules.length > 0" class="space-y-3">
      <div
        v-for="(rule, index) in rules"
        :key="getRuleKey(rule)"
        class="rounded-lg border border-gray-200 p-3 dark:border-dark-600"
        data-testid="temp-unsched-rule"
      >
        <div class="mb-2 flex items-center justify-between">
          <span class="text-xs font-medium text-gray-500 dark:text-gray-400">
            {{ t('admin.accounts.tempUnschedulable.ruleIndex', { index: index + 1 }) }}
          </span>
          <div class="flex items-center gap-2">
            <button
              type="button"
              :disabled="index === 0"
              class="rounded p-1 text-gray-400 transition-colors hover:text-gray-600 disabled:cursor-not-allowed disabled:opacity-40 dark:hover:text-gray-200"
              @click="moveRule(index, -1)"
            >
              <Icon name="chevronUp" size="sm" :stroke-width="2" />
            </button>
            <button
              type="button"
              :disabled="index === rules.length - 1"
              class="rounded p-1 text-gray-400 transition-colors hover:text-gray-600 disabled:cursor-not-allowed disabled:opacity-40 dark:hover:text-gray-200"
              @click="moveRule(index, 1)"
            >
              <Icon name="chevronDown" size="sm" :stroke-width="2" />
            </button>
            <button
              type="button"
              class="rounded p-1 text-red-500 transition-colors hover:text-red-600"
              @click="removeRule(index)"
            >
              <Icon name="x" size="sm" :stroke-width="2" />
            </button>
          </div>
        </div>

        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <div>
            <label class="input-label">{{ t('admin.accounts.tempUnschedulable.errorCode') }}</label>
            <input
              v-model.number="rule.error_code"
              type="number"
              min="100"
              max="599"
              class="input"
              :placeholder="t('admin.accounts.tempUnschedulable.errorCodePlaceholder')"
            />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accounts.tempUnschedulable.durationMinutes') }}</label>
            <input
              v-model.number="rule.duration_minutes"
              type="number"
              min="1"
              class="input"
              :placeholder="t('admin.accounts.tempUnschedulable.durationPlaceholder')"
            />
          </div>
          <div class="sm:col-span-2">
            <label class="input-label">{{ t('admin.accounts.tempUnschedulable.keywords') }}</label>
            <input
              v-model="rule.keywords"
              type="text"
              class="input"
              :placeholder="t('admin.accounts.tempUnschedulable.keywordsPlaceholder')"
            />
            <p class="input-hint">{{ t('admin.accounts.tempUnschedulable.keywordsHint') }}</p>
          </div>
          <div class="sm:col-span-2">
            <label class="input-label">{{ t('admin.accounts.tempUnschedulable.description') }}</label>
            <input
              v-model="rule.description"
              type="text"
              class="input"
              :placeholder="t('admin.accounts.tempUnschedulable.descriptionPlaceholder')"
            />
          </div>
        </div>
      </div>
    </div>

    <button
      type="button"
      data-testid="temp-unsched-add-rule"
      class="w-full rounded-lg border-2 border-dashed border-gray-300 px-4 py-2 text-sm text-gray-600 transition-colors hover:border-gray-400 hover:text-gray-700 dark:border-dark-500 dark:text-gray-400 dark:hover:border-dark-400 dark:hover:text-gray-300"
      @click="addRule()"
    >
      <Icon name="plus" size="sm" class="mr-1 inline" :stroke-width="2" />
      {{ t('admin.accounts.tempUnschedulable.addRule') }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { TempUnschedRuleForm } from '@/components/account/credentialsBuilder'
import { createStableObjectKeyResolver } from '@/utils/stableObjectKey'

const rules = defineModel<TempUnschedRuleForm[]>({ required: true })
const { t } = useI18n()
const getRuleKey = createStableObjectKeyResolver<TempUnschedRuleForm>('temp-unsched-rule')

const presets = computed(() => [
  {
    label: t('admin.accounts.tempUnschedulable.presets.overloadLabel'),
    rule: {
      error_code: 529,
      keywords: 'overloaded, too many',
      duration_minutes: 60,
      description: t('admin.accounts.tempUnschedulable.presets.overloadDesc')
    }
  },
  {
    label: t('admin.accounts.tempUnschedulable.presets.rateLimitLabel'),
    rule: {
      error_code: 429,
      keywords: 'rate limit, too many requests',
      duration_minutes: 10,
      description: t('admin.accounts.tempUnschedulable.presets.rateLimitDesc')
    }
  },
  {
    label: t('admin.accounts.tempUnschedulable.presets.unavailableLabel'),
    rule: {
      error_code: 503,
      keywords: 'unavailable, maintenance',
      duration_minutes: 30,
      description: t('admin.accounts.tempUnschedulable.presets.unavailableDesc')
    }
  }
])

const addRule = (preset?: TempUnschedRuleForm) => {
  rules.value = [
    ...rules.value,
    preset
      ? { ...preset }
      : { error_code: null, keywords: '', duration_minutes: 30, description: '' }
  ]
}

const removeRule = (index: number) => {
  rules.value = rules.value.filter((_, i) => i !== index)
}

// 规则按顺序匹配，上下移动会改变优先级。
const moveRule = (index: number, direction: number) => {
  const target = index + direction
  if (target < 0 || target >= rules.value.length) return
  const next = [...rules.value]
  const current = next[index]
  next[index] = next[target]
  next[target] = current
  rules.value = next
}
</script>
