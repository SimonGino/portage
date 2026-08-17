# Triage Labels

The skills speak in terms of five canonical triage roles. This file maps those roles to the actual label strings used in this repo's issue tracker.

| Label in mattpocock/skills | Label in our tracker | Meaning                                  |
| -------------------------- | -------------------- | ---------------------------------------- |
| `needs-triage`             | `needs-triage`       | Maintainer needs to evaluate this issue  |
| `needs-info`               | `needs-info`         | Waiting on reporter for more information |
| `ready-for-agent`          | `ready-for-agent`    | Fully specified, ready for an AFK agent  |
| `ready-for-human`          | `ready-for-human`    | Requires human implementation            |
| `wontfix`                  | `wontfix`            | Will not be actioned                     |

When a skill mentions a role (e.g. "apply the AFK-ready triage label"), use the corresponding label string from this table.

Edit the right-hand column to match whatever vocabulary you actually use.

## 本仓库另有的标签

这两个不在 skills 的角色词汇里，是本仓库自己的：

| 标签      | 含义                                                                 |
| --------- | -------------------------------------------------------------------- |
| `blocked` | 规格完整但被前置票挡着，暂不可领。阻塞源以 GitHub 原生 issue dependencies 为准（见 `issue-tracker.md`），正文 `Blocked by` 行只在 dependencies 不可用时兜底 |
| `bug`     | 类型标签而非 triage 角色，与上表的角色标签正交，可并存                 |

`blocked` 与 `ready-for-agent` 互斥：规格再全，前置票没落地就不能说「AI 可独立领走」。前置票一关，摘 `blocked` 换 `ready-for-agent`。M2 的转换路径是链式依赖（#10 → #11 → #12），这个状态会反复用到。

GitHub 默认标签里除 `bug` 外已全部删除（2026-08-06）：单人私仓用不上 `good first issue` / `help wanted` 一类，留着只让标签选择器变吵。
