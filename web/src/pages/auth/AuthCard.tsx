import type { ReactNode } from 'react'
import { PortageMark } from '../../brand'

/**
 * 注册/验证/找回那几页共用的门页壳——与登录页同一张纸：这些页面跟登录页是同一段
 * 旅程（门外），视觉上不该换一间房。骨架同登录页（v0.56）：标记方块 + 大标题，
 * 标题直接说这一页是哪扇门，不再重复品牌字。
 */
export default function AuthCard({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="login-wrap">
      <div className="login">
        <div className="login-mark">
          <PortageMark size={26} />
        </div>
        <h1>{title}</h1>
        {children}
      </div>
    </div>
  )
}
