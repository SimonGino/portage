import { useState } from 'react'
import { api } from '../api'
import { Dialog, ErrorBar, Field } from '../ui'

export default function ChangePassword({
  onClose,
  onChanged,
}: {
  onClose: () => void
  onChanged: () => void
}) {
  const [oldPw, setOldPw] = useState('')
  const [newPw, setNewPw] = useState('')
  const [confirmPw, setConfirmPw] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      await api.post('/password', { old_password: oldPw, new_password: newPw })
      // 后端改完密码会把**全部**会话吊销，包括发起这次修改的这一个。
      // 所以这里不是关掉框就完了，得让 App 重新问一次 /session，
      // 界面会自己回到登录页。
      onClose()
      onChanged()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog title="修改密码" onClose={onClose}>
      <form className="form" onSubmit={submit}>
        <div className="bar bar-warn">改完所有登录状态一起失效，需要用新密码重新登录。</div>
        <Field label="原密码">
          <input
            type="password"
            autoFocus
            autoComplete="current-password"
            value={oldPw}
            onChange={(e) => setOldPw(e.target.value)}
          />
        </Field>
        <Field label="新密码" hint="至少 8 位">
          <input
            type="password"
            autoComplete="new-password"
            value={newPw}
            onChange={(e) => setNewPw(e.target.value)}
          />
        </Field>
        <Field
          label="确认新密码"
          hint={confirmPw && confirmPw !== newPw ? '两次输入不一致' : undefined}
        >
          <input
            type="password"
            autoComplete="new-password"
            value={confirmPw}
            onChange={(e) => setConfirmPw(e.target.value)}
          />
        </Field>
        <ErrorBar message={error} />
        <div className="form-actions">
          <button type="button" className="btn btn-quiet" onClick={onClose}>
            取消
          </button>
          <button
            className="btn btn-primary"
            disabled={busy || !oldPw || newPw.length < 8 || confirmPw !== newPw}
          >
            {busy ? '提交中…' : '确认修改'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}
