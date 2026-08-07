# 个人 AI 模型网关 MVP 设计草案

> 状态：草案 v0.25
> v0.25 变更（#10 M2-1 canonical 模型定稿，2026-08-07）：§4 重写、§5 接口定稿并落骨架，均为实现层，口径不变。原 v0.2 的 canonical 草案照协议文档拍，本次拿 9 份真实 harness 入站样本逐字段核过，**草案被证伪四处**（§4.3）：`System string` 装不下带 `cache_control` 断点的 system 数组；role 集合装不下 Anthropic mid-conversation-system beta 塞在 messages 中段的 system 消息；`Tool` 的 name/description/JSON-schema 三件套装不下 Codex 的 lark 文法 custom 工具与 Claude Code 的服务端工具；`EvToolArgsDelta{JSONFragment}` 建立在「工具入参必是 JSON」这个不成立的不变量上（Codex code-mode 的入参是 JS 源码）。同时立两条规矩：①**装得下 ≠ 转得过去**——decode 必须是全函数，跨协议丢什么是 encode 侧的决策，「记为丢弃」与「无处存放」不是一回事，§4.4 列显式丢弃清单及代价；②逐键路径的归宿清单**只存在于 `internal/protocol/canonical_coverage_test.go`**，文档不抄第二份（两份必漂移），该测试双向红，写表时当场逮出漏掉的字段。§5 补两条实测坑（工具入参非 JSON 时编码到 CC 的后果、Codex 并行只发生在 code-mode 内部故不能拿它验交错重组）。其中两处提交 PO 拍板并获确认（jinpenga）：Responses `developer` 角色 decode 归一为 `RoleSystem`（R 出口方向再展开回 `developer`），以及 §4.4 那三项显式丢弃。修改人 jinpenga。
> v0.24 变更（#20 修复，2026-08-07）：§6.1 补「客户端查询串整串照抄」——透传口径原文只规定了 body（「除顶层 `model` 值外逐字节相等」）与请求头白名单，查询串既不在白名单也不在丢弃清单里，是**漏项**不是裁决过的行为。PO 裁定不过滤、整串照抄（jinpenga）：查询参数不像请求头那样天然带客户端指纹，且各家 harness 的私有参数不可穷举，白名单在这里没有可枚举的对象。
> v0.23 变更（M2-1 入站样本实采回写 #10，2026-08-07）：两条实测观察落档，均为实现层，口径不变。①§6.1 白名单段补**反例**——某些中转站的 Anthropic 端点靠 `user-agent` + `x-app` 判定客户端，白名单转发一律 503。结论仍是**白名单不放宽**（为迎合一家中转站撤掉「不泄露本机指纹」这条口径，代价与收益不对等），绕法在配置层：Anthropic 配一条不设该闸的独立上游。顺带说明 goldenrec「转发照抄、落盘白名单」为何不算双标——防指纹外泄的对象是 git 仓库不是上游。②§9 补 `log_bodies` 的实测量级——Claude Code 2.x 单轮请求体 **185 KB**（42 个 tool 定义占大头），Codex CLI 0.144.1 是 47~50 KB，即 64 KiB 上限对前者是**几乎必截断**而非偶尔越过。不改 `bodyCaptureLimit`（排障日志该有这个上限），改的是读日志时的预期：`truncated` 在真实 harness 下是常态不是故障信号。
> v0.22 变更（M2-1 入站样本采集 #10，2026-08-06）：§9 补「入站样本」这一类——golden 库自此分 `direction: upstream | inbound` 两类，`cmd/goldenrec` 随之加 inbound 模式。新决策一条：**没有对应协议的真实上游时，用手写 stub 应答驱动 harness 走完多轮**（PO 裁定 jinpenga）。依据 = `A→CC` 最难啃的输入是第二轮那个带 `tool_result` 的请求体，而 harness 只有先收到过一个合法 tool 调用响应才会发出它；手上没有 Anthropic / OpenAI 官方 key（#7 仍挂着），纯录制回 501 只能采到第一轮。stub 是道具不是样本：不保真、不进转录库，入库的只有 harness 发出来的真实请求字节。
> v0.21 变更（M2 同候选退避重试落地 #13，2026-08-06）：v0.16 定的口径实现化，均为实现层，口径不变。①§6 重试段的「次数：可配，默认值随 M2 实测定」定稿为 `max_retries: 2` / `base_delay: 500ms` / `max_delay: 10s`（量级对齐 M0 实测的 Codex 5xx 退避 0.22→0.45→0.84→1.62s）；②补两条实现期新决策——退避抖动取**半区间** `[d/2, d)` 而非全区间（全区间会让实际退避短于上游明说的 `Retry-After`），`Retry-After` 超过 `max_delay` 时**不重试**而非照等（照等等于把客户端扣在网关一分钟，不如把那份 429 原样交出去让调用方自己决定）；③§7 配置补 `retry` 块并说明「块缺席 = 用默认、显式写 0 = 关闭」；④§7 `call_logs.retry_count` 注明对应的结构化日志字段 `retries`（仅非 0 时记，0 是常态、每行都背个恒 0 字段没意义）。
> v0.20 变更（M0 遗留修复 #14，口径层 v0.21，2026-08-06）：①§6.1 超时分层补上**拨号**这一层——零值 `http.Transport` 用无超时的 `net.Dialer`，后面几个超时都在 TCP 连上之后才起算，地址被黑洞时请求挂到操作系统放弃。②§7 启动校验补一条——未停用渠道的 `base_url` 必须是带 host 的绝对 http/https 地址、且不带查询串与 fragment（后者会把协议子路径整个吞掉，请求永远打错地方），且该错误**不回显 `base_url` 原值**（可能带 userinfo）。两条均由 #8 / #15 的自动审查提出、人工核实后修复；②是口径层 v0.18「配置能过校验但请求时才炸不算合法状态」的同类缺陷换个写法，口径层同步 v0.21 把该原则从枚举改为通则。
> v0.19 变更（M0 收官，2026-08-06）：§6.1「放弃解析」的粒度定稿为**丢那一帧**（v0.13 提出时标的待裁，PO 裁定 jinpenga），该段由待裁改为定稿。
> v0.18 变更（M0 验收回写，2026-08-06）：三条实测与 #1 规格的出入落档，均为实现层，口径不变。①§6.1 头部白名单加实测复核结论——录下 Codex 全部请求头逐条对照后**不放宽**白名单（私有头丢弃不影响整轮工具调用，且 `X-Codex-Turn-Metadata` 携带 `installation_id`）。②§8 `/v1/models` 注明**不迎合** Codex 期待的 OpenAI 私有目录格式（无公开契约、字段随版本漂移；实测降级路径可用，只多两条 warning）。③§5 坑清单补 Responses reasoning 的 `encrypted_content`——上游侧不透明密文，P1 跨协议转换必然作废，M2 须有用例钉住「带它的 input 不使转换报错」。
> v0.17 变更（口径层 v0.20，2026-08-06）：harness 分档修订——CC 入口的必过 harness 由 Codex CLI 改为 pi（§0 占位假设 7、§10 验收清单）。依据 = Codex CLI 0.144.1 移除 `wire_api = "chat"`（openai/codex#7782），实测 `Error loading config.toml: wire_api = "chat" is no longer supported`；pi 0.83.0 已经网关跑通 CC 整轮工具调用（#6）。
> v0.16 变更（口径层 v0.19，2026-08-06）：恢复同候选退避重试并从 M4 提前到 M2——§6 故障转移流程加最内环（429/5xx/网络错误且未写首字节触发，401/403 与其余 4xx 不重试，退避以 `Retry-After` 为下界）；§9 测试矩阵作废「harness 自身重试逻辑生效」一条；§11 M2/M4 描述随之调整。依据 = M0 验收实测（#6）：网关侧 429 逐字节透传成立，但 Codex 0.144.1 对 429 一次即弃、对 503 才退避，单上游限流时无人自愈。PO 裁定只取「同候选重试」，不放开临时闸单候选、候选间转移仍留 M4（jinpenga）。
> v0.15 变更（M0 联调期，2026-08-06）：§7 启动校验的可达性判定扩到**渠道 / 纳管模型 / 凭证三者**，与 `Resolve` 的 JOIN 逐条对齐。v0.14 只覆盖渠道与凭证，漏了 `channel_models.disabled`——同一类错三种写法只拦两种；PO 裁定「纳入」（jinpenga）。口径层同步 v0.18。
> v0.14 变更（M0 联调期，2026-08-06）：§7 启动校验补一条——未停用接入点的 weight>0 候选，其渠道必须未停用且有启用凭证。原规则字面上不含这条，实测「只停渠道、忘了停接入点」能通过启动、接入点仍挂在 `/v1/models` 上、请求时才 503；PO 裁定改为启动即报（jinpenga）。
> v0.13 变更（M0-4 实现期，2026-08-05）：§6.1 明确超限判定与分块无关、补 Tap 两条硬约束的实现落点；§9 补 golden 样本库的目录结构、`cmd/goldenrec` 录制流程与 `verified` 人工关卡——以上为实现层展开，口径不变。另：§6.1 「放弃解析」的粒度（丢那一帧 vs 丢整条流）是对验收条款的重新解读，已按「丢那一帧」实现并标注**待 PO 裁定**，未按口径变更处理。
> v0.12 变更（M0-3 实现期，2026-08-05）：`GET /v1/models` 实现落在 `internal/server` 而非 §3 列出的 `internal/admin/`，理由与待办见 §3 脚注。仅记实现偏离，口径不变。
> v0.11 变更（M0-1 实现期，2026-08-05）：§6.1 补「模型名翻译走字节级 splice」——PO 裁定接入点对外名 → 纳管模型名的改写在 M0 就做，透传保真口径精确为「除顶层 `model` 值外逐字节相等」。另：路由解析（`Resolve`）实现落在 `internal/store` 而非 §3 列出的 `internal/router/`，理由与待办见 §3 脚注。
> v0.10 变更（M0 开工前实现层展开，2026-08-05）：临时闸校验时机拆为「启动时校验单候选单凭证 / 请求时校验协议匹配」——接入点本身不绑协议，协议匹配只能到请求时才判（§7）；golden 样本子集提前到 M0 采集，因 Tap 测试需真实转录作输入（§9/§11）；新增 §6.1 透传实现细则（上游 URL 拼接、请求/响应头规则、流式转发与超时分层）。均为实现层展开，口径不变。
> v0.9 变更（口径层 v0.17，2026-08-05）：初始渠道集改为 Anthropic / OpenAI / Gemini Vertex AI / 阿里百炼（oMLX、DeepSeek、硅基流动移出）；Vertex 与百炼走 OpenAI 兼容端点（协议矩阵不动）；渠道凭证类型 `api_key` / `service_account` 二选一，key 池泛化为凭证池（§0/§7/§11/§12）。
> v0.8 变更（口径层 v0.13~v0.16，2026-08-05）：harness 验收分档（必过：Claude Code、Codex CLI；顺带：pi、OpenCode）；管理员 session 鉴权细则、全局限流配置项、key 前缀 `sk-aig-` 落定（§0/§7/§8/§10）。
> v0.7 变更（口径层 v0.12，2026-08-05）：候选改绑「渠道纳管的模型」（新增 `channel_models` 表，candidates 引用之）；里程碑重排——多候选分流/候选间转移/key 池聚合实现打包 M4 置于 M3 后，M0~M2 强制单候选单 key；占位假设 #2 关闭（§0/§6/§7/§11）。
> v0.6 变更（口径层 v0.11，2026-08-05）：占位假设 #1 关闭——v1 初始渠道集定为主流五渠道；渠道多 key 聚合（`channel_keys` 表 + key_mode + 401/403 摘 key）；§6 故障转移补 key 层内环（§0/§6/§7）。
> v0.5 变更（口径层 C4 收敛，2026-08-05）：候选间故障转移定稿——429/5xx/网络错误/连接超时且未写首字节触发，剔除失败候选后剩余重新归一化权重再抽，其余 4xx 不切，无同候选重试，A-14 D3 范式无跨请求状态（§1/§6/§11）。
> v0.4 变更（口径层 C2 收敛，2026-08-05）：配置载体由 YAML 改为「最小启动配置 + 业务配置（渠道/接入点/key）全 DB」，React 管理端入 v1（M3，含渠道/接入点写界面——PO 日常高频编辑渠道）；管理端就绪前用 SQL 手工维护。里程碑与口径层统一为 M0~M3（§0/§1/§3/§7/§8/§11）。
> v0.3 变更（口径层 C1/C3 收敛，2026-08-05）：①协议转换确认属 **v1 承诺范围**，P1 = v1 内后续里程碑而非另立项，分批 ①~④ 按口径层 §2.1 优先级；跨协议校验降格为临时闸。②路由模型重写为「接入点 + 候选（渠道，上游模型名，权重）加权随机」，`routes`/「模型映射表」术语退役（§0/§2/§6/§7/§11）。
> v0.2 变更：协议转换从 P0 降为 P1。设计态考虑保留四项交付物：架构 seam（§6）、canonical 事件模型（§4）、codec 接口与坑清单（§5）、golden 素材（§9），均按定稿标准维护。
> 定位：个人使用的 AI 模型网关，参考 new-api 的转发内核重写，只做转发 + 协议转换 + key 鉴权 + 调用日志，不含任何运营功能。

## 0. 待确认的占位假设

以下四项中，#1/#2/#7 是起草时的默认假设（**正式开工前须替换为真实值**），#9 已决策定稿：

| # | 项目 | 当前假设 | 待确认 |
|---|------|---------|--------|
| 1 | ~~上游渠道~~（已决 v0.17） | v1 初始集：Anthropic 官方、OpenAI 官方、Gemini Vertex AI（OpenAI 兼容端点 + SA 凭证）、阿里百炼（OpenAI 兼容）；渠道多凭证聚合见 §7 `channel_keys` | 已决 |
| 2 | ~~接入点清单~~（已决） | 运营数据不冻结。候选 = 渠道纳管模型 + 权重（§7）。M0 验收集：`claude-sonnet-4-5`→Anthropic 官方；一个 CC 透传接入点→百炼（qwen 系）或 OpenAI 官方，各单候选 | 已决 |
| 7 | ~~目标 harness~~（已决） | 必过档：Claude Code（Anthropic 入口）、Codex CLI（Responses 入口）、pi（CC 入口）——三者一人盖一条入口协议，挡里程碑验收；顺带档：OpenCode（不挡，坏了再修）。v0.20 修订：Codex CLI 0.144.1 移除 `wire_api = "chat"`，CC 入口改由 pi 承担 | 已决 |
| 9 | ~~转换方向优先~~（已决） | 协议转换属 v1 承诺，P0 仅同协议透传，P1 按口径层 §2.1 优先级分批；设计态考虑见 §2 | 已决 |

其余采用默认值：单用户（单管理员）、最小启动配置 + 业务配置全 DB、React 管理端（M3）、每 key 可限 allowed_models、默认不记录请求体。

## 1. 目标与非目标

**目标**

- 对外提供 3 种协议入口：OpenAI Chat Completions、OpenAI Responses、Anthropic Messages
- 上游支持同三种协议出口；**P0 仅同协议透传，协议转换为 P1（属 v1 承诺范围，非另立项）**（设计态考虑，见 §2/§4/§5）
- 同候选退避重试（429/5xx/网络错误且未写首字节触发；v0.19，实现在 M2；详见 §6）与候选间故障转移（同触发条件加连接超时；剔除失败候选、重新归一化加权再抽；实现在 M4；详见 §6）
- API key 鉴权（可多张 key，区分调用来源）
- 调用日志（含 token 用量，用于排障与自查用量）
- React 管理端（M3）：渠道 / 接入点（候选+权重）/ key / 用量查询，构建产物 embed 进单二进制

**非目标（明确不做）**

多用户与注册、额度/计费/支付/兑换码、上游模型列表自动同步、批处理、文件上传、音频、图像生成、模型微调。

## 2. 协议支持矩阵与分期

入口 × 出口 共 9 格：

| 入口 ↓ / 出口 → | Anthropic | Chat Completions | Responses |
|---|---|---|---|
| Anthropic Messages | **P0 透传** | P1-① 转换 | P1-④ 转换 |
| Chat Completions | P1-③ 转换 | **P0 透传** | P1-③ 转换 |
| Responses | P1-② 转换 | P1-① 转换 | **P0 透传** |

- 分批号 ①~④ 即口径层 §2.1 实现优先级：**①** A→CC、R→CC（主诉求：harness 挂第三方便宜模型）；**②** R→A（Codex 用 Claude）；**③** CC→A、CC→R；**④** A→R（允许滑到最后）。
- 首批特性集 = 纯文本 + tool calls（含并行调用）+ system prompt + 停止原因 + usage；图片、count_tokens 估算、thinking 精细策略等横切增强随 ③④ 批排期。
- Responses 无状态化（`previous_response_id` 处理）随 ① 的 R→CC 一并落地。

**「设计态考虑」落为三条硬约束：**
1. **管线 seam 现在就定型**（§6）：同协议走原始字节透传，异协议走 canonical 编解码；P1 只是填充后一路，seam 位置不变。
2. **canonical 事件模型与 codec 接口现在就按定稿标准写**（§4/§5），P1 开工不重设计。
3. **golden 样本 M1 就采集**（§9），刻意选「同语义、双协议」场景，天然构成 P1 转换的黄金输入对。
- 有损转换策略（已决）：
  - thinking / reasoning：跨协议**丢弃 + 记日志警告**，不做伪映射；同协议透传保留。
  - cache_control：仅「出口为 Anthropic」时保留；转往其他协议时静默剥离。
  - temperature：Anthropic 区间 0~1，OpenAI 0~2，转换时 clamp。
  - count_tokens：P0 上游非 Anthropic 时返回 501 风格错误；P1 做字符估算。

## 3. 模块划分

```
cmd/gateway/main.go        # 装配：config → store → server
internal/config/           # 最小启动配置加载；业务配置读 DB，校验 + 变更热生效
internal/auth/             # API key 中间件：hash 校验、allowed_models 过滤
internal/router/           # 模型名 → 有序渠道列表解析
internal/protocol/         # canonical 事件模型（P0 定稿，§4）；codec 接口（P0 定稿，P1 实现，§5）
  internal/protocol/anthropic/      # 每协议包内两层：Tap（P0）+ Codec（P1）
  internal/protocol/openaicc/
  internal/protocol/openairesponses/
internal/convert/          # canonical 之间的请求级归一（其实是 codec 内部实现细节）
internal/upstream/         # HTTP client、SSE 读取、failover 驱动
internal/logging/          # 调用日志写库、查询
internal/store/            # SQLite：channels、access_points、candidates、api_keys、call_logs
internal/admin/            # /healthz、管理端 API（渠道/接入点/key CRUD、用量查询，M3 扩全）、React 静态资源 embed
```

关键模块职责：

- **`protocol/<proto>`**：包内两层——
  - **`Tap`（P0，最深模块之一）**：旁路解析同协议透传流，只提取 usage / 模型 / stop reason 供日志，只读不改流。透传保真优先级最高——P0 **不做** decode→encode 转码，避免 canonical 模型丢字段。
  - **`Codec`（P1，最深模块）**：实现 §5 接口，协议怪癖（tool call 增量重组、stop reason 映射等）全封在里面；P1 落地时 Tap 复用 Codec 的解码器。
- **`upstream`**：负责把一次「canonical 请求 + 渠道」打成真实 HTTP 调用，返回事件流或错误；驱动 failover。
- **`router`** 与 **`auth`** 保持浅薄，不藏逻辑。

> **实现偏离待裁（v0.11）**：M0-1 把接入点解析（`Resolve`，返回命中候选 + 其渠道连通信息）实现在 `internal/store` 而非本节列出的 `internal/router/`。理由：临时闸下解析就是一条 SQL，单开一个只做转调的包是空壳。代价：`store` 同时管 schema、启动校验与解析，职责在发散。M4 上多候选加权分流时解析会长出真正的逻辑，届时要么拆出 `internal/router/`、要么本节按实际改写——请 PO 在 M4 排期时一并裁定。
>
> **实现偏离待裁（v0.12）**：M0-3 把 `GET /v1/models` 实现在 `internal/server` 而非本节列出的 `internal/admin/`。理由：它是 harness 走网关 key 打的**业务**端点（Claude Code / Codex CLI 启动时拉模型列表），与管理端 CRUD 不是一类东西——放 `admin` 会让「业务面 vs 管理面」的边界糊掉。M3 上管理端时请 PO 确认：`admin` 只收管理面（`/healthz` 亦然待定），业务面的模型列表留在 `server`。

## 4. 内部事件模型（canonical events）

> **v0.25 定稿（#10 M2-1）。** P0 透传路径不经过本模型（由 Tap 旁路解析），本节锁定 M2 转换的语义底座。
>
> 原 v0.2 草案照协议文档拍，本次拿 9 份**真实 harness 入站样本**（Claude Code 2.x 五份、Codex CLI 0.144 四份）逐字段核过，草案被证伪四处，见 §4.3。
>
> 代码：`internal/protocol/request.go`、`internal/protocol/event.go`。逐键路径的归宿清单在 `internal/protocol/canonical_coverage_test.go` 的 `coverage` 表，**它是穷举的事实源，本节只讲为什么**——路径清单不在文档里抄第二份，两份必然漂移。该测试双向红：样本冒出表上没有的路径红，表上留了样本已无的路径也红。

贯穿全模型的一条原则：**装得下 ≠ 转得过去**。canonical 层的职责是让 `DecodeRequest` 成为**全函数**——任何该协议的合法入站字节都有地方放；跨协议丢什么，是 encode 侧按口径做的决策。「记为丢弃」和「无处存放」是两件事，前者是决策，后者是缺陷。

四类归宿，`coverage` 表逐路径标注：

| 归宿 | 含义 |
|---|---|
| `field` | 有对应的 canonical 结构体字段，跨协议能转 |
| `extras` | 进 `Extras`，同协议原样取回，跨协议由 encode 侧按口径决定丢不丢 |
| `opaque` | 整棵子树按原始字节保留（JSON Schema、lark 文法、工具入参），不解开也不下钻——键序与数值精度都可能影响上游行为 |
| `dropped` | 显式丢弃，且本节 §4.4 写明后果 |

### 4.1 请求侧

```go
type Request struct {
    Model       string
    System      []Block      // 块序列，不是字符串——块上带 cache_control 断点
    Messages    []Message
    Tools       []Tool
    ToolChoice  ToolChoice   // Mode: ""|auto|none|required|tool
    MaxTokens   int          // Anthropic 必填；OpenAI 来源零值时由 default_max_tokens 补
    Temperature *float64
    Stop        []string
    Stream      bool
    Extras      map[string]any
}

type Message struct {
    Role    Role      // system / user / assistant / tool
    Content []Block   // 纯字符串 content 退化为单个 text 块
    Extras  map[string]any
}

type Block struct {
    Kind       BlockKind    // text / thinking / tool_use / tool_result / image(M2 未实现)
    Text       string
    ToolCall   *ToolCall    // Kind==tool_use
    ToolResult *ToolResult  // Kind==tool_result
    Extras     map[string]any  // cache_control / signature / encrypted_content
}

type ToolCall struct {
    ID, Name   string
    Args       string       // 原样载荷，不预设是 JSON
    ArgsIsJSON bool
    Extras     map[string]any
}

type ToolResult struct {
    ToolCallID string        // A tool_use_id / CC tool_call_id / R call_id，原样携带不重编号
    Content    []Block       // 字符串 content 退化为单块
    IsError    bool
}

type Tool struct {
    Kind        ToolKind        // function / custom / server
    Name        string
    Description string
    Schema      json.RawMessage // 仅 function
    Extras      map[string]any  // format(lark) / strict / type / model
}
```

角色映射，三协议对照：

| canonical | Anthropic | CC | Responses |
|---|---|---|---|
| `system` | `messages[].role=="system"`（会话中段，见 §4.3）与顶层 `system` 块 | `role=="system"` | `role=="developer"` |
| `user` | `user` | `user` | `user` |
| `assistant` | `assistant` | `assistant` | 输出侧的 `message` 项 |
| `tool` | 无——工具结果是 user 消息里的 `tool_result` 块 | `role=="tool"` 独立消息 | 无——`custom_tool_call_output` / `function_call_output` 独立 input 项 |

`developer` 是**归一，不是丢弃**（PO 确认 jinpenga，2026-08-07）：decode 收敛成 `RoleSystem`，原字符串不留；R 出口方向收到 `RoleSystem` 一律按 Responses 惯例发 `developer`。这条不对称成立是因为同协议路径根本不进 codec，没有任何链路会把 `developer` 原样转回去。两个方向在 sub2api 上都能对上（收敛 `apicompat/chatcompletions_responses_bridge.go:518`，展开 `apicompat/anthropic_to_responses.go:133`）。

`ToolChoice.Mode` 的 `required` 对应 Anthropic 的 `tool_choice.type=="any"`。本仓 5 份 Anthropic 样本**都不带 `tool_choice`**，这一格取自参考仓库而非实采。

`Temperature` 与 `Stop` 在 9 份样本里**一次都没出现过**（两个 harness 都不发），它们进模型的依据是参考仓库里的标准字段映射（`litellm/llms/*/chat/`、sub2api `apicompat/`），不是实采。`coverage` 表因此不列它们——那张表校的是「样本里的字段有没有被漏掉」，把没采到的字段塞进去只会让它恒红。

### 4.2 事件侧

三个协议的流统一归一到以下事件序列；非流式响应当作「完整事件序列一次性回放」，上下游代码不分流式两套。

```go
const (
    EvMessageStart  // {ID, Model}
    EvTextDelta     // {Text}
    EvThinkingDelta // {Text, Channel}  Channel: ""(正文) / summary / signature
    EvToolCallStart // {Index, ToolID, ToolName, ArgsIsJSON}
    EvToolArgsDelta // {Index, Text}    ——不叫 JSONFragment 了，见 §4.3
    EvToolCallEnd   // {Index}
    EvUsage         // {Usage}          累计快照，后来者非零字段覆盖先前值
    EvDone          // {StopReason}     stop / tool_calls / length / content_filter，未知一律 stop
    EvError         // {Status, Message}
)
```

- `Index` 为序、`ToolID` 为稳定标识。并行调用下 CC 的参数分片按 index **交错**到达，必须按 `Index` 缓存再按序输出。Anthropic 用 content block index，Responses 用 output_index，语义对齐。
- `EvUsage` 一条流里可出现多次：Anthropic 在 `message_start` 给 `input_tokens`、在 `message_delta` 给 `output_tokens`。语义是累计快照，消费方**不做加法**。
- `Usage` 与 `Tap.Summary` 一致，**保留各协议原始语义不归一**（Anthropic 的 `input_tokens` 不含缓存命中，OpenAI 的 `prompt_tokens` 含）。
- `ThinkingChannel` 要显式判别式而非塞 `Extras`：Responses 同时有 `response.reasoning_text.delta` 与 `response.reasoning_summary_text.delta` 两条流，语义不同（推理正文 vs 面向展示的摘要），codec 必须分支。藏进 map 等于每个 codec 各写一次魔法键查找。

事实来源：Anthropic 侧取自 `testdata/golden/raw/anthropic-*` 五份真实上游 SSE 转录；CC 侧取自 M0 语料 `testdata/golden/cc-stream-*`；**Responses 侧没有真实上游转录**（入站采集走的是 stub，stub 是道具不是样本），事件名以 `sub2api backend/internal/pkg/apicompat/` 为准，M2 拿到真实上游流后须复核。

### 4.3 样本逼出来的修正（v0.2 草案的四处证伪）

| 草案原文 | 被谁证伪 | 改成 |
|---|---|---|
| `System string` | 5 份 Anthropic 样本的 `system` 都是**数组**，块上带 `cache_control` 断点 | `System []Block`。字符串拼接会抹平断点位置，而断点位置正是要被测的东西——脱敏口径专门保住了它 |
| `Messages []Message // role: user/assistant/tool` | `in-anthropic-tool-turn2`：序列是 `user → system → assistant → user`，一条 `role=system` 的消息在 messages **中段**（Anthropic mid-conversation-system beta），content 是纯字符串 | `Role` 增 `RoleSystem`；`Message.Content` 统一块序列，纯字符串退化为单个 text 块 |
| `Tools []Tool // name/description/parameters(JSON schema)` | 两处：`in-responses-tool-turn2` 的 `exec` 是 **custom 工具**，没有 schema，只有一份 lark 文法的 `format`；Claude Code 声明的 `advisor_20260301` 自带 `type` 与 `model`，是上游服务端工具 | `Tool` 加 `Kind`（function/custom/server）与 `Extras` |
| `EvToolArgsDelta // {Index, JSONFragment}` | 同上——Codex code-mode 的 `exec` 入参是 **JavaScript 源码**，分片拼起来也不是 JSON | 字段改 `Text`；是不是 JSON 由 `EvToolCallStart.ArgsIsJSON` 说了算。编码到 CC 的后果见 §5 坑清单 |

### 4.4 显式丢弃清单

以下三项**能装下但选择不留**，代价已知（PO 确认 jinpenga，2026-08-07）：

| 丢什么 | 为什么 | 代价 |
|---|---|---|
| 正文块边界（`content_block_start/stop`） | canonical 只留拼接后的增量流。要保边界就得在事件流里加一对纯结构事件，而三协议里只有 Anthropic 用得上 | 回编码到 Anthropic 时多块合成单块。对客户端渲染等价 |
| `additional_tools` 的容器位置 | Responses 把工具声明包在一个 `role=developer` 的 input 项里；decode 时提升到 `Request.Tools` | 「它原本是第几条 input 项」丢失。回编 Responses 按首项重建即可；转 CC/Anthropic 时本就没有对应容器 |
| `developer` 角色原字符串 | 归一为 `RoleSystem`，理由见 §4.1 | 无——没有链路需要把它原样转回去 |

不在此列、但**跨协议必然作废**的是 `signature` 与 `reasoning.encrypted_content`：它们在 canonical 层有地方放（`Block.Extras`），只是转到别的协议时无处安放。见 §5 坑清单。

## 5. 转换器（codec）接口

> **v0.25 接口定稿、骨架已落（#10 M2-1）。** 代码在 `internal/protocol/codec.go`，三个协议骨架在各自子包，实现见 #11 / #12。

```go
type Codec interface {
    DecodeRequest(body []byte, stream bool) (*Request, error)   // 入口请求 → canonical，必须是全函数
    EncodeRequest(req *Request, stream bool) ([]byte, error)    // canonical → 出口请求
    DecodeStream(r io.Reader) (<-chan Event, error)             // 上游 SSE → 事件流，实现负责关 channel
    EncodeStream(w io.Writer, events <-chan Event) error        // 事件流 → 下行 SSE（含分帧与 flush）
    EncodeFullBody(events []Event) ([]byte, error)              // 非流式响应聚合
    EncodeError(w http.ResponseWriter, status int, msg string)  // 协议原生错误格式
}
```

- 骨架统一返回 `protocol.ErrNotImplemented` 而**不 panic**：转换闸门一放开这些方法就会被真实请求打到，panic 带走整个进程，而一个能被 relay 转成 5xx 的错误只坏这一条请求。骨架期的正确行为是「明确地不支持」，不是「崩给你看」。`EncodeError` 例外——它直接委托 M0 就已落地的 `Protocol.WriteError`，错误格式不是转换逻辑。
- `EncodeError` 收 `http.ResponseWriter` 而非 `io.Writer`（草案原文如此）：它要设 Content-Type 与状态码，且这条路径只在**首字节写出之前**走得通。流一旦开头，错误就只能以 `EvError` 的形态走在流里，那是 `EncodeStream` 的活。msg 由调用方保证已脱敏——上游 key 与 base_url 严禁出现在错误回显里。
- 「协议 → Codec」的表在 `internal/protocol/codecs`，与 `internal/protocol/taps` 同构同理由：`protocol` 不能反向导入自己的三个子包。转换路径要**两个** Codec（入口协议解出 canonical、渠道协议编回去）；两者相等时不该走这条路——同协议透传不做 decode→encode 转码。

- 每个协议一个包实现 `Codec`；「A→B 转换」= CodecA 解码 + CodecB 编码，**不存在两两互转的转换器**。实证依据：网桥式（逐对状态机）并非不可行——sub2api `apicompat/` 在三协议六方向上做成了生产级；但其 CC→A 流式路径是 `CC→Responses + R→Anthropic` 链式二次转换，恰说明无统一中枢时方向组合退化为拼凑链。枢纽式对新增协议保持 O(n) 扩展，本设计取枢纽。
- `EncodeStream` 内部管理：SSE 分帧、index 追踪（OpenAI 工具调用按 index 分片需按出现顺序重建）、`[DONE]` 终止符、Anthropic 的 `message_start/stop` 包裹。

### 转换坑清单（codec 实现时的验收关注点）

| 坑 | 说明 |
|---|---|
| tool call 增量重组 | OpenAI 按 index 分发参数分片；Anthropic `input_json_delta`；并行调用下 index 交错出现，必须按 Index 缓存再按序输出 |
| `metadata.user_id` | 上游以此判定「是否官方 Claude Code 请求」，中间层重序列化丢弃会被归入第三方 app。策略：**不可转但须保留**——A 入口的请求体 metadata 原样随请求携带；P0 透传天然不受影响（sub2api 实证坑） |
| 严格中转的请求校验 | 第三方 OpenAI 兼容上游会拒绝：消息 content 为数组（须拼纯文本）、`tool_choice` 引用未声明的 tool、有 tool_choice 无 tools——编码侧做规整，别指望上游宽容 |
| stop_reason 合法性 | Anthropic 非流式响应 stop_reason 不允许 null/空串，映射表必须给出合法默认值 |
| 工具入参不保证是 JSON | Codex CLI 0.144 code-mode 只声明一个 `custom` 工具 `exec`，入参是 **JavaScript 源码**（`in-responses-tool-turn2` 实测），`ToolCall.ArgsIsJSON` 为 false。编码到 CC 时 `function.arguments` 按契约必须是 JSON 字符串，encode 侧只能自行合成包装对象——**合成规则须与解包侧对称**，否则工具结果对不回去 |
| 并行只在 code-mode 内部 | 同一实测：Codex 的并行工具调用发生在那段 JS 的 `Promise.all` 里，线上永远只有一个 `custom_tool_call`，`parallel_tool_calls` 恒 false。别拿 Codex 样本去验证「多路 tool_call 交错重组」——那条路径要用 CC 语料（`testdata/golden/cc-stream-parallel-tools`）验 |
| 厂商私有推理字段 | DeepSeek 系 `reasoning_content` 等非标字段不建模，走 `Request.Extras` 透传 |
| Responses reasoning 的 `encrypted_content`（M0 实测） | Codex CLI 的 `/v1/responses` 请求会在 `input` 里回带上一轮的 reasoning item，其 `encrypted_content` 是**上游侧不透明密文**，只有原上游解得开。P0 透传无影响；**P1 一旦跨协议转换就必然作废**——转成 CC/Anthropic 时它无处安放，转回来也已换了上游。落到口径上：这就是「thinking 跨协议丢弃」的具体形态之一，转换路径不得伪造或复用该字段，只能丢，且丢了会让 Codex 失去上一轮的推理上下文（表现为质量下降而非报错）。M2 做 R→CC / R→A 时须有专门用例钉住「带 `encrypted_content` 的 input 不使转换报错」 |
| Responses 无状态化（P1-①，R 入口转换即需） | `previous_response_id` / store 语义需自行承接；参考 `sub2api backend/internal/pkg/apicompat/responses_namespace.go` |
| Anthropic 必填 max_tokens | OpenAI 可缺省；转 Anthropic 出口时必须填默认（配置项 `default_max_tokens`） |
| 角色交替约束 | Anthropic 要求 user/assistant 交替；OpenAI 允许多条连续同角色；转 Anthropic 前需合并相邻同角色消息 |
| assistant 空 content | 纯 tool_calls 的 assistant 消息 content 可能为 null，转 Anthropic 时空块要剔除 |
| tool_result id 对齐 | OpenAI `tool_call_id` ↔ Anthropic `tool_use_id`，互转时 id 原样携带 |
| streaming usage | OpenAI 需 `stream_options.include_usage` 才在流末尾给 usage；向 OpenAI 出口发流式请求时**强制注入该参数**，否则日志拿不到 token 数 |
| stop reason 映射 | `end_turn`↔`stop`、`tool_use`↔`tool_calls`/`function_call`、`max_tokens`↔`length` 查表，未知值统一 `stop` |

## 6. 主链路时序

```
client (harness)
  │ POST /v1/messages | /v1/chat/completions | /v1/responses
  ▼
auth 中间件：key hash 校验 → 取出 allowed_models
  ▼
入口协议识别（路径匹配）
  ▼
router：接入点（对外模型名）→ 命中候选（渠道纳管模型；M0~M2 单候选直连，M4 起加权随机；过滤 key 的 allowed_models）
  ▼
协议分流（seam，P0 定型）：
  渠道协议 == 入口协议 ──► 原始字节透传，Tap 旁路提取 usage（P0）
  渠道协议 != 入口协议 ──► codec 转换路径（P1；P0 期配置校验保证不命中，见 §7）
  ▼
upstream 驱动候选间故障转移（C4 已决语义；A-14 D3：不探测、不记忆、不摘除。**实现在 M4**；M0~M2 单候选单 key 退化：失败不切换，直接按入口协议原生格式回错）：
  候选集 = 该接入点 weight>0 的候选
  loop：对未试过的候选重新归一化权重，加权随机抽一个
      渠道内按 key_mode 选启用 key（key 层内环，v0.11）：
          请求上游成功 ──► 透传 / 转换下行（写出首字节后不再切换）
          429/5xx/网络错误（未写首字节）──► 同候选同 key 退避重试（最内环，v0.19，实现在 M2）
          429/401/403（未写首字节，同候选重试耗尽后）──► 渠道内换未试过的启用 key 重试；401/403 同时摘除该 key（记原因，可恢复）
          5xx/网络错误/连接超时（同上，重试耗尽后）──► 不换 key，跳出内环
      渠道内 key 耗尽 或 5xx/网络错误/连接超时 ──► 剔除该候选，继续 loop
      其余 4xx ──► 不切换，也不重试，按入口协议原生错误格式直接返回
      候选耗尽 ──► 最后一次上游错误按入口协议原生格式返回
  同候选退避重试（v0.19，推翻 C4 的「无同候选重试」）：
      触发：429 / 5xx / 网络错误，且未向 client 写出首字节；401/403 与其余 4xx 不重试（确定性失效，重试必然同样失败）
      退避：指数退避 + 抖动；上游给了 `Retry-After` 就以它为下界
            抖动取半区间 `[d/2, d)`，不是全区间 `[0, d)`——全区间会让实际退避短于 `Retry-After`，下界就白设了
            `Retry-After` 超过 `max_delay` 时**不重试**，把那份 429 原样交给 client：照等等于把客户端在网关扣一分钟
      次数：可配（`retry.max_retries`，见 §7），默认 2；退避 `base_delay: 500ms` / `max_delay: 10s`（v0.21 定稿）
      超时不重试：刚超时的对端原地重试大概率再超时，只会把客户端的等待翻倍（与候选间转移的触发条件不同，那里超时要切候选）
      客户端中途取消：不再打上游；退避途中那份响应的 body 已被读空丢弃，不能当结果交出去
      依据：C4 原本把重试留给 harness，M0 验收实测（#6）证伪一半——Codex 0.144.1 对 5xx 会退避重试，
            对 429 一次即弃（带不带 `Retry-After`、把 request_max_retries/stream_max_retries 调到 4 都只打一次）。
            单候选单上游被限流时无人自愈，故网关必须自己补这一环。
      429 仍原样透传（§10）：重试耗尽后回给 client 的仍是上游那份字节，网关不改写不吞
  ▼
logging：无论成败异步落 call_logs
```

**关键约束：failover 边界 = 向 client 写出第一个字节之前。** 一旦开始下行写流，格式承诺已生效，再失败只能：终止连接或注入出口协议的错误事件，并记日志；**不得切换渠道重发**（new-api 同原则）。

### 6.1 透传实现细则（v0.10 定，M0 落地）

**上游 URL 拼接**：`channels.base_url` 存「协议子路径之前」的前缀，网关按渠道协议追加固定后缀（`/v1/messages`、`/v1/messages/count_tokens`、`/v1/chat/completions`、`/v1/responses`），尾部斜杠归一化。代价是百炼这类自带路径前缀的兼容端点须填 `https://dashscope.aliyuncs.com/compatible-mode`，而非官方文档里带 `/v1` 的那串；换来的是不按厂商特判拼 URL。new-api 走 base_url 存根域名 + 各家 adaptor 特判，该复杂度不取。**建渠道的示例 SQL 必须写明这条**，否则填错是必踩的坑。

**客户端查询串整串照抄**（v0.24 定，#20，PO 裁定 jinpenga）：入站 URL 上的 query 原样接在拼好的上游 URL 后面，不过滤、不重排、不解码再编码。原实现只拼固定后缀，查询串被静默丢弃——实测 Claude Code 发的是 `POST /v1/messages?beta=true`，上游收到的是另一个请求，而丢没丢不看日志根本发现不了。不做白名单是因为这里没有可枚举的对象：各家 harness 的私有参数不可穷举，而查询参数不像请求头那样天然带客户端指纹（那条是请求头白名单的立论，不能照搬）。若日后发现某个参数确实泄露信息，再按「哪个参数、泄露什么」逐个拦。拼接顺序只能是 `base + 固定后缀 + "?" + query`——§7 的启动校验已拦掉带查询串的 `base_url`，所以不会拼出两个 `?`；query 为空时不产生裸 `?`。

**模型名翻译走字节级 splice，不是整体重编码**（v0.11 PO 裁定）：接入点对外模型名 → 纳管模型名的翻译（口径层 §2.3）必须发生，否则接入点在 M0 退化成没有翻译能力的空壳——对外叫 `qwen-fast`、上游叫 `qwen3-max-2025-09-23` 的接入点会带着对外名打到百炼被拒。做法是用 JSON 词法定位**顶层** `model` 值的字节区间，只替换那一段，其余字节一个不碰；嵌套对象里的同名键不受影响，顶层键重复时改最后一个（与 `encoding/json` 的 last-wins 一致）。这不违反「不做 decode→encode 转码」——整体重编码会打乱键序、改写数字字面量、丢掉未建模的厂商字段，splice 都不会。对外名与纳管名相同时原样返回，连 splice 都不做。因此透传路径的保真口径精确表述为：**除顶层 `model` 值外逐字节相等**。

**请求头（网关 → 上游）重建而非复制**，默认丢弃客户端全部请求头，白名单构造：

- `Content-Type` 取自客户端；`Accept` 取自客户端，未给且流式时补 `text/event-stream`。
- 凭证注入按渠道协议：`anthropic` → `x-api-key: <凭证>`；`openai_cc` / `openai_responses` → `Authorization: Bearer <凭证>`。
- Anthropic 渠道额外：`anthropic-version` 取自客户端、未给时默认 `2023-06-01`；`anthropic-beta` 客户端给了就原样转发（Claude Code 靠它开 1M 上下文、computer use 等能力，丢了会静默退化）。
- 一律不转发：hop-by-hop 头（`Connection`/`Keep-Alive`/`TE`/`Trailer`/`Transfer-Encoding`/`Upgrade`/`Proxy-*`）、`Host`、`Content-Length`（Go 按 body 重设）、`Cookie`，以及**客户端自带的 `Authorization` / `x-api-key`——M1 起那里放的是网关 key，绝不能漏到上游**。
- `Accept-Encoding` 不转发客户端值，流式请求显式设 `identity`（避免上游压缩引入分块缓冲、拖长首字延迟）；不注入 `X-Forwarded-*`（个人自用零收益且泄露内网信息）。

> **白名单实测复核（M0 验收，2026-08-06）**：用一次性反代录下 Codex CLI 实际发出的全部请求头，逐条对照上面的白名单。结论是**白名单不放宽**，依据两条：① Codex 的私有头（`X-Codex-*` 一族、session/turn 标识等）全部被丢弃，整轮工具调用照样跑通——上游不需要它们；② 其中 `X-Codex-Turn-Metadata` 携带 `installation_id`，属于客户端安装标识，转发出去等于把本机指纹泄露给上游，个人自用场景零收益。若日后某个 harness 因缺头而降级，按「哪个头、丢了坏什么」逐个加白，不做整类放行。

> **反例：认客户端指纹的上游（v0.23 实测，M2-1 采集，2026-08-07）**。上面那条结论有个前提——上游不关心你是谁。实测遇到不成立的一类：**某些中转站的 Anthropic 端点限死「只服务 Claude Code 客户端」，靠 `user-agent` 与 `x-app` 两个头一起判定**，白名单转发不带这两个，于是每个请求都回 503（同样的请求绕过网关直连则 200）。
>
> **白名单仍不放宽。** 把客户端指纹加白等于为迎合一家中转站的判定方式，把「不泄露本机指纹」这条口径整体撤掉，代价与收益不对等。绕法是配置层的：给 Anthropic 配一条不设这种闸的上游（示例见 `scripts/seed-example.sql`）。这也是那份示例把 Anthropic 拆成独立渠道、而 CC 与 Responses 共用中转站的原因。
>
> 需要区分的是 `cmd/goldenrec`：它**转发时照抄入站头、落盘时才走白名单**（`8de1dab`）。看着像双标，其实是两件事——采集要的是「让 harness 与真上游把整轮跑通」，防指纹外泄的对象是 git 仓库而不是上游。网关不同，它的对象就是上游，所以按白名单构造。

**响应头（上游 → 客户端）**：除 `Content-Length` 外原样回传（流式下无意义，非流式由 Go 按实际写入量重设），状态码原样。上游 `x-request-id` / `request-id` 既回传客户端也记日志——个人自用场景下能拿它去找上游对账，比藏起来有用。

**流式转发按字节块复制，永不按帧切分**：循环 `Read`（32KB 量级）→ `Write` → `Flush` 直到 EOF。不用 `bufio.Scanner` 按行读再重组——会引入换行/空行的重写风险，且 Scanner 的 token 上限会变成透传路径的截断上限。SSE 帧解析**只发生在 Tap 内**，Tap 从 `io.TeeReader` 拿同一份字节自组帧、自管缓冲上限（MB 级，并行工具调用的 JSON 参数单帧可以很大），**超限即放弃解析并降级**：Tap 的上限只影响日志字段完整性，绝不截断转发字节。（new-api 按行 Scanner 读、靠把 token 上限调到 64MB 躲大参数帧截断，本设计不取该路径。）

> **「放弃解析」的粒度＝丢那一帧**（v0.13 提出，v0.19 定稿，jinpenga 裁定）：本节原文与 Issue #5 只说「超限即放弃解析并降级」，没说放弃的是**那一帧**还是**整条流**。取前者——丢掉超限帧后在下一个帧边界重新对齐、继续解析。理由是 Anthropic 的 `output_tokens` 与 stop_reason 都在流最末的 `message_delta` 里，按后者读，一个畸形大帧会把整次调用的 usage 全带走。两种读法下 `Degraded` 都会置位，日志的可信度标记不受影响。
>
> 超限判定与分块无关：整帧连同结尾空行落在同一个 TCP 块里到达时同样按帧长判超限，否则「这帧算不算超限」会取决于上游恰好怎么切块。
>
> Tap 的两条硬约束落在实现上是：`Write` 恒定返回 `(len(p), nil)`（返回错误会被 `io.TeeReader` 变成读错误、直接打断转发），提取逻辑外包一层 `recover`（panic 只降级日志字段）。

**超时分层，不设 `http.Client.Timeout`**——它覆盖整个 body 读取周期，长流必被拦腰掐断。改设 `DialContext` 的 `net.Dialer.Timeout`（TCP 拨号，10s 量级）、`TLSHandshakeTimeout`、`ResponseHeaderTimeout`（等上游首个响应头，120s 量级）、`IdleConnTimeout`。**拨号这一层不能漏（v0.20 补）**：零值 `http.Transport` 用的是无超时的 `net.Dialer`，而后面几个超时都在 TCP 连上之后才起算——渠道地址被黑洞（丢包不回 RST）时，请求会一直挂到操作系统放弃（75s 量级）而不是及时回 502。向客户端写出前用 `http.NewResponseController(w).SetWriteDeadline` 每次推进（30s 量级），防慢客户端把 handler 永久挂住。客户端断连靠把 `c.Request.Context()` 传给上游 request 自动传播取消。

## 7. 配置与数据模型

### 启动配置（config.yaml，最小）

```yaml
listen: "127.0.0.1:8317"          # 公网暴露时改 0.0.0.0 并配合 Caddy/限流
db_path: "./gateway.db"
admin_password: "change-me"        # 仅首启初始化管理员；改密后此项失效
default_max_tokens: 8192
log_bodies: false                  # 排障开关；默认不记请求体
rate_limit_qps: 10                 # 全局令牌桶（v0.15）；超限 429 + Retry-After
rate_limit_burst: 20
retry:                             # 同候选退避重试（v0.19 口径，v0.21 定稿）
  max_retries: 2                   # **重试**次数，不含首次尝试
  base_delay: 500ms
  max_delay: 10s
```

> **`retry` 块缺席 = 用默认（重试 2 次），显式写 `max_retries: 0` = 关闭**。两者在 YAML 里都解出 0，靠「先填默认值再 Unmarshal 覆盖」区分：加载后不许再给 `max_retries` 补零值，否则「写了 0」被悄悄改回 2，重试就关不掉了。两个退避间隔反过来必须兜底——只写 `max_retries` 时不补就退了个寂寞。

业务配置（渠道/接入点/key）全部落 DB，由管理端维护（M3）；管理端就绪前（M0~M2）用 SQL 手工维护（口径层 C2 收敛，v0.8）。

> **配置校验规则（临时闸，随转换批次逐步放开）**：候选渠道协议与入口协议不同、且对应转换路径尚未实现时报错。这不是 v1 边界——全互转属 v1 承诺（口径层 C1 已收敛）。
>
> **校验时机拆两处**（v0.10）：接入点本身不绑定协议，入口协议要到请求时（由路径）才知道，因此「协议必须一致」无法在启动时判。
> - **启动加载时 + 管理端保存时**：每个未停用接入点有且仅有一个 weight>0 的候选；每个未停用渠道有且仅有一份未停用凭证；每个候选引用的纳管模型确实属于存在的渠道；**未停用接入点的 weight>0 候选必须真的可达——其渠道、纳管模型、凭证均未停用**（v0.15，判定条件逐条对齐 `Resolve` 的 JOIN）；**未停用渠道的 `base_url` 必须是带 host 的绝对 http/https 地址，且不带查询串与 fragment**（v0.20——schema 只要求非 NULL，而配置是手写 SQL 灌进来的，空串 / 漏 scheme 的裸域名 / `ftp://` 都存得进去，过得了校验却每次请求才在 `http.Client.Do` 里失败回 502；查询串与 fragment 更隐蔽，`buildURL` 是字符串拼接，`https://h/p?x=1` 接上 `/v1/messages` 后 Go 解出来是 `path=/p`、`query=x=1/v1/messages`——协议子路径被整个吞进查询串，请求永远打到 `/p`，启动、日志、响应三处都看不出异常）。违规即拒绝启动，报错须点名违规记录的 id/name。

> **这条错误信息不回显 `base_url` 本身**：`cmd/gateway` 会把 `Validate` 的错误直接落 stderr，而 `base_url` 可以带 userinfo（`https://user:pw@host`），回显等于把上游密码打进日志。按 CLAUDE.md「错误回显严禁泄露上游 key 与 base_url」，只报「哪里不对」加渠道 name/id，让运维自己查 `channels` 表——可诊断性不靠回显原值。
> - **请求时**：入口协议 ≠ 命中候选所在渠道协议 → 按入口协议原生格式回错，文案明确为「该转换路径尚未实现」。

### SQLite 表

```sql
CREATE TABLE channels (            -- 渠道只管连通性，不承担路由职责
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  protocol TEXT NOT NULL,          -- anthropic | openai_cc | openai_responses
  base_url TEXT NOT NULL,
  credential_type TEXT NOT NULL DEFAULT 'api_key',  -- api_key | service_account（Vertex：SA JSON→token 刷新，v0.17）
  key_mode TEXT NOT NULL DEFAULT 'polling',  -- polling | random：凭证池选取模式
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE channel_keys (        -- 渠道凭证池（new-api 密钥聚合的建表版，不用 blob+JSON 状态 map）
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  credential TEXT NOT NULL,        -- 静态 key 或 SA JSON（按渠道 credential_type）；仅存服务端，错误回显严禁泄露
  disabled INTEGER NOT NULL DEFAULT 0,
  disabled_reason TEXT,            -- 仅 401/403 确定性失效自动摘除；429/5xx 不摘；管理端可恢复
  disabled_at DATETIME,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE access_points (       -- 接入点：对外模型名（客户端 model 字段）
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  model TEXT NOT NULL UNIQUE,
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE channel_models (      -- 渠道纳管的可用模型（上游模型名）；候选只能引用纳管条目
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  upstream_model TEXT NOT NULL,
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(channel_id, upstream_model)
);

CREATE TABLE candidates (          -- 候选 =（渠道纳管模型，权重）；weight=0 临时摘除
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  access_point_id INTEGER NOT NULL REFERENCES access_points(id) ON DELETE CASCADE,
  channel_model_id INTEGER NOT NULL REFERENCES channel_models(id),
  weight INTEGER NOT NULL DEFAULT 100,
  UNIQUE(access_point_id, channel_model_id)
);
-- M0~M2 临时闸：配置校验强制每接入点单候选、每渠道单 key；多候选/多 key 的实现在 M4

CREATE TABLE api_keys (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  key_hash TEXT NOT NULL UNIQUE,
  allowed_models TEXT NOT NULL DEFAULT '*',  -- JSON 数组或 *
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE call_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  api_key_name TEXT NOT NULL,
  client_protocol TEXT NOT NULL,       -- anthropic | openai_cc | openai_responses
  upstream_protocol TEXT NOT NULL,
  model_requested TEXT NOT NULL,
  model_upstream TEXT NOT NULL,
  channel_name TEXT NOT NULL,
  status INTEGER NOT NULL,             -- 最终对 client 的状态
  retry_count INTEGER NOT NULL DEFAULT 0,  -- 同候选重试次数；结构化日志里已有对应的 retries 字段（v0.21），落库时接过来
  ttft_ms INTEGER,                     -- 首字节耗时（流式）
  total_ms INTEGER NOT NULL,
  input_tokens INTEGER, output_tokens INTEGER,
  cache_read_tokens INTEGER, cache_write_tokens INTEGER,
  error TEXT                           -- 截断后的错误摘要
);
CREATE INDEX idx_call_logs_created_at ON call_logs(created_at);
```

注：若未来改 MySQL，表须显式 `CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`（与团队 DDL 规范一致）。

## 8. 最小管理接口

- `GET /healthz`
- `GET /v1/models`：返回配置中声明的对外模型（harness 启动时会拉），格式为 OpenAI 公开的 `{"object":"list","data":[{"id":…}]}`

> **不迎合 harness 的私有目录格式（M0 验收实测，2026-08-06）**：Codex CLI 拉的其实是 OpenAI 的**私有**模型目录——`{fetched_at, etag, client_version, models:[{slug, supported_reasoning_levels, apply_patch_tool_type, …}]}`，与公开的 `/v1/models` 不是一个东西。拿不到时 Codex 打两条 warning（`Model metadata for X not found. Defaulting to fallback metadata`、`service tier priority is not advertised…`）后**照常工作**，整轮工具调用不受影响。故本项目**不实现该私有格式**：它无公开契约、字段随 Codex 版本漂移，为它建一张模型能力表要长期跟着上游跑，而收益只是消掉两条 warning。降级路径已实测可用，就停在降级上。
- `GET /admin/logs?limit=50&model=...`：近期调用日志（管理员 session 鉴权，细则见口径层 §2.7）
- 管理端 CRUD API（渠道/接入点/key）随 M3 扩全，届时另列；上表为 M0~M2 最小集
- Anthropic 出口/入口的 `count_tokens`：P0 仅在上游为 Anthropic 时透传，否则 501

## 9. Golden 测试方案

**样本采集（M0 抓子集、M1 补全，别等写完代码）**：用真实渠道抓下列 SSE 转录（raw 字节存档）。样本 1~6 刻意选「同语义、双协议」场景，天然构成 P1 转换测试的黄金输入对。场景清单可对照 sub2api `apicompat/` 的测试文件命名（`tool_pairing`、`parallel_tool`、`stream_lifecycle`、`codex_events` 等）补漏：

| # | 样本 | 场景 |
|---|------|------|
| 1 | Anthropic streaming | 纯文本长回复 |
| 2 | Anthropic streaming | 单次 tool_use |
| 3 | Anthropic streaming | 并行多 tool_use 交错增量 |
| 4 | OpenAI CC streaming | 纯文本 |
| 5 | OpenAI CC streaming | 单次 tool_calls（参数跨 chunk） |
| 6 | OpenAI CC streaming | 并行 tool_calls，index 交错 |
| 7 | 以上 1~6 的非流式版本 | |
| 8 | OpenAI Responses streaming | function_call 事件序列（P1 备料） |
| 9 | 上游 429 / 500 / 流中途断连 | failover 与流内错误注入 |

**M0 必抓子集（v0.10）**：样本 1~3（Anthropic 流式：文本 / 单 tool_use / 并行 tool_use）、4~6（CC 流式：文本 / 单 tool_calls 参数跨 chunk / 并行 index 交错）及其非流式版本（样本 7 的对应部分）。理由是 Tap 的测试要真实转录作输入，M0 就得有，等不到 M1。样本 8（Responses）与 9（上游异常）仍留 M1。**raw 字节存档前须人工过一遍，去掉真实凭证与个人对话内容。**

**采集与存放（v0.13 落地）**：录制反代 `cmd/goldenrec`（刻意在 `internal/` 之外——它只为喂测试库存在）转发到真实上游并把每次调用的原始字节落盘。样本库在仓库根 `testdata/golden/<样本名>/`，含 `meta.json`（protocol / stream / endpoint / status / expect / verified）、`request.json`、`response.raw`；不放在某个包的 `testdata/` 下，是因为同一份样本到 P1 还要喂给 codec 的跨协议用例。

`meta.json` 的 `expect` 由 goldenrec 用 Tap 自己预填，**只是草稿**：出自被测代码的期望值等于让实现给自己判卷，因此 `golden_test.go` 拒绝一切 `verified: false` 的样本。把 `verified` 置 true 是人工关卡，与「脱敏时人工过一遍」是同一道工序——核对脱敏、核对 expect 与原始字节相符，一起做。未采集的样本按名字逐个 skip，目录空着不会一路绿灯。

**入站样本（v0.22，M2 起）**：上面说的是**上游响应**转录，驱动 Tap 与 codec 的编码侧；codec 的**解码**侧要的是另一端——harness 发出来的入站请求字节。两类样本同库不同 `direction`（`upstream` / `inbound`），后者无 `response.raw` 与 `expect`，另有 `headers`（白名单）与 `stub`。

采集不能靠 gateway 的 `log_bodies`：那条路是排障日志，单侧 body 有 64 KiB 上限（`internal/server/calllog.go` 的 `bodyCaptureLimit`），而 Claude Code 带全套 tool 定义与长上下文时轻易越过它，半截样本看着还像回事、喂给 codec 才发现是坑。改由 `cmd/goldenrec` 的 **inbound 模式**全量落盘（超限报错，绝不截断）。

> **实测量级（v0.23，M2-1 采集，2026-08-07）**：Claude Code 2.x 无头单轮请求体 **185 KB**，其中 42 个 tool 定义占大头；Codex CLI 0.144.1 是 47~50 KB。也就是说 `log_bodies` 对 Claude Code 是**几乎必截断**，不是「长上下文时偶尔越过」。
>
> 这不改 `bodyCaptureLimit` —— 64 KiB 对排障日志是对的上限，一条长流的完整 body 进日志只会把日志冲垮。要改的是别处的预期：`log_bodies` 的截断标记（`truncated`）在真实 harness 下是常态而非异常，读日志时别把它当故障信号；要完整字节一律走 goldenrec。

> **stub 应答（v0.22 新决策，PO 裁定 jinpenga）**：inbound 模式不碰上游、不要凭证，按脚本回**手写的假响应**。
>
> 理由是采集目标里最有价值的那份是**第二轮**请求——带 `tool_result`（Anthropic）或 `function_call_output`（Responses）的那个包，`tool_use → tool_calls` 的映射全靠它钉住。而 harness 只有先收到过一个合法的 tool 调用响应才会去执行工具、才会发出第二轮；手上没有 Anthropic / OpenAI 官方 key（#7 仍挂着），纯录制回 501 的话对话在第一轮就断了。
>
> 边界要划清：**stub 是道具，不是样本**。它手写、不保真、不进 `testdata/golden/`——一旦混进转录库就是往事实里掺伪造。入库的只有 harness 发出来的 `request.json`，仍是 100% 真实字节。有真实上游时一律走 proxy 模式，那边顺带把出站样本也采了。
>
> 三条实现口径：脚本按文件名顺序一请求消耗一个，**发完报 503 不循环重放**（静默重放会让 harness 原地打转）；`count_tokens` 就地估算**不消耗脚本**（Claude Code 每轮都打它，吃掉一格会把后面全串位）；未预料的端点回 404 且不消耗脚本。脚本与调参见 `testdata/goldenstub/README.md`。

**测试方法**：样本 → DecodeStream → 内存事件序列 → （跨协议用例再过 EncodeStream+对方 DecodeStream）→ 语义比对（忽略空白与顺序无关差异，比对文本全文、工具调用 name/参数解析后相等、usage、stop reason）。字节级 diff 只用于透传回归。

## 10. harness 验收清单

必过档挡里程碑验收；顺带档不挡、坏了再修（#7 已决）。

| harness | 档位 | 协议 | 必过项 |
|---|---|---|---|
| Claude Code | 必过 | Anthropic | `/v1/messages` 流式工具调用整轮跑通；cache_control 透传（Anthropic 出口）；`count_tokens` 不阻塞启动 |
| Codex CLI | 必过 | Responses | Responses 模式整轮工具调用与透传（P0）。**CC 模式已不可用**——0.144.1 移除 `wire_api = "chat"`（openai/codex#7782） |
| pi | 必过 | CC | `/v1/chat/completions` 整轮工具调用、streaming usage。用 `PI_CODING_AGENT_DIR` 指向验收专用配置目录，避免动开发者全局 `~/.pi` |
| OpenCode | 顺带 | CC | `/v1/models` 列表 + 工具调用 |
| 全部 | — | 429 原样透传不被网关吞掉（M0 已验：状态码/头部/body 逐字节，见 #6）。**「harness 自身重试逻辑生效」这条已作废**——Codex 0.144.1 对 429 不重试，改由网关侧同候选退避重试兜底（v0.19，M2 验收） |

## 11. 里程碑

与口径层统一为 M0~M3（C2 收敛后统一编号）：

| 里程碑 | 内容 | 粗估 |
|---|---|---|
| M0 透传骨架 | 骨架 + 三协议原始字节透传 + SSE + Tap usage 提取（细则见 §6.1）；渠道/接入点 SQL 手工建；golden 样本必抓子集（§9）；对 Anthropic 官方跑通 Claude Code、对百炼/OpenAI 官方跑通 CC 透传。规格见 Issue [#1](https://github.com/SimonGino/ai-gateway/issues/1) | 1~2 个周末 |
| M1 Key + 日志 | key 鉴权中间件 + key CRUD（SQL 手工）+ call_logs 落库；上游错误按入口协议原生回错 + 错误注入打磨；harness 透传实机验收 | 1 个周末 |
| M2 协议转换（P1-①~④ 按序） | ① A→CC、R→CC（含 Responses 无状态化）→ ② R→A → ③ CC→A、CC→R → ④ A→R 与横切增强；每批 golden 全绿 + 真实 harness 验收。成本锚点：sub2api `apicompat/` 六方向全量 ≈ 7k 行实现 + 9k 行测试，测试为实现 1.3 倍。**另含同候选退避重试**（v0.19 从 M4 提前，见 §6；不依赖多候选，临时闸不放开） | ① ≥2~3 个周末（主工作量在 tool call 增量重组），后续批次随复盘排期 |
| M3 管理端 + 部署 | React 管理端：渠道（模型纳管、key 池）/ 接入点（候选+权重）/ key / 用量查询，embed 单二进制；公网部署（Caddy TLS + 全局限流） | 待估 |
| M4 分流与转移 | 多候选加权随机分流 + 候选间故障转移（C4）+ 渠道 key 池聚合与 key 层内环（v0.11）；语义均已决，纳管成熟后实现，管理端配权重实测验收。**同候选退避重试已于 v0.19 提前到 M2**，不在本里程碑 | 待估 |

## 12. 参考对照

仓库索引与各仓库定位、许可证注意事项见本仓库 `CLAUDE.md`「参考仓库」一节；下表是逐文件的路径对照。**本项目不参考公司 fork `maix_ops_go`**；下表 new-api 路径均为 `~/Code/GitHub/new-api`（上游）相对路径，已逐一核实存在。

| 参考仓库 / 位置 | 参考什么 |
|---|---|
| `new-api/relaykit/dto/`（claude.go、openai_request.go） | canonical 请求模型的现实形态（注意：上游在 `relaykit/dto/`，非 fork 的 `dto/`） |
| `new-api/relay/channel/claude/adaptor.go`、`relay/claude_handler.go` | Claude 编解码 |
| `new-api/relay/chat_completions_via_responses.go`、`relay/responses_handler.go` | CC ↔ Responses 互转 |
| `new-api/relay/helper/`、`relay/common/` | SSE 工具函数 |
| `new-api/relay/relay_adaptor.go`、`model/channel*.go` | failover / 渠道选择思路（不取其复杂度）；`model/channel.go` 多 key 聚合语义（我们改为建表实现） |
| `new-api/relay/channel/vertex/service_account.go` | Vertex service account → access token 刷新（credential_type=service_account 参考） |
| `new-api/model/log.go` | 日志落库 |
| `litellm/litellm/llms/*/chat/`（各 provider transformation） | P1 协议转换字段映射的交叉对照（thinking、tool calling、usage 语义） |
| `sub2api/backend/internal/pkg/apicompat/` | **P1 转换的 Go 实现首要参考**：自包含转换库（A↔CC、Responses↔CC/A bridge、Responses SSE 事件线格式，含 Codex 事件流测试）；注意 LGPL-3.0，参考思路可、整包复制需评估义务 |
| `sub2api/backend/` 其余 | Anthropic 协议侧处理与 Go 工程结构参考（订阅池/计费不抄） |

## 附录：开放问题记录

- ~~上游清单与各渠道 key 数~~（#1 已决 v0.17：Anthropic/OpenAI/Vertex/百炼四渠道 + 渠道多凭证聚合，见 §0/§7）
- ~~真实接入点与候选清单~~（#2 已决：运营数据不冻结，M0 验收集见 §0）
- ~~harness 清单增删~~（#7 已决：必过档 Claude Code、Codex CLI；顺带档 pi、OpenCode）
- ~~转换方向优先级~~（#9 已决：协议转换属 v1、透传先行、按口径层 §2.1 分批，设计态考虑落 §2/§4/§5/§6）
- ~~**转换方向集合**~~（已决：口径层 v0.3 裁定为三协议全互转，6 转换 + 3 透传，全集为准；分期之争已随 C1 收敛：属 v1 承诺、节奏透传先行）
- 公网暴露与否 → 决定 TLS / 限流 / listen 地址默认值
- ~~thinking 同协议透传是否进 P0~~（已随 P0 原始字节透传自动解决；跨协议丢弃仍是 P1 已决策略）
