import { useEffect, useState } from 'react'
import { api } from '../../api'
import { ErrorBar } from '../../ui'
import AuthCard from './AuthCard'

/**
 * 去验证页（#62 决议 2）：未验证可登录但功能全锁，登进来看到的就是这一页。
 * 出口只有三个——重发验证信（60s 冷却）、「我已验证」刷新会话、退出。
 */
export default function VerifyGate({
  email,
  onRefresh,
  onLogout,
}: {
  email: string
  onRefresh: () => void
  onLogout: () => void
}) {
  const [error, setError] = useState('')
  const [sent, setSent] = useState(false)
  // 冷却倒计时只是页面侧的礼貌钟：真正的闸在后端（429 + Retry-After）。
  const [wait, setWait] = useState(0)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (wait <= 0) return
    const t = setTimeout(() => setWait((w) => w - 1), 1000)
    return () => clearTimeout(t)
  }, [wait])

  async function resend() {
    setBusy(true)
    setError('')
    try {
      await api.post('/verify-email/resend')
      setSent(true)
      setWait(60)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setWait(60)
    } finally {
      setBusy(false)
    }
  }

  return (
    <AuthCard title="验证邮箱">
      <p className="login-note">
        一封验证邮件已发往 <b>{email}</b>，点开里面的链接完成验证（24 小时内有效）。
        验证之前其他功能都锁着。
      </p>
      {sent && !error && <div className="bar bar-ok">已重发，注意查收（也翻翻垃圾箱）。</div>}
      <ErrorBar message={error} />
      <button className="btn btn-primary" disabled={busy || wait > 0} onClick={() => void resend()}>
        {wait > 0 ? `重发验证邮件（${wait}s）` : busy ? '发送中…' : '重发验证邮件'}
      </button>
      <div className="login-links">
        <a
          href="/admin/"
          onClick={(e) => {
            e.preventDefault()
            onRefresh()
          }}
        >
          我已验证，刷新
        </a>
        <a
          href="/admin/"
          onClick={(e) => {
            e.preventDefault()
            void api.post('/logout').then(onLogout, onLogout)
          }}
        >
          退出登录
        </a>
      </div>
    </AuthCard>
  )
}
