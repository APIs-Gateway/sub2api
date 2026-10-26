import { onBeforeUnmount, ref, watch } from 'vue'
import { getAuthSessionVersion } from '@/utils/authSessionVersion'
import { useAuthStore } from '@/stores/auth'

// A model preview belongs to the connection draft and login session that started it.
export function useModelSyncRequest(context: () => string) {
  const busy = ref(false)
  const authStore = useAuthStore()
  let generation = 0
  let disposed = false
  watch([context, () => authStore.authSessionVersion, () => authStore.user?.id], () => {
    generation++
    busy.value = false
  }, { flush: 'sync' })
  onBeforeUnmount(() => {
    disposed = true
    generation++
  })

  const begin = () => {
    if (busy.value || disposed) return null
    const request = ++generation
    const snapshot = context()
    const session = getAuthSessionVersion()
    const userID = authStore.user?.id
    busy.value = true
    const current = () => !disposed && request === generation && snapshot === context()
      && session === getAuthSessionVersion() && userID === authStore.user?.id
    return {
      current,
      finish: () => {
        if (request === generation) busy.value = false
      }
    }
  }
  return { busy, begin }
}
