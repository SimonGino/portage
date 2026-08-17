# Portage

[![CI](https://github.com/SimonGino/portage/actions/workflows/ci.yml/badge.svg)](https://github.com/SimonGino/portage/actions/workflows/ci.yml)

> portage：在两段不通航的水道之间，把船和货扛过陆地。

个人自用的 AI 模型网关。一个 Go 二进制，对外说三种大模型协议——OpenAI Chat Completions、OpenAI Responses、Anthropic Messages——把 agent harness 的请求转发到任意一家上游，需要时跨协议翻译。

**扛过去是有代价的，能漂过去就别上岸。** 这句既是名字的来历，也是实现上的硬约束：入口协议与上游协议相同时，走的是原始字节转发，不做 decode→encode 转码；只有真要跨协议时才拆成 canonical 事件序列再编出去。转换会丢东西（Responses 的 `encrypted_content` 是上游侧不透明密文，跨过去只能丢），所以能不转就不转，非转不可时丢了什么要在日志里说得出来。

流式（SSE）与 tool calling 是及格线不是特性——agent harness 全程依赖这两项，任何一条转换路径不支持它们就等于没做。

## 它做什么

- **三协议转发与互转**。Claude Code 挂到便宜的第三方 OpenAI 兼容模型上，Codex CLI 挂到 Claude 上，客户端不用改。
- **接入点路由**。对外暴露一个模型名，绑定若干候选（渠道纳管的模型 + 权重）；也支持用限定名 `渠道名/模型名` 直连纳管模型。
- **渠道与凭证池**。一个渠道 = base_url + 支持协议集 + 纳管模型 + 1..N 份上游凭证。凭证只写不回读，401 自动摘除并人工恢复，403 换而不摘。
- **两层重试**。同一凭证内退避重试（默认 2 次），渠道内换凭证重试，全局尝试上限封顶。流式已写出首字节后一律不重试——首字节边界即承诺边界。
- **网关 key 生命周期**。前缀 `sk-ptg-`，hash 落库，明文只在创建时展示一次，与上游凭证严格两回事。
- **用量流水**。每次调用一行，记模型、渠道、真正用上的那份凭证、token 数（含缓存读写）、状态与耗时；usage 出自上游自己说的数，透传路径靠旁路 Tap 只读提取，不改流。
- **单二进制**。React 管理端 embed 进 Go 二进制，SQLite 落库，没有外部依赖。

## 协议转换矩阵

同协议对角线是字节透传。跨协议六条已全部放开（#80 收尾），九格全开：

| 入站 ＼ 上游 | Chat Completions | Responses | Anthropic |
| --- | --- | --- | --- |
| **Chat Completions** | 透传 | ✅ 已放开 | ✅ 已放开 |
| **Responses** | ✅ 已放开 | 透传 | ✅ 已放开 |
| **Anthropic Messages** | ✅ 已放开 | ✅ 已放开 | 透传 |

矩阵全开后，仍会回 501 的只剩 `/v1/messages/count_tokens` 落到非 Anthropic 渠道：那个端点在另外两种协议里没有对应物，是**没得做**而不是「还没做」，文案为「该端点没有对应的转换路径」。

转发端点：`/v1/messages`、`/v1/messages/count_tokens`、`/v1/chat/completions`、`/v1/responses`，另有 `/v1/models`（按接入点与协议交集出清单）与免鉴权的 `/healthz`。embeddings / rerank 已定口径为仅透传，尚未接线。

转换走枢纽式而非网桥式：每个协议实现一个 `Codec`，`A→B` = CodecA 解成 canonical + CodecB 编出去，不存在两两互转器。每条路径先备真实 harness 发包与 SSE 转录的 golden 样本，再写实现。

## 快速开始

Docker（推荐）：

```bash
PORTAGE_ADMIN_PASSWORD='想好的密码' docker compose -f deploy/docker-compose.yml up -d --build
```

起来之后开 <http://127.0.0.1:8317/admin> 登录，在页面上配渠道、纳管模型、接入点、网关 key。库是空的很正常，日志里会有一条「`api_keys` 表是空的，所有转发请求都会回 401」，配完就没了。密码走环境变量而不是配置文件，因为镜像里那份配置是烤进镜像层历史的；它只用于初始化，库里已有密码后改它不生效。

从源码构建（需要 Go 1.26+ 与 Node）：

```bash
make build          # 前端产物 embed 进 bin/portage
./bin/portage
```

不带 `-tags webui` 直接 `go build ./cmd/portage` 也能过，只是 `/admin` 会显示一页「前端未构建」——CI 和没装 Node 的机器走的就是那条路。

公网暴露：容器端口保持只发布给本机，前面挂 nginx 收 TLS 并只放行 `/v1`，样例见 [`deploy/nginx.conf.example`](deploy/nginx.conf.example)。**nginx 对 SSE 的几个默认值必须显式改**，漏了不报错，只表现为卡住或断流。

## 接 Codex CLI

把 portage 配成 Codex 的 custom provider（`~/.codex/config.toml`）：

```toml
model_provider = "portage"

[model_providers.portage]
name = "Portage"
base_url = "http://127.0.0.1:8317/v1"
wire_api = "responses"          # 走 /v1/responses，网关按渠道决定要不要转成别的协议
env_key = "PORTAGE_API_KEY"     # 值是网关 key（sk-ptg-…），不是上游 key

# 每个真会用到的接入点各来一份 profile，窗口按**上游真实窗口**写
[profiles.sonnet]
model = "gw-sonnet"
model_provider = "portage"
model_context_window = 200000
# 可选：想更早开始压缩就显式设它；不设则由 Codex 按窗口比例自己算触发点
model_auto_compact_token_limit = 160000
```

**`model_context_window` 必须自己设**，这是网关侧无法代劳的一件事：Codex 不读网关的
`/v1/models` 去取窗口，它认的是自己内置的模型目录。接入点名沿用 Codex 认得的真名
（`gpt-5.1-codex` 这类）时目录里的元数据自然对得上；起了 `gw-sonnet` 这种自定义名字，
或者真实上游窗口比同名模型小，Codex 就会吃 fallback 的 272k、把自动压缩的触发点摆在
约 245k 上——上游真窗口更小的话，请求会先撞上游的 400，压根轮不到压缩。

**压缩（remote compaction）能不能用要看渠道**：Codex 到点会发一个 input 尾部带
`compaction_trigger` 的请求，并要求响应里恰好一个 compaction item，收不到就当场 Fatal
且不重试。所以网关只在**上游自己认得这个 trigger 的 Responses 渠道**上放行它——在管理端
渠道页把「Codex 压缩」勾成「支持」。没勾、或者这个模型路由到的是需要跨协议转换的渠道
（Responses → Anthropic / Chat Completions），压缩请求会被明确拒绝（400，文案说明原因），
而不是转发出去让 Codex 收到一个空的压缩结果。网关侧的本地合成尚未实现
（[#74](https://github.com/SimonGino/portage-legacy/issues/74)），在那之前把窗口设小、让压缩晚点
来，或者把 Codex 挂在支持压缩的 Responses 渠道上。legacy 的 `POST /v1/responses/compact`
不实现，回 501。

## 管理端

`/admin` 下的 React 界面覆盖日常运营的全部动作：渠道（协议集、base_url、凭证池、模型纳管、连通性探测、拉上游模型列表）、接入点（候选与权重）、网关 key、用量与最近调用。

设计规范见 [`DESIGN.md`](DESIGN.md)：单色、排版先行、表格即证据、默认静止。凭证在任何界面与任何错误提示里都脱敏，上游 key 与 base_url 不出现在回显中。

## 配置

业务配置——渠道、纳管模型、接入点、候选、凭证——全部落 DB，不进配置文件。`config.yaml` 只管启动：监听地址、库路径、管理密码初始化、重试参数、全局令牌桶（出厂 10 QPS / 突发 20，只挂转发面）。整个文件都可以省，缺失时全用默认值。

## 明确不做

多用户与租户、计费与充值、兑换码、通知、评测、ES 日志、模型训练相关的一切；new-api 式的「用户等级 × 渠道分组」体系不做（权重分流要做，那是路由不是运营）。图像生成与 audio 端点 v1 不做。Responses 的有状态子路径不做——只支持无状态用法，会话失配时删掉 `previous_response_id` 用完整 input 重建上下文。

能力探测（模型是否支持工具调用/思考）不做：探不准、不可证伪、且没有消费者——网关不按能力拦请求。真需要这类信息时从调用流水里学，观测数据不会过期。

## 状态

M0 透传骨架、M1 key 与流水已交付；M2 六条转换路径已全开，同候选退避重试已落地；M3 管理端与凭证池已落地，收尾中；M4（多候选加权分流、候选间故障转移）语义已拍板，尚未实现——在此之前单候选的临时闸仍然挂着。

进行中的工作在 [Issues](https://github.com/SimonGino/portage-legacy/issues)。

## 文档

| 文档 | 层级 | 说明 |
| --- | --- | --- |
| [docs/口径层设计.md](docs/口径层设计.md) | 口径层 | 需求口径、转换矩阵、边界与非目标、决策版本记录 |
| [docs/MVP设计草案.md](docs/MVP设计草案.md) | 展开层 | 模块划分、canonical 事件模型、codec 接口、数据模型、golden 测试方案 |
| [CONTEXT.md](CONTEXT.md) | 术语表 | 领域语言词典（接入点／候选／渠道／凭证池／网关 key……） |
| [DESIGN.md](DESIGN.md) | 设计规范 | 管理端视觉与交互原则 |

两份设计文档冲突时以口径层 §6 为准。决策连同依据记在口径层版本记录里，只记口径变化不记流水账。

## 参考与致谢

从头重写，不 fork 代码。协议细节、字段语义与转换坑参考了三个项目：

- [new-api](https://github.com/QuantumNous/new-api) —— Go 网关的转发内核、SSE 流式转发、key 鉴权与渠道路由。
- [sub2api](https://github.com/Wei-Shaw/sub2api) —— 协议转换的 Go 实现首要参考，其 `apicompat/` 覆盖本项目全部转换方向。**LGPL-3.0：本项目参考的是实现思路与字段语义，不复制代码。**
- [litellm](https://github.com/BerriAI/litellm) —— 字段映射最全的对照物，thinking、tool calling、usage 语义以它逐 provider 核对。
