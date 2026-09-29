import { afterEach, describe, expect, it, vi } from 'vitest'
import { fetchLatest, isRelease, newer } from './release'

describe('newer', () => {
  it('按语义化版本比，不按字符串', () => {
    expect(newer('v0.10.0', '0.9.0')).toBe(true)
    expect(newer('v0.1.1', '0.1.0')).toBe(true)
  })

  it('相等、本地更新、解析不了都算没有新版', () => {
    expect(newer('v0.5.0', '0.5.0')).toBe(false)
    expect(newer('v0.5.0', '0.6.0')).toBe(false)
    expect(newer('v0.5.0', 'dev')).toBe(false)
  })

  it('只有纯 x.y.z 算发版构建', () => {
    expect(isRelease('0.1.1')).toBe(true)
    for (const v of ['dev', 'test', '0.1.1-rc.1', '0.1.1-SNAPSHOT-6eb2a33', undefined]) {
      expect(isRelease(v)).toBe(false)
    }
  })
})

describe('fetchLatest', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('releases/latest 404 回 null 且不写缓存', async () => {
    const store = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => store.set(k, v),
      removeItem: (k: string) => store.delete(k),
    })
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{"message":"Not Found"}', { status: 404 })))

    expect(await fetchLatest()).toBeNull()
    expect(store.size).toBe(0)
  })
})
