# Portage

个人自用 AI 模型网关（旧名 `ai-gateway`，口径层 v0.42 改名）：三协议转发与互转、API Key 生命周期、接入点路由、用量记录。

## Language

### 路由与渠道

**接入点**：
别名层：对外暴露一个模型名（`model` 字段），绑定 1..N 个候选。不是唯一入口——纳管模型可用限定名直连（v0.32）。
_Avoid_: routes、模型映射表、endpoint

**转发端点**：
网关对外的四条转发 HTTP 路径（`/v1/messages`、`/v1/messages/count_tokens`、`/v1/chat/completions`、`/v1/responses`）。入站协议由路径定死，不嗅探请求体；路径比协议细一档——`/v1/messages` 与 `/v1/messages/count_tokens` 的入站协议同为 `anthropic`。v0.82（#17）起逐行落进 `call_logs.endpoint`，管理端可按它筛。
_Avoid_: 接入点（那是模型别名层，与 HTTP 路径无关）、把它简称「端点」以外的叫法

**限定名**：
纳管模型的对外寻址名 `渠道名/纳管模型名`。渠道名与渠道内模型名各自唯一，且**渠道名不含 `/`**（v0.34），故限定名天然唯一，无需重名规则。撞上同名接入点时接入点优先。
_Avoid_: 裸 modelId 寻址（v0.22 封的是那条路，不是限定名）

**候选**：
（渠道纳管的模型，权重）二元组，接入点分流的单元；weight=0 即临时摘除。

**渠道**：
上游连接 = base_url + **支持协议集** + 模型纳管 + 凭证池；只管连通性，不承担路由职责。一个上游账号只建一个渠道——CC 与 Responses 共用 base_url 前缀，不拆两个（v0.33）。
_Avoid_: 供应商（不是独立实体：一个渠道就对应一家上游的一次接入，图标分组由 Web 端启发式解决）、upstream（指渠道时）

**支持协议集**：
渠道声明它能说的上游协议，如 `{openai, openai_responses}` 或 `{anthropic}`。取代 v0.33 之前的单值 `protocol` 列。协议不出现在对外模型名里。

**纳管模型**：
渠道声明的可用上游模型条目；候选只能引用纳管条目，不自由填字符串。

**凭证池**：
渠道下 1..N 份上游凭证，每份带一个渠道内唯一的人写名字；类型 `api_key` / `service_account` 渠道级二选一。凭证值管理端可回读可复制（v0.47 推翻 v0.28 的「只写不回读」），但只由凭证池那一个接口发。渠道页上这一段叫「上游凭证」（不叫「API 密钥」，那会与导航上的 API Key 同形不同义）。
_Avoid_: key 池（v0.17 泛化后的旧称）、API 密钥（指这一段时）

**key 层内环**：
故障转移中「渠道内换凭证重试」的环节，套在候选间转移外环之内。只有 401 摘凭证（403 换而不摘，429 换而不冷却），摘除只人工恢复。

**全局 QPS 桶**：
入口速率闸，保护网关自身（防 key 泄露刷爆上游账单），不分 key/IP 维度（v0.15）。**桶数是 2 不是 1**（v0.81）：**生成面**——`/v1/messages`、`/v1/chat/completions`、`/v1/responses`——共用一只，`count_tokens` 独占一只，两只用同一份 `rate_limit_qps`/`rate_limit_burst`。拆的理由是 harness 开场的 count_tokens 风暴会打空共用桶、把真实请求 429 掉（#16）。
_Avoid_: 单个全局令牌桶（v0.81 之前的说法）、限流（笼统，与渠道并发上限混）

**渠道并发上限**：
渠道级 in-flight 请求数上限，空/0 = 不限（v0.49）；保护自部署上游不被排队堆死。限的是「同时在上游跑的存量」，与**全局 QPS 桶**（限入口速率、保护网关自身）两回事。闸满走网关侧**有界排队**（队列 ×1 / 超时 30s，全局默认），队满/超时回 429（v0.50）；拥塞期在此之外不加机制——无熔断、无探活恢复、重试不动（v0.51）；观测走流水一列两词——`queue_wait_ms` + error 词表加 `queue_full`/`queue_timeout`（v0.52）。
_Avoid_: 渠道限流（笼统，易与 RPM/配额混）、QPS（并发是存量不是速率）

**渠道 compaction 能力位**：
渠道上的布尔位（`supports_compaction`，v0.54），语义只有一句：**这个渠道的上游认不认 Codex 的 `compaction_trigger`**，默认否（PO 于 2026-08-13 裁定，存量渠道随迁移落否）。它保护的是**透传**那条路——Responses 形状的 wire 不等于支持压缩，不认 trigger 的上游会把它当无关字段忽略、照回 0 个 compaction item，而 Codex 收到 0 个即 Fatal。为否时压缩 turn 明确拒绝（400 + 流水词 `compaction_unsupported`）；转换路径不看这个位——它走**本地合成**，自己产得出那个 item。界面上叫「Codex 压缩（remote compaction）」。
_Avoid_: 压缩开关（听着像网关能压缩，网关侧那件事叫本地合成）、Responses 透传总开关（它只管压缩 turn，普通 turn 不受影响）

**API Key**：
网关下发给客户端的鉴权 key（前缀 `sk-ptg-`），与上游凭证严格两回事。界面上叫「API Key」（PO 于 2026-08-12 裁定改名，原称「网关 key」）；散文里指代上游那份时必须写全「上游凭证」，不能简称 api key。用量维度同理写全称——「按 API Key」与「按上游凭证」是两个维度，只写「按凭证」两边都像（v0.53 就是这么被看混的）。
_Avoid_: 网关 key（旧称）、api key（指上游凭证时）、按凭证（作为用量维度时）

**错误详情**：
流水里上游错误原文的那一列（`error_detail`，v0.53），截前 2KB、只在失败时存、只走管理端。与 `error` 列的**固定词表**分开：词表可枚举可聚合，原文是不可控自由文本。上游透传 4xx 时词表列空着而这一列有值，两者不同步出现。
_Avoid_: 错误摘要（那是 `error` 列的词表）

**outcome 词表**：
流水 `error` 列的那份固定词表，**10 个词**（v0.70 补记）：`upstream_error`、`stream_aborted`、`unauthorized`、`rejected`、`queue_full`、`queue_timeout`、`queue_abandoned`、`compaction_unsupported`、`model_not_allowed`、`rate_limited`。成功那一档是哨兵 `ok`，**不落库**——库里留 NULL，NULL 即「这行没有错误词」；接口层对外给空串（同 `upstream_request_id` 的成例，v0.67 ⑤）。一档一个词，不叠加：一次调用只落一个。
_Avoid_: 把 `ok` 当成第 11 个词（它是哨兵，不进库）、把 NULL 与空串当两态（写侧只产生 NULL，空串是接口层的形态）

### 协议与转换

**透传**：
同协议路径的原始字节转发，不做 decode→encode。
_Avoid_: 代理、转发（指透传时）

**Tap**：
透传旁路解析器，只读提取 usage / stop reason 供日志，不改流。

**Codec**：
单协议编解码器；A→B 转换 = CodecA 解码 + CodecB 编码，无两两互转器（枢纽式）。

**canonical 事件模型**：
三协议统一归一的内部事件序列；非流式 = 完整序列一次性回放。

**毛值 input**：
canonical `Usage.InputTokens` 的口径——**含**缓存读写，缓存两项是它的明细而非另外两笔（#72）。CC/Responses 的 input 本就是毛值；Anthropic 的是**净值**（与缓存互不相交），解码加回、出口减回。**流水 `input_tokens` 列同属这个口径**（v0.71）：落库前按 `upstream_protocol` 归一，Anthropic 行加回缓存两项，跨协议求和才是构造上可加的。
_Avoid_: 净值（那是 Anthropic 线上的形态）；Tap 的 `Summary` 不归一，保留上游原样——**不归一的是 `Summary` 这个中间物，不是流水那一列**，两者不是一回事

**思考 token**：
usage 里的 reasoning token（v0.66，#97）——是 **output token 的明细而非另一笔加数**（`total = input + output` 恒成立，思考不进这个加法），同 `毛值 input` 下缓存两项与 input 的关系。流水那一列**三档**：正数 = 思考了这么多，`0` = 上游报了、这次没思考，`NULL` = 上游根本不报（Anthropic 一路、非推理模型、v0.66 之前的老行）。
_Avoid_: 把它加进 total 或从 output 里减掉；把 NULL 抹成 0（那是在说「确凿零思考成本」）

**本地合成**：
转换路径（R→A / R→CC）上让 Codex 压缩可用的做法（v0.54，#74）：把压缩 turn 改写成一次纯总结请求打给上游，再把摘要装进自造信封当成**恰好一个** compaction item 发回去，下一轮 Codex 回带时再拆开还原成 user 消息。上游没有 compact 端点可转发，这是唯一路子（与 opencodex 同构）。信封是 `ptg1:` + base64(摘要)，**透明不加密**，且前缀是长期兼容约束。
_Avoid_: 远程压缩（那是 Codex 侧的叫法，指的是「让服务端压」这件事）、加密摘要（信封谁都解得开）

**临时闸**：
某项实现落地前的配置校验限制，非 v1 边界。两半各自独立放开：凭证那半已于 v0.38（M3）放开，单候选那半仍在（M4 放开）。

### 文档与流程

**口径层**：
需求口径文档（`docs/口径层设计.md`）；决策以版本记录落档，署 jinpenga。

**展开层**：
实现设计文档（`docs/MVP设计草案.md`）；实现细节只落这里。
_Avoid_: 实现层（指文档时）

**必过档 / 顺带档**：
harness 验收分级——必过档（Claude Code、Codex CLI）挡里程碑，顺带档（pi、OpenCode）不挡。
