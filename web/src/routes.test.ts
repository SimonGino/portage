import { describe, expect, it } from 'vitest'
import { LEGACY_REDIRECTS, NAV } from './routes'

describe('routes', () => {
  it('旧路径跳转表', () => {
    expect(LEGACY_REDIRECTS).toEqual({
      '/keys': '/gateway',
      '/access-points': '/routing',
      '/rankings': '/usage',
      '/logs': '/usage/logs',
      '/pricing': '/channels/pricing',
      '/overview': '/usage',
    })
  })

  it('五项导航，首项渠道', () => {
    expect(NAV.map((n) => n.label)).toEqual(['渠道', '网关', '路由', '用量', '用户'])
    expect(NAV[0].to).toBe('/channels')
  })

  it('跳转目标都不再是旧路径（无链式跳转）', () => {
    for (const to of Object.values(LEGACY_REDIRECTS)) expect(LEGACY_REDIRECTS[to]).toBeUndefined()
  })
})
