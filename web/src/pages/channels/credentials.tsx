import { useState } from 'react'
import type { ReactNode } from 'react'
import { api, KEY_MODE_OPTIONS } from '../../api'
import type { Channel, Credential, KeyMode } from '../../api'
import { Confirm, Empty, ErrorBar, Field, SecretValue, Toggle, useList } from '../../ui'
import { Segmented } from '../../fields'

/**
 * CredentialSection 是渠道凭证池（口径层 v0.38），长在右栏里的一段（v0.46 由弹框
 * 改为常驻 section）。
 *
 * 逐条 CRUD，不是整把替换：覆盖会连带清掉已停用的凭证，那是 401 摘除的现场，是
 * 「这把为什么不转了」的唯一记录。列表里有名字、**值**（v0.47 起可回读，默认掩码）、
 * 状态与时间。
 */
export function CredentialSection({
  channel,
  onChanged,
  action,
  children,
}: {
  channel: Channel
  /** 池子变了就通知外面重拉渠道——「缺凭证」那个标记挂在渠道对象上。 */
  onChanged: () => void
  /** 段标题右边那颗按钮（「检测」），由右栏传进来——探测状态是整页共享的。 */
  action?: ReactNode
  /** 探测结论条，紧跟在这一段里显示：它答的正是「这几把 key 现在还活不活」。 */
  children?: ReactNode
}) {
  const { data, error, reload, setError } = useList(() =>
    api.get<Credential[] | null>(`/channels/${channel.id}/credentials`),
  )
  const list = data ?? []
  const [keyMode, setKeyMode] = useState<KeyMode>(channel.key_mode ?? 'polling')

  async function mutate(fn: () => Promise<unknown>) {
    try {
      await fn()
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      return
    }
    await reload()
    onChanged()
  }

  /** 改选取模式立即落库。渠道的修改接口是整体覆盖，其余字段原样回传。 */
  function saveKeyMode(mode: KeyMode) {
    const prev = keyMode
    setKeyMode(mode)
    void mutate(() =>
      api
        .put(`/channels/${channel.id}`, {
          name: channel.name,
          protocols: channel.protocols ?? [],
          base_url: channel.base_url,
          key_mode: mode,
          disabled: channel.disabled,
        })
        .catch((e) => {
          setKeyMode(prev) // 没写成就把单选钮拨回去，别让页面撒谎
          throw e
        }),
    )
  }

  return (
    <section className="section cred-section">
      <header className="section-head">
        {/* 段名是「上游凭证」不是「API 密钥」（v0.48）：导航上那个「API Key」指的是
            网关下发给客户端的 key，两个词摆在同一个界面里同形不同义。 */}
        <h2>上游凭证{list.length > 0 && ` · ${list.length}`}</h2>
        {action}
      </header>
      <p className="muted">值默认掩码，点「显示」看明文、点「复制」拿全串。名字是归因依据，它会出现在流水与用量里。</p>
      <ErrorBar message={error} />
      {children}

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

      {/* 选取模式只在 ≥2 把（含停用）时出现（v0.44 修订 v0.38 ⑨ 的位置）：它描述
          的是「多把 key 之间怎么轮」，单 key 渠道从头到尾不该看到这个概念；含停用
          是因为 1 启用 + 1 停用时人马上要恢复第二把，模式马上就有意义。原裁决
          「露出来」的立论（多凭证后「为什么总是第一把在跑」必然被问）在 key 列表
          旁边成立得更彻底。 */}
      {list.length >= 2 && (
        <Field
          label="凭证选取"
          hint="按哪种顺序用池子里的 key。轮询把量摊开；随机适合上游按 key 限流、想避开固定节奏的场景。改了立即生效"
        >
          <Segmented value={keyMode} options={KEY_MODE_OPTIONS} onChange={saveKeyMode} />
        </Field>
      )}
    </section>
  )
}

/** CredentialRow 是池子里的一行：值、改名、停用/启用、删除。值能看能复制（v0.47），
 *  但改不了——换 key 就是加一份新的、把旧的停掉，那样 401 摘除的现场还留着。 */
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
      {/* 已有的凭证是一条**记录**，不是一张表单。名字仍可改（就地改，失焦即存），
          但平时不画边框——三个同等粗细的框叠在一起时，「库里已经有这么一把」与
          「你正在贴一把新的」看着一模一样。data-1p-ignore 是给密码管理器看的：
          这一段里唯一像登录框的是下面那个 key 输入，1Password 会顺手把图标塞进
          它旁边的任意输入框里。 */}
      <input
        className="cred-name ghost-input"
        data-1p-ignore
        value={name}
        onChange={(e) => setName(e.target.value)}
        onBlur={() => {
          if (name.trim() && name !== cred.name) {
            void mutate(() => api.put(`/credentials/${cred.id}`, { name, disabled: cred.disabled }))
          }
        }}
      />
      <SecretValue value={cred.credential} />
      <div className="cred-state">
        {cred.disabled ? (
          /* 摘除只人工恢复（口径层 v0.38），所以原因与时刻要一直摆着——它就是
             「这把为什么不转了」的唯一记录。 */
          <span className="tag tag-off" title={cred.disabled_at}>
            {cred.disabled_reason || '已停用'}
          </span>
        ) : (
          /* 只到天，完整时刻进 title：`2026-08-10T06:33:27Z` 摆在行里像调试输出，
             而「这把是什么时候加的」精确到天就够用了。 */
          <span className="muted" title={cred.created_at}>
            {cred.created_at.slice(0, 10)}
          </span>
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
 * 起名字不在这里：单条与批量都由后端给 `凭证 N`，加完在上面那份列表里就地改名。
 * 名字框曾经并排在 key 旁边，代价是这一段多一个同等分量的框、而它只在少数时候被填
 * ——而改名这件事列表里本来就能做，只是换个时机。
 */
function AddCredentials({
  channelID,
  mutate,
}: {
  channelID: number
  mutate: (fn: () => Promise<unknown>) => Promise<void>
}) {
  const [credential, setCredential] = useState('')
  const [bulk, setBulk] = useState('')
  const [batch, setBatch] = useState(false)
  const [reveal, setReveal] = useState(false)

  const lines = bulk
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
  const ready = batch ? lines.length > 0 : credential.trim().length > 0

  return (
    <form
      className="form cred-add-form"
      onSubmit={(e) => {
        e.preventDefault()
        if (!ready) return
        const body = batch ? { credentials: bulk } : { credential }
        setCredential('')
        setBulk('')
        setReveal(false)
        void mutate(() => api.post(`/channels/${channelID}/credentials`, body))
      }}
    >
      {/* 单条模式就是一行：输入框 + 「显示」 + 「添加」。按钮跟着它要提交的那个框走，
          不再另起一条右对齐的按钮行——那条行把「添加」推到离输入框一整行远的地方，
          右边缘还跟上面的凭证列表对不齐。 */}
      {batch ? (
        <Field label="批量粘贴（一行一份，只追加）">
          <textarea rows={3} value={bulk} onChange={(e) => setBulk(e.target.value)} />
        </Field>
      ) : (
        <div className="cred-add">
          {/* 「显示」只作用于**这次正在打的这一串**，跟「只写不回读」不冲突：回读说的
              是库里那些，而这一串还在输入框里、还没提交。粘完看一眼有没有把前后引号
              或者换行一起粘进来，是最省事的一次自检——粘错的表现是保存成功但全部
              请求 401。 */}
          <input
            className="cred-add-key"
            type={reveal ? 'text' : 'password'}
            autoComplete="off"
            data-1p-ignore
            placeholder="上游 API key"
            value={credential}
            onChange={(e) => setCredential(e.target.value)}
          />
          <button
            type="button"
            className="btn btn-ghost"
            onClick={() => setReveal((v) => !v)}
            disabled={credential === ''}
            title={reveal ? '藏起来' : '看一眼刚粘进来的这串'}
          >
            {reveal ? '隐藏' : '显示'}
          </button>
          <button className="btn btn-quiet" disabled={!ready} title="只追加，不覆盖池子里已有的">
            添加
          </button>
        </div>
      )}
      {/* 批量粘贴是次要入口，一句安静的小字：单条那一行才是常走的路。批量模式下
          textarea 装不下按钮，「添加 N 份」才回到这一行来。 */}
      <div className="cred-add-alt">
        <button type="button" className="btn btn-ghost" onClick={() => setBatch(!batch)}>
          {batch ? '改为单条' : '批量粘贴'}
        </button>
        {batch && (
          <button className="btn btn-quiet" disabled={!ready} title="只追加，不覆盖池子里已有的">
            {lines.length > 1 ? `添加 ${lines.length} 份` : '添加'}
          </button>
        )}
      </div>
    </form>
  )
}
