package server

import (
	"net/http"
	"strconv"

	"github.com/SimonGino/portage/internal/calllog"
	"github.com/SimonGino/portage/internal/protocol"
	"github.com/SimonGino/portage/internal/protocol/codecs"
	"github.com/SimonGino/portage/internal/store"
	"github.com/SimonGino/portage/internal/tokencount"

	"github.com/gin-gonic/gin"
)

// countTokensLocal 是 count_tokens 命中非 anthropic 渠道时的本地路（#18，口径层
// v0.80）：CC / Responses 那边没有原生端点可转发，网关本地估算回 200
// `{"input_tokens":N}`。此前这一格回 501，真机代价见票面：这版 Claude Code 每轮都
// 打、开场连打二十几次，501 让客户端的压缩判断退化，几十条 501 还冲空限流桶误伤
// 真实请求（#16）。
//
// 三件事这条路**不做**：不打上游（为了更准去打上游是禁手——那会把本地端点变成一次
// 真实调用）；不写 usage 列（rec 没有 Summarized，这不是上游报的数）；不记出站端点
// （rec 没有 Dialing，「非空 ⟺ 真的向上游发起过」的不变量原样成立，流水里这种行
// 靠端点列辨认，#17）。
func (s *Server) countTokensLocal(c *gin.Context, rec *calllog.Recorder, ep protocol.Endpoint, cand store.Candidate, body []byte) {
	codec := codecs.New(ep.Proto, codecs.Options{DefaultMaxTokens: s.cfg.DefaultMaxTokens})
	if codec == nil {
		ep.Proto.WriteError(c.Writer, http.StatusInternalServerError, "估算路径不可用")
		return
	}
	req, err := codec.DecodeRequest(body, false)
	if err != nil {
		// 解不动是客户端的问题；收场词照早退分支的缺省（rejected），与转换路径的
		// 解码 400 同一档。
		s.log.Warn("count_tokens 请求解码失败", "err", err)
		ep.Proto.WriteError(c.Writer, http.StatusBadRequest, "请求体无法解析为 "+string(ep.Proto)+" 请求")
		return
	}
	n, err := tokencount.Estimate(req)
	if err != nil {
		s.log.Error("count_tokens 本地估算失败", "err", err)
		ep.Proto.WriteError(c.Writer, http.StatusInternalServerError, "token 估算不可用")
		return
	}

	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)
	rec.Succeeded()
	rec.FirstByte()
	if _, err := c.Writer.Write([]byte(`{"input_tokens":` + strconv.Itoa(n) + `}`)); err != nil {
		rec.Failed(calllog.StreamAborted, "")
		s.log.Warn("count_tokens 估算响应写出失败", "err", err)
	}
}
