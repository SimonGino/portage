# 参考仓库

本地 `~/Code/GitHub/` 下的以下几个仓库。涉及协议细节、字段语义、转换坑，先查这里再下结论，不凭记忆；**本机上其他 fork 一律不参考**。逐文件的路径对照表见 `docs/MVP设计草案.md` §12。

## `new-api`（QuantumNous/new-api）

Go 网关主参考。读 `relay/` 协议适配层、SSE 流式转发、`controller/` + `middleware/` 的 key 鉴权与渠道路由；运营功能（计费 / 多用户 / 渠道权重）不抄。

**上游 canonical DTO 在 `relaykit/dto/`；下游 fork 常把它改成另一套 `dto/`，勿混。**

## `sub2api`（Wei-Shaw/sub2api）

**协议转换的 Go 实现首要参考。** `backend/internal/pkg/apicompat/` 是自包含转换库（Responses↔ChatCompletions、Responses↔Anthropic、CC↔Anthropic bridge、Responses SSE 事件线格式，含 Codex 事件流测试）；`previous_response_id` 处理见 `internal/service/openai_previous_response_id.go` 与 `RemovePreviousResponseIDFromBody`。

**LGPL-3.0**：参考思路与字段语义可以，整包复制需评估义务（Go 静态链接下约等于整项目跟随）。订阅池、计费不在本项目范围。

## `litellm`（BerriAI/litellm）

Python 网关，字段映射最全。做 Anthropic↔OpenAI 转换时对照 `litellm/llms/*/chat/` 各 provider transformation 核对 thinking、tool calling、usage 语义。

## `opencodex`（lidge-jun/opencodex，MIT）

TS/Bun 本地代理，把 Codex 的 Responses API 转译到任意 provider。**Codex CLI（codex-rs）客户端行为兼容的首要参考**：自动压缩（`src/responses/compaction.ts`、`src/bridge.ts` 合成 compaction item）、reasoning 回放、Responses SSE 事件线。

PO 于 2026-08-13 裁定加入参考名单。

## `mimo2codex`（7as0nch/mimo2codex，MIT）

TS/Node 本地代理，把 Codex 的 Responses 翻译成上游的 Chat Completions（或原生 Responses）。转换层集中在 `src/translate/`：`reqToChat.ts`（入向）、`respToResponses.ts` + `streamToSse.ts`（回向）、`autoCompact.ts`（压缩）。

**`namespace` 工具的另一条路线**，这是它的首要参考价值——它与本项目的裁定**正面不同**：

- **不摊平**。`reqToChat.ts` 第 4 支把 `namespace` 壳递归展平，**子工具名保持裸名**，靠 `buildNamespaceMap`（`server.ts:783`）建一张「裸名 → 命名空间名」的表，在回向侧（`respToResponses.ts:145-158`、`streamToSse.ts:211/350`）给 `function_call` item 贴回 `namespace` 字段。本项目取摊平（`<ns>__<name>`，口径层 v1.14 ①），差别与代价见那一条。
- **撞名不 400，去重保留第一个**。`dedupeToolsByName`（`reqToChat.ts:585`）键取 `fn:<function.name>` / `builtin:<type>`，重复的丢掉并一次性 warn。它的注释是一份**一手证据**：Codex CLI / Desktop（尤其新版 / DeX）确实会同时发顶层 `function` 工具 `_fetch` 与 `namespace` 里同名的 `_fetch`，上游因此回 `400 Param Incorrect: tools contains duplicate names`（其 issue #20）。本项目撞名取 400（口径层 v1.14 ③）。
- **去重键 `builtin:<type>` 与本项目 `Tool.Label()` 的兜底同构**：无名的内建/服务端工具用它的 `type` 当身份，且 function 名与 builtin type 不共用命名空间、不互撞（口径层 v1.18）。

其余可参考的点：`web_search` / `web_search_preview` 不丢而是映射成上游原生 `web_search`（`reqToChat.ts:329-345`）——只有认这个能力的上游才有得映，本项目两个出口都没有，仍按 `SERVER_SIDE_TOOLS`（`reqToChat.ts:246`）那批丢并记 type；`sanitizeFunctionCallArguments`（`reqToChat.ts:628`）是 Codex 回带 `function_call.arguments` 的清洗坑。

MIT，义务同 CLIProxyAPI / opencodex：阅读借鉴零义务，复制代码需在分发物保留版权声明与许可全文。

PO 于 2026-09-03 裁定加入参考名单。

## `CLIProxyAPI`（router-for-me/CLIProxyAPI，MIT）

Go 本地代理。**「thinking/reasoning 跨协议保真与 signature 处置」这一主题的首要参考**（主题之外不参考）：出向合成见 `internal/translator/openai/claude/`，回带按 signature provenance 整块丢弃的决策表见 `internal/signature/provider_compatibility.go`，「思考多少 / 展示与否」正交两维见 `internal/thinking/`。

**架构不参考**（点对点 N×M、30 对逐一注册、无 canonical 事件模型），五套有状态 reasoning 回放账本知道即可、不抄。

MIT 标准条款：阅读借鉴零义务，复制代码只需在分发物保留版权声明与许可全文，**不因 Go 静态链接传染**（与 sub2api 的 LGPL-3.0 关键不同）。

PO 于 2026-08-13 裁定加入参考名单。
