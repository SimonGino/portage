# Research：参考实现对 Responses `namespace` 工具摊平 / 回程的做法对照（#84）

调研日期 2026-09-02。所有结论回溯到源码行；查不到的点明说查不到。本文只对照事实，不裁口径——口径由地图 #93 逐项交 PO 拍板。

## 事实源

本地参考仓库（名单与许可证义务见 `docs/agents/reference-repos.md`；行号按下列 commit）：

1. **sub2api**（Wei-Shaw/sub2api，LGPL-3.0）本地 `main` @ `5a61430`（克隆于 2026-07-30）。文件都在 `backend/internal/pkg/apicompat/`：`chatcompletions_responses_bridge.go`（下称 **bridge.go**）、`responses_namespace.go`、`responses_client_tools.go`、`responses_to_anthropic_request.go`、`responses_stream_event_wire.go`、`types.go`；服务层 `backend/internal/service/{gateway_forward_as_responses.go,openai_gateway_responses_chat_fallback.go,openai_responses_namespace.go}`。
2. **CLIProxyAPI**（router-for-me/CLIProxyAPI，MIT）本地 `main` @ `d757063`（克隆于 2026-08-13）。`internal/runtime/executor/xai_executor_request.go`（下称 **xai_req.go**）、`xai_executor_response.go`（**xai_resp.go**）、`xai_executor_execute.go`、`xai_executor_stream.go`、`xai_executor_test.go`。注意票里写的 `xai_executor.go` 只剩 109 行常量与入口，逻辑已拆到上述两个文件。
3. **opencodex**（lidge-jun/opencodex，MIT）本地 `main` @ `4e0ffb2`（克隆于 2026-08-13）。`src/types.ts`、`src/responses/parser.ts`、`src/server/responses/collaboration.ts`、`src/bridge.ts`、`src/adapters/{openai-chat,anthropic,openai-responses}.ts`，测试 `tests/openai-responses-passthrough.test.ts`、`tests/multi-agent-compat.test.ts`。
4. **new-api PR 5209**（QuantumNous/new-api#5209，open，head `Rosons/new-api@6642c9d`，2026-06-04；AGPL-3.0）：`service/openaicompat/responses_to_chat_request.go`（下称 **pr/r2c.go**）、`service/openaicompat/chat_to_responses_response.go`。经 `gh api repos/Rosons/new-api/contents/...?ref=6642c9d…` 抓取，本地 new-api 克隆（`main` @ `ccd535e`，2026-08-13）不含此 PR，且 `relay/`、`service/`、`dto/` 下 grep `"namespace"` 无命中。
5. **litellm**（BerriAI/litellm，MIT）：`litellm/` 下 grep `namespace` 仅 `litellm/responses/mcp/litellm_proxy_mcp_handler.py:785` 的 `namespaced_tool_name`（MCP 服务端命名，与 Responses `namespace` 工具无关）。确认无对应实现。
6. Portage 现状：`internal/protocol/openairesponses/decode.go:483-493`（`namespace` 落 `default` → `ToolServer`）、`internal/protocol/openaicc/encode.go:273-276` 与 `internal/protocol/anthropic/encode_request.go:426-429`（`ToolServer` 一律 drop）；golden `testdata/golden/responses-stream-parallel-turn1/request.json:5-12`（`additional_tools` 里 `namespace` 名为 `functions`，子项含 `custom` exec 与 `function` wait）。

---

## 一、总对照表

| 维度 | sub2api（R→CC 桥 + 原生 R→R/R→Anthropic 降级） | CLIProxyAPI（仅 xAI 路径，R→R） | opencodex（R→CC / R→Anthropic / R→Gemini 通用） | new-api PR 5209（R→CC） |
| --- | --- | --- | --- | --- |
| **摊平命名** | `ns + "__" + name`（bridge.go:747-748） | `ns + "__" + name`；ns 自带尾缀 `__` 不重复加；子名已带 `ns__` 前缀或以 `mcp__` 开头则**原样不加**（xai_req.go:1027-1041；测试 :3574-3592） | `ns__name`（`namespacedToolName`，src/types.ts:210-212），所有 chat 形态 adapter 共用（openai-chat.ts:650、anthropic.ts:717、google.ts:236） | 不摊平：`type != "function"` 一律 `continue`，namespace 整体丢（pr/r2c.go:437-439） |
| **超长** | 摊平名 >64 字节：按 rune 截到 54 字节 + `__` + sha256 前 4 字节 hex（8 字符），总长 ≤64（bridge.go:742-763；测试 `…_custom_tools_test.go:442-450`）。截断后无法按字符串切分还原，回程必须查映射表（bridge.go:138、测试 :596-604） | 无处理 | 无处理（`src/` 无 64 上限；anthropic.ts:548-558 只做 provider 前缀 `cx_` / Claude OAuth 前缀，不截断） | 无处理（不摊平） |
| **撞名** | 三条都**显式拒绝**（返回 error，服务层回 400）：① 摊平名撞顶层 `function`/`custom` 名；② 不同 `(ns,name)` 摊到同一摊平名；③ 摊平名撞 `tool_search` 代理名。同一 `(ns,name)` 重复声明去重不算撞（bridge.go:622-632、704-743；原生路径 responses_namespace.go:32-43、64-72；responses_client_tools.go:69-71；测试 responses_namespace_test.go:64-74、124-136） | **不检测**：`refs[qualified]` 后写覆盖先写（xai_req.go:1043-1073）；摊平名撞顶层同名工具时两份都发给上游。同名子工具在不同 ns 下自然不撞（测试 :3445-3465） | **不检测**：parser 只 push，无去重（parser.ts:150-164）；`toolNsMap.set` 后写覆盖（collaboration.ts:118） | 不适用 |
| **非 function 子项** | 只收 `type=function` 且 name 非空的子项，其余（含 `custom`）**静默跳过**（bridge.go:715-717；responses_namespace.go:57-63）。子项列表读 `tools`，为空再读 `children`（types.go:311-313；bridge.go:709-712）。namespace 自身 name 为空 → 整个跳过（bridge.go:705-707） | `custom` 子项改写成 `type=function` 后再摊平（xai_req.go:968-975 → 1010-1020）；`custom` 名为 `apply_patch`、`tool_search`、`image_generation` 直接丢（:948-953）；缺 `parameters` 补 `{"type":"object","properties":{}}`（:985-991）。只读 `tools`，无 `children` | 只收 `type=function` 且 name 为字符串的子项（parser.ts:155-164）；namespace 内的 `custom` 静默丢（`custom` 仅在顶层处理 :166-175）。只读 `tools` | 不适用（整体丢） |
| **回程还原** | 请求期建映射 `flat → {ns,name}`（`NamespaceToolNames` bridge.go:141-164）；非流 bridge.go:936-946；流式 `output_item.added` :1467-1482、`…arguments.done` / `output_item.done` :1571-1592；原生 R→R 路径 `RestoreResponsesNamespaceCalls` responses_namespace.go:187-207 + 流式 restorer responses_client_tools.go:580-616。还原项 = 裸子名 + `namespace` 字段；wire 上 `namespace` 仅非空时输出（responses_stream_event_wire.go:187-190）。**历史** `function_call` 带 `namespace` → 请求方向按同一函数摊平（bridge.go:268-272；原生 responses_namespace.go:171-185，仅当 `(ns,name)` 在声明表内才改写） | 请求期先 `collectXAINamespaceToolRefs`（从 `tools` 与 `input[].additional_tools` 收，**在摊平前**，xai_req.go:91、1043-1073），响应侧对每个 SSE 事件的 `item` 与 `response.output[*]` 中 `type=function_call` 按 qualified 名查表 → 改回短名 + 写 `namespace`（xai_resp.go:315-345；调用点 xai_executor_stream.go:112、xai_executor_execute.go:92）。**历史** `input[].function_call` 带 `namespace` → qualified 名并删 `namespace`（xai_resp.go:282-313） | `buildToolBridgeMaps` 建 `toolNsMap`（wire 名 → `{namespace,name}`），**只收 tool_choice 允许的工具**（collaboration.ts:103-119）；流式 bridge.ts:975-987、非流 :1525-1547 还原为裸名 + `namespace`（仅 mapped 时带字段）。**历史**：parser 保留 `namespace` 进 `OcxToolCall`（parser.ts:519-521），chat adapter 发 `ns__name`（openai-chat.ts:400、408） | 回程裸名、无 `namespace`（chat_to_responses_response.go:119-125）；历史 `function_call` 名原样、`namespace` 字段忽略（pr/r2c.go:165-200） |
| **tool_choice 映射** | R→CC 桥：`type=function/custom` 取 `name`（忽略 `namespace` 字段），不在已声明集合内 → **整个 tool_choice 丢**（返回 nil）；`type=namespace` 落 default → 丢（bridge.go:765-811）。原生降级路径：`type=namespace` → 改成 `"auto"`；`function` 选择带 `namespace` → 改写成摊平名（responses_namespace.go:113-120；测试 responses_namespace_test.go:76-91） | `type=function` 选择带 `namespace` → qualified 名 + 删 `namespace`；`tool_choice.tools[*]`（allowed_tools）同样处理；非 function 选择不动（xai_req.go:903-945；测试 :3502-3571）。之后 `pruneXAIOrphanedToolChoice`、无 tools 时整体删 tool_choice（xai_req.go:100-103、870） | `toolChoiceAliases` 同时认 `ns__name` 与 `ns.name` 点分形（types.ts:214-217）；`resolveToolChoiceWireName` 把选择名解析成 wire 名（:223-225；openai-chat.ts:685-690；anthropic.ts:922）。无 `type=namespace` 处理 | `function` → 嵌套 `function.name`，`namespace` 字段忽略；其余类型原样透传（pr/r2c.go:488-524） |
| **`functions` 特判** | **无**：`apicompat/` 无 `"functions"` 字面量。唯一特判是 `image_gen`：OpenAI 上游保留原生 namespace（`FlattenResponsesNamespacesExcept(..., {"image_gen": true})`，service/openai_responses_namespace.go:52） | **无**：特判的是 `codex_app` namespace + `automation_update` 工具（schema 换成宽松对象，xai_req.go:33-36、994-1000） | **无**：R→R passthrough 只降 `image_gen` namespace（openai-responses.ts:711-714、866-895），其余 namespace 原样保留（测试 passthrough.test.ts:1115-1152、1658-1697）；`codex-spark` 模型把 namespace 子项**不加前缀**直接提到顶层并剥掉 input 里的 `namespace`（:262-350，又一种摊平） | 无 |
| **`additional_tools`** | 转换前先合并进顶层 tools：`EffectiveResponsesTools`（bridge.go:74-）与服务层 `liftResponsesAdditionalTools`（gateway_forward_as_responses.go:206、226） | `promoteXAIAdditionalTools` 提到顶层 tools（xAI 不认 additional_tools，xai_req.go:771-830） | passthrough 路径就地降级 `input[].additional_tools[].tools`（测试 :952-1000） | **无处理**（pr/r2c.go 无 `additional_tools`）——Codex 的工具在这里连 function 都会丢 |
| **R→Anthropic** | 复用同一套：`AdaptResponsesClientTools`（内部调 `FlattenResponsesNamespaces`）先改写 body，再 `ResponsesToAnthropicRequest`；回程 `RestoreResponsesClientToolPayload` / `NewResponsesClientToolStreamRestorer` 用同一映射表（gateway_forward_as_responses.go:41、206-215、425、469）。转换器本身无 namespace 分支，未经预处理时 `default` 会把 `type=namespace` 原样塞给 Anthropic（responses_to_anthropic_request.go:557-590）；历史 `function_call` 的 `namespace` 字段被忽略（:135-147） | 不适用 | 同一 `namespacedToolName` + `toolNsMap`，`tool_use.name = toWire(ns__name)`（anthropic.ts:597、644-646、717） | 不适用 |
| **许可证** | LGPL-3.0：思路与字段语义可参考；整包复制需评估义务（Go 静态链接≈整项目跟随） | MIT：借鉴零义务；复制代码保留版权与许可全文 | MIT：同上 | AGPL-3.0：只借坑清单思路（schema 修补、空名过滤），不复制代码 |

---

## 二、各家分歧点

1. **撞名**：只有 sub2api 拒绝（400）。CLIProxyAPI 与 opencodex 都是后写覆盖、静默；new-api 不适用。sub2api 的注释说明了理由：歧义由摊平制造、原生 Responses 上游按 `ns+name` 路由本是合法请求，不能静默降级到「重复声明发上游 + 回程还原到错误工具」（bridge.go:622-626）。
2. **超长**：只有 sub2api 处理（64 字节 + sha256 短哈希）。它的代价是回程不能字符串切分、必须带映射表——sub2api 三条路径都已经是「请求期建表、响应期查表」，所以不额外付费。其余三家都没有上限，撞到上游 64 限制时直接由上游 400。
3. **非 function 子项**：sub2api / opencodex 静默丢 namespace 内的 `custom`；CLIProxyAPI 把它改成 `function` 再摊平（但回程只还原成 `function_call`，不会还原成 `custom_tool_call`，`collectXAINamespaceToolRefs` 收的是所有子项，xai_req.go:1058-1065）。对 Portage 的 golden 样本（`functions` namespace 里 `custom exec` + `function wait`）这是实质差异：按 sub2api/opencodex 做法 exec 会丢，按 CLIProxyAPI 会变成 function。
4. **`children` 别名**：只有 sub2api 同时认 `tools` 与 `children`（types.go:311-313）。golden 样本与其他三家都只用 `tools`。
5. **tool_choice 指向 namespace 子工具**：sub2api R→CC 桥是「不在声明集合 → 整个 tool_choice 丢」，且它不看 `namespace` 字段，所以 `{"type":"function","name":"wait","namespace":"functions"}` 会被丢成等价 auto（bridge.go:765-811）；sub2api 原生路径与 CLIProxyAPI 都会正确改写成摊平名；opencodex 额外认 `ns.name` 点分形。`type=namespace` 的选择（整组）只有 sub2api 原生路径处理（→ `auto`）。
6. **回程映射表的范围**：opencodex 只把 tool_choice 允许的工具收进 `toolNsMap`（「上游输出不可信，只还原客户端授权的工具」，collaboration.ts:112-114），其余两家全量收。
7. **前缀幂等**：CLIProxyAPI 独有——子名已带 `ns__` 或以 `mcp__` 开头时不再加前缀（xai_req.go:1027-1041），代价是 `mcp__` 开头的子名回程查表键与其他家不一致。sub2api / opencodex 都无条件拼接。
8. **`functions` 特判**：四家都没有。各自特判的是 `image_gen`（sub2api、opencodex）与 `codex_app`（CLIProxyAPI）。

---

## 三、new-api PR 5209 的两条坑（作 §5 坑清单候选，本票只记录）

- **空名过滤**（pr/r2c.go:441-445）：`strings.TrimSpace(name) == ""` 的 function 工具跳过，注释给出实证：DeepSeek 对 `function.name` 要求最小长度 1，空名回 400 `Invalid 'tools[0].function.name': empty string`。
- **`normalizeToolParameters`**（pr/r2c.go:467-484）：`parameters` 为 nil 或非对象 → 替换为 `{"type":"object","properties":{}}`；缺 `type` → 补 `"object"`；缺 `properties` → 补 `{}`。注意它就地改 map。CLIProxyAPI 也有同类补丁（缺 `parameters` 补空对象 schema，xai_req.go:985-991；根 union 分支补 `type:object`，:957-967），opencodex 的 `normalizeParameters` 只补 `type:"object"`（parser.ts:141-144）。

---

## 四、对 #93「尚未指定」项的事实性输入（不裁）

- **超长截断与哈希算法**：现成算法只有 sub2api 一份（截 54 字节 rune 边界 + `__` + sha256[:4] hex）；LGPL-3.0，抄算法思路无碍，逐行复制需评估。
- **多 namespace 同名子工具**：`ns__name` 前缀天然区分（CLIProxyAPI 测试 :3445-3465）；真正的撞名只发生在「摊平名撞顶层名」或「截断哈希碰撞」两种情况，四家中只有 sub2api 检测并拒绝。
- **R→Anthropic 回程是否直接复用 R→CC 口径**：sub2api 与 opencodex 都是复用（同一摊平函数 + 同一映射表，只是 `tool_use`/`tool_calls` 载体不同）。
- **R→R 透传遇到不认 namespace 的上游**：sub2api 原生路径（`FlattenResponsesNamespacesExcept`，OpenAI 上游保留 `image_gen`）、CLIProxyAPI xAI 路径、opencodex keyed-platform（只降 `image_gen`）三家都是「按上游能力选择性摊平 + 回程还原」，没有一家在 R→R 上一律透传。
