// PROTOTYPE（wayfinder #67，用后即弃）：用户面板信息架构三变体，`/prototype/panel?variant=A|B|C`。
// 问题：用户面板与 /admin 的关系（同壳按角色 / 双空间切换 / 独立入口）+ 页面清单 + 导航结构。
// 全部假数据、只读、不打接口；仅 DEV 构建可达（App.tsx 里 import.meta.env.DEV 闸）。
import { type ReactNode, useEffect } from 'react'
import { useSearchParams } from 'react-router-dom'
import { PortageMark } from '../brand'
import { Card } from '../ui'

// ---------- 假数据 ----------

const ME = { email: 'alice@example.com', name: 'Alice' }
const QUOTA = { used: 3.42, limit: 20 }
const KEYS = [
  { id: 1, name: 'cursor', key: 'sk-ptg-a1b2…f9', allowed: '*', disabled: false, created: '2026-08-02' },
  { id: 2, name: 'cheap-only', key: 'sk-ptg-c3d4…e7', allowed: 'gpt-5-mini', disabled: false, created: '2026-08-11' },
  { id: 3, name: 'old-phone', key: '', allowed: '*', disabled: true, created: '2026-07-20' },
]
const USAGE = [
  { model: 'claude-sonnet-5', calls: 412, tokens: '1.2M', cost: 2.87 },
  { model: 'gpt-5-mini', calls: 96, tokens: '340K', cost: 0.41 },
  { model: 'deepseek-v3', calls: 55, tokens: '210K', cost: 0.14 },
]
const MODELS = ['claude-sonnet-5', 'gpt-5-mini', 'deepseek-v3', 'glm-4.7']

const USER_NAV = [
  { label: '我的 Key', active: true },
  { label: '用量与配额', active: false },
  { label: '模型', active: false },
  { label: '账号', active: false },
]

function QuotaBar({ compact }: { compact?: boolean }) {
  const pct = Math.round((QUOTA.used / QUOTA.limit) * 100)
  return (
    <div>
      {!compact && (
        <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 6 }}>
          <span>
            本月已用 <b>${QUOTA.used.toFixed(2)}</b> / ${QUOTA.limit}（UTC 自然月）
          </span>
          <span style={{ color: 'var(--muted, #888)' }}>{pct}%</span>
        </div>
      )}
      <div style={{ height: 8, borderRadius: 4, background: 'rgba(128,128,128,.18)' }}>
        <div style={{ width: `${pct}%`, height: '100%', borderRadius: 4, background: '#2f4a6b' }} />
      </div>
      {compact && (
        <div style={{ marginTop: 4, fontSize: 12, color: 'var(--muted, #888)' }}>
          ${QUOTA.used.toFixed(2)} / ${QUOTA.limit}
        </div>
      )}
    </div>
  )
}

function KeysTable() {
  return (
    <table className="table">
      <thead>
        <tr>
          <th>名称</th>
          <th>Key</th>
          <th>模型白名单</th>
          <th>创建于</th>
          <th className="col-actions" />
        </tr>
      </thead>
      <tbody>
        {KEYS.map((k) => (
          <tr key={k.id} className={k.disabled ? 'is-off' : undefined}>
            <td>{k.name}</td>
            <td>{k.key ? <code>{k.key}</code> : <span style={{ color: 'var(--muted, #888)' }}>（早期 key，明文未存）</span>}</td>
            <td>{k.allowed}</td>
            <td className="nowrap">{k.created}</td>
            <td className="col-actions">
              <span className="row-actions">
                <button className="btn btn-quiet">复制</button>
                <button className="btn btn-quiet">停用</button>
              </span>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function UsageTable() {
  return (
    <table className="table">
      <thead>
        <tr>
          <th>模型</th>
          <th className="num">调用</th>
          <th className="num">token</th>
          <th className="num">费用</th>
        </tr>
      </thead>
      <tbody>
        {USAGE.map((u) => (
          <tr key={u.model}>
            <td>
              <code>{u.model}</code>
            </td>
            <td className="num">{u.calls}</td>
            <td className="num">{u.tokens}</td>
            <td className="num">${u.cost.toFixed(2)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

// ---------- 变体 A：同壳按角色 ----------
// 现有 /admin 壳原封不动，登录角色是 user 时导航换成四项、账户区换成本人。
// admin 看到的仍是今天的五项 + 用户管理；同一应用同一入口同一视觉。

function VariantA() {
  return (
    <div className="app">
      <aside className="rail" aria-label="导航">
        <span className="brand">
          <PortageMark size={22} />
          <b>Portage</b>
        </span>
        <nav className="nav">
          {USER_NAV.map((n) => (
            <a key={n.label} className={n.active ? 'active' : undefined} href="#">
              {n.label}
            </a>
          ))}
        </nav>
        <div className="rail-mid" />
        <div className="account">
          <div className="who">
            <div className="acct-avatar" aria-hidden>
              A
            </div>
            <div>
              <strong>{ME.name}</strong>
              <span>{ME.email}</span>
            </div>
          </div>
          <div className="acct-acts">
            <button type="button">改密码</button>
            <button type="button">退出</button>
          </div>
        </div>
      </aside>
      <main className="main">
        <Card title="我的 Key" action={<button className="btn btn-primary">新建 Key</button>}>
          <KeysTable />
        </Card>
        <Card title="本月配额">
          <QuotaBar />
        </Card>
      </main>
    </div>
  )
}

// ---------- 变体 B：双空间切换 ----------
// 仍是同一应用，但 rail 顶部有「管理 ⇄ 我的」空间切换（普通用户只有「我的」，
// admin 两个都有）。「我的」空间是独立的一套导航；admin 治理与个人使用不混排。

function VariantB() {
  return (
    <div className="app">
      <aside className="rail" aria-label="导航">
        <span className="brand">
          <PortageMark size={22} />
          <b>Portage</b>
        </span>
        <div
          style={{
            display: 'flex',
            margin: '10px 14px 4px',
            borderRadius: 8,
            overflow: 'hidden',
            border: '1px solid rgba(128,128,128,.3)',
            fontSize: 13,
          }}
        >
          <button type="button" style={{ flex: 1, padding: '5px 0', border: 0, background: 'transparent', cursor: 'pointer' }}>
            管理
          </button>
          <button
            type="button"
            style={{ flex: 1, padding: '5px 0', border: 0, background: '#2f4a6b', color: '#fff', cursor: 'pointer' }}
          >
            我的
          </button>
        </div>
        <nav className="nav">
          {USER_NAV.map((n) => (
            <a key={n.label} className={n.active ? 'active' : undefined} href="#">
              {n.label}
            </a>
          ))}
        </nav>
        <div className="rail-mid" />
        <div className="account">
          <div className="who">
            <div className="acct-avatar" aria-hidden>
              A
            </div>
            <div>
              <strong>{ME.name}</strong>
              <span>admin · 我的空间</span>
            </div>
          </div>
        </div>
      </aside>
      <main className="main">
        <Card title="本月配额">
          <QuotaBar />
        </Card>
        <Card title="我的 Key" action={<button className="btn btn-primary">新建 Key</button>}>
          <KeysTable />
        </Card>
        <Card title="我的用量（近 30 天）">
          <UsageTable />
        </Card>
      </main>
    </div>
  )
}

// ---------- 变体 C：独立入口 ----------
// 用户面板是 /panel 下另一个轻应用：无 rail，顶栏 tab + 单列文档流。
// /admin 完全不动；面板只有用户四页，视觉更「产品」少「控制台」。

function VariantC() {
  return (
    <div style={{ minHeight: '100vh', background: 'var(--bg, #f6f5f2)' }}>
      <header
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 24,
          padding: '0 28px',
          height: 52,
          borderBottom: '1px solid rgba(128,128,128,.25)',
        }}
      >
        <span style={{ display: 'flex', alignItems: 'center', gap: 8, fontWeight: 600 }}>
          <PortageMark size={20} /> Portage
        </span>
        <nav style={{ display: 'flex', gap: 18, fontSize: 14 }}>
          {USER_NAV.map((n) => (
            <a
              key={n.label}
              href="#"
              style={{
                padding: '15px 2px',
                borderBottom: n.active ? '2px solid #2f4a6b' : '2px solid transparent',
                color: n.active ? 'inherit' : 'var(--muted, #777)',
                textDecoration: 'none',
              }}
            >
              {n.label}
            </a>
          ))}
        </nav>
        <span style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 10, fontSize: 13 }}>
          <QuotaMini />
          <span className="acct-avatar" aria-hidden>
            A
          </span>
        </span>
      </header>
      <main style={{ maxWidth: 760, margin: '28px auto', padding: '0 20px', display: 'grid', gap: 20 }}>
        <Card title="本月配额">
          <QuotaBar />
        </Card>
        <Card title="我的 Key" action={<button className="btn btn-primary">新建 Key</button>}>
          <KeysTable />
        </Card>
        <Card title="可用模型">
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
            {MODELS.map((m) => (
              <code key={m} style={{ padding: '3px 8px', borderRadius: 6, background: 'rgba(128,128,128,.12)' }}>
                {m}
              </code>
            ))}
          </div>
        </Card>
      </main>
    </div>
  )
}

function QuotaMini() {
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
      <span style={{ width: 72 }}>
        <QuotaBar compact />
      </span>
    </span>
  )
}

// ---------- 切换条 ----------

const VARIANTS: { key: string; name: string; el: ReactNode }[] = [
  { key: 'A', name: '同壳按角色', el: <VariantA /> },
  { key: 'B', name: '双空间切换', el: <VariantB /> },
  { key: 'C', name: '独立入口', el: <VariantC /> },
]

export default function PanelPrototype() {
  const [params, setParams] = useSearchParams()
  const cur = Math.max(
    0,
    VARIANTS.findIndex((v) => v.key === (params.get('variant') ?? 'A')),
  )
  const go = (d: number) => {
    const next = VARIANTS[(cur + d + VARIANTS.length) % VARIANTS.length]
    setParams({ variant: next.key }, { replace: true })
  }
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement
      if (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable) return
      if (e.key === 'ArrowLeft') go(-1)
      if (e.key === 'ArrowRight') go(1)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })
  return (
    <>
      {VARIANTS[cur].el}
      <div
        style={{
          position: 'fixed',
          bottom: 16,
          left: '50%',
          transform: 'translateX(-50%)',
          display: 'flex',
          alignItems: 'center',
          gap: 12,
          padding: '8px 14px',
          borderRadius: 999,
          background: '#1c1c1e',
          color: '#fff',
          boxShadow: '0 4px 16px rgba(0,0,0,.3)',
          fontSize: 13,
          zIndex: 9999,
        }}
      >
        <button onClick={() => go(-1)} style={{ background: 'none', border: 0, color: '#fff', cursor: 'pointer' }}>
          ←
        </button>
        <span>
          {VARIANTS[cur].key}（{VARIANTS[cur].name}）
        </span>
        <button onClick={() => go(1)} style={{ background: 'none', border: 0, color: '#fff', cursor: 'pointer' }}>
          →
        </button>
      </div>
    </>
  )
}
