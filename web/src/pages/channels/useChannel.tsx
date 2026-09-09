import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { useParams } from 'react-router-dom'
import { api } from '../../api'
import type { Channel, Credential, ModelListResult } from '../../api'
import { sortChannels } from './derive'

/**
 * 模型页的状态收在这一个 store 里（#56，架构评审卡 8）。
 *
 * 此前 fetch / mutate / refresh 三件套在 Channels、CredentialBlock 各手抄一份，
 * 同一渠道两份缓存（渠道行上的 enabled_keys 与凭证池本身）靠 onChanged 手工对齐，
 * mutate 与 fetched 这些又以 prop 钻两层到叶子——detail / form / probe / credentials
 * 在近 60 次提交里 6 次一起动的原因。现在：清单、当前渠道的凭证池、拉到的上游模型
 * 列表都在 provider 里，**一把 mutate** 写完同时重拉清单与凭证池，叶子用
 * useChannel(id) 直接拿，不再钻 prop。
 */

/** 跑一笔写。成了重拉，回 true；败了把后端那句话贴到页面顶上的错误条，回 false。
 *  挑选面板据回值决定关不关框，别的调用方不看。 */
export type Mutate = (fn: () => Promise<unknown>) => Promise<boolean>

interface Store {
  /** 已按左栏顺序排好（停用沉底、组内按 id）。 */
  channels: Channel[]
  loading: boolean
  error: string
  /** 路由说的是「新建」（/channels/new）。 */
  creating: boolean
  /** 右栏正在看的渠道：路由里的 id，没有或对不上就取清单第一个；新建态为 null。 */
  current: Channel | null
  reload: () => Promise<void>
  mutate: Mutate
  /** 当前渠道的凭证池，带着它属于哪个渠道；换渠道时先清空再拉。 */
  credentials: { id: number; list: Credential[] }
  credentialsError: string
  /**
   * 上游拉到的模型列表，按渠道 id 存（裁决 1A——保留 fetched state）。只进内存、
   * 刷新即失（口径层 v0.40），供模型格子里的 listedOn / listComplete 建议位用。
   */
  fetched: Record<number, ModelListResult[]>
  setFetched: (id: number, results: ModelListResult[]) => void
}

const Ctx = createContext<Store | null>(null)

/**
 * ChannelsProvider 挂在模型页根上。「当前渠道」由路由参数 + 清单推出来，凭证池只拉
 * 这一个（其它渠道的池子等切过去再拉）。
 */
export function ChannelsProvider({ children }: { children: ReactNode }) {
  const { id: param } = useParams()
  const [raw, setRaw] = useState<Channel[] | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [creds, setCreds] = useState<{ id: number; list: Credential[] }>({ id: 0, list: [] })
  const [credentialsError, setCredentialsError] = useState('')
  const [fetched, setFetchedMap] = useState<Record<number, ModelListResult[]>>({})

  const reloadChannels = useCallback(async () => {
    setLoading(true)
    try {
      setRaw((await api.get<Channel[] | null>('/channels')) ?? [])
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [])

  const reloadCredentials = useCallback(async (id: number | null) => {
    if (id === null) {
      setCreds({ id: 0, list: [] })
      setCredentialsError('')
      return
    }
    try {
      const list = (await api.get<Credential[] | null>(`/channels/${id}/credentials`)) ?? []
      setCreds({ id, list })
      setCredentialsError('')
    } catch (e) {
      setCreds({ id, list: [] })
      setCredentialsError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  useEffect(() => {
    void reloadChannels()
  }, [reloadChannels])

  const setFetched = useCallback((id: number, results: ModelListResult[]) => {
    setFetchedMap((p) => ({ ...p, [id]: results }))
  }, [])

  const channels = useMemo(() => sortChannels(raw ?? []), [raw])
  const creating = param === 'new'
  const current = creating ? null : (channels.find((c) => String(c.id) === param) ?? channels[0] ?? null)
  const currentID = current?.id ?? null

  useEffect(() => {
    void reloadCredentials(currentID)
  }, [currentID, reloadCredentials])

  const reload = useCallback(async () => {
    await Promise.all([reloadChannels(), reloadCredentials(currentID)])
  }, [reloadChannels, reloadCredentials, currentID])

  // 一把 mutate 管所有写：不分「这笔动的是凭证还是模型」——凭证的增删改会动渠道行上的
  // enabled_keys，停用渠道那笔又会连着凭证页的连带提示；分开重拉正是两份缓存对不齐
  // 的来处。多一次 GET 换一处对齐，单人网关上不是代价。
  const mutate = useCallback<Mutate>(
    async (fn) => {
      try {
        await fn()
        setError('')
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
        return false
      }
      await reload()
      return true
    },
    [reload],
  )

  const value = useMemo<Store>(
    () => ({
      channels,
      loading: loading && raw === null,
      error,
      creating,
      current,
      reload,
      mutate,
      credentials: creds.id === currentID ? creds : { id: currentID ?? 0, list: [] },
      credentialsError: creds.id === currentID ? credentialsError : '',
      fetched,
      setFetched,
    }),
    [channels, loading, raw, error, creating, current, reload, mutate, creds, currentID, credentialsError, fetched, setFetched],
  )
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

/** 整页的清单与错误条：只有 Channels 页根用。 */
export function useChannels(): Store {
  const s = useContext(Ctx)
  if (!s) throw new Error('useChannels 只能在 ChannelsProvider 里用')
  return s
}

/**
 * 某一个渠道的视图：右栏里的每一个组件都从这里拿，不再从 props 接 mutate。
 * `ch` 为 undefined 表示清单里没有这个 id（刚删掉、或还没拉到）。
 */
export function useChannel(id: number) {
  const s = useChannels()
  const ch = s.channels.find((c) => c.id === id)
  const listed = s.fetched[id] ?? null
  const setFetched = useCallback((r: ModelListResult[]) => s.setFetched(id, r), [s, id])
  const mine = s.credentials.id === id
  return {
    ch,
    mutate: s.mutate,
    reload: s.reload,
    /** 这个渠道的凭证池；不是当前渠道时为空（池子只拉当前那一个）。 */
    credentials: mine ? s.credentials.list : [],
    credentialsError: mine ? s.credentialsError : '',
    /** 拉到的上游列表；null = 还没拉过。 */
    listed,
    setFetched,
  }
}
