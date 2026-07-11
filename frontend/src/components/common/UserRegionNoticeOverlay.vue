<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useRoute } from "vue-router";
import { useI18n } from "vue-i18n";
import { getRegion } from "@/api/region";
import { useAppStore, useAuthStore } from "@/stores";

const POLL_INTERVAL_MS = 30_000;

const route = useRoute();
const authStore = useAuthStore();
const appStore = useAppStore();
const { t } = useI18n();

const visible = ref(false);
let pollTimer: number | undefined;
let requestToken = 0;

const shouldCheckRegion = computed(
  () =>
    appStore.userRegionNoticeEnabled === true &&
    authStore.isAuthenticated === true &&
    authStore.isAdmin !== true &&
    route.meta.requiresAuth === true &&
    route.meta.requiresAdmin !== true,
);

function stopPolling() {
  if (pollTimer !== undefined) {
    window.clearInterval(pollTimer);
    pollTimer = undefined;
  }
}

function startPolling() {
  if (pollTimer !== undefined || !shouldCheckRegion.value) {
    return;
  }

  pollTimer = window.setInterval(() => {
    void checkRegion();
  }, POLL_INTERVAL_MS);
}

async function checkRegion() {
  const token = ++requestToken;

  if (!shouldCheckRegion.value) {
    visible.value = false;
    stopPolling();
    return;
  }

  try {
    const region = await getRegion();
    if (token !== requestToken) {
      return;
    }

    visible.value =
      region.user_region_notice_enabled === true &&
      region.country_code === "CN";
  } catch {
    if (token === requestToken) {
      visible.value = false;
    }
  }

  if (shouldCheckRegion.value) {
    startPolling();
  }
}

watch(
  () => [
    route.fullPath,
    route.meta.requiresAuth,
    route.meta.requiresAdmin,
    appStore.userRegionNoticeEnabled,
    authStore.isAuthenticated,
    authStore.isAdmin,
  ],
  () => {
    if (!shouldCheckRegion.value) {
      requestToken++;
      visible.value = false;
      stopPolling();
      return;
    }

    void checkRegion();
  },
  { immediate: true },
);

onBeforeUnmount(() => {
  requestToken++;
  visible.value = false;
  stopPolling();
});
</script>

<template>
  <div
    v-if="visible"
    data-testid="user-region-notice-overlay"
    role="alertdialog"
    aria-modal="true"
    aria-labelledby="user-region-notice-title"
    aria-describedby="user-region-notice-description"
    class="fixed inset-0 z-[1000] flex items-center justify-center bg-gray-950/75 px-4 py-6 backdrop-blur-sm"
  >
    <div
      class="w-full max-w-md rounded-lg border border-gray-200 bg-white p-6 text-center shadow-2xl dark:border-dark-700 dark:bg-dark-900"
    >
      <div
        class="mx-auto flex h-12 w-12 items-center justify-center rounded-full border border-amber-200 bg-amber-50 text-xl text-amber-700 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-300"
        aria-hidden="true"
      >
        !
      </div>
      <h2
        id="user-region-notice-title"
        class="mt-4 text-lg font-semibold text-gray-900 dark:text-white"
      >
        {{ t("regionNotice.title") }}
      </h2>
      <p
        id="user-region-notice-description"
        class="mt-3 text-sm leading-6 text-gray-600 dark:text-gray-300"
      >
        {{ t("regionNotice.description") }}
      </p>
    </div>
  </div>
</template>
