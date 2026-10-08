# Research：渠道预设的数据来源（#178）

调研日期 2026-10-08，隶属地图 #177，阻塞 #181。结论回溯一手来源：magpie 源码（`~/Code/GitHub/magpie`，MIT）、models.dev 实拉的 `api.json`（226 个 provider、8454 个模型）、本仓库现状。

## 结论

**三者混合，不三选一**：预设目录 = 仓库内**手写静态清单**（烤进二进制，Go 或 JSON 文件，约 40 条）；**模型清单与建议价继续走 models.dev 快照**（发版时 `make update-models-snapshot`，不启动拉取）；预设条目用 `models_dev` 字段指向快照里的 provider id，与现有 provider 标注是同一个键。理由：每协议出站根地址是预设的核心字段，models.dev 给不出（见下），magpie 的清单不能整包抄但可当核对底稿。

## 1. 字段对照

| 字段 | magpie 预设（`internal/provider/presets.go`） | models.dev `api.json` | 手写静态清单 |
| --- | --- | --- | --- |
| 每协议出站根地址 | 有：`Chat` / `Responses` / `Anthropic` 三个独立字段（`presets.go:20-27`），另有 `Regions` 切套餐/区域（`:64-90`） | 只有一个 `api`（200/226 有）；协议只有 `npm` 一个提示，无 Responses/Anthropic 分地址 | 想要什么写什么 |
| 协议集 | 隐含：哪个字段非空即支持该协议 | `npm` 推断：185 个 `@ai-sdk/openai-compatible`，仅 8 个 `@ai-sdk/anthropic`；模型级 `provider.npm` 覆盖 354 个模型 | 显式 |
| 模型清单 | 不烤进二进制：`Catalog` 指向 models.dev provider id，`internal/catalog` 读 `magpie sync` 缓存（`catalog.go:1-4,308,337,465`、`:595-596` 注明 "nothing is compiled in"），另有厂商 `/models` 实时拉取（`live.go`）与少数硬编码 `Models`（如 clinepass `:365`） | 有，且最全：`id/name/family/reasoning/tool_call/modalities/limit/cost/release_date` 等 | 手写会过期，不做 |
| 图标 | 有：`Icon` 字段，176 份资产在 `internal/gui/assets/icons` | 无字段；有站点 logo `https://models.dev/logos/{id}.svg`（实测 200），非 API 内容 | 本仓库已有 74 份 cherry-studio MIT 图标，按 host 匹配（`web/src/icons/README.md`、`index.tsx`） |
| 分组 | 有：`Kind` = vendor / relay / local（`:11-17`）；实数 52 条：30 vendor、19 relay、3 local | 无 | 自定 |
| 其他有用字段 | `KeysURL`（拿 key 页）、`Website`、`HeaderHints`、`NoKey`、`Note` | `doc`（文档页）、`env`（环境变量名） | 自定 |
| 更新路径 | 改源码发版；模型走 `magpie sync` 与实时拉取 | 上游 PR 社区维护，更新频繁；本项目发版时快照 | 手改，发版 |
| 许可证 | MIT：阅读零义务，复制代码需保留版权声明与许可全文（`docs/agents/reference-repos.md`）。URL 与事实不受版权，但逐条整表搬入应署来源 | MIT（sst/models.dev），已随包 `internal/pricing/LICENSE-models.dev` | 自有 |
| 规模 | `presets.go` 589 行；catalog 包 2373 行（含测试） | 完整 api.json 5.4MB；本项目裁剪快照 gzip 60KB | 约 40 条 × 一行 |

## 2. 六家实例

magpie 取自 `presets.go`；models.dev 取自实拉 `api.json`。

| 厂商 | magpie 预设 | models.dev |
| --- | --- | --- |
| anthropic | vendor；Anthropic `https://api.anthropic.com`，无 Chat/Responses（`:103`） | `api` 为空；npm `@ai-sdk/anthropic`；17 个模型 |
| openai | vendor；Chat 与 Responses 均 `https://api.openai.com/v1`（`:108`） | `api` 为空；npm `@ai-sdk/openai`；53 个模型，部分模型 `limit` 为 0 |
| deepseek | vendor；Chat `https://api.deepseek.com/v1`、Anthropic `https://api.deepseek.com/anthropic`（`:115`） | `api=https://api.deepseek.com`；openai-compatible；4 个模型，含 cost、limit、`reasoning_options`；**无 Anthropic 地址** |
| moonshotai | 国际 / 国内两条：Chat `…moonshot.ai/v1` / `.cn/v1`，Anthropic `…/anthropic`（`:121-126`），另有 Kimi Code 会员两条 | 国际 `moonshotai`、国内 `moonshotai-cn` 分开，`api` 各为 `/v1`；4 个模型；**无 Anthropic 地址**；无 `kimi-for-coding`，只有 `kimi-code-plan-cn` / `-global` |
| openrouter | relay；Chat `https://openrouter.ai/api/v1`、Anthropic `https://openrouter.ai/api`（`:343`），带 HeaderHints | `api=https://openrouter.ai/api/v1`；npm 为专用 SDK；390 个模型；**无 Anthropic 地址** |
| siliconflow | relay；Chat `https://api.siliconflow.cn/v1`（`:398`），仅 Chat | `siliconflow`（`.com/v1`，57 个模型）与 `siliconflow-cn`（`.cn/v1`，44 个）分开；`doc` 两条都指向 `.com` |

## 3. 缺口

1. **中文厂商的 Anthropic 协议地址，models.dev 基本不知道。** 全库 `api` 含 `anthropic` 的只有 minimax 系与 thinkingmachines 共 5 个；DeepSeek、Kimi、智谱 GLM、StepFun、小米 MiMo 等在 models.dev 里都只有 OpenAI 兼容地址。这些地址（如 `https://api.deepseek.com/anthropic`、`https://open.bigmodel.cn/api/anthropic`）只有 magpie 清单或厂商文档知道，必须手写。
2. **models.dev 的 `api` 是 AI SDK 的 baseURL 约定，不等于可直接填的网关出站根。** minimax 的 Anthropic 条目为 `…/anthropic/v1`，而 magpie 填 `…/anthropic`（`presets.go:156`）；Anthropic 协议客户端会自己追加 `/v1/messages`，直接用会拼出 `/v1/v1`。deepseek 的 `api` 不带 `/v1`，magpie 带。取值前须人工核对，不可机器直通。
3. **Responses 协议地址：models.dev 完全没有。** magpie 对 openai、deepseek、xai、modelscope、opencode 等标了 Responses，来源是各家文档，本调研未逐家验证真伪，落实现前逐条验。
4. **套餐 / 区域分地址**：智谱、Z.ai、StepFun、小米、百度千帆的 Coding Plan 与按量付费是不同地址，且 key 互不通用（`presets.go:140-147,161-162,177-189`）。models.dev 用独立 provider id 表达（`zhipuai-coding-plan`、`zai-coding-plan` 等），没有「同一厂商多套餐」的结构。预设需要自己的 `plans` 概念，或每个套餐一条预设。
5. **models.dev 缺失的厂商**：本次实测无 `baidu`、`tencent`（有 `tencent-*` 子套餐）、`kimi-for-coding`。`api` 为空的 26 个 provider 里有 anthropic、openai、google、xai、azure、bedrock 等主流方，其地址只能手写。
6. **local 组**：地图已划出范围，不做。

## 4. 与现有机制的衔接

现状：`internal/pricing` 内嵌裁剪快照（`pricing.go:25-28`，`//go:embed snapshot.json.gz`），生成器 `internal/pricing/gen/main.go:10` 说明发版时 `make update-models-snapshot` 重拉；只留 provider id / name 与每模型四价，其余字段 "多留一个字段就是白背一份会过期的数据"（`gen/main.go:4-8`）。渠道上的 provider 标注取值对齐 models.dev provider id，只服务建议价与图标分组、不参与路由（CONTEXT.md「provider 标注」）；批量填价按点击时刻算一次、不保持同步（CONTEXT.md「批量填价」）。

衔接建议：

- 预设条目携带 `models_dev: "<provider id>"`。选预设建渠道时，把它写进渠道的 provider 标注，建议价与批量填价无需任何新代码。
- 预设里的模型候选（「加模型」交互的列表）由快照按该 id 派生。这需要把 `gen` 裁剪逻辑**放宽**：现在只留有价模型，且只留四价；要补 `name`、`limit.context`、`modalities.input`（#179 的能力胶囊如果要）。每多留一个字段就多一份会过期的数据，按 #179 最终要哪些胶囊再定，不预留。体积影响很小：全量才 5.4MB 未压缩，现快照 60KB。
- 同一厂商国内外分 id（`moonshotai` / `moonshotai-cn`、`siliconflow` / `siliconflow-cn`）：预设按地区拆成两条各挂一个 id，与 magpie 一致。
- 图标沿用现有 `web/src/icons` 按 host 匹配；预设可直接显式带 `icon` 键，省去 host 猜测。图标不从 models.dev 抓（运行时联网、也不是 API 内容）。
- 套餐类（缺口 4）：预设里允许一条厂商带多个 `plans`，每个 plan 一组三协议地址和一个 `models_dev` id（如智谱：`zhipuai` / `zhipuai-coding-plan`）。

## 5. 更新方式比较

| 方式 | 评价 |
| --- | --- |
| 烤进二进制（推荐） | 与现有快照同一条路：单二进制自洽、离线可用、构建确定。代价：新模型最长滞后一个发版周期。个人自用、PO 即开发者，可以接受；模型清单是 "建议"，用户随时可手填 |
| 启动拉取 models.dev | 违背 "单二进制自洽"、外网受限的服务端易失败，需要超时/缓存/降级三套路径；且 `api` 字段不能直通（缺口 2），拉来也不能自动用于地址。拒绝 |
| 手动同步 | 本方案里手写清单就是手动同步；不要再叠一层脚本。可选：加一个测试，断言每条预设的 `models_dev` id 在快照里存在，快照更新后能立刻发现 id 改名或下线 |

## 6. 建议的数据形态（供 #181 起拆票参考，非裁决）

手写清单每条字段：`id`、`name`、`group`（vendor / relay）、`icon`、`models_dev`、`protocols`（每协议一个出站根，空 = 不支持）、`keys_url`、`note`、可选 `plans`。落地为仓库内一个 Go 或 JSON 文件；magpie 的 `presets.go` 只当核对底稿，逐条 URL 对厂商文档确认，不整表复制（若复制则保留 MIT 版权声明）。初版约 30 条（厂商 20 + 中转 10）足够，随用随加。

## 待 PO 裁决

1. 预设清单形态：Go 源码常量 vs 内嵌 JSON（建议 JSON 嵌入，便于只改数据；放在 `internal/pricing` 旁还是独立包，实现票再定）。
2. `gen` 是否放宽到留模型清单（取决于 #179 对能力胶囊的结论）。
3. 套餐用 `plans` 嵌套，还是每套餐一条预设（建议 `plans`，key 不通用的事实在界面上要说清）。
4. 初版收哪些厂商：建议以 magpie 前 20 家 vendor 里本项目用户常用的为准，PO 点名。

## 来源

- magpie：`internal/provider/presets.go`（`:11-17,20-90,103-126,137-189,343,398`）、`internal/catalog/catalog.go`（`:1-4,308,337,465,595-596`）、`internal/gui/assets/icons`（176 份）。
- models.dev：<https://models.dev/api.json>（2026-10-08 实拉，5.4MB）、<https://models.dev/logos/deepseek.svg>。
- 本仓库：`internal/pricing/pricing.go:1-28`、`internal/pricing/gen/main.go:1-26`、`internal/pricing/LICENSE-models.dev`、`Makefile` `update-models-snapshot`、`internal/admin/admin.go:232-236`、`web/src/icons/README.md`、`CONTEXT.md`「批量填价」「provider 标注」、`docs/agents/reference-repos.md`。
