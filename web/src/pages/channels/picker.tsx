import { useEffect, useMemo, useState } from 'react'
import { api, PROTOCOL_LABEL, PROTOCOL_SHORT } from '../../api'
import type { Channel, ModelListResult, Protocol } from '../../api'
import { Dialog, Empty } from '../../ui'
import { ModelIcon } from '../../icons'

/**
 * familyOf 给模型名归族，用来在挑选面板里分组。
 *
 * 两条规则够用：带斜杠的取斜杠前那段（`kimi/kimi-k3` → `kimi`，中转站转售别家模型
 * 时的惯例写法），否则取开头那串字母（`qwen-image-3.0-pro`、`qwen3.8-max` 都 → `qwen`）。
 * 特意在字母处断而不是在第一个 `-` 处断：后者会把 `qwen3.7-flash` 和 `qwen3.8-max`
 * 拆成两族，而人扫这份列表时想的是「通义那一堆」。
 */
export function familyOf(name: string): string {
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
 * ModelPicker 是「获取并挑选模型」的弹框（口径层 v0.40）：开框即拉上游
 * /v1/models，拉到之后就地挑选、逐个落库。
 *
 * 收成一步是模仿 cherry-studio 那套——点一个入口、开框、框内拉取→列表→勾选。
 * 此前是「先点获取拿到一份列表、再点挑要纳管的打开同一个框」两步，中间那条
 * 结论条只起到「告诉你拉到几个」的作用，而弹框里的 mpick-note 自己也写着同一句，
 * 重复摆一遍。
 *
 * 它仍然只是**填表助手**：这里的一切都还没进库，勾完点「添加」才逐个落库。所以面板里
 * 不出现「同步」，也没有「按上游对齐」这种动作——以一份可能写死的上游列表为准，正是
 * §2.2 拒绝把探测做成闸的那条立论。已纳管而上游列表里没有的，这儿一个字都不提，
 * 更不会去删：那件事由模型格子里那句「上游列表里没有它」提示，删不删是人的决定。
 *
 * 批量不是罪，无差别才是：全选按钮只作用于**当前可见**的那些（搜索词 + 未纳管筛选
 * 之后剩下的），所以「选中 5 个」永远是人先划定了范围才发生的事。
 */
export function ModelPicker({
  channel,
  existing,
  onClose,
  onAdd,
  /** 初值：开框前若已拉过就喂进来，省一次重复请求（裁决 1A——保留 fetched state）。 */
  initial,
  /**
   * 拉取成功后回吐结果。调用方（Channels 页）把它写回自己的 fetched state，
   * 这样模型格子里的「上游只在 X 侧列出 · 采纳」建议才能拿到框内重拉的最新一份
   * （裁决 1A——那两条存量提示是 v0.40 刻意做的能力，不砍）。
   */
  onResults,
}: {
  channel: Channel
  existing: Set<string>
  onClose: () => void
  onAdd: (names: string[]) => Promise<void>
  initial?: ModelListResult[]
  onResults?: (results: ModelListResult[]) => void
}) {
  // 拉取态三档：'running' | 'failed' | 成果数组。开框自动拉一次（cherry-studio 那套），
  // 拉到的结果留在框里不动；重拉走 refresh()。复用 calling 方传进来的 initial：
  // 已拉过就别再打一次网——人打开框看的就是上一份，不是再等一遍 loading。
  const [results, setResults] = useState<ModelListResult[] | null>(initial ?? null)
  const [state, setState] = useState<'running' | 'failed' | 'done'>(
    initial ? 'done' : 'running',
  )

  async function refresh() {
    setState('running')
    try {
      const r = await api.post<{ results: ModelListResult[] }>(`/channels/${channel.id}/fetch-models`)
      setResults(r.results)
      setState('done')
      onResults?.(r.results)
    } catch {
      // 拉不到不算错误：上游没有 /v1/models 是常事，手工填就是了。
      setState('failed')
    }
  }

  // 开框自动拉。initial 给了就跳过——那是别处已拉到的成果，再发一次只是多花钱等 loading。
  useEffect(() => {
    if (!initial) void refresh()
    // 只在挂载时跑一次：refresh 自己闭包着 channel.id，重拉由框里那颗按钮显式调。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const [query, setQuery] = useState('')
  const [showManaged, setShowManaged] = useState(false)
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [busy, setBusy] = useState(false)

  // 每个名字被哪几侧列出来了。同一个模型可能在多份结果里出现（渠道支持多协议时
  // 是逐侧拉的），协议要去重。
  const sides = useMemo(() => {
    const m = new Map<string, Protocol[]>()
    for (const r of results ?? []) {
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
  // 第一组无条件默认展开（PO 裁定）：分组时特意把最大的族排最前——「真正要找的
  // 多半在最大那族里」——再把它收起来，等于把最可能的答案藏在第一次点击后面。
  const isOpen = (g: { name: string; items: string[] }, index: number) =>
    q !== '' || (expanded[g.name] ?? (index === 0 || g.items.length <= COLLAPSE_AT))

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
      title={`获取并挑选模型：${channel.name}`}
      onClose={onClose}
      wide
      guard={picked.size > 0}
    >
      <div className="mpick">
        {/* 先把这份列表的性质说清：它不是配置，勾了才是。拉取还在跑/拉失败时
            这两句改成状态文案，不重复在下面再开一条结论条（收成一步的代价是
            原先那条内联结论条 FetchedModels 被删了，它说的话并到这里）。 */}
        <div className="mpick-note muted">
          {state === 'running' ? (
            '拉取中…朝勾选的协议侧各请求一次上游 /v1/models。'
          ) : state === 'failed' ? (
            '拉取失败——上游没有 /v1/models 是常事，可以关掉手填，或重试。'
          ) : total === 0 ? (
            '上游没列出任何模型。可以关掉手填，或重试。'
          ) : (
            <>
              上游列出 {total} 个，其中 {managed} 个已纳管。这份列表只用来帮你填表，不落库、不影响路由——
              中转站把没有的模型也写进列表是常事，勾之前最好确认它真的能调通。
            </>
          )}
        </div>

        <div className="mpick-bar">
          {/* 拉取中/失败时搜索与全选都没有东西可作用，但重拉是有意义的动作，
              所以重拉按钮独立放在搜索框右侧，而搜索框在 loading 时禁用而不是隐藏——
              隐藏会让框在拉完那一刻突然跳出来。 */}
          <input
            autoFocus
            className="mpick-search"
            placeholder="搜模型名，比如 qwen3.7 或 embedding"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            disabled={state !== 'done' || total === 0}
          />
          <button
            type="button"
            className={'btn btn-quiet' + (showManaged ? ' is-on' : '')}
            onClick={() => setShowManaged((v) => !v)}
            title="已纳管的也显示出来（灰着、勾不动），用来核对哪些已经加过了"
            disabled={state !== 'done' || total === 0}
          >
            显示已纳管
          </button>
          <span className="spacer" />
          {/* 重拉：上游列表会变（中转站随时加/减模型），开框自动拉的那一份不是权威。 */}
          <button
            type="button"
            className="btn btn-quiet"
            onClick={() => void refresh()}
            disabled={state === 'running'}
            title="重新朝上游拉一次 /v1/models"
          >
            {state === 'running' ? '拉取中…' : '重新拉取'}
          </button>
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
          {/* 拉取中不渲染 Empty：后者会闪一下「都已经纳管了」再被真实列表盖掉。 */}
          {state === 'running' && <div className="muted">拉取中…</div>}
          {state === 'failed' && (
            <div className="muted">
              拉取失败。可以「重新拉取」重试，或关掉手填。
            </div>
          )}
          {state === 'done' && visible.length === 0 && (
            <Empty>
              {q ? `没有匹配「${query}」的模型。` : '上游列出的都已经纳管了。'}
            </Empty>
          )}
          {state === 'done' &&
            groups.map((g, gi) => {
              const open = isOpen(g, gi)
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
