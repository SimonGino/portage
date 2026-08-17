import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api } from '../api'
import type { Channel, ChannelProbe, ModelListResult } from '../api'
import { Empty, ErrorBar, useList } from '../ui'
import { ChannelIcon } from '../icons'
import { ChannelDetail } from './channels/detail'
import { ChannelForm } from './channels/form'
import { RailMid } from '../rail'

/**
 * 模型页（路由仍是 /channels）：渠道列表 portal 进左栏中段，主画布是选中渠道的
 * 纳管模型（口径层 v0.75）。新建时主区整页是表单，没有模型列表。
 */
export default function Channels() {
  const { id } = useParams()
  const nav = useNavigate()
  const { data, error, loading, reload, setError } = useList(() =>
    api.get<Channel[] | null>('/channels'),
  )
  const [query, setQuery] = useState('')
  const [probes, setProbes] = useState<Record<number, ChannelProbe | 'running'>>({})
  const [fetched, setFetched] = useState<Record<number, ModelListResult[]>>({})

  useEffect(() => {
    document.querySelector('.main')?.scrollTo(0, 0)
  }, [id])

  async function probe(cid: number, withModels: boolean) {
    setProbes((p) => ({ ...p, [cid]: 'running' }))
    try {
      const r = await api.post<ChannelProbe>(
        `/channels/${cid}/probe${withModels ? '?models=1' : ''}`,
      )
      setProbes((p) => ({ ...p, [cid]: r }))
    } catch {
      setProbes((p) => {
        const next = { ...p }
        delete next[cid]
        return next
      })
    }
  }

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
  const channels = data ?? []
  const creating = id === 'new'
  const current = creating ? null : channels.find((c) => String(c.id) === id) ?? channels[0] ?? null
  const q = query.trim().toLowerCase()
  const visible = q
    ? channels.filter(
        (c) => c.name.toLowerCase().includes(q) || (c.base_url ?? '').toLowerCase().includes(q),
      )
    : channels

  return (
    <>
      <RailMid>
        <div className="rail-label">渠道</div>
        <input
          className="rail-search"
          placeholder="搜渠道…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <div className="rail-list">
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
          className={'rail-new' + (creating ? ' is-on' : '')}
          onClick={() => nav('/channels/new')}
        >
          新建渠道
        </button>
      </RailMid>

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
              void reload()
              nav(`/channels/${newID}`, { replace: true })
              void probe(newID, false)
            }}
          />
        </>
      ) : current ? (
        <ChannelDetail
          key={current.id}
          ch={current}
          probe={probes[current.id]}
          onProbe={() => void probe(current.id, true)}
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
          onSaved={(savedID) => {
            void reload()
            void probe(savedID, false)
          }}
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
    </>
  )
}
