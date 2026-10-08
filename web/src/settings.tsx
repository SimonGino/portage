import { useEffect, useRef, useState } from 'react'
import { api, download, importConfig, previewImport } from './api'
import type { AuthSettings } from './api'
import { Segmented } from './fields'
import { Confirm, CopyCode, Dialog, ErrorBar, Field, useList } from './ui'

/**
 * 设置面（DESIGN §2 v0.71，#180）：右上齿轮点开的弹框，收两样——登录与邮件配置、
 * 导出 / 导入配置。两者都是全局动作，不挂进任何一页。
 *
 * 登录与邮件是个攒改动的表单：有未保存改动时遮罩点击不关（Esc 照关），同「上游设置」。
 * 从里面开出的弹框（测试邮件、导入试算）开着时，Esc 只关最上层由 Dialog 自己管。
 */
export function SettingsDialog({ onClose }: { onClose: () => void }) {
  const [dirty, setDirty] = useState(false)
  return (
    <Dialog title="设置" guard={dirty} onClose={onClose}>
      <div className="form">
        <h3 className="form-section">登录与邮件配置</h3>
        <AuthSettingsSection onDirtyChange={setDirty} />
        <h3 className="form-section">导出 / 导入配置</h3>
        <div className="form-actions">
          <ExportButton />
          <ImportButton />
        </div>
      </div>
    </Dialog>
  )
}

/**
 * 把整份业务配置导成 channels.yaml 下载（口径层 §2.9 #32）。
 *
 * 放在设置面里而不是某一页里（v0.71 前在头像菜单、更早在左栏底部，理由不变）：它导的是**全部**
 * 业务配置，渠道、接入点、API Key 一份都不落，挂在其中任何一页下面都会读成
 * 「只导这一页的东西」。
 *
 * 沿用当前会话、不再问一次口令——登录者本来就能在页面上逐条看到全部秘密，导出只是
 * 把 1+N 次点击压成一次。失败只有一类（存量 API Key 拿不到原值），报文里点了名，
 * 用弹框来显示。
 */
function ExportButton() {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  return (
    <>
      <button
        type="button"
        className="btn btn-quiet"
        disabled={busy}
        title="导出全部业务配置，用于部署一台无管理界面的纯转发实例"
        onClick={async () => {
          setBusy(true)
          try {
            await download('/export', 'channels.yaml')
          } catch (err) {
            setError(err instanceof Error ? err.message : String(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        {busy ? '导出中…' : '导出配置'}
      </button>
      {error && (
        <Dialog title="导出失败" onClose={() => setError('')}>
          <div className="dialog-note">
            <ErrorBar message={error} />
            <div className="form-actions">
              <button type="button" className="btn btn-quiet" onClick={() => setError('')}>
                知道了
              </button>
            </div>
          </div>
        </Dialog>
      )}
    </>
  )
}

/**
 * 把一份 channels.yaml 一次性导入并**整份覆盖**当前业务配置（#59）。
 *
 * 与导出并排放在设置面里，理由同款：它动的是**全部**业务配置，挂在任何一页下面
 * 都会读成「只导这一页的东西」。
 *
 * 流程（口径层 v1.03，推翻 v0.37 的「先警告后清单」时序）：选文件 → 立即试算 →
 * 确认框直接摆试算结果（对账单：汇总 + 逐行清单）→ 导入。试算与真导入同一条链路，
 * 所以 400 的原文在确认阶段就看全——试算被闸打回时真导入也会被同一道闸打回，确认键
 * 随之整个收起（摆着它就是邀请人去撞闸）。覆盖语义只剩常驻一句；「整份回滚、一次
 * 报全」是后端事务的保证，真导入失败时 400 原文自会说话，不再写进确认框说教。
 * 确认键用 danger 形态：全量覆盖是弹框里唯一主操作，第一眼就该危险，两段式确认的
 * 防误触语义不变。成功弹框缩成一句——清单在确认阶段已经看过，重复摆第二遍是同一份
 * 数据说两遍。成功关框后整页重载：整份配置换掉了，各页面攒着的本地状态全部过期，
 * 重载比逐页打补丁诚实。
 */
type ImportPreview =
  | { state: 'loading' }
  | { state: 'error'; message: string }
  | { state: 'ok'; changes: string[] }

// 汇总行按清单动词前缀数。清单格式（「新增渠道 X」「删除 API Key Y」）是后端
// reconcile 的输出、前后端同仓：后端改动词这里会悄悄归零，别只改一边。
// others 兜「新增/删除」前缀之外的行（如 headers 改动/清空，#160）——不数它们
// 汇总句就会在清单明明列着一行时读成「新增 0 项、删除 0 项」（#173）。
const adds = (changes: string[]) => changes.filter((c) => c.startsWith('新增')).length
const dels = (changes: string[]) => changes.filter((c) => c.startsWith('删除')).length
const others = (changes: string[]) => changes.length - adds(changes) - dels(changes)

// 清空额外出站头是静默破坏性的（headers 只在「上游设置」弹框里看得到，旧文件导入
// 把它清空时没人会逐个渠道去翻），单独判危险——「改动」headers 不算，只有「清空」
// 那一行加 danger 着色（#173，PO 2026-09-29 裁决）。
const isHeaderClear = (c: string) => c.startsWith('渠道 ') && c.endsWith('的额外出站头将被清空')

// 清单按动词分两摞摆（v0.55）：后端 reconcile 的输出按实体类型交错，混排时人得
// 逐行扫动词才拼得出「哪些会没」。删除是覆盖导入里真正危险的那半，组头着警示色。
// 动词对不上号的行（后端加了新动词而这里没跟上）兜进末尾无头组，掉出清单才是事故；
// 组内单行若命中 isHeaderClear 再单独着色（组头没法覆盖到「只清空这一条危险」）。
function ImportChanges({ changes }: { changes: string[] }) {
  const added = changes.filter((c) => c.startsWith('新增'))
  const deleted = changes.filter((c) => c.startsWith('删除'))
  const rest = changes.filter((c) => !c.startsWith('新增') && !c.startsWith('删除'))
  const groups = [
    { label: '新增', items: added },
    { label: '删除', items: deleted, danger: true },
    { label: '', items: rest },
  ].filter((g) => g.items.length > 0)
  return (
    <>
      {groups.map((g) => (
        <div className="import-group" key={g.label || '其他'}>
          {g.label && (
            <p className={'import-group-label' + (g.danger ? ' import-group-danger' : '')}>
              {g.label} · {g.items.length}
            </p>
          )}
          <ul className="import-changes">
            {g.items.map((c) => (
              <li key={c} className={isHeaderClear(c) ? 'import-group-danger' : undefined}>
                {c}
              </li>
            ))}
          </ul>
        </div>
      ))}
    </>
  )
}

function ImportButton() {
  const fileRef = useRef<HTMLInputElement>(null)
  const [pending, setPending] = useState<{ name: string; text: string } | null>(null)
  const [preview, setPreview] = useState<ImportPreview | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState<string[] | null>(null)

  // 试算是异步的，而选文件可以连着来（试算途中取消、再选另一份）：迟到的响应必须
  // 认得出自己已经过期，否则确认框会摆着 B 的文件名、A 的清单，而按下去导的是 B——
  // 人照着另一份文件的账批准了全量覆盖。每次选文件递一个号，回来对不上就丢掉。
  const previewSeq = useRef(0)

  const close = () => {
    previewSeq.current++
    setPending(null)
    setPreview(null)
    setError('')
  }

  return (
    <>
      <button
        type="button"
        className="btn btn-quiet"
        title="导入一份 channels.yaml，整份覆盖当前业务配置"
        onClick={() => fileRef.current?.click()}
      >
        导入配置
      </button>
      <input
        ref={fileRef}
        type="file"
        accept=".yaml,.yml"
        hidden
        onChange={async (e) => {
          const file = e.target.files?.[0]
          // 清掉 value：不清的话「取消后再选同一个文件」不触发 onChange，按钮就哑了。
          e.target.value = ''
          if (!file) return
          // 选完立即试算：确认框里摆的必须是「这份文件对这个库会干什么」的事实，
          // 不是覆盖语义的说教。试算没回来之前确认键不渲染——没算过的账不能签。
          setError('')
          const seq = ++previewSeq.current
          const text = await file.text()
          if (seq !== previewSeq.current) return
          setPending({ name: file.name, text })
          setPreview({ state: 'loading' })
          try {
            const changes = await previewImport(text)
            if (seq === previewSeq.current) setPreview({ state: 'ok', changes })
          } catch (err) {
            if (seq === previewSeq.current) {
              setPreview({ state: 'error', message: err instanceof Error ? err.message : String(err) })
            }
          }
        }}
      />
      {pending && (
        <Dialog title="导入配置" guard scroll onClose={close}>
          <div className="dialog-note">
            {preview?.state === 'loading' && <p>正在试算 {pending.name} 会带来的变更…</p>}
            {preview?.state === 'error' && (
              <>
                <p>
                  试算 <b>{pending.name}</b> 没过，导入不会执行——同一份文件真导入也会被同一道闸打回：
                </p>
                <div className="bar bar-error bar-pre">{preview.message}</div>
              </>
            )}
            {preview?.state === 'ok' && (
              <>
                <p>
                  试算 <b>{pending.name}</b>：
                  {preview.changes.length === 0
                    ? '配置无变化——文件内容与当前配置一致。'
                    : others(preview.changes) > 0
                      ? `将新增 ${adds(preview.changes)} 项、删除 ${dels(preview.changes)} 项、改动 ${others(preview.changes)} 项。`
                      : `将新增 ${adds(preview.changes)} 项、删除 ${dels(preview.changes)} 项。`}
                </p>
                {preview.changes.length > 0 && <ImportChanges changes={preview.changes} />}
              </>
            )}
            <p className="muted">文件里没有的一律删除，有的一律按文件覆盖。</p>
            {error && <div className="bar bar-error bar-pre">{error}</div>}
            <div className="form-actions">
              <button type="button" className="btn btn-quiet" disabled={busy} onClick={close}>
                取消
              </button>
              {preview?.state === 'ok' && (
                <Confirm
                  danger
                  label={busy ? '导入中…' : '导入并覆盖'}
                  confirm="确定覆盖当前配置？"
                  onConfirm={async () => {
                    setBusy(true)
                    setError('')
                    try {
                      setDone(await importConfig(pending.text))
                      close()
                    } catch (err) {
                      setError(err instanceof Error ? err.message : String(err))
                    } finally {
                      setBusy(false)
                    }
                  }}
                />
              )}
            </div>
          </div>
        </Dialog>
      )}
      {done && (
        <Dialog title="导入完成" onClose={() => window.location.reload()}>
          <div className="dialog-note">
            <p>{done.length === 0 ? '配置无变化——文件内容与当前配置一致。' : '配置已按文件覆盖。'}</p>
            <div className="form-actions">
              <button type="button" className="btn btn-quiet" onClick={() => window.location.reload()}>
                完成
              </button>
            </div>
          </div>
        </Dialog>
      )}
    </>
  )
}

const ENCRYPTION_OPTIONS = [
  { value: 'starttls', label: 'STARTTLS', hint: '587' },
  { value: 'ssl', label: 'SSL', hint: '465' },
  { value: 'none', label: '不加密', hint: '仅内网' },
] as const

type Encryption = (typeof ENCRYPTION_OPTIONS)[number]['value']

/**
 * 登录与邮件配置（#62 决议 7）：站点外部 URL、SMTP、两家 OAuth client。改完即生效。
 *
 * secret 三样（SMTP 密码、client_secret）**从不回显**，页面只知道「设没设」。
 * 表单里的 secret 输入框留空 = 保留现值（不发这个字段），要清空得点「清除」。
 */
function AuthSettingsSection({ onDirtyChange }: { onDirtyChange: (dirty: boolean) => void }) {
  const settings = useList(() => api.get<AuthSettings>('/auth-settings'))
  if (settings.loading && settings.data === null) return null
  if (settings.data === null) return <ErrorBar message={settings.error} />
  return <AuthSettingsForm initial={settings.data} reload={settings.reload} onDirtyChange={onDirtyChange} />
}

const smtpOf = (s: AuthSettings) => ({
  host: s.smtp.host,
  port: s.smtp.port,
  encryption: (s.smtp.encryption || 'starttls') as Encryption,
  username: s.smtp.username,
  from: s.smtp.from,
})

function AuthSettingsForm({
  initial,
  reload,
  onDirtyChange,
}: {
  initial: AuthSettings
  reload: () => Promise<void>
  onDirtyChange: (dirty: boolean) => void
}) {
  const [siteURL, setSiteURL] = useState(initial.site_url)
  const [smtp, setSmtp] = useState(() => smtpOf(initial))
  // null = 没动过（不发字段，后端保留现值）；空串 = 明确清空。
  const [smtpPassword, setSmtpPassword] = useState<string | null>(null)
  const [github, setGithub] = useState({ id: initial.github.client_id, secret: null as string | null })
  const [google, setGoogle] = useState({ id: initial.google.client_id, secret: null as string | null })
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [busy, setBusy] = useState(false)
  const [testing, setTesting] = useState(false)

  // 未保存改动只喂外层 Dialog 的 guard。比的是当前 initial：保存后 reload 换上新值，
  // 表单状态正好等于它，dirty 自然归零。
  const dirty =
    siteURL !== initial.site_url ||
    JSON.stringify(smtp) !== JSON.stringify(smtpOf(initial)) ||
    smtpPassword !== null ||
    github.id !== initial.github.client_id ||
    github.secret !== null ||
    google.id !== initial.google.client_id ||
    google.secret !== null
  useEffect(() => onDirtyChange(dirty), [dirty, onDirtyChange])

  // 保存后 reload 换上后端规范化过的值（去空白、站点地址去尾斜杠），表单跟着同步，
  // 否则输入里的原样字符串与 initial 对不上，dirty 一直为真、遮罩关不掉。
  // reload 只在保存成功后发生，不会冲掉未保存的编辑。
  useEffect(() => {
    setSiteURL(initial.site_url)
    setSmtp(smtpOf(initial))
    setGithub((g) => ({ ...g, id: initial.github.client_id }))
    setGoogle((g) => ({ ...g, id: initial.google.client_id }))
  }, [initial])

  async function save(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setSaved(false)
    setError('')
    try {
      await api.put('/auth-settings', {
        site_url: siteURL,
        smtp: {
          host: smtp.host,
          port: smtp.port,
          encryption: smtp.encryption,
          username: smtp.username,
          from: smtp.from,
          ...(smtpPassword !== null ? { password: smtpPassword } : {}),
        },
        github: { client_id: github.id, ...(github.secret !== null ? { secret: github.secret } : {}) },
        google: { client_id: google.id, ...(google.secret !== null ? { secret: google.secret } : {}) },
      })
      setSaved(true)
      setSmtpPassword(null)
      setGithub((g) => ({ ...g, secret: null }))
      setGoogle((g) => ({ ...g, secret: null }))
      await reload()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <form className="form" onSubmit={save}>
        <Field
          label="站点外部 URL"
          hint="邮件里的验证/重置链接、OAuth 回调地址都从它拼；不配则注册与 OAuth 都开不了"
        >
          <input
            placeholder="https://portage.example.com"
            value={siteURL}
            onChange={(e) => setSiteURL(e.target.value)}
          />
        </Field>

        <h3 className="form-section">SMTP 发信</h3>
        <p className="muted">
          未配 SMTP 时注册入口关闭，验证信与找回密码也发不出去——那时发号走用户页的「添加用户」。
        </p>
        <div className="form-grid">
          <Field label="服务器">
            <input
              placeholder="smtp.example.com"
              value={smtp.host}
              onChange={(e) => setSmtp({ ...smtp, host: e.target.value })}
            />
          </Field>
          <Field label="端口" hint="留空按加密方式取默认">
            <input
              inputMode="numeric"
              placeholder={smtp.encryption === 'ssl' ? '465' : smtp.encryption === 'none' ? '25' : '587'}
              value={smtp.port}
              onChange={(e) => setSmtp({ ...smtp, port: e.target.value })}
            />
          </Field>
        </div>
        <Field label="加密方式">
          <Segmented
            value={smtp.encryption}
            options={[...ENCRYPTION_OPTIONS]}
            onChange={(v) => setSmtp({ ...smtp, encryption: v })}
          />
        </Field>
        <div className="form-grid">
          <Field label="用户名" hint="留空 = 匿名发信">
            <input value={smtp.username} onChange={(e) => setSmtp({ ...smtp, username: e.target.value })} />
          </Field>
          <SecretField
            label="密码"
            set={initial.smtp.password_set}
            value={smtpPassword}
            onChange={setSmtpPassword}
          />
        </div>
        <Field label="发件地址">
          <input
            placeholder="noreply@example.com"
            value={smtp.from}
            onChange={(e) => setSmtp({ ...smtp, from: e.target.value })}
          />
        </Field>

        <h3 className="form-section">GitHub 登录</h3>
        <OAuthClientFields
          provider="GitHub"
          callback={initial.callback_urls.github}
          clientID={github.id}
          onClientID={(v) => setGithub({ ...github, id: v })}
          secretSet={initial.github.secret_set}
          secret={github.secret}
          onSecret={(v) => setGithub({ ...github, secret: v })}
        />

        <h3 className="form-section">Google 登录</h3>
        {/* Google 上游硬限 HTTPS、禁裸 IP：内网 http 部署注册不了 client，这不是网关能
            绕的——只配 GitHub 或完全不配 OAuth 都是设计内的形态。 */}
        <OAuthClientFields
          provider="Google"
          callback={initial.callback_urls.google}
          clientID={google.id}
          onClientID={(v) => setGoogle({ ...google, id: v })}
          secretSet={initial.google.secret_set}
          secret={google.secret}
          onSecret={(v) => setGoogle({ ...google, secret: v })}
        />

        {saved && !error && <div className="bar bar-ok">已保存，即刻生效。</div>}
        <ErrorBar message={error} />
        <div className="form-actions">
          <button type="button" className="btn btn-quiet" onClick={() => setTesting(true)}>
            发测试邮件
          </button>
          <button className="btn btn-primary" disabled={busy}>
            {busy ? '保存中…' : '保存'}
          </button>
        </div>
      </form>
      {testing && <TestEmailDialog onClose={() => setTesting(false)} />}
    </>
  )
}

/** secret 输入框：从不回显现值，只标「已设置」。留空 = 保留，点清除 = 置空。 */
function SecretField({
  label,
  set,
  value,
  onChange,
}: {
  label: string
  set: boolean
  value: string | null
  onChange: (v: string | null) => void
}) {
  const cleared = value === ''
  return (
    <Field
      label={label}
      hint={set && !cleared ? '已设置——留空保留现值，输入即替换' : cleared ? '将清除' : '未设置'}
    >
      <div className="secret-row">
        <input
          type="password"
          autoComplete="new-password"
          placeholder={set && !cleared ? '••••••••' : ''}
          value={value ?? ''}
          onChange={(e) => onChange(e.target.value === '' ? null : e.target.value)}
        />
        {set && !cleared && (
          <button type="button" className="btn btn-quiet" onClick={() => onChange('')}>
            清除
          </button>
        )}
      </div>
    </Field>
  )
}

function OAuthClientFields({
  provider,
  callback,
  clientID,
  onClientID,
  secretSet,
  secret,
  onSecret,
}: {
  provider: string
  callback: string
  clientID: string
  onClientID: (v: string) => void
  secretSet: boolean
  secret: string | null
  onSecret: (v: string | null) => void
}) {
  return (
    <>
      <div className="form-grid">
        <Field label="Client ID">
          <input value={clientID} onChange={(e) => onClientID(e.target.value)} />
        </Field>
        <SecretField label="Client Secret" set={secretSet} value={secret} onChange={onSecret} />
      </div>
      <Field label="回调地址" hint={`在 ${provider} 注册 OAuth 应用时填这个（先配好站点外部 URL）`}>
        {callback ? <CopyCode value={callback} /> : <span className="muted">先在上面配站点外部 URL</span>}
      </Field>
    </>
  )
}

function TestEmailDialog({ onClose }: { onClose: () => void }) {
  const [to, setTo] = useState('')
  const [error, setError] = useState('')
  const [ok, setOk] = useState(false)
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    setOk(false)
    try {
      await api.post('/auth-settings/test-email', { to })
      setOk(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog title="发测试邮件" onClose={onClose}>
      <form className="form" onSubmit={submit}>
        <p className="muted">
          用当前<b>已保存</b>的 SMTP 配置发一封测试信——改了配置先保存再测。
        </p>
        <Field label="收件地址">
          <input type="email" autoFocus value={to} onChange={(e) => setTo(e.target.value)} />
        </Field>
        {ok && <div className="bar bar-ok">已发出。收到它说明 SMTP 配置可用。</div>}
        <ErrorBar message={error} />
        <div className="form-actions">
          <button type="button" className="btn btn-quiet" onClick={onClose}>
            关闭
          </button>
          <button className="btn btn-primary" disabled={busy || !to}>
            {busy ? '发送中…' : '发送'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}
