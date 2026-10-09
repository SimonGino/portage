import { useEffect, useState } from 'react'
import { api, ApiError } from '../../api'
import type { Credential, CredentialType } from '../../api'
import { Dialog, ErrorBar, Field } from '../../ui'
import { useChannel } from './useChannel'

// 授权页地址按「渠道 + replace 归属」记在 sessionStorage：10 分钟 TTL 内那次登录还活着时（弹层
// 关掉又开，start 会吃 409），上次的地址就是那次登录的地址——找回它就能续上，
// 不至于干等 TTL 过期。键带上 replaceCred 的 id：「重新登录 A」的缓存不会被
// 「新增一把」的弹层错拿——服务器按它记住的那行替换/追加，拿错了地址会把
// A 行悄悄换掉（Codex/CodeRabbit 同点）。完成时清掉；过期自愈：complete 报错
// 后关弹层再开，start 就重新起了一轮、新地址覆盖旧缓存。
function loginURLKey(channelID: number, replaceID: number) {
  return `portage:login-url:${channelID}:${replaceID}`
}

function cacheLoginURL(key: string, url: string) {
  try {
    sessionStorage.setItem(key, url)
  } catch {
    // 隐私模式等存不进就算了——只是少续上一手，不致命。
  }
}

function cachedLoginURL(key: string) {
  try {
    return sessionStorage.getItem(key) ?? ''
  } catch {
    return ''
  }
}

function dropCachedLoginURL(key: string) {
  try {
    sessionStorage.removeItem(key)
  } catch {
    // 同上。
  }
}

// 在途的 start 按「渠道 + replace 归属」只发一次：StrictMode（开发构建）把 effect
// 放两遍，第二遍复用第一遍的在途请求——不发第二个 start，就不会吃 409，也不依赖
// 两个响应谁先到（Codex：靠响应次序续缓存有竞态）。请求落地（无论成败）就出表，
// 之后的新开弹层走正常的新 start；密了不到的场合（关闭立即重开）也顺带由它去重。
const startsInFlight = new Map<string, Promise<string>>()

function startLoginOnce(channelID: number, replaceCred: Credential | null): Promise<string> {
  const key = loginURLKey(channelID, replaceCred?.id ?? 0)
  let p = startsInFlight.get(key)
  if (!p) {
    p = api
      .post<{ url: string }>(`/channels/${channelID}/login/start`, replaceCred ? { credential_id: replaceCred.id } : {})
      .then((r) => r.url)
      .finally(() => {
        startsInFlight.delete(key)
      })
    startsInFlight.set(key, p)
  }
  return p
}

/**
 * LoginDialog 是订阅渠道的登录弹层（#212，DESIGN v0.77：弹框档）。按渠道的
 * credential_type 切形态——ChatGPT = 打开授权页 + 粘贴回调；Copilot 设备码是同一
 * 弹层的第二形态（user_code 大字 + interval 轮询），随其实现票启用，这里只立
 * 结构。新增一把的形态里另有一个「粘贴凭证 JSON」入口（口径层 §2.2 v1.52）：换机迁移
 * 把旧机器导出的凭证整包贴进来，走既有批量凭证入口、服务端校验后落库——只追加，
 * 重新登录的形态不露它（导入不会替换原行，见 form-actions 处注释）。
 *
 * 开弹层即起一次登录（POST login/start）；「重新登录」（reauth_required 行尾那颗
 * 按钮）带 credential_id 进来——成功后原行替换、名字不变，归因不断。
 *
 * 参数是 channelID + credentialType 而不是整个 Channel：新建表单里「登录」是先建
 * 渠道再开弹层（#216），那一刻手里只有刚建出的 id；拼一个假渠道对象不如只要
 * 真用得上的两样。
 */
export function LoginDialog({
  channelID,
  credentialType,
  replaceCred,
  onClose,
}: {
  channelID: number
  credentialType: CredentialType
  /** 「重新登录」要替换的那一行；null = 新增一把。 */
  replaceCred: Credential | null
  onClose: () => void
}) {
  const { mutate } = useChannel(channelID)
  const cacheKey = loginURLKey(channelID, replaceCred?.id ?? 0)
  const [url, setUrl] = useState('')
  const [callback, setCallback] = useState('')
  const [mode, setMode] = useState<'login' | 'paste'>('login')
  const [pasted, setPasted] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  // 开弹层即起登录：state / nonce / PKCE verifier 都由服务端生成，这里只拿授权页
  // 地址。start 失败时，只有 409（这次 start 撞上同渠道活着的登录——最典型：
  // 弹层关了又开）才从缓存找回上次的地址续上；其他错误（pending 已死、参数不对）
  // 照实摆出来——跟着死地址走到底只会收获一个说不清的报错（CodeRabbit 同点）。
  useEffect(() => {
    let alive = true
    startLoginOnce(channelID, replaceCred)
      .then((url) => {
        // 缓存不等 alive：待完成登录是服务端事实，不属于这个弹层——StrictMode 的
        // 第二遍 effect 靠它拿回同一份地址。
        cacheLoginURL(cacheKey, url)
        if (alive) {
          setUrl(url)
        }
      })
      .catch((e) => {
        if (alive) {
          if (e instanceof ApiError && e.status === 409) {
            const cached = cachedLoginURL(cacheKey)
            if (cached) {
              setUrl(cached)
              return
            }
          }
          setError(e instanceof Error ? e.message : String(e))
        }
      })
    return () => {
      alive = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- replaceCred 是行对象，身份就是行 id
  }, [channelID, replaceCred?.id])

  /** 完成一次登录：粘贴回调整条 URL 交给服务端换 token 落库；错误留在框里，登录不烧。 */
  async function finish() {
    setBusy(true)
    setError('')
    try {
      await api.post(`/channels/${channelID}/login/complete`, { callback_url: callback })
      dropCachedLoginURL(cacheKey)
      await mutate(() => Promise.resolve())
      onClose()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  /** 粘贴凭证 JSON（换机迁移）：走既有批量凭证入口，坏 JSON 的拒因原文回在框里。 */
  async function importJSON() {
    setBusy(true)
    setError('')
    try {
      await api.post(`/channels/${channelID}/credentials`, { credential: pasted })
      await mutate(() => Promise.resolve())
      onClose()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog title={replaceCred ? '重新登录' : '登录账号'} onClose={onClose}>
      {credentialType === 'copilot_account' ? (
        // 第二形态的结构位：Copilot 设备码（user_code 大字 + github.com/login/device
        // 链接 + interval 轮询）随其实现票启用——渠道类型那道闸现在根本建不出这种
        // 渠道，这个分支先立好「弹层按 credential_type 切形态」的骨架。
        <p className="muted">GitHub Copilot 的设备码登录随 Copilot 实现票启用。</p>
      ) : mode === 'paste' ? (
        <form
          className="form"
          onSubmit={(e) => {
            e.preventDefault()
            if (pasted.trim()) void importJSON()
          }}
        >
          <ErrorBar message={error} />
          <Field
            label="粘贴凭证 JSON"
            hint="换机迁移用：把旧机器上导出的凭证整包 JSON 贴进来，校验后落库"
          >
            <textarea rows={4} value={pasted} onChange={(e) => setPasted(e.target.value)} />
          </Field>
          <div className="form-actions">
            <button type="button" className="btn btn-quiet" onClick={() => setMode('login')}>
              回到登录
            </button>
            <button className="btn btn-primary" disabled={!pasted.trim() || busy}>
              导入
            </button>
          </div>
        </form>
      ) : (
        <form
          className="form"
          onSubmit={(e) => {
            e.preventDefault()
            if (callback.trim()) void finish()
          }}
        >
          <ErrorBar message={error} />
          <button
            type="button"
            className="btn btn-primary"
            disabled={!url}
            onClick={() => url && window.open(url, '_blank', 'noopener')}
          >
            打开授权页
          </button>
          <p className="muted">
            登录后浏览器会停在一个打不开的 127.0.0.1 页——那是正常的，回调本来就落
            在本机。把地址栏整条复制回来贴进下面的框里。
          </p>
          <Field label="回调地址">
            <textarea
              rows={2}
              placeholder="http://127.0.0.1:1455/auth/callback?code=…"
              value={callback}
              onChange={(e) => setCallback(e.target.value)}
            />
          </Field>
          <div className="form-actions">
            {/* 粘贴导入只属于「新增一把」的形态（Codex 评审）：它走的是只追加的批量
                入口，在重新登录的弹层里露出来的话，导入会把 reauth 的那行留在原地、
                另建一行不同名的凭证——与「成功后原行替换、名字不变」相抵。 */}
            {!replaceCred && (
              <button type="button" className="btn btn-quiet" onClick={() => setMode('paste')}>
                粘贴凭证 JSON
              </button>
            )}
            <button className="btn btn-primary" disabled={!callback.trim() || busy}>
              {replaceCred ? '完成重新登录' : '完成登录'}
            </button>
          </div>
        </form>
      )}
    </Dialog>
  )
}
