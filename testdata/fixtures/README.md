# testdata/fixtures —— 构造样本（**不是** golden）

这里放的是**照官方文档形状手工构造**的样本。它与 `testdata/golden/` 是两回事，别混：

| | `testdata/golden/` | `testdata/fixtures/`（本目录） |
| --- | --- | --- |
| 出处 | 真实 harness 发包 / 真实上游 SSE 转录（`cmd/goldenrec` 录） | 照官方文档改真实转录的数字构造出来的 |
| meta 闸门 | `verified: true`（人核过才准进） | `synthetic: true`（钉死它不是转录） |
| 能证明什么 | 上游真的这么发 | 我们的解析对**这个形状**是对的 |
| 不能证明什么 | —— | 上游真的这么发 |

**构造样本不得改名搬进 `golden/`。** 真录到了就在 `golden/` 下新开目录、走 `verified` 那道闸；
本目录这份留着当形状回归即可。

## anthropic-cache-hit / anthropic-stream-cache-hit

补的是 #37 第 2 项的**一半**：`anthropic-*` 六份真实样本的 cache 计数全是 0（中转那侧压根
不回），于是 `cache_read_input_tokens` / `cache_creation_input_tokens` 的解析路径此前只有 CC
样本走到过——Anthropic 侧读错了没有任何样本会发现。

这两份从 `golden/anthropic-text` 与 `golden/anthropic-stream-text` 的真实转录派生，**只改
usage 里的数字**，其余字节一字未动。数字取值依据 platform.claude.com/docs/en/api/rate-limits
（2026-08-13 核对）：`input_tokens` 只算最后一个缓存断点**之后**的 token，与两项缓存互不相交，
即 `total_input = cache_read + cache_creation + input`。

它**不满足** #37 的验收——那一条要的是官方直连实测。等拿到官方 key，按 `request.json` 里的
形状（超长固定前缀配 `cache_control`）连打两遍、取第二遍，录进 `golden/`。
