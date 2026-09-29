/**
 * PROTOTYPE — 票 #153（地图 #150）。三个变体答「版本号 + GitHub 图标 + 升级角标摆哪」，
 * 一个 `?state=` 参数答「升级弹框在 binary / docker / dev / 非 admin 下各长什么样」。
 * 全部是桩数据、无真实请求；DEV 构建才渲染，生产构建整个模块为空。
 * 定稿后本文件连同 App.tsx / MySpace.tsx / topshell.tsx 里的挂点一并从 main 摘掉，
 * 留在 prototype/version-badge-153 分支作一手资料。
 *
 * 变体（`?variant=`）：
 *   A 顶栏右侧：GitHub 图标 + 等宽版本号摆在头像左边；有新版时版本号前亮一个点，点开弹框。
 *   B 头像菜单：顶栏不动；菜单底部一行「Portage v0.4.9 · GitHub」，有新版时多一项「升级到 vX」，头像角上亮点。
 *   C 品牌旁：版本号与 GitHub 图标并进左侧品牌簇；有新版时品牌旁出一枚中性小胶囊「新版 vX」。
 *
 * 状态（`?state=`）：fresh 无新版 · new 有新版（binary）· docker 有新版（docker 形态）· dev 开发构建 · user 非 admin 视角。
 */
import { useEffect, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Confirm, CopyCode, Dialog } from '../ui'

export const PROTO_ON = import.meta.env.DEV

const VARIANTS = [
  { key: 'A', name: '顶栏右侧' },
  { key: 'B', name: '头像菜单' },
  { key: 'C', name: '品牌旁' },
] as const
const STATES = [
  { key: 'fresh', name: '无新版' },
  { key: 'new', name: '有新版 · binary' },
  { key: 'docker', name: '有新版 · docker' },
  { key: 'dev', name: 'dev 构建' },
  { key: 'user', name: '非 admin 视角' },
] as const
type VariantKey = (typeof VARIANTS)[number]['key']
type StateKey = (typeof STATES)[number]['key']

const REPO = 'https://github.com/SimonGino/portage'
const LATEST = { version: '0.5.0', date: '2026-09-27', url: `${REPO}/releases/tag/v0.5.0` }

function useProto() {
  const [sp] = useSearchParams()
  const variant = (VARIANTS.find((v) => v.key === sp.get('variant'))?.key ?? 'A') as VariantKey
  const state = (STATES.find((s) => s.key === sp.get('state'))?.key ?? 'new') as StateKey
  const current = state === 'dev' ? 'dev' : '0.4.9'
  const admin = state !== 'user'
  const distro: 'binary' | 'docker' = state === 'docker' ? 'docker' : 'binary'
  // dev 构建不查、非 admin 不提示：两种情况下「有新版」这个事实对当前页面不存在。
  const hasNew = admin && state !== 'dev' && state !== 'fresh'
  const clickable = admin && state !== 'dev'
  return { variant, state, current, admin, distro, hasNew, clickable }
}

/* ── GitHub 标记（Octicons mark-github，MIT），currentColor 跟 --ink 走，与品牌标记同一条纪律 ── */
function GitHubMark({ size = 18 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0 0 16 8c0-4.42-3.58-8-8-8Z" />
    </svg>
  )
}

function GitHubLink({ size, className }: { size?: number; className?: string }) {
  return (
    <a
      className={'proto-gh ' + (className ?? '')}
      href={REPO}
      target="_blank"
      rel="noreferrer"
      title="GitHub 仓库"
      aria-label="GitHub 仓库"
    >
      <GitHubMark size={size} />
    </a>
  )
}

/* ── 版本号：可点就是按钮（开弹框），不可点就是一段等宽字 ── */
function VersionText({ onOpen, dot }: { onOpen?: () => void; dot?: boolean }) {
  const { current, state } = useProto()
  const text = current === 'dev' ? 'dev' : `v${current}`
  const title = state === 'dev' ? '开发构建，不检查更新' : onOpen ? '版本与升级' : `Portage v${current}`
  if (!onOpen) {
    return (
      <span className="proto-ver mono" title={title}>
        {text}
      </span>
    )
  }
  return (
    <button type="button" className="proto-ver proto-ver-btn mono" title={title} onClick={onOpen}>
      {dot && <i className="proto-dot" aria-hidden="true" />}
      {text}
    </button>
  )
}

/* ── 变体 A：顶栏右侧 ── */
function SlotA() {
  const { hasNew, clickable } = useProto()
  const [open, setOpen] = useState(false)
  return (
    <>
      <GitHubLink />
      <VersionText onOpen={clickable ? () => setOpen(true) : undefined} dot={hasNew} />
      {open && <UpgradeDialog onClose={() => setOpen(false)} />}
    </>
  )
}

/* ── 变体 C：品牌簇 ── */
function BrandC() {
  const { hasNew, clickable } = useProto()
  const [open, setOpen] = useState(false)
  return (
    <>
      <VersionText onOpen={clickable && !hasNew ? () => setOpen(true) : undefined} />
      {hasNew && (
        <button type="button" className="proto-newpill" onClick={() => setOpen(true)}>
          <i className="proto-dot" aria-hidden="true" />
          新版 v{LATEST.version}
        </button>
      )}
      <GitHubLink size={16} className="proto-gh-brand" />
      {open && <UpgradeDialog onClose={() => setOpen(false)} />}
    </>
  )
}

/* ── 变体 B：头像菜单底部 ── */
function MenuB() {
  const { hasNew, clickable, current } = useProto()
  const [open, setOpen] = useState(false)
  return (
    <>
      {hasNew && (
        <button type="button" className="menu-item proto-menu-new" onClick={() => setOpen(true)}>
          <i className="proto-dot" aria-hidden="true" />
          升级到 v{LATEST.version}
        </button>
      )}
      <div className="proto-menu-foot">
        {clickable && !hasNew ? (
          <button type="button" className="proto-ver proto-ver-btn mono" onClick={() => setOpen(true)}>
            Portage v{current}
          </button>
        ) : (
          <span className="proto-ver mono">Portage {current === 'dev' ? 'dev' : `v${current}`}</span>
        )}
        <GitHubLink size={16} />
      </div>
      {open && <UpgradeDialog onClose={() => setOpen(false)} />}
    </>
  )
}

/* ── 挂点：三处各自按变体决定渲染什么 ── */
export function ProtoBrandSlot() {
  const { variant } = useProto()
  if (!PROTO_ON || variant !== 'C') return null
  return <BrandC />
}
export function ProtoRightSlot() {
  const { variant } = useProto()
  if (!PROTO_ON || variant !== 'A') return null
  return <SlotA />
}
export function ProtoMenuSlot() {
  const { variant } = useProto()
  if (!PROTO_ON || variant !== 'B') return null
  return <MenuB />
}
/** 变体 B 的头像角标：有新版时头像右上角一个点。包在 AvatarMenu 外面用。 */
export function ProtoAvatarDot({ children }: { children: ReactNode }) {
  const { variant, hasNew } = useProto()
  if (!PROTO_ON || variant !== 'B' || !hasNew) return <>{children}</>
  return <span className="proto-avatar-wrap">{children}</span>
}

/* ── 升级弹框：三变体共用，随 state 分 binary / docker / 已是最新 三种正文 ── */
type Phase = 'idle' | 'downloading' | 'verifying' | 'restarting' | 'done' | 'failed'
type Fail = 'ok' | 'download' | 'checksum' | 'readonly'
const FAIL_TEXT: Record<Exclude<Fail, 'ok'>, string> = {
  download: `下载失败：连不上 github.com。可设置 PORTAGE_DOWNLOAD_BASE 指向国内镜像后重试，或在机器上手动执行 portage upgrade。`,
  checksum: '校验失败：下载的文件与 checksums.txt 不一致，已丢弃、未替换。多半是下载被截断或代理改了内容，重试一次；仍失败就换下载源。',
  readonly: '不能替换：当前可执行文件所在目录对网关进程不可写。在机器上用有权限的账号执行 portage upgrade，或重新用 install.sh 安装。',
}

function UpgradeDialog({ onClose }: { onClose: () => void }) {
  const { hasNew, distro, current } = useProto()
  const [phase, setPhase] = useState<Phase>('idle')
  const [fail, setFail] = useState<Fail>('ok')
  const [checking, setChecking] = useState(false)

  // 桩时间线：下载 1.2s → 校验 0.6s → 重启 1.6s → 成功/失败。真实实现里「重启」段是
  // 前端轮询 /session 看版本号变化，这里只是让人看见每一步的形状。
  useEffect(() => {
    if (phase === 'idle' || phase === 'done' || phase === 'failed') return
    const next: Partial<Record<Phase, [Phase, number]>> = {
      downloading: ['verifying', 1200],
      verifying: ['restarting', 600],
      restarting: ['done', 1600],
    }
    const failAt: Record<Exclude<Fail, 'ok'>, Phase> = { download: 'downloading', checksum: 'verifying', readonly: 'verifying' }
    const [to, ms] = next[phase]!
    const t = setTimeout(() => setPhase(fail !== 'ok' && failAt[fail] === phase ? 'failed' : to), ms)
    return () => clearTimeout(t)
  }, [phase, fail])

  const busy = phase === 'downloading' || phase === 'verifying' || phase === 'restarting'

  return (
    <Dialog title="版本与升级" onClose={busy ? () => {} : onClose} guard={busy}>
      <div className="dialog-note">
        <p className="proto-verline">
          <span>
            当前 <code>v{current}</code>
          </span>
          {hasNew ? (
            <>
              <span>
                最新 <code>v{LATEST.version}</code> <span className="muted">（{LATEST.date} 发布）</span>
              </span>
              <a href={LATEST.url} target="_blank" rel="noreferrer">
                发布说明
              </a>
            </>
          ) : (
            <span className="muted">已是最新{checking ? '，正在重新检查…' : '（3 分钟前检查）'}</span>
          )}
        </p>

        {!hasNew && (
          <div className="form-actions">
            <button type="button" className="btn btn-quiet" onClick={onClose}>
              关闭
            </button>
            <button
              type="button"
              className="btn"
              disabled={checking}
              onClick={() => {
                setChecking(true)
                setTimeout(() => setChecking(false), 900)
              }}
            >
              重新检查
            </button>
          </div>
        )}

        {hasNew && distro === 'docker' && (
          <>
            <p>这台是 Docker 部署，网关不能替换自己所在的镜像。到部署目录执行：</p>
            <CopyCode value="docker compose pull && docker compose up -d" className="proto-cmd" />
            <p className="muted">
              钉了版本（<code>PORTAGE_IMAGE=…:0.4.9</code>）的话先把版本号改成 <code>0.5.0</code> 再执行。
              容器重建期间在途请求会被打断。
            </p>
            <div className="form-actions">
              <button type="button" className="btn btn-quiet" onClick={onClose}>
                关闭
              </button>
            </div>
          </>
        )}

        {hasNew && distro === 'binary' && phase === 'idle' && (
          <>
            <p>
              升级会下载 v{LATEST.version}、校验后替换当前可执行文件，然后停收新请求、等在途请求结束（最多 30 秒）再自行重启。
              <b>正在串流的会话会被打断。</b>
            </p>
            <div className="form-actions">
              <button type="button" className="btn btn-quiet" onClick={onClose}>
                取消
              </button>
              <Confirm
                label={`升级到 v${LATEST.version}`}
                confirm="确定升级？在途请求将被打断"
                onConfirm={() => setPhase('downloading')}
              />
            </div>
            <ProtoFailPicker fail={fail} setFail={setFail} />
          </>
        )}

        {hasNew && distro === 'binary' && phase !== 'idle' && (
          <>
            <ol className="proto-steps">
              <Step at="downloading" phase={phase} fail={fail}>
                下载 v{LATEST.version}
              </Step>
              <Step at="verifying" phase={phase} fail={fail}>
                校验并替换
              </Step>
              <Step at="restarting" phase={phase} fail={fail}>
                等在途请求结束、重启
              </Step>
            </ol>
            {phase === 'failed' && <div className="bar bar-error">{FAIL_TEXT[fail as Exclude<Fail, 'ok'>]}</div>}
            {phase === 'done' && (
              <div className="bar bar-ok">
                已升级到 v{LATEST.version}。网关已重启，刷新页面即可。
              </div>
            )}
            <div className="form-actions">
              {busy && (
                <span className="muted proto-busy">
                  {phase === 'restarting' ? '网关重启中，页面会自动恢复…' : '请勿关闭页面'}
                </span>
              )}
              {phase === 'failed' && (
                <>
                  <button type="button" className="btn btn-quiet" onClick={onClose}>
                    关闭
                  </button>
                  <button type="button" className="btn" onClick={() => setPhase('downloading')}>
                    重试
                  </button>
                </>
              )}
              {phase === 'done' && (
                <button type="button" className="btn" onClick={() => window.location.reload()}>
                  刷新页面
                </button>
              )}
            </div>
          </>
        )}
      </div>
    </Dialog>
  )
}

const ORDER: Phase[] = ['downloading', 'verifying', 'restarting', 'done']
function Step({ at, phase, fail, children }: { at: Phase; phase: Phase; fail: Fail; children: ReactNode }) {
  const failAt: Record<Exclude<Fail, 'ok'>, Phase> = { download: 'downloading', checksum: 'verifying', readonly: 'verifying' }
  const failedHere = phase === 'failed' && fail !== 'ok' && failAt[fail] === at
  const i = ORDER.indexOf(at)
  const cur = phase === 'failed' ? ORDER.indexOf(failAt[fail as Exclude<Fail, 'ok'>]) : ORDER.indexOf(phase)
  const st = failedHere ? 'fail' : i < cur ? 'done' : i === cur && phase !== 'done' ? 'now' : phase === 'done' ? 'done' : 'todo'
  return (
    <li className={'proto-step is-' + st}>
      <span className="proto-step-mark" aria-hidden="true">
        {st === 'done' ? '✓' : st === 'fail' ? '×' : st === 'now' ? '…' : ''}
      </span>
      {children}
    </li>
  )
}

/** 原型专用：选这次桩流程的结局。深底样式明示「不是设计的一部分」。 */
function ProtoFailPicker({ fail, setFail }: { fail: Fail; setFail: (f: Fail) => void }) {
  const opts: [Fail, string][] = [
    ['ok', '成功'],
    ['download', '下载失败'],
    ['checksum', '校验失败'],
    ['readonly', '不可写'],
  ]
  return (
    <div className="proto-chips" title="原型控制：模拟升级结局">
      <span>模拟结局</span>
      {opts.map(([k, n]) => (
        <button key={k} type="button" className={fail === k ? 'is-on' : ''} onClick={() => setFail(k)}>
          {n}
        </button>
      ))}
    </div>
  )
}

/* ── 底部切换条：变体 ←→ 与状态 chips。DEV 才渲染。 ── */
export function ProtoBar() {
  const [sp, setSp] = useSearchParams()
  const { variant, state } = useProto()
  const set = (k: string, v: string) => {
    const n = new URLSearchParams(sp)
    n.set(k, v)
    setSp(n, { replace: true })
  }
  const go = (d: number) => {
    const i = VARIANTS.findIndex((v) => v.key === variant)
    set('variant', VARIANTS[(i + d + VARIANTS.length) % VARIANTS.length].key)
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
  if (!PROTO_ON) return null
  const cur = VARIANTS.find((v) => v.key === variant)!
  return (
    <>
      <style>{CSS}</style>
      <div className="proto-bar">
        <div className="proto-bar-row">
          <button type="button" onClick={() => go(-1)}>
            ←
          </button>
          <span>
            {cur.key}（{cur.name}）
          </span>
          <button type="button" onClick={() => go(1)}>
            →
          </button>
        </div>
        <div className="proto-bar-row proto-bar-states">
          {STATES.map((s) => (
            <button key={s.key} type="button" className={s.key === state ? 'is-on' : ''} onClick={() => set('state', s.key)}>
              {s.name}
            </button>
          ))}
        </div>
      </div>
    </>
  )
}

const CSS = `
.proto-ver { font-size: 12px; color: var(--mute); letter-spacing: 0; white-space: nowrap; }
.proto-ver-btn { border: 0; background: none; padding: 4px 6px; border-radius: var(--r-sm); cursor: pointer; display: inline-flex; align-items: center; gap: 6px; }
.proto-ver-btn:hover { background: var(--hover); color: var(--ink); }
.proto-dot { width: 7px; height: 7px; border-radius: 50%; background: var(--warn-text); display: inline-block; flex: none; }
.proto-gh { display: inline-flex; align-items: center; color: var(--mute); padding: 4px; border-radius: var(--r-sm); }
.proto-gh:hover { color: var(--ink); background: var(--hover); }
.proto-gh-brand { margin-left: -4px; }
.proto-newpill { display: inline-flex; align-items: center; gap: 6px; border: 0; background: var(--sunken); color: var(--ink-2); font-size: 12px; padding: 3px 9px; border-radius: 999px; cursor: pointer; white-space: nowrap; }
.proto-newpill:hover { color: var(--ink); box-shadow: 0 0 0 2px var(--hover); }
.proto-menu-new { display: flex; align-items: center; gap: 8px; }
.proto-menu-foot { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-top: 6px; padding: 8px 4px 4px 10px; border-top: 1px solid var(--line-soft); }
.proto-avatar-wrap { position: relative; display: inline-flex; }
.proto-avatar-wrap::after { content: ''; position: absolute; top: -1px; right: -1px; width: 9px; height: 9px; border-radius: 50%; background: var(--warn-text); box-shadow: 0 0 0 2px var(--paper); pointer-events: none; }
.proto-verline { display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 14px; }
.proto-cmd { display: block; }
.proto-steps { margin: 0; padding: 0; list-style: none; display: flex; flex-direction: column; gap: 8px; }
.proto-step { display: flex; align-items: center; gap: 10px; color: var(--faint); }
.proto-step.is-now { color: var(--ink); }
.proto-step.is-done { color: var(--ink-2); }
.proto-step.is-fail { color: var(--err-text); }
.proto-step-mark { width: 18px; height: 18px; border-radius: 50%; border: 1px solid var(--line); display: grid; place-items: center; font-size: 11px; flex: none; }
.proto-step.is-done .proto-step-mark { background: var(--sunken); border-color: var(--sunken); }
.proto-step.is-fail .proto-step-mark { background: var(--err-bg); border-color: var(--err-line); }
.proto-busy { margin-right: auto; font-size: 13px; }
.proto-chips { display: flex; align-items: center; gap: 4px; font-size: 11px; color: #fff; background: #1c1c1e; border-radius: 999px; padding: 4px 6px 4px 10px; align-self: flex-start; }
.proto-chips span { margin-right: 4px; opacity: .7; }
.proto-chips button, .proto-bar button { border: 0; background: none; color: #fff; cursor: pointer; font-size: 12px; padding: 3px 8px; border-radius: 999px; }
.proto-chips button.is-on, .proto-bar button.is-on { background: rgba(255,255,255,.22); }
.proto-bar { position: fixed; bottom: 16px; left: 50%; transform: translateX(-50%); z-index: 9999; background: #1c1c1e; color: #fff; border-radius: 14px; box-shadow: 0 4px 16px rgba(0,0,0,.3); padding: 6px 10px; display: flex; flex-direction: column; gap: 2px; font-size: 13px; }
.proto-bar-row { display: flex; align-items: center; justify-content: center; gap: 10px; }
.proto-bar-states { gap: 2px; font-size: 12px; opacity: .95; }
`
