package server_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/SimonGino/portage/internal/gatewaytest"
)

// 删渠道被候选拦住时，错误必须点名是哪些接入点引着它。
//
// 这条以前会撞到外键，管理端把它翻成「引用了不存在的渠道/模型」——对建/改是对的，
// 对删正好说反了：不是它引用了别人，是别人在引用它。而人拿着那句话没法行动，
// 只能挨个点开接入点找是哪一个。
func TestDeleteChannelNamesTheAccessPointsHoldingIt(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	a := g.LoggedIn(t)

	var ch struct {
		ID int64 `json:"id"`
	}
	a.JSONInto(t, http.MethodPost, "/panel/api/channels", `{
		"name":"old-resp","base_url":{"openai_responses":"`+up.URL+`"},
		"credential":"sk-upstream"}`, &ch)
	a.JSONInto(t, http.MethodPost, "/panel/api/channels/"+itoa(ch.ID)+"/models",
		`{"upstream_model":"gpt-5.6-luna"}`, nil)

	var channels []adminChannel
	a.JSONInto(t, http.MethodGet, "/panel/api/channels", "", &channels)
	modelID := channels[0].Models[0].ID
	a.JSONInto(t, http.MethodPost, "/panel/api/access-points",
		`{"model":"gpt-luna","channel_model_id":`+itoa(modelID)+`}`, nil)

	status, body := a.Do(t, http.MethodDelete, "/panel/api/channels/"+itoa(ch.ID), "")
	if status != http.StatusConflict {
		t.Fatalf("被引用的渠道应该删不掉且回 409，得到 %d %s", status, body)
	}
	if !strings.Contains(body, "gpt-luna") {
		t.Errorf("错误里没点名拦着它的接入点，人不知道该去改哪个：%s", body)
	}
	if strings.Contains(body, "不存在") {
		t.Errorf("还在报「引用了不存在的渠道/模型」——这句话把因果说反了：%s", body)
	}
}

// 单个纳管模型被候选引用时同理：这是「删渠道里某一个模型」那条按钮的路径。
func TestDeleteChannelModelNamesTheAccessPointsHoldingIt(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	a := g.LoggedIn(t)

	var ch struct {
		ID int64 `json:"id"`
	}
	a.JSONInto(t, http.MethodPost, "/panel/api/channels", `{
		"name":"old-resp","base_url":{"openai_responses":"`+up.URL+`"},
		"credential":"sk-upstream"}`, &ch)
	a.JSONInto(t, http.MethodPost, "/panel/api/channels/"+itoa(ch.ID)+"/models",
		`{"upstream_model":"gpt-5.6-luna"}`, nil)

	var channels []adminChannel
	a.JSONInto(t, http.MethodGet, "/panel/api/channels", "", &channels)
	modelID := channels[0].Models[0].ID
	a.JSONInto(t, http.MethodPost, "/panel/api/access-points",
		`{"model":"gpt-luna","channel_model_id":`+itoa(modelID)+`}`, nil)

	status, body := a.Do(t, http.MethodDelete, "/panel/api/channel-models/"+itoa(modelID), "")
	if status != http.StatusConflict {
		t.Fatalf("被引用的纳管模型应该删不掉且回 409，得到 %d %s", status, body)
	}
	if !strings.Contains(body, "gpt-luna") {
		t.Errorf("错误里没点名拦着它的接入点：%s", body)
	}
}

// 把接入点改指到别的渠道之后，原渠道就该删得掉——这正是「两个协议渠道并成一个」
// 要走的路（口径层 v0.33）。拦得住但解不开的话，这个提示就是死路。
func TestDeleteChannelSucceedsOnceTheAccessPointPointsElsewhere(t *testing.T) {
	up := gatewaytest.NewUpstream(t)
	g := gatewaytest.Start(t, gatewaytest.NewDB(t))
	a := g.LoggedIn(t)

	mkChannel := func(name string) int64 {
		var ch struct {
			ID int64 `json:"id"`
		}
		a.JSONInto(t, http.MethodPost, "/panel/api/channels", `{
			"name":"`+name+`","base_url":{"openai":"`+up.URL+`"},
			"credential":"sk-upstream"}`, &ch)
		a.JSONInto(t, http.MethodPost, "/panel/api/channels/"+itoa(ch.ID)+"/models",
			`{"upstream_model":"gpt-5.6-luna"}`, nil)
		return ch.ID
	}
	oldID := mkChannel("old-resp")
	mkChannel("gpt-luna")

	var channels []adminChannel
	a.JSONInto(t, http.MethodGet, "/panel/api/channels", "", &channels)
	byName := map[string]int64{}
	for _, c := range channels {
		byName[c.Name] = c.Models[0].ID
	}

	var ap struct {
		ID int64 `json:"id"`
	}
	a.JSONInto(t, http.MethodPost, "/panel/api/access-points",
		`{"model":"gpt-luna","channel_model_id":`+itoa(byName["old-resp"])+`}`, &ap)

	// 改指向：UpdateAccessPoint 是整条换候选，所以这一步就把引用挪走了。
	a.JSONInto(t, http.MethodPut, "/panel/api/access-points/"+itoa(ap.ID),
		`{"model":"gpt-luna","channel_model_id":`+itoa(byName["gpt-luna"])+`}`, nil)

	if status, body := a.Do(t, http.MethodDelete, "/panel/api/channels/"+itoa(oldID), ""); status != http.StatusNoContent {
		t.Fatalf("引用挪走之后渠道还是删不掉：%d %s", status, body)
	}
}
