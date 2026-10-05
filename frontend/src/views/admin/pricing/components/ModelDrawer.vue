<template>
  <SideDrawer :show="!!model" :title="model?.key ?? ''" :subtitle="subtitle" @close="emit('close')">
    <div v-if="model" class="space-y-7" data-test="model-drawer">
      <section>
        <h3 class="section-title">{{ t('admin.pricingConfig.drawer.basic') }}</h3>
        <dl class="mt-3 grid grid-cols-[6.5rem_1fr] gap-x-4 gap-y-2.5 text-sm">
          <dt class="dt">{{ t('admin.pricingConfig.models.columns.platform') }}</dt>
          <dd class="flex items-center gap-1.5 text-gray-900 dark:text-white">
            <PlatformIcon :platform="asGroupPlatform(model.platform)" size="sm" />{{ platformLabel(model.platform) }}
          </dd>
          <dt class="dt">{{ t('admin.pricingConfig.models.columns.status') }}</dt>
          <dd>
            <span class="badge" :class="model.status === 'active' ? 'badge-success' : ''" data-test="drawer-status">{{ statusText }}</span>
          </dd>
          <dt class="dt">{{ t('admin.pricingConfig.models.columns.officialPrice') }}</dt>
          <dd class="num text-gray-900 dark:text-white" data-test="drawer-official">
            <template v-if="officialRef?.priced && officialRef.per_mtok">
              {{ t('admin.pricingConfig.drawer.inOut', { input: officialIn.main, output: officialOut.main }) }}
              <span class="ml-1 text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingConfig.perMillionTokens') }}</span>
              <p v-if="officialIn.sub && officialOut.sub" class="mt-0.5 text-xs text-gray-500 dark:text-dark-300">
                {{ officialIn.sub }} / {{ officialOut.sub }}
              </p>
            </template>
            <span v-else class="font-medium text-primary-700 dark:text-primary-300">{{ t('admin.pricingConfig.drawer.noOfficialPrice') }}</span>
          </dd>
          <dt class="dt">{{ t('admin.pricingConfig.models.columns.source') }}</dt>
          <dd class="text-gray-900 dark:text-white">{{ t(`admin.pricingConfig.source.${sourceKey(officialRef?.source)}`) }}</dd>
          <template v-if="model.aliases.length">
            <dt class="dt">{{ t('admin.pricingConfig.drawer.aliases') }}</dt>
            <dd class="flex flex-wrap gap-1.5">
              <span v-for="a in model.aliases" :key="a" class="badge num">{{ a }}</span>
            </dd>
          </template>
          <template v-if="model.referenceModel">
            <dt class="dt">{{ t('admin.pricingConfig.drawer.referenceModel') }}</dt>
            <dd class="text-gray-900 dark:text-white">{{ model.referenceModel }}</dd>
          </template>
        </dl>
      </section>

      <section>
        <div class="flex items-center justify-between gap-3">
          <h3 class="section-title">{{ t('admin.pricingConfig.drawer.groups') }}</h3>
          <CurrencyModeSwitch />
        </div>
        <p v-if="!model.registered" class="mt-3 text-sm text-gray-500 dark:text-dark-300">{{ t('admin.pricingConfig.drawer.unregisteredHint') }}</p>
        <p v-else-if="groups.length === 0" class="mt-3 text-sm text-gray-500 dark:text-dark-300">{{ t('admin.pricingConfig.drawer.noGroups') }}</p>
        <ul
          v-if="groups.length"
          class="mt-3 divide-y divide-gray-100 overflow-hidden rounded-md border border-gray-200 dark:divide-dark-800 dark:border-dark-700"
          data-test="mini-matrix"
        >
          <li v-for="g in groups" :key="g.id" class="flex items-center justify-between gap-3 px-3 py-2.5" :data-test="`mini-row-${g.id}`">
            <div class="min-w-0">
              <p class="truncate text-sm font-medium text-gray-900 dark:text-white">{{ g.name }}</p>
              <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-300">
                <span class="num">{{ t('admin.pricingConfig.group.rate', { rate: trimNum(g.rate) }) }}</span>
                <span v-if="g.accessMode" class="ml-3">{{ t(`admin.pricingConfig.group.access.${g.accessMode}`) }}</span>
                <span v-if="isGroupUnswitched(g)" class="ml-3 inline-flex items-center gap-1">
                  <Icon name="lock" size="xs" />{{ t('admin.pricingConfig.group.unswitched') }}
                </span>
              </p>
            </div>
            <div class="flex-shrink-0 text-right">
              <CellFace v-if="viewOf(g)" :view="viewOf(g)!" />
              <span v-else class="text-gray-400 dark:text-dark-400">—</span>
            </div>
          </li>
        </ul>
        <p v-if="groups.length && hasUnswitched" class="mt-2 text-xs text-gray-500 dark:text-dark-300">
          {{ t('admin.pricingConfig.drawer.derivedNote') }}
        </p>
        <router-link
          v-if="groups.length"
          to="/admin/pricing/matrix"
          class="mt-2 inline-block text-xs text-gray-500 underline decoration-gray-300 underline-offset-2 hover:text-gray-900 dark:text-dark-300 dark:decoration-dark-500 dark:hover:text-white"
        >
          {{ t('admin.pricingConfig.drawer.toMatrix') }}
        </router-link>
      </section>
    </div>

    <template #footer>
      <span class="mr-auto text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingConfig.comingSoon') }}</span>
      <button v-if="model" type="button" class="btn btn-secondary" disabled :title="t('admin.pricingConfig.comingSoon')" data-test="drawer-action">
        {{ actionText }}
      </button>
    </template>
  </SideDrawer>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import CurrencyModeSwitch from '@/components/common/CurrencyModeSwitch.vue'
import SideDrawer from './SideDrawer.vue'
import CellFace from './CellFace.vue'
import { usePricingData } from '../usePricingData'
import { usePricingFormat } from '../usePricingFormat'
import { asGroupPlatform, cellKey, cellView, isGroupUnswitched, platformLabel, sourceKey, type ModelRow, type PricingGroup } from '../pricingModel'

const props = defineProps<{ model: ModelRow | null }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()
const { state } = usePricingData()
const { official } = usePricingFormat()

const officialRef = computed(() => (props.model ? state.refs[props.model.key] : undefined))
const groups = computed(() => (props.model ? state.groups.filter((g) => g.platform === props.model!.platform) : []))
const hasUnswitched = computed(() => groups.value.some(isGroupUnswitched))
const subtitle = computed(() => (props.model && props.model.displayName !== props.model.key ? props.model.displayName : ''))
const statusText = computed(() => t(`admin.pricingConfig.status.${props.model?.status ?? 'unregistered'}`))
const officialIn = computed(() => official(officialRef.value?.per_mtok?.input))
const officialOut = computed(() => official(officialRef.value?.per_mtok?.output))

const actionText = computed(() => {
  const status = props.model?.status ?? null
  if (status === null) return t('admin.pricingConfig.drawer.actionRegister')
  return t(`admin.pricingConfig.drawer.action.${status}`)
})

function viewOf(g: PricingGroup) {
  return props.model ? cellView(state.cells[cellKey(g.id, props.model.key)], g) : null
}

function trimNum(n: number): string {
  return String(Number(n.toFixed(4)))
}
</script>

<style scoped>
.section-title {
  @apply font-serif text-base font-medium text-gray-900 dark:text-white;
}
.dt {
  @apply text-gray-500 dark:text-dark-300;
}
</style>
