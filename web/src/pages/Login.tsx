import { useState } from 'react'
import { api } from '../api'
import { PortageMark } from '../brand'
import { ErrorBar } from '../ui'

export default function Login({
  passwordSet,
  onLoggedIn,
}: {
  passwordSet: boolean
  onLoggedIn: () => void
}) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      await api.post('/login', { password })
      onLoggedIn()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login-wrap">
      <form className="login" onSubmit={submit}>
        <h1>
          <PortageMark size={20} />
          Portage
        </h1>
        {/* 「还没设密码」跟「密码错了」要分开说：前者的补救是去改配置重启，
            后者是再输一次。含糊成一句「登录失败」会让人对着配置文件反复重试。 */}
        {!passwordSet ? (
          <div className="bar bar-warn">
            尚未设置管理密码。在 config.yaml 里填 <code>admin_password</code>，
            或给容器设环境变量 <code>PORTAGE_ADMIN_PASSWORD</code>，然后重启。
          </div>
        ) : (
          <>
            <input
              type="password"
              autoFocus
              autoComplete="current-password"
              placeholder="管理密码"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
            <ErrorBar message={error} />
            <button className="btn btn-primary" disabled={busy || !password}>
              {busy ? '登录中…' : '登录'}
            </button>
          </>
        )}
      </form>
    </div>
  )
}
