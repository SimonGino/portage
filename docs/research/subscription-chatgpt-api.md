# Research：ChatGPT API（Sign in with ChatGPT）接入机制、ToS 与服务器形态（#187）

调研日期 2026-10-08；父图 [#186](https://github.com/SimonGino/portage/issues/186)。只作事实收集，不含裁决。

## 事实源

- OpenAI 官方开发者文档（2026-10-08 抓取）：
  - [D1] <https://developers.openai.com/siwc/token-sharing-open-source>（总览）
  - [D2] `.../token-sharing-open-source/sign-in`
  - [D3] `.../token-reference`
  - [D4] `.../models-and-inference`
  - [D5] `.../errors-and-recovery`
  - [D6] `.../preview-limitations`
  - [D7] `.../self-hosted-vms`
  - [D8] <https://developers.openai.com/siwc/request-client-id>、[D9] <https://developers.openai.com/siwc/quickstart>
  - [D10] <https://learn.chatgpt.com/docs/sign-in-with-chatgpt>（用户侧说明）
- 参考实现 magpie（MIT，`~/Code/GitHub/magpie`，只读）：`internal/provider/chatgpt_api.go`（下称 `api.go`）、`signin.go`、`logins_on.go`、`login_usage.go`、`subscription_usage.go`、`session_identity.go`；对照 `provider/codex_request.go`、`provider/account.go`、`gateway/gateway.go`、`gateway/codex_backend.go`。
- 本项目：`CONTEXT.md`「凭证池」「配额」，`docs/口径层设计.md` 第 54、253、278、314 行，`internal/protocol/openairesponses/decode.go`。
- **没拿到的一手源**：OpenAI Terms of Use 原文（`openai.com/policies/*`、`help.openai.com/*` 对抓取一律 403）；help 文章 `20001410`、`20001542`、`10471989` 同样读不到。ToS 条款只有第三方转录（见 §5），标明为二手。

## 1. 授权流（逐步）

magpie 与官方 D2 一致，逐步如下。

0. **前提**：官方口径是「开源 / 本地托管 app」通道。D1：「These docs explain ChatGPT plan usage for open-source and locally hosted apps.」
1. **host id**：首次登录前生成并持久化 `ext_agent_host_id`，每台宿主一个，同一宿主永远发同一个值（D2）。magpie 用 `urn:uuid:` v4，存 `appdir.Config()/chatgpt-api-host`（`api.go:142`）。D1：「A host ID is not an authentication credential.」且须是不透明值，不得含邮箱 / 用户 id。
2. **起监听、生成 state / nonce / PKCE verifier**（每次尝试新生成）。
3. **打开授权页** `https://auth.openai.com/api/accounts/authorize`（`api.go:173`），参数：`client_id=dynamic_agent_client`（首次，动态注册）、`agent_name_hint`（仅首次，展示用）、`ext_agent_host_id`、`response_type=code`、`redirect_uri`、`scope=openid profile email offline_access resource.invoke chatgpt.tokens.use.direct`（`api.go:65`）、`resource=https://api.openai.com/v1`、`state`、`nonce`、`code_challenge`(S256)。
4. **回调**：校验 `state`；首次注册必须回带 OpenAI 签发的 `client_id`（形如 `oaiapp_...`），没有则注册未完成（D2；magpie `api.go:203`）。再次登录回调可省略 `client_id`，沿用已存的，不一致则拒。
5. **换 token**：POST 表单 `grant_type=authorization_code` 到 `https://auth.openai.com/api/accounts/oauth/token`，带签发的 `client_id`、`code`、`code_verifier`、**同一个** `redirect_uri`、`resource`；无 client secret（公共客户端）。
6. **校验**：ID token 验 JWKS 签名、`iss`、`aud`=签发的 client_id、`exp`、`nonce`；**必须确认授予的 scope 含 `chatgpt.tokens.use.direct`**，否则只是身份登录、不能用订阅额度（D2、D5）。magpie 校验 iss / aud / nonce / exp（`api.go:250`），**没验签名**（`jwtClaims` 只解码）；官方要求验签，我们若做须补。缺 direct scope 时 magpie 直接报错「账号可能不合格」（`api.go:348`）。
7. **存储**：每个 (client_id, 验证过的身份) 一条受保护记录，存 token、scope、过期、ID token、host id；0600、原子写（D2）。
8. **sign-out**：D2 / D3 都**没写**撤销。magpie 读 `/.well-known/openid-configuration` 的 `revocation_endpoint`，POST `token` + `token_type_hint=refresh_token` + `client_id`（`api.go:435`）。该端点来自 OIDC 发现文档，官方开发者文档未承诺。用户也可在 ChatGPT 设置里断开，但 D5：「OpenAI doesn't notify your tool when a user disconnects」。

### redirect_uri 的硬约束（直接影响服务器形态）

D2 原文：「Use an HTTP loopback callback on `127.0.0.1` from initial registration onward」；「only the port may vary」；「Do not substitute with `localhost`」；`/callback` 与 `/auth/callback` 不等价。magpie 默认 `127.0.0.1:1455`，被占则随机端口（`api.go:55`）。**结论：redirect_uri 不能改成网关的外部 URL**，从首次注册起就绑定 loopback 形态。

## 2. token 字段与刷新

| 字段 | 内容 | 来源 |
| --- | --- | --- |
| `access_token` | JWT，`aud=https://api.openai.com/v1`，`expires_in: 3600`（1 小时） | D3 |
| `refresh_token` | 30 天；每次刷新**轮换**，返回新 refresh 并重置 30 天 | D3 |
| `id_token` | 身份；`sub` 作账号身份，`https://api.openai.com/auth.chatgpt_plan_type` 带套餐（magpie `siwcPlan`） | D2、magpie |
| `scope` | `chatgpt.tokens.use.direct email offline_access openid profile resource.invoke` | D3 |
| `earliest_refresh_at` | 响应里有，文档未解释语义；magpie 不读 | D3 |
| `client_id` | 签发的 `oaiapp_...`，绑定「认证用户 + 注册时所选 workspace」；刷新、撤销都带它 | D1 |

magpie 落库形态 `siwcCreds`（`api.go:81`）：`clientId / hostId / sub / email / idToken / access / refresh / expires / scopes`，整体 JSON 存 `logins.json` 的 `Auth`。

**刷新**（`api.go:378`）：
- 过期前 3 分钟（`siwcMargin`，`api.go:68`）提前刷新；`grant_type=refresh_token` + `client_id` + `refresh_token` + `resource`。
- **全局互斥锁**：refresh 轮换，并发两路会用掉同一个旧 token；拿到锁后复查是否已被别人刷新。
- 死亡码（`api.go:368`，与 D5 一致）：`invalid_grant / invalid_refresh_token / token_expired / refresh_token_expired / refresh_token_invalidated / refresh_token_reused` → 账号标 `Lapsed`，要求重新登录。`invalid_client` → 配置问题，重新登录。
- 其他错误（网络抖动）：access 还没过期就继续用旧的，不清凭证。D5：「Don't erase credentials for temporary network or infrastructure failures.」
- refresh 30 天内无人用即过期，需要保活。magpie 的 `keepalive.go` 只管 Codex 保存账号，**chatgpt-api 没有保活**。

## 3. 出站：头、模型、协议

**头**：只有 `Authorization: Bearer <access>`（D4；magpie `sign` 只设这一个，`api.go:701` 起）。magpie 通用转发另加 `Content-Type`、`Accept`、`User-Agent: magpie/<ver>`（`gateway.go:2667`）。**没有** `chatgpt-account-id`、`originator`、`OpenAI-Beta`、Codex 版本头（对照 §6）。D4：不要用 ChatGPT 的 `backend-api`。

**协议**：D4 只文档化 `POST https://api.openai.com/v1/responses`，Chat Completions 未提；D5 的 `subscription_sharing_route_not_supported`（403）说明别的路由会被拒。**结论：仅 Responses**；Chat / Anthropic 入口要走我们已有的转换路径转成 Responses 出向。

**模型列表**：`GET https://api.openai.com/v1/models`，带 Bearer；响应是 `models[]`（不是标准 OpenAI `data[]`），留 `visibility=="list"`，`slug` 作请求名、`display_name` 作展示，切账号时刷新（D4）。magpie 解析 `slug / display_name / visibility / input_modalities / supported_reasoning_levels / context_window`（`api.go:472` 起的 `siwcFetchModels` 与其后的 `parseSIWCModels`）。magpie 另把 Codex 目录里的新模型并入（`api.go:510`，注释称 gpt-6-luna 等「能跑但两边都没列」），这是实测行为，官方文档没写。

**请求体必须改（D6；magpie `siwcBody` `api.go:584`）**：

| 项 | 要求 |
| --- | --- |
| `store` / `stream` | HTTP 上强制 `store:false`、`stream:true`（非流式客户端要由网关代为聚合） |
| 丢弃字段 | `background conversation max_output_tokens max_tool_calls metadata moderation multi_agent prompt prompt_cache_retention safety_identifier temperature top_logprobs top_p truncation user`（D6 全列；magpie 另删 `previous_response_id`，`api.go:571`） |
| `previous_response_id` | HTTP 上不得带，历史全放 `input` |
| system 角色 | 显式 `system` 消息项被拒；改 `developer` 或用 `instructions` |
| 工具 | 支持 function / custom（含 namespace、`additional_tools`）；**不支持** 图像生成、file_search、code_interpreter、原生 computer use、托管 MCP / connector、`tool_search`；`web_search` 受模型与账号策略约束 |
| 输入 | 文本、图、文件（模型支持时）；不支持音视频、Files 上传、转写 |

**与本项目 Responses codec 的兼容点**（`internal/protocol/openairesponses`、`docs/口径层设计.md:314`）：
- 本项目 Responses 入口的有状态口径已是「`previous_response_id` 显式拒绝 / 能力位」，与 `store:false` 一致；这个渠道的 `supports_stateful_responses` 须为否。
- `max_output_tokens`、`temperature` 我们的 canonical 会解码（`decode.go:28-50`），出向要**按渠道丢弃**。现在没有这类「渠道级丢字段」机制；同协议透传也得改 body，与「透传保真」硬约束有张力，属渠道专属的必要改写。
- 强制 `stream:true`：下游非流式请求需网关吞 SSE 再合成一次性响应（magpie `Account.Stream=true` 即此语义）。
- `usage_limit_exceeded` 可能在流中途以 `response.failed` 到达（D4），不是开流前 429；我们的收场词（`stream_aborted` 等）要能区分。

## 4. 额度与记账

- **共享池**：D10：「App usage counts toward your existing plan limits.」「An app's limit is a cap, not a separate allowance or a reserved portion of your plan.」用户可在 ChatGPT 设置里给每个 app 设「每周额度占比」上限。官方文本是**周**口径；magpie 注释写「five hours」（`api.go:15`），官方文档里**没找到**5 小时窗，只有第三方博客提到，视为未证实。
- **余量不可查**：D4「does not document a usage query endpoint」，响应里的用量字段也没写。用户只能去 <https://chatgpt.com/settings/usage> 看。
- **旁路**：magpie 对 chatgpt-api **没有**余量查询。`subscription_usage.go:324` 只在「只刷新一张卡」时把它列入隐藏名单；`login_usage.go:184` 列出账号后，`loginQuota` 落到通用分支，`savedLoginToken`（`logins_on.go:226`）的 switch 只认 `codex`，其余走 default 报 "can't be used side by side"。Codex 通道用的 `chatgpt.com/backend-api/wham/usage`（`subscription_usage.go:827`）要 Codex 的 token + `chatgpt-account-id`，SIWC token 能否调用**没验证**，且那是 `backend-api`，D4 明令不要碰。`session_identity.go` 只处理 codex / claude，与此通道无关。
- **撞限形态**（D5）：429 `subscription_sharing_usage_limit_exceeded`（别推算重置时间）、503 `usage_unavailable`（退避重试）、403 `user_not_eligible`（不重试不重新授权）。magpie `siwcExplain` 转成人话（`api.go:675`）。
- **记账含义**（供裁决，不是结论）：订阅无单价，流水 `cost` 无从算；token 数是否在 `response.completed.usage` 里，D4 未写、我们没有真机转录，**需采样验证**。月度 USD 配额（`CONTEXT.md`「配额」）对这类渠道不成立，只能记 token 或按等价单价估算（后者是 PO 决策）。上游撞限不是我们的 `quota_exceeded`，流水里要另设词。

## 5. ToS 摘录

**官方一手（开发者文档）**：
- D1：「These docs explain ChatGPT plan usage for open-source and locally hosted apps.」
- D1：「If you're interested in offering it in a paid or remotely hosted app, complete the interest form.」（表单：<https://openai.com/form/sign-in-with-chatgpt-interest/>）
- D1：「An agent host is the environment where an instance of your tool runs, such as a laptop installation or a self-hosted VM.」
- D9：「ChatGPT plan usage is available to all open-source partners and selected private clients.」
- D8：Sign in with ChatGPT「is currently offered to a select group of commercial partners.」（指标准 client id 申请；开源通道走动态注册）
- D7（自托管 VM）：「A `127.0.0.1` callback reaches the computer running the browser, not the remote VM.」；「Host-specific usage attribution and revocation of ChatGPT plan access for transferred sessions are not yet available.」
- D1：「each issued client_id is bound to the authenticated user and the workspace selected during registration.」

**官方一手没有的**：没有任何一句明确禁止或明确允许「服务器部署后给他人使用」。「跑在用户自己机器上」不是被定义出来的术语，D1 只用 laptop / self-hosted VM 举例；「paid or remotely hosted」则被明确划到另一条申请路径。D1 / D7 / D9 均未链到专门条款，只链到 <https://openai.com/policies>。

**ToS 本体（二手）**：OpenAI Terms of Use 抓不到。下面来自第三方转录 <https://conductatlas.com/platform/openai/terms-of-use-row/provision/CA-P-017119/account-credential-sharing-prohibited/>（标 2026-01-01 版，抓于 2026-03-10）：「You may not share your account credentials or make your account available to anyone else」。账号共享政策页（help 10471989）只见搜索摘要：账号供创建者个人使用，他人应自行注册。**这两处须 PO 或后续用浏览器读原文复核后再写进口径层。**

**读法（推断，非官方表述）**：官方通道的设计意图是「用户本人授权、用户本人的机器 / VM」；网关把一个账号的额度转手给其他用户，既超出 D1 的适用范围，也撞上账号共享条款。magpie 自己不拦（父图 #186 已记）。

## 6. 与 Codex 后端通道的对照（只说明为什么不选）

| | ChatGPT API（本票） | Codex 后端 |
| --- | --- | --- |
| 端点 | `api.openai.com/v1/responses` | `chatgpt.com/backend-api/codex`（`account.go:833`） |
| client | 动态注册的自家公共 client，自报 `agent_name_hint` | 借用 Codex CLI 的 client `app_EMoamEEZ73f0CkXaXp7hrann`（`account.go:825`） |
| 授权 | 官方文档化，scope `chatgpt.tokens.use.direct` | 抄 Codex 的 scope 与 `originator=codex_cli_rs`（`signin.go:40`） |
| 头 | 仅 Bearer | Bearer + `chatgpt-account-id` + `OpenAI-Beta` + `originator: codex_cli_rs` + 伪造 Codex 版本与 UA（`codex_request.go:37-47`） |
| 请求体 | 自带 instructions，不带 Codex 的 | 带 Codex instructions、compaction、封印（`codex_backend.go`） |
| 性质 | OpenAI 开的正规口 | 冒充官方客户端 |

不选 Codex 后端的理由：冒充客户端、依赖未公开协议、绑 Codex 版本头，封号与失效风险更高；ChatGPT API 通道有官方文档与错误码合同。

## 7. 服务器部署形态下的登录方案

约束：redirect_uri 只能是 `http://127.0.0.1:<port>/auth/callback`（§1）。我们的网关多半不在用户浏览器所在机器上。

**方案 A：粘贴回调地址（magpie 现行做法）**。网关生成授权 URL，redirect 写 `127.0.0.1:<port>`，用户在自己浏览器登录；浏览器最后落在一个打不开的 `127.0.0.1` 页，用户把整条地址栏复制回管理端；网关校验 state / 端口 / 路径后用 code 换 token。magpie 实现：`signin.go:340`（`PasteCallback = true`）、`pastedCallback`（`signin.go:418`）、`ownCallback` 只认 localhost / 127.0.0.1 且端口、路径、state 吻合（`signin.go:476`）。要点：换 token 不依赖监听端口，只要 redirect_uri 字符串与授权时一致；host id 由网关生成并持久化；网关要在内存里短时保存 verifier / state / nonce。**官方文档没描述这条路**，但 redirect 仍是 loopback，机制上不违反 D2。

**方案 B：本机登录后导入凭证（官方文档化，D7）**。D7 流程：在 VM 上先建稳定 host id；在自己电脑用同一工具与 client 为同一用户、同一 workspace 完成 OAuth；经 SSH 等安全通道把受保护凭证（`client_id / access / refresh / id_token`）拷到 VM；VM 保留自己的 host id，后续刷新由 VM 负责。落地需要一个本机登录工具：口径层有「v1 不做 CLI」的裁定，所以得另议；或管理端提供「粘贴凭证 JSON」导入框。

**方案 C：网关外部 URL 回调**。**不可行**，违反 D2。

另两条配套事实：
- 声明文件（`channels.yaml`）形态下无管理面（`口径层设计.md:253,278`），登录流程无处发起，只能走方案 B 把凭证值写进文件；但 refresh 轮换后凭证会变，与「文件即事实源」纪律冲突，须单设口径。
- 凭证落库形态：现有 `api_key` / `service_account` 是渠道级二选一（`CONTEXT.md` 凭证池、`口径层设计.md:54`）。OAuth 账号要存一个会变的 JSON（access / refresh / exp / client_id / host_id），刷新要有全局锁与「失效」状态；而 v0.95 口径是「任何状态码都不改凭证状态」，refresh 死亡码要不要例外须裁。

## 8. 待 PO 裁决

1. **做不做**：官方通道只覆盖「开源 / 本地托管 app」，「paid or remotely hosted」需另申请；网关给多用户用，落在范围外且撞账号共享条款（二手）。建议：做，但定位「PO 本人 / 单用户」，口径层写明风险由 PO 自担（与 #186 既有裁定一致）。
2. **登录方案**：A（粘贴回调）、B（本机导入）或两者都做。建议先 A：管理端已有 Web 形态，不需要新 CLI；B 作为声明文件形态的补充。
3. **ToS 原文复核**：Terms of Use 与账号共享政策我只拿到二手转录，要不要 PO 用浏览器读一遍原文再写口径。建议要。
4. **多用户边界**：技术上仍不拦（#186 已裁不拦），还是至少把该渠道标成「仅管理员可用」之类的软限制。
5. **渠道级请求改写**：该渠道需强制 `stream:true`、丢 14 个字段、system 改 developer、剥托管工具，同协议透传也要改 body。是否接受这条对「透传保真」的渠道专属例外。
6. **流水与配额**：订阅无单价，`cost` 记 0、记 token 还是按等价单价估算；该渠道是否计入用户月度 USD 配额；是否单设流水词（如 `plan_limit_exceeded`）区分上游撞限与我们自己的 `quota_exceeded`。需要先采一份真机 SSE 确认 usage 字段。
7. **余量展示**：官方不给查询，是否只展示「上次撞限时间」，不做余量。建议不做余量。
8. **保活**：refresh 30 天不用即死，是否做定时刷新；magpie 没做。
9. **失效状态例外**：refresh 死亡码时是否允许把凭证标为「需重新登录」，这与 v0.95「任何状态码都不改凭证状态」冲突，要明确是例外还是改口径。
10. **模型目录**：只认 `/v1/models` 的官方列表，还是照 magpie 并入 Codex 目录里的未列模型（后者官方文档未承诺）。建议只认官方列表。
