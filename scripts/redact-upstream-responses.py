#!/usr/bin/env python3
"""脱敏 Responses upstream 样本的 `response.raw`。

    python3 scripts/redact-upstream-responses.py raw/<原目录> > golden/<样本名>/response.raw

`request.json` 那半边有 `scripts/redact-inbound-responses.jq`；这一半是给上游响应用的。
起因是 Codex 的 `turn_id` 会被上游**回显**在 `response.*` 事件里（`metadata` 与
`internal_chat_message_metadata_passthrough` 两处，一份响应里出现四次），只洗请求侧
等于没洗。

两条硬约束决定了这里是**纯文本替换**而不是解析→重序列化：

1. `response.raw` 的存在理由是逐字节保真。用 jq 逐行 `data:` 重写会顺手改掉键序、
   空白与数字格式，样本就不再是上游发的那串字节了。
2. 指纹是 UUID，替换成等长的全零 UUID，**字节偏移一个不动**。路径替换会改长度，
   但 SSE 没有长度前缀，改了不影响帧边界。

指纹**不硬编码、也不靠正则扫**：从同目录的 `request.json` 里把 Codex 自报的那几个 id
读出来，只替换这几个确切的值。扫全文的做法会误伤 `encrypted_content`——那串 base64url
里理论上凑得出 8-4-4-4-12 的形状，而它恰恰是压缩样本唯一不能动的东西。

**例外是 `zero_derived_ids`（2026-08-14，PO 裁定，#79 收尾）**：响应侧的
`prompt_cache_key` 回显值与 `safety_identifier`（`user-…`）是**中转/上游派生**的，请求体
里根本没有这两个值，上面那套「从 request 收值」的做法结构上就够不着它们，只能按键名扫。
这不违反上一段的口径：上一段禁的是按**值的形状**扫（UUID 长这样就换），而 base64url
的字母表里没有 `"` 也没有 `:`，键名锚定的 `"<key>":"…"` 永远落不到 `encrypted_content`
的密文里。归零同样**等长**——UUID 保连字符位、16 位 hex 换 16 个 0、`safety_identifier`
留 `user-` 前缀其余等长归零，`response.raw` 的字节偏移一个不动，`meta.json` 的 expect
不用跟着动。函数幂等，已归零的样本重跑是 no-op。
"""

import json
import re
import sys
from pathlib import Path

ZERO = "00000000-0000-0000-0000-000000000000"


def fingerprints(request: dict) -> set[str]:
    """从请求体里收集 Codex 自报的会话 id。收的是**值**，不是模式。"""
    found: set[str] = set()

    def take(v):
        if isinstance(v, str) and re.fullmatch(r"[0-9a-f-]{36}(:\d+)?", v):
            found.add(v.split(":")[0])

    meta = request.get("client_metadata")
    if isinstance(meta, dict):
        for k, v in meta.items():
            if k == "x-codex-turn-metadata" and isinstance(v, str):
                try:
                    for iv in json.loads(v).values():
                        take(iv)
                except json.JSONDecodeError:
                    pass
            else:
                take(v)
    take(request.get("prompt_cache_key"))
    for item in request.get("input") or []:
        if isinstance(item, dict):
            for key in ("internal_chat_message_metadata_passthrough", "metadata"):
                if isinstance(item.get(key), dict):
                    for v in item[key].values():
                        take(v)
    return found


def _zero_like(v: str) -> str:
    """等长归零：连字符留在原位，其余字符一律换 `0`。"""
    return "".join("-" if c == "-" else "0" for c in v)


def _zero_safety(v: str) -> str:
    """`safety_identifier` 保留 `user-` 前缀，其余等长归零。"""
    return "user-" + "0" * (len(v) - 5) if v.startswith("user-") else _zero_like(v)


DERIVED_KEYS = {"prompt_cache_key": _zero_like, "safety_identifier": _zero_safety}


def zero_derived_ids(text: str) -> str:
    """按**键名**归零中转/上游派生的标识。等长、幂等，见模块文档最后一段。"""
    for key, zero in DERIVED_KEYS.items():

        def sub(m, zero=zero):
            new = zero(m.group(2))
            assert len(new) == len(m.group(2)), (key, m.group(2), new)
            return m.group(1) + new + m.group(3)

        text = re.sub(rf'("{key}"\s*:\s*")([^"\\]*)(")', sub, text)
    return text


def main() -> int:
    src = Path(sys.argv[1])
    request = json.loads((src / "request.json").read_text())
    raw = (src / "response.raw").read_text()

    for fp in sorted(fingerprints(request)):
        raw = raw.replace(fp, ZERO)
    raw = zero_derived_ids(raw)
    # 兜底一遍路径，口径同 jq 那份的 scrub_paths：模型能把主机路径写进正文里。
    #
    # 字符类**必须排除反斜杠**（2026-08-14 修，#79）：这里扫的是 SSE 的 data 行，路径
    # 躺在一层 JSON 字符串里、收尾是 `\"`。只排除 `"` 的话正则会把那个转义反斜杠一起
    # 吃掉，留下一个裸 `"`，整帧 JSON 就废了——responses-stream-reasoning-turn1 有两帧
    # 是这么坏的（模型写的 `\"workdir\":\"/private/tmp/claude-…/work\"`）。jq 那份的
    # scrub_paths 不受影响：它作用在**已解析**的字符串上，看不到转义反斜杠。
    raw = re.sub(r"/private/tmp/claude-\d+/[^\"\\\s]*", "/tmp/goldenrec-work", raw)
    raw = re.sub(r"/Users/[^\"/\\\s]+", "/Users/tester", raw)

    sys.stdout.write(raw)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
