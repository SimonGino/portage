package preset

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/pricing"
	"github.com/SimonGino/portage/internal/protocol"
)

// 清单是手写数据，错了不会编译失败，只会在管理端预填出一条打不通的渠道——在这儿逮。
func TestPresetsWellFormed(t *testing.T) {
	ps, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ps) < 16 {
		t.Fatalf("清单只有 %d 条，初版名单是厂商 13 + 中转 3", len(ps))
	}
	seen := map[string]bool{}
	for _, p := range ps {
		if p.ID == "" || p.Name == "" || p.Icon == "" || p.KeysURL == "" {
			t.Errorf("%q 缺 id / name / icon / keys_url", p.ID)
		}
		if seen[p.ID] {
			t.Errorf("id %q 重复", p.ID)
		}
		seen[p.ID] = true
		if p.Group != "vendor" && p.Group != "relay" {
			t.Errorf("%s: group %q 不是 vendor / relay", p.ID, p.Group)
		}
		switch p.AuthScheme {
		case "", "default", "bearer", "raw":
		default:
			t.Errorf("%s: auth_scheme %q 不认得", p.ID, p.AuthScheme)
		}
		checkURL(t, p.ID+" keys_url", p.KeysURL)
		// 图标键对的是前端 web/src/icons/svg 下的文件名：拼错了 tile 只会退成首字母块，不报错。
		if _, err := os.Stat("../../web/src/icons/svg/" + p.Icon + ".svg"); err != nil {
			t.Errorf("%s: 图标 %q 在 web/src/icons/svg 下没有", p.ID, p.Icon)
		}
		if len(p.Plans) == 0 {
			checkProtocols(t, p.ID, p.Protocols)
			continue
		}
		if len(p.Protocols) != 0 || p.ModelsDev != "" {
			t.Errorf("%s: 有 plans 时地址与 models_dev 只写在 plans 里", p.ID)
		}
		planIDs := map[string]bool{}
		for _, pl := range p.Plans {
			if pl.ID == "" || pl.Name == "" || planIDs[pl.ID] {
				t.Errorf("%s: plan 缺 id / name 或 id 重复：%+v", p.ID, pl)
			}
			planIDs[pl.ID] = true
			if pl.KeysURL != "" {
				checkURL(t, p.ID+"/"+pl.ID+" keys_url", pl.KeysURL)
			}
			checkProtocols(t, p.ID+"/"+pl.ID, pl.Protocols)
		}
	}
}

// models_dev 是渠道 provider 标注的键：快照里没有就既没有建议模型也没有建议价，
// 快照更新后 id 改名或下线在这儿立刻露出来。
func TestModelsDevInSnapshot(t *testing.T) {
	ps, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	check := func(where, id string) {
		if id == "" {
			return // 中转可以不标注：没有建议模型，合法
		}
		prices, err := pricing.ModelPrices(id)
		if err != nil {
			t.Fatalf("ModelPrices: %v", err)
		}
		if len(prices) == 0 {
			t.Errorf("%s: models_dev %q 在快照里没有有价模型", where, id)
		}
	}
	for _, p := range ps {
		check(p.ID, p.ModelsDev)
		for _, pl := range p.Plans {
			check(p.ID+"/"+pl.ID, pl.ModelsDev)
		}
	}
}

// 地址存的是协议子路径之前的前缀，网关追加 /v1/chat/completions、/v1/responses、
// /v1/messages（展开层 §6.1）——所以前缀带 /v1 或带完整路径都会拼成死地址。
func checkProtocols(t *testing.T, where string, ps map[string]string) {
	t.Helper()
	if len(ps) == 0 {
		t.Errorf("%s: 一个协议都没有", where)
	}
	for k, v := range ps {
		if !protocol.Protocol(k).Valid() || protocol.Normalize(protocol.Protocol(k)) != protocol.Protocol(k) {
			t.Errorf("%s: 协议键 %q 不认得", where, k)
		}
		checkURL(t, where+" "+k, v)
		for _, bad := range []string{"/v1", "/chat/completions", "/responses", "/messages"} {
			if strings.HasSuffix(v, bad) {
				t.Errorf("%s %s: %q 以 %s 结尾，网关还会再追加协议子路径", where, k, v, bad)
			}
		}
	}
}

func checkURL(t *testing.T, where, s string) {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(s, "/") {
		t.Errorf("%s: %q 不是不带查询串、不带尾斜杠的 https 地址", where, s)
	}
}
