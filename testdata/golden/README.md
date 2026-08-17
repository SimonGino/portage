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
  meta.json      # direction / protocol / stream / endpoint / source / verified …
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

### 用 Codex CLI 交互式 TUI 采压缩样本（remote compaction v2）

`/compact` 是 TUI 的 slash 命令，`codex exec` 发不出来；靠上下文自然涨到阈值触发自动压缩
也不可靠（实测模型会 `cat big.txt >/dev/null` 把内容挡在上下文外）。所以这一档只能开个
伪终端按键，或者你自己手打。

**决定性的一条前置**（2026-08-13 实测，踩过一遍才知道）：远程压缩走不走，看**客户端**
配置里该 provider 的 **`name`**：

```toml
[model_providers.rec]
name = "OpenAI"      # → remote compaction v2：input 尾部发 compaction_trigger
# name = "portage"   # → 本地压缩：一个 trigger 都不发，静默走两次普通请求
```

取错名字时 `/compact` **照样成功**，只是压缩在客户端本地做完了——网关侧看到的是一次
带 `COMPACT_PROMPT` 的普通请求加一次带 `SUMMARY_PREFIX` 的普通请求，不留任何压缩痕迹。
不知道这条的话，会以为自己采到了压缩样本。**`requires_openai_auth` 不是这个开关**，
设成 true 反而让 Codex 一个请求都不发。

其余与上面那段 `codex exec` 的采法相同（`CODEX_HOME` 隔离、`base_url` 要带 `/v1`）。
一点不同：交互式 `codex` **不认 `--ignore-user-config`**（那是 `exec` 的旗标），
隔离全靠 `CODEX_HOME`。

`goldenrec` 的 `recordedHeaders` 收了 `x-codex-beta-features`，样本的 `meta.json` 里
`remote_compaction_v2` 就是压缩档位的判据——丢了这个头，v1 与 v2 的样本从字节上分不开。

#### 已入库压缩样本（2026-08-13，`verified: true`）

同一个会话的连续三轮，采自经 sub2api 中转的 OpenAI 兼容上游 + Codex CLI 0.147。

| 样本 | 这份样本立的边界 |
|---|---|
| `responses-stream-compact-turn1` | 压缩前的普通一轮，基线 |
| `responses-stream-compact-trigger` | **input 尾项 `{"type":"compaction_trigger"}`**；响应里 `output_item.added` 与 `.done` 各一个 compaction item、两份 `encrypted_content` **不同**，`response.completed.output` 是**空数组** |
| `responses-stream-compact-replay` | 回带轮：compaction 项的 id 与密文逐字节等于上一份的 `.done` |

**`compaction.encrypted_content` 与 `cmp_…` id 原样留着**，理由同 `in-responses-tool-turn2`
里那段 reasoning 密文：它是这批样本的存在理由。跨两份样本的那组对应关系正是「客户端取的是
`.done` 那份」的全部证据，脱敏时动了它，样本就只剩形状不剩结论。内容是模型对「一句话解释
SSE 注释行」这轮对话的摘要密文，不含个人信息。

`expect` 用 `scripts/verify-expect-responses.py` 独立重算核对过（不复用 Go 侧任何代码，
做法同 `cc-*` 与 `anthropic-*` 两批），三份全对得上。这批顺带补上了 **Responses Tap 的
`cached_tokens` 真实覆盖**——此前只有 CC 样本走到过缓存解析路径。

脱敏两半各有一份脚本：

```bash
jq -f scripts/redact-inbound-responses.jq raw/<原目录>/request.json > golden/<样本名>/request.json
python3 scripts/redact-upstream-responses.py raw/<原目录>                > golden/<样本名>/response.raw
```

`response.raw` 那半是**纯文本替换**而不是解析→重序列化：样本的存在理由是逐字节保真，
指纹又都是 UUID，换成等长的全零 UUID 字节偏移一个不动。要换的值从同目录的 `request.json`
里读，不扫全文——`encrypted_content` 那串 base64url 理论上凑得出 UUID 的形状，
而它恰恰是唯一不能动的东西。

**两个例外按键名扫**（2026-08-14 起，见下方 base 批那节）：响应侧的 `prompt_cache_key`
回显值与 `safety_identifier` 是中转/上游**派生**的，请求体里没有这两个值，「从 request
读值」结构上够不着。禁的是按**值的形状**扫，键名锚定的 `"<键>":"…"` 落不进密文里
（base64url 的字母表没有 `"` 也没有 `:`）。归零同样等长，`meta.json` 的 expect 不用动。

**没入库的**：本地压缩那一档（`name` 非 `"OpenAI"`）。它走的是普通请求路径，对本项目
没有特殊形状，不值当占一份样本；`COMPACT_PROMPT` / `SUMMARY_PREFIX` 两个常量的逐字节
核对已经靠它做完了（见 `internal/protocol/openairesponses/compaction.go`）。

#### 已入库 reasoning 样本（2026-08-14，`verified: true`，#93）

口径层 v0.62「thinking 出向合成」的前置样本。六份，三档：

| 样本 | 方向 | 这份样本立的边界 |
|---|---|---|
| `cc-stream-reasoning-text` | upstream | **纯 reasoning 开头**：两条 `delta.reasoning_content` 之后才出第一个 `delta.content`——#87 起因里「`message_start` 之后几秒空白」的线上形态 |
| `cc-stream-reasoning-tool-turn1` | upstream | **reasoning + 工具调用**：`reasoning_content` 后直接进 `tool_calls`，`finish_reason=tool_calls`，全程无 `content` |
| `cc-stream-reasoning-tool-turn2` | upstream | 工具结果回去后**又一段 reasoning + 正文** |
| `responses-stream-reasoning-turn1` | upstream | **reasoning item 完整生命周期**：`output_item.added`（`encrypted_content` 是**空串**）→ `reasoning_summary_part.added` → `reasoning_summary_text.delta`/`.done` → `summary_part.done` → `output_item.done`（**这份才带真密文**） |
| `responses-stream-reasoning-replay` | upstream | 回带轮：上一轮的 reasoning item 原样发回 `input`（同 id、同密文），本轮响应里没有新的 reasoning item |
| `in-anthropic-thinking-replay` | inbound | **Claude Code 回带无 signature 的 thinking 块**，`signature` 被客户端自己补成空串 `""` |

三条这批才有的注意事项：

**上游换了型号。** 票面写的是 glm-5.2 / deepseek 一类，手上两个上游都没有；改用经中转的
`gpt-5.6-luna`（`reasoning_effort=high` + `reasoning.summary=detailed`）顶上——解码侧吃的是
`reasoning_content` 增量这个**线形态**，模型身份不进 codec。PO 若有 glm/deepseek 的 key，
补一份同形态样本即可，不必推翻这批。

**CC 那三份的请求是 curl 手造的**，不是 harness 发的：没有 harness 说 CC，而 CC 那侧未来
的客户端就是 portage 自己，这里近似的是它将来的出站形状。响应仍是真实上游字节。

**「单条消息内交错」这一格采不到，只登记不伪造。** 这个中转把 summary 分片全部前置，实测
逼不出「正文中途插一段 reasoning」；`tool-turn1` + `tool-turn2` 覆盖的是**轮级**交错。
手造 SSE 补这一格会违反本库唯一那条规矩。

`in-anthropic-thinking-replay` 的 `signature` **刻意没走** `redact-inbound-anthropic.jq` 的
占位符（那条规则会把它换成 `[redacted thinking-sig len=0]`）：样本的存在理由就是这个空串，
换掉之后 codec 测试读到的是一个非空签名，结论就没了。理由同压缩批留 `encrypted_content`。
它对应的 stub 是 `testdata/goldenstub/anthropic-thinking-nosig/`（道具，不入库）。

`expect`：Responses 两份用 `scripts/verify-expect-responses.py` 核对；CC 三份按 CC wire 语义
独立重算（终态 `usage` + `finish_reason`，不复用 Go 侧代码），五份全对得上。

#### 已入库 Responses 出口半边样本（2026-08-14，`verified: true`，#79）

`openairesponses` 出口半边的样本前提。五份，三档，全部 `direction: upstream`：

| 样本 | 这份样本立的边界 |
|---|---|
| `responses-stream-text` | **纯文本 happy-path**：output 只有一个 message item，九帧走完 |
| `responses-stream-tool-turn1` | **custom 工具整轮的第一轮**：output 三个 item——reasoning（无 summary 事件）、message 正文、`custom_tool_call`；工具 item **没有 content_part 那一层**，终态字段名是 `input` 不是 `arguments` |
| `responses-stream-tool-turn2` | 回带轮：上一轮的 reasoning 密文原样发回 `input`，`custom_tool_call` ↔ `custom_tool_call_output` 按 `call_id` 配对 |
| `responses-stream-parallel-turn1` | **code-mode 并行**：提示词诱导同时读两个文件，线上仍只有**一个** `custom_tool_call`——并行在工具入参那段 JS 的 `Promise.all` 里，不在 output item 层 |
| `responses-stream-parallel-turn2` | 并行轮的回带：一个 `custom_tool_call_output`，两个文件的结果合在一条 output 文本里 |

**「并行」指的是 code-mode 那一档，不是线级两个工具项。** 线级并行仍只有
`in-responses-parallel-turn2` 那个 stub 覆盖（真上游逼不出来，理由见
`testdata/goldenstub/README.md`）。样本名里的 parallel 别误读成两个 `custom_tool_call`。

四条这批才有的注意事项：

**采集环境「干净」但 `<skills_instructions>` 照样在。** 这批用 `codex exec --ignore-user-config`
+ `CODEX_HOME=/tmp/goldenrec-codex` + `cwd=/tmp/goldenrec-work` 采的，本以为躲开了 08-07
那 13.7 KB 的技能清单——**没躲开**：`~/.agents/skills/` 不归 `CODEX_HOME` 管，第二条
developer 消息里仍有 13.8 KB 清单逐行列着 `/Users/<你>/.agents/skills/*/SKILL.md`。
`redact-inbound-responses.jq` 把 developer 消息整条换掉，所以它被顺手清了；但**下次采集
别指望环境隔离能挡住它**，指纹在 harness 的 system prompt 里，只能靠脱敏。

**终帧的 `response.completed.output` 是中转重组的降级形态，作不了数。** 实采里它把
`custom_tool_call` 改回 `function_call`、丢掉 `arguments`、reasoning item 整个不列，
item 上连 `id` / `status` 都没有。压缩批与 reasoning 批同渠道同形态，所以这是**中转的
行为而不是这批的偶然**——终帧 output 列表的真实形状在这个渠道上验不了，要等官方直连。
`openairesponses.EncodeStream` 按 OpenAI 契约列全 item，与实采不同是有意的。

**`safety_identifier`（`user-…`）与响应侧回显的 `prompt_cache_key` 已清**（PO 2026-08-14
裁定，#79 收尾）。采样当天按「它是中转账号侧的标识、不是本机指纹，改这批不改那批只会让
同一渠道的样本自相矛盾」暂留，条件是**真要清就三批一起清**；这次正是三批一起清的：压缩批
3 份、reasoning 批 2 份、base 批 5 份，每份 `response.raw` 各 6 处（两个字段各 3 次）。
请求侧的 `prompt_cache_key` 本来就由 jq 那半归零，没动。归零**等长保格式**——16 位 hex 换
16 个 `0`、UUID 形态保连字符位、`safety_identifier` 留 `user-` 前缀其余等长归零——所以
`response.raw` 字节数不变、`meta.json` 的 expect 也不用跟着动（10 份重跑
`verify-expect-responses.py` 全一致）。规则已并进 `scripts/redact-upstream-responses.py`
的 `zero_derived_ids`，下批采集自动带上，不靠人记。

**`expect` 用 `scripts/verify-expect-responses.py` 独立重算核对过**（不复用 Go 侧任何代码），
五份全对得上；字段级与 `EncodeStream` 的离线比对结论记在
`internal/protocol/openairesponses/encode.go` 的文件头与展开层 §4.2。

顺带修掉的一个坑：`scripts/redact-upstream-responses.py` 的路径兜底正则字符类只排除了
`"` 没排除 `\`，扫 SSE 的 data 行时会把 `\"` 的转义反斜杠一起吃掉，留下裸 `"` 把整帧
JSON 弄废——`responses-stream-reasoning-turn1` 有两帧是这么坏的（已按原样补回反斜杠）。
jq 那份的 `scrub_paths` 不受影响：它作用在已解析的字符串上，看不到转义反斜杠。

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
- upstream：填 `source`——采自哪个上游、哪天，中转的话连它已知的偏差一起写（如
  `anthropic-*` 那句「input_tokens 恒含中转注入的 357」）。这一列不参与任何断言，
  它是给人看的：同一个目录树迟早同时躺着中转采的和官方直连采的样本（#37），
  到那时「这个数是谁报的」只能靠它区分，写在 README 里跟不着单个样本走。
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
