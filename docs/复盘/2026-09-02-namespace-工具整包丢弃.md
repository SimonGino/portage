# 复盘：Responses `namespace` 工具被整包丢弃（GLM-5.2「无法调用已注册工具」）

复盘日期 2026-09-03，记录人 jinpenga。涉及 [#94](https://github.com/SimonGino/portage/issues/94)、[#95](https://github.com/SimonGino/portage/issues/95)，寻路图 [#93](https://github.com/SimonGino/portage/issues/93)。

> 本文只记事：现象、归因、过程与教训。**不作实现依据**——口径以 `docs/口径层设计.md` §2.6 为准，实现细节以 `docs/MVP设计草案.md` §4/§5 为准。两者与本文冲突时以它们为准。

## 一句话

不是模型的问题，是我们自己把工具丢了——`namespace` 声明被当成服务端工具整包丢弃，客户端声明的 55 个工具只剩 10 个到达上游。

## 现象

客户经 Portage 用 Responses 协议接 GLM-5.2（SGLang，Chat Completions 上游），工具「无法识别」。同一接入点下 Qwen3.8 正常，客户反馈 GLM 前一天也正常。

排除项（报障方已自证）：GLM-5.2 直连 `/v1/chat/completions` 注册单个 `get_weather` 能正常返回 `tool_calls` + `finish_reason=tool_calls`，所以 GLM 支持 function calling、`glm47` parser 配置无误、SGLang 基础链路正常。

## 归因

`type=namespace` 不是工具种类，是**声明的分组外壳**：子项进壳前是 `function` / `custom`，出壳还是；外壳只贡献名字。

我们把它当成了「认不得的工具种类」：

- `internal/protocol/openairesponses/decode.go` 的 `decodeTools` 把整个外壳归成 `ToolServer`，子项落在 `Extras["tools"]` 里没人读；
- `internal/protocol/openaicc/encode.go` 与 `internal/protocol/anthropic/encode_request.go` 对 `ToolServer` 一律丢弃。

结果是整个命名空间连同子项消失。真机复现的数字：ADE 的 55 个工具丢 45 个，`input_tokens` 从 159 掉到 19，模型无工具可调，编了个不存在的工具名以纯文本吐出。

兜底逻辑本身没错（认不得的种类要丢，否则 decode 不是全函数）。**错在兜底太安静**：整包丢 45 个工具，日志里只有一句 `dropped=[server_tool]`，既不报数也不点名。客户看到的是「模型不会用工具」，我们看到的是一行看不出严重性的日志，中间隔了三层。

## 时间线

| 阶段 | 发生了什么 |
| --- | --- |
| 报障 | 客户反馈 GLM-5.2 工具「无法识别」，Qwen3.8 正常 |
| 初判（错） | 交接材料里的最高优先级怀疑是 `input → messages` 转换让 `messages=[]` |
| 真机复现 | 打出转换后的请求体：`messages` 正常，**`tools` 从 55 掉到 10** |
| 定位 | `namespace` 归 `ToolServer` → 两个出口整体丢弃 |
| 定口径 | 开寻路图 #93，八张决策票逐项交 PO 拍板 |
| 实现 | #94 摊平出向、#95 回程还原，随 v0.4.8 发布 |
| 取证 | 加 `PORTAGE_DUMP_DIR` 全文落盘开关，公网部署跑真实 ADE，两轮抓包 |
| 收口 | 第二轮实采脱敏进 golden，#95 关票 |

附带发现（与本题独立，各自单修）：Chat Completions 形态的 body 打到 `/v1/responses` 会转成 `messages=[]` 原样转发，上游报 `Messages cannot be empty.`；无 tools 时 `tool_choice=required` 被出口保护静默去掉。两条都归到口径层 v1.14 ⑦⑧ 的「转换后请求已不成立就自己回 400」。

## 定下的口径

八条决策见寻路图 #93 的 Decisions-so-far，要点：

- 摊平规则照 codex-rs：`functions` / 空 / 缺失是**默认命名空间**，子项对外用裸名（Codex 主路径三份 golden 字节不变）；其余用 `<命名空间名>__<子工具名>`。
- 摊平名须满足 `^[a-zA-Z0-9_-]{1,64}$`，不满足即 400，**不截断**（截断会制造新撞名）。
- 摊平后一名两源即 400，两个来源都点名。
- 回程：出向发 `namespace` 字段 + 裸名；回带先认字段、再本轮声明表唯一查表、对不上原样带过去不 400（历史 item 被拒客户端无法自救）。
- 命名空间名自身可含 `__`（ADE 实测 `mcp__ade_asset_knowledge`），所以还原**只能查每请求映射表**，任何按分隔符拆串都会拆错。
- R→Anthropic 同规则，摊平全部在入口 codec 做一次，两出口零差异。

## 实采证据

PO 开 `PORTAGE_DUMP_DIR` 在公网部署上抓真实 ADE 流量，第二轮请求脱敏后入库为 `testdata/golden/in-responses-namespace-turn2`：72 个工具声明（9 个命名空间 / 61 个子工具 / 10 个顶层 function / 1 个 web_search），17 次回带调用。

结论：真实 harness 把命名空间子工具的历史调用写成**裸子名 + `namespace` 字段**，17 次里 4 次如此，摊平名一次都没出现。此前设想的三种回带形态代码全收，但只有这一种有实采背书。

## 做对的

- **先真机复现再动手**。交接材料给的方向（`messages=[]`）看着很合理，一打请求体就知道走岔了。
- **口径与实现分开**。八个决策先在寻路图里逐条落定，实现时没有一处需要现场发明规则。
- **查参考仓库，不凭记忆**。摊平规则、默认命名空间 `functions` 的特例、回程带 `namespace` 字段，三条都是从 codex-rs 与 sub2api 读出来的。
- **拿真流量收尾**。命名空间名含 `__` 这一条只有看到真实声明才想得到——按分隔符拆名的实现全错。

## 走了弯路的

- **第一轮采样开太大**：全局开关，315 个文件 28MB，全是客户完整对话；而且那一轮模型只回散文，没触发工具调用，等于白抓。
- **减量建议给错了**：曾建议「只对那个接入点开采样」，但这个开关没有按模型或接入点过滤的能力。纠正后走「另起一个专用容器」，第二轮 49 个文件 2.1MB 就拿到了全部证据。
- 采样开关的粒度应当在做之前想清楚，而不是先做全局再补救。

## 教训

1. **静默丢弃是最贵的 bug**。丢东西时日志必须带数量和名字清单——已写进口径层 v1.14 ⑨（工具类三档带名单，渲染在 `protocol.Drops.LogValue`）。
2. **别信交接材料的结论，信它的现象**。本次现象全对、归因全错。
3. **协议形态问题，构造样本证明不了任何事**。三种回带形态都实现了，但只有真流量能说明 harness 实际发哪一种。
4. **排障采样开关要一次想清三件事**：怎么开、怎么关、抓的是谁的数据。第三条决定了它必须是临时的。

## 遗留

- 两类样本缺口只有构造样本：**摊平撞名 400**、**具名命名空间内的 custom 子项**。
  - 撞名有现成信号，不需要盯：服务端 `入站请求被拒` WARN 带 `code=invalid_value` 与 `param`，调用日志收场为 `rejected`，客户端拿到的 400 正文点名两个来源。
  - 具名命名空间内 custom 今天**没有任何信号**（不报错、不丢弃、不打日志，与顶层 custom 共用同一条 `decodeTool`）。要判断真机有没有出现只能重开采样 grep。缺的是样本覆盖而非正确性风险，暂不为它常开开关。
- `PORTAGE_DUMP_DIR` 抓下的 dump 目录是客户完整对话原文，取证完成后即关，不留存。
