import { afterEach, describe, expect, it } from 'vitest'

import {
  DEFAULT_ENDPOINT_ID,
  ENDPOINT_STORAGE_KEY,
  loadSavedEndpointId,
  pickEndpoint,
  resolveApiBases,
  resolveEndpointOptions,
  saveEndpointId
} from '../apiEndpoints'

const ORIGIN = 'https://site.example'

describe('apiEndpoints', () => {
  afterEach(() => localStorage.clear())

  it('localStorage key 与文档页一致', () => {
    expect(ENDPOINT_STORAGE_KEY).toBe('docs_api_endpoint')
  })

  it('resolveApiBases 去掉结尾的 / 和 /v1，留空时退回站点来源', () => {
    expect(resolveApiBases(' https://a.example/v1/ ', ORIGIN)).toEqual({ base: 'https://a.example', v1: 'https://a.example/v1' })
    expect(resolveApiBases('', ORIGIN).base).toBe(ORIGIN)
    expect(resolveApiBases(undefined, ORIGIN).v1).toBe(`${ORIGIN}/v1`)
  })

  it('没有自定义端点时只有默认地址', () => {
    expect(resolveEndpointOptions('https://api.example', [], ORIGIN).map((o) => o.id)).toEqual([DEFAULT_ENDPOINT_ID])
    expect(resolveEndpointOptions('https://api.example', undefined, ORIGIN)).toHaveLength(1)
  })

  it('自定义端点：名称说明原样保留，无名称用域名，非 http(s) 与重复地址被丢掉', () => {
    const opts = resolveEndpointOptions(
      'https://api.example/',
      [
        { name: ' CDN 加速 ', endpoint: 'https://cdn.example/v1/', description: ' 国内更快 ' },
        { name: '', endpoint: 'https://bare.example' },
        { name: 'x', endpoint: 'ftp://nope.example' },
        { name: 'dup', endpoint: 'https://api.example/v1' },
        { name: 'dup2', endpoint: 'https://cdn.example' }
      ],
      ORIGIN
    )
    expect(opts.map((o) => [o.id, o.name, o.description, o.isDefault])).toEqual([
      ['default', '', '', true],
      ['https://cdn.example', 'CDN 加速', '国内更快', false],
      ['https://bare.example', 'bare.example', '', false]
    ])
    expect(opts[1].v1).toBe('https://cdn.example/v1')
  })

  it('pickEndpoint：保存的端点已被删除时回落到默认地址', () => {
    const opts = resolveEndpointOptions('https://api.example', [{ name: 'a', endpoint: 'https://a.example' }], ORIGIN)
    expect(pickEndpoint(opts, 'https://a.example').name).toBe('a')
    expect(pickEndpoint(opts, 'https://gone.example').isDefault).toBe(true)
  })

  it('持久化：自定义端点写入，选回默认时删除，读不到时是默认', () => {
    expect(loadSavedEndpointId()).toBe(DEFAULT_ENDPOINT_ID)
    saveEndpointId('https://a.example')
    expect(localStorage.getItem('docs_api_endpoint')).toBe('https://a.example')
    expect(loadSavedEndpointId()).toBe('https://a.example')
    saveEndpointId(DEFAULT_ENDPOINT_ID)
    expect(localStorage.getItem('docs_api_endpoint')).toBeNull()
  })
})
