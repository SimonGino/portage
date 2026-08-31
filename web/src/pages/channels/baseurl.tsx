import { useState } from 'react'
import {
  PROTOCOL_LABEL,
  PROTOCOL_ORDER,
  PROTOCOL_PATH,
  PROTOCOL_SHORT,
  PROTOCOL_SOON,
  declaredProtocols,
  joinBaseURLs,
} from '../../api'
import type { BaseURLDraft, Protocol } from '../../api'
import { Field } from '../../ui'
import { IconPencil } from '../../icons/acts'
import { joinURL } from './form'

/**
 * BaseURLFields 是渠道「API 地址 + 协议声明」的共用控件（DESIGN v0.46，PO
 * 2026-08-30 裁定方案 A）：一份共用前缀 + 协议 chips + 个别协议的「单独填」覆盖。
 * 绝大多数上游所有协议共用一个前缀——一份输入、勾协议即声明，不再把同一个地址
 * 抄进每协议一行（v0.96 ② 时代 placeholder 里那句「共用前缀就几行填同一个值」
 * 正是它要消掉的苦役）。
 *
 * 新建表单与编辑态「API 地址」区块都渲染它：两处各写一份编辑态会把后保存的悄悄
 * 盖回去（v0.96 ② 立的规矩），所以收口成一个组件。持久化节奏留给调用方——打字只
 * 回 onChange；离散动作（勾协议、恢复共用、覆盖地址失焦/回车）与共用前缀失焦/
 * 回车追加一拍 onCommit：编辑态在那一拍落库（沿用 v0.35「失焦即存」），新建态
 * 不给 onCommit、攒到「创建」一次交。
 *
 * 至少一个协议的闸（渠道必须 ≥1 协议，服务端同闸）在组件里落成**不给口**：
 * 会把声明集清空的那下勾/恢复根本点不动，而不是点了再报错。
 */
export function BaseURLFields({
  draft,
  onChange,
  onCommit,
}: {
  draft: BaseURLDraft
  onChange: (next: BaseURLDraft) => void
  /** 离散动作与失焦时追加一拍；编辑态在此落库，新建态不给。 */
  onCommit?: (next: BaseURLDraft) => void
}) {
  const urls = joinBaseURLs(draft)
  const declared = declaredProtocols(urls)
  // 正在原地展开成输入框的那条预览行（值级覆盖的编辑态）。UI 状态，不进 draft。
  const [editing, setEditing] = useState<Protocol | null>(null)
  const [editValue, setEditValue] = useState('')

  function emit(next: BaseURLDraft, commit: boolean) {
    onChange(next)
    if (commit) onCommit?.(next)
  }

  function toggleChip(p: Protocol) {
    if (draft.chips.includes(p)) {
      const next = { ...draft, chips: draft.chips.filter((x) => x !== p) }
      // 勾掉后声明集若清空则点不动：渠道至少要声明一个协议。
      if (declaredProtocols(joinBaseURLs(next)).length === 0) return
      // 覆盖随勾一起摘：勾都摘了，单独填的值没有挂处。
      const overrides = { ...next.overrides }
      delete overrides[p]
      emit({ ...next, overrides }, true)
    } else {
      emit({ ...draft, chips: [...draft.chips, p] }, true)
    }
  }

  function startOverride(p: Protocol) {
    setEditing(p)
    setEditValue(urls[p] ?? '')
  }

  /** 覆盖地址收场：与共用前缀同值（或清空）即回到共用，其余照存。 */
  function commitOverride(p: Protocol) {
    setEditing(null)
    const overrides = { ...draft.overrides }
    if (editValue.trim() === '' || editValue.trim() === draft.shared.trim()) {
      if (overrides[p] === undefined) return
      delete overrides[p]
    } else {
      overrides[p] = editValue
    }
    emit({ ...draft, overrides }, true)
  }

  function restoreShared(p: Protocol) {
    // 共用前缀还空着时恢复 = 把这个地址清成未声明；它是最后一份时会清空声明集，
    // 照「不给口」的闸禁掉。
    if (draft.shared.trim() === '') return
    const overrides = { ...draft.overrides }
    delete overrides[p]
    emit({ ...draft, overrides }, true)
  }

  return (
    <>
      <Field
        label="API 地址"
        hint="所有勾中协议共用的出站前缀（协议子路径之前的那段，网关自己接子路径）；个别协议要不同前缀时在下面那几行单独填"
      >
        <input
          value={draft.shared}
          onChange={(e) => onChange({ ...draft, shared: e.target.value })}
          onBlur={() => onCommit?.(draft)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              onCommit?.(draft)
            }
          }}
          placeholder="https://…"
        />
      </Field>
      <div className="baseurl-chips">
        <span className="muted">声明协议</span>
        <div className="baseurl-chip-row">
          {PROTOCOL_ORDER.map((p) => {
            const on = draft.chips.includes(p)
            return (
              <button
                key={p}
                type="button"
                className={'chip-toggle' + (on ? ' is-on' : '')}
                title={PROTOCOL_LABEL[p] + ' · ' + PROTOCOL_PATH[p]}
                onClick={() => toggleChip(p)}
              >
                {PROTOCOL_SHORT[p] ?? p}
              </button>
            )
          })}
          {/* 路线图里还有、网关还说不了的协议（口径层 v0.36）：摆出来回答
              「这里为什么没有 Gemini」，不可选——后端不认这个取值。 */}
          {PROTOCOL_SOON.map((s) => (
            <button key={s.value} type="button" className="chip-toggle" disabled title={s.hint}>
              {s.label}
            </button>
          ))}
        </div>
        {draft.chips.length > 0 && declared.length === 0 && (
          <span className="muted">地址还空着——填了才真声明</span>
        )}
      </div>
      {/* 已声明协议各一行预览（§7：能拼出来的就摆出来，/v1/v1 的坑靠它现形）。
          与共用前缀不同值的行带着「单独填」标记，勾着协议但没值的行不出现——
          没声明就没有可预览的地址。 */}
      {declared.map((p) => {
        const v = urls[p] ?? ''
        const overridden = (draft.overrides[p] ?? '').trim() !== ''
        const lastOne = declared.length === 1
        return (
          <div key={p} className="baseurl-view-row">
            <span className="baseurl-proto" title={PROTOCOL_LABEL[p] + ' · ' + PROTOCOL_PATH[p]}>
              {PROTOCOL_SHORT[p] ?? p}
            </span>
            {editing === p ? (
              <div className="baseurl-row">
                <input
                  className="baseurl-input"
                  value={editValue}
                  autoFocus
                  onChange={(e) => setEditValue(e.target.value)}
                  onBlur={() => commitOverride(p)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault()
                      commitOverride(p)
                    }
                    if (e.key === 'Escape') {
                      e.preventDefault()
                      setEditing(null)
                    }
                  }}
                  placeholder="留空 = 回到共用前缀"
                />
                <span className="muted baseurl-state">回车保存</span>
              </div>
            ) : (
              <>
                <code
                  className="baseurl-view-url"
                  title="网关实际会请求的完整地址：你填的前缀 + 协议固定子路径"
                >
                  {joinURL(v, PROTOCOL_PATH[p] ?? '')}
                </code>
                {overridden && <span className="muted baseurl-state">单独填</span>}
                <span className="baseurl-line-acts">
                  <button
                    type="button"
                    className="act-icon"
                    title={overridden ? '改这个协议自己的前缀' : '给这个协议单独填一个前缀'}
                    onClick={() => startOverride(p)}
                  >
                    <IconPencil />
                  </button>
                  {overridden && (
                    <button
                      type="button"
                      className="chip-toggle"
                      disabled={lastOne && draft.shared.trim() === ''}
                      title="恢复共用前缀"
                      onClick={() => restoreShared(p)}
                    >
                      恢复共用
                    </button>
                  )}
                </span>
              </>
            )}
          </div>
        )
      })}
    </>
  )
}
