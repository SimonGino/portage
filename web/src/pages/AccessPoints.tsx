import { useState } from 'react'
import { api } from '../api'
import type { AccessPoint, Channel } from '../api'
import { Card, Confirm, CopyCode, Dialog, Empty, ErrorBar, Field, StateText, useList } from '../ui'
import { Picker } from '../fields'
import type { Option } from '../fields'
import { Avatar, ChannelIcon, ModelIcon, vendorForModel } from '../icons'

export default function AccessPoints() {
  const aps = useList(() => api.get<AccessPoint[] | null>('/access-points'))
  // 渠道也要拉：新建接入点时得从「渠道 → 纳管模型」里挑一个当候选，
  // 而接入点自己的接口只回候选的 id，凑不出可选项列表。
  const channels = useList(() => api.get<Channel[] | null>('/channels'))
  const [editing, setEditing] = useState<AccessPoint | 'new' | null>(null)

  async function mutate(fn: () => Promise<unknown>) {
    try {
      await fn()
      aps.setError('')
    } catch (e) {
      aps.setError(e instanceof Error ? e.message : String(e))
      return
    }
    await aps.reload()
  }

  if (aps.loading && aps.data === null) return <div className="boot">加载中…</div>
  const list = aps.data ?? []
  const chList = channels.data ?? []

  return (
    <>
      <ErrorBar message={aps.error || channels.error} />
      <Card
        title="接入点"
        action={
          <button className="btn btn-primary" onClick={() => setEditing('new')} disabled={chList.length === 0}>
            新建接入点
          </button>
        }
      >
        {list.length === 0 ? (
          <Empty>
            {chList.length === 0
              ? '先去「渠道」建一个上游并给它加纳管模型，这里才有东西可选。'
              : '还没有接入点。建一个，客户端就能用这个名字调过来了。'}
          </Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>接入点</th>
                <th>候选</th>
                <th>状态</th>
                <th className="col-actions" />
              </tr>
            </thead>
            <tbody>
              {list.map((ap) => {
                const cands = ap.candidates ?? []
                return (
                  <tr key={ap.id} className={ap.disabled ? 'is-off' : ''}>
                    <td>
                      <span className="icon-row">
                        <ModelIcon model={ap.model} size={18} />
                        {ap.disabled ? <code>{ap.model}</code> : <CopyCode value={ap.model} />}
                      </span>
                    </td>
                    <td>
                      {cands.length === 0 ? (
                        // 零候选的接入点一定路由不到，而且会把启动闸卡住。
                        <span className="tag tag-warn">没有候选</span>
                      ) : (
                        cands.map((c) => (
                          <div key={c.id} className="cand">
                            <ModelIcon model={c.upstream_model} size={16} />
                            <span className="muted">{c.channel_name} /</span>
                            <code>{c.upstream_model}</code>
                            <span className="muted"> 权重 {c.weight}</span>
                          </div>
                        ))
                      )}
                    </td>
                    {/* 只读：接入点的启用状态在编辑表单里改。跟网关 key 那页的开关
                        共用同一套写法，同一件事不该在两页长得不一样。 */}
                    <td>
                      <StateText on={!ap.disabled} />
                    </td>
                    {/* 按钮包一层 div：直接把 display:flex 挂在 td 上，这一格就脱离了
                        表格的列模型，渲染出来会跑到卡片外面去。 */}
                    <td className="col-actions">
                      <div className="row-actions">
                        <button className="btn btn-quiet" onClick={() => setEditing(ap)}>
                          编辑
                        </button>
                        <Confirm ghost onConfirm={() => void mutate(() => api.del(`/access-points/${ap.id}`))} />
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </Card>

      {editing && (
        <AccessPointForm
          ap={editing === 'new' ? null : editing}
          channels={chList}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void aps.reload()
          }}
        />
      )}
    </>
  )
}

function AccessPointForm({
  ap,
  channels,
  onClose,
  onSaved,
}: {
  ap: AccessPoint | null
  channels: Channel[]
  onClose: () => void
  onSaved: () => void
}) {
  const first = ap?.candidates?.[0]
  const [model, setModel] = useState(ap?.model ?? '')
  const [cmID, setCmID] = useState<number>(first?.channel_model_id ?? 0)
  const [weight, setWeight] = useState(first?.weight ?? 100)
  const [disabled, setDisabled] = useState(ap?.disabled ?? false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  // 只列启用的渠道与启用的纳管模型：停用的选了也路由不到，摆在下拉里只会让人
  // 配出一个「保存成功但怎么都不通」的接入点。
  //
  // 按渠道分组，每行带供应商图标；base_url 进 keywords 但不显示——同一家上游在
  // 不同中转下会重名，搜 `dashscope` 能把它们分出来。
  const options: Option<number>[] = channels
    .filter((ch) => !ch.disabled)
    .flatMap((ch) =>
      (ch.models ?? [])
        .filter((m) => !m.disabled)
        .map((m) => ({
          value: m.id,
          label: m.upstream_model,
          group: ch.name,
          keywords: ch.base_url,
          icon: <ModelIcon model={m.upstream_model} size={18} />,
        })),
    )

  const pickedChannel = channels.find((ch) => (ch.models ?? []).some((m) => m.id === cmID))
  const pickedModel = pickedChannel?.models?.find((m) => m.id === cmID)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      const body = { model, channel_model_id: cmID, weight, disabled }
      if (ap) await api.put(`/access-points/${ap.id}`, body)
      else await api.post('/access-points', body)
      onSaved()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog title={ap ? `编辑接入点：${ap.model}` : '新建接入点'} onClose={onClose}>
      <form className="form" onSubmit={submit}>
        <div className="form-preview">
          <Avatar vendor={vendorForModel(model)} fallback={model || '?'} size={40} />
          <div>
            <div className="form-preview-name">{model || '未命名接入点'}</div>
            <div className="muted">
              {pickedModel && pickedChannel ? (
                <span className="icon-row">
                  → <ChannelIcon channel={pickedChannel} size={14} /> {pickedChannel.name} /{' '}
                  <code>{pickedModel.upstream_model}</code>
                </span>
              ) : (
                '还没选候选'
              )}
            </div>
          </div>
        </div>

        <Field label="接入点名" hint="客户端请求里的 model 字段填的就是这个">
          <input autoFocus value={model} onChange={(e) => setModel(e.target.value)} />
        </Field>
        <Field label="候选（渠道 / 纳管模型）" hint="M0~M2 的临时闸：每个接入点只能有一个候选">
          <Picker
            value={cmID === 0 ? null : cmID}
            options={options}
            onChange={setCmID}
            placeholder="选一个纳管模型…"
          />
        </Field>
        <Field label="权重" hint="只有一个候选时不影响路由，多候选放开后才有用">
          <input
            type="number"
            min={1}
            value={weight}
            onChange={(e) => setWeight(Number(e.target.value))}
          />
        </Field>
        <label className="check">
          <input type="checkbox" checked={disabled} onChange={(e) => setDisabled(e.target.checked)} />
          停用这个接入点
        </label>
        <ErrorBar message={error} />
        <div className="form-actions">
          <button type="button" className="btn btn-quiet" onClick={onClose}>
            取消
          </button>
          <button className="btn btn-primary" disabled={busy || !model.trim() || cmID === 0}>
            {busy ? '保存中…' : '保存'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}
