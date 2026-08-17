// 供应商图标：渠道一套、模型一套，对不上都退回首字母。
//
// 渠道只给默认那几家官方（Anthropic / OpenAI / 百炼 / Vertex…）画图，中转和自建
// 不猜——跟 Cherry Studio「预置供应商有 logo、自定义没有」同一条。模型名另走
// MODEL_PATTERNS，按族认（gpt / claude / qwen / glm），不按渠道。
//
// 只有**供应商**一级的图标，没有逐模型的图标。cherry-studio 两套都有
// （`packages/ui/icons/{providers,models}`），但那套 model 图标是 24×24 的透明描摹、
// provider 图标是 120×120 带底色的方块，混着用两种视觉语言在一张列表里会打架。
//
// 图标资产取自 cherry-studio 的 `packages/ui`（该 workspace 包自身声明 MIT，仓库整体
// 是 AGPL-3.0 —— 只取 SVG 资产，匹配表是本项目自己写的，没有搬它的代码）。见
// ./README.md。

import type { ReactNode } from 'react'

// eager 是有意的：整套图标一共 ~94KB（gzip），而管理端是 embed 进单二进制的 SPA，
// 拆成异步 chunk 只会让每张列表首次渲染闪一下空白，省不下任何东西。
const RAW = import.meta.glob('./svg/*.svg', { query: '?raw', import: 'default', eager: true }) as Record<
  string,
  string
>

/**
 * namespaceIds 把一份 SVG 里的 id 全部加上文件名前缀。
 *
 * **不加就会串图。** 这些标记是内联进同一个文档的，而 id 在文档里是全局的；导出工具
 * 给 mask / gradient / clipPath 起的名字是 `mask0_1_26` 这种按序号来的，不同文件之间
 * 撞名是常态。撞上时先出现的那个定义赢，后面那枚图标就套着别人的蒙版渲染——表现是
 * 「某几个图标长得莫名其妙」，而且换一批图标就换一批受害者，极难往这上头想。
 *
 * 只改 `id="…"` 和引用它的 `url(#…)` / `href="#…"`，不碰别的。
 */
function namespaceIds(svg: string, prefix: string) {
  const ids = new Set<string>()
  for (const m of svg.matchAll(/\sid="([^"]+)"/g)) ids.add(m[1])
  let out = svg
  for (const id of ids) {
    // id 是导出工具生成的（字母数字下划线），但仍然转义一次——将来手加一枚带
    // 特殊字符的图标时不该悄悄失效。
    const esc = id.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    out = out
      .replace(new RegExp(`(\\sid=")${esc}(")`, 'g'), `$1${prefix}-${id}$2`)
      .replace(new RegExp(`url\\(#${esc}\\)`, 'g'), `url(#${prefix}-${id})`)
      .replace(new RegExp(`(\\shref="#)${esc}(")`, 'g'), `$1${prefix}-${id}$2`)
  }
  return out
}

/** light/dark 两份标记分开存：深色版只有少数几个（浅色版是纯黑描摹的那些）才有。 */
const LIGHT = new Map<string, string>()
const DARK = new Map<string, string>()
for (const [path, svg] of Object.entries(RAW)) {
  const file = path.slice(path.lastIndexOf('/') + 1, -4)
  const marked = namespaceIds(svg, file.replace(/\./g, '_'))
  if (file.endsWith('.dark')) DARK.set(file.slice(0, -5), marked)
  else LIGHT.set(file, marked)
}

/**
 * MODEL_PATTERNS 只看模型名（接入点名 / 纳管名 / 限定名），跟渠道图标是两套逻辑。
 *
 * **顺序即优先级**，特化的必须排在泛化的前面——`glm-4v` 要在 `glm` 前面才轮得到，
 * `gpt-oss` 要在 `gpt` 前面否则会被认成 OpenAI。
 *
 * 匹配的是**小写后的整个字符串**，不切 `/`：限定名 `bailian/qwen3-max` 里两半都可能
 * 带信息，而且第三方中转的模型名本来就长成 `anthropic/claude-sonnet-4.5` 这样。
 */
const MODEL_PATTERNS: ReadonlyArray<readonly [RegExp, string]> = [
  // OpenAI 系。gpt-oss 是 OpenAI 放出来的开放权重模型，图标仍是 OpenAI，但要先于
  // 下面那条泛化的 gpt 匹配掉，否则 `gpt-oss-120b` 落到哪条都一样、纯属巧合。
  [/gpt-oss/, 'openai'],
  [/\b(o1|o3|o4)\b|^(o1|o3|o4)[-.]/, 'openai'],
  [/gpt|chatgpt|davinci|sora|dall[-·]?e|whisper|codex/, 'openai'],

  [/claude|anthropic/, 'anthropic'],

  // Google。vertex/aistudio 是接入形态，gemini/gemma 是模型，图标分开。
  [/vertex/, 'vertexai'],
  [/gemini|gemma|imagen|palm|bison|nano[-\s]?banana/, 'google'],

  [/deepseek/, 'deepseek'],
  [/qwen|qwq|qvq|tongyi|wanx/, 'qwen'],
  [/kimi|moonshot/, 'moonshot'],
  [/glm|chatglm|codegeex|cogview|zhipu|z-?ai/, 'z-ai'],
  [/doubao|seed-|volc/, 'volcengine'],
  [/hunyuan/, 'tencent-cloud-ti'],
  [/ernie|wenxin|文心/, 'wenxin'],
  [/minimax|abab|hailuo/, 'minimax'],
  [/grok|xai/, 'grok'],
  [/mistral|mixtral|codestral|magistral|devstral|ministral|pixtral/, 'mistral'],
  [/llama|meta-/, 'meta'],
  [/command[-\s]?[ar]?|cohere|rerank-(english|multilingual)/, 'cohere'],
  [/spark|xinghuo|星火/, 'xinghuo'],
  [/step-\d|stepfun/, 'step'],
  [/yi-|zero-?one|01-ai/, 'zero-one'],
  [/internlm|intern-/, 'internlm'],
  [/baichuan/, 'baichuan'],
  [/longcat/, 'longcat'],
  [/kwaipilot|kwai/, 'kwaipilot'],
  [/skywork/, 'skywork'],
  [/sonar|perplexity/, 'perplexity'],
  [/nemotron|nvidia/, 'nvidia'],
  [/phi-\d|azure/, 'azureai'],
  [/nova-(pro|lite|micro|premier)|bedrock|titan-/, 'aws-bedrock'],
  [/flux/, 'bfl'],
  [/jina/, 'jina'],
  [/voyage/, 'voyage'],
  [/\bbge\b|\bm3e\b/, 'baai'],
]

/**
 * 渠道图标只给「默认那几家官方」——跟 Cherry Studio 一样：预置供应商有图，
 * 自己填的中转 / 自建走首字母。不拿模型名表来猜渠道，两套逻辑不许串。
 *
 * 口径层 v0.17 初始集是 Anthropic / OpenAI / Vertex / 百炼；「等」补的是同样
 * 有官方域名的实验室。host 写死官方后缀，不写 `aliyuncs` 这种会把 PAI 推理
 * 也认成百炼的宽模式。
 */
const CHANNEL_HOSTS: ReadonlyArray<readonly [RegExp, string]> = [
  [/anthropic\.com/, 'anthropic'],
  [/openai\.azure\.com/, 'azureai'],
  [/(^|\.)openai\.com$/, 'openai'],
  [/aiplatform\.googleapis|vertexai/, 'vertexai'],
  [/generativelanguage\.googleapis/, 'google'],
  [/(^|\.)deepseek\.com$/, 'deepseek'],
  [/dashscope\.aliyuncs\.com/, 'dashscope'],
  [/(^|\.)moonshot\.cn$/, 'moonshot'],
  [/bigmodel\.cn|(^|\.)z\.ai$/, 'z-ai'],
  [/volces\.com|volcengine\.com/, 'volcengine'],
  [/(^|\.)minimax\.(io|chat)$/, 'minimax'],
  [/(^|\.)x\.ai$/, 'grok'],
  [/(^|\.)mistral\.ai$/, 'mistral'],
]

/**
 * 渠道名兜底：只认人明显按官方起的名字（`openai`、`ali-prod`、`kimi`）。
 * 不扫模型名表——渠道叫 `gpt-relay` 不该因此长成 OpenAI。
 */
const CHANNEL_NAMES: ReadonlyArray<readonly [RegExp, string]> = [
  [/^anthropic\b/, 'anthropic'],
  [/^openai\b/, 'openai'],
  [/^azure\b/, 'azureai'],
  [/^(vertex|gemini|google)\b/, 'vertexai'],
  [/^(bailian|dashscope|百炼|ali[-_])/, 'dashscope'],
  [/^deepseek\b/, 'deepseek'],
  [/^(moonshot|kimi)\b/, 'moonshot'],
  [/^(zhipu|z-?ai|智谱)\b/, 'z-ai'],
  [/^(volc|doubao|ark)\b/, 'volcengine'],
  [/^minimax\b/, 'minimax'],
  [/^(grok|xai)\b/, 'grok'],
  [/^mistral\b/, 'mistral'],
]

function matchFirst(patterns: ReadonlyArray<readonly [RegExp, string]>, text: string) {
  for (const [re, key] of patterns) {
    if (re.test(text) && LIGHT.has(key)) return key
  }
  return null
}

/** vendorForModel 只看模型名，不看渠道。 */
export function vendorForModel(model: string): string | null {
  return matchFirst(MODEL_PATTERNS, model.toLowerCase())
}

/**
 * vendorForChannel 只认官方渠道：先 host，再渠道名。对不上就是首字母。
 *
 * 不回落到 MODEL_PATTERNS——渠道叫什么跟它上面跑的模型是两件事。
 * base_url 解析失败不算错，退回按名字猜；失败模式只能是「没有图标」。
 */
export function vendorForChannel(channel: { name: string; base_url: string }): string | null {
  let host = ''
  try {
    host = new URL(channel.base_url).host.toLowerCase()
  } catch {
    host = channel.base_url.toLowerCase()
  }
  return matchFirst(CHANNEL_HOSTS, host) ?? matchFirst(CHANNEL_NAMES, channel.name.toLowerCase())
}

/**
 * Avatar 是那枚方块本身。没匹配到图标时退回首字母块，而不是留个空洞——列表里每一行
 * 都得占同样宽度，否则名字会参差不齐。品牌色在 CSS 里统一 grayscale 掉（DESIGN.md
 * §1.4 / §3），这里不管颜色，只管取哪一份标记。
 *
 * 深色版靠 CSS 显隐而不是 JS 选：跟随系统主题切换时不用监听 media query，也就不会有
 * 「切了主题但图标还是上一套」的窗口。只有一份标记的（大多数带底色的品牌方块深浅色
 * 通用）两种主题下都显示同一份。
 */
export function Avatar({
  vendor,
  fallback,
  size = 20,
  title,
}: {
  vendor: string | null
  fallback: string
  size?: number
  title?: string
}) {
  const style = { width: size, height: size }
  const light = vendor === null ? undefined : LIGHT.get(vendor)
  if (light === undefined || vendor === null) {
    return (
      <span className="avatar avatar-text" style={style} title={title}>
        {initialOf(fallback)}
      </span>
    )
  }
  const dark = DARK.get(vendor)
  return (
    <span className="avatar" style={style} title={title ?? vendor ?? undefined}>
      <span
        className={dark ? 'avatar-light' : undefined}
        // 图标是构建期就固定在仓库里的静态资产，不是任何用户输入。
        dangerouslySetInnerHTML={{ __html: light }}
      />
      {dark && <span className="avatar-dark" dangerouslySetInnerHTML={{ __html: dark }} />}
    </span>
  )
}

/** ModelIcon 给模型名（接入点名 / 纳管模型名 / 限定名）配图标。 */
export function ModelIcon({ model, size }: { model: string; size?: number }) {
  return <Avatar vendor={vendorForModel(model)} fallback={model} size={size} title={model} />
}

/** ChannelIcon 给渠道配图标。 */
export function ChannelIcon({
  channel,
  size,
}: {
  channel: { name: string; base_url: string }
  size?: number
}) {
  return <Avatar vendor={vendorForChannel(channel)} fallback={channel.name} size={size} title={channel.name} />
}

/** initialOf 取一个能当头像用的字符：CJK 取首字，拉丁取首字母大写。 */
function initialOf(name: string) {
  const s = name.trim()
  if (!s) return '?'
  const first = [...s][0]
  return /[a-z]/i.test(first) ? first.toUpperCase() : first
}

/** IconRow 把「图标 + 文字」这个到处都在重复的组合收成一个。 */
export function IconRow({ icon, children }: { icon: ReactNode; children: ReactNode }) {
  return (
    <span className="icon-row">
      {icon}
      <span className="icon-row-text">{children}</span>
    </span>
  )
}
