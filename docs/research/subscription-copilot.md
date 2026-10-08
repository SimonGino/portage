# Research：GitHub Copilot 订阅接入机制、ToS 与模型范围（#188，地图 #186）

调研日期 2026-10-08。一手来源：magpie 源码（MIT，只读；路径均相对 `~/Code/GitHub/magpie/internal/provider/`，下称 `mp:`）与 GitHub 官方条款页。查不到的点明说查不到。magpie 的行为是「别人怎么抄的编辑器」，**不是 GitHub 的公开契约**：`copilot_internal/*` 无公开文档，magpie 源码注释里引的 VS Code / Copilot CLI 行为是二手转述，未逐条核 VS Code 源码。

## 结论先行

1. 技术上可行：设备码无需回调，服务器形态零障碍；协议面 Chat / Responses / Anthropic Messages 三家都有，与现有 codec（`internal/protocol/{openaicc,openairesponses,anthropic}`）直接对得上。
2. 但整条通道是**冒用官方编辑器身份**（借 Copilot 编辑器的 client id，伪装 `Editor-Version` / `Copilot-Integration-Id` 头）打**未公开接口** `copilot_internal/*`。GitHub 条款没有「允许第三方客户端」的条文，反而有「一个登录只能一人用」，且有公开的限制账号先例（针对代理与凭证共享）。风险高于 ChatGPT API 通道（后者是 OpenAI 为本地开源 app 开的正规口）。
3. 实现体量小到中：设备码轮询 + token 换取 + 缓存 ≈ 300 行（`copilot_signin.go` 131 行 + `copilot_accounts.go` 的 `copilotUser` + `account.go` 的 `copilotToken`）；magpie 额外做的 Auto、模型拒绝重试、GHE、CLI 凭证读取我们都可以不做。

## 一、接入步骤

### 1. 设备码（`mp:copilot_signin.go`）

| 步 | 请求 | 要点 |
| --- | --- | --- |
| ① 申请码 | `POST https://github.com/login/device/code`，form：`client_id=Iv1.b507a08c87ecfe98`、`scope=read:user`（`:20`、`:62`） | 回 `device_code` / `user_code` / `verification_uri` / `expires_in` / `interval`（`:55-61`） |
| ② 展示 | 管理端显示 `user_code` 与 `https://github.com/login/device`（`:70-73`） | 用户在任意设备的浏览器输码并授权 |
| ③ 轮询 | `POST https://github.com/login/oauth/access_token`，`grant_type=urn:ietf:params:oauth:grant-type:device_code`（`:89-90`） | 按 `interval` 起步；`authorization_pending` 继续，`slow_down` 间隔 +5s（`:98-100`），`expired_token` / `access_denied` 终止（`:101-106`） |
| ④ 核身 | `GET api.github.com/user` 取 login；`GET api.github.com/copilot_internal/user` 取 plan（`copilot_accounts.go:306-340`） | 401/403/404 即「无 Copilot 订阅」，拒绝入库 |
| ⑤ 落库 | magpie 存 `{oauth_token, host}` 于 `logins.json`（`copilot_accounts.go:264-271`） | |

**client id `Iv1.b507a08c87ecfe98` 是谁的**：magpie 注释称它是「Copilot 编辑器所用的 OAuth app」（`copilot_signin.go:19`、`copilot_accounts.go:5`）。含义：我们不注册自己的应用，直接**借用官方 Copilot 插件的身份**；授权页上用户看到的是官方 Copilot 而非本网关，GitHub 侧无法区分这个 token 是否来自编辑器。scope 仅 `read:user`。这是此通道最根本的合规瑕疵，也是它免注册的原因。我未能从 GitHub 官方文档找到该 id 的公开归属说明，「官方编辑器所用」仅出自 magpie 注释，非官方背书。

### 2. GitHub token → Copilot token（`mp:account.go`，`copilotToken`、`copilotSession`）

- `GET https://api.github.com/copilot_internal/v2/token`（`account.go:952`），头 `Authorization: token <gh_token>` 加整套伪装头（见下）。回 `{token, expires_at(unix秒), endpoints.api}`。
- 缓存按 GitHub token 为键，剩余寿命不足 2 分钟即重换（`copilotToken` 内 `time.Until(...) > 2*time.Minute`）。
- **有效期**：magpie 不写死，读 `expires_at`；其测试夹具用 1 小时（`copilot_accounts_test.go:48`），Auto 会话 token 夹具用 24 小时（`copilot_api_version_test.go:44`）。真实时长 magpie 源码里没有，**查不到**。
- **GitHub token 本身**：magpie 没有刷新逻辑，用到被撤销为止；失效表现为换 token 非 200，报「Copilot is signed out … sign in again」。「设备码 token 无 refresh」是我从代码「没有刷新」推断，GitHub 对该类 token 的过期策略未核官方文档。
- Copilot CLI 登录的 token 不能换（换取接口回 403），原样当 Bearer 发（`mp:copilot_cli.go:3-8、135-168`）；我们走设备码用编辑器 id，不涉及。

### 3. Entitlement（`mp:copilot_entitlement.go`、`copilot_accounts.go:293-302`）

`GET /copilot_internal/user` 回 `copilot_plan`、`access_type_sku`、`quota_snapshots`。plan 取值：`free / individual(Pro) / individual_pro(Pro+) / individual_max(Max) / individual_edu / business / enterprise`。`access_type_sku=free_educational_quota` / `free_limited_copilot` 区分教育版与 Free，**plan 相同不等于模型范围相同**（`copilot_entitlement.go:5-6`）。

### 4. GHE（`mp:copilot_ghe.go`）

仅支持 `github.com` 与 `<name>.ghe.com`（数据驻留版），同一 client id、同一设备码；URL 全部换成 `https://<name>.ghe.com/login/...`、`api.<name>.ghe.com/copilot_internal/...`、出站 `copilot-api.<name>.ghe.com`。主机白名单校验防 token 被发往任意主机（`:23-44`）。**自建 GHE Server 不支持**。出站地址以 token 响应的 `endpoints.api` 为准（`:119-125`），不写死。

## 二、出站

- 地址：`https://api.githubcopilot.com`（`account.go:954`），路径照协议原样：`/chat/completions`、`/responses`、`/v1/messages`（`account.go:1111` 三个协议同一 base）。
- 必带头（`account.go:956-973、1058-1082`）：

| 头 | magpie 取值 | 说明 |
| --- | --- | --- |
| `Authorization` | `Bearer <copilot_token>` | |
| `Editor-Version` | `vscode/1.140.0` | 伪装 VS Code |
| `Editor-Plugin-Version` | `copilot-chat/0.68.0` | 伪装 |
| `Copilot-Integration-Id` | `vscode-chat` | 伪装；CLI 版为 `copilot-developer-cli`（`copilot_cli.go:31`） |
| `User-Agent` | `GitHubCopilotChat/0.68.0` | 伪装 |
| `X-GitHub-Api-Version` | `2026-01-09` | 缺了 Auto 会话不被读取（`account.go:963-969`） |
| `Openai-Intent` | `conversation-panel` | |
| `X-Initiator` | 最后一条是用户消息→`user`，否则（工具回合）`agent` | **影响计费**，见第四节 |
| `Copilot-Vision-Request` | `true`（请求体含图片时） | |

  这些版本号会随编辑器迭代陈旧，需要维护（magpie 自己的值也在变，CLI 头 `copilot/1.0.88`）。
- 模型与协议：`GET {api}/models` 返回每个模型的 `supported_endpoints`、`vendor`、`model_picker_enabled`、`policy.state/terms`、`billing.is_premium`、`capabilities.supports.reasoning_effort`（`account.go` `copilotModels`）。magpie 注释概括：GPT 新模型**只在 Responses**，Claude 模型在 Chat + Anthropic Messages，无 `supported_endpoints` 的老模型（gpt-4.1、gpt-4o 等）只走 Chat（`account.go:105-110`、`copilotAPIs`）。打错端点回 400（见第三节）。
- 模型启用：部分模型 `policy.terms` 非空，需先 `POST {api}/models/{id}/policy` `{"state":"enabled"}` 接受条款才可用（`account.go` `copilotAccept`）。
- Auto（`mp:copilot_auto.go`）：Copilot 自选模型，Student 计划唯一可选；走 `/auto` 或 `/models/session`，版本头苛刻。**属可不做的部分。**
- 与本项目 codec 的兼容点：三种协议都与现有 codec 同形，渠道三个协议地址填同一 base 即可（口径层「每协议出站根地址」）。须关注：①按模型选协议，渠道级「支持协议集」不足以表达「GPT 仅 Responses、Claude 不走 Responses」，需模型纳管上记端点，或依赖互转；②Responses 的加密 reasoning 只认 Copilot 自己的（magpie `gateway/responses.go:601` 注释）。①是推论，待口径裁决。

## 三、拒绝形态（`mp:copilot_refused.go`）

| 形态 | 含义 | magpie 处理 |
| --- | --- | --- |
| 400 `model_not_supported` / "The requested model is not supported." | 账号不能调用该模型，`/models` 却列着（Student 计划 7 个 enabled 仅 gpt-4.1 可用，`:3-9`） | 该账号该模型 6 小时内移出（`:21`） |
| 400 `unsupported_api_for_model` / "not accessible via the /chat/completions endpoint" | 端点错，不是账号问题（`:28-31`） | 改走模型的另一端点 |
| 400 `no model endpoints available given user constraints` | Anthropic 端点不服务该模型，读起来像计划被拒（`account.go:1114-1130`） | 提示文案纠偏 |
| 400 "Requested model not available for session" | Auto 会话不含该模型（`:34-37`） | 重取会话 |
| 403 | 模型非账号可启用（`account.go` `copilotModels` default 分支注释） | 不列出 |
| 换 token 非 200 | GitHub token 失效 / 无订阅 | 提示重新登录 |

对本项目：400 类拒绝要区分「账号不可用」与「端点错」，前者进渠道状态 / 流水词，后者走协议回退。与口径层「401/403/429 只换不摘、任何状态码不改凭证状态」（`CONTEXT.md` 凭证池）不直接冲突，但 OAuth 失效是否例外见待裁决。

## 四、额度与 premium requests

- 查询：`GET api.github.com/copilot_internal/user`（GHE 换域名），头同上（`mp:subscription_usage.go:968-1060`）。`quota_snapshots` 有 `chat` / `completions` / `premium_interactions` 三项，字段 `unlimited`、`entitlement`、`quota_remaining`、`percent_remaining`、`overage_permitted`、`has_quota`、`quota_reset_at`；`entitlement` / `quota_remaining` 可能是数字**或字符串**（`:1063-1091`）。重置日取 `quota_reset_date_utc` / `quota_reset_date`。
- 陷阱（magpie 注释 #1063）：Business/Enterprise 的 premium 池耗尽时读作 `unlimited:true` + `has_quota:false`；token-based billing 下 `has_quota` 对所有项为 false，要看 entitlement；Business/Enterprise 超额由组织决定（`:1005-1030`）。fixture 已见 `token_based_billing:true`、`credits_used` 字段（`copilot_usage_test.go:59-72`），**计费口径正在变，不能假定「次数」语义长期稳定**。
- 计数规则（官方）：premium request 按**用户提示**计、乘模型倍率，agent 的自主工具调用不计；每月 1 日 00:00 UTC 重置。原句 "only the prompts you send count as premium requests"，<https://docs.github.com/en/copilot/concepts/billing/copilot-requests>（经抓取摘要转述，复核请回原页）。`X-Initiator` 正对应此：magpie 把「最后一条是 tool 结果」标 `agent`、其余标 `user`（`account.go:1074-1078`，注释「a turn the user typed is billed as one; a tool's reply is not」）。**含义：网关写的头决定计费，标错会吃额度，也近乎伪造。**
- magpie 的记账：不自记 premium 数，只展示上游快照；把 `gpt-4.1` 当作不占额度的 base model（`account.go` `copilotBaseModel`）。对本项目：订阅无单价，流水 cost 记什么属 #186「额度与用量」待问项；Copilot 的余量**可在 API 内读取**（对比 ChatGPT API 仅 chatgpt.com 可见），可在渠道页展示。

## 五、ToS

条款文本无「第三方客户端」「网关」「转发」专条。以下均为 GitHub 官方页（抓取日 2026-10-08，抓取经摘要模型转述，引文为其返回的原句，复核请回原页）：

| 条款 | 原文（≤15 词） | 与本通道关系 | URL |
| --- | --- | --- | --- |
| Terms of Service §B | "Your login may only be used by one person." | 把一个 Copilot 账号经网关给多人 / 多 key 使用，直接冲突 | <https://docs.github.com/en/site-policy/github-terms/github-terms-of-service> |
| 同上 §B | "You are responsible for all content posted and activity that occurs under your Account." | 他人经网关的用量由账号持有人担责 | 同上 |
| 同上 §H（API） | 摘要转述：禁止共享 API token 规避速率限制；"Abuse or excessively frequent requests … may result in" 暂停 API 访问（后句原文被截，未逐字核） | 与网关汇聚多人请求相关 | 同上 |
| 同上 §J | 仅称 Copilot 属 AI Features，管训练与输出归属，**未涉及客户端限制** | 条款里没有明文准许或禁止第三方客户端 | 同上 |
| Acceptable Use §4 | "using our servers for any form of excessive automated bulk activity" | 脚本化、批量请求的触发点 | <https://docs.github.com/en/site-policy/acceptable-use-policies/github-acceptable-use-policies> |
| Additional Products and Features | Copilot 章节只指向 §J / Generative AI Services Terms，无客户端条文 | 同上 | <https://docs.github.com/en/site-policy/github-terms/github-terms-for-additional-products-and-features> |

**缺口**：Copilot 专属的 Generative AI Services Terms、Copilot 产品条款、Microsoft Product Terms（Business/Enterprise 经微软购买时适用）本次**未抓**，其中可能有更直接的条文，不能据此断言「没有」。

**限制 / 封号先例（非权威，社区来源）**
- Wappler 社区帖（2025-03-07）转贴 GitHub Security 邮件，含 "further anomalous activity could result in a temporary suspension of your Copilot access."；帖中转述的触发原因含脚本化使用、过量使用、为绕过用量限制用多账号、经干扰 Copilot 工作方式的代理使用、凭证共享（原因清单来自搜索摘要，抓取页只确认了上句与处理经过）；后 Wappler 声明请求来源并遵守规则，GitHub 人员处理。<https://community.wappler.io/t/copilot-github-problems/61983>（第三方论坛，非 GitHub 官方）。
- `ericc-ch/copilot-api`（同类反向代理，OpenAI / Anthropic 兼容）自述："It is not supported by GitHub, and may break unexpectedly."，并引 GitHub 禁止 "excessive automated bulk activity"，提示可能触发滥用检测。<https://github.com/ericc-ch/copilot-api>（项目 README，非 GitHub 官方）。
- 搜索未找到「某用户因 copilot-api / magpie 类网关被永久封号」的可核实个案；一篇日文博客称超过某 token 量或 429 后反复重试会触发风控，未能核到出处，**不采信**。结论：已见先例是警告 / 暂时限制，永久封号个案本次无证据，但不等于无风险。
- magpie README 只写 "There is no key to copy"，对 Copilot 无 ToS 提示；地图 #186 记 magpie 对 Gemini CLI / Grok / Cursor / Kiro 等标 deprecated 防封号，Copilot 仍保留。

风险分层：①冒用编辑器身份与头，条款上无法自洽；②多人共用，违 §B 一人一登录；③单人自用于自己的多个工具，条款上仍灰，实际靠用量形态检测。PO 已裁「不做技术拦截、风险口径写进文档」（#186），此处只列事实。

## 六、服务器形态可行性

- 设备码流程**无浏览器回调、无 redirect_uri**：请求 ① 只带 client_id 与 scope，② 用户在任意设备输码，③ 服务端轮询。已确认（`copilot_signin.go` 全文无 redirect / listener）。对比 ChatGPT API 需 loopback 回调（`mp:chatgpt_api.go:53-56` 默认 `127.0.0.1:1455`），对容器 / 服务器部署是障碍。
- 管理端展示：点「登录 Copilot」→ 后端发 ① 并保存 `device_code`；前端展示 `user_code` + 链接 + 倒计时（`expires_in`）；后端 goroutine 按 `interval` 轮询，前端轮询登录状态（magpie 用 `signInFlow`：pending / done / failed）。关弹框应取消轮询（`s.stop=cancel`，`:74`）。
- 网络前提：服务端要能出站访问 `github.com`、`api.github.com`、`api.githubcopilot.com`（受限网络需代理）。
- 存储：只需存 GitHub OAuth token + host；Copilot session token 内存缓存即可，重启重换。GitHub token 是高价值秘密；凭证池现口径「值管理端可回读可复制」会让它可见，需 PO 裁。

## 七、与 ChatGPT API 通道的对照

ChatGPT API 一栏取自 magpie `chatgpt_api.go` 头部注释与常量及 #186 记载，**本次未深查，OpenAI 条款未核**，由 #187 负责，仅作对照。

| 维度 | GitHub Copilot（设备码） | ChatGPT API（Sign in with ChatGPT） |
| --- | --- | --- |
| 授权流 | GitHub 设备码，借官方编辑器 client id，scope `read:user`；GitHub token 无刷新逻辑 | PKCE 授权码 + 动态注册公共 client（`dynamic_agent_client`），有 refresh / sign-out（`chatgpt_api.go:46-70、394`） |
| 回调依赖 | **无**，用户输码即可 | 有：浏览器回到 loopback（`127.0.0.1:1455`），服务器形态需另解 |
| 协议 | Chat Completions + Responses + Anthropic Messages，按模型分端点 | Responses（`api.openai.com/v1/responses`） |
| 模型范围 | 多厂商（GPT、Claude 等），随计划与策略变，需 `/models` 实查与逐模型启用条款 | OpenAI 模型，随 Plus 等计划 |
| 额度可见性 | API 内可读（`quota_snapshots`），月重置；字段口径在漂移 | 余量只在 chatgpt.com/settings/usage 可见；5 小时窗与 chatgpt.com 共享；超限回 `subscription_sharing_usage_limit_exceeded`（`chatgpt_api.go:678`） |
| ToS 风险 | **高**：借编辑器身份 + 伪装头 + 未公开接口；§B 一人一登录；有限制账号先例 | 较低：OpenAI 为本地开源 app 开的正规口（#186 事实项，未核 OpenAI 条款） |
| 实现体量 | 小到中：设备码 + 换 token + 头维护；Auto / GHE / 拒绝重试可后置 | 中：动态注册 + PKCE + 回调 + 刷新 + 注销（magpie 该文件 751 行） |

## 八、待 PO 裁决

1. **是否做 Copilot 通道**。技术最省，ToS 风险最高。若做，是否接受「冒用官方编辑器 client id 与 `Editor-Version` 等头」这一性质，并在口径层与部署文档明写？
2. **与 ChatGPT API 的取舍 / 顺序**：二者都做、先做哪个、还是只做风险较低的一个？
3. **范围**：首版只做 github.com；GHE（`<name>.ghe.com`）、Auto、Student 受限账号处理是否一律列为非目标？
4. **凭证形态**：GitHub OAuth token 无刷新，作为凭证池第三类型「OAuth 账号」时，管理端是否允许回读（口径层 v0.47 默认可回读）？换 token 非 200（失效）后渠道状态怎么落——现口径「任何状态码都不改凭证状态」是否对 OAuth 失效例外？
5. **协议路由**：Copilot 的 GPT 仅 Responses、Claude 走 Chat / Anthropic，由模型纳管上记端点分流，还是首版只开放 Chat 可用模型？
6. **额度展示与流水 cost**：订阅无单价，流水记 0 还是按 premium 倍率？渠道页是否展示 `premium_interactions` 余量（口径随 token-based billing 漂移）？
7. **`X-Initiator` 语义**：网关是否沿用 magpie「按最后一条消息角色」规则代客户端写？标错改变计费，需明确由谁负责。
8. **头版本维护**：伪装头里的 `vscode/1.140.0` 等写死还是做成可配置项（会随编辑器迭代陈旧）？
9. **风险声明文案**：「发给别人使用」违反 §B「一个登录只能一人用」，部署文档是否把这一条单独点出来，而不只写「由 PO 自担」？
