// login.go 是登录弹层的服务端状态机（#212，口径层 §2.2 v1.52 / 展开层 §7.13）：
// start 生成 state / nonce / PKCE verifier 拼授权 URL，待完成登录态存**内存** map
// （渠道 id → 状态，TTL 10 分钟、一个渠道同时只一个）；complete 校粘贴回调的
// state / host / port / path → 换 token → 验签 → 拼凭证 JSON。落库不在这里：那是
// 管理端写路径的事（要跟 store.Validate 同一个事务），本包只交出「可信的凭证值」。
//
// 校验判据参照 magpie 的 pastedCallback / ownCallback（MIT，internal/provider/signin.go）：
// 只认这次登录自己的回调——host 限环回、端口 1455、路径 /auth/callback、state 对上；
// 动态注册签发的 client_id 跟着 code 一起从回调 query 里回来（#205 真机证据
// callback-query.redacted.json：code / scope / state / client_id 四键）。

package subscription

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SimonGino/portage/internal/store"
)

// loginTTL 是待完成登录态的寿命（口径钉 10 分钟）。做成 var 是给单测留的余地：
// TTL 过期那条用例等不了十分钟（magpie 的 signInTimeout 同款处理）。
var loginTTL = 10 * time.Minute

// siwcAuthorizePath 拼在 issuer 后面是 Sign in with ChatGPT 的授权页（magpie
// chatgpt_api.go 的 siwcAuthorizeURL 同址）。
const siwcAuthorizePath = "/api/accounts/authorize"

// siwcCallbackURL 是钉死的 redirect_uri：OpenAI 只把自家的 app 发回 127.0.0.1:1455，
// portage 不在那个端口上听——浏览器停在打不开的一页，人把整条地址复制回来。
const siwcCallbackURL = "http://127.0.0.1:1455/auth/callback"

// 授权 URL 的三样常量：动态注册用 client_id=dynamic_agent_client（公开客户端，
// 无 secret，OpenAI 在回调里发还真正签发的 oaiapp_…）；agent_name_hint 按 #203 裁决
// 写 portage；scope 集合与 magpie 同款——直连范围 chatgpt.tokens.use.direct 是硬要求。
const (
	siwcDynamicClient = "dynamic_agent_client"
	siwcAgentName     = "portage"
	siwcScopes        = "openid profile email offline_access resource.invoke " + store.SIWCDirectScope
)

// ErrLoginInProgress 是「这个渠道已有一个进行中的登录」：口径「一个渠道同时只一个」
// 的拒绝形态，管理端翻成 409。
var ErrLoginInProgress = errors.New("这个渠道已有一个进行中的登录（10 分钟内有效）：先完成那一次，或等它过期后重新开始")

// pendingLogin 是一次进行中的登录。state / nonce / verifier 是本次登录独享的
// 随机数——nonce 尤其要紧：complete 换 code 得来的 id_token 必须带它，否则验签
// 形同虚设（#211 local-reviewer 转告，magpie siwcCheckID 同判）。
type pendingLogin struct {
	state, nonce, verifier string
	// hostID 是本实例的 ext_agent_host_id（settings 表里那份），授权 URL 带它。
	hostID string
	// replaceID 是「重新登录」要替换的凭证行（0 = 新增，追加进池子）。成功后
	// 原行换值、名字不变——归因不断。
	replaceID int64
	expiresAt time.Time
}

// authorizeURL 拼这次登录的授权页地址（判据与字段见文件头）。
func (p *pendingLogin) authorizeURL(issuer string) string {
	q := url.Values{}
	q.Set("client_id", siwcDynamicClient)
	q.Set("agent_name_hint", siwcAgentName)
	q.Set("ext_agent_host_id", p.hostID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", siwcCallbackURL)
	q.Set("resource", siwcResource)
	q.Set("scope", siwcScopes)
	q.Set("state", p.state)
	q.Set("nonce", p.nonce)
	q.Set("code_challenge", pkceChallenge(p.verifier))
	q.Set("code_challenge_method", "S256")
	return issuer + siwcAuthorizePath + "?" + q.Encode()
}

// pkceChallenge 是 PKCE 的 S256 挑战值。
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// StartLogin 为一个渠道起一次登录，回授权页地址。
//
// 一个渠道同时只一个（口径层 v1.52）：上一个还活着就拒绝——重叠的两个 state 会
// 让「粘贴的地址是哪一次的」在页面上说不清。过期的待完成态就地清掉，不占坑。
func (e *Engine) StartLogin(ctx context.Context, channelID, replaceID int64) (string, error) {
	// host_id 的取造在 loginMu 之外做（#212 集成评审，CodeRabbit）：库里单连接
	// （SetMaxOpenConns(1)），settings 读写撞上开着的写事务时会让整个登录面
	// （start/complete/poll 都要这把锁）一起干等；「每实例一份」由 extAgentHostID
	// 自己的 hostIDMu 保证。
	hostID, err := e.extAgentHostID(ctx)
	if err != nil {
		return "", err
	}
	e.loginMu.Lock()
	defer e.loginMu.Unlock()
	e.expireLoginLocked(channelID)
	if _, ok := e.pending[channelID]; ok {
		return "", ErrLoginInProgress
	}
	p := &pendingLogin{
		state:     randomToken(24),
		nonce:     randomToken(24),
		verifier:  randomToken(48),
		hostID:    hostID,
		replaceID: replaceID,
		expiresAt: time.Now().Add(loginTTL),
	}
	e.pending[channelID] = p
	return p.authorizeURL(e.issuer), nil
}

// CompleteLogin 校验粘贴回调整条 URL，换 token、验签，回可落库的凭证 JSON 与
// 要替换的凭证行 id（0 = 追加）。
//
// 待完成态在**校验都过了、真要拿 code 去换 token 之前**取走（magpie 的 claim 同
// 判）：authorization code 是一次性的，从取走那刻起同一段回调重放只会撞 token
// 端点；而贴错了地址这种小错不该把人的一次登录烧掉——那些错误在取走之前就回，
// 待完成态还活着，改一下重新提交就行。
func (e *Engine) CompleteLogin(ctx context.Context, channelID int64, pasted string) (store.ChatGPTCredential, int64, error) {
	e.loginMu.Lock()
	e.expireLoginLocked(channelID)
	p, ok := e.pending[channelID]
	e.loginMu.Unlock()
	if !ok {
		return store.ChatGPTCredential{}, 0, errors.New("这个渠道没有进行中的登录：先点「开始登录」")
	}

	q, err := parseCallback(pasted)
	if err != nil {
		return store.ChatGPTCredential{}, 0, err
	}
	if q.Get("state") != p.state {
		return store.ChatGPTCredential{}, 0, errors.New("粘贴的地址不是这次登录的回调——state 对不上；回到授权页完成这次登录，或重新开始")
	}
	code := q.Get("code")
	if code == "" {
		return store.ChatGPTCredential{}, 0, errors.New("回调里没有 code：把浏览器停住的那个页面的整条地址原样贴进来")
	}
	clientID := strings.TrimSpace(q.Get("client_id"))
	if clientID == "" {
		return store.ChatGPTCredential{}, 0, errors.New("回调里没有 client_id——OpenAI 没完成动态注册；重新开始这次登录")
	}

	// 到这儿校验全过，这枚 code 要真的拿去换 token 了：取走待完成态，同一段回调
	// 不认第二遍（拿锁后复查指针：等锁的这会儿它可能已被另一路取走或顶掉）。
	e.loginMu.Lock()
	if cur, ok := e.pending[channelID]; !ok || cur != p {
		e.loginMu.Unlock()
		return store.ChatGPTCredential{}, 0, errors.New("这个渠道没有进行中的登录：先点「开始登录」")
	}
	delete(e.pending, channelID)
	e.loginMu.Unlock()

	// 换 token 不随请求取消（与刷新链 #217 同判，Codex 评审）：code 一次性、待
	// 完成态已消费——浏览器中途断开时若交换随 ctx 取消，上游可能已发了一把自己
	// 再拿不回的 refresh token，重贴回调也换不回来。HTTP 客户端的超时仍兜底；
	// 取消只影响后续校验链，失败路径自会 revokeIssued。
	tok, err := e.postTokenForm(context.WithoutCancel(ctx), url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"code_verifier": {p.verifier},
		"redirect_uri":  {siwcCallbackURL},
		"resource":      {siwcResource},
	})
	if err != nil {
		return store.ChatGPTCredential{}, 0, fmt.Errorf("换 token 失败: %w", err)
	}
	// 从 token 端点回包那刻起，上游已发了一把自己还没存的 refresh token：后面
	// 哪一步失败（缺直连 scope、没带回 ID token、验签、账号声明），这把 token
	// 都会悬在上游再也撤不掉——失败路径 best-effort 撤掉刚发出来的这份（#212
	// 审查，Codex）。撤销失败不吞原错误：原错误才是要给人看的。
	revokeIssued := func(err error) (store.ChatGPTCredential, int64, error) {
		_ = e.Revoke(context.WithoutCancel(ctx),
			store.ChatGPTCredential{ClientID: clientID, RefreshToken: tok.Refresh})
		return store.ChatGPTCredential{}, 0, err
	}
	c, err := applyToken(store.ChatGPTCredential{ClientID: clientID, HostID: p.hostID}, tok, time.Now())
	if err != nil {
		return revokeIssued(err)
	}
	if c.IDToken == "" {
		return revokeIssued(errors.New("认证服务没带回 ID token，验不了这次登录"))
	}
	// nonce 传的是这次登录生成的那个、恒非空——complete 的 id_token 是拿当次
	// authorization code 换来的，必须带当次 nonce，否则这条验签形同虚设
	// （refresh 回包的 id_token 才没有当次 nonce、传空跳过，与 magpie 同判）。
	claims, err := e.VerifyIDToken(ctx, c.IDToken, clientID, p.nonce)
	if err != nil {
		return revokeIssued(err)
	}
	c.Sub, c.Email = claimString(claims, "sub"), claimString(claims, "email")
	if c.Sub == "" && c.Email == "" {
		return revokeIssued(errors.New("ID token 里没有账号（sub / email 都空）——OpenAI 没说是谁登录了"))
	}
	return c, p.replaceID, nil
}

// LoginStatus 是 poll 的回包。ChatGPT 形态只有 waiting / none；Copilot 设备码
// （随其实现票）在同一形态上扩展——它是弹层第二形态的轮询口。
type LoginStatus struct {
	Status string `json:"status"` // waiting：有一个进行中的登录；none：没有
}

// PollLogin 报一个渠道有没有进行中的登录（过期视同没有）。
func (e *Engine) PollLogin(channelID int64) LoginStatus {
	e.loginMu.Lock()
	defer e.loginMu.Unlock()
	e.expireLoginLocked(channelID)
	if _, ok := e.pending[channelID]; ok {
		return LoginStatus{Status: "waiting"}
	}
	return LoginStatus{Status: "none"}
}

// expireLoginLocked 把这个渠道过期的待完成态清掉。调它的先拿 loginMu。
func (e *Engine) expireLoginLocked(channelID int64) {
	if p, ok := e.pending[channelID]; ok && time.Now().After(p.expiresAt) {
		delete(e.pending, channelID)
	}
}

// parseCallback 校验人贴进来的整条地址（判据见文件头，magpie ownCallback 同判），
// 过了回它的 query。
func parseCallback(pasted string) (url.Values, error) {
	u, err := url.Parse(strings.TrimSpace(pasted))
	if err != nil || u.Scheme != "http" || u.RawQuery == "" {
		return nil, errors.New("把浏览器停在的那个页面的整条地址贴进来（以 http:// 开头、带着 query）")
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
	default:
		return nil, errors.New("那不是这次登录的回调页——地址该停在 127.0.0.1:1455 上")
	}
	if u.Port() != "1455" || u.Path != "/auth/callback" {
		return nil, errors.New("那不是这次登录的回调页——地址该停在 127.0.0.1:1455/auth/callback 上")
	}
	return u.Query(), nil
}

// extAgentHostID 是本实例在 OpenAI 侧的名字（口径：每实例一个、落 settings 表）。
// urn:uuid: 前缀的 v4 UUID，与 magpie 的 siwcHostID 同构；取不到再生成，幂等。
func (e *Engine) extAgentHostID(ctx context.Context) (string, error) {
	// 取造串行（hostIDMu，#212 集成评审）：loginMu 不再罩着这里，两个渠道并发
	// 起登录也会同时进来，没这把锁会各自造一个 UUID、各写各的，「每实例一份」
	// 就破了。
	e.hostIDMu.Lock()
	defer e.hostIDMu.Unlock()
	id, err := store.GetSetting(ctx, e.db, store.SettingExtAgentHostID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(id) != "" {
		return id, nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80 // v4
	h := hex.EncodeToString(b)
	id = "urn:uuid:" + h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	if err := store.SetSetting(ctx, e.db, store.SettingExtAgentHostID, id); err != nil {
		return "", err
	}
	return id, nil
}

// Revoke 打 OIDC 发现文档点名的 revocation_endpoint，撤销一把凭证的 refresh
// token（口径层 v1.52：删凭证 best-effort 撤销，失败不阻止删除——错误交给调用方
// 记日志，删照删）。Copilot 无撤销，随其实现票。
func (e *Engine) Revoke(ctx context.Context, c store.ChatGPTCredential) error {
	var doc struct {
		Revoke string `json:"revocation_endpoint"`
	}
	if err := e.fetchJSON(ctx, e.issuer+"/.well-known/openid-configuration", &doc); err != nil {
		return fmt.Errorf("拉 OIDC 发现文档失败: %w", err)
	}
	if doc.Revoke == "" {
		return errors.New("发现文档没给 revocation_endpoint")
	}
	form := url.Values{
		"token":           {c.RefreshToken},
		"token_type_hint": {"refresh_token"},
		"client_id":       {c.ClientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, doc.Revoke, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := e.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20)) //nolint:errcheck // 撤销只看成败，回包是空或错误页
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("撤销端点回 %d", res.StatusCode)
	}
	return nil
}

// randomToken 是一段 n 字节熵的 base64url 随机串（state / nonce / verifier 共用）。
func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// claimString 从验过签的 claims 里取一个字符串。
func claimString(claims map[string]any, key string) string {
	s, _ := claims[key].(string)
	return s
}
