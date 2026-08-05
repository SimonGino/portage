# ai-gateway

个人自用的 AI 模型网关，参考 new-api 的转发内核从头重写。核心能力：三协议（OpenAI Chat Completions / OpenAI Responses / Anthropic Messages）转发与互转、API Key 生命周期、接入点路由（多候选加权分流与故障转移在 M4）、调用用量记录。明确不做多用户运营功能。

技术栈：Go + Gin + SQLite，React 管理端（渠道/接入点/key/用量，embed 单二进制）。

## 文档

| 文档 | 层级 | 说明 |
|---|---|---|
| [docs/口径层设计.md](docs/口径层设计.md) | 口径层 | 需求口径、转换矩阵、边界与非目标、待澄清清单 |
| [docs/MVP设计草案.md](docs/MVP设计草案.md) | 展开/实现层 | 模块划分、canonical 事件模型、codec 接口、配置与数据模型、golden 测试方案 |
| [CONTEXT.md](CONTEXT.md) | 术语表 | 领域语言词典（接入点/候选/渠道/凭证池……），AI 与文档统一用词 |
| [DESIGN.md](DESIGN.md) | 设计规范 | M3 管理端视觉与交互原则（单色克制、表格即证据、凭证脱敏） |

> 两份文档间原有 5 项分歧（转换分期、配置与管理端、路由模型、故障转移、thinking 策略）已于 2026-08-05 全部收敛，记录见 [口径层设计](docs/口径层设计.md) 版本记录 v0.7~v0.10。

## 状态

设计定稿（2026-08-05 口径全收敛），待开工 M0。里程碑 M0~M4 见两份文档（已统一编号）。

## 参考仓库

索引见 [CLAUDE.md](CLAUDE.md)「参考仓库」一节（new-api、sub2api、litellm，均在本地 `~/Code/GitHub/`），逐文件路径对照见 [MVP设计草案 §12](docs/MVP设计草案.md)。本项目不参考公司 fork `maix_ops_go`。
