import { describe, expect, it, vi } from 'vitest'
import {
  installChunkLoadRecovery,
  isChunkLoadError,
  recoverFromChunkLoadError
} from '../chunkLoadRecovery'

function createStorage() {
  const values = new Map<string, string>()
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value)
  }
}

describe('chunkLoadRecovery', () => {
  it('recognizes browser and bundler chunk failures', () => {
    expect(isChunkLoadError(new Error('Failed to fetch dynamically imported module: /assets/a.js'))).toBe(true)
    expect(isChunkLoadError(new Error('Importing a module script failed.'))).toBe(true)

    const webpackError = new Error('request failed')
    webpackError.name = 'ChunkLoadError'
    expect(isChunkLoadError(webpackError)).toBe(true)
    expect(isChunkLoadError(new Error('network error'))).toBe(false)
  })

  it('reloads once inside the cooldown window', () => {
    const storage = createStorage()
    const reload = vi.fn()
    const error = new Error('Failed to fetch dynamically imported module')

    expect(recoverFromChunkLoadError(error, { now: 100_000, reload, storage })).toBe(true)
    expect(recoverFromChunkLoadError(error, { now: 105_000, reload, storage })).toBe(false)
    expect(recoverFromChunkLoadError(error, { now: 111_000, reload, storage })).toBe(true)
    expect(reload).toHaveBeenCalledTimes(2)
  })

  it('prevents the Vite preload error and starts recovery', () => {
    sessionStorage.setItem('chunk_reload_attempted', String(Date.now()))
    const remove = installChunkLoadRecovery()
    const event = new Event('vite:preloadError', { cancelable: true }) as Event & { payload?: unknown }
    event.payload = new Error('Failed to fetch dynamically imported module')

    window.dispatchEvent(event)

    expect(event.defaultPrevented).toBe(true)
    remove()
    sessionStorage.removeItem('chunk_reload_attempted')
  })
})
