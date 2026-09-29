// 新版检测（口径层 v1.38 ⑤，展开层 §7.11「前端」）：浏览器直接问 GitHub 的 releases/latest，
// 服务端不轮询、不 phone home。结果在 localStorage 缓存一天——未鉴权 60 次/时/IP，
// 每开一次面板问一次会把同一出口 IP 后面的几台机器一起打进限额。

export const REPO = 'https://github.com/SimonGino/portage'
const API = 'https://api.github.com/repos/SimonGino/portage/releases/latest'
const KEY = 'portage.latest-release'
const TTL = 24 * 60 * 60 * 1000

/** 缓存里的一份检测结果。tag 是 GitHub 原样的 `v0.5.0`，比较时去 v。 */
export interface Latest {
  tag: string
  /** published_at 原样（ISO），弹框里只显示日期部分。 */
  published: string
  checkedAt: number
}

// 发版号只认纯 x.y.z（PO 2026-09-29：「就按照 0.1.1 这种来」）：带 -rc、-SNAPSHOT 后缀的与
// dev、test 一样算非发版构建，不查更新。
function parse(v: string): number[] | null {
  const m = /^v?(\d+)\.(\d+)\.(\d+)$/.exec(v)
  return m ? [+m[1], +m[2], +m[3]] : null
}

/** isRelease：纯 x.y.z 才算发版构建；`dev`、workflow_dispatch 出的 `test`、快照都不是，不查更新。 */
export function isRelease(v: string | undefined): v is string {
  return !!v && parse(v) !== null
}

/**
 * newer(a, b)：a 是否比 b 新。**不是字符串相等**——本地比远端还新（刚发完版、GitHub 缓存
 * 还没翻）也算没有新版，否则会提示「升级」到一个旧版本。解析不了的一律 false。
 */
export function newer(a: string, b: string): boolean {
  const x = parse(a)
  const y = parse(b)
  if (!x || !y) return false
  for (let i = 0; i < 3; i++) {
    if (x[i] !== y[i]) return x[i] > y[i]
  }
  return false
}

// localStorage 在隐私窗口、禁站点数据的浏览器里会整个抛异常：读写全包 try/catch，
// 拿不到就当没缓存——多问一次 GitHub 而已。
function readCache(): Latest | null {
  try {
    const raw = localStorage.getItem(KEY)
    return raw ? (JSON.parse(raw) as Latest) : null
  } catch {
    return null
  }
}

function writeCache(v: Latest | null) {
  try {
    if (v) localStorage.setItem(KEY, JSON.stringify(v))
    else localStorage.removeItem(KEY)
  } catch {
    // 同上：写不进去就下次再问。
  }
}

/**
 * fetchLatest 回最新 Release；查不到回 null。force（「重新检查」）先清缓存再问。
 *
 * **非 2xx 与网络失败都不写缓存**：仓库还没有 Release 时这里是 404，缓存它就是把
 * 「查不到」缓存成一天的「已是最新」。null 在界面上也不许显示成「已是最新」。
 */
export async function fetchLatest(force = false): Promise<Latest | null> {
  if (force) {
    writeCache(null)
  } else {
    const c = readCache()
    if (c && Date.now() - c.checkedAt < TTL) return c
  }
  try {
    const res = await fetch(API, { headers: { Accept: 'application/vnd.github+json' } })
    if (!res.ok) return null
    const j = (await res.json()) as { tag_name?: string; published_at?: string }
    if (!j.tag_name) return null
    const v: Latest = { tag: j.tag_name, published: j.published_at ?? '', checkedAt: Date.now() }
    writeCache(v)
    return v
  } catch {
    return null
  }
}
