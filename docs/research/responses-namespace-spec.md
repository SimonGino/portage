# Research：Responses `namespace` 工具的规范与 Codex 客户端契约（#83）

调研日期 2026-09-02。四问逐条回答，结论回溯一手来源（OpenAI 官方 OpenAPI spec / 官方 SDK 类型 / 官方文档 / openai/codex 源码 / 本仓库 golden）；查不到的点明说查不到。本页只出事实，命名与回程口径由 #93 图下的裁决票定。

## 事实源

**OpenAI 官方（抓取日期 2026-09-02）**

1. OpenAPI spec（`master` 分支，OpenAPI 3.1）：<https://raw.githubusercontent.com/openai/openai-openapi/master/openapi.yaml>（仓库 <https://github.com/openai/openai-openapi>）。旧的 `manual_spec` 分支（3.0.0）里**没有** namespace 工具。
2. openai-python：`src/openai/types/responses/` 下 `namespace_tool.py`、`namespace_tool_param.py`、`response_function_tool_call.py`、`response_function_tool_call_param.py`、`response_custom_tool_call.py`、`response_input_item_param.py`、`tool_choice_function.py`、`tool_choice_custom.py`、`tool_choice_allowed.py`、`tool.py` — <https://github.com/openai/openai-python>
3. openai-node：`src/resources/responses/responses.ts` — <https://github.com/openai/openai-node>
4. 函数调用指南：<https://developers.openai.com/api/docs/guides/function-calling>（platform.openai.com 的同名页 301 到此）
5. tool search 指南：<https://developers.openai.com/api/docs/guides/tools-tool-search>
6. API reference（responses.create）：<https://developers.openai.com/api/reference/resources/responses/methods/create>

**openai/codex（codex-rs）** —— 引文均取自 commit `bdfd769640927a627dd48ab3fcc1ae8bc08bdd0a`（2026-09-02 的 `main`），永久链接前缀 <https://github.com/openai/codex/blob/bdfd769640927a627dd48ab3fcc1ae8bc08bdd0a/codex-rs/>。**codex-rs 本机没有 clone，全部经 GitHub 原始文件与 API 读取。**

**本仓库 golden**：`testdata/golden/responses-stream-*/{request.json,response.raw}`（10 个样本，Codex CLI 0.147 + gpt-5.6-luna，2026-08-14 采）。

**参考仓库**（`docs/agents/reference-repos.md` 名单内）：sub2api `backend/internal/pkg/apicompat/`、CLIProxyAPI `internal/translator/{claude,openai}/openai/responses/`、opencodex `src/{types.ts,responses/parser.ts,bridge.ts}`。new-api 与 litellm 全仓无 Responses namespace 处理（`grep -i namespace` 只命中 pydantic `protected_namespaces` 与 MCP 的 `namespaced_tool_name`）。

---

## 一、`{"type":"namespace",…}` 的规范定义

`NamespaceToolParam`（spec，逐字）：

```yaml
NamespaceToolParam:
  properties:
    type:  { enum: [namespace], default: namespace }
    name:  { type: string, minLength: 1,
             description: The namespace name used in tool calls (for example, `crm`). }
    description: { type: string, description: A description of the namespace shown to the model. }
    tools:
      type: array
      minItems: 1
      items:
        oneOf:
          - $ref: "#/components/schemas/FunctionToolParam"
          - $ref: "#/components/schemas/CustomToolParam"
        discriminator: { propertyName: type }
  required: [type, name, description, tools]
  title: Namespace
  description: Groups function/custom tools under a shared namespace.
```

| 问 | 答 | 来源 |
| --- | --- | --- |
| 子项允许哪些 type | **只有 `function` 与 `custom`**，`minItems: 1`。没有第三种。 | spec `NamespaceToolParam.tools.items.oneOf`；openai-python `namespace_tool.py` 的 `Tool = Union[ToolFunction, CustomTool]`；openai-node `tools: Array<NamespaceTool.Function \| CustomTool>` |
| 是否允许嵌套 | **不允许**——`NamespaceToolParam` 不在自己的 `tools.items.oneOf` 里。文档对嵌套只字未提，是 schema 直接排除。 | 同上 |
| `description` 语义 | **必填**（`required` 四项之一），「A description of the namespace shown to the model」。tool search 指南：「Make namespace descriptions clear and descriptive of the use case, because the model relies on this description to decide when to load a subset of functions in that namespace. Avoid overly long descriptions.」函数调用指南补：命名空间描述保持简短，细节放子工具描述。 | 来源 1、4、5 |
| `name` 约束 | 工具声明处**只有 `minLength: 1`**，无 pattern、无 maxLength。但 `function_call_output` 上的 `namespace` 字段是 `minLength 1 / maxLength 64 / pattern ^[a-zA-Z0-9_-]+$`——这才是能安全回程的事实约束。子 function 的 `name` 是 `minLength 1 / maxLength 128 / ^[a-zA-Z0-9_-]+$`。 | spec `NamespaceToolParam`、`FunctionCallOutputItemParam`、`FunctionToolParam` |
| GA 还是 beta | 在 **GA** 的 `Tool` union（`POST /responses`）里；`/responses?beta=true` 另有一份平行的 `BetaNamespaceToolParam`。spec 与 SDK 都没有 preview/beta 标注。 | spec `Tool:` oneOf |
| 何时引入 | SDK 首次出现于 openai-python 2.25.0 / openai-node 6.26.0（均 2026-03-05，「api: gpt-5.4, tool search tool, and new computer tool」）；`defer_loading` 于 node 6.30.0（03-16）加入；`function_call_output` 的 `name`/`namespace` 迟至 python 2.53.0（2026-08-03，「Add gpt-5.5 and tool name/namespace to Responses types」）。 | 两份 SDK CHANGELOG |

补两条与摊平决策相关的事实：

- 子 function 除常规字段外还有 `defer_loading`、`allowed_callers`、`output_schema` 等（openai-node `NamespaceTool.Function`）。tool search 指南明说「For namespaces, `defer_loading` applies to the functions inside the namespace, not to the namespace object itself」，同一 namespace 可混放 deferred 与非 deferred 子工具，并建议「keep each namespace to fewer than 10 functions」。
- 模型门槛：文档只对 tool search 说「Only gpt-5.4 and later models support it」，**没有**对 namespace 单独声明模型门槛。

**golden 实测**（`testdata/golden/responses-stream-*/request.json`，10/10 一致）：namespace 不在顶层 `tools`，而在 input 里的 `{"type":"additional_tools","role":"developer","tools":[…]}` 项内，一个 `{"type":"namespace","name":"functions","description":"","tools":[…]}`，三个子项——`{"type":"custom","name":"exec","format":…}`、`{"type":"function","name":"wait",…}`、`{"type":"function","name":"request_user_input",…}`。即：**custom 与 function 混在同一 namespace 里，是线上常态，不是理论可能**。响应侧 `response.created/in_progress/completed` 的 `response.tools` 原样回显这一个 namespace 工具。

---

## 二、回程 `function_call` 的形态：带不带 `namespace`，名字裸不裸

**规范：`namespace` 是 output item 上的可选字段，`name` 是裸子工具名。**

spec `FunctionToolCall`（同一 schema 既做 output 也做 input item）：

```yaml
FunctionToolCall:
  properties:
    type: { enum: [function_call] }
    call_id: …
    namespace: { type: string, description: "The namespace of the function to run." }
    name:      { type: string, description: "The name of the function to run." }
    arguments: …
    status: { enum: [in_progress, completed, incomplete] }
  required: [type, call_id, name, arguments]
```

`CustomToolCall` 同样有 `namespace: string`（"The namespace of the custom tool being called."）。openai-python `ResponseFunctionToolCall.namespace: Optional[str] = None`，openai-node `namespace?: string`。

tool search 指南的官方示例（逐字）：

```json
{ "type": "function_call", "name": "list_open_orders", "namespace": "crm",
  "call_id": "call_abc123", "arguments": "{\"customer_id\":\"CUST-12345\"}" }
```

—— `name` 是裸的 `list_open_orders`，归属靠 `namespace: "crm"`。同一页另有一例：**顶层**（不在任何 namespace 里）的 deferred function 回程时 `namespace` 等于它自己的名字（`"name":"get_shipping_eta","namespace":"get_shipping_eta"`）；openai-agents-python 把这条编码成 `is_reserved_synthetic_tool_namespace`（`namespace == name` 即「reserved deferred top-level wire shape」，<https://github.com/openai/openai-agents-python/blob/main/src/agents/_tool_identity.py>）。这条只在文档与 Agents SDK 里，spec 未写。

**本仓库 golden 的裸名无 `namespace`，是「`functions` 是默认命名空间」的特例，不是通例。** 依据：

- codex-rs `protocol/src/tool_name.rs` L6-7：`/// Namespace used for top-level function and custom tools.` `pub const DEFAULT_FUNCTION_NAMESPACE: &str = "functions";`；`is_default_namespace()` 把 `None | Some("") | Some("functions")` 判为同一件事；`with_default_namespace()` 把空/缺失补成 `"functions"`。
- 即：`functions` 命名空间的子工具，回程带不带 `namespace` 字段都能被 Codex 路由到。golden 里 `exec` 走的正是这条。
- 非默认命名空间（`mcp__*`、`collaboration`、`multi_agent_v1`、`clock`、`editor` 等）**必须**带 `namespace` 字段，见下面第四节。

golden 观测细节（`response.raw`）：`response.output_item.added/done` 的 item 是 `{"type":"custom_tool_call","id":"ctc_…","call_id":"call_…","name":"exec","input":…}`，**无 `namespace` 键**；`response.completed` 里同一调用被列为 `{"type":"function_call","call_id":"call_…","name":"exec"}`（无 `id`、类型也从 `custom_tool_call` 变成了 `function_call`）。**这份 golden 是经 sub2api 中转录的**（见各 `meta.json` 的 `source`），`response.completed` 的 output 摘要与流式 item 不一致，很可能是中转侧合成的产物，**不足以当作 OpenAI 原生 `response.completed` 形态的证据**；能当证据的是流式 item 那部分（裸名、无 namespace）。

回程之后客户端再发回去时：`function_call` 在 input 里用的是同一个 `FunctionToolCall` schema，`namespace` 可带；`function_call_output` 另有 `name`（≤128）与 `namespace`（≤64，`^[a-zA-Z0-9_-]+$`）两个可选字段（spec `FunctionCallOutputItemParam`）。Codex 侧只回显 `function_call` 的 `namespace`，自己产的 output **不带** `name`/`namespace`（见第四节）。

---

## 三、`tool_choice` 引用 namespace 子工具

**规范里没有任何 namespace 感知的 `tool_choice` 形态。** 逐字：

```yaml
ToolChoiceFunction:
  properties:
    type: { enum: [function] }
    name: { type: string, description: The name of the function to call. }
  required: [type, name]
ToolChoiceCustom:
  properties: { type: {enum: [custom]}, name: {type: string} }
ToolChoiceParam:
  oneOf: [ToolChoiceOptions, ToolChoiceAllowed, ToolChoiceTypes, ToolChoiceFunction,
          ToolChoiceMCP, ToolChoiceCustom, SpecificProgrammaticToolCallingParam,
          SpecificApplyPatchParam, SpecificFunctionShellParam]
```

- 没有 `{"type":"namespace",…}` 这个 choice 变体；
- `ToolChoiceFunction` / `ToolChoiceCustom` **没有** `namespace` 属性（openai-python `ToolChoiceFunction` 只有 `name` 与 `type`，node 同）；
- `ToolChoiceAllowed.tools` 的 items 是 `type: object, additionalProperties: true`（无类型约束），文档示例只有 `{"type":"function","name":"get_weather"}` / `{"type":"mcp",…}` / `{"type":"image_generation"}`，**没有任何 namespace 示例**。服务端是否接受 `{"type":"function","name":…,"namespace":…}` 进 `allowed_tools`，**一手来源查不到**。

也就是说：规范层面，指名调用 namespace 里的某个子工具**没有明确写法**；`allowed_tools` 是否能表达也无据。参考实现各自防御性处理：

- sub2api：`tool_choice.type == "namespace"` 直接降为 `"auto"`；带 `namespace` 字段的具名 choice 按摊平名重写（`responses_namespace.go` `FlattenResponsesNamespacesExcept`，测试 `TestFlattenResponsesNamespaces_NamespaceGroupChoiceFallsBackToAuto`）。
- CLIProxyAPI：从 `tool_choice.namespace`、`tool_choice.function.namespace`、`tool_choice.custom.namespace` 三处任取其一，拼成限定名去匹配（`claude_openai-responses_request.go` L490-500）。
- opencodex：`toolChoiceAliases()` 同时接受 `ns__name` 与 `ns.name` 两种别名（`src/types.ts` L214-217）。

三家都在猜，且猜法不一致——这正说明规范没定。

---

## 四、Codex 客户端如何路由，平铺名会怎样

**（1）Codex 怎么发 tools。** 两条路：

- 常规 Responses 路径：`ToolSpec` 原样序列化进顶层 `tools`（`tools/src/tool_spec.rs` L82-93，`core/src/client.rs` L936-975）。内置 function/custom 工具留在顶层，**不套 `functions`**；只有 handler 自带命名空间的工具（MCP、动态/app 工具、子 agent 工具、`clock`）以 `{"type":"namespace",…}` 出现。
- **Responses Lite** 路径（`use_responses_lite` 的模型：gpt-5.6-*、gpt-daybreak-* 等）：`create_tools_json_for_responses_lite`（`tools/src/tool_spec.rs` L95-142）把所有顶层 function/custom 工具折进一个 `ResponsesApiNamespace { name: "functions", description: "", tools }`，并且整份 tools 以 developer 角色的 `additional_tools` **input item** 下发，不走顶层 `tools` 字段。`default_namespace_description()` 对 `functions` 返回空串，对别的返回 `Tools in the {ns} namespace.`（`tools/src/responses_api.rs` L46-80）。

  —— **本仓库 golden 命中的正是这条**：模型 gpt-5.6-luna、`additional_tools` 里一个 `functions` namespace、`description` 为空串。

  演进见 PR [#27946](https://github.com/openai/codex/pull/27946)（06-23，改用 `additional_tools` input item，并预告「Forced namespacing for _all_ tools will land in a following PR … The goal is to eventually expand the scope of this to _all_ requests from codex」）与 [#37022](https://github.com/openai/codex/pull/37022)（08-05，「Canonicalize default tools under the `functions` namespace」）。**即：`functions` 全包目前限于 Responses Lite，但 Codex 明说要推广到所有请求。**

- 非默认命名空间取值：MCP 是 `mcp__<server>`（或 `mcp__codex_apps__<connector>`，`codex-mcp/src/tools.rs` L57-61、L228-234）、子 agent 是 `multi_agent_v1` / `collaboration`、还有 `clock`、`editor`。整条能力由 provider gate `ProviderCapabilities.namespace_tools`（默认 `true`）控制。

**（2）入站 item 类型。** `protocol/src/models.rs`：

```rust
FunctionCall {
    id: Option<ResponseItemId>,
    name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    namespace: Option<String>,
    arguments: String,
    call_id: String, …
},
```

`CustomToolCall` 同款 `namespace: Option<String>`。测试 `function_call_deserializes_optional_namespace` 往返 `"namespace": "mcp__codex_apps__gmail"`。

**（3）路由按 `(namespace, name)` 精确查表，不解析平铺名。** `core/src/tools/router.rs` L246-298 用 `ToolName::new(namespace, name).with_default_namespace()` 构 key；`core/src/tools/registry.rs` L322-337 / L459-463 注册与查找都用同一个归一化后的 key。**代码里没有任何按 `__` 或 `.` 拆名的回退**——早期 PR [#17556](https://github.com/openai/codex/pull/17556)（04-12「Support flattened deferred MCP tool calls」，起因正是模型回了 `mcp__node_repl__js` 而 handler 键是 `mcp__node_repl:js`）已被 [#17404](https://github.com/openai/codex/pull/17404)（04-15「register all mcp tools with namespace … without supporting fallbacks」）取代并删除。

**（4）查不到名字 → `unsupported call`，且不致命。** `registry.rs` L495-537、L818-823：

```rust
fn unsupported_tool_call_message(payload: &ToolPayload, tool_name: &ToolName) -> String {
    match payload {
        ToolPayload::Custom { .. } => format!("unsupported custom tool call: {tool_name}"),
        _ => format!("unsupported call: {tool_name}"),
    }
}
```

错误是 `FunctionCallError::RespondToModel`，`core/src/tools/parallel.rs` L219-246 把它变成一条 `function_call_output`（custom 则 `custom_tool_call_output`）回给模型，`success: false`、body 就是上面那句话，**本轮继续，不 abort**（abort 走 `Fatal`，只在 payload 类型不匹配等情况）。集成测试 `core/tests/suite/search_tool.rs` L313-318 断言 `function_call_output` 含 `"unsupported call"`。

于是对 #93 最关键的那条：

- 回 `{"name":"functions__exec"}`（无 `namespace`）→ key 归一为 `functions:functions__exec` → **`unsupported call: functions__exec`**。`functions.exec` 同理。
- 非默认命名空间的子工具回裸名而不带 `namespace`（如 `create_event` 却不带 `mcp__codex_apps__calendar`）→ 同样 `unsupported call`。
- **sub2api 注释「平铺名会被判 unsupported call」属实**，且有其自身的端到端记录佐证（sub2api commit `794233832`，2026-07-10，「Codex 0.14x 将 MCP 工具声明为 namespace 工具……codex 按 namespace+name 路由查不到该名字，所有 MCP 工具调用被判为 unsupported call」，提交信息称已用 codex exec + MCP server 端到端验证）。opencodex `src/types.ts` L205-208 独立记同一事实：「the proxy maps this back to {namespace, name} on the return trip (Codex routes MCP calls by an explicit `namespace` field, not by parsing the name)」。
- 后果是「模型被告知这个工具不存在」，不是连接中断——**降级形态是静默失效而非报错**，与 GLM 那次现象（编工具名、当文本吐出）同类。

**（5）Codex 自己回下一轮时。** 入站 `FunctionCall` 原样存进历史并原样回发（`core/src/stream_events_utils.rs` L290-330、`client_common.rs` L55-64），带 `namespace` 进来就带 `namespace` 出去（测试断言序列化出 `{"type":"function_call","name":"spawn_agent","namespace":"collaboration",…}`）。Codex **自产的 `function_call_output` 永远 `name: None, namespace: None`**（`models.rs` L1838-1845），字段被 `skip_serializing_if` 略掉。

---

## 五、对 #93 的直接含义（供裁决票引用，不是裁决）

1. 「`functions` 无 namespace 字段」是默认命名空间的特例，**不能推广**成「回程一律裸名」。R→CC / R→Anthropic 的回程还原必须按声明侧的归属决定带不带 `namespace`：默认 ns 可带可不带，非默认 ns 必须带。
2. 摊平必须是**可逆**的：Codex 端不做任何名字拆分，网关必须自己留映射表（sub2api、opencodex、CLIProxyAPI 三家都是这么做的）。摊平名超长截断+哈希后更不可能按字符串还原，映射表是唯一出路。
3. 子工具含 `custom` 是常态（golden 10/10 里 `exec` 就是），摊平口径必须同时覆盖 `function` 与 `custom` 两类——sub2api 的原生摊平只处理 `function` 子项（`responses_namespace.go` 里 `child["type"] != "function"` 就 skip），CLIProxyAPI 两类都收。这是两家的口径差，不是笔误。
4. `tool_choice` 指名 namespace 子工具规范无定义。要么按参考实现选一种猜法，要么在网关侧降级（sub2api 的 `namespace` 型 choice → `auto`）。这条得 PO 拍。
5. Codex 已宣示要把 `functions` 全包推广到所有请求（PR #27946 原文），因此「只有 Lite 模型才有 namespace」不能当成稳定假设写进口径。

## 查不到 / 存疑

- `tool_choice` 或 `allowed_tools` 引用 namespace 子工具的服务端行为：spec、SDK、两份指南、API reference 均无。**未找到一手来源**，不做推断。
- namespace `name` 在声明处的字符集/长度约束：spec 只有 `minLength: 1`。64 字符 + `^[a-zA-Z0-9_-]+$` 是从 `function_call_output.namespace` 反推的事实约束，不是声明处的约束。
- 嵌套 namespace：schema 排除，但没有任何文档语句正面说明「不允许嵌套」。
- namespace 工具本身是否有模型门槛（非 tool search 场景）：文档只对 tool search 声明 gpt-5.4+。
- 本仓库 golden 的 `response.completed` output 摘要经 sub2api 中转合成，不可当原生形态证据（见第二节）。要拿到原生 `response.completed` 形态，需直连 OpenAI 重录一份 golden。

---

文档修改人：jinpenga
