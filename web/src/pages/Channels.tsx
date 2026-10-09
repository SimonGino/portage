import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { Empty, ErrorBar, PageTitle } from '../ui'
import { CHANNEL_TABS } from '../routes'
import { ChannelIcon } from '../icons'
import { ChannelDetail } from './channels/detail'
import { NewChannel } from './channels/presets'
import { channelMark, filterChannels } from './channels/derive'
import { ChannelsProvider, useChannels } from './channels/useChannel'

/**
 * 渠道页（/channels；v0.71 前叫「模型」页）：页内主从两栏——左列渠道清单（sticky），右栏是
 * 选中渠道的纳管模型（口径层 v0.75；v0.54 左栏退役后清单从壳搬回页内）。
 * 新建时右栏整页是「预设目录 → 表单」两态（DESIGN v0.72，#181），零渠道时空态即目录；
 * 建成后带 `{ pick: true }` 跳详情，那边据此自动开 ModelPicker。
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
  // 保存后什么都不跑（口径层 v0.96 ①）：真实请求的钱只在人手点检测时花。拉模型列表
  // 是开 ModelPicker 那一下的事，由详情页按 location.state 打开（口径层 v1.47）。
  // 先等清单里有了新渠道再跳：否则 current 会回落到别的渠道，自动开的 ModelPicker
  // 就开在了那一家上。
  const onCreated = async (newID: number) => {
    await reload()
    nav(`/channels/${newID}`, { replace: true, state: { pick: true } })
  }
  const [query, setQuery] = useState('')

  useEffect(() => {
    window.scrollTo(0, 0)
  }, [id])

  if (loading) return <div className="boot">加载中…</div>
  const visible = filterChannels(channels, query)

  // 零渠道：整页就是预设目录（DESIGN v0.71/v0.72，同 magpie 零供应商形态）——左列
  // 此刻只有一句「还没有渠道」和一颗与目录重复的「新建渠道」，不摆。
  if (channels.length === 0 && !creating) {
    return (
      <div className="detail-col">
        <ErrorBar message={error} />
        <header className="page-head">
          <PageTitle title="渠道" tabs={CHANNEL_TABS} />
        </header>
        <Empty>还没有渠道。选一家接入，贴上 key 就能拉模型。</Empty>
        <NewChannel onSaved={(id) => void onCreated(id)} />
      </div>
    )
  }

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
            <NewChannel
              key="new"
              onCancel={() => nav(current ? `/channels/${current.id}` : '/channels')}
              onSaved={(id) => void onCreated(id)}
            />
          </>
        ) : current ? (
          <ChannelDetail key={current.id} id={current.id} />
        ) : null}
      </div>
    </div>
  )
}
