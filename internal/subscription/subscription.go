// Package subscription 是订阅渠道（口径层 §2.2「订阅渠道」条 v1.52，chatgpt_account
// 先落地）的凭证引擎：请求前懒刷新、每把凭证一把互斥锁、refresh 死亡码标停用，与
// ChatGPT 侧 ID token 的 JWKS 验签（magpie 不验，本项目要验——不照抄）。
//
// 字段表与 JSON 形状住在 store（subcred.go）——凭证值是 channel_keys.credential
// 那一列的存储格式；本包只管「什么时候换它」与「换来的 token 可不可信」。
package subscription

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/store"
)

// DefaultIssuer 是 Sign in with ChatGPT 的 OIDC 签发方；发现文档、token 端点都
// 由它拼出（登录弹层与测试把它换成别的——#212 的 httptest 假上游也走同一个口）。
const DefaultIssuer = "https://auth.openai.com"

// siwcResource 是 token 的用途声明（refresh 表单照带，登录票同样）；api.openai.com
// 的官方直连入口（#205 证据即在此址采集）。
const siwcResource = "https://api.openai.com/v1"

// refreshMargin 是「剩多久才刷」的门槛：口径钉「过期前约 3 分钟」（§2.2 v1.52），
// 与 magpie 同值。不做定时保活——刷不刷只由「有请求要用它」决定。
const refreshMargin = 3 * time.Minute

// tokenTimeout 是 token 端点整次调用的上限：这是有界的小请求，不是转发流，
// 挂住就该掐掉换下一把凭证，别把整个请求拖到客户端超时。
const tokenTimeout = 30 * time.Second

// deathCodes 是 refresh 的七词死亡表（§7.13）：撞上即标停用、reason 落
// reauth_required——v0.95「任何状态码都不改凭证状态」对凭证生命周期事件的唯一
// 例外（那条管的是上游请求状态码）。其余错误一律不动凭证。
var deathCodes = []string{
	"invalid_grant", "invalid_refresh_token", "token_expired",
	"refresh_token_expired", "refresh_token_invalidated",
	"refresh_token_reused", "invalid_client",
}

// ErrReauthRequired 是「这把凭证需要重新登录」：死亡码标停用后回它，relay 据此
// 落流水词 reauth_required（第 13 词）。拿锁后复查发现凭证已带着同一原因停用时
// 也回它——那就是上一次死亡留下的现场。与解析层 store.ErrReauthRequired（Resolve
// 倒下）是同一个概念、同一个值：一处定义、两层共用，别再立第二份哨兵。
var ErrReauthRequired = store.ErrReauthRequired

// Engine 是订阅凭证引擎。零散的运行期状态都在内存里：每把凭证一把锁（refresh
// token 会轮换，两把并发刷同一凭证，后一把必撞 refresh_token_reused）、JWKS
// 缓存一天。一个进程一份，由 server 在启动时建。
type Engine struct {
	db     *sql.DB
	issuer string
	http   *http.Client

	mu    sync.Mutex
	locks map[int64]*sync.Mutex

	// loginMu 管待完成登录态（#212）：渠道 id → 状态，TTL 10 分钟、一个渠道
	// 同时只一个。跟刷新锁分开一把：登录是管理面的稀有动作，不该跟刷新抢锁。
	loginMu sync.Mutex
	pending map[int64]*pendingLogin

	// hostIDMu 只串行 ext_agent_host_id 的取造（#212 集成评审）：loginMu 不再
	// 罩着它之后，两个渠道并发起登录也会同时进来，没这把锁会各自造 UUID。
	hostIDMu sync.Mutex

	jwksMu       sync.Mutex
	jwksKeys     []jwk
	jwksAt       time.Time
	jwksForcedAt time.Time
}

// NewEngine 建引擎。issuer 空串用 DefaultIssuer；db 为 nil 时只可用验签那半边
// （登录弹层的单测这么用）。
func NewEngine(db *sql.DB, issuer string) *Engine {
	if issuer == "" {
		issuer = DefaultIssuer
	}
	return &Engine{
		db:     db,
		issuer: strings.TrimRight(issuer, "/"),
		// token 端点是定址小请求，没有合法的重定向理由：307/308 会把带 refresh_token
		// 的 POST 原样转投 Location 目标（#217 评审），故绯掉自动跟随，3xx 按
		// 非死亡错误收场——不动凭证，access 未过期照用。发现文档与 JWKS 的 GET
		// 同走这个 client，同样不跟。
		http: &http.Client{
			Timeout: tokenTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		locks:   map[int64]*sync.Mutex{},
		pending: map[int64]*pendingLogin{},
	}
}

// tokenURL 由 issuer 拼：OpenAI 的 token 端点挂在 /api/accounts/oauth/token
// （magpie chatgpt_api.go 的 siwcTokenURL 同址，#205 的 token 往返也在此址采）。
func (e *Engine) tokenURL() string { return e.issuer + "/api/accounts/oauth/token" }

// lockFor 取这把凭证的互斥锁——按凭证 id 一把，不按渠道：多把凭证各自刷新互不
// 等待，同把凭证串行（§7.13「每把凭证一把 sync.Mutex」）。
func (e *Engine) lockFor(id int64) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	if l, ok := e.locks[id]; ok {
		return l
	}
	l := &sync.Mutex{}
	e.locks[id] = l
	return l
}

// WithCredentialLock 让管理端把「读值 → 删除 → 撤销」整段放在这把凭证的锁里跑
// （#212 集成评审，Codex）：删除不拿这把锁时，在途刷新可能刚好从 token 端点换回
// 新的 refresh token——行已删、CAS 落 0 行轮换静默作废，新换的 grant 就悬在上游
// 再也撤不掉。刷新要么先做完（删除读到轮换后的新值、撤的就是它），要么根本
// 没起（复查撞行已不在直接回）。
func (e *Engine) WithCredentialLock(id int64, fn func()) {
	l := e.lockFor(id)
	l.Lock()
	defer l.Unlock()
	fn()
}

func expiry(c store.ChatGPTCredential) time.Time { return time.Unix(c.ExpiresAt, 0) }

func fresh(c store.ChatGPTCredential) bool { return time.Until(expiry(c)) > refreshMargin }

// Effective 走一遍「请求前懒刷新」，回这把凭证本次该用的出站 access token。
//
// 剩约 3 分钟内才动 token 端点；动之前先拿这把凭证的锁、拿锁后**从库里**复查
// （复查不是再看一眼入参：refresh token 每刷一次轮换一次，另一条请求可能刚把
// 库里那份换掉，拿旧值再判过期就会把轮换打穿成 refresh_token_reused）。死亡码
// 标停用并回 ErrReauthRequired；其余错误不动凭证，access 未过期照用（§2.2 v1.52）。
func (e *Engine) Effective(ctx context.Context, cred store.Credential) (string, error) {
	c, err := store.ParseChatGPTCredential(cred.Value)
	if err != nil {
		return "", fmt.Errorf("凭证 %q 的 JSON 不合法: %w", cred.Name, err)
	}
	if fresh(c) {
		return c.AccessToken, nil
	}
	lock := e.lockFor(cred.ID)
	lock.Lock()
	defer lock.Unlock()
	cur, err := store.CredentialByID(ctx, e.db, cred.ID)
	if err != nil {
		return "", err
	}
	if cur.Disabled {
		if cur.DisabledReason == calllog.ReauthRequired.String() {
			return "", ErrReauthRequired
		}
		return "", fmt.Errorf("凭证 %q 已停用", cred.Name)
	}
	if c, err = store.ParseChatGPTCredential(cur.Credential); err != nil {
		return "", fmt.Errorf("凭证 %q 的 JSON 不合法: %w", cred.Name, err)
	}
	if fresh(c) {
		return c.AccessToken, nil
	}
	// 从这里起是凭证生命周期事件，不随请求走：token 端点一旦受理轮换，旧
	// refresh_token 即作废——客户端中途断开也要把这轮刷完、落库，否则丢掉的
	// 新值会让下一轮必撞 refresh_token_reused 死亡码（#217 评审，Codex）。时长
	// 上界仍由 http.Client 的 Timeout 兜着，不会失控。
	bg := context.WithoutCancel(ctx)
	tok, err := e.postToken(bg, c)
	var te *tokenError
	switch {
	case err == nil:
	case errors.As(err, &te) && slices.Contains(deathCodes, te.Code):
		if err := store.MarkCredentialReauth(bg, e.db, cred.ID, cur.Credential); err != nil {
			return "", err
		}
		return "", ErrReauthRequired
	default:
		// 网络 / 限流 / 认证服务打摆：不动凭证。手里的 access 还没到期就照用——
		// 「其余错误不动凭证，access 未过期照用」的完整一半是「过期了才认输」。
		if time.Until(expiry(c)) > 0 {
			return c.AccessToken, nil
		}
		return "", fmt.Errorf("刷新凭证 %q 失败: %w", cred.Name, err)
	}
	c, err = applyToken(c, tok, time.Now())
	if err != nil {
		// 200 但回包畸形（缺 token / 缺有效期 / 没授直连 scope）与其余错误同判
		// （§2.2 v1.52）：不动凭证，手里的 access 还没到期就照用，过期了才认输——
		// 与上面网络错误那一档的收场对齐。
		if time.Until(expiry(c)) > 0 {
			return c.AccessToken, nil
		}
		return "", fmt.Errorf("刷新凭证 %q 失败: %w", cred.Name, err)
	}
	merged, err := rotatedJSON(cur.Credential, c)
	if err != nil {
		return "", err
	}
	// 落库走 CAS：库里的值被管理端在飞行中粘贴换掉时（#212 之前的迁移正路），
	// 轮换作废、人粘的那份作数；刚换出的 access token 本次照发，下次请求自会读新值。
	if _, err := store.RotateCredentialValue(bg, e.db, cred.ID, cur.Credential, merged); err != nil {
		return "", err
	}
	return c.AccessToken, nil
}

// rotatedJSON 把轮换结果叠回库里的那份原文：结构体只改它认识的那几个键（token
// 三样 + expires_at + scopes；id_token 回包没带就不动），§7.13 字段表之外的键
// （#205 现场就带过 earliest_refresh_at 这种）原样保留——「原文落库、原样回读」
// （v0.47）不因刷了一次而缩水。
func rotatedJSON(raw string, c store.ChatGPTCredential) (string, error) {
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return "", err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	overlay := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &overlay); err != nil {
		return "", err
	}
	for k, v := range overlay {
		m[k] = v
	}
	out, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// EffectiveAll 给 relay 用：凭证池逐把过懒刷新，出站值全换成 access token。
//
// 死亡的 / 刷不动又过期的 / 坏 JSON 的凭证摘掉不出声——池里还有兄弟就照发（口径
// 「多账号：凭证池 1..N 语义照旧」）。全部不可用才回错误：有死亡码在场回
// ErrReauthRequired（relay 落 reauth_required），否则回最后一个别的错误（relay
// 落默认词 rejected）。
func (e *Engine) EffectiveAll(ctx context.Context, creds []store.Credential) ([]store.Credential, error) {
	var out []store.Credential
	var reauth bool
	var lastErr error
	for _, c := range creds {
		access, err := e.Effective(ctx, c)
		switch {
		case err == nil:
			c.Value = access
			out = append(out, c)
		case errors.Is(err, ErrReauthRequired):
			reauth = true
		default:
			lastErr = err
		}
	}
	if len(out) > 0 {
		return out, nil
	}
	if reauth {
		return nil, ErrReauthRequired
	}
	if lastErr == nil {
		lastErr = errors.New("候选没有可用凭证")
	}
	return nil, lastErr
}

// tokenReply 是 token 端点的回包（形状钉自 #205 真机证据：access_token /
// refresh_token / id_token / expires_in / scope）。
type tokenReply struct {
	Access    string  `json:"access_token"`
	Refresh   string  `json:"refresh_token"`
	IDToken   string  `json:"id_token"`
	ExpiresIn float64 `json:"expires_in"`
	Scope     string  `json:"scope"`
}

// tokenError 是 token 端点的拒绝，Code 是 OAuth 错误码（死亡码判它）。OpenAI 的
// error 字段有字符串与 {code,message} 对象两种形态（#205 现场两种都出现过），
// 都收。
type tokenError struct {
	Status int
	Code   string
	Msg    string
}

func (e *tokenError) Error() string {
	msg := strings.TrimSpace(e.Code + " " + e.Msg)
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return "认证服务拒绝（" + strconv.Itoa(e.Status) + "）：" + msg
}

// postToken 打一次 token 端点（refresh_token grant），带回包或拒绝。
func (e *Engine) postToken(ctx context.Context, c store.ChatGPTCredential) (tokenReply, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {c.ClientID},
		"refresh_token": {c.RefreshToken},
		"resource":      {siwcResource},
	}
	return e.postTokenForm(ctx, form)
}

// postTokenForm 是 token 端点的公共半边：发表单、判状态码、解回包（refresh 与
// 登录的 authorization_code 两个 grant 共用它，拒绝形态同一套：#205 现场error 字段
// 字符串与 {code,message} 对象两种都出现过）。
func (e *Engine) postTokenForm(ctx context.Context, form url.Values) (tokenReply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return tokenReply{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := e.http.Do(req)
	if err != nil {
		return tokenReply{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return tokenReply{}, parseTokenError(b, res.StatusCode)
	}
	var tok tokenReply
	if err := json.Unmarshal(b, &tok); err != nil {
		return tokenReply{}, fmt.Errorf("认证服务的回包读不动: %w", err)
	}
	return tok, nil
}

// parseTokenError 从拒绝体里抠 OAuth 错误码与描述（形态见 tokenError）。
func parseTokenError(b []byte, status int) error {
	var body struct {
		Error   json.RawMessage `json:"error"`
		Desc    string          `json:"error_description"`
		Code    string          `json:"code"`
		Message string          `json:"message"`
	}
	_ = json.Unmarshal(b, &body)
	out := &tokenError{Status: status, Code: body.Code, Msg: body.Desc}
	switch v := string(body.Error); {
	case strings.HasPrefix(v, `"`): // "error":"invalid_grant"
		var s string
		_ = json.Unmarshal(body.Error, &s)
		out.Code = firstNonEmpty(out.Code, s)
	case strings.HasPrefix(v, "{"): // "error":{"code":"…","message":"…"}
		var obj struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body.Error, &obj)
		out.Code = firstNonEmpty(out.Code, obj.Code)
		out.Msg = firstNonEmpty(out.Msg, obj.Message)
	}
	return out
}

// applyToken 把回包并进凭证：token 换新、refresh token 轮换、scope 以服务端
// 实授为准（直连范围是硬要求，没有它账号的订阅额度用不到 API 上）、expires_at
// 按 expires_in 推；id_token 有则随包更新。失败全部是「不落库不动凭证」的错误。
func applyToken(c store.ChatGPTCredential, tok tokenReply, now time.Time) (store.ChatGPTCredential, error) {
	if tok.Access == "" || tok.Refresh == "" {
		return c, errors.New("认证服务没带回 token")
	}
	if tok.ExpiresIn <= 0 {
		return c, errors.New("认证服务带回的 token 没有有效期")
	}
	scopes := strings.Fields(tok.Scope)
	if !slices.Contains(scopes, store.SIWCDirectScope) {
		return c, fmt.Errorf("认证服务没授 %s，这个账号可能不符合订阅直连资格", store.SIWCDirectScope)
	}
	c.AccessToken, c.RefreshToken, c.Scopes = tok.Access, tok.Refresh, scopes
	c.ExpiresAt = now.Add(time.Duration(tok.ExpiresIn * float64(time.Second))).Unix()
	if tok.IDToken != "" {
		c.IDToken = tok.IDToken
	}
	return c, nil
}

// ScopeHasDirect 判一段空格分隔的 scope 串（token 回包的 scope 字段形态）有没有
// 订阅直连范围。登录票换 code 之后也拿它把关（§7.13「scope 须含」）。
func ScopeHasDirect(scope string) bool {
	return slices.Contains(strings.Fields(scope), store.SIWCDirectScope)
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
