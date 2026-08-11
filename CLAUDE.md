# ai-gateway 项目约定

个人项目（PO 即唯一开发者/裁决人）。参考仓库只有「参考仓库」一节列的那三个，本机上的其他 fork 一律不参考。

## 角色与协作方式

- PO（本人）裁决需求口径、范围、优先级、术语。
- 技术**事实**（代码现状、链路、字段语义、既有实现）自己查代码/查参考仓库求证，不要拿事实问题来问 PO；**决策**逐项交 PO 拍板，一次一个，每项附你的建议与理由。
- PO 的回答是裁决不是建议；裁决与此前口径冲突时，先指出冲突再执行。
- 沟通结论先行，一句能说清的不写三句，不堆砌背景铺垫。

## 文档层级与事实源

- `docs/口径层设计.md`：口径层——需求口径、关键决策、边界与非目标、待澄清清单、待收敛冲突。不写 DDL、接口契约、页面像素细节，页面最多到信息架构与卡片/区带级排版。
- `docs/MVP设计草案.md`：展开/实现层——模块划分、canonical 事件模型、codec 接口、配置与数据模型、测试方案。实现层细节只落这里，不回灌口径层。
- 两份文档冲突时不要自行择一执行；已知冲突见口径层 §6，新发现的列出来交 PO 拍板。口径收敛的结果同时回写两份文档。
- 版本记录只记口径变化与依据（谁裁定、为什么），不记流水账；**修改人一律署 `jinpenga`**（不写 Claude）。
- 不复制 `_v2` 文件，历史靠文内版本记录与 git。

## 参考仓库（本地 `~/Code/GitHub/`）

涉及协议细节、字段语义、转换坑，先查以下仓库再下结论，不凭记忆。逐文件的路径对照表见 `docs/MVP设计草案.md` §12。

- `new-api`（QuantumNous/new-api）：Go 网关主参考。读 `relay/` 协议适配层、SSE 流式转发、`controller/`+`middleware/` 的 key 鉴权与渠道路由；运营功能（计费/多用户/渠道权重）不抄。**上游 canonical DTO 在 `relaykit/dto/`；下游 fork 常把它改成另一套 `dto/`，勿混。**
- `sub2api`（Wei-Shaw/sub2api）：**协议转换的 Go 实现首要参考**。`backend/internal/pkg/apicompat/` 是自包含转换库（Responses↔ChatCompletions、Responses↔Anthropic、CC↔Anthropic bridge、Responses SSE 事件线格式，含 Codex 事件流测试）；`previous_response_id` 处理见 `internal/service/openai_previous_response_id.go` 与 `RemovePreviousResponseIDFromBody`。**LGPL-3.0：参考思路与字段语义可以，整包复制需评估义务（Go 静态链接下约等于整项目跟随）。**订阅池、计费不在本项目范围。
- `litellm`（BerriAI/litellm）：Python 网关，字段映射最全。做 Anthropic↔OpenAI 转换时对照 `litellm/llms/*/chat/` 各 provider transformation 核对 thinking、tool calling、usage 语义。

## 工程约定

- Go + Gin + SQLite；React 管理端（若口径收敛保留）构建产物 embed 进单二进制。
- 透传保真优先：同协议路径不做 decode→encode 转码。
- 转换路径先备 golden 样本（真实 harness 发包/SSE 转录）再实现，验收对照 `docs/MVP设计草案.md` §5 坑清单与 §9 测试方案。
- 上游 key 只存服务端，错误回显严禁泄露上游 key 与 base_url。
- 提交信息简洁中文，可用 `feat:` 等前缀。

## Agent skills

### Issue tracker

Issue 记在 GitHub Issues（`SimonGino/ai-gateway`，gh CLI 操作）；外部 PR 不作为 triage 入口。见 `docs/agents/issue-tracker.md`。

### Triage 标签

五个默认标签：needs-triage / needs-info / ready-for-agent / ready-for-human / wontfix。见 `docs/agents/triage-labels.md`。

### Domain docs

单上下文：根 `CONTEXT.md`（仅术语表）+ `docs/adr/`（仅实现层决策；口径决策走口径层版本记录）。见 `docs/agents/domain.md`。
