// 管理空间五项导航与旧路径跳转表（DESIGN v0.71 §2、口径层 v1.46）。
// 跳转是前端 <Navigate>（同旧 /overview 做法），不是服务端 302。

export const NAV: { to: string; label: string }[] = [
  { to: '/channels', label: '渠道' },
  { to: '/gateway', label: '网关' },
  { to: '/routing', label: '路由' },
  { to: '/usage', label: '用量' },
  { to: '/users', label: '用户' },
]

export const USAGE_TABS = [
  { to: '/usage', label: '排行' },
  { to: '/usage/logs', label: '调用记录' },
]

export const CHANNEL_TABS = [
  { to: '/channels', label: '渠道' },
  { to: '/channels/pricing', label: '定价' },
]

export const LEGACY_REDIRECTS: Record<string, string> = {
  '/keys': '/gateway',
  '/access-points': '/routing',
  '/rankings': '/usage',
  '/logs': '/usage/logs',
  '/pricing': '/channels/pricing',
  '/overview': '/usage',
}
