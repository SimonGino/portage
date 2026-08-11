# golden 转录库

真实字节存档。样本清单与场景口径见 `docs/MVP设计草案.md` §9。

## 两类样本

`meta.json` 的 `direction` 分开它们——两类样本录的是链路的两端，别混：

| direction | 录的是 | 驱动 | 起于 |
|---|---|---|---|
| `upstream` | 上游**响应**的原始字节 | Tap 测试、codec 的**编码**侧 | M0 |
| `inbound` | harness **入站请求**的原始字节 | codec 的**解码**侧 | M2 |

```
testdata/golden/<样本名>/
  meta.json      # direction / protocol / stream / endpoint / verified …
  request.json   # 请求体（脱敏后）；inbound 样本要的就是这一份
  response.raw   # 上游响应原始字节，逐字节保真——仅 upstream 样本有
```

`upstream` 样本另有 `status` 与 `expect`；`inbound` 样本另有 `headers`（白名单里那几个
影响转换语义的头）与 `stub`（本轮回了哪个假响应，只为让录制可复现）。

**`inbound` 样本没有 `expect`。** 它那边响应来自手写 stub，Tap 算出来的是道具的数，不是
任何事实——写进去只会给人一个可以核对的错觉。

## 采集流程

两类样本都用 `cmd/goldenrec`，模式不同：

**upstream（proxy 模式）** —— 需要真实上游凭证：

```bash
GOLDENREC_BASE_URL=https://api.anthropic.com GOLDENREC_PROTOCOL=anthropic GOLDENREC_CREDENTIAL=sk-ant-... go run ./cmd/goldenrec
```

**inbound 模式** —— 不碰上游、不要凭证，按手写脚本回假响应把 harness 驱动到下一轮。
脚本与用法见 `testdata/goldenstub/README.md`：

```bash
GOLDENREC_MODE=inbound GOLDENREC_PROTOCOL=anthropic GOLDENREC_STUBS=./testdata/goldenstub/anthropic-tool-round go run ./cmd/goldenrec
```

随后把 harness 指过去跑出目标场景（Claude Code：`ANTHROPIC_BASE_URL=http://127.0.0.1:8318`）。
样本落在 `testdata/golden/raw/`，**那是暂存区，不进 git**。

### 用 Claude Code 无头模式采 Anthropic 入站样本

实测跑通的方式（2026-08-07），几处坑都是踩过才知道的：

```bash
GOLDENREC_LISTEN=127.0.0.1:8325 GOLDENREC_PROTOCOL=anthropic \
GOLDENREC_BASE_URL="$ANTHROPIC_BASE_URL" GOLDENREC_CREDENTIAL="$ANTHROPIC_API_KEY" \
go run ./cmd/goldenrec &

env -u CLAUDECODE -u CLAUDE_CODE_ENTRYPOINT -u CLAUDE_CODE_CHILD_SESSION \
    -u CLAUDE_CODE_SDK_HAS_HOST_AUTH_REFRESH -u CLAUDE_CODE_SDK_HAS_OAUTH_REFRESH \
    -u CLAUDE_CODE_OAUTH_SCOPES -u CLAUDE_CODE_SESSION_ID -u CLAUDE_CODE_HOST_SESSION_ID \
    -u CLAUDE_AGENT_SDK_VERSION -u CLAUDE_CODE_EXECPATH \
  ANTHROPIC_BASE_URL=http://127.0.0.1:8325 ANTHROPIC_API_KEY=placeholder \
  claude -p '把 /tmp/goldenrec-a.txt 读出来' --model claude-sonnet-5 --allowedTools Read
```

- **`CLAUDE_CODE_*` 必须 unset**。在 Claude Code 会话里起子进程时它们会被继承，子进程
  于是拿宿主的 OAuth 去打你的 `ANTHROPIC_BASE_URL`，一律 401 `Invalid token`。
- **凭证由 goldenrec 换**，harness 那侧填 `placeholder` 就行——真 key 只在 goldenrec 的
  环境变量里，不进 harness 的日志与会话记录。
- **一个场景起一个 goldenrec，跑完确认端口真的放了再起下一个**。`pkill -f exe/goldenrec`
  杀不掉 `go run` 那个父进程，端口仍被占着，第二个实例静默起不来，于是后面几轮的样本
  全落进第一个实例的目录里、序号也接着排。用 `lsof -ti :8325` 核一下最省事。
- **Claude Code 打的是 `POST /v1/messages?beta=true`**（带查询串），并且每轮都先打一次
  `/v1/messages/count_tokens`。proxy 模式照转，inbound 模式就地估算、不吃 stub。
- 场景靠 `-p` 的提示词凑：读一个文件 → 单 tool_use；读两个文件 → 并行 tool_use；
  纯问答 → 纯文本。工具轮的第二轮请求（带 `tool_result` 的那个）才是 `A→CC` 要的输入。

### 用 Codex CLI 无头模式采 Responses 入站样本

```bash
CODEX_HOME=/tmp/goldenrec-codex OPENAI_API_KEY=placeholder \
codex exec --ignore-user-config --skip-git-repo-check -C /tmp/goldenrec-work -s read-only \
  -c model_provider=rec -c model_providers.rec.name=rec \
  -c model_providers.rec.base_url=http://127.0.0.1:8326/v1 \
  -c model_providers.rec.env_key=OPENAI_API_KEY \
  -c model_providers.rec.wire_api=responses \
  -c model=gpt-5.6-luna -c approval_policy=never -c model_reasoning_effort=high \
  '把 /tmp/goldenrec-a.txt 的第一行读出来'
```

- **`CODEX_HOME` 指到别处 + `--ignore-user-config`**，免得动到你自己的 `~/.codex`
  （那里有 `auth.json`）。凭证走 `env_key`，真 key 只在 goldenrec 那侧。
- `base_url` **要带 `/v1`**——Codex 直接拼 `<base_url>/responses`，与网关 `base_url`
  不带 `/v1` 的填法正好相反，别照抄。
- `-s read-only -c approval_policy=never` 让它能跑 `cat` 一类只读命令而不停下来问。

### 人工关卡

最后人工过一遍，才移进 `testdata/golden/<样本名>/`：

- 删掉真实凭证（请求头只留白名单，但请求体里可能有你粘进去的东西）
- 删掉个人对话内容——换成无意义的测试文本，upstream 样本还要**相应改掉 `response.raw`
  里的文本增量**
- upstream：核对 `meta.json` 的 `expect` 与 `response.raw` 里的数字确实相符
- inbound：确认没有 `installation_id`、机器名、绝对路径一类客户端指纹漏在请求体里。
  **Claude Code 的指纹在请求体里，不在头里**：`metadata.user_id` 是一串 JSON，含
  `device_id`（稳定的机器指纹）、`account_uuid`、`session_id`，头白名单拦不住它，必抓。
  正文里的 `/Users/<你>` 一类绝对路径同理。
- 确认无误后把 `verified` 改成 `true`

入站样本的脱敏已经写成过滤器，别再手工改（185 KB × 42 个 tool 定义，手工既不可复现
也不可复核）：

```bash
jq -f scripts/redact-inbound-anthropic.jq testdata/golden/raw/<原目录>/request.json > testdata/golden/<样本名>/request.json
jq -f scripts/redact-inbound-responses.jq testdata/golden/raw/<原目录>/request.json > testdata/golden/<样本名>/request.json
```

口径是**保结构、换文本**（PO 2026-08-07 裁定）：块数与顺序、cache_control 断点、tool 的
name 与 input_schema 形状、`tool_use.id` ↔ `tool_result.tool_use_id`（Responses 侧是
`custom_tool_call.call_id` ↔ `custom_tool_call_output.call_id`）配对、消息角色序列全部原样
保留；换掉的是 harness 的提示词原文与各自的指纹。占位符带原文长度，「这是个 29 KB 的
缓存块」这件事跟着样本走。

两侧指纹藏的地方不同，都**不在请求头里**：

| harness | 指纹字段 |
|---|---|
| Claude Code | `metadata.user_id`（内含 `device_id`/`session_id`）、`system[0]` 那条内含 `cc_version` 的 billing header、`thinking.signature` |
| Codex CLI | `client_metadata.x-codex-installation-id` 与同层的 session/thread/turn/window id、`prompt_cache_key`、`<environment_context>` 里的 cwd |

两份过滤器最后都跑一遍 `scrub_paths` 兜底扫绝对路径。这不是多余的：Codex 那次，**模型自己**
在 `exec` 的 JS 入参里写出了 `workdir:"/private/tmp/claude-…/scratchpad/work"`，路径里带着
用户名与会话 id。按项脱敏永远追不上模型能把路径写到哪儿。

**`reasoning.encrypted_content` 原样留着**（`in-responses-tool-turn2` 里是 1720 字符的真密文）。
它是这批样本的存在理由——CC 那边没有任何位置放得下它，`R→CC` 必须当面回答「丢还是留」，
而只有真的长度与真的不透明性摆在那儿，这个问题才提得出来。内容是模型对「读一个 /tmp 测试
文件」的推理密文，不含个人信息。

### 已入库入站样本（2026-08-07，均 `verified: false`，等人工核）

| 样本 | 采法 | 这份样本立的边界 |
|---|---|---|
| `in-anthropic-text` | Claude Code → 真实上游 | system 三块 + 3 个 cache_control 断点，42 个 tool 声明 |
| `in-anthropic-tool-turn1` | 同上 | 单 `tool_use` 前的那一轮 |
| `in-anthropic-tool-turn2` | 同上 | `tool_use` + `tool_result`，**另带 thinking 块** |
| `in-anthropic-parallel-turn1` | 同上 | 并行工具轮的第一轮 |
| `in-anthropic-parallel-turn2` | 同上 | **2 个 `tool_use` + 2 个 `tool_result`** |
| `in-responses-text` | Codex CLI → 真实上游 | `additional_tools` 输入项（这版 Codex 不用顶层 `tools`） |
| `in-responses-tool-turn1` | 同上 | 单 `custom_tool_call` 前的那一轮 |
| `in-responses-tool-turn2` | 同上 | **reasoning 项带真 `encrypted_content`** + `custom_tool_call` + output |
| `in-responses-parallel-turn2` | Codex CLI → **stub** | **2 个并行 `custom_tool_call` + 2 个 output**（真上游逼不出来，理由见 `testdata/goldenstub/README.md`） |

对应的 `response.raw` 仍没跟着入库，但理由已经不是「字段出处不明」了——那条 2026-08-10
核清楚了（见下节「经中转站采集」）。真正的理由是这批响应是 Claude Code 真实会话的产物，
带 67 KB 缓存上下文与 thinking 块，脱敏成本远高于照 §9 场景重录一遍；六个 `anthropic-*`
upstream 样本因此另采（2026-08-11），没有复用它们。原始未脱敏目录仍在
`testdata/golden/raw/`，要回头核对时对着它看。

**还缺非流式变体**：两个 harness 都只走流式。要补就拿录下来的请求体改 `"stream":false`
重放一遍。

`expect` 是 goldenrec 用 Tap 自己算出来的**草稿**：不经人核对就当期望值，等于让实现给自己判卷。
`golden_test.go` 因此拒绝任何 `verified: false` 的样本。入站样本更甚——脱敏动作本身会改字节，
改错了不看就发现不了。

## M0 必抓子集

| 样本名 | 场景 |
|---|---|
| `anthropic-stream-text` | Anthropic 流式，纯文本长回复 |
| `anthropic-stream-tool` | Anthropic 流式，单次 tool_use |
| `anthropic-stream-parallel-tools` | Anthropic 流式，并行多 tool_use 交错增量 |
| `cc-stream-text` | CC 流式，纯文本 |
| `cc-stream-tool` | CC 流式，单次 tool_calls（参数跨 chunk） |
| `cc-stream-parallel-tools` | CC 流式，并行 tool_calls，index 交错 |
| `anthropic-text` / `anthropic-tool` / `anthropic-parallel-tools` | 以上 Anthropic 三例的非流式版 |
| `cc-text` / `cc-tool` / `cc-parallel-tools` | 以上 CC 三例的非流式版 |

Responses 样本与上游异常样本（§9 的 8、9）留到 M1。

### 已入库（2026-08-06）

`cc-*` 六个全部采自真实 OpenAI 兼容上游。`expect` 由一个独立写的解析器从
`response.raw` 重算核对过——不复用 Go 侧任何代码，否则又是实现给自己判卷。

两点要知道：

- **`cc-text` / `cc-stream-text` 刻意带缓存命中**（`CacheReadTokens: 3840`）。
  第一版样本六个的 cache 全是 0，`cached_tokens` 那条解析路径没有任何样本走到，
  把它读错也没人发现；重录时用超长固定前缀打两遍取第二遍，缺口才补上。
- **`cc-stream-parallel-tools` 没能体现 §9 要的「index 交错」**。这个上游三次
  工具调用固定按序成块吐（`[0,0,1,1,2,2]`），换长参数提示重试也一样。不挡 M0
  ——Tap 只提 usage / model / stop_reason，不重组工具调用；index 交错是 P1
  codec 的事，届时要么换个会交错的上游采，要么承认 §9 这条脱离实际。

### 已入库（2026-08-11）

`anthropic-*` 六个补齐，M0 必抓子集不再有 skip。采自**第三方 Anthropic 协议中转**
（订阅池型，非官方直连；PO 2026-08-10 裁定可当真实上游用，理由见下节的透传核查）。
场景是照 §9 直接构造的六个 curl，不是 harness 会话：三例非流式 + 同三例流式，
文本 / 单 `tool_use` / 并行 `tool_use`。

`expect` 同样用一个独立的 jq 脚本从 `response.raw` 重算核对过，六个全对得上——
与 `cc-*` 那批同一个做法，不复用 Go 侧任何代码。

两点要知道：

- **`InputTokens` 偏大 357**（纯文本那例 376，而请求体自己只有 12 token）。中转会往
  每个请求里塞一段固定内容，实测两个不同长度的 prompt 差值恒定 357。这不影响样本
  作数：`golden_test.go` 只拿 `response.raw` 喂 Tap，`request.json` 从不参与断言，
  数值是「这条响应自己报的数」，前后自洽。但**别拿这批样本去推请求体与 token 的关系**。
- **cache 全 0**。`cc-*` 那批特意补过缓存命中的缺口，Anthropic 这侧还没有；
  `cache_read_input_tokens` 的解析路径目前只有 CC 样本走到。要补就照 `cc-text` 的做法，
  超长固定前缀配 `cache_control` 打两遍取第二遍。

### 经中转站采集：先核透传，再避三个雷

2026-08-10 起、到 08-11 采样前，对手上这个中转做过一轮源码 + 探针核查（它跑的是
`sub2api`，本地有源码）。
结论是**响应体逐字透传**（`gateway_anthropic_passthrough.go` 按行 `io.WriteString` 原样
回写，只旁路解析 usage，不做 decode→encode），佐证是响应里的 `inference_geo`、
`usage.iterations` 在中转源码里根本不存在，它造不出来。`stop_details` 则是当前 API 的
无条件字段——加不加 `anthropic-beta` 都在，四组对照实测一致，不是 beta 门控、也不是中转
杜撰。**响应头是被过滤的**（中转有一张响应头白名单），所以 `request-id`、
`anthropic-ratelimit-*` 一类头的保真度这里验不了，得等官方 key。

拿它录样本要绕开三个雷，都是读源码读出来的：

| 雷 | 触发条件 | 绕法 |
|---|---|---|
| 假响应顶包 | 正文含 `[SUGGESTION MODE:`、CC 那句取标题提示词或 `Warmup`；或 `max_tokens=1` + haiku | 场景正文别用这几个词，`max_tokens` 给大 |
| 工具名字节改写 | 工具名以 `session_` / `sessions_` 开头（默认就开，不是配置项） | 工具名别用这两个前缀 |
| 请求体注入 | 无条件，固定 357 token | 认了，只在读 `InputTokens` 时记得 |

还有个操作上的坑：**流式连打会把 goldenrec 那个进程的上游连接打死**——非流式三个跑完
接着跑流式，之后每个流式请求都在上游那一跳超时（`*url.Error`），换个新进程立刻就好。
原因没深究，照方抓药：流式样本单独起一个 goldenrec，中间隔几秒。

## 顺带核对

采集时留意 harness 实际发了什么，用来验证 §6.1 里那些**从参考仓库推断**的假设：
请求头白名单、`anthropic-beta` 是否真的要转发、`count_tokens` 的调用时机。
对不上的记在验收票里（Anthropic 侧是 #7，其余 #6 已收官），回写展开层。
