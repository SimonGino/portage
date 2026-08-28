import { useEffect, useState } from 'react'
import { api, KEY_MODE_OPTIONS } from '../../api'
import type { Channel, Credential, KeyMode } from '../../api'
import { Confirm, DetailBlock, Dialog, Empty, ErrorBar, Field, SecretValue, Toggle, useList } from '../../ui'
import { IconEye, IconEyeOff, IconKey, IconPulse, IconRows } from '../../icons/acts'
import { Segmented } from '../../fields'
import { ProbeDialog } from './probe'

/**
 * CredentialBlock 是渠道凭证在模型页上的区块（PO 2026-08-20 裁决从「上游设置」井
 * 提出来，布局参照 Cherry Studio 的服务商页，视觉与命名不跟；2026-08-28 裁决回到
 * 常驻展开——「管理」「检测」要一眼可见一击可点，折一层就是多一步，推翻 08-26 的默认收起）。
 *
 * 页面上只摆**正在被用的那一把**（掩码 + 显示 / 复制）和「管理」「检测」两颗按钮；
 * 池子的逐条 CRUD、选取模式、停用现场都在「管理」弹框里。检测点开检测弹层
 * （口径层 v0.96 ③）：这里的入口预选当前在用的那把；「管理」弹框每行的检测预选
 * 那一把（含已停用）。两处开的是同一个弹层。
 */
export function CredentialBlock({
  channel,
  onChanged,
}: {
  channel: Channel
  /** 池子变了就通知外面重拉渠道——「缺凭证」那个标记挂在渠道对象上。 */
  onChanged: () => void
}) {
  const { data, error, reload, setError } = useList(() =>
    api.get<Credential[] | null>(`/channels/${channel.id}/credentials`),
  )
  const list = data ?? []
  const [managing, setManaging] = useState(false)
  // 检测弹层预选的那把凭证；null = 没开弹层。从「管理」进来时把管理框关掉——
  // 两个弹框叠着，Esc 会把两层一起带走。
  const [probing, setProbing] = useState<Credential | null>(null)
  const enabled = list.filter((c) => !c.disabled)
  // 「正在被用的那一把」按池子顺序取第一把启用的。轮询/随机模式下这只是代表——
  // 完整的池子在弹框里，行尾的计数提醒着「不止这一把」。
  const primary = enabled[0] ?? null

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

  return (
    <DetailBlock
      title={<>上游凭证{list.length > 0 && ` · ${list.length}`}</>}
      /* 「管理」「检测」上收标题行（v0.38）：它们是**块级**动作——管的是整个池子，
         测的是渠道，不是行里那串掩码的从属物；与「API 地址」的「编辑」同位同形，
         值那一行只剩值和贴着它的显示 / 复制微按钮。 */
      action={
        list.length > 0 && (
          <>
            <button type="button" className="act" onClick={() => setManaging(true)}>
              <IconKey />
              管理
            </button>
            <button
              type="button"
              className="act"
              onClick={() => setProbing(primary)}
              disabled={!primary}
              title="用这把凭证给选中的模型发带模型名的最小真实请求；只提示，不落库也不影响路由"
            >
              <IconPulse />
              检测
            </button>
          </>
        )
      }
    >
      <ErrorBar message={error} />
      {list.length === 0 ? (
        /* 空态直接摆添加行：贴一份 key 是此刻唯一要做的事，不必先进弹框。 */
        <AddCredentials channelID={channel.id} mutate={mutate} />
      ) : (
        <div className="cred-inline">
          {primary ? (
            <SecretValue value={primary.credential} />
          ) : (
            <span className="muted">全部已停用——进「管理」恢复或加一份新的</span>
          )}
          {enabled.length > 1 && (
            <span className="muted cred-inline-count">共 {enabled.length} 把在轮</span>
          )}
        </div>
      )}
      {managing && (
        <CredentialsDialog
          channel={channel}
          list={list}
          mutate={mutate}
          onClose={() => setManaging(false)}
          onProbe={(c) => {
            setManaging(false)
            setProbing(c)
          }}
        />
      )}
      {probing && (
        <ProbeDialog
          channel={channel}
          credentials={list}
          initial={probing}
          onClose={() => setProbing(null)}
        />
      )}
    </DetailBlock>
  )
}

/**
 * CredentialsDialog 是凭证池的管理弹框（口径层 v0.38 的池子语义原样搬进来）。
 *
 * 逐条 CRUD，不是整把替换：覆盖会连带清掉已停用的凭证，连同停用原因与时刻——
 * 「这把为什么不转了」的唯一记录。列表里有名字、**值**（v0.47 起可回读，默认掩码）、
 * 状态与时间。段名仍是「上游凭证」不是「API 密钥」（v0.48）：导航上那个「API Key」
 * 指的是网关下发给客户端的 key，两个词摆在同一个界面里同形不同义。
 */
function CredentialsDialog({
  channel,
  list,
  mutate,
  onClose,
  onProbe,
}: {
  channel: Channel
  list: Credential[]
  mutate: (fn: () => Promise<unknown>) => Promise<void>
  onClose: () => void
  /** 这一行的检测：关掉管理框、开检测弹层并预选这把（含已停用的）。 */
  onProbe: (c: Credential) => void
}) {
  const [keyMode, setKeyMode] = useState<KeyMode>(channel.key_mode ?? 'polling')
  const enabled = list.filter((c) => !c.disabled).length

  /** 改选取模式立即落库，走单字段的意图写（#48 批2），别的列碰不到。 */
  function saveKeyMode(mode: KeyMode) {
    const prev = keyMode
    setKeyMode(mode)
    void mutate(() =>
      api.put(`/channels/${channel.id}/key-mode`, { key_mode: mode }).catch((e) => {
        setKeyMode(prev) // 没写成就把单选钮拨回去，别让页面撒谎
        throw e
      }),
    )
  }

  return (
    <Dialog title="上游凭证" wide onClose={onClose}>
      <div className="cred-dialog">
        <p className="muted">名字是归因依据，它会出现在流水与用量里。换 key 就是加一份新的、把旧的停掉。</p>

        {list.length === 0 ? (
          <Empty>这个渠道还没有凭证。启用中的渠道没有可用凭证会连启动都过不去。</Empty>
        ) : (
          <div className="cred-list">
            {list.map((c) => (
              <CredentialRow
                key={c.id}
                cred={c}
                channel={channel}
                enabledCount={enabled}
                mutate={mutate}
                onProbe={() => onProbe(c)}
              />
            ))}
          </div>
        )}

        <AddCredentials channelID={channel.id} mutate={mutate} />

        {/* 选取模式只在 ≥2 把（含停用）时出现（v0.44 修订 v0.38 ⑨ 的位置）：它描述
            的是「多把 key 之间怎么轮」，单 key 渠道从头到尾不该看到这个概念；含停用
            是因为 1 启用 + 1 停用时人马上要恢复第二把，模式马上就有意义。 */}
        {list.length >= 2 && (
          <Field
            label="凭证选取"
            hint="按哪种顺序用池子里的 key。轮询把量摊开；随机适合上游按 key 限流、想避开固定节奏的场景。改了立即生效"
          >
            <Segmented value={keyMode} options={KEY_MODE_OPTIONS} onChange={saveKeyMode} />
          </Field>
        )}

        {list.length > 0 && (
          <div className="muted cred-dialog-foot">
            {enabled} / {list.length} 已启用
          </div>
        )}
      </div>
    </Dialog>
  )
}

/** CredentialRow 是池子里的一行：值、改名、检测、停用/启用、删除。值能看能复制
 *  （v0.47），但改不了——换 key 就是加一份新的、把旧的停掉，那样停用的现场还留着。 */
function CredentialRow({
  cred,
  channel,
  enabledCount,
  mutate,
  onProbe,
}: {
  cred: Credential
  channel: Channel
  /** 池子里启用凭证的总数——判断「这是最后一把」要看全池，不是看这一行。 */
  enabledCount: number
  mutate: (fn: () => Promise<unknown>) => Promise<void>
  onProbe: () => void
}) {
  const [name, setName] = useState(cred.name)
  // 停用最后一把的举起态：同 Confirm 的两击，3 秒不按第二下自动放下。
  const [armedOff, setArmedOff] = useState(false)

  useEffect(() => {
    if (!armedOff) return
    const t = setTimeout(() => setArmedOff(false), 3000)
    return () => clearTimeout(t)
  }, [armedOff])

  // 这是启用渠道的最后一把启用凭证：删掉或停掉它都会撞上「能保存的配置一定能启动」
  // 的写后校验（启用渠道零可用凭证过不去）。失效的 key 必须删得掉（PO 2026-08-28
  // 裁决），出路是把后果摆进确认文案、确认后先停用渠道再动凭证——校验不放宽，
  // 提示给足，但不拦人。
  const cascade = !cred.disabled && enabledCount === 1 && !channel.disabled

  /** 先停渠道再动凭证：顺序反过来第一笔就被校验打回。两笔各自成事务，第二笔
   *  失败时渠道已停——状态照实摆在页面上，不藏。 */
  async function withChannelOff(fn: () => Promise<unknown>) {
    await api.put(`/channels/${channel.id}/disabled`, { disabled: true })
    await fn()
  }

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
          /* 停用原因与时刻要一直摆着——它就是「这把为什么不转了」的唯一记录。 */
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
        {/* 停用的也给检测（口径层 v0.96 承接 v0.38）：恢复是纯人工的，「这把还
            坏不坏」除了发一次请求没有别的办法回答。 */}
        <button
          type="button"
          className="act"
          onClick={onProbe}
          title="用这把凭证检测；只提示，不落库也不影响路由"
        >
          <IconPulse />
          检测
        </button>
        {armedOff ? (
          <button
            type="button"
            className="btn btn-danger"
            onClick={() => {
              setArmedOff(false)
              void mutate(() =>
                withChannelOff(() => api.put(`/credentials/${cred.id}`, { name, disabled: true })),
              )
            }}
          >
            连渠道一起停用？
          </button>
        ) : (
          <Toggle
            on={!cred.disabled}
            label={cred.name}
            onChange={(on) => {
              if (!on && cascade) {
                setArmedOff(true)
                return
              }
              void mutate(() => api.put(`/credentials/${cred.id}`, { name, disabled: !on }))
            }}
          />
        )}
        <Confirm
          ghost
          confirm={cascade ? '删除并停用渠道？' : undefined}
          onConfirm={() =>
            void mutate(() =>
              cascade
                ? withChannelOff(() => api.del(`/credentials/${cred.id}`))
                : api.del(`/credentials/${cred.id}`),
            )
          }
        />
      </div>
    </div>
  )
}

/**
 * AddCredentials 往池子里**追加**。
 *
 * 起名字不在这里：单条与批量都由后端给 `凭证 N`，加完在管理弹框的列表里就地改名。
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
            className="act-icon"
            onClick={() => setReveal((v) => !v)}
            disabled={credential === ''}
            aria-label={reveal ? '隐藏' : '显示'}
            title={reveal ? '藏起来' : '看一眼刚粘进来的这串'}
          >
            {reveal ? <IconEyeOff /> : <IconEye />}
          </button>
          <button className="btn btn-quiet" disabled={!ready} title="只追加，不覆盖池子里已有的">
            添加
          </button>
        </div>
      )}
      {/* 批量粘贴是次要入口，一句安静的小字：单条那一行才是常走的路。批量模式下
          textarea 装不下按钮，「添加 N 份」才回到这一行来。 */}
      <div className="cred-add-alt">
        <button type="button" className="act" onClick={() => setBatch(!batch)}>
          <IconRows />
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
