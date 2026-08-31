import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api } from '../api'
import type { Channel, ModelListResult } from '../api'
import { Empty, ErrorBar, useList } from '../ui'
import { ChannelIcon } from '../icons'
import { ChannelDetail } from './channels/detail'
import { ChannelForm } from './channels/form'

/**
 * 模型页（路由仍是 /channels）：页内主从两栏——左列渠道清单（sticky），右栏是
 * 选中渠道的纳管模型（口径层 v0.75；v0.54 左栏退役后清单从壳搬回页内）。
 * 新建时右栏整个是表单，没有模型列表。
 */
export default function Channels() {
  const { id } = useParams()
  const nav = useNavigate()
  const { data, error, loading, reload, setError } = useList(() =>
    api.get<Channel[] | null>('/channels'),
  )
  const [query, setQuery] = useState('')
  const [fetched, setFetched] = useState<Record<number, ModelListResult[]>>({})

  useEffect(() => {
    window.scrollTo(0, 0)
  }, [id])

  async function mutate(fn: () => Promise<unknown>): Promise<boolean> {
    try {
      await fn()
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      return false
    }
    await reload()
    return true
  }

  if (loading && data === null) return <div className="boot">加载中…</div>
  // 停用的沉底，其余按 id（即接入先后）排：左栏是天天扫的清单，停用项混在中间
  // 每次都要跳读。组内都按 id，稳定不随改名跳位。
  const channels = [...(data ?? [])].sort(
    (a, b) => Number(a.disabled) - Number(b.disabled) || a.id - b.id,
  )
  const creating = id === 'new'
  const current = creating ? null : channels.find((c) => String(c.id) === id) ?? channels[0] ?? null
  const q = query.trim().toLowerCase()
  // 搜索匹配任意一个协议的地址（口径层 v0.96）：同一家上游可能只有某一协议挂在
  // 特征域名下，只搜第一份会漏。
  const visible = q
    ? channels.filter(
        (c) =>
          c.name.toLowerCase().includes(q) ||
          Object.values(c.base_url ?? {}).some((u) => (u ?? '').toLowerCase().includes(q)),
      )
    : channels

  return (
    <div className="split">
      <aside className="master" aria-label="渠道">
        <div className="master-label">渠道</div>
        <input
          className="master-search"
          placeholder="搜渠道…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <div className="master-list">
          {visible.length === 0 ? (
            <div className="muted master-empty">
              {channels.length === 0 ? '还没有渠道。' : `没有匹配「${query}」的渠道。`}
            </div>
          ) : (
            visible.map((ch) => (
              <button
                key={ch.id}
                type="button"
                className={
                  (current?.id === ch.id && !creating ? 'is-on' : '') + (ch.disabled ? ' is-off' : '')
                }
                onClick={() => nav(`/channels/${ch.id}`)}
              >
                <span className="nm">
                  <ChannelIcon channel={ch} size={18} />
                  {ch.name}
                </span>
                <span className="mark">
                  {ch.disabled
                    ? '停用'
                    : ch.enabled_keys === 0
                      ? '缺凭证'
                      : (ch.protocols ?? []).length === 0
                        ? '无协议'
                        : ''}
                </span>
              </button>
            ))
          )}
        </div>
        <button
          type="button"
          className={'master-new' + (creating ? ' is-on' : '')}
          onClick={() => nav('/channels/new')}
        >
          新建渠道
        </button>
      </aside>

      <div className="detail-col">
        <ErrorBar message={error} />
        {creating ? (
          <>
            <header className="page-head">
              <h1>新建渠道</h1>
            </header>
            <ChannelForm
              key="new"
              channel={null}
              onCancel={() => nav(current ? `/channels/${current.id}` : '/channels')}
              onSaved={(newID) => {
                // 保存后什么都不跑（口径层 v0.96 ①）：真实请求的钱只在人手点检测时花。
                void reload()
                nav(`/channels/${newID}`, { replace: true })
              }}
            />
          </>
        ) : current ? (
          <ChannelDetail
            key={current.id}
            ch={current}
            fetched={fetched[current.id]}
            onFetchModelsDone={(results) =>
              setFetched((p) => ({ ...p, [current.id]: results }))
            }
            onCredentialsChanged={() => void reload()}
            onDelete={() => {
              void mutate(() => api.del(`/channels/${current.id}`)).then((ok) => {
                if (ok) nav('/channels', { replace: true })
              })
            }}
            onSaved={() => void reload()}
            mutate={mutate}
          />
        ) : (
          <>
            <header className="page-head">
              <h1>模型</h1>
            </header>
            <Empty>还没有渠道。左边「新建渠道」接一家，再拉模型。</Empty>
          </>
        )}
      </div>
    </div>
  )
}
