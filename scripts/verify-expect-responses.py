#!/usr/bin/env python3
"""独立重算 Responses upstream 样本的 `expect`，与 `meta.json` 里那份比对。

    python3 scripts/verify-expect-responses.py testdata/golden/<样本名> …

`meta.json` 的 `expect` 是 goldenrec 用 Go 侧的 Tap 预填的**草稿**——不核就当期望值，
等于让实现给自己判卷。这份脚本因此**不复用 Go 侧任何代码**，直接从 `response.raw`
按 wire 语义重算一遍，做法同 `cc-*`（2026-08-06）与 `anthropic-*`（2026-08-11）两批。

语义按 `protocol.Summary` 的约定——那一层**保留上游原始取值**，不做 canonical 归一：

    Model            = response.model
    InputTokens      = usage.input_tokens                              （Responses 本就是毛值）
    OutputTokens     = usage.output_tokens
    CacheReadTokens  = usage.input_tokens_details.cached_tokens
    CacheWriteTokens = usage.input_tokens_details.cache_write_tokens
    ReasoningTokens  = usage.output_tokens_details.reasoning_tokens    （v0.66；输出的明细，不是加数）
    HasReasoningTokens = 上面那个 **键在不在**                          （0 与「不报」要分得开）
    StopReason       = response.status                                 （原生词，如 completed）
    Degraded         = false                                           （Tap 主动放弃解析才为真）

取的是**终态事件**：`response.completed` / `.incomplete` / `.failed` 里那个 response 对象。
"""

import json
import sys
from pathlib import Path

TERMINAL = ("response.completed", "response.incomplete", "response.failed")


def recompute(raw: str) -> dict:
    final = None
    for line in raw.splitlines():
        if not line.startswith("data: "):
            continue
        try:
            ev = json.loads(line[6:])
        except json.JSONDecodeError:
            continue
        if ev.get("type") in TERMINAL:
            final = ev.get("response") or {}
    if final is None:
        raise SystemExit("response.raw 里没有终态事件")
    usage = final.get("usage") or {}
    details = usage.get("input_tokens_details") or {}
    # 判的是**键在不在**而不是值：上游报了 0（这次没思考）与整个不报（不认这个字段）
    # 在流水上是两档，靠 HasReasoningTokens 分开（口径层 v0.66）。
    out_details = usage.get("output_tokens_details") or {}
    reasoning = out_details.get("reasoning_tokens")
    return {
        "Model": final.get("model", ""),
        "InputTokens": usage.get("input_tokens", 0),
        "OutputTokens": usage.get("output_tokens", 0),
        "CacheReadTokens": details.get("cached_tokens", 0),
        "CacheWriteTokens": details.get("cache_write_tokens", 0),
        "ReasoningTokens": reasoning or 0,
        "HasReasoningTokens": reasoning is not None,
        "StopReason": final.get("status", ""),
        "Degraded": False,
    }


def main() -> int:
    bad = 0
    for arg in sys.argv[1:]:
        d = Path(arg)
        meta = json.loads((d / "meta.json").read_text())
        got = recompute((d / "response.raw").read_text())
        want = meta.get("expect") or {}
        diff = {k: (want.get(k), v) for k, v in got.items() if want.get(k) != v}
        print(f"{d.name}: {'一致' if not diff else '不一致 ' + json.dumps(diff)}")
        for k, v in got.items():
            print(f"    {k:<17}= {v!r}")
        bad += bool(diff)
    return 1 if bad else 0


if __name__ == "__main__":
    raise SystemExit(main())
