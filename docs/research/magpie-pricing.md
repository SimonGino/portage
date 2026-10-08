# 调研：magpie 的模型价格配置与费用口径

> wayfinder [#183](https://github.com/SimonGino/portage/issues/183)（地图 #177）。读法：magpie（yetone/magpie，MIT，本机 `~/Code/GitHub/magpie`，HEAD `40c5b92a`）只读参考，阅读借鉴零义务。
> 下文 `catalog.go:99` 等一律指 magpie 的 `internal/` 下文件（`gui/assets/app.js` 简写 `app.js`）；本项目侧写明路径。
> 结论先行：**magpie 的价格口径是「查询时现算」（改价、改系数立即重算全部历史），本项目是「落库时点计价」（改价不追溯）——两套口径对立，价格层级与系数、Restore default、目录价兜底计费都不能整套搬。值得借的只有两件有真实计价误差的事：长上下文分档、Anthropic 1h 缓存写价；其余的 UI 手法已有等价物或与口径冲突。**

## 1. 数据层级（一次调用最终按哪层价算）

`provider.EffectivePriceIn`（`provider/models.go:934-969`）的查找序，先中先用：

| 序 | 层 | 来源 | 受供应商「价格系数」影响 |
| --- | --- | --- | --- |
| 1 | 用户逐模型价 `<provider>/<model>` | settings `modelPrices` | 否 |
| 2 | 用户供应商通配价 `<provider>/*` | 同上 | 否 |
| 3 | 用户跨供应商价 `*/<model>`（小写） | 同上；也服务「供应商已删」的老用量 | 否 |
| 4 | 供应商目录价 `Provider.ListPrice`（该供应商对应的 models.dev id） | models.dev 缓存 | **是** |
| 5 | 制造商目录价 `MakerPrice`（presets 里造这个模型的厂商） | models.dev 缓存 | **是** |
| — | 都没有 | `ok=false`，调用记为 unpriced | — |

要点（均为源码事实）：

- 第 1~3 层命中即**原样返回**，不乘系数（`models.go:948-955` 在 `PriceRate` 之前 return；UI 提示也这么写：`app.js:9278`、`app.js:6842`「a price you set for a model stays as set」）。只有 4/5 层乘 `PriceRate`（`models.go:965-967`，`Price.Times` 连同所有分档一起乘，`catalog.go:199-205`）。
- 第 4/5 层：订阅账号（Codex 的 ChatGPT 登录、Copilot）与没有 models.dev id 的中转，用制造商价兜底（`models.go:884-903` 注释）；Cline/Kilo 的免费模型直接返回**零价且 ok**（`models.go:890-893`）。
- 目录**没有编进二进制的兜底表**：`catalog.Provider` 注释「nothing is compiled in」（`catalog.go:596`），`load()` 只读 `magpie sync` 写的缓存和 opencode 缓存（`catalog.go:355-361`）。刷新靠运行时后台任务：`KeepFresh` 每小时、`Missing()` 在遇到无价模型且缓存超 6 小时时补拉（`catalog/fresh.go:34-61`）。ticket 里「内置 fallback」在价格这条线上不成立。
- 长上下文分档在**每一层都可带**：目录价的 `tiers`（models.dev 的 `tier.type=context`，`catalog.go:134-178`）和用户价的 `tiers`（`settings.go:448-450`）。
- `OneHourFor`：Claude 模型缓存写有价而 1h 缓存写未给时补 `2×input`（`catalog.go:186-196`），目录加载与用户价读取时都调（`catalog.go:380`、`models.go:952`）。

## 2. 存储形态

- **落盘的只有「用户的话」**：settings 里 `modelPrices: map[string]ModelPrice`，key 见上表 1~3 层（`settings.go:382-390`）；供应商 `priceRate: float64`，0=不设，0~1000、至多三位小数（`provider.go:145-150`、`models.go:973-981`）。
- `ModelPrice` 每个价是 `*float64`（`settings.go:439-451`）：input/output/cache_read/cache_write 必须**四项齐全**才算有效价（`settings.go:463-469`，缺一项 `Price()` 返回错误，读时跳过该条，`models.go:950`）；`cache_write_1h` 与 `tiers[]` 可选，tier 的 `above` 须严格递增（`settings.go:474-479`）。
- **算出来、不落盘**：目录价（models.dev 缓存）、系数后的有效价、每次调用的成本。用量日志 `usage.jsonl` 的 `Record` 只存 token（`in/out/cache_read/cache_write/cache_write_1h/reasoning`，`usage/usage.go:59-69`），**没有 cost 字段**。
- 写入口 `SetModelPrice`：先校验 key 的模型确属该供应商（`modelprefs.go:138`），`nil` 价=删除（回到目录价），**价为 0 是「免费」不是「清除」**（`modelprefs.go:109-113`）；`DropModelPrice` 不要求供应商还在（价比供应商活得久，`modelprefs.go:201-229`）。

## 3. 费用公式

`Price.CostSplit`（`catalog.go:246-252`）：

```
f    = p.At(input + cacheRead + cacheWrite)        // 整个请求按「输入总量」所达最高档计，严格大于 above 才升档（catalog.go:217-225）
cost = ( input·f.Input + output·f.Output + cacheRead·f.CacheRead
       + (cacheWrite − cacheWrite1h)·f.CacheWrite + cacheWrite1h·f.OneHour() ) / 1e6
```

- `Record.Input` 是**非缓存输入**（`budget.go:11` 注释「uncached input」），三项输入加总才是分档用的 prompt 量；本项目 `Prices.CostUSD` 收毛值再减缓存（`internal/calllog/cost.go:32-38`），两边等价。
- **推理 token**：「已含在 output 里，按 output 价计」（`catalog.go:236-238`）；`Reasoning` 字段只展示，不进公式。与本项目「reasoning 是 output 明细，不进这里」一致（`cost.go:29`）。
- `cache_write_1h` 为 0 时按 5 分钟价计（`catalog.go:229-234`）；`cacheWrite1h` 被夹到 `[0, cacheWrite]`（`catalog.go:247`）。
- 汇总：`Totals.add` 对 `Input+Output==0` 的记录不计价，价缺失计 `Unpriced++` 且**不加钱**（`usage.go:409-416`）。UI 在「≈$ effective prices」旁挂「N calls had no known price and are not counted」（`app.js:15356`）。
- **每次读都重算**：`pricer()` 每次查询读一遍 settings，按 `(provider, model)` 缓存（`usage/ledger.go:405-423`）；`NewPricer` 注释明说「make it again on the next read so a changed tariff re-prices history」（`ledger.go:425-428`）。改价/改系数即时改写全部历史总额。
- 小不一致：会话统计走 `p.At(0).CostSplit`，即**不看分档**（`sessions/sessions.go:988`、`sessions/stats.go:239,312`）；用量页走分档。

## 4. 订阅账号与「免费」

- **订阅账号没有单价输入**。其「≈$」是**等价 API 价**：`ListPrice` 先查该供应商的 models.dev，再回落制造商价（`models.go:884-903`），所以 ChatGPT 订阅用 gpt 的官方价算出一个「如果走 API 要花多少」。真实订阅余量另走额度卡（`SubscriptionQuota`，`provider/quota_last.go`），与费用口径无关。
- 订阅模型的 `Rate`（Qoder `price_factor`、WorkBuddy `"credits":"x0.03"`，积分倍率）只做 chip 上的 rate tag 展示，**未进 `EffectivePriceIn`**（`catalog.go:74-80`；用量与价格代码中无引用）。
- **`free` 徽章判据**（`app.js:9113`）：`m.free || namedFree(id, name)`。`m.free` 来自厂商列表的显式标记（WorkBuddy `x0.00`、Cline/Kilo 的列表，`catalog.go:71-73`、`provider/cline.go:142-175`、`kilo.go:163-175`）；`namedFree` 是 id 或名称里有独立单词 `free` 的正则（`app.js:3892-3894`，覆盖 OpenRouter `:free`、OpenCode Zen `-free`）。**与价格无关**：用户把价设成 0 不出 `free` 徽章，反之 `free` 徽章也不改价（只有 Cline/Kilo 在 `ListPrice` 里直接给零价，`models.go:890`）。
- **「Free only」**（`app.js:9497-9507`）：仅当列表同时有免费与付费时出现；点击把勾选集替换为「免费且符合当前过滤框」的模型，是勾选捷径，不是价格筛选。

## 5. 网关 key 的成本预算用哪层价

- `access.Key.Limit{Period day|week|month, Tokens, Cost, CacheReads}`（`access/limit.go:16-21`，`access.go:117`）；窗口是**本地时区自然日/周（周一起）/月**（`limit.go:56-69`）。
- **结算价 = 用量页的有效价**：`budget.read` 用 `usage.NewCoster()` 把窗口内该 key 的 `usage.jsonl` 逐条重算后求和（`budget/budget.go:104-114`；`ledger.go:774-784`），所以包含用户逐模型价、系数、分档、1h 缓存写；缓存读**始终计入成本**，`CacheReads` 开关只决定 token 上限是否把缓存读计入（`budget.go:60-66`）。无价调用不加钱（`budget.go:11-12`）。
- 预扣用另一把简化尺：在途请求按「body 字节/4 + 本窗口平均输出」估 token，成本取「窗口均价（已花/已用 token）」，没有历史才退到**仅 input 价**（`budget.go:256-266,286-298`）。所以并发可超支约一次调用。
- 重启不丢：窗口总额每次从日志重读（`budget.go:94-102`）。

## 6. UI 流程（逐步；截图 `docs/research/magpie-web-survey/f2-3-names.png` 在 `research/magpie-web-survey` 分支）

1. 供应商编辑器 → 模型区：chip 列表（名称 / `free` 徽章 / rate tag / 上下文 tag，`app.js:9108-9122`）；供应商级字段「Price rate」是一个文本框，占位「1, the official price」，提示「Costs are counted at the official price times this… a price you set for a model stays as set」（`app.js:6837-6856`）。
2. 点某个 chip 的菜单进「Names & levels」子表单；其中「Price, $ / 1M tokens」一组框：Input / Output / Cache read / Cache write / Cache write 1h（后者仅 Claude 模型或已有值时才显示，`app.js:9300`），下方「Long-context price, over [N]」一行（目录价有分档时直接展示，否则藏在「Long-context price」链接后，`app.js:9306-9332`）。
3. 空框显示**目录价作灰色占位符**（`app.js:9296,9318`，占位是系数前的目录价，`providers.go:49-51` 明说「before the provider's price rate」）；框里空着的部分取目录价，**全部空 = 清回目录价**（`app.js:9347-9351`）。1h 缓存写空 = 2×input（`app.js:9296,9299`）。
4. 失焦/回车触发 `takePrice`（`app.js:9341-9391`）：缺了的价取目录价补齐；模型无目录价时记下已输的格，等 input/output 都给了才成立（`app.js:9356-9365`）；与已存价相同则不记；分档价空格取目录分档或本价。
5. 修改**只进草稿**（`prefs[id]`、`draft.priceTyped`），行内出现「unsaved」小标，tooltip「Made when the provider is saved; Cancel drops it」（`app.js:9444-9445`）；整个供应商点 Save 才一起提交（`modelPrefs`，`providers.go:858-861,1165`）。
6. 「Restore default」把该模型的名称、推理档、图片、协议、同款、**价格**一并清回默认；价格这项是写 `ownPrice=true`，保存时转为 `SetModelPrice(nil)` 即删除 key（`app.js:9446-9464`；`modelprefs.go:485-491`）。
7. 用量页右上角「≈$X effective prices」就是第 3 节的总和（`app.js:13562`），顶端一行 scope note 说明只含网关调用。

## 7. 字段对照：magpie vs 本项目

| 维度 | magpie | portage（口径层 §2.10 / CONTEXT） |
| --- | --- | --- |
| 价格归属 | settings map，key = `<供应商>/<模型>`、`<供应商>/*`、`*/<模型>` | **纳管模型条目**上 4 列 `price_input/output/cache_read/cache_write`（`internal/store/admin.go:107`、`:226`），不挂渠道（CONTEXT「单价」_Avoid_ 渠道价） |
| 价格项 | input / output / cache_read / cache_write / **cache_write_1h** / **tiers[]**（每档 5 价） | 仅四价 |
| 「没有价」 | 无价=unpriced，调用**不计钱**，用量页提示未计入条数 | `NULL`=未定价，与 `0`=真免费两态；未定价**记 0 + 管理端 `tag-warn` 提醒**（四价全 NULL 且有用量，DESIGN §5.1） |
| 四价完整性 | 用户价必须四项齐全才生效，UI 用目录价补齐 | 四项各自可 NULL；单项 NULL 按 0 计（`cost.go:32-44`） |
| 目录价 | models.dev，**运行时**同步缓存（每小时）；不进二进制 | models.dev **构建期**裁剪快照 embed（`internal/pricing`），不做运行时刷新（口径层 v1.10 ④） |
| 目录价的角色 | **直接参与计费**（第 4/5 层兜底） | **只做建议**，点「采纳」才落库，不自动参与计价（`pricing.go` 包注释） |
| 目录对应键 | 预设自带 models.dev id；制造商兜底 | 渠道「provider 标注」（可选，仅服务建议价与图标分组） |
| 折扣中转 | 供应商级**持久系数** `priceRate`，乘目录价（不乘用户价） | **一次性批量填价**：建议价 × 系数，落成普通四价，系数不落库（口径层 v1.10，`web/src/pages/channels/detail.tsx:596`） |
| 计价时点 | **查询时现算**，改价/改系数追溯全部历史 | **落库时点**，改价不追溯（CONTEXT「单价」） |
| 成本落盘 | 否（只存 token） | 是（流水 `cost`，NULL=无用量可计） |
| 输入口径 | `Input` 已是非缓存输入 | 流水 `input` 是毛值，计价时减缓存读写并 clamp 到 0（`cost.go:32-38`） |
| 推理 token | 含在 output 内，不另计 | 同 |
| 分档 | 整请求按输入总量所达档计 | 无 |
| 订阅账号 | 无单价输入；用制造商等价 API 价估 ≈$；另有额度卡 | 无订阅概念（地图 #177 Out of scope） |
| `free` | 厂商标记或名字含 `free` 的 chip 徽章 + 「Free only」勾选捷径，与价无关 | 无徽章；`0` = 真免费，纯价格语义 |
| 预算/配额 | **按 key**：token 与成本双上限，日/周/月（本地自然窗），用有效价重算；估算预扣 | **按用户**月度 USD（UTC 自然月），累计 `SUM(cost)` ≥ 限额即 429，不预扣（`internal/server/quota.go`）；CONTEXT「配额」_Avoid_ 按 Key 配额 |
| 编辑 UI | 供应商表单内**暂存草稿** + 一次 Save；「Restore default」按模型回退 | 胶囊三态**失焦即存**，空=清回 NULL；定价页与详情页共用 `modelprices.tsx` |
| 总表 | 无 | 定价页总表（`web/src/pages/Pricing.tsx`） |

## 8. 差异清单（按对本项目意义排序）

1. **计价时点相反**：magpie 查询时重算、可改写历史；portage 落库后定死。后者是 PO 的明确口径（v1.02/v1.10），直接决定了「持久系数」「目录价兜底」在这里都不成立（系数改一次历史总额变，正是 v1.10 否掉持久系数的理由）。
2. **目录价地位**：magpie 里目录价是计费层；portage 里只是建议。前者让「未配置也有个大概的钱」，后者让「未定价」可见可催。
3. **magpie 价只有一张全局表**，portage 价在条目上、同名模型跨渠道各记各的价（口径层 §2.10 末条）。
4. **分档与 1h 缓存写**：magpie 有，portage 没有——这是唯一真实造成**计价误差**的差异。
5. magpie 的系数**不作用于用户价**，且 UI 占位符显示系数前的目录价（`providers.go:49-51`），使「系数 0.8 + 空框」看到的灰字与实际计费价不一致；会话统计不看分档（`sessions.go:988`）。这是其设计内的口子，借鉴时不应复制。
6. 订阅等价价、rate tag、`free` 徽章、Free only：服务于「订阅/积分」生态，portage 无对应对象。
7. 预算维度（按 key vs 按用户）属于配额口径，不在价格层，与本票无关。

## 9. 建议：借什么、不借什么

**借（均不动「落库时点计价」「NULL ≠ 0」两条口径）**

1. **长上下文分档（建议借，另立票）**。理由：这是真实误差源——OpenAI 超 272K、Anthropic 部分模型超 200K 整请求升价，当前四价对大上下文请求系统性低估，而配额是按钱记账的。落法：条目加可选一档 `price_tier_above` + 四价（或一列 JSON），`Prices.CostUSD` 里按「毛输入」判档（本项目毛 input 已在手，不必像 magpie 要三项求和）；严格大于才升档、整请求按该档计、与 magpie 同。models.dev 快照需放宽 `internal/pricing/gen` 的裁剪（保留 `tiers`），与 #178 已指出的「放宽裁剪」是同一处改动，可合并。批量填价顺带乘分档。优先级：中；个人自用若几乎不发 >200K 请求可再推后，但数据结构要预留。
2. **`cache_write_1h`（建议借，依赖 codec，晚于分档）**。理由：Anthropic 1h 写价是 2×input，5 分钟是 1.25×，Claude Code 已在用 1h TTL，混算会偏。前置条件是 Anthropic 响应解析要拿到 `cache_creation.ephemeral_1h_input_tokens`、流水加一列，这是 codec 与流水迁移的事，不是定价页的事——先单独核对上游字段再决定，不为价格页独占一票。magpie 的缺省规则（Claude 且有 5 分钟写价时 1h 缺省 = 2×input，其余模型不给 1h 框）可直接抄为「采纳建议」时的补全规则，但不要补进落库（缺省补出来的数是我们替 PO 做主，违背「建议不自动落库」）。
3. **未计价调用的就地提示（核对后可能零成本）**：magpie 在合计旁挂「N 条调用无已知价、未计入」。本项目已有 `tag-warn` 提醒挂条目；若排行页费用合计没有对应脚注，加一句即可。仅属核对项，不立票。

**不借（含理由）**

1. **持久 `priceRate` 系数**：与落库时点计价冲突（改系数追不追溯会长出第二套口径，v1.10 已否）；且 magpie 自己的实现有「不乘用户价、占位符显示系数前价」的口子（差异 5）。批量填价已覆盖「折扣中转」用例。
2. **目录价直接参与计费（第 4/5 层兜底）**：会让「未定价」消失，`tag-warn`、「NULL ≠ 0」「采纳才落库」整套失效；快照又是发版时点的世面价，网关记账不该依赖它（`pricing.go` 包注释的既有立论）。
3. **查询时重算历史**：流水 `cost` 已落盘，且配额 `SUM(cost)` 靠这个才不需要每次扫日志重算。
4. **暂存草稿 + 「Restore default」+ 空框回落目录价**：magpie 需要它是因为「保存」绑在整个供应商表单上，且空框有计费语义（回落目录价）。本项目胶囊失焦即存、空 = NULL 清回未定价，「Restore default」没有可回的默认；灰色占位符的等价物已经是 `chip-suggest` 的建议价对照与「采纳」，沿用即可。
5. **`free` 徽章 / 「Free only」 / rate tag / 订阅等价价**：对象是订阅与积分制，本项目无此概念（#177 Out of scope）；`free` 的名字正则判据还会误标，且与价格脱钩，不适合做「真免费」判据——portage 里「真免费 = 价 0」更干净。
6. **按 key 的成本预算**：属配额口径（CONTEXT 明写不做按 Key 配额），不是定价借鉴项。其「结算价与用量页同一把尺」的做法本项目天然满足（都读流水 `cost`）。

## 10. 未验证 / 注意

- 未实跑 magpie 页面，UI 步骤来自 `app.js` 源码与已有调研截图；`Rate` 未进计费是据 `grep` 无引用推断。
- `Record.Input` 的「非缓存」口径取自 `budget.go:11` 的注释与 `CostSplit` 的三项求和写法，未逐个核对各 gateway 路径的 token 归一化。
- 本项目侧的分档所需改动（快照裁剪、流水迁移）只做方向判断，未估工作量。
