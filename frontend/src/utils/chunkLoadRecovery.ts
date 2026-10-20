const CHUNK_RELOAD_KEY = 'chunk_reload_attempted'
const CHUNK_RELOAD_COOLDOWN_MS = 10_000

type ReloadStorage = Pick<Storage, 'getItem' | 'setItem'>

export function isChunkLoadError(error: unknown): boolean {
  if (!(error instanceof Error)) return false

  return (
    error.message.includes('Failed to fetch dynamically imported module') ||
    error.message.includes('Importing a module script failed') ||
    error.message.includes('Loading chunk') ||
    error.message.includes('Loading CSS chunk') ||
    error.name === 'ChunkLoadError'
  )
}

export function recoverFromChunkLoadError(
  error: unknown,
  options: {
    now?: number
    reload?: () => void
    storage?: ReloadStorage
  } = {}
): boolean {
  if (!isChunkLoadError(error)) return false

  const now = options.now ?? Date.now()
  const storage = options.storage ?? window.sessionStorage
  const reload = options.reload ?? (() => window.location.reload())
  const lastReload = Number.parseInt(storage.getItem(CHUNK_RELOAD_KEY) || '', 10)

  if (Number.isFinite(lastReload) && now - lastReload <= CHUNK_RELOAD_COOLDOWN_MS) {
    return false
  }

  storage.setItem(CHUNK_RELOAD_KEY, String(now))
  reload()
  return true
}

export function installChunkLoadRecovery(): () => void {
  const handlePreloadError = (event: Event) => {
    const preloadEvent = event as Event & { payload?: unknown }
    if (!isChunkLoadError(preloadEvent.payload)) return

    event.preventDefault()
    recoverFromChunkLoadError(preloadEvent.payload)
  }

  window.addEventListener('vite:preloadError', handlePreloadError)
  return () => window.removeEventListener('vite:preloadError', handlePreloadError)
}

