import { useState } from 'react'
import { api, PROTOCOL_LABEL, PROTOCOL_PATH, PROTOCOL_SHORT } from '../../api'
import type {
  Channel,
  ChannelModel,
  ChannelProbe,
  ModelListResult,
  Protocol,
} from '../../api'
import { Card, Confirm, CopyCode, Toggle } from '../../ui'
import { Avatar, ChannelIcon, ModelIcon, vendorForModel } from '../../icons'
import { ChannelForm } from './form'
import { CredentialSection } from './credentials'
import { ModelPicker } from './picker'
import { ModelProbeGrid, ProbeRow } from './probe'

/**
 * ChannelDetail 是右栏：**选中的那一个渠道的全部**（口径层 v0.45）。
 *
 * 抬头只放身份与偶发动作（探测 / 拉列表 / 凭证池 / 删除），下面两段分别是上游设置
 * 与纳管模型。设置摆在模型前面是有意的：左栏已经承担了「在渠道之间跳」，右栏第一屏
 * 就该回答「这个渠道是什么」，而模型是一条会长到几十行的列表，让它开头等于把身份
 * 挤出视野。
 */
export function ChannelDetail({
  ch,
  probe,
  onProbe,
  fetched,
  onFetchModels,
  onCredentialsChanged,
  onDelete,
  onSaved,
  mutate,
}: {
  ch: Channel
  probe?: ChannelProbe | 'running'
  onProbe: () => void
  fetched?: ModelListResult[] | 'running'
  onFetchModels: () => void
  onCredentialsChanged: () => void
  onDelete: () => void
  onSaved: (id: number) => void
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
    <>
      <div className={'detail-head' + (ch.disabled ? ' is-off' : '')}>
        <ChannelIcon channel={ch} size={28} />
        <h2>{ch.name}</h2>
        {/* 协议集全列出来：这一格回答的是「这个渠道能接住哪些客户端」，
            只显示一个就看不出来 Responses 的 harness 会不会走转换。 */}
        {protos.map((p) => (
          <span key={p} className="tag" title={PROTOCOL_LABEL[p] + ' · ' + PROTOCOL_PATH[p]}>
            {PROTOCOL_SHORT[p] ?? p}
          </span>
        ))}
        {protos.length === 0 && <span className="tag tag-warn">协议集为空</span>}
        <span className="spacer" />
        {/* 启停是这一页最重的开关（停用 = 所有走它的请求立刻改道或失败），所以它在
            抬头最右，跟渠道名同一行——藏在下面表单的一个复选框里，改完还得点保存，
            那不是一个开关该有的手感。删除压到 ghost：它是不可逆的，但也是最少用的。 */}
        <Toggle
          on={!ch.disabled}
          label={ch.name}
          onChange={(on) =>
            void mutate(() =>
              api.put(`/channels/${ch.id}`, {
                name: ch.name,
                protocols: protos,
                base_url: ch.base_url,
                disabled: !on,
              }),
            )
          }
        />
        <Confirm ghost label="删除渠道" onConfirm={onDelete} />
      </div>

      {/* 可用凭证归零是渠道从能用变不能用的唯一运行期路径（摘光不设特例，口径层
          v0.38），而且启用渠道零凭证连启动闸都过不去，所以这条得显眼——不是抬头上
          一枚小标签的分量。 */}
      {ch.enabled_keys === 0 && (
        <div className="bar bar-warn">
          这个渠道没有可用凭证，所有走它的请求都会失败；启用状态下连启动闸都过不去。在下面「上游凭证」里加一份。
        </div>
      )}

      {/* 这里**不要**再挂 key={ch.id}：换渠道该重挂的是整个右栏，调用方已经在
          <ChannelDetail key={current.id}> 上挂了，这一层再挂一遍纯属重复。而且
          重复的代价是实打实的——在这一串「有的带 key、有的是 {cond && …}」的
          兄弟里再插一个 keyed 子节点，React 重挂它时会把新的 <section> 插进来
          却不删旧的，页面上就并排出现两份「渠道」表单（StrictMode 下必现）。 */}
      <ChannelForm channel={ch} onSaved={onSaved} />

      {/* 凭证与「检测」放在一起：子路径层探测逐把凭证探（v0.38），它答的正是
          「这几把 key 现在还活不活」——结论离被它评价的那份列表越近越好。 */}
      <CredentialSection
        channel={ch}
        onChanged={onCredentialsChanged}
        action={
          <button className="btn btn-quiet" onClick={onProbe} disabled={probe === 'running'}>
            {probe === 'running' ? '检测中…' : '检测'}
          </button>
        }
      >
        {/* 探测结论只提示，不挡任何操作，也不落库——它会过期（口径层 v0.33）。
            按凭证分行（v0.38）：同一个子路径对不同的号可以有不同结论，合成一条就把
            「哪一把不行」抹掉了。 */}
        {probe && probe !== 'running' && (
          <div className="detail-probes">
            {probe.credentials.map((g) => (
              <ProbeRow key={g.credential} group={g} multi={probe.credentials.length > 1} />
            ))}
          </div>
        )}
      </CredentialSection>

      <Card
        title={`模型${models.length > 0 ? ` · ${models.length}` : ''}`}
        action={
          /* 拉的是上游自己声明的模型列表，用来省掉「这个模型到底在哪一侧」的手工
             核对。结果只进表单不进路由（口径层 v0.40）。 */
          <button
            className="btn btn-quiet"
            onClick={onFetchModels}
            disabled={fetched === 'running'}
            title="拉上游 /v1/models，只用来帮你填表，不落库也不影响路由"
          >
            {fetched === 'running' ? '拉取中…' : '获取模型列表'}
          </button>
        }
      >
        {/* 模型矩阵（口径层 v0.43）：子路径存在不等于模型存在——聚合型中转的
            gpt-4o 未必在 Anthropic 那一侧列出，这一段答的就是那个差别。它评价的是
            下面这份列表，所以跟着列表走，不跟凭证那段走。 */}
        {probe && probe !== 'running' && (probe.models?.length ?? 0) > 0 && (
          <ModelProbeGrid rows={probe.models!} credential={probe.model_credential} />
        )}
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
      </Card>
    </>
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
