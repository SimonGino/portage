package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// #211：credential_type 一列四值（api_key / service_account / chatgpt_account /
// copilot_account），chatgpt_account 的凭证按展开层 §7.13 字段表存 JSON、写侧校验形状。

// siwcJSON 拼一份合法的 chatgpt_account 凭证 JSON（展开层 §7.13 字段表）。
func siwcJSON(expiresAt int64) string {
	return `{"client_id":"oaiapp-1","host_id":"urn:uuid:x","sub":"u1","email":"a@b.c",` +
		`"id_token":"idt","access_token":"at-1","refresh_token":"rt-1",` +
		`"expires_at":` + strconv.FormatInt(expiresAt, 10) + `,"scopes":["chatgpt.tokens.use.direct","openid"]}`
}

func TestCredentialTypeAcceptsFourValues(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for _, ct := range []string{"api_key", "service_account", "chatgpt_account", "copilot_account"} {
		in := ChannelInput{Name: "ch-" + ct, BaseURLs: BaseURLs{OpenAI: "https://x"}, CredentialType: ct}
		if _, err := CreateChannel(ctx, db, in); err != nil {
			t.Errorf("credential_type=%q 建渠道被拒: %v", ct, err)
		}
	}
	if _, err := CreateChannel(ctx, db, ChannelInput{
		Name: "ch-bad", BaseURLs: BaseURLs{OpenAI: "https://x"}, CredentialType: "apikey"}); err == nil {
		t.Error("拼错的 credential_type 该在写入时被拒，而不是留给启动闸")
	}
	// 空串哨兵：建渠道落默认 api_key（既有行为），改渠道不动那一列。
	id, err := CreateChannel(ctx, db, ChannelInput{Name: "ch-def", BaseURLs: BaseURLs{OpenAI: "https://x"}})
	if err != nil {
		t.Fatalf("建渠道: %v", err)
	}
	var got string
	if err := db.QueryRowContext(ctx,
		`SELECT credential_type FROM channels WHERE id = ?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "api_key" {
		t.Errorf("缺省 credential_type = %q, 期望 api_key", got)
	}
}

func TestChatGPTChannelValidatesCredentialJSON(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	id, err := CreateChannel(ctx, db, ChannelInput{
		Name: "sub", BaseURLs: BaseURLs{OpenAIResponses: "https://x"}, CredentialType: "chatgpt_account"})
	if err != nil {
		t.Fatalf("建订阅渠道: %v", err)
	}

	// 坏 JSON：拒因点名是哪里不行。
	if err := AddChannelCredentials(ctx, db, id, []NewCredential{
		{Name: "坏", Value: `{not json`}}); err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Errorf("坏 JSON 该被拒并说明原因, got %v", err)
	}
	// 缺字段：一次点名。
	err = AddChannelCredentials(ctx, db, id, []NewCredential{
		{Name: "缺", Value: `{"client_id":"oaiapp-1","access_token":"at"}`}})
	if err == nil || !strings.Contains(err.Error(), "host_id") {
		t.Errorf("缺字段该被点名, got %v", err)
	}
	// 缺直连 scope：拒。
	err = AddChannelCredentials(ctx, db, id, []NewCredential{
		{Name: "缺scope", Value: `{"client_id":"oaiapp-1","host_id":"h","access_token":"at","refresh_token":"rt","expires_at":100,"scopes":["openid"]}`}})
	if err == nil || !strings.Contains(err.Error(), "chatgpt.tokens.use.direct") {
		t.Errorf("缺 chatgpt.tokens.use.direct 该被拒并点名, got %v", err)
	}

	// 合法 JSON：原文落库（回读口径 v0.47 不动）。
	want := siwcJSON(4102444800)
	if err := AddChannelCredentials(ctx, db, id, []NewCredential{{Name: "主号", Value: want}}); err != nil {
		t.Fatalf("合法 JSON 不该被拒: %v", err)
	}
	creds, err := ListChannelCredentials(ctx, db, id)
	if err != nil || len(creds) != 1 {
		t.Fatalf("读凭证: %v (%d)", err, len(creds))
	}
	if creds[0].Credential != strings.TrimSpace(want) {
		t.Errorf("落库的凭证原文被改写\nwant %s\ngot  %s", want, creds[0].Credential)
	}

	// 换值同样过形状校验。
	if err := UpdateCredential(ctx, db, creds[0].ID, CredentialUpdate{
		Value: `{"client_id":"oaiapp-2","host_id":"h","access_token":"at-2","refresh_token":"rt-2","expires_at":99,"scopes":["chatgpt.tokens.use.direct"]}`}); err != nil {
		t.Errorf("换合法值不该被拒: %v", err)
	}
	if err := UpdateCredential(ctx, db, creds[0].ID, CredentialUpdate{Value: `nope`}); err == nil {
		t.Error("换坏值该被拒")
	}

	// api_key 渠道零变化：任意非空串照收。
	ok, err := CreateChannel(ctx, db, ChannelInput{Name: "plain", BaseURLs: BaseURLs{OpenAI: "https://x"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := AddChannelCredentials(ctx, db, ok, []NewCredential{{Name: "k", Value: "whatever-not-json"}}); err != nil {
		t.Errorf("api_key 渠道该照收任意值: %v", err)
	}
}

func TestStartupGateOnSubscriptionCredentialTypes(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	mk := func(name, ct string) {
		t.Helper()
		id, err := CreateChannel(ctx, db, ChannelInput{Name: name, BaseURLs: BaseURLs{OpenAIResponses: "https://x"}, CredentialType: ct})
		if err != nil {
			t.Fatalf("建渠道 %s: %v", name, err)
		}
		if err := AddChannelCredentials(ctx, db, id, []NewCredential{{Name: "c", Value: siwcJSON(4102444800)}}); err != nil {
			t.Fatalf("加凭证 %s: %v", name, err)
		}
	}

	// chatgpt_account：闸放行——本票起它是真正能跑的 OAuth 账号类型（CONTEXT 术语）。
	mk("siwc", "chatgpt_account")
	if err := Validate(ctx, db); err != nil {
		t.Errorf("chatgpt_account 渠道不该被启动闸拦下: %v", err)
	}

	// copilot_account：值收得进列里，行为随 Copilot 实现票启用——闸要拦并说明。
	mk("cop", "copilot_account")
	err := Validate(ctx, db)
	if err == nil || !strings.Contains(err.Error(), "Copilot") {
		t.Fatalf("copilot_account 渠道该被启动闸拦下并说明原因, got %v", err)
	}
	// service_account：行为照旧（只有口径无刷新代码），闸照拦。
	if _, err := db.ExecContext(ctx,
		`UPDATE channels SET name = 'sa', credential_type = 'service_account' WHERE name = 'cop'`); err != nil {
		t.Fatal(err)
	}
	err = Validate(ctx, db)
	if err == nil || !strings.Contains(err.Error(), "service_account") {
		t.Fatalf("service_account 渠道该照旧被启动闸拦下, got %v", err)
	}
}

func TestResolveCarriesCredentialType(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if _, err := db.Exec(
		`INSERT INTO channels (id, name, base_url_openai_responses, credential_type)
		 VALUES (1, 'sub', 'https://up.example', 'chatgpt_account')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO channel_keys (channel_id, name, credential) VALUES (1, '主号', ?)`, siwcJSON(4102444800)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO channel_models (id, channel_id, upstream_model) VALUES (1, 1, 'gpt-6.1-sol')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO access_points (id, model) VALUES (1, 'sub-模型')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO candidates (access_point_id, channel_model_id, weight) VALUES (1, 1, 100)`); err != nil {
		t.Fatal(err)
	}
	cand, err := Resolve(ctx, db, "sub-模型", protocol.OpenAIResponses)
	if err != nil {
		t.Fatal(err)
	}
	if cand.CredentialType != "chatgpt_account" {
		t.Errorf("Candidate.CredentialType = %q, 期望 chatgpt_account——relay 要靠它走订阅凭证的懒刷新", cand.CredentialType)
	}
	// 直连那条路也要带：两条解析路径共用的投影，漏一边就绕过了。
	if _, err := db.Exec(`UPDATE access_points SET disabled = 1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	cand, err = Resolve(ctx, db, "sub/gpt-6.1-sol", protocol.OpenAIResponses)
	if err != nil {
		t.Fatal(err)
	}
	if cand.CredentialType != "chatgpt_account" {
		t.Errorf("直连路径的 Candidate.CredentialType = %q, 期望 chatgpt_account", cand.CredentialType)
	}
}

// 凭证被死亡码全部停用后，后续请求在 Resolve 就失败——那也是「因凭证需重新登录
// 未打到上游」，解析层要能区分出来（relay 据此落 reauth_required 而不是默认词）。
func TestResolveDistinguishesReauthFromUnusable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	seed := func(reason string) {
		t.Helper()
		if _, err := db.Exec(
			`INSERT INTO channels (id, name, base_url_openai_responses, credential_type)
			 VALUES (1, 'sub', 'https://up.example', 'chatgpt_account')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(
			`INSERT INTO channel_keys (id, channel_id, name, credential, disabled, disabled_reason)
			 VALUES (1, 1, '主号', ?, 1, ?)`, siwcJSON(4102444800), reason); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(
			`INSERT INTO channel_models (id, channel_id, upstream_model) VALUES (1, 1, 'gpt-6.1-sol')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO access_points (id, model) VALUES (1, 'sub-模型')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(
			`INSERT INTO candidates (access_point_id, channel_model_id, weight) VALUES (1, 1, 100)`); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("接入点路径", func(t *testing.T) {
		seed("reauth_required")
		if _, err := Resolve(ctx, db, "sub-模型", protocol.OpenAIResponses); !errors.Is(err, ErrReauthRequired) {
			t.Errorf("期望 ErrReauthRequired, got %v", err)
		}
	})
	t.Run("直连路径", func(t *testing.T) {
		db = openTestDB(t)
		seed("reauth_required")
		if _, err := Resolve(ctx, db, "sub/gpt-6.1-sol", protocol.OpenAIResponses); !errors.Is(err, ErrReauthRequired) {
			t.Errorf("期望 ErrReauthRequired, got %v", err)
		}
	})
	t.Run("人工停用不算需重新登录", func(t *testing.T) {
		db = openTestDB(t)
		seed("人工停用")
		if _, err := Resolve(ctx, db, "sub-模型", protocol.OpenAIResponses); !errors.Is(err, ErrNoUsableCandidate) {
			t.Errorf("人工停用该还是 ErrNoUsableCandidate, got %v", err)
		}
	})
}
