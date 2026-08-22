package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/protocol"
)

// seedChannel 灌一个渠道 + 一把启用凭证 + 一个纳管模型 + 一个指向它的接入点。
// channelProtocols 仍按逗号分隔传（用例好读），落库时展开成每协议地址、同一个值
// （口径层 v0.96）。modelProtocols 为空串就是「继承渠道全集」那条常态路径。
func seedChannel(t *testing.T, db *sql.DB, channelProtocols, modelProtocols string) {
	t.Helper()
	set, err := protocol.ParseSet(channelProtocols)
	if err != nil {
		t.Fatalf("解析协议集 %q: %v", channelProtocols, err)
	}
	var urls BaseURLs
	for _, p := range set {
		urls.Set(p, "https://up.example")
	}
	if _, err := db.Exec(
		`INSERT INTO channels (id, name, base_url_openai, base_url_openai_responses, base_url_anthropic)
		 VALUES (1, 'ch', ?, ?, ?)`,
		urls.OpenAI, urls.OpenAIResponses, urls.Anthropic); err != nil {
		t.Fatalf("插渠道: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO channel_keys (channel_id, name, credential) VALUES (1, 'k', 'sk-x')`); err != nil {
		t.Fatalf("插凭证: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO channel_models (id, channel_id, upstream_model, protocols) VALUES (1, 1, 'gpt-4o', ?)`,
		modelProtocols); err != nil {
		t.Fatalf("插纳管模型: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO access_points (id, model) VALUES (1, 'ap')`); err != nil {
		t.Fatalf("插接入点: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO candidates (access_point_id, channel_model_id, weight) VALUES (1, 1, 100)`); err != nil {
		t.Fatalf("插候选: %v", err)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatalf("建库: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// 两条解析路径都要吃这一列——接入点路径和限定名直连路径各走各的 SQL，漏掉一处的话
// 直连就绕过了模型的协议声明。
func TestResolveAppliesModelProtocolsOnBothPaths(t *testing.T) {
	for _, tc := range []struct{ name, model string }{
		{"接入点", "ap"},
		{"限定名直连", "ch/gpt-4o"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			seedChannel(t, db, "anthropic,openai", "openai")

			// 渠道会说 anthropic，但这个模型只在 openai 侧存在：入站 anthropic 不该
			// 透传去 /v1/messages 撞 404，而该回退到 openai 走转换。
			cand, err := Resolve(context.Background(), db, tc.model, protocol.Anthropic)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if cand.Protocol != protocol.OpenAI {
				t.Errorf("选定协议 = %q，期望 openai", cand.Protocol)
			}
		})
	}
}

// 空串继承渠道全集，行为与 v0.40 之前一字不变——这是存量行不用回填的前提。
func TestResolveEmptyModelProtocolsKeepsPassthrough(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "anthropic,openai", "")

	cand, err := Resolve(context.Background(), db, "ap", protocol.Anthropic)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if cand.Protocol != protocol.Anthropic {
		t.Errorf("选定协议 = %q，期望透传 anthropic", cand.Protocol)
	}
}

// 交集为空落 503 那一档，不是 500。
func TestResolveEmptyIntersectionIsNoUsableCandidate(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "anthropic", "openai")

	_, err := Resolve(context.Background(), db, "ap", protocol.Anthropic)
	if !errors.Is(err, ErrNoUsableCandidate) {
		t.Fatalf("err = %v，期望 ErrNoUsableCandidate", err)
	}
}

// /v1/models 的直连那半边不许列出一个必然 503 的限定名（口径层 v0.32 ③）：交集为空
// 时它当下就是调不通的，跟渠道停用、凭证归零同办。
func TestListExposedModelsHidesEmptyIntersectionDirectName(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "anthropic", "openai")

	got, err := ListExposedModels(context.Background(), db)
	if err != nil {
		t.Fatalf("列模型: %v", err)
	}
	for _, m := range got {
		if m.Direct {
			t.Errorf("列出了限定名 %q，但它的协议交集为空，打过去必 503", m.ID)
		}
	}
}

// 接入点那半边同样不许列。交集为空不进启动闸（口径层 v0.40 ②），所以启动闸这一项
// 兜不住接入点——「列出来的必须调得通」在这半边只能靠运行期过滤。
func TestListExposedModelsHidesDeadAccessPoint(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "anthropic", "openai")

	got, err := ListExposedModels(context.Background(), db)
	if err != nil {
		t.Fatalf("列模型: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("清单 = %+v，期望空——接入点唯一的候选交集为空，打过去必 503", got)
	}
}

// 但只要还有一个候选活着，接入点就得留在清单上：M4 放开多候选之后，一个死候选不该
// 把整个接入点抹掉，它还有兄弟候选能接。
func TestListExposedModelsKeepsAccessPointWithOneLiveCandidate(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "anthropic", "openai") // 1 号候选：交集为空
	if _, err := db.Exec(
		`INSERT INTO channel_models (id, channel_id, upstream_model, protocols)
		 VALUES (2, 1, 'claude-4', '')`); err != nil {
		t.Fatalf("插第二个纳管模型: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO candidates (access_point_id, channel_model_id, weight) VALUES (1, 2, 100)`); err != nil {
		t.Fatalf("插第二个候选: %v", err)
	}

	got, err := ListExposedModels(context.Background(), db)
	if err != nil {
		t.Fatalf("列模型: %v", err)
	}
	var hasAP bool
	for _, m := range got {
		if !m.Direct && m.ID == "ap" {
			hasAP = true
		}
	}
	if !hasAP {
		t.Errorf("清单 = %+v，期望含接入点 ap——它还有一个交集非空的候选", got)
	}
}

// 反面：交集非空的限定名必须照列，别把过滤写成一刀切。
func TestListExposedModelsKeepsIntersectingDirectName(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "anthropic,openai", "openai")

	got, err := ListExposedModels(context.Background(), db)
	if err != nil {
		t.Fatalf("列模型: %v", err)
	}
	var found bool
	for _, m := range got {
		if m.Direct && m.ID == "ch/gpt-4o" {
			found = true
		}
	}
	if !found {
		t.Errorf("清单 = %+v，期望含限定名 ch/gpt-4o", got)
	}
}

// 启动闸拦「值不合法」，但**不拦**「交集为空」——后者是运行期状态不是数据损坏
// （口径层 v0.40 ②），拦了等于让渠道少勾一个协议把整个进程掀翻。
func TestValidateChecksModelProtocolValues(t *testing.T) {
	t.Run("不合法的值拦下", func(t *testing.T) {
		db := openTestDB(t)
		seedChannel(t, db, "anthropic,openai", "")
		// 手写 SQL 塞一个 ParseSet 认不得的值——管理端写不进来，导入的库能。
		if _, err := db.Exec(`UPDATE channel_models SET protocols = 'gemini' WHERE id = 1`); err != nil {
			t.Fatalf("塞脏值: %v", err)
		}
		err := Validate(context.Background(), db)
		if err == nil {
			t.Fatal("期望校验失败")
		}
		if !strings.Contains(err.Error(), "gpt-4o") {
			t.Errorf("校验原文没点名是哪个模型: %v", err)
		}
	})

	t.Run("交集为空不拦", func(t *testing.T) {
		db := openTestDB(t)
		seedChannel(t, db, "anthropic", "openai")
		if err := Validate(context.Background(), db); err != nil {
			t.Errorf("交集为空不该拦在启动闸: %v", err)
		}
	})
}

// 老库迁移：没有 protocols 列的 channel_models 加完列之后，存量行拿到的是空串，
// 也就是「继承渠道全集」——迁移前后行为一字不变，所以不需要回填。
func TestMigrateAddsModelProtocolsWithInheritingDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("开老库: %v", err)
	}
	// v0.40 之前的形状：channel_models 没有 protocols 这一列。
	if _, err := old.Exec(`CREATE TABLE channel_models (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		channel_id INTEGER NOT NULL,
		upstream_model TEXT NOT NULL,
		disabled INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(channel_id, upstream_model))`); err != nil {
		t.Fatalf("建老表: %v", err)
	}
	if _, err := old.Exec(
		`INSERT INTO channel_models (channel_id, upstream_model) VALUES (1, 'legacy')`); err != nil {
		t.Fatalf("插存量行: %v", err)
	}
	old.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("迁移: %v", err)
	}
	defer db.Close()

	var got string
	if err := db.QueryRow(
		`SELECT protocols FROM channel_models WHERE upstream_model = 'legacy'`).Scan(&got); err != nil {
		t.Fatalf("读存量行: %v", err)
	}
	if got != "" {
		t.Errorf("存量行 protocols = %q，期望空串（继承渠道全集）", got)
	}
}

// 迁移靠问库里的列长什么样判断跑没跑过，所以重复 Open 必须是空操作。
func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	for i := range 3 {
		db, err := Open(path)
		if err != nil {
			t.Fatalf("第 %d 次 Open: %v", i+1, err)
		}
		db.Close()
	}
}

// 写侧：空集合归一成空串（继承），非空则折旧名去重，认不得的取值当场拒。
func TestSetChannelModelProtocolsNormalizes(t *testing.T) {
	db := openTestDB(t)
	seedChannel(t, db, "anthropic,openai", "")
	ctx := context.Background()

	if err := SetChannelModelProtocols(ctx, db, 1, protocol.Set{"openai_cc", "openai"}); err != nil {
		t.Fatalf("写协议子集: %v", err)
	}
	var got string
	if err := db.QueryRow(`SELECT protocols FROM channel_models WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("回读: %v", err)
	}
	if got != "openai" {
		t.Errorf("protocols = %q，期望折旧名后去重成 openai", got)
	}

	// 空集合是显式改回「继承渠道全集」，不是错误。
	if err := SetChannelModelProtocols(ctx, db, 1, protocol.Set{}); err != nil {
		t.Fatalf("清空协议子集: %v", err)
	}
	if err := db.QueryRow(`SELECT protocols FROM channel_models WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("回读: %v", err)
	}
	if got != "" {
		t.Errorf("protocols = %q，期望空串", got)
	}

	if err := SetChannelModelProtocols(ctx, db, 1, protocol.Set{"gemini"}); err == nil {
		t.Error("认不得的协议名应当被拒")
	}
}
