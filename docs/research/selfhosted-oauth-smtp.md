# Research：自托管单二进制下的 GitHub/Google OAuth 与 SMTP 发信（#69）

调研日期 2026-08-29。所有结论回溯一手来源；查不到的点明说查不到。

## 事实源

官方文档（抓取日期均为 2026-08-29）：

1. GitHub Docs — Authorizing OAuth apps：<https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps>
2. GitHub Docs — Creating an OAuth app：<https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app>
3. GitHub Docs — About the user authorization callback URL（GitHub Apps）：<https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/about-the-user-authorization-callback-url>
4. GitHub Docs — Scopes for OAuth apps：<https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps>
5. GitHub REST — Emails：<https://docs.github.com/en/rest/users/emails>；Users：<https://docs.github.com/en/rest/users/users>
6. GitHub Changelog 2025-07-14 — PKCE support for OAuth and GitHub App authentication：<https://github.blog/changelog/2025-07-14-pkce-support-for-oauth-and-github-app-authentication/>
7. Google Identity — OAuth 2.0 for Web Server Applications（含 redirect URI validation rules）：<https://developers.google.com/identity/protocols/oauth2/web-server>
8. Google Identity — OpenID Connect：<https://developers.google.com/identity/openid-connect/openid-connect>
9. Google Identity — Using OAuth 2.0（token 过期/Testing 状态）：<https://developers.google.com/identity/protocols/oauth2>；GCP Console Help — Manage App Audience：<https://support.google.com/cloud/answer/15549945>
10. Google Identity — OAuth 2.0 for Mobile & Desktop Apps（PKCE、loopback）：<https://developers.google.com/identity/protocols/oauth2/native-app>；Limited-Input Device（device flow）：<https://developers.google.com/identity/protocols/oauth2/limited-input-device>
11. golang.org/x/oauth2 源码仓库 go.googlesource.com/oauth2（GitHub 镜像 golang/oauth2）：最新 tag v0.36.0（2026-02-11，模块代理元数据）；`pkce.go`（v0.13.0 起存在，v0.12.0 无——按 tag 逐一核对）；`go.mod`
12. Go 1.26.4 标准库源码（本机 GOROOT）`src/net/smtp/auth.go`、`src/net/smtp/smtp.go`；`go doc net/smtp` frozen 声明
13. wneessen/go-mail：最新 release v0.8.1（2026-07-09），MIT，仓库源码 `go.mod` / `auth.go` / `tls.go` / `client.go`（GitHub API + raw 源码）
14. markbates/goth：最新 release v1.82.0（2025-08-18），MIT，`go.mod`
15. jordan-wright/email（最后 release v4.0.0，2020-06-04）、go-gomail/gomail（最后 commit 2016-04-11）——GitHub API
16. Resend Docs — Send with SMTP：<https://resend.com/docs/send-with-smtp>

---

## 一、OAuth 配置要求与坑

### 核心结论：「每个自托管实例各自注册 OAuth 应用」属实，无官方旁路

- GitHub 与 Google 都要求 redirect/callback URL **预先注册在应用配置里**，且按下述规则匹配（来源 1、7）。回调域名部署前未知 → 单一共享 OAuth 应用无法覆盖任意实例域名。官方没有「任意 redirect」旁路；loopback 特例只救 `127.0.0.1`/localhost 场景，救不了公网域名可变。
- 旁路评估：GitHub device flow 可行但 UX 是「开 github.com/login/device 输码」；Google device flow 官方定位是 TV/受限输入设备（"devices such as TVs, game consoles, and printers"，来源 10），虽然 scope 白名单恰好含 `openid email profile`，但不为 web 登录设计。两者都不适合做管理端主登录路径。
- 落地即：Portage 管理端做「自带 client_id/client_secret」配置（类似 Grafana/Gitea/Miniflux 的做法），文档指引用户到 GitHub/Google 各注册一个应用、回调填 `https://<自己的域名>/admin/oauth/callback/<provider>`。

### 1. GitHub OAuth App

| 项 | 结论 | 来源 |
| --- | --- | --- |
| callback URL 数量 | **最多 10 个**："You can enter up to 10 callback URLs. To add additional callback URLs, click Add callback URL."（旧认知「只能一个」已过时） | 2 |
| redirect_uri 省略时 | 跳到「第一个」已配置的 callback URL："If left out, GitHub will redirect users to the first callback URL configured in the OAuth app settings." | 1 |
| 匹配规则（启用通配符匹配时） | host+port 精确匹配、path 是注册 path 的**子目录**；禁用通配符匹配时整串精确匹配。注意仅存量应用启用通配符："all OAuth apps and some GitHub Apps created before that date have wildcard matching enabled"——**新建应用按精确匹配对待** | 1、3 |
| localhost 特例 | localhost callback 的**端口可变**：redirect_uri 的端口不必与注册端口一致；loopback `http://127.0.0.1:port/path` 受支持 | 1 |
| GitHub App 差别 | GitHub App 同样 "You can specify up to 10 callback URLs"——数量上与 OAuth App 已无差别 | 3 |
| PKCE | **2025-07-14 起正式支持**（OAuth App 与 GitHub App），仅 S256（"the plain method is unsupported"）。但 GitHub "does not distinguish between public and confidential clients"，PKCE 不免除 client_secret——token 交换仍必带 `client_secret` | 1、6 |
| authorize 端点 | `GET https://github.com/login/oauth/authorize`，必带 `client_id`，建议 `redirect_uri`、`state`、`code_challenge(+_method=S256)` | 1 |
| token 端点 | `POST https://github.com/login/oauth/access_token`，必带 `client_id`、`client_secret`、`code`，PKCE 时加 `code_verifier`。授权码 10 分钟过期（"The temporary code will expire after 10 minutes."） | 1 |
| token 响应格式坑 | **默认 form-encoded 字符串**，要 JSON 必须带 `Accept: application/json`；响应字段 `access_token`/`scope`/`token_type`；OAuth App 的 access token **默认不过期** | 1 |
| scope（只要身份） | 空 scope 即 "read-only access to public information (including user profile info…)"，够拿 `GET /user` 公开资料；`read:user` 读私有 profile；**email 要 `user:email`** | 4 |
| 用户信息 API | `GET /user` 拿 id/login 等；其 `email` 字段在用户未设公开邮箱时为 null → 需 `user:email` scope 调 `GET /user/emails`（字段 `email`/`primary`/`verified`/`visibility`），取 primary+verified 那条 | 5 |
| device flow | OAuth App 支持，需在应用设置勾选 "Enable Device Flow"；端点 `POST https://github.com/login/device/code`，grant_type `urn:ietf:params:oauth:grant-type:device_code`，**不需要 client_secret** | 1、2 |

### 2. Google OAuth 2.0（Web application client）

| 项 | 结论 | 来源 |
| --- | --- | --- |
| redirect URI 匹配 | 在 Console 精确注册、按 RFC 3986 校验精确匹配；不能含 fragment | 7 |
| scheme/host 限制 | **必须 HTTPS**（"Redirect URIs must use the HTTPS scheme, not plain HTTP. Localhost URIs (including localhost IP address URIs) are exempt"）；**禁止裸 IP**（"Hosts cannot be raw IP addresses."，localhost IP 除外）；**域名 TLD 必须在 public suffix list**（`.local`/`.lan` 等内网域名注册不了） | 7 |
| 端点 | authorize `https://accounts.google.com/o/oauth2/v2/auth`；token `https://oauth2.googleapis.com/token`；userinfo `https://openidconnect.googleapis.com/v1/userinfo`；discovery `https://accounts.google.com/.well-known/openid-configuration` | 7、8 |
| 拿身份走 OIDC | scope `openid email profile`：`openid`→`sub`，`email`→`email`+`email_verified`，`profile`→`name`/`picture` 等。id_token 若是**直接经 HTTPS 从 Google token 端点取回**可免本地验签（"you are communicating directly over an intermediary-free HTTPS channel and using your client secret to authenticate yourself to Google"——但转交给其他组件就必须验）。`state` 防 CSRF、`nonce` 防重放 | 8 |
| PKCE | web-server 流程官方文档**未记载** PKCE 参数；PKCE 记载于 installed/native app 流程（"Google supports the Proof Key for Code Exchange (PKCE) protocol to make the installed app flow more secure"）。Web application client 上 client_secret 必需，PKCE 至多是锦上添花，官方未承诺 web client 校验行为——**不要依赖** | 7、10 |
| consent screen / 验证 | Testing 状态：**最多 100 个 test user**、授权与 refresh token **7 天过期**；In production：refresh token 不再 7 天过期（长期不用约 6 个月失效）。非敏感 scope（`openid email profile` 属之）转 production 不强制走验证审核，但应用名/logo 展示需验证，未验证会出「unverified app」告警画面。个人自托管：**只要登录时拿 id_token、不存 refresh token，7 天过期影响为零**；但 Testing 状态要把自己邮箱加进 test users | 9 |
| device flow | 仅面向受限输入设备，scope 白名单限 `openid email profile` + Drive/YouTube 少数几个——技术上能拿身份，产品定位不适合 web 管理端登录 | 10 |

### 3. Go 生态选型（OAuth）

结论先行：**golang.org/x/oauth2 足够，不引 goth**。

| 库 | 事实 | 来源 |
| --- | --- | --- |
| golang.org/x/oauth2 | Go 官方 x 仓库，持续发版，最新 v0.36.0（2026-02-11）；`go.mod` 直接依赖仅 `cloud.google.com/go/compute/metadata`（只被 `google` 子包的 ADC 逻辑用到，用 `endpoints`/顶层包不拖它进二进制的实际调用路径）。内置端点：`golang.org/x/oauth2/github`、`golang.org/x/oauth2/google` 及汇总包 `golang.org/x/oauth2/endpoints`。PKCE API：`oauth2.GenerateVerifier` / `S256ChallengeOption` / `VerifierOption`，**v0.13.0（2023-10）加入**（v0.12.0 无 `pkce.go`）；device flow 有 `Config.DeviceAuth`/`DeviceAccessToken` | 11 |
| 自己要写的 | x/oauth2 只管 AuthCodeURL/Exchange/token 刷新：`state`（+Google `nonce`）的生成、落 cookie/session、回调校验，回调路由，以及拿用户信息（GitHub `GET /user`+`/user/emails`、Google userinfo 或解析 id_token）都要自己写——两家合计约一两百行 | 11、5、8 |
| markbates/goth | 活跃（v1.82.0，2025-08），MIT，80+ providerï¼但 `go.mod` 直接依赖 11 个：gorilla/pat+sessions+mux、lestrrat-go/jwx（JWT 栈）、golang-jwt/jwt 等。只接 GitHub+Google 两家、且 Portage 已有自己的 session 体系，为省一两百行引入整套 gorilla+jwx 依赖不划算 | 14 |

坑：GitHub token 响应默认 form-encoded——x/oauth2 的 `Exchange` 已内部处理（其 internal token 解析同时支持 JSON 与 form-encoded 响应），自己裸写 HTTP 才会踩。

---

## 二、SMTP 发信选型

结论先行：**选 wneessen/go-mail**，net/smtp 只在「坚决零依赖」时才值得，代价是自己补 AUTH LOGIN 和 465 implicit TLS。

### 1. 标准库 net/smtp 的边界（Go 1.26.4 源码求证）

- frozen 声明（`go doc net/smtp` 原文）："The smtp package is frozen and is not accepting new features. Some external packages provide more functionality."（来源 12）
- 有：`PlainAuth`、`CRAMMD5Auth`、`Client.StartTLS`、`SendMail`（自动机会式 STARTTLS：`if ok, _ := c.Extension("STARTTLS"); ok { c.StartTLS(...) }`）。
- 没有：
  - **AUTH LOGIN**（`auth.go` 全文只有 plain 与 cramMD5 两个实现）——要支持得自己实现 `smtp.Auth` 接口：`Start(server *ServerInfo) (proto string, toServer []byte, err error)` + `Next(fromServer []byte, more bool) ([]byte, error)`（来源 12）。
  - **implicit TLS（465）**：`SendMail`/`Dial` 走明文 TCP，465 需要自己 `tls.Dial` 后 `smtp.NewClient`。
- 安全限制（`auth.go` 源码注释原文）："PlainAuth will only send the credentials if the connection is using TLS or is connected to localhost. Otherwise authentication will fail with an error, without sending the credentials."——`isLocalhost` 只认 `localhost`/`127.0.0.1`/`::1`。**内网明文 relay + PLAIN 直接失败**，这是刻意设计（防中间人骗密码），绕不过，只能换实现（来源 12）。

### 2. wneessen/go-mail（建议选它）

- 维护：v0.8.1（2026-07-09），仓库活跃（2026-08 仍有 push），MIT，1.4k+ stars；`go.mod` 要求 go 1.25.0ï¼依赖仅 `golang.org/x/crypto` + `golang.org/x/text` 两个（早期宣传零依赖，v0.8 起为 SCRAM/DKIM 引入这两个，均为 x 官方库ï¼不算重）（来源 13）。
- 三态加密（`tls.go` 源码确认）：`TLSMandatory`（STARTTLS 必须，失败即断）/ `TLSOpportunistic`（STARTTLS 尽力，回落明文）/ `NoTLS`（强制明文）；`WithTLSPortPolicy(policy)` 按策略自动选端口（Mandatory→587、NoTLS→25），`WithSSL()`/`WithSSLPort(fallback bool)` 走 **465 implicit TLS**（`DefaultPort=25`、`DefaultPortTLS=587`、`DefaultPortSSL=465` 常量在 `client.go`）（来源 13）。
- AUTH 机制（`auth.go` 源码确认的常量名）：`SMTPAuthPlain`、`SMTPAuthLogin`、`SMTPAuthCramMD5`、`SMTPAuthXOAUTH2`、`SMTPAuthSCRAMSHA1(-PLUS)`、`SMTPAuthSCRAMSHA256(-PLUS)`、`SMTPAuthNoAuth`、`SMTPAuthCustom`，以及 **`SMTPAuthAutoDiscover`**（自动探测服务器通告的最强机制）；另有刻意标注不安全的 `SMTPAuthPlainNoEnc`/`SMTPAuthLoginNoEnc`（明文连接也肯发凭证，覆盖内网 relay 场景——net/smtp 做不到的正是这个）（来源 13）。
- 配置面完整：`WithUsername/WithPassword/WithSMTPAuth/WithTLSConfig/WithTimeout/WithDebugLog` 等（`client.go`）。

### 3. 其他候选（简查即弃）

| 库 | 状态 | 来源 |
| --- | --- | --- |
| jordan-wright/email | 未 archive 但事实停更：最后 release v4.0.0（2020-06），最后 push 2024-02 | 15 |
| go-gomail/gomail | 死了：最后 commit 2016-04-11 | 15 |

### 4. Resend SMTP 端点参数（官方文档原文）

| 项 | 值 | 备注 |
| --- | --- | --- |
| Host | `smtp.resend.com` | 来源 16 |
| STARTTLS 端口 | **25、587、2587** | 来源 16 |
| implicit SSL/TLS 端口 | **465、2465** | 来源 16 |
| Username | 固定字符串 `resend` | 来源 16 |
| Password | Resend API key | 来源 16 |
| From 要求 | 前置条件 "A verified domain"——发信域名必须已在 Resend 验证 | 来源 16 |
| 限速 | 与 API 相同的 rate limit；支持 `Resend-Idempotency-Key` 头防重 | 来源 16 |

用 go-mail 对接 Resend：587 + `TLSMandatory` + `SMTPAuthPlain`（或 AutoDiscover）即可,465 则 `WithSSLPort`。

---

## 三、对 Portage 的建议选型（供设计引用）

1. **OAuth 库**：`golang.org/x/oauth2`（顶层包 + `endpoints` 包），不引 goth。授权码流 + `state` 随机串落一次性 cookie；GitHub/Google 都带上 PKCE（`GenerateVerifier`+`S256ChallengeOption`，需要 x/oauth2 ≥ v0.13.0）——GitHub 官方已支持且推荐,Google web client 上无害。client_secret 两家都必须配，属服务端机密，遵守「上游 key 只存服务端」同款纪律。
2. **回调 URL 方案**：不做也做不了「共享应用」。管理端做 provider 配置页（client_id + client_secret + 可选自定义端点），回调路径固定为 `/<前缀>/oauth/callback/<provider>`ï¼页面上把「当前实例的完整回调 URL」直接展示出来让用户复制去 GitHub/Google 注册。文档写清 Google 三条硬限制：HTTPS 必须（localhost 除外）、禁裸 IP、域名 TLD 须在 public suffix list——纯 IP/内网域名部署只能用 GitHub 或免 OAuth。
3. **身份字段**：GitHub scope 用 `read:user user:email`ï¼身份键用 `GET /user` 的不可变数字 `id`（login 可改名），邮箱从 `/user/emails` 取 primary+verified；Google scope 用 `openid email profile`，身份键用 id_token 的 `sub`（email 可换绑，不做主键），直连 token 端点取回的 id_token 可免验签。不存 refresh tokenï¼登录即用即弃——顺带让 Google Testing 状态的 7 天过期无感。
4. **SMTP 库**：`wneessen/go-mail`。三态配置映射:`starttls`→`WithTLSPortPolicy(TLSMandatory)`（默认 587）、`ssl`→`WithSSLPort`（465）、`none`→`WithTLSPolicy(NoTLS)`（25/自定义端口ï¼配合 `SMTPAuthPlainNoEnc`/`LoginNoEnc` 才能在明文内网 relay 上带凭证）；AUTH 默认 `SMTPAuthAutoDiscover`ï¼允许显式指定 LOGIN/PLAIN/CRAM-MD5。net/smtp 不用——frozen、无 AUTH LOGIN、无 465、PlainAuth 拒绝非 TLS 非 localhost。
5. **Resend 预设**：`smtp.resend.com` + 587 STARTTLS + 用户名固定 `resend` + 密码填 API keyï¼提示发信域名须已在 Resend 验证。
