import { useEffect, useRef, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { api } from '../../api'
import AuthCard from './AuthCard'

/**
 * 邮箱验证落地页：邮件链接指到 /panel/verify?token=...，加载即消费。点链接的
 * 浏览器未必是注册时那个——token 本身就是凭据，这页不要求登录。
 */
export default function Verify({ onVerified }: { onVerified: () => void }) {
  const [params] = useSearchParams()
  const token = params.get('token') ?? ''
  const [state, setState] = useState<'busy' | 'ok' | 'fail'>('busy')
  const [error, setError] = useState('')
  // StrictMode 下 effect 会跑两遍，而 token 是一次性的：第二遍消费必然 400，
  // 把刚成功的页面改写成「链接无效」。用 ref 锁住只发一次。
  const fired = useRef(false)

  useEffect(() => {
    if (fired.current || !token) return
    fired.current = true
    api.post('/verify-email', { token }).then(
      () => {
        setState('ok')
        onVerified()
      },
      (err: unknown) => {
        setState('fail')
        setError(err instanceof Error ? err.message : String(err))
      },
    )
  }, [token, onVerified])

  return (
    <AuthCard title="邮箱验证">
      {!token ? (
        <div className="bar bar-error">链接不完整：请从邮件里完整复制链接打开。</div>
      ) : state === 'busy' ? (
        <p className="muted login-note">验证中…</p>
      ) : state === 'ok' ? (
        <>
          <div className="bar bar-ok">邮箱验证完成。</div>
          <Link className="btn btn-primary" to="/">
            进入 Portage
          </Link>
        </>
      ) : (
        <>
          <div className="bar bar-error">{error}</div>
          <Link className="btn btn-quiet" to="/">
            回登录页
          </Link>
        </>
      )}
    </AuthCard>
  )
}
