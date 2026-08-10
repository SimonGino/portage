// 供应商图标：把一个模型名或渠道推断成一枚品牌方块。
//
// 只有**供应商**一级的图标，没有逐模型的图标。参考仓库 cherry-studio 两套都有
// （`packages/ui/icons/{providers,models}`），但那套 model 图标是 24×24 的透明描摹、
// provider 图标是 120×120 带底色的方块，混着用两种视觉语言在一张列表里会打架；而且
// 逐模型图标要跟着上游发新模型一直追，收益只是 gpt-5-codex 和 gpt-5 长得不一样。
// 供应商一级则基本不动。
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
 * VENDOR_PATTERNS 把模型名匹配成图标键。**顺序即优先级**，特化的必须排在泛化的前面
 * ——`glm-4v` 要在 `glm` 前面才轮得到，`gpt-oss` 要在 `gpt` 前面否则会被认成 OpenAI。
 *
 * 匹配的是**小写后的整个字符串**，不切 `/`：限定名 `bailian/qwen3-max` 里两半都可能
 * 带信息，而且第三方中转的模型名本来就长成 `anthropic/claude-sonnet-4.5` 这样。
 */
const VENDOR_PATTERNS: ReadonlyArray<readonly [RegExp, string]> = [
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
 * HOST_PATTERNS 把渠道的 base_url 主机名匹配成图标键。
 *
 * 渠道没有「供应商」这个字段（它只有名字 + base_url + 协议），所以只能推。先看 host
 * 再看名字：host 是配置里唯一不撒谎的部分，而渠道名是人随手起的（「便宜的那个」
 * 「备用」都很常见）。
 */
const HOST_PATTERNS: ReadonlyArray<readonly [RegExp, string]> = [
  [/anthropic\.com/, 'anthropic'],
  [/openai\.azure\.com|azure/, 'azureai'],
  [/openai\.com/, 'openai'],
  [/aiplatform\.googleapis|vertex/, 'vertexai'],
  [/generativelanguage\.googleapis|aistudio|googleapis/, 'google'],
  [/deepseek\.com/, 'deepseek'],
  [/dashscope|aliyuncs/, 'dashscope'],
  [/moonshot/, 'moonshot'],
  [/bigmodel\.cn|z\.ai/, 'z-ai'],
  [/volces\.com|volcengine/, 'volcengine'],
  [/hunyuan|tencent/, 'tencent-cloud-ti'],
  [/baidubce|baidu/, 'baidu'],
  [/minimax/, 'minimax'],
  [/x\.ai/, 'grok'],
  [/mistral\.ai/, 'mistral'],
  [/siliconflow/, 'silicon'],
  [/modelscope/, 'modelscope'],
  [/openrouter/, 'openrouter'],
  [/groq\.com/, 'groq'],
  [/together\.(ai|xyz)/, 'together'],
  [/fireworks\.ai/, 'fireworks'],
  [/cerebras/, 'cerebras'],
  [/nvidia|nim\./, 'nvidia'],
  [/localhost|127\.0\.0\.1|:11434|ollama/, 'ollama'],
  [/:1234|lmstudio|lm-studio/, 'lmstudio'],
  [/githubcopilot|github/, 'github-copilot'],
  [/perplexity/, 'perplexity'],
  [/sparkapi|xf-yun|xinghuo/, 'xinghuo'],
  [/stepfun/, 'step'],
  [/lingyiwanwu|01\.ai/, 'zero-one'],
  [/intern-ai|internlm/, 'internlm'],
  [/baichuan/, 'baichuan'],
  [/hyperbolic/, 'hyperbolic'],
  [/ppinfra|ppio/, 'ppio'],
  [/gitee/, 'gitee-ai'],
  [/aihubmix/, 'aihubmix'],
  [/cloudflare|workers\.dev/, 'cloudflare'],
  [/poe\.com/, 'poe'],
  [/huggingface|hf\.co/, 'huggingface'],
  [/302\.ai/, '302ai'],
  [/qiniu/, 'qiniu'],
  [/upstage/, 'upstage'],
  [/sophnet/, 'sophnet'],
  [/dify/, 'dify'],
  [/lanyun/, 'lanyun'],
  [/burncloud/, 'burncloud'],
  [/one-?api|new-?api/, 'newapi'],
]

function matchFirst(patterns: ReadonlyArray<readonly [RegExp, string]>, text: string) {
  for (const [re, key] of patterns) {
    if (re.test(text) && LIGHT.has(key)) return key
  }
  return null
}

/** vendorForModel 从模型名（接入点名、纳管模型名或限定名都行）推供应商图标键。 */
export function vendorForModel(model: string): string | null {
  return matchFirst(VENDOR_PATTERNS, model.toLowerCase())
}

/**
 * vendorForChannel 从渠道推供应商图标键：先 base_url 的 host，再渠道名。
 *
 * base_url 解析失败（配到一半、手写 SQL 灌进来的脏值）不算错，退回按名字猜——这个
 * 函数的失败模式只能是「没有图标」，不能是「页面炸了」。
 */
export function vendorForChannel(channel: { name: string; base_url: string }): string | null {
  let host = ''
  try {
    host = new URL(channel.base_url).host.toLowerCase()
  } catch {
    host = channel.base_url.toLowerCase()
  }
  return matchFirst(HOST_PATTERNS, host) ?? matchFirst(HOST_PATTERNS, channel.name.toLowerCase()) ??
    matchFirst(VENDOR_PATTERNS, channel.name.toLowerCase())
}

/**
 * Avatar 是那枚方块本身。没匹配到图标时退回首字母块，而不是留个空洞——列表里每一行
 * 都得占同样宽度，否则名字会参差不齐。
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
      <span className="avatar avatar-text" style={{ ...style, ...hueOf(fallback) }} title={title}>
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

/** hueOf 把名字散成一个稳定的色相——同一个名字每次渲染都是同一个颜色。 */
function hueOf(name: string) {
  let h = 0
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) % 360
  return { '--avatar-hue': String(h) } as React.CSSProperties
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
