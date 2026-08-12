import { useMemo, useState } from 'react'
import {
  api,
  KEY_MODE_OPTIONS,
  PROTOCOL_LABEL,
  PROTOCOL_PATH,
  PROTOCOL_SHORT,
  PROTOCOL_SOON,
} from '../api'
import type {
  Channel,
  ChannelModel,
  Credential,
  KeyMode,
  ModelListResult,
  ProbeGroup,
  Protocol,
} from '../api'
import { Card, Confirm, CopyCode, Dialog, Empty, ErrorBar, Field, Toggle, useList } from '../ui'
import { Segmented, SegmentedMulti } from '../fields'
import { Avatar, ChannelIcon, ModelIcon, vendorForChannel, vendorForModel } from '../icons'

export default function Channels() {
  const { data, error, loading, reload, setError } = useList(() =>
    api.get<Channel[] | null>('/channels'),
  )
  const [editing, setEditing] = useState<Channel | 'new' | null>(null)
  const [credFor, setCredFor] = useState<Channel | null>(null)
  // 探测结果只活在这个组件的内存里：口径层 v0.33 定的是「只提示、不落库、不参与
  // 路由」——探测结果会过期，存下来就变成一份会撒谎的缓存。刷新页面它就该没了。
  const [probes, setProbes] = useState<Record<number, ProbeGroup[] | 'running'>>({})
  // 拉回来的上游模型列表同样只活在内存里（口径层 v0.40）：它是填表助手，不是配置。
  // 中转站的 /v1/models 返回一份写死的大列表是常态，存下来就成了一份会撒谎的缓存——
  // 与 v0.33 拒绝把探测做成闸是同一条立论。刷新页面它就该没了。
  const [fetched, setFetched] = useState<Record<number, ModelListResult[] | 'running'>>({})

  async function fetchModels(id: number) {
    setFetched((p) => ({ ...p, [id]: 'running' }))
    try {
      const r = await api.post<{ results: ModelListResult[] }>(`/channels/${id}/fetch-models`)
      setFetched((p) => ({ ...p, [id]: r.results }))
    } catch {
      // 拉不到不算错误：上游没有 /v1/models 是常事，手工填就是了。
      setFetched((p) => {
        const next = { ...p }
        delete next[id]
        return next
      })
    }
  }

  async function probe(id: number) {
    setProbes((p) => ({ ...p, [id]: 'running' }))
    try {
      // 逐把凭证探（口径层 v0.38），所以结果是按凭证分的组。
      const r = await api.post<{ credentials: ProbeGroup[] }>(`/channels/${id}/probe`)
      setProbes((p) => ({ ...p, [id]: r.credentials }))
    } catch {
      // 探测失败不算保存失败，也不该盖掉页面上别的错误：静默丢掉那一格。
      setProbes((p) => {
        const next = { ...p }
        delete next[id]
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
  // 选择的挑选面板要看——那儿失败还照常关框，等于把人勾了半天的东西丢了，
  // 正是 Dialog 的 guard 要挡的那件事。
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

  return (
    <>
      <ErrorBar message={error} />
      <Card
        title="渠道"
        action={
          <button className="btn btn-primary" onClick={() => setEditing('new')}>
            新建渠道
          </button>
        }
      >
        {channels.length === 0 ? (
          <Empty>还没有渠道。先建一个上游，再给它加纳管模型——加完就能直接调了。</Empty>
        ) : (
          <div className="channels">
            {channels.map((ch) => (
              <ChannelCard
                key={ch.id}
                ch={ch}
                onEdit={() => setEditing(ch)}
                onCredential={() => setCredFor(ch)}
                onProbe={() => void probe(ch.id)}
                probe={probes[ch.id]}
                onFetchModels={() => void fetchModels(ch.id)}
                fetched={fetched[ch.id]}
                mutate={mutate}
              />
            ))}
          </div>
        )}
      </Card>

      {editing && (
        <ChannelForm
          channel={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={(id) => {
            setEditing(null)
            void reload()
            // 保存成功之后才探测，且不挡保存——勾错协议集的后果（一半端点全 404、
            // 另一半完全正常）启动闸看不见，人正好在这一刻最有可能改对它。
            void probe(id)
          }}
        />
      )}
      {credFor && (
        /* 关掉就重拉：凭证计数会变，「缺凭证」那个标记得跟着消失。 */
        <CredentialPool
          channel={credFor}
          onClose={() => {
            setCredFor(null)
            void reload()
          }}
        />
      )}
    </>
  )
}

function ChannelCard({
  ch,
  onEdit,
  onCredential,
  onProbe,
  probe,
  onFetchModels,
  fetched,
  mutate,
}: {
  ch: Channel
  onEdit: () => void
  onCredential: () => void
  onProbe: () => void
  probe?: ProbeGroup[] | 'running'
  onFetchModels: () => void
  fetched?: ModelListResult[] | 'running'
  /** 回 false 表示这次写没成——挑选面板据此决定关不关框，别的调用方不看。 */
  mutate: (fn: () => Promise<unknown>) => Promise<boolean>
}) {
  const [picking, setPicking] = useState(false)
  const models = ch.models ?? []
  const protos = ch.protocols ?? []
  const listed = Array.isArray(fetched) ? fetched : null
  // 上游在哪些协议侧列出了这个模型。**只用于给建议**，不自动改配置——拉回来的列表
  // 可能是中转站写死的（口径层 v0.40），采信它等于把探测做成了闸。
  function listedOn(model: string): Protocol[] {
    if (!listed) return []
    return listed
      .filter((r) => (r.models ?? []).includes(model))
      .flatMap((r) => r.protocols)
      .filter((p) => protos.includes(p))
  }
  // 渠道的每一个协议侧都真拉到了一份列表。**证据不全就不推断子集**：`models` 为 null
  // 是「这一侧没拉到」（401、超时、回的不是 JSON），与「拉到了但没列出它」在证据上是
  // 两回事，而 listedOn 把两者压成了同一个「不在里面」。按后者写库，等于凭零证据砍掉
  // 一条本来可能原生可走的协议路径，把请求推去做有损转换——比没推断坏得多。
  const listComplete =
    listed !== null &&
    protos.every((p) => listed.some((r) => r.models !== null && r.protocols.includes(p)))

  return (
    <div className={'channel' + (ch.disabled ? ' is-off' : '')}>
      <div className="channel-head">
        <div className="channel-title">
          <ChannelIcon channel={ch} size={24} />
          <strong>{ch.name}</strong>
          {/* 协议集全列出来：这一格回答的是「这个渠道能接住哪些客户端」，
              只显示一个就看不出来 Responses 的 harness 会不会走转换。 */}
          {protos.map((p) => (
            <span key={p} className="tag" title={PROTOCOL_LABEL[p] + ' · ' + PROTOCOL_PATH[p]}>
              {PROTOCOL_SHORT[p] ?? p}
            </span>
          ))}
          {protos.length === 0 && <span className="tag tag-warn">协议集为空</span>}
          {ch.disabled && <span className="tag tag-off">已停用</span>}
          {/* 可用凭证归零是渠道从能用变不能用的唯一运行期路径（摘光不设特例，
              口径层 v0.38），而且启用渠道零凭证连启动闸都过不去，所以这条得显眼。
              有凭证时也把数目摆出来——3 把里坏了 2 把，只说「有凭证」等于把劣化
              过程整个藏住。 */}
          {ch.enabled_keys === 0 ? (
            <span className="tag tag-warn">缺凭证</span>
          ) : (
            <span className="tag" title="可用凭证数">
              凭证 {ch.enabled_keys}
            </span>
          )}
          {ch.disabled_keys > 0 && (
            <span className="tag tag-warn" title="401 自动摘除或人工停用，只能人工恢复">
              停用 {ch.disabled_keys}
            </span>
          )}
        </div>
        {/* 五颗等分量的描边按钮排一行，等于没有主次（DESIGN.md §5：渠道编辑是最高频
            动作）。只有「编辑」保留边框，探测/拉列表/凭证池这三个是偶发的诊断与配置
            动作，压到 ghost 那一档——文字照常可读，只是不再跟正主抢视线。 */}
        <div className="row-actions">
          <button className="btn btn-ghost" onClick={onProbe} disabled={probe === 'running'}>
            {probe === 'running' ? '探测中…' : '探测协议'}
          </button>
          {/* 拉的是上游自己声明的模型列表，用来省掉「这个模型到底在哪一侧」的手工核对。
              结果只进表单不进路由（口径层 v0.40）。 */}
          <button
            className="btn btn-ghost"
            onClick={onFetchModels}
            disabled={fetched === 'running'}
            title="拉上游 /v1/models，只用来帮你填表，不落库也不影响路由"
          >
            {fetched === 'running' ? '拉取中…' : '拉模型列表'}
          </button>
          <button className="btn btn-ghost" onClick={onCredential}>
            凭证池
          </button>
          <button className="btn" onClick={onEdit}>
            编辑
          </button>
          <Confirm ghost onConfirm={() => void mutate(() => api.del(`/channels/${ch.id}`))} />
        </div>
      </div>

      <div className="channel-url">{ch.base_url}</div>

      {/* 探测结论只提示，不挡任何操作，也不落库——它会过期（口径层 v0.33）。
          按凭证分行（v0.38）：同一个子路径对不同的号可以有不同结论，合成一条就把
          「哪一把不行」抹掉了。 */}
      {Array.isArray(probe) &&
        probe.map((g) => <ProbeRow key={g.credential} group={g} multi={probe.length > 1} />)}

      {listed && (
        <FetchedModels
          results={listed}
          existing={new Set(models.map((m) => m.upstream_model))}
          onPick={() => setPicking(true)}
        />
      )}
      {picking && listed && (
        <ModelPicker
          channel={ch}
          results={listed}
          existing={new Set(models.map((m) => m.upstream_model))}
          onClose={() => setPicking(false)}
          onAdd={async (names) => {
            const ok = await mutate(async () => {
              for (const name of names) {
                // 逐个 POST 而不是一把批量接口：AddChannelModel 本来就是幂等的，
                // 而逐个发能让「加到一半上游把我限流了」停在一个确定的位置上。
                const on = listedOn(name)
                await api.post(`/channels/${ch.id}/models`, {
                  upstream_model: name,
                  // 上游在每一侧都列出了它，就不写子集——那等价于继承，写进去只是
                  // 一份会在渠道加协议时挡路的冗余。有一侧没拉到（listComplete 为
                  // 假）同样留继承：见上面那段，缺证据不是「不支持」的证据。
                  protocols: listComplete && on.length < protos.length ? on : [],
                })
              }
            })
            // 失败就把框留在原地：勾选还在，错误条已经写明是哪一步断的，人可以
            // 只重发剩下的。关掉等于让人从头再勾一遍两百条里的那几个。
            // （POST 幂等，已加进去的重发无害。）
            if (ok) setPicking(false)
          }}
        />
      )}

      <div className="models">
        <div className="models-title">纳管模型{models.length > 0 && ` · ${models.length}`}</div>
        {models.length === 0 ? (
          <div className="muted">还没有纳管模型。填上游那边真实的模型名，比如 gpt-4o、deepseek-chat。</div>
        ) : (
          <div className="model-grid">
            {models.map((m) => (
              <div key={m.id} className={'model' + (m.disabled ? ' is-off' : '')}>
                <ModelIcon model={m.upstream_model} size={18} />
                {/* 摆的是裸模型名（网格单元里放不下限定名），复制走的是限定名——
                    那才是客户端 `model` 字段要填的东西（口径层 v0.32），手抄一个带
                    斜杠的长串很容易漏字符，漏了的表现是 404。停用的不给复制：抄走
                    了也调不通，title 里给全名就够。 */}
                {m.disabled ? (
                  <code className="model-name" title={m.upstream_model}>
                    {m.upstream_model}
                  </code>
                ) : (
                  <CopyCode
                    className="model-name"
                    value={`${ch.name}/${m.upstream_model}`}
                    label={m.upstream_model}
                    title={`点击复制 ${ch.name}/${m.upstream_model}`}
                  />
                )}
                <div className="model-actions">
                  {/* 状态标注不再单独挂一枚 tag：开关自己就写着「已停用」，
                      两处说同一件事只是把行挤窄。 */}
                  <Toggle
                    on={!m.disabled}
                    label={m.upstream_model}
                    onChange={(on) =>
                      void mutate(() => api.put(`/channel-models/${m.id}`, { disabled: !on }))
                    }
                  />
                  <Confirm
                    ghost
                    onConfirm={() => void mutate(() => api.del(`/channel-models/${m.id}`))}
                  />
                </div>
                {/* 单协议渠道通常没什么可勾的，但**存量子集在的时候必须照实显示**
                    （口径层 v0.40 ①）：渠道从多协议缩成一个、而这个模型的子集不含它，
                    正是它变得不可用的那一刻——把这一格藏了，人就只能对着一个看上去
                    哪都没问题的配置查 503，而且没有入口把那份存量值清掉。 */}
                {(protos.length > 1 || (m.protocols ?? []).length > 0) && (
                  <ModelProtocols
                    model={m}
                    channelProtocols={protos}
                    listedOn={listedOn(m.upstream_model)}
                    listComplete={listComplete}
                    mutate={mutate}
                  />
                )}
              </div>
            ))}
          </div>
        )}
        <AddModels channel={ch} mutate={mutate} />
      </div>
    </div>
  )
}

/**
 * FetchedModels 是「拉模型列表」的结论条（口径层 v0.40）。
 *
 * 它是**填表助手**，不是配置：这里显示的一切都还没进库，人点了「加进来」才落库。
 * 中转站返回一份写死的大列表是常态，所以这条不给「同步」这种字眼——同步意味着以
 * 上游为准，而以一份会撒谎的列表为准正是 §2.2 拒绝把探测做成闸的理由。
 */
function FetchedModels({
  results,
  existing,
  onPick,
}: {
  results: ModelListResult[]
  existing: Set<string>
  onPick: () => void
}) {
  // 各侧的名字并起来去重：一个模型出现在哪几侧是 ModelProtocols 那行的事，
  // 这里只回答「还有哪些没纳管」。
  const all = Array.from(new Set(results.flatMap((r) => r.models ?? [])))
  const fresh = all.filter((m) => !existing.has(m))
  const failed = results.filter((r) => (r.models ?? []).length === 0)

  return (
    <div className={'probe' + (failed.length === results.length ? ' probe-bad' : '')}>
      <span>
        {results
          .map(
            (r) =>
              `${r.protocols.map((p) => PROTOCOL_SHORT[p] ?? p).join('/')} 侧：${r.detail}`,
          )
          .join('；')}
        {all.length > 0 &&
          (fresh.length > 0 ? ` · 其中 ${fresh.length} 个还没纳管` : ' · 都已经纳管了')}
      </span>
      {/* 结论条到此为止：不在这儿摆名单，也不给「全加进来」。上游动辄回两百多个
          （中转站把能想到的名字全写死在列表里），那颗按钮点下去等于给自己造一份
          两百行的假配置——纳管是「我要用哪几个」，不是「上游有哪几个」。 */}
      {fresh.length > 0 && (
        <button className="btn btn-quiet" onClick={onPick}>
          挑要纳管的…
        </button>
      )}
    </div>
  )
}

/**
 * familyOf 给模型名归族，用来在挑选面板里分组。
 *
 * 两条规则够用：带斜杠的取斜杠前那段（`kimi/kimi-k3` → `kimi`，中转站转售别家模型
 * 时的惯例写法），否则取开头那串字母（`qwen-image-3.0-pro`、`qwen3.8-max` 都 → `qwen`）。
 * 特意在字母处断而不是在第一个 `-` 处断：后者会把 `qwen3.7-flash` 和 `qwen3.8-max`
 * 拆成两族，而人扫这份列表时想的是「通义那一堆」。
 */
function familyOf(name: string): string {
  const slash = name.indexOf('/')
  if (slash > 0) return name.slice(0, slash)
  const m = /^[a-zA-Z]+/.exec(name)
  return m ? m[0].toLowerCase() : '其它'
}

/** 只剩一个成员的族没有分组的意义，全并进「其它」，摆在最后。 */
const OTHER = '其它'
/** 超过这么多的族默认收起来：一屏塞不下就等于没分组。 */
const COLLAPSE_AT = 12

/**
 * ModelPicker 是「拉模型列表」之后挑哪些纳管的面板（口径层 v0.40）。
 *
 * 它仍然只是**填表助手**：这里的一切都还没进库，勾完点「添加」才逐个落库。所以面板里
 * 不出现「同步」，也没有「按上游对齐」这种动作——以一份可能写死的上游列表为准，正是
 * §2.2 拒绝把探测做成闸的那条立论。已纳管而上游列表里没有的，这儿一个字都不提，
 * 更不会去删：那件事由模型格子里那句「上游列表里没有它」提示，删不删是人的决定。
 *
 * 批量不是罪，无差别才是：全选按钮只作用于**当前可见**的那些（搜索词 + 未纳管筛选
 * 之后剩下的），所以「选中 5 个」永远是人先划定了范围才发生的事。
 */
function ModelPicker({
  channel,
  results,
  existing,
  onClose,
  onAdd,
}: {
  channel: Channel
  results: ModelListResult[]
  existing: Set<string>
  onClose: () => void
  onAdd: (names: string[]) => Promise<void>
}) {
  const [query, setQuery] = useState('')
  const [showManaged, setShowManaged] = useState(false)
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [busy, setBusy] = useState(false)

  // 每个名字被哪几侧列出来了。同一个模型可能在多份结果里出现（渠道支持多协议时
  // 是逐侧拉的），协议要去重。
  const sides = useMemo(() => {
    const m = new Map<string, Protocol[]>()
    for (const r of results) {
      for (const name of r.models ?? []) {
        const cur = m.get(name) ?? []
        m.set(name, [...cur, ...r.protocols.filter((p) => !cur.includes(p))])
      }
    }
    return m
  }, [results])

  const q = query.trim().toLowerCase()
  // 排序按名字：上游返回的顺序没有语义（多半是库里的自增序），而人是竖着扫这一列的。
  const visible = useMemo(
    () =>
      Array.from(sides.keys())
        .filter((n) => (showManaged || !existing.has(n)) && (!q || n.toLowerCase().includes(q)))
        .sort((a, b) => a.localeCompare(b)),
    [sides, existing, showManaged, q],
  )

  // 分族。大族在前——两百个名字里真正要找的那几个多半在最大的那一族里。
  const groups = useMemo(() => {
    const by = new Map<string, string[]>()
    for (const n of visible) {
      const f = familyOf(n)
      by.set(f, [...(by.get(f) ?? []), n])
    }
    const out: { name: string; items: string[] }[] = []
    const other: string[] = []
    for (const [name, items] of by) {
      if (items.length === 1) other.push(...items)
      else out.push({ name, items })
    }
    out.sort((a, b) => b.items.length - a.items.length || a.name.localeCompare(b.name))
    if (other.length > 0) out.push({ name: OTHER, items: other.sort((a, b) => a.localeCompare(b)) })
    return out
  }, [visible])

  // 搜的时候一律展开：搜完还要再点开一层，等于这个搜索框只帮你缩小了标题栏。
  const isOpen = (g: { name: string; items: string[] }) =>
    q !== '' || (expanded[g.name] ?? g.items.length <= COLLAPSE_AT)

  const pickable = visible.filter((n) => !existing.has(n))
  const allPicked = pickable.length > 0 && pickable.every((n) => picked.has(n))

  function toggle(name: string) {
    setPicked((prev) => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  }

  /** 一批一起改：族级全选和「全选可见」都走这里。 */
  function setMany(names: string[], on: boolean) {
    setPicked((prev) => {
      const next = new Set(prev)
      for (const n of names) {
        if (existing.has(n)) continue
        if (on) next.add(n)
        else next.delete(n)
      }
      return next
    })
  }

  const total = sides.size
  const managed = Array.from(sides.keys()).filter((n) => existing.has(n)).length

  return (
    <Dialog
      title={`挑要纳管的模型：${channel.name}`}
      onClose={onClose}
      wide
      guard={picked.size > 0}
    >
      <div className="mpick">
        {/* 先把这份列表的性质说清楚：它不是配置，勾了才是。 */}
        <div className="mpick-note muted">
          上游列出 {total} 个，其中 {managed} 个已纳管。这份列表只用来帮你填表，不落库、不影响路由——
          中转站把没有的模型也写进列表是常事，勾之前最好确认它真的能调通。
        </div>

        <div className="mpick-bar">
          <input
            autoFocus
            className="mpick-search"
            placeholder="搜模型名，比如 qwen3.7 或 embedding"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          <button
            type="button"
            className={'btn btn-quiet' + (showManaged ? ' is-on' : '')}
            onClick={() => setShowManaged((v) => !v)}
            title="已纳管的也显示出来（灰着、勾不动），用来核对哪些已经加过了"
          >
            显示已纳管
          </button>
          <span className="spacer" />
          <button
            type="button"
            className="btn btn-quiet"
            disabled={pickable.length === 0}
            onClick={() => setMany(pickable, !allPicked)}
            title="只作用于当前筛出来的这些"
          >
            {allPicked ? `取消这 ${pickable.length} 个` : `全选可见的 ${pickable.length} 个`}
          </button>
        </div>

        <div className="mpick-list">
          {visible.length === 0 && (
            <Empty>
              {q ? `没有匹配「${query}」的模型。` : '上游列出的都已经纳管了。'}
            </Empty>
          )}
          {groups.map((g) => {
            const open = isOpen(g)
            const free = g.items.filter((n) => !existing.has(n))
            const on = free.filter((n) => picked.has(n)).length
            return (
              <div key={g.name} className="mpick-group">
                <div className="mpick-group-head">
                  <button
                    type="button"
                    className="mpick-group-name"
                    aria-expanded={open}
                    onClick={() => setExpanded((p) => ({ ...p, [g.name]: !open }))}
                  >
                    <span className="mpick-caret" aria-hidden>
                      {open ? '▾' : '▸'}
                    </span>
                    {g.name}
                    <span className="muted">{g.items.length}</span>
                  </button>
                  {on > 0 && <span className="tag">已选 {on}</span>}
                  {free.length > 0 && (
                    <button
                      type="button"
                      className="btn btn-ghost"
                      onClick={() => setMany(free, on < free.length)}
                    >
                      {on < free.length ? '全选' : '全不选'}
                    </button>
                  )}
                </div>
                {open && (
                  <div className="mpick-rows">
                    {g.items.map((name) => {
                      const has = existing.has(name)
                      return (
                        <label
                          key={name}
                          className={'mpick-row' + (has ? ' is-off' : '')}
                          title={has ? '已经纳管过了' : `${channel.name}/${name}`}
                        >
                          <input
                            type="checkbox"
                            checked={has || picked.has(name)}
                            disabled={has}
                            onChange={() => toggle(name)}
                          />
                          <ModelIcon model={name} size={16} />
                          <code className="mpick-name">{name}</code>
                          {/* 上游在哪几侧列出了它。渠道只说一种协议时这一列全一样，
                              没有信息量，就不摆。 */}
                          {(channel.protocols ?? []).length > 1 &&
                            (sides.get(name) ?? []).map((p) => (
                              <span key={p} className="tag" title={PROTOCOL_LABEL[p]}>
                                {PROTOCOL_SHORT[p] ?? p}
                              </span>
                            ))}
                          {has && <span className="tag tag-off">已纳管</span>}
                        </label>
                      )
                    })}
                  </div>
                )}
              </div>
            )
          })}
        </div>

        <div className="form-actions">
          <span className="muted">
            {picked.size > 0 ? `已选 ${picked.size} 个` : '还没选'}
          </span>
          <button type="button" className="btn btn-quiet" onClick={onClose} disabled={busy}>
            取消
          </button>
          <button
            type="button"
            className="btn btn-primary"
            disabled={picked.size === 0 || busy}
            onClick={() => {
              setBusy(true)
              // 失败时 mutate 已经把后端那句话贴到页面顶上了，这儿只负责把按钮放回去。
              void onAdd(Array.from(picked)).finally(() => setBusy(false))
            }}
          >
            {busy ? `添加中…共 ${picked.size} 个` : `添加 ${picked.size} 个`}
          </button>
        </div>
      </div>
    </Dialog>
  )
}

/**
 * ModelProtocols 是模型格子里那行协议子集（口径层 v0.40）。
 *
 * 只在渠道支持多个协议时出现——单协议渠道没有子集可言，摆一行只能勾一个的 chips
 * 是纯噪音。它总是占一行而不是「有值才出现」：模型网格里某一格凭空高一截，同一行
 * 其它格会跟着拉高，整片网格参差（同 .model-name 那条截断不换行的理由）。
 *
 * 全勾等价于继承，所以勾满时归一成空数组存回去，不在库里留一份跟渠道集重复的冗余：
 * 那份冗余会在渠道日后加一个协议时，悄悄把新协议挡在这个模型外面。
 */
function ModelProtocols({
  model,
  channelProtocols,
  listedOn,
  listComplete,
  mutate,
}: {
  model: ChannelModel
  channelProtocols: Protocol[]
  /** 上游在哪些协议侧列出了这个模型。空数组 = 没拉过，或哪一侧都没列。 */
  listedOn: Protocol[]
  /** 渠道的每一侧都真拉到了列表。为假时 listedOn 的空缺分不清「没列出」和「没拉到」。 */
  listComplete: boolean
  mutate: (fn: () => Promise<unknown>) => Promise<unknown>
}) {
  const current = model.protocols ?? []
  const inherit = current.length === 0
  // 渠道协议集缩小之后，模型上没跟着改的那些值会留在这儿（宽松存，见口径层 v0.40）。
  // 照实显示而不是悄悄滤掉：它们此刻确实让这个模型不可用，藏起来只会让人对着一个
  // 「看上去哪都没问题」的配置查 503。
  const stale = current.filter((p) => !channelProtocols.includes(p))

  function save(next: Protocol[]) {
    const inChannel = channelProtocols.filter((p) => next.includes(p))
    const rest = next.filter((p) => !channelProtocols.includes(p))
    // 勾满且没有失效项才归一成继承——还留着失效项时归零会把它们一并抹掉，
    // 而那是人没点过的东西。
    const norm = inChannel.length === channelProtocols.length && rest.length === 0 ? [] : [...inChannel, ...rest]
    void mutate(() =>
      api.put(`/channel-models/${model.id}`, { disabled: model.disabled, protocols: norm }),
    )
  }

  function toggle(p: Protocol) {
    save(current.includes(p) ? current.filter((x) => x !== p) : [...current, p])
  }

  // 建议只在「上游确实只列出了一部分」时给，且不自动应用——拉回来的列表可能是中转站
  // 写死的，采信它等于把探测做成了闸（口径层 v0.33 立论）。
  const suggest =
    listComplete && listedOn.length > 0 && listedOn.length < channelProtocols.length
      ? listedOn
      : null
  const same =
    suggest !== null &&
    suggest.length === current.length &&
    suggest.every((p) => current.includes(p))

  return (
    <div className="model-protocols">
      <span className="model-protocols-label" title="不勾 = 跟渠道一样。勾了就只走勾中的那些。">
        协议
      </span>
      {channelProtocols.map((p) => (
        <button
          key={p}
          type="button"
          className={'chip-toggle' + (current.includes(p) ? ' is-on' : '')}
          onClick={() => toggle(p)}
          title={PROTOCOL_LABEL[p] + ' · ' + PROTOCOL_PATH[p]}
        >
          {PROTOCOL_SHORT[p] ?? p}
        </button>
      ))}
      {stale.map((p) => (
        <button
          key={p}
          type="button"
          className="chip-toggle is-stale"
          onClick={() => toggle(p)}
          title={`渠道已经不说 ${PROTOCOL_LABEL[p] ?? p} 了，这一项正让这个模型没有可用协议。点一下移除。`}
        >
          {PROTOCOL_SHORT[p] ?? p}
        </button>
      ))}
      {inherit && stale.length === 0 && <span className="muted">跟渠道一样</span>}
      {!inherit && stale.length === current.length && (
        <span className="tag tag-warn" title="与渠道协议集没有交集，这个模型当下用不了">
          无可用协议
        </span>
      )}
      {suggest && !same && (
        <button type="button" className="chip-toggle chip-suggest" onClick={() => save(suggest)}>
          上游只在 {suggest.map((p) => PROTOCOL_SHORT[p] ?? p).join('、')} 侧列出 · 采纳
        </button>
      )}
      {listComplete && listedOn.length === 0 && (
        <span className="muted" title="拉回来的列表里没有这个名字。可能是上游没提供 /v1/models，也可能是名字写错了">
          上游列表里没有它
        </span>
      )}
    </div>
  )
}

/** ProbeRow 是一份凭证的探测结论。全通报一句，不通的逐条列。 */
function ProbeRow({ group, multi }: { group: ProbeGroup; multi: boolean }) {
  const unreachable = group.results.filter((r) => !r.reachable)
  const who = group.credential ? group.credential + (group.disabled ? '（已停用）' : '') : ''
  return (
    <div className={'probe' + (unreachable.length > 0 ? ' probe-bad' : '')}>
      {unreachable.length === 0 ? (
        <span>
          {multi && who ? `${who}：` : ''}探测通过：勾选的 {group.results.length} 个协议子路径上游都有
        </span>
      ) : (
        <>
          <span>
            {multi && who ? `${who}：` : ''}探测未通过 {unreachable.length}{' '}
            项——只是提示，不影响保存与路由，但这些协议的客户端打过来会 404：
          </span>
          <ul>
            {unreachable.map((r) => (
              <li key={r.protocol}>
                <code>{PROTOCOL_PATH[r.protocol] ?? r.protocol}</code> {r.detail}
                {r.status > 0 && ` (HTTP ${r.status})`}
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  )
}

/**
 * AddModels 是往渠道里加纳管模型的那一行。
 *
 * 接受**一次粘一批**——逗号、空格、换行都算分隔。上游控制台的模型列表复制下来就是
 * 这种形状，逐个敲进去要来回十几趟。已经纳管过的自动跳过而不是报错：粘一份完整清单
 * 进来「把新的加上」是最常见的用法，为几个重复项整批失败没有道理。
 */
function AddModels({
  channel,
  mutate,
}: {
  channel: Channel
  mutate: (fn: () => Promise<unknown>) => Promise<unknown>
}) {
  const [draft, setDraft] = useState('')
  const existing = new Set((channel.models ?? []).map((m) => m.upstream_model))

  const parsed = Array.from(
    new Set(
      draft
        .split(/[\s,，、]+/)
        .map((s) => s.trim())
        .filter(Boolean),
    ),
  )
  const fresh = parsed.filter((m) => !existing.has(m))
  const dupes = parsed.length - fresh.length

  return (
    <form
      className="add-models"
      onSubmit={(e) => {
        e.preventDefault()
        if (fresh.length === 0) return
        setDraft('')
        void mutate(async () => {
          // 串行而不是 Promise.all：SQLite 那头连接池是 1，并发写只会排队，
          // 而串行出错时能停在第一个失败上，不至于半成功一片。
          for (const m of fresh) {
            await api.post(`/channels/${channel.id}/models`, { upstream_model: m })
          }
        })
      }}
    >
      <div className="add-models-row">
        <Avatar
          vendor={fresh.length === 1 ? vendorForModel(fresh[0]) : null}
          fallback={fresh.length === 1 ? fresh[0] : '+'}
          size={20}
        />
        <input
          placeholder="上游模型名，可一次粘一批（逗号或换行分隔）"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
        />
        <button className="btn btn-quiet" disabled={fresh.length === 0}>
          {fresh.length > 1 ? `添加 ${fresh.length} 个` : '添加'}
        </button>
      </div>
      {fresh.length > 1 && (
        <div className="add-models-preview">
          {fresh.map((m) => (
            <span key={m} className="chip">
              <ModelIcon model={m} size={16} />
              <code>{m}</code>
            </span>
          ))}
        </div>
      )}
      {dupes > 0 && <div className="field-hint">其中 {dupes} 个已经纳管过，会跳过。</div>}
    </form>
  )
}

const PROTOCOLS: Protocol[] = ['anthropic', 'openai', 'openai_responses']

function ChannelForm({
  channel,
  onClose,
  onSaved,
}: {
  channel: Channel | null
  onClose: () => void
  onSaved: (id: number) => void
}) {
  const [name, setName] = useState(channel?.name ?? '')
  // 支持协议集（口径层 v0.33）。默认只勾 OpenAI：绝大多数上游只提供它，多勾一个探测
  // 不过反而要人回来改。
  const [protos, setProtos] = useState<Protocol[]>(
    channel?.protocols?.length ? channel.protocols : ['openai'],
  )
  const [baseURL, setBaseURL] = useState(channel?.base_url ?? '')
  const [keyMode, setKeyMode] = useState<KeyMode>(channel?.key_mode ?? 'polling')
  const [disabled, setDisabled] = useState(channel?.disabled ?? false)
  // 凭证只在**新建**时出现在这张表单里。编辑走单独的入口，这样「改个名字」
  // 不可能顺手把凭证清空——后端的修改接口本来就不看这个字段。
  const [credential, setCredential] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    try {
      const body = { name, protocols: protos, base_url: baseURL, key_mode: keyMode, disabled }
      if (channel) {
        await api.put(`/channels/${channel.id}`, body)
        onSaved(channel.id)
      } else {
        const created = await api.post<{ id: number }>('/channels', { ...body, credential })
        onSaved(created.id)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog title={channel ? `编辑渠道：${channel.name}` : '新建渠道'} onClose={onClose}>
      <form className="form" onSubmit={submit}>
        {/* 图标是从 base_url 的 host 猜出来的（渠道没有「供应商」这个字段）。
            边填边显示，等于顺手校验了域名有没有填错——图标一直是首字母块，
            多半是 base_url 还没填对。 */}
        <div className="form-preview">
          <Avatar vendor={vendorForChannel({ name, base_url: baseURL })} fallback={name || '?'} size={40} />
          <div>
            <div className="form-preview-name">{name || '未命名渠道'}</div>
            <div className="muted">{baseURL || '还没填 base_url'}</div>
          </div>
        </div>

        <Field label="渠道名" hint="会出现在调用流水里，也是限定名的前半截（如 bailian/qwen3-max）。不能含 `/`——模型名那半截本来就可能带，两边都带就分不清界在哪">
          <input autoFocus value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field
          label="支持的上游协议"
          hint="这个上游能说的都勾上——同一个账号同时提供 CC 与 Responses 是常态，不必拆成两个渠道。选哪个由客户端打的端点决定：能透传就透传，说不了才转换"
        >
          <SegmentedMulti
            value={protos}
            onChange={setProtos}
            options={PROTOCOLS.map((p) => ({
              value: p,
              label: PROTOCOL_LABEL[p],
              hint: PROTOCOL_PATH[p],
            }))}
            soon={PROTOCOL_SOON}
          />
        </Field>
        {/* base_url 存的是「协议子路径之前」的前缀，上面那几个子路径由网关自己接。
            这是必踩的坑：填成 …/v1 会拼出 /v1/v1/chat/completions。 */}
        <Field
          label="Base URL"
          hint="填到协议子路径之前，网关自己接后缀。OpenAI 官方是 https://api.openai.com（不带 /v1），百炼是 https://dashscope.aliyuncs.com/compatible-mode"
        >
          <input value={baseURL} onChange={(e) => setBaseURL(e.target.value)} />
        </Field>
        {/* 露出选取模式（口径层 v0.38）：多凭证放开之后，「为什么总是第一把在跑」
            是必然被问的第一个问题，答案不该只藏在 SQL 里。 */}
        <Field
          label="凭证选取"
          hint="池子里有多把时按哪种顺序用。轮询把量摊开；随机适合上游按 key 限流、想避开固定节奏的场景"
        >
          <Segmented value={keyMode} options={KEY_MODE_OPTIONS} onChange={setKeyMode} />
        </Field>
        {!channel && (
          <Field label="上游凭证" hint="只写不回读：保存之后页面上再也看不到它。建完可以在「凭证池」里继续加">
            <input
              type="password"
              autoComplete="off"
              value={credential}
              onChange={(e) => setCredential(e.target.value)}
            />
          </Field>
        )}
        <label className="check">
          <input type="checkbox" checked={disabled} onChange={(e) => setDisabled(e.target.checked)} />
          停用这个渠道
        </label>
        <ErrorBar message={error} />
        <div className="form-actions">
          <button type="button" className="btn btn-quiet" onClick={onClose}>
            取消
          </button>
          <button className="btn btn-primary" disabled={busy || !name.trim()}>
            {busy ? '保存中…' : '保存'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}

/**
 * CredentialPool 是渠道凭证池的管理面（口径层 v0.38）。
 *
 * 逐条 CRUD，不是整把替换：值不回读 ⇒ 页面上没法把新贴进来的这堆与库里已有的对齐；
 * 而覆盖还会连带清掉已停用的凭证，那是 401 摘除的现场，是「这把为什么不转了」的唯一
 * 记录。列表里只有名字与状态——没有凭证值，也没有掩码。
 */
function CredentialPool({ channel, onClose }: { channel: Channel; onClose: () => void }) {
  const { data, error, reload, setError } = useList(() =>
    api.get<Credential[] | null>(`/channels/${channel.id}/credentials`),
  )
  const list = data ?? []

  async function mutate(fn: () => Promise<unknown>) {
    try {
      await fn()
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      return
    }
    await reload()
  }

  return (
    <Dialog title={`凭证池：${channel.name}`} onClose={onClose}>
      <div className="form">
        <div className="bar bar-warn">
          上游凭证只写不回读，服务端不会把它发回来，掩码也不做。认凭证靠下面这个名字——
          它会出现在调用流水与用量里，渠道内不能重名。
        </div>
        <ErrorBar message={error} />

        {list.length === 0 ? (
          <Empty>这个渠道还没有凭证。启用中的渠道没有可用凭证会连启动都过不去。</Empty>
        ) : (
          <div className="cred-list">
            {list.map((c) => (
              <CredentialRow key={c.id} cred={c} mutate={mutate} />
            ))}
          </div>
        )}

        <AddCredentials channelID={channel.id} mutate={mutate} />

        <div className="form-actions">
          <button type="button" className="btn btn-primary" onClick={onClose}>
            完成
          </button>
        </div>
      </div>
    </Dialog>
  )
}

/** CredentialRow 是池子里的一行：改名、停用/启用、删除。凭证值改不了也看不到。 */
function CredentialRow({
  cred,
  mutate,
}: {
  cred: Credential
  mutate: (fn: () => Promise<unknown>) => Promise<void>
}) {
  const [name, setName] = useState(cred.name)

  return (
    <div className={'cred' + (cred.disabled ? ' is-off' : '')}>
      <input
        className="cred-name"
        value={name}
        onChange={(e) => setName(e.target.value)}
        onBlur={() => {
          if (name.trim() && name !== cred.name) {
            void mutate(() => api.put(`/credentials/${cred.id}`, { name, disabled: cred.disabled }))
          }
        }}
      />
      <div className="cred-state">
        {cred.disabled ? (
          /* 摘除只人工恢复（口径层 v0.38），所以原因与时刻要一直摆着——它就是
             「这把为什么不转了」的唯一记录。 */
          <span className="tag tag-off" title={cred.disabled_at}>
            {cred.disabled_reason || '已停用'}
          </span>
        ) : (
          <span className="muted">{cred.created_at}</span>
        )}
      </div>
      <div className="row-actions">
        <Toggle
          on={!cred.disabled}
          label={cred.name}
          onChange={(on) =>
            void mutate(() => api.put(`/credentials/${cred.id}`, { name, disabled: !on }))
          }
        />
        <Confirm ghost onConfirm={() => void mutate(() => api.del(`/credentials/${cred.id}`))} />
      </div>
    </div>
  )
}

/**
 * AddCredentials 往池子里**追加**。
 *
 * 单条可以自己起名字；一次贴一批（一行一份）时名字由后端给 `凭证 N`——批量粘贴的
 * 场景里人手上只有一堆 key，逼他为每一行想个名字只会让这个入口没人用。
 */
function AddCredentials({
  channelID,
  mutate,
}: {
  channelID: number
  mutate: (fn: () => Promise<unknown>) => Promise<void>
}) {
  const [name, setName] = useState('')
  const [credential, setCredential] = useState('')
  const [bulk, setBulk] = useState('')
  const [batch, setBatch] = useState(false)

  const lines = bulk
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
  const ready = batch ? lines.length > 0 : credential.trim().length > 0

  return (
    <form
      className="form"
      onSubmit={(e) => {
        e.preventDefault()
        if (!ready) return
        const body = batch ? { credentials: bulk } : { name, credential }
        setName('')
        setCredential('')
        setBulk('')
        void mutate(() => api.post(`/channels/${channelID}/credentials`, body))
      }}
    >
      <Field
        label={batch ? '批量粘贴（一行一份，追加）' : '添加一份凭证'}
        hint={
          batch
            ? '只追加，不覆盖已有的——名字由网关给「凭证 N」，之后可以改'
            : '只追加，不影响池子里已有的几份；名字之后随时能改'
        }
      >
        {batch ? (
          <textarea rows={4} value={bulk} onChange={(e) => setBulk(e.target.value)} />
        ) : (
          /* 两行，key 在上：这一行是必填的那个，名字只是给它起个称呼，
             缺省时后端会给「凭证 N」。挤成一行会让两个宽度需求差很多的输入
             互相将就。 */
          <div className="cred-add">
            <input
              type="password"
              autoComplete="off"
              placeholder="上游 API key（必填）"
              value={credential}
              onChange={(e) => setCredential(e.target.value)}
            />
            <input
              placeholder="名字（可留空，默认「凭证 N」）"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
        )}
      </Field>
      <div className="form-actions">
        <button type="button" className="btn btn-quiet" onClick={() => setBatch(!batch)}>
          {batch ? '改为单条添加' : '批量粘贴'}
        </button>
        <button className="btn btn-primary" disabled={!ready}>
          {batch && lines.length > 1 ? `添加 ${lines.length} 份` : '添加'}
        </button>
      </div>
    </form>
  )
}
