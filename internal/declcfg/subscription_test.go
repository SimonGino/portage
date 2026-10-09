package declcfg_test

import (
	"context"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/declcfg"
)

// #211：声明文件形态不支持订阅渠道——apply 遇 *_account 整份拒启并说明原因（口径层
// §2.2 v1.52「声明文件」子条）。含订阅渠道的库导出的声明文件在任何实例上都 apply 不了，
// 属同一已知边界，文案要写明白。

const subChannelFile = `
channels:
  - name: siwc
    base_url:
      openai_responses: https://api.openai.com/v1
    credential_type: chatgpt_account
    credentials:
      - name: 主号
        credential: '{"client_id":"oaiapp-1","access_token":"at"}'
    models:
      - upstream_model: gpt-6.1-sol
access_points:
  - model: chatgpt
    candidates:
      - target: siwc/gpt-6.1-sol
api_keys:
  - name: laptop
    key: sk-ptg-real-one
`

func TestApplyRejectsSubscriptionCredentialTypes(t *testing.T) {
	for _, ct := range []string{"chatgpt_account", "copilot_account"} {
		t.Run(ct, func(t *testing.T) {
			db := openDB(t)
			msg := applyErr(t, db, strings.Replace(subChannelFile, "chatgpt_account", ct, 1))
			for _, want := range []string{
				"订阅渠道",
				ct,
				"声明文件形态不支持",
				"纯转发机迁移 = 本机登录 + 拷 gateway.db",
				"导出的声明文件在任何实例上都 apply 不了",
			} {
				if !strings.Contains(msg, want) {
					t.Errorf("拒启文案缺 %q:\n%s", want, msg)
				}
			}
		})
	}
}

// 边界断言：含订阅渠道的库导出的声明文件再 apply 回任何库（含同一个）也是拒——
// 这不是数据丢了，是同一已知边界，两边的文案一模一样。
func TestExportedSubscriptionFileStillCannotApply(t *testing.T) {
	db := openDB(t)
	mustApply(t, db, goodFile)
	// 手工把渠道改成订阅形态（导出的形状就是这么来的）。
	if _, err := db.Exec(
		`UPDATE channels SET credential_type = 'chatgpt_account' WHERE name = 'qwen'`); err != nil {
		t.Fatal(err)
	}
	f, _, err := declcfg.Snapshot(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := declcfg.Apply(context.Background(), db, f, discardLogger()); err == nil {
		t.Fatal("含订阅渠道的导出文件该被 apply 拒掉")
	} else if !strings.Contains(err.Error(), "声明文件形态不支持") {
		t.Errorf("导出文件被拒的文案该与导入一致:\n%s", err)
	}
}
