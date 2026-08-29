# 调研：models.dev 目录（数据获取、结构、许可证、快照方案）

- Issue：[#68](https://github.com/SimonGino/portage/issues/68)（地图 [#60](https://github.com/SimonGino/portage/issues/60)）
- 调研日期：2026-08-29（所有数字为当日实测快照）
- 调研人：jinpenga

## 结论先行

models.dev 是 sst 维护的开源 AI 模型目录（MIT 许可证），数据以 TOML 文件存于 GitHub 仓库、经 CI 发布为三个静态 JSON 端点。`api.json`（provider → models 两级字典）约 4.4 MB / gzip 438 KB，含 207 个 provider、7483 条模型；四项价格字段名恰为 `cost.input` / `cost.output` / `cost.cache_read` / `cost.cache_write`，单位统一 **USD 每百万 token**，与 Portage 渠道价格口径直接对齐。裁剪到 Portage 所需字段后快照约 1.46 MB / gzip 164 KB，`go:embed` 打包无压力；上游每小时自动发布、带 ETag，随版本更新快照就是一次条件 GET + 重新生成 + 提交。

## 1. 数据怎么拿

| 途径 | 地址 | 说明 |
| --- | --- | --- |
| API（静态 JSON） | `https://models.dev/api.json` | 完整 provider + 模型数据，**推荐**。实测 HTTP 200，4,425,612 字节 |
| API | `https://models.dev/models.json` | 仅 provider 无关的模型元数据（含 benchmarks、weights 链接），293,118 字节 |
| API | `https://models.dev/catalog.json` | provider 端点 + 模型元数据合并，4,718,754 字节 |
| Logo | `https://models.dev/logos/{provider}.svg` | provider 的 SVG logo |
| 仓库 | `https://github.com/sst/models.dev`（默认分支 `dev`） | 源数据为 TOML：`providers/<id>/provider.toml` + `providers/<id>/models/*.toml` + `logo.svg`；PR 贡献，GitHub Action 按 schema 校验后合并、发布 JSON |

- 端点是静态文件，无鉴权、无速率限制说明；响应带 `etag`（实测 `"55a8b5d9…"`）与 `cache-control: public, max-age=0, must-revalidate`，支持条件 GET 判断是否有更新。
- 三个端点与 schema 见仓库 README（`https://raw.githubusercontent.com/sst/models.dev/dev/README.md`）。

## 2. 数据结构（以 `api.json` 为准）

### 2.1 顶层与 provider id 体系

顶层是 **provider id → provider 对象** 的字典，共 **207** 个 provider。id 为小写 kebab-case（`anthropic`、`amazon-bedrock`、`azure-cognitive-services`、`alibaba-cn`……），既有一方厂商也有聚合网关（openrouter、aihubmix 等）。

Provider 对象字段（全量并集，实测）：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | string | 同键名 |
| `name` | string | 展示名，如 `Anthropic` |
| `models` | object | **模型 id → 模型对象** 字典 |
| `env` | string[] | 凭证环境变量名，如 `["ANTHROPIC_API_KEY"]` |
| `npm` | string | AI SDK 包名，如 `@ai-sdk/anthropic` |
| `doc` | string | 官方模型文档 URL |
| `api` | string（可选） | API base URL，207 个中 181 个有 |

### 2.2 模型条目

`models` 的键 = 该 provider 的**真实 API 模型 id**（如 anthropic 下 `claude-sonnet-4-5`、`claude-opus-4-5-20251101`；聚合网关下可能是路径式 `openai/gpt-5.5`）。全库共 **7483** 条模型条目。

字段全量并集（实测）：`id`、`name`、`description`、`family`、`attachment`、`reasoning`、`reasoning_options`、`tool_call`、`structured_output`、`temperature`、`knowledge`、`release_date`、`last_updated`、`modalities`（`input`/`output` 数组）、`open_weights`、`limit`、`cost`、`provider`、`status`（`beta`/`deprecated`）、`experimental`、`interleaved`。

`limit`（README 定义，单位 token）：

- `limit.context`：最大上下文窗口
- `limit.input`：最大输入（可选）
- `limit.output`：最大输出

### 2.3 价格字段（核心）

`cost` 各字段单位均为 **USD per million tokens**（README 原文 “Cost per million input tokens (USD)” 等）：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `cost.input` | 是（有 cost 时） | 输入 |
| `cost.output` | 是（有 cost 时） | 输出 |
| `cost.cache_read` | 否 | 缓存读 |
| `cost.cache_write` | 否 | 缓存写 |
| `cost.reasoning` | 否 | 推理 token |
| `cost.input_audio` / `cost.output_audio` | 否 | 音频输入/输出 |
| `cost.tiers` | 否 | 分层价：数组，每项含四价字段 + `tier: {type: "context", size: N}`（超过 size 后的价格） |
| `cost.context_over_200k` | 否 | 超 200k 上下文的另一组四价字段（tiers 出现前的旧形态，并存） |

实测样例（anthropic `claude-sonnet-4-5`）：`{"input": 3, "output": 15, "cache_read": 0.3, "cache_write": 3.75}`——与 Anthropic 官方价目一致，单位吻合。

注意：**434/7483 条模型无 `cost` 字段**（免费或未收录价格），消费侧必须容忍缺价。

## 3. 许可证与使用义务

- 仓库 LICENSE：**MIT**（`Copyright (c) 2025 models.dev`；GitHub API `license.spdx_id` 亦为 `MIT`）。数据与代码同仓库、同许可证，无单独数据许可。
- 义务：再分发（含把快照 embed 进 Portage 二进制）需**随附版权声明与 MIT 许可文本**。做法：快照文件旁放 `LICENSE` 或在 NOTICE/文档中注明来源与 MIT 文本，并入 `docs/agents/reference-repos.md` 名单。
- 无 attribution 之外的义务，无 share-alike，无 API 使用条款页。

## 4. 数据量与快照体积（2026-08-29 实测）

| 形态 | 原始 | gzip -9 |
| --- | --- | --- |
| `api.json` 全量 | 4,425,612 B（约 4.4 MB） | 438,462 B（约 438 KB） |
| 裁剪版（每模型只留 `id`/`name`/`limit`/四价 `cost`，provider 只留 `id`/`name`/`models`） | 1,456,827 B（约 1.46 MB） | 163,989 B（约 164 KB） |

## 5. 打包进 Go 单二进制的可行方式

均为标准做法，无风险：

1. **`go:embed` 裁剪版 gzip**（推荐）：构建期脚本 `curl api.json | jq <裁剪>` 生成 `snapshot.json.gz` 入库，`//go:embed` + `gzip.NewReader` 启动时解压解码。二进制增量约 164 KB，启动解析 1.46 MB JSON 为微秒~毫秒级。
2. `go:embed` 原样 `api.json`：二进制 +4.4 MB，胜在零预处理（Go embed 不压缩，全量原样入二进制）。
3. 构建期 codegen 成 Go 字面量：省运行时解析，但生成物大、diff 噪音大，不必要。

与 Portage 现有 webui embed（`internal/webui/dist`）同机制，`-tags` 可选性不需要——快照恒定嵌入即可。

## 6. 随版本更新快照的成本

- 上游更新频率：**每小时都有自动发布提交**（实测 `dev` 分支最近 10 次提交全在 2026-08-29 当日、约每小时一次；6.6k stars，社区 PR 活跃）。
- 更新成本 = 一条命令：重新 `curl https://models.dev/api.json`（可带 `If-None-Match` ETag 判 304），重跑裁剪脚本，提交新 `snapshot.json.gz`。建议做成 `make update-models-snapshot`；随 Portage 发版手动跑一次即可，无需追每小时。
- 风险：schema 演进（如 `context_over_200k` → `tiers` 的并存过渡）说明字段会增长；消费侧只读取自己需要的字段、对未知字段与缺价条目宽容，即可免疫。

## 7. 供设计直接引用的事实清单

1. Provider 取值域 = `api.json` 顶层键：207 个小写 kebab-case id（`anthropic` / `openai` / `amazon-bedrock` / `azure` / `openrouter` / `alibaba-cn` …）。
2. 模型键 = provider 的真实 API 模型 id，可直接与 Portage 纳管模型名对齐（anthropic 下同时有别名 `claude-sonnet-4-5` 与带日期 `claude-sonnet-4-5-20250929` 两种键）。
3. 四价字段名与 Portage 渠道价格一一对应：`cost.input` / `cost.output` / `cost.cache_read` / `cost.cache_write`，单位 USD/百万 token，无需换算。
4. 缺价是常态（434 条无 `cost`），`cache_read`/`cache_write` 亦为可选——填价辅助必须允许「查无价，手填」。
5. `limit.context` / `limit.output` 可顺带用于「输入上限（估算）」的预填参考。
6. MIT 许可证，embed 快照须随附版权与许可文本。
7. 快照裁剪后 gzip 约 164 KB，`go:embed` 即可；更新为发版时一条命令，上游 ETag 支持条件 GET。
