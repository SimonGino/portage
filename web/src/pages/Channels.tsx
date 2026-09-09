import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { Empty, ErrorBar } from '../ui'
import { ChannelIcon } from '../icons'
import { ChannelDetail } from './channels/detail'
import { ChannelForm } from './channels/form'
import { channelMark, filterChannels } from './channels/derive'
import { ChannelsProvider, useChannels } from './channels/useChannel'

/**
 * 模型页（路由仍是 /channels）：页内主从两栏——左列渠道清单（sticky），右栏是
 * 选中渠道的纳管模型（口径层 v0.75；v0.54 左栏退役后清单从壳搬回页内）。
 * 新建时右栏整个是表单，没有模型列表。
 *
 * 状态全在 ChannelsProvider（#56）：清单、当前渠道、凭证池、拉到的上游列表与
 * 那一把 mutate，右栏各组件用 useChannel(id) 直接拿。
 */
export default function Channels() {
  return (
    <ChannelsProvider>
      <ChannelsPage />
    </ChannelsProvider>
  )
}

function ChannelsPage() {
  const { id } = useParams()
  const nav = useNavigate()
  const { channels, loading, error, creating, current, reload } = useChannels()
  const [query, setQuery] = useState('')

  useEffect(() => {
    window.scrollTo(0, 0)
  }, [id])

  if (loading) return <div className="boot">加载中…</div>
  const visible = filterChannels(channels, query)

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
                <span className="mark">{channelMark(ch)}</span>
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
          <ChannelDetail key={current.id} id={current.id} />
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
