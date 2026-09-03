# 复盘：Responses `namespace` 工具被整包丢弃（GLM-5.2「无法调用已注册工具」）

复盘日期 2026-09-03，记录人 jinpenga。涉及 [#94](https://github.com/SimonGino/portage/issues/94)、[#95](https://github.com/SimonGino/portage/issues/95)、[#96](https://github.com/SimonGino/portage/issues/96)，寻路图 [#93](https://github.com/SimonGino/portage/issues/93)；余波与对照 mimo2codex 的修补记到口径层 v1.19。

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
| 余波 | 实采日志里 `server_tool` 裸着没带名字 → 丢弃名单空名退到 `type`（v1.18） |
| 对照 | PO 好奇 mimo2codex 怎么处理 `web_search`，拉下来进参考名单，逐条对照出三条我们没有的历史修补 |
| 修补 | 回带残缺入参救治、CC 出口孤儿 tool 消息、两出口缺失结果占位（v1.19），两个 opus 子代理并行落码 |
| 复查 | PO 问 CC↔Responses 转换有没有同款问题：四路 opus 只读审查，CC 入口工具那块落后一整轮，拉平并顺手修三处响应侧实洞（v1.20），响应解码侧无登记通道立待澄清 14 |

附带发现（与本题独立，各自单修）：Chat Completions 形态的 body 打到 `/v1/responses` 会转成 `messages=[]` 原样转发，上游报 `Messages cannot be empty.`；无 tools 时 `tool_choice=required` 被出口保护静默去掉。两条都归到口径层 v1.14 ⑦⑧ 的「转换后请求已不成立就自己回 400」。

## 定下的口径

八条决策见寻路图 #93 的 Decisions-so-far，要点：

- 摊平规则照 codex-rs：`functions` / 空 / 缺失是**默认命名空间**，子项对外用裸名（Codex 主路径三份 golden 字节不变）；其余用 `<命名空间名>__<子工具名>`。
- 摊平名须满足 `^[a-zA-Z0-9_-]{1,64}$`，不满足即 400，**不截断**（截断会制造新撞名）。
- 摊平后一名两源即 400，两个来源都点名。
- 回程：出向发 `namespace` 字段 + 裸名；回带先认字段、再本轮声明表唯一查表、对不上原样带过去不 400（历史 item 被拒客户端无法自救）。
- 命名空间名自身可含 `__`（ADE 实测 `mcp__ade_asset_knowledge`），所以还原**只能查每请求映射表**，任何按分隔符拆串都会拆错。
- R→Anthropic 同规则，摊平全部在入口 codec 做一次，两出口零差异。

## 修改清单（改了什么、为什么）

按提交顺序。每条只写「改动 → 原因」，口径编号指 `docs/口径层设计.md` §2.6。

### 第一批：把工具找回来（#94，`feec84c`）

| 改动 | 原因 |
| --- | --- |
| `openairesponses/namespace.go` 新增 `toolDecoder`，顶层 `tools` 与各个 `additional_tools` 灌进同一个解码器，`namespace` 外壳展开成子项 | 根因本身：外壳被当成 `ToolServer` 整包丢。展开后子项种类不变（`function` / `custom` 同规），外壳只贡献前缀 |
| 摊平名 `<ns>__<name>`；`functions` / 空 / 缺失是默认命名空间，子项裸名 | 照 codex-rs `is_default_namespace()`。默认命名空间裸名让 Codex 主路径三份 golden 字节不变；全裸名撞名靠运气，全前缀让 Codex 路径也变 |
| `Codec.namespaceTools` 每请求映射表（摊平名 → 命名空间名 + 裸名） | 命名空间名自身可含 `__`（ADE 的 `mcp__ade_asset_knowledge`），回程按分隔符拆串必错，只能查表 |
| 摊平名校验 `^[a-zA-Z0-9_-]{1,64}$`，不满足 400，不截断不改名 | CC 与 Anthropic 两家共同上限；这是我们拼出来的名字，拼出必被拒的名字再让上游报错归因是反的；截断会制造新撞名 |
| 摊平后一名两源 400，点名两个来源 | 不查重照带，回程只能靠覆盖顺序猜；自动改名发明规范没有的名字，回程 Codex 认不出 |
| 构造样本 `fixtures/in-responses-namespace-{collision,badname}` | 各钉一道闸 |

### 第二批：丢东西要有声音（#96，`01d279a`）

| 改动 | 原因 |
| --- | --- |
| `dropped` 从 `[]string` 换成 `protocol.Drops`，`server_tool` / `tool_grammar` / `tool_choice` 三档附工具名清单（上限 64） | 本次归因的另一半：整包丢 45 个工具，日志只有一句 `dropped=[server_tool]`。按种类去重，丢 45 与丢 1 长得一样 |
| 转换后 `messages` 为空 → 自家 400 `EmptyMessagesRejection` | 此前交上游裁，SGLang 的 `Messages cannot be empty.` 原样透出，渠道无辜却在流水里背「上游拒绝」 |
| `tool_choice=required` / 点名落空 → 400 `ToolChoiceRejection`；`auto` / `none` 落空静默省略但登记 | 此前 `required` 静默降成 `auto`，模型改回文本，客户端按「必有工具调用」写的代码当场崩且查不到因 |
| `relayConverted` 对 `*protocol.RequestError` 走入口协议外壳回 400，先打丢弃 Warn 再拒 | 看日志的人才知道 `required` 为什么会落空 |
| 三条规则扩到 Responses 出口（v1.15，`95e537b` / `6c6dcf0`） | 三个出口同一规则；R 出口原本点名落空降 `auto`、不登记 |

### 第三批：回程还原（#95，`dd16fd9`）

| 改动 | 原因 |
| --- | --- |
| 出向：`streamEncoder` 带同源 `namespaceTools`，`flushTool` 对摊平名只查表，命中的在四处事件里发裸名 + `namespace` 字段 | 真 GLM-5.2 实测：回带裸名 + `namespace` 后模型跟着历史叫裸名 `orchestrateTask`，调到声明表里没有的工具 |
| 回带：`restoreReplayNames` 先认自带 `namespace` 字段，再拿裸名在本轮声明表唯一查表补前缀；零中或多中原样带过去**不 400** | 历史 item 被拒客户端无法自救 |
| `tool_choice` 点名走同一张表 | 规范没有点名子项的形态，不发明私有扩展 |

### 第四批：取证（`2c16721`、`efd94c1`）

| 改动 | 原因 |
| --- | --- |
| `PORTAGE_DUMP_DIR` 排障采样：入站 / 上游请求 / 响应三份字节全文落盘，只走 env，启动 Warn 一次 | 回带形态设想了三种，只有真流量能说明 harness 发哪一种 |
| ADE 第二轮实采脱敏进 `golden/in-responses-namespace-turn2`，用例钉「17 次回带全部落回本轮声明表」 | 证实真实形态是裸名 + `namespace` 字段；另两种形态代码保留但只有构造样本 |

### 第五批：余波（v1.18，`a1bc61a`）

| 改动 | 原因 |
| --- | --- |
| `protocol.Tool.Label()`：名单里的工具名为空时退到 `type`，两个出口丢弃登记改用它 | 实采日志 `dropped=[thinking server_tool …]` 里 `server_tool` 裸着——Responses 的 `web_search` 声明本来就没有 `name`，`Drops.Add` 跳过空名，第二批立的名单在这类声明上失效 |

### 第六批：对照 mimo2codex 补的三条历史修补（v1.19，`99b3e93`、`39708ff`）

起因：PO 问 mimo2codex 怎么处理 `server_tool[web_search]`，把它拉进参考名单（`f297e90`）后逐条对照。它的 namespace 路线（不摊平、同名保留首个）**不采**：多 MCP 服务器同名子工具会静默丢。但它有三条针对「客户端历史已经坏了、客户端自己改不了、严格上游逐次 400 把会话砖死」的修补，我们没有：

| 改动 | 原因 |
| --- | --- |
| (a) Responses 回带的 `function_call.arguments` 不是合法 JSON 或为空 → 换成 `{}`，调用保留，`Codec.ArgsSalvaged()` 登记，relay 打 Warn | 上一轮流被截断时 Codex 会把半截入参永久存进历史，严格 CC 上游逐次 400 直到新开会话。**不删整条**：删了配对的 `function_call_output` 变孤儿。`custom_tool_call` 的自由文本入参不碰 |
| (b) CC 出口丢孤儿 `role=tool` 消息，登记 `orphan_result` | DeepSeek V4 撞到孤儿 tool 消息即 400。Anthropic 出口原本就有，CC 出口漏了 |
| (c) 两出口对「有调用、没结果」合成占位 `protocol.MissingToolResultPlaceholder`，登记 `missing_result` 记 call id；Anthropic 侧占位块排在下一条 user 消息前部，必要时另插一条 user | DeepSeek V4 与 Anthropic 都硬校验配对。与「不做伪映射」有张力，PO 复裁做：占位明说缺失、不伪装语义，补的是形状不是语义。先丢孤儿再补缺失，两者不互相制造对方要处理的情况 |
| `web_search` 映射成上游原生能力**不做**，立口径层待澄清 13 | mimo 能做是因为只服务一家上游、方言写死；我们缺渠道能力位与厂商方言表，没有这两样就映射等于对不支持的上游发它看不懂的字段 |

现有 golden 与 fixture 的调用 / 结果全部配平，(b)(c) 在它们身上惰性；真机残缺历史的样本还没有。

### 第七批：CC↔Responses 同款复查（v1.20，`d69b63a`、`550ae85`）

起因：PO 问「CC / Responses 转换是不是也有同样的问题」。四路 opus 只读审查（CC→R 出口请求编码 / CC 入口解码 / Responses 上游响应解码与 CC 响应编码 / 全路径静默丢弃穷举）。主干干净：孤儿 output 丢弃登记、空 input 400、tool_choice 三档、`store=false`、call_id 区分、index 重编、三种收尾、usage 明细、响应 id 都对。病灶在 **CC 入口解码**——工具那块落后 Responses 入口一整轮，与本次复盘同款：

| 改动 | 原因 |
| --- | --- |
| (a) CC 回带 `tool_calls[].function.arguments` 残缺（截断 / 空串 / 缺失 / 非字符串对象四档）→ `{}`，`openaicc.Codec.ArgsSalvaged()` 登记；relay 那条 Warn 改断言小接口，两入口共用 | 第六批 (a) 只修了 Responses 入口。CC→A 出口拿它当 `json.RawMessage` Marshal 报错是**我们自己 500**，比上游 400 更难归因 |
| (b) CC 的 `type:"custom"` 工具声明归 `ToolCustom`，`format` 进 Extras；`collectExtras` 的 known 集去掉 `type` | 官方 SDK 是 `Function \| Custom` union，new-api 由 Responses 转出真会发。此前整包丢且**丢时无名**——`type` 被吃进 known 集，第五批那条空名退 `type` 在 CC 入口是死的。和本次 namespace 整包丢弃是同一类事故 |
| (c) `tool_calls[].type=="custom"` 从 `custom.name` / `custom.input` 读，`ArgsIsJSON=false` | 只读 `function` 解出空名空参，出口发 `name:""` 的调用 |
| (d) Responses 出口服务端工具丢弃登记 `t.Name` → `t.Label()` | 第五批漏的一处，三出口从此同规 |
| Responses 响应解码与出口编码的单槽 `pending` 改 `map[int]` + 首次出现序；`response.incomplete` 在 `done` 前 flush 全部；CC 流式零入参补 `{"arguments":"{}"}` | 三处实洞，不立口径。两个 `custom_tool_call` item 同开时前者分片无声丢光；打断时半截入参连 `EvToolCallEnd` 一起消失；流式与非流式在「入参必是 JSON 串」上不对称。今天上游都串行放工具，线上走不到，是回归闸 |
| 响应解码侧无登记通道（`web_search_call` 等服务端 item、未列事件名、`refusal` 静默跳过）+ CC 入口 `tool_choice` 的 `custom` / `allowed_tools` 形态静默丢 + Responses 出口非 assistant 消息上的 `tool_calls` 不发却记配对表 → **不在本批修**，立口径层待澄清 14 | 三条都要先有通道再谈明细，形态与占位扩不扩到 Responses 出口是决策，等 PO 与 13 一起拍 |

golden 零改动：六份 `in-cc-*` 样本工具全是 `type:"function"` 且入参合法。

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
- 第六批三条修补只有构造样本。日志里出现 `orphan_result` / `missing_result` 档位或「回带历史里有残缺的工具入参」那条 Warn，就是真机撞上的信号，届时补实采。
- 第七批 CC 入口三件同样只有构造样本；那条残缺入参 Warn 两入口共用一句，靠 `calls` 里的 id 形态（`call_` 前缀是 CC/Responses 同源，分不出入口）不够，真要区分看同一条调用日志的入口协议。响应解码侧登记通道（待澄清 14）没立之前，上游自带搜索花的钱在日志里仍是零字。
- 顶层工具与默认命名空间子项同名的撞名（mimo2codex issue #20 证明 Codex 会这么发）今天按第一批规则 400。是否收窄成「同名同 schema 去重」等真机撞上再议，不预设。
