/**
 * 行内动作按钮的笔画图标（DESIGN v0.38）。与 icons/index.tsx 的**供应商标识**是两套
 * 逻辑（§6）：那边是彩色资产转灰阶，这边是 currentColor 的 1.5px 笔画，随按钮文字
 * 一起变色。统一 16 视口、圆头，不混别的粗细——同一行里两种笔画宽度立刻显脏。
 */

const S = {
  width: 14,
  height: 14,
  viewBox: '0 0 16 16',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.5,
  strokeLinecap: 'round',
  strokeLinejoin: 'round',
  'aria-hidden': true,
} as const

/** 上游设置：滑杆。齿轮是「系统设置」的形状，这里改的是这一个渠道的几个参数。 */
export function IconSliders() {
  return (
    <svg {...S}>
      <path d="M3 5h10M3 11h10" />
      <circle cx="6.2" cy="5" r="1.7" fill="var(--canvas, #f5f4ed)" />
      <circle cx="9.8" cy="11" r="1.7" fill="var(--canvas, #f5f4ed)" />
    </svg>
  )
}

export function IconEye() {
  return (
    <svg {...S}>
      <path d="M1.8 8s2.3-4 6.2-4 6.2 4 6.2 4-2.3 4-6.2 4-6.2-4-6.2-4Z" />
      <circle cx="8" cy="8" r="1.8" />
    </svg>
  )
}

export function IconEyeOff() {
  return (
    <svg {...S}>
      <path d="M1.8 8s2.3-4 6.2-4c1.3 0 2.4.4 3.4 1M14.2 8s-2.3 4-6.2 4c-1.3 0-2.4-.4-3.4-1" />
      <path d="M3 13 13 3" />
    </svg>
  )
}

export function IconCopy() {
  return (
    <svg {...S}>
      <rect x="5.5" y="5.5" width="8" height="8" rx="1.5" />
      <path d="M10.5 3.5v-.2A1.3 1.3 0 0 0 9.2 2H3.8a1.3 1.3 0 0 0-1.3 1.3v5.4a1.3 1.3 0 0 0 1.3 1.3h.2" />
    </svg>
  )
}

export function IconCheck() {
  return (
    <svg {...S}>
      <path d="m3 8.6 3.2 3.2L13 4.6" />
    </svg>
  )
}

export function IconPencil() {
  return (
    <svg {...S}>
      <path d="M11.3 2.7a1.6 1.6 0 0 1 2.3 2.3l-7.8 7.7-3.1.8.8-3.1 7.8-7.7Z" />
    </svg>
  )
}

/** 检测：脉冲线——发一次最小真实请求、看回波。 */
export function IconPulse() {
  return (
    <svg {...S}>
      <path d="M1.8 8h2.7l1.8-4.2L9 12.2 10.7 8h3.5" />
    </svg>
  )
}

/** 管理（凭证池）：钥匙。池子的逐条 CRUD 都在弹框里。 */
export function IconKey() {
  return (
    <svg {...S}>
      <circle cx="5" cy="11" r="2.8" />
      <path d="M7.2 8.8 13.4 2.6M11 5l2 2M9 7l1.6 1.6" />
    </svg>
  )
}

export function IconX() {
  return (
    <svg {...S}>
      <path d="M4 4l8 8M12 4l-8 8" />
    </svg>
  )
}

/** 批量粘贴：多行清单。 */
export function IconRows() {
  return (
    <svg {...S}>
      <path d="M3 4.5h10M3 8h10M3 11.5h6" />
    </svg>
  )
}
