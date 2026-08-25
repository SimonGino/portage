package upstream

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/store"
)

// ProbeSelection 是一次检测的选择（口径层 v0.96 ③）：点哪把凭证用哪把（含已停用）、
// 全部纳管模型或单选一个、勾了哪几个协议。勾选不落库，结果也不落库。
type ProbeSelection struct {
	CredentialID int64
	// Model 空串 = 全部启用中的纳管模型；非空必须是其中之一。
	Model     string
	Protocols []string
}

// ModelProbeRow 是一个纳管模型的检测结论行。凭证值不在里面，也不会有掩码。
type ModelProbeRow struct {
	Model   string             `json:"model"`
	Results []ModelProbeResult `json:"results"`
}

// ProbeMatrix 是一次模型级检测的三态矩阵（#51 收层后检测的完整答案）。
// 只报凭证名与格子结论，不报 base_url，更不报凭证值——403 的格子要靠凭证名
// 说清「用的是哪把」（那正是 403 的凭证相关含义）。
type ProbeMatrix struct {
	Credential string
	// Protocols 是归一化去重后的勾选序，即每行 Results 的列序。
	Protocols protocol.Set
	Rows      []ModelProbeRow
}

// probeConcurrency 并发压到 4：这一层的请求形状就是普通推理流量，4 路并发是任何
// 客户端都会有的样子；串行在 20 个模型 × 8 秒超时的最坏情形下要等三分钟，人在
// 弹层前面等不了那么久。
const probeConcurrency = 4

// ProbeChannel 跑一次模型级检测（口径层 v0.96 检测收成一层）：选中的模型 × 勾选的
// 协议，每格发一个带模型名的最小真实请求，回一个三态矩阵。
//
// **只提示、不做闸**（v0.33 血统）：不落库、不参与路由、不影响保存成败。它只由人
// 手点——保存渠道/设置后什么都不跑（v0.96 ①），真实请求的钱只在人手点时花。也因此
// 参数不对就整个拒掉、一个请求都不发：参数错误不该花钱。选择类错误一律是
// store.InvalidInput，管理端翻成 400 原文回显。
func ProbeChannel(ctx context.Context, target store.ProbeTarget, sel ProbeSelection) (ProbeMatrix, error) {
	// 凭证按 id 找，**含已停用的**（v0.38 的立论承接）：恢复是纯人工的，「这把还
	// 坏不坏」除了发一次请求没有别的办法回答。
	var cred store.ProbeCredential
	found := false
	for _, x := range target.Credentials {
		if x.ID == sel.CredentialID {
			cred, found = x, true
			break
		}
	}
	if !found {
		return ProbeMatrix{}, store.InvalidInput{Reason: "这个渠道里没有这份凭证"}
	}

	// 协议集合必须 ⊆ 已声明协议：没声明的那一侧连出站根地址都没有，没有东西可打。
	if len(sel.Protocols) == 0 {
		return ProbeMatrix{}, store.InvalidInput{Reason: "至少勾一个协议"}
	}
	var protos protocol.Set
	for _, raw := range sel.Protocols {
		p := protocol.Normalize(protocol.Protocol(strings.TrimSpace(raw)))
		if !p.Valid() {
			return ProbeMatrix{}, store.InvalidInput{Reason: "协议 " + strconv.Quote(raw) + " 不是 anthropic/openai/openai_responses 之一"}
		}
		if !target.Protocols.Has(p) {
			return ProbeMatrix{}, store.InvalidInput{Reason: "渠道没有声明 " + string(p) + "（没填它的出站根地址），不能检测这一侧"}
		}
		if !protos.Has(p) {
			protos = append(protos, p)
		}
	}

	models := target.Models
	if sel.Model != "" {
		models = nil
		for _, m := range target.Models {
			if m == sel.Model {
				models = []string{m}
				break
			}
		}
		if models == nil {
			return ProbeMatrix{}, store.InvalidInput{Reason: "这个渠道里没有这个启用中的纳管模型"}
		}
	}
	if len(models) == 0 {
		return ProbeMatrix{}, store.InvalidInput{Reason: "这个渠道还没有启用中的纳管模型，先纳管再检测"}
	}

	rows := make([]ModelProbeRow, len(models))
	sem := make(chan struct{}, probeConcurrency)
	var wg sync.WaitGroup
	for i, m := range models {
		rows[i] = ModelProbeRow{Model: m, Results: make([]ModelProbeResult, len(protos))}
		for j, p := range protos {
			wg.Add(1)
			go func(i, j int, p protocol.Protocol, model string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				// 各协议打各的根地址（口径层 v0.96）：测的就是「这一侧真会被请求的那一串」。
				rows[i].Results[j] = ProbeModel(ctx, target.BaseURLs.Get(p), p, cred.Value, model)
			}(i, j, p, m)
		}
	}
	wg.Wait()

	return ProbeMatrix{Credential: cred.Name, Protocols: protos, Rows: rows}, nil
}
