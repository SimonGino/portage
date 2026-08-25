# Portage

个人自用 AI 模型网关：三协议（Anthropic Messages / OpenAI Chat Completions / OpenAI Responses）转发与全互转、API Key 生命周期、接入点路由、用量记录。Go + Gin + SQLite，React 管理端产物 embed 进单二进制。个人项目，PO 即唯一开发者与裁决人。

## 构建与测试

```bash
make build                # 前端 npm ci && vite build → internal/webui/dist，再 go build -tags webui
go build ./cmd/portage    # 不带 -tags webui 也能过，/admin 出「前端未构建」页——CI 与无 Node 的机器走这条
go test -count=1 ./...    # 禁缓存：改了 golden 样本要重跑；涉并发/收场序的改动加 -race
gofmt -w .                # CI 有独立 gofmt 闸，未格式化即红（vet 与 test 都不管格式）
```

## 与 PO 协作

- 技术**事实**（代码现状、链路、字段语义、既有实现）自己查代码与参考仓库求证，不拿事实问题问 PO。
- **决策**逐项交 PO 拍板，一次一个，每项附你的建议与理由。PO 的回答是裁决不是建议；裁决与此前口径冲突时，先指出冲突再执行。
- 结论先行，一句能说清的不写三句，不堆砌背景铺垫。

## 事实源：查什么去哪

| 要查什么 | 去哪 |
| --- | --- |
| 术语 | 根 `CONTEXT.md`（只做术语表） |
| 需求口径、边界与非目标、待澄清、待收敛冲突 | `docs/口径层设计.md` |
| 模块划分、canonical 事件模型、codec 接口、数据模型、测试方案 | `docs/MVP设计草案.md` |
| 管理端视觉、交互、页面级信息架构 | 根 `DESIGN.md` |

三层的职责边界、冲突处理、版本记录规则见 [`docs/agents/doc-layers.md`](docs/agents/doc-layers.md)。**文档修改人一律署 `jinpenga`。**

## 硬约束

- **透传保真优先**：同协议路径不做 decode→encode 转码。
- **上游 key 只存服务端**，错误回显不带上游 key 与 base_url。
- **协议细节、字段语义、转换坑先查参考仓库再下结论，不凭记忆**——名单、读法与许可证义务见 [`docs/agents/reference-repos.md`](docs/agents/reference-repos.md)；本机上其他 fork 一律不参考。
- **转换路径先备 golden 样本（真实 harness 发包 / SSE 转录）再实现**，验收对照 `docs/MVP设计草案.md` §5 坑清单与 §9 测试方案。golden 与构造样本的闸门差别见 [`testdata/fixtures/README.md`](testdata/fixtures/README.md)。
- 提交信息简洁中文，可用 `feat:` 等前缀。

## Agent skills

- **Issue tracker**：GitHub Issues（`SimonGino/portage`，gh CLI）——[`docs/agents/issue-tracker.md`](docs/agents/issue-tracker.md)
- **Triage 标签**：五个默认角色标签 + 本仓库另加的 `blocked`——[`docs/agents/triage-labels.md`](docs/agents/triage-labels.md)
- **Domain docs**：探索代码前先读的术语与决策——[`docs/agents/domain.md`](docs/agents/domain.md)
