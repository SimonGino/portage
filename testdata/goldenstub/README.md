# 应答脚本（stub）

goldenrec 入站模式用的**假上游响应**。它们是道具，不是样本。

## 为什么需要它

M2 的 `A→CC` / `R→CC` 要的入站 golden，最难啃的那份是**第二轮**请求——带 `tool_result`
（Anthropic）或 `function_call_output`（Responses）的那个包。而 harness 只有先收到过一个
合法的 tool 调用响应，才会去执行工具、才会发出第二轮：

```
第 1 轮  harness ──► goldenrec   "帮我看看这个文件"          ← 录到样本 1
                 ◄──             01-tool_use.sse（stub）
         harness 本地执行工具
第 2 轮  harness ──► goldenrec   [原对话 + tool_use + tool_result]  ← 录到样本 2 ★
                 ◄──             02-final.sse（stub）
```

手上没有对应协议的真实上游 key，所以中间那两格由手写 stub 顶上。

## 三条铁律

1. **stub 不进 `testdata/golden/`。** 入库的只有 harness 发出来的 `request.json`，那仍是
   100% 真实字节；stub 是驱动流程的道具，一旦混进转录库就等于往事实里掺伪造。
2. **stub 不需要保真。** 它只要合法到 harness 肯接受并执行工具即可。不合法当场就报错，
   看得见。
3. **有真实上游就别用它。** 直连真上游采到的是真响应，顺带把出站样本也一起采了
   （proxy 模式）。stub 只是「没 key 也要把 M2 推起来」的替代路径。

## 怎么用

```bash
GOLDENREC_MODE=inbound \
GOLDENREC_PROTOCOL=anthropic \
GOLDENREC_STUBS=./testdata/goldenstub/anthropic-tool-round \
go run ./cmd/goldenrec
```

另一个终端把 harness 指过去，跑一遍目标场景：

```bash
ANTHROPIC_BASE_URL=http://127.0.0.1:8318 claude     # Anthropic 入站
OPENAI_BASE_URL=http://127.0.0.1:8318 codex          # Responses 入站
```

样本落在 `testdata/golden/raw/in-*`，那是暂存区、不进 git。人工脱敏核对后按
`testdata/golden/README.md` 的规矩移进转录库。

## 现有脚本

| 目录 | 协议 | 场景 |
|---|---|---|
| `anthropic-text/` | anthropic | 纯文本单轮 |
| `anthropic-tool-round/` | anthropic | 单工具调用整轮（两轮请求） |
| `anthropic-parallel-tools/` | anthropic | 并行工具调用整轮（两轮请求） |
| `responses-text/` | openai_responses | 纯文本单轮 |
| `responses-tool-round/` | openai_responses | 单工具调用整轮（两轮请求） |
| `responses-parallel-custom-tools/` | openai_responses | **并行** `custom_tool_call` 整轮（两轮请求） |

`responses-parallel-custom-tools/` 是「有真实上游也得用 stub」的那种例外，值得单独说：

Codex CLI 0.144 走的是 **code-mode 工具**——它只声明一个叫 `exec` 的 `custom` 工具，入参
是一段 JavaScript，模型要并行就在 JS 里写 `Promise.all`。于是并行发生在**工具入参内部**，
线上永远只有一个 `custom_tool_call` 项。实测拿真实上游反复诱导也逼不出并行项
（响应里 `parallel_tool_calls` 恒为 `false`）。

`R→CC` 又必须回答「一轮里多个工具调用怎么映射」，样本不能没有。所以这一格由 stub 顶：
脚本一次吐两个 `custom_tool_call`（`output_index` 1 与 2，两个 `call_id`），Codex 收到后
真的并行执行、真的回了两个 `custom_tool_call_output`——**要的那份第二轮请求字节仍是真的**。

顺带两个 `R→CC` 要处理的形状，都是这么才看见的：`custom_tool_call` 只有自由文本 `input`、
**没有 `arguments` 对象**（CC 那边 `function.arguments` 要求 JSON 串）；
`custom_tool_call_output.output` 是 `input_text` **数组**而不是字符串。

## 规则

- **按文件名排序依次发**，一个请求消耗一个。所以命名带序号前缀（`01-` / `02-`）。
- 扩展名决定回法：`.sse` → `text/event-stream` 逐帧 flush，`.json` → `application/json`。
- **发完就报 503 收场**，不循环重放——静默重放会让 harness 原地打转，而「脚本不够长」
  正是该立刻看见的事。要重跑就重启 goldenrec。
- `/v1/messages/count_tokens` 由 goldenrec 就地估算，**不消耗脚本里的一格**。Claude Code
  每轮都打它，让它吃掉一格会把后面几轮全串位。
- 没预料到的端点回 404 并打日志，同样不消耗脚本。

## harness 不认这个 stub 怎么办

大概率是工具名或参数形状对不上——stub 里写的 `Read`（Claude Code）、`shell`（Codex）
取自各自 harness 的常见工具集，但**版本一变就可能不同**。

排法很直接：第 1 轮的 `request.json` 已经录下来了，里面 `tools` 数组就是这个 harness
这个版本实际声明的工具清单。照着它改 stub 里的 `name` 与参数即可。

Codex 侧另外要留意：它对 Responses 事件的字段完备性很严
（`sequence_number` / `output_index` / `content_index` / `call_id` 缺一不可，即便值是 0
或空串），删字段会被判为非法事件。这几个脚本已按此写全，改的时候别顺手删。
