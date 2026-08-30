import type { ReactNode } from 'react'
import { PortageMark } from '../../brand'

/**
 * 注册/验证/找回那几页共用的卡片壳——与登录页同一张纸：这些页面跟登录页是同一段
 * 旅程（门外），视觉上不该换一间房。
 */
export default function AuthCard({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="login-wrap">
      <div className="login">
        <h1>
          <PortageMark size={20} />
          Portage
        </h1>
        <p className="login-sub">{title}</p>
        {children}
      </div>
    </div>
  )
}
