import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { api, ApiError } from './api'
import type { SessionState } from './api'
import { fetchLatest, isRelease, newer, REPO, type Latest } from './release'
import { Confirm, CopyCode, Dialog } from './ui'

/** 品牌簇要的那几样：版本与分发形态随 /session 来，admin 决定查不查新版、能不能点开弹框。 */
export interface Build {
  version?: string
  distro?: SessionState['distro']
  admin: boolean
}

/**
 * 品牌簇尾巴（DESIGN §2 v0.65，口径层 v1.38 ④）：版本号 → 新版胶囊 → GitHub 图标，两空间同形。
 *
 * - `dev` / `test` 这类非发版构建：显原样、不可点、不查更新。
 * - 非 admin：只见版本号与图标，不查也不可点——新版提示只给能动手升级的人。
 * - admin 有新版：版本号不可点，后面多一枚胶囊开弹框；无新版（含查不到）：版本号本身开同一弹框。
 */
export function BrandVersion({ version, distro, admin }: Build) {
  const release = isRelease(version)
  const checkable = admin && release
  const [open, setOpen] = useState(false)
  const [latest, setLatest] = useState<Latest | null>(null)
  const [checking, setChecking] = useState(false)

  const check = useCallback(async (force: boolean) => {
    setChecking(true)
    try {
      setLatest(await fetchLatest(force))
    } finally {
      setChecking(false)
    }
  }, [])

  useEffect(() => {
    if (checkable) void check(false)
  }, [checkable, check])

  const hasNew = checkable && !!latest && newer(latest.tag, version)

  return (
    <>
      {version &&
        (checkable && !hasNew ? (
          <button type="button" className="brand-ver" title="版本与升级" onClick={() => setOpen(true)}>
            v{version}
          </button>
        ) : (
          <span className="brand-ver" title={release ? undefined : '开发构建，不检查更新'}>
            {release ? `v${version}` : version}
          </span>
        ))}
      {hasNew && (
        <button type="button" className="brand-newpill" onClick={() => setOpen(true)}>
          <i className="brand-newpill-dot" aria-hidden />
          新版 {latest.tag}
        </button>
      )}
      <a className="brand-gh" href={REPO} target="_blank" rel="noreferrer" title="GitHub 仓库" aria-label="GitHub 仓库">
        <GitHubMark />
      </a>
      {open && release && (
        <UpgradeDialog
          version={version}
          distro={distro}
          latest={latest}
          checking={checking}
          onRecheck={() => void check(true)}
          onClose={() => setOpen(false)}
        />
      )}
    </>
  )
}

/** Octicons mark-github（MIT）。单色 currentColor，跟品牌标记同一条纪律（DESIGN §11）。 */
function GitHubMark() {
  return (
    <svg width={16} height={16} viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0 0 16 8c0-4.42-3.58-8-8-8Z" />
    </svg>
  )
}

function ago(ms: number) {
  const min = Math.floor((Date.now() - ms) / 60000)
  if (min < 1) return '刚刚检查'
  if (min < 60) return `${min} 分钟前检查`
  return `${Math.floor(min / 60)} 小时前检查`
}

/**
 * 「版本与升级」弹框（DESIGN §2 v0.65）：一行版本对照，下面按「有没有新版 × 分发形态」分三种正文。
 * 查不到（GitHub 404 / 不通 / 限额）不冒充「已是最新」，单独说一句，同样给「重新检查」。
 */
function UpgradeDialog({
  version,
  distro,
  latest,
  checking,
  onRecheck,
  onClose,
}: {
  version: string
  distro?: SessionState['distro']
  latest: Latest | null
  checking: boolean
  onRecheck: () => void
  onClose: () => void
}) {
  const [busy, setBusy] = useState(false)
  const hasNew = !!latest && newer(latest.tag, version)

  return (
    // 升级进行中不许关：POST 还没回或网关正在重启，关了框就没人去轮询、看不到结局。
    <Dialog title="版本与升级" onClose={busy ? () => {} : onClose} guard={busy}>
      <div className="dialog-note">
        <p className="ver-line">
          <span>
            当前 <code>v{version}</code>
          </span>
          {hasNew ? (
            <>
              <span>
                最新 <code>{latest.tag}</code>
                {latest.published && <span className="muted">（{latest.published.slice(0, 10)} 发布）</span>}
              </span>
              <a href={`${REPO}/releases/tag/${latest.tag}`} target="_blank" rel="noreferrer">
                发布说明
              </a>
            </>
          ) : (
            <span className="muted">
              {checking
                ? '正在检查…'
                : latest
                  ? `已是最新（${ago(latest.checkedAt)}）`
                  : '暂时查不到最新版本（GitHub 不通或尚无发布）'}
            </span>
          )}
        </p>

        {!hasNew && (
          <div className="form-actions">
            <button type="button" className="btn btn-quiet" onClick={onClose}>
              关闭
            </button>
            <button type="button" className="btn" disabled={checking} onClick={onRecheck}>
              重新检查
            </button>
          </div>
        )}

        {hasNew && distro === 'docker' && (
          <>
            <p>这台是 Docker 部署，网关不能替换自己所在的镜像。到部署目录执行：</p>
            <CopyCode value="docker compose pull && docker compose up -d" />
            <p className="muted">
              钉了版本（<code>PORTAGE_IMAGE=…:{version}</code>）的先把版本号改成 <code>{latest.tag.replace(/^v/, '')}</code>{' '}
              再执行。容器重建期间在途请求会被打断。
            </p>
            <div className="form-actions">
              <button type="button" className="btn btn-quiet" onClick={onClose}>
                关闭
              </button>
            </div>
          </>
        )}

        {hasNew && distro !== 'docker' && (
          <Upgrade version={version} target={latest.tag.replace(/^v/, '')} onBusy={setBusy} onClose={onClose} />
        )}
      </div>
    </Dialog>
  )
}

type Phase = 'idle' | 'posting' | 'restarting' | 'done' | 'failed'

/** 失败落在哪一步、说什么。词表见展开层 §7.11 `POST /panel/api/upgrade`。 */
function failure(e: unknown): { step: number; text: string } {
  // 接口不存在：老网关（v0.5.0 以前）没有这条路由，走 /panel/api 的 JSON 404。
  if (e instanceof ApiError && e.status === 404) {
    return { step: 0, text: '这台网关还没有一键升级接口。在机器上用 install.sh 装新版，再重启 portage。' }
  }
  const code = e instanceof Error ? e.message : String(e)
  switch (code) {
    case 'download_failed':
      return {
        step: 0,
        text: '下载失败：连不上 GitHub。给网关进程设置 PORTAGE_DOWNLOAD_BASE（下载代理前缀）后重试，或在机器上手动执行 portage upgrade。',
      }
    case 'checksum_mismatch':
      return {
        step: 1,
        text: '校验失败：下载的文件与 checksums.txt 不一致，已丢弃、未替换。多半是下载被截断或代理改了内容，重试一次；仍失败就换下载源。',
      }
    case 'not_writable':
      return {
        step: 1,
        text: '不能替换：可执行文件所在目录对网关进程不可写。在机器上用有权限的账号执行 portage upgrade，或重新用 install.sh 安装。',
      }
    case 'already_running':
      return { step: 0, text: '已有一次升级在进行，等它结束后刷新页面。' }
    case 'unsupported_distro':
      return { step: 0, text: '这台是 Docker 部署，不能一键升级。' }
    default:
      return { step: 0, text: code }
  }
}

/**
 * binary 形态的升级流程：Confirm 两段式 → POST（同步做完下载、校验、替换）→ 200 后网关收场并
 * 自重启，这边每 2s 轮询 /session，版本号变了即刷新；60s 没变露出「刷新页面」兜底，轮询不停。
 */
function Upgrade({
  version,
  target,
  onBusy,
  onClose,
}: {
  version: string
  target: string
  onBusy: (b: boolean) => void
  onClose: () => void
}) {
  const [phase, setPhase] = useState<Phase>('idle')
  const [fail, setFail] = useState<{ step: number; text: string } | null>(null)
  const [late, setLate] = useState(false)

  useEffect(() => onBusy(phase === 'posting' || phase === 'restarting'), [phase, onBusy])

  useEffect(() => {
    if (phase !== 'restarting') return
    const poll = setInterval(async () => {
      try {
        const s = await api.get<SessionState>('/session')
        if (s.version && s.version !== version) {
          setPhase('done')
          // 让 ✓ 与成功条露一眼再刷新，不然像是页面自己闪了一下。
          setTimeout(() => window.location.reload(), 1000)
        }
      } catch {
        // 重启窗口里连不上、反代回 502 都是常态，下一拍再问。
      }
    }, 2000)
    const giveUp = setTimeout(() => setLate(true), 60_000)
    return () => {
      clearInterval(poll)
      clearTimeout(giveUp)
    }
  }, [phase, version])

  const start = async () => {
    setFail(null)
    setLate(false)
    setPhase('posting')
    try {
      await api.post('/upgrade', { version: target })
      setPhase('restarting')
    } catch (e) {
      setFail(failure(e))
      setPhase('failed')
    }
  }

  if (phase === 'idle') {
    return (
      <>
        <p>
          升级会下载 v{target}、校验后替换当前可执行文件，然后停收新请求、等在途请求结束（最多 30 秒）再自行重启。
          <b>正在串流的会话会被打断。</b>
        </p>
        <div className="form-actions">
          <button type="button" className="btn btn-quiet" onClick={onClose}>
            取消
          </button>
          <Confirm label={`升级到 v${target}`} confirm="确定升级？在途请求将被打断" onConfirm={() => void start()} />
        </div>
      </>
    )
  }

  // 当前走到第几步：POST 在途 = 0；POST 成功 = 前两步已完成、在等重启；done = 全部完成。
  const at = phase === 'posting' ? 0 : phase === 'restarting' ? 2 : phase === 'done' ? 3 : (fail?.step ?? 0)
  const step = (i: number, label: ReactNode) => {
    const st = phase === 'failed' && i === at ? 'fail' : i < at ? 'done' : i === at ? 'now' : 'todo'
    return (
      <li className={'is-' + st}>
        <span className="upgrade-step-mark" aria-hidden>
          {{ done: '✓', now: '…', fail: '×', todo: '' }[st]}
        </span>
        {label}
      </li>
    )
  }

  return (
    <>
      <ol className="upgrade-steps">
        {step(0, `下载 v${target}`)}
        {step(1, '校验并替换')}
        {step(2, '等在途请求结束、重启')}
      </ol>
      {phase === 'failed' && fail && <div className="bar bar-error">{fail.text}</div>}
      {phase === 'done' && <div className="bar bar-ok">已升级到 v{target}，正在刷新页面…</div>}
      {late && phase === 'restarting' && (
        <p className="muted">60 秒内没等到新版本上线。网关可能已经起来了，刷新页面看看；仍是旧版就去机器上看日志。</p>
      )}
      <div className="form-actions">
        {phase === 'posting' && <span className="muted upgrade-busy">请勿关闭页面</span>}
        {phase === 'restarting' && <span className="muted upgrade-busy">网关重启中，页面会自动刷新…</span>}
        {late && phase === 'restarting' && (
          <button type="button" className="btn" onClick={() => window.location.reload()}>
            刷新页面
          </button>
        )}
        {phase === 'failed' && (
          <>
            <button type="button" className="btn btn-quiet" onClick={onClose}>
              关闭
            </button>
            <button type="button" className="btn" onClick={() => void start()}>
              重试
            </button>
          </>
        )}
      </div>
    </>
  )
}
