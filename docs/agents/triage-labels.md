# Triage 标签

skills 说的五个角色词，在本仓库就是它们的字面量：

| 标签 | 含义 |
| --- | --- |
| `needs-triage` | 待裁决 |
| `needs-info` | 等报告人补信息 |
| `ready-for-agent` | 规格完整，AI 可独立领走 |
| `ready-for-human` | 需要人来实现 |
| `wontfix` | 不做 |

## 本仓库另有的两个

| 标签 | 含义 |
| --- | --- |
| `blocked` | 规格完整但被前置票挡着，暂不可领 |
| `bug` | 类型标签而非 triage 角色，与上表的角色标签正交，可并存 |

**`blocked` 与 `ready-for-agent` 互斥**：规格再全，前置票没落地就不能说「AI 可独立领走」。前置票一关，摘 `blocked` 换 `ready-for-agent`。

阻塞源以 GitHub 原生 issue dependencies 为准（见 `issue-tracker.md`），正文 `Blocked by` 行只在 dependencies 不可用时兜底。

GitHub 默认标签里除 `bug` 外已全部删除（2026-08-06）：单人私仓用不上 `good first issue` / `help wanted` 一类，留着只让标签选择器变吵。
