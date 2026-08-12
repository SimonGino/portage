import { useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api } from '../api'
import type { Channel, ChannelProbe, ModelListResult } from '../api'
import { Empty, ErrorBar, useList } from '../ui'
import { ChannelIcon } from '../icons'
import { ChannelDetail } from './channels/detail'
import { ChannelForm } from './channels/form'

/**
 * 渠道页是**主从两栏**（口径层 v0.45）：左栏只列渠道，右栏是选中那一个的全部——
 * 设置表单、探测结论、拉模型列表、纳管模型都在右边。
 *
 * 此前是一列长块，每个渠道把自己的全部内容摊在页面上。摊开的代价随渠道数线性涨：
 * 三个渠道各带十几个模型，「切到另一个渠道看看」就是滚三屏，而屏幕右边同时空着一大片。
 * 两栏把「在渠道之间跳」交给位置而不是滚动条，右栏也因此拿到一整块可以纵向铺开的地方。
 *
 * 选中项进 URL（`/channels/:id`）：刷新、回退、贴给自己看都还在同一个渠道上；
 * 存在 state 里的话 F5 就跳回第一个。
 */
export default function Channels() {
  const { id } = useParams()
  const nav = useNavigate()
  const { data, error, loading, reload, setError } = useList(() =>
    api.get<Channel[] | null>('/channels'),
  )
  // 左栏的搜索。挂十几二十个渠道之后（参照那份客户端的截图就是这个量级），
  // 「哪个是阿里那个生产号」靠竖着扫比不过打两个字母。
  const [query, setQuery] = useState('')
  // 探测结果只活在这个组件的内存里：口径层 v0.33 定的是「只提示、不落库、不参与
  // 路由」——探测结果会过期，存下来就变成一份会撒谎的缓存。刷新页面它就该没了。
  const [probes, setProbes] = useState<Record<number, ChannelProbe | 'running'>>({})
  // 拉回来的上游模型列表同样只活在内存里（口径层 v0.40）：它是填表助手，不是配置。
  // 中转站的 /v1/models 返回一份写死的大列表是常态，存下来就成了一份会撒谎的缓存——
  // 与 v0.33 拒绝把探测做成闸是同一条立论。刷新页面它就该没了。
  const [fetched, setFetched] = useState<Record<number, ModelListResult[] | 'running'>>({})

  async function fetchModels(cid: number) {
    setFetched((p) => ({ ...p, [cid]: 'running' }))
    try {
      const r = await api.post<{ results: ModelListResult[] }>(`/channels/${cid}/fetch-models`)
      setFetched((p) => ({ ...p, [cid]: r.results }))
    } catch {
      // 拉不到不算错误：上游没有 /v1/models 是常事，手工填就是了。
      setFetched((p) => {
        const next = { ...p }
        delete next[cid]
        return next
      })
    }
  }

  // withModels 分开的是「谁在调」：人点探测按钮才连模型矩阵一起跑（口径层 v0.43 ①
  // 只由人手点——那一层每格都是要花钱的真请求），保存渠道后自动跑的那次只要
  // 免费的子路径层（v0.33 定的就是「朝勾选的子路径各发一次」）。
  async function probe(cid: number, withModels: boolean) {
    setProbes((p) => ({ ...p, [cid]: 'running' }))
    try {
      // 子路径层逐把凭证探（v0.38），模型矩阵只用第一把启用凭证。
      const r = await api.post<ChannelProbe>(
        `/channels/${cid}/probe${withModels ? '?models=1' : ''}`,
      )
      setProbes((p) => ({ ...p, [cid]: r }))
    } catch {
      // 探测失败不算保存失败，也不该盖掉页面上别的错误：静默丢掉那一格。
      setProbes((p) => {
        const next = { ...p }
        delete next[cid]
        return next
      })
    }
  }

  // 任何写操作都走这里：出错就把后端那句话原样显示出来。400 装的是启动闸的
  // 校验原文（「渠道 x 已启用但没有可用凭证」这种），改写成「保存失败」等于
  // 把唯一有用的信息扔掉。写完一律重拉——删渠道会级联带走它的候选，
  // 就地改那一行会让页面和库悄悄分叉。
  //
  // 回一个「成没成」：多数调用方不看（失败时错误条已经说明了一切），但攒着未提交
  // 选择的挑选面板要看——那儿失败还照常关框，等于把人勾了半天的东西丢了。
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
  // 落到一个不存在的 id（渠道刚被删、或者链接过期）就退回第一个，不显示空右栏：
  // 右栏空着而左栏有内容，看起来像页面坏了。
  const current = creating ? null : channels.find((c) => String(c.id) === id) ?? channels[0] ?? null
  const q = query.trim().toLowerCase()
  // base_url 也参与匹配但不显示：同一家上游在不同中转下会重名，搜 `dashscope`
  // 能把它们分出来（跟接入点表单里那个下拉是同一条理由）。
  const visible = q
    ? channels.filter(
        (c) => c.name.toLowerCase().includes(q) || (c.base_url ?? '').toLowerCase().includes(q),
      )
    : channels

  return (
    <>
      <ErrorBar message={error} />
      <div className="split">
        <div className="master">
          <div className="master-head">
            <input
              className="master-search"
              placeholder="搜渠道…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            <button
              className="btn btn-quiet"
              onClick={() => nav('/channels/new')}
              title="新建渠道"
            >
              ＋
            </button>
          </div>
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
                    'master-item' +
                    (current?.id === ch.id ? ' is-current' : '') +
                    (ch.disabled ? ' is-off' : '')
                  }
                  onClick={() => nav(`/channels/${ch.id}`)}
                >
                  <ChannelIcon channel={ch} size={20} />
                  <span className="master-name">{ch.name}</span>
                  {/* 左栏只放「哪个渠道现在不对劲」这一档信息：协议集与模型数右栏
                      都有，重复摆一遍会把这一列挤成第二份详情页；缺凭证不一样，
                      它是渠道从能用变不能用的那条线（口径层 v0.38），得在跳之前
                      就看得见。 */}
                  {/* 启停的开关在右栏抬头，不在这儿：这一行整个是个按钮（点它换
                      渠道），往按钮里再嵌一个按钮既不合法也会让「点歪一格就把渠道
                      停了」变成日常。这里只报状态。 */}
                  {ch.disabled && <span className="tag tag-off">停用</span>}
                  {!ch.disabled && ch.enabled_keys === 0 && (
                    <span className="tag tag-warn">缺凭证</span>
                  )}
                  {!ch.disabled && (ch.protocols ?? []).length === 0 && (
                    <span className="tag tag-warn">无协议</span>
                  )}
                </button>
              ))
            )}
          </div>
        </div>

        <div className="detail">
          {creating ? (
            <ChannelForm
              key="new"
              channel={null}
              onCancel={() => nav(current ? `/channels/${current.id}` : '/channels')}
              onSaved={(newID) => {
                void reload()
                nav(`/channels/${newID}`, { replace: true })
                // 保存成功之后才探测，且不挡保存——勾错协议集的后果（一半端点全
                // 404、另一半完全正常）启动闸看不见，人正好在这一刻最有可能改对它。
                // 只跑免费的子路径层：建个渠道不该顺手打出一屏花钱的模型探测。
                void probe(newID, false)
              }}
            />
          ) : current ? (
            <ChannelDetail
              key={current.id}
              ch={current}
              probe={probes[current.id]}
              onProbe={() => void probe(current.id, true)}
              fetched={fetched[current.id]}
              onFetchModels={() => void fetchModels(current.id)}
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
            <Empty>还没有渠道。先建一个上游，再给它加纳管模型——加完就能直接调了。</Empty>
          )}
        </div>
      </div>

    </>
  )
}
