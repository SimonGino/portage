# golden 转录库

真实上游响应的原始字节存档，驱动 Tap 测试（M0）与 codec 转换测试（P1）。
样本清单与场景口径见 `docs/MVP设计草案.md` §9。

## 目录结构

每个样本一个目录，目录名即 `internal/protocol/golden_test.go` 里 `m0Samples` 的条目：

```
testdata/golden/<样本名>/
  meta.json      # protocol / stream / endpoint / status / expect / verified
  request.json   # 发给上游的请求体（脱敏后）
  response.raw   # 上游响应的原始字节，逐字节保真，不做任何重排
```

## 采集流程

1. 起录制反代，指向真实渠道：

   ```bash
   GOLDENREC_BASE_URL=https://api.anthropic.com GOLDENREC_PROTOCOL=anthropic GOLDENREC_CREDENTIAL=sk-ant-... go run ./cmd/goldenrec
   ```

2. 把 harness 指过去跑出目标场景（Claude Code：`ANTHROPIC_BASE_URL=http://127.0.0.1:8318`）。
   样本落在 `testdata/golden/raw/`，**那是暂存区，不进 git**。

3. 人工过一遍，然后才移进 `testdata/golden/<样本名>/`：
   - 删掉真实凭证（请求头不落盘，但请求体里可能有你粘进去的东西）
   - 删掉个人对话内容——换成无意义的测试文本，**同时相应改掉 `response.raw` 里的文本增量**
   - 核对 `meta.json` 的 `expect` 与 `response.raw` 里的数字确实相符
   - 确认无误后把 `verified` 改成 `true`

`expect` 是 goldenrec 用 Tap 自己算出来的**草稿**：不经人核对就当期望值，等于让实现给自己判卷。
`golden_test.go` 因此拒绝任何 `verified: false` 的样本。

## M0 必抓子集

| 样本名 | 场景 |
|---|---|
| `anthropic-stream-text` | Anthropic 流式，纯文本长回复 |
| `anthropic-stream-tool` | Anthropic 流式，单次 tool_use |
| `anthropic-stream-parallel-tools` | Anthropic 流式，并行多 tool_use 交错增量 |
| `cc-stream-text` | CC 流式，纯文本 |
| `cc-stream-tool` | CC 流式，单次 tool_calls（参数跨 chunk） |
| `cc-stream-parallel-tools` | CC 流式，并行 tool_calls，index 交错 |
| `anthropic-text` / `anthropic-tool` / `anthropic-parallel-tools` | 以上 Anthropic 三例的非流式版 |
| `cc-text` / `cc-tool` / `cc-parallel-tools` | 以上 CC 三例的非流式版 |

Responses 样本与上游异常样本（§9 的 8、9）留到 M1。

### 已入库（2026-08-06）

`cc-*` 六个全部采自真实 OpenAI 兼容上游。`expect` 由一个独立写的解析器从
`response.raw` 重算核对过——不复用 Go 侧任何代码，否则又是实现给自己判卷。

两点要知道：

- **`cc-text` / `cc-stream-text` 刻意带缓存命中**（`CacheReadTokens: 3840`）。
  第一版样本六个的 cache 全是 0，`cached_tokens` 那条解析路径没有任何样本走到，
  把它读错也没人发现；重录时用超长固定前缀打两遍取第二遍，缺口才补上。
- **`cc-stream-parallel-tools` 没能体现 §9 要的「index 交错」**。这个上游三次
  工具调用固定按序成块吐（`[0,0,1,1,2,2]`），换长参数提示重试也一样。不挡 M0
  ——Tap 只提 usage / model / stop_reason，不重组工具调用；index 交错是 P1
  codec 的事，届时要么换个会交错的上游采，要么承认 §9 这条脱离实际。

`anthropic-*` 六个仍缺，卡在没有 Anthropic 上游（见验收票 #6）。

## 顺带核对

采集时留意 harness 实际发了什么，用来验证 §6.1 里那些**从参考仓库推断**的假设：
请求头白名单、`anthropic-beta` 是否真的要转发、`count_tokens` 的调用时机。
对不上的记在验收票（#6）里，回写展开层。
