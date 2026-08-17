# 个人 AI 模型网关 MVP 设计草案

> 状态：草案 v0.85
> v0.85 变更（口径层 v0.81 落地：全局令牌桶拆两只，`count_tokens` 独占一只，[#16](https://github.com/SimonGino/portage/issues/16)，2026-08-17）：①**§7.2 那条「一只桶、四条转发路由共用」改写**：生成面三个端点共用 `s.lim`，`count_tokens` 用 `s.countTokensLim`，选桶收在新增的 `pickLimiter(ep)`。两只桶都在 `New()` 里造、都用同一份 `rate_limit_qps`/`rate_limit_burst`——配置面零增长，`写 0 即关闭` 跟着走（`newLimiter` 一行未改）。②**选桶在 `rateLimit` 返回闭包之前算一次**，不是每个请求判一遍：端点在路由注册时就定死了。判据是**端点**不是入口协议——`count_tokens` 与 `/v1/messages` 的 `ep.Proto` 同为 anthropic，按协议判会把 `/v1/messages` 一起分过去（同 `conversionOpen` 踩过的坑）。③**哨兵用例改名并换第二个端点**：`TestRateLimitBucketIsSharedAcrossEndpoints` → `…AcrossGenerationEndpoints`，第二个请求由 `count_tokens` 改 `/v1/chat/completions`——原用例钉的正是本票要拆开的那个不变量，留着它等于让哨兵反向站岗。它的 model 没纳管无妨：限流在路由之前，压根解析不到模型（真机 429 行 `model_requested` 为空正是这个）。④**新增两条**：`TestCountTokensStormDoesNotStarveMessages`（30 连打 `count_tokens` 后 `/v1/messages` 不被 429，验收 #16 第一条）、`TestCountTokensIsStillRateLimited`（它自己那只桶照样限，含 `Retry-After`）。⑤与 v0.83 那条（`count_tokens` 改本地估算）独立：改回 200 之后它仍然占桶。⑥**两轴评审的四处修**：a. `s.lim` 改名 `s.genLim`——它现在只服务生成面，而兄弟字段是全名 `countTokensLim`，不改的话选桶那两行要靠注释才读得懂；b. 哨兵补 `/v1/responses`（原来只点 `/v1/chat/completions`，将来谁给 responses 单开一只桶会漏过去），`TestRateLimitZeroQPSDisablesIt` 补 `count_tokens` 那只桶的见证（结构上共用 `newLimiter` 必然一起关，但「配 0 即关闭」是配置项对外的承诺，两只都要有人看着）；c. 风暴用例 `burst` 由 2 改 1，去掉对「30 连打跑得比 1 秒快」的时钟依赖（改后按删 `pickLimiter` 的 `count_tokens` 分支实测，用例照红）；d. **三处「单个全局令牌桶」的陈述同步**——本文档 §11.3 的 nginx 段、`internal/config/config.go` 的 `RateLimitQPS` 注释、`CONTEXT.md` 术语表（新立**全局 QPS 桶**词条，桶数与生成面口径写进去；`渠道并发上限` 那条的交叉引用随之加粗对齐）。改口径不改这些的话，读者从任一处进来看到的都还是被推翻的那句。修改人 jinpenga。
> v0.84 变更（[#15](https://github.com/SimonGino/portage/issues/15) 跨协议丢弃日志报幻影 `vendor_request`，2026-08-17）：口径不变，修的是日志说假话。①**根因**：CC / Responses 两个出口都先把 `metadata` 单独登记成 `DropMetadata`，紧接着的 `Extras` 循环又没排除它，同一个键再落进 `else` 记一次 `DropVendorRequest`。丢的东西没错，错的是 WARN 在说客户端发了个不存在的未知顶层字段——§2.6 立这条日志的目的是「不做伪映射也不静默」，明处报幻影等于把它的可信度打掉：真来一个认不得的新字段时，没人分得清是新字段还是这个已知的假阳。②**分档规则上提 `protocol.ClassifyExtrasKey`**，`metadata` / `thinking_param` / `vendor_request` 三档收一份，三个出口共用（`DropXxx` 常量照旧各包一份）。不是只在两处 `if` 里加个 `continue`：那样第三个出口（anthropic）的循环形状仍与另两个不同，下一个「先在循环外单独登记、循环里忘了排除」的键就是本票的重演——这正是 v0.82 ⑨a 收口那句「惯例覆盖的是名字、不是逻辑表，这条以后照此办理」。③**回归位落 `internal/protocol/extras_drop_test.go`**（`protocol_test` 包，形制照 `effort_test.go`）：三种 Extras 组合 × 三个出口，断言的是**完整** dropped 集合而非「包含」——多记一条幻影正是要防的那件事；各 codec 包里镜像一份就是被钉的东西自己的失败模式。④**评审规格轴订正票面一处依据**：#15 说 A 出口没这个问题、理由是「CC / Responses 入口本来就没有 metadata 这个字段」——结论对（确实不重复登记），括号里的依据不对。两个 OpenAI 协议顶层都有 `metadata`，两边 `topLevelKnown` 都没写它，所以 CC→A / R→A 上它会落进 Extras 被 A 出口记成 `vendor_request`：不是重复登记，是**错档**（Anthropic 原生认这个字段）。与本票同源不同形，口径待裁，另立 [#19](https://github.com/SimonGino/portage/issues/19)；现状已被 ③ 那张表的 anthropic 行钉住，改口径时它会红，是有意的。**PO 于 2026-08-17 裁定认 ② 这笔「超票面」改动并保留**（票面只写「两处同改」；依据是 v0.82 ⑨a 那条 standing ruling）。修改人 jinpenga。
> v0.83 变更（口径层 v0.80 落地：非 Anthropic 出口的 `count_tokens` 由 501 改本地估算，2026-08-17）：**只改文档，`internal/` 一行未动**——实现在 [#18](https://github.com/SimonGino/portage/issues/18)。①**§2 有损转换策略那条改写**：原文「P0 上游非 Anthropic 时回 501 风格错误；P1 做字符估算」的 P1 提上来，并补上口径层从来没写过的承诺边界——不承诺与上游一致、不作对账依据、不进计费。②**§2 `conversionOpen` 那段与 §8 端点清单同步**：「没有上游对应端点」不再等同于「回 501」，CC / Responses 出口换成一条本地路，闸的形状要跟着改；判据仍是按端点分流，这一条没变。三处都显式标注「代码尚未落地，在那之前 501 是现状」，免得读文档的人以为已经能用。③依据是 [#3](https://github.com/SimonGino/portage/issues/3) 的 2026-08-17 A→CC 真机轮：这版 Claude Code 每轮都打 `count_tokens`、开场连打二十余次，501 不阻塞启动但让客户端的压缩判断退化，且冲空限流桶误伤真实请求（[#16](https://github.com/SimonGino/portage/issues/16)）。估算算法（分词器选型、system/tools 的序列化口径、附件折算）属实现层，随 #18 落地时补进本文档。修改人 jinpenga。
> v0.82 变更（口径层 v0.62 / v0.65 / v0.72 / v0.73 落地：thinking 出向合成 + 请求侧 effort 直传，六格全通，[#4](https://github.com/SimonGino/portage/issues/4)，2026-08-17）：口径不变，这一批把两条早已裁定却一直只在文档里的口径写成代码，并**销掉 §9.2 与 §9.4 各自置顶的那条「用户可感知的退化」**。①**丢弃条件下沉到通道判别**：三个出口此前是整类 `EvThinkingDelta` 一刀切丢，现在由 `protocol.OutboundThinkingText` 单点判定（签名通道与空文本不写），流式与非流式六个半边共用它——这是 v0.62 ①「一台状态机」在本仓的落法（new-api 的流式/非流式不对称是 bug，不抄）。②**补齐两个此前不存在的解码半边**：CC 的 `reasoning_content`（流式非流式同名同义，共用 `streamState.body`）与 Responses 的 `reasoning_summary_text.delta`（外加 `reasoning_text.delta` 与非流式 `summary[]`）。**只认 `.delta`**：同一段摘要在上游流里出现三遍（`.delta` / `.done` 的 `text` / `output_item.done` 的 `summary[]`），三处都收就发三遍。同批推翻 `openairesponses/decode_response.go` 头部那条 v0.10 注释（「reasoning 一律不产事件」）与 `encode.go` 那条「待真实转录」的丢弃理由——后者早就过期，`responses-stream-reasoning-turn1` 把生命周期录全了。③**Responses 出口的 `reasoning_summary_part.added` 不是装饰**：Codex 侧靠它立起 `summary[0]` 槽位，缺了它后续 delta 索引到不存在的 part（同正文侧 `content_part.added` 那个坑）；合成的 item **不写 `encrypted_content`**——我们手里没有封装，空串等于声称「有一个空封装」。帧序与这两条都照 opencodex `src/bridge.ts`（Codex 兼容首要参考）核过，sub2api 省了 part 帧，不跟。④**`Effort` 提成 canonical 一等字段**（理由同 `Image.Detail`：`Extras` 永不外带），三入口读三出口写、六条路全开；`LiftNestedEffort` **提完即删**（父键掏空则连键一起删）——不删的话出口会为一个其实转发出去了的键登记 `vendor_request`，账本说假话。A 出口**只写 `output_config.effort`**，不写 `thinking`（写它就是替客户端开思考，v0.65 ④）；域外值不钳（v0.65 ⑤）。⑤**新增登记档 `thinking_param`**，与 `DropThinking`（内容块）、`DropVendorRequest`（其余顶层字段）三分；三个出口的 Extras 处理从「有住户就笼统记一笔 vendor_request」改成**按档分类**，否则「客户端点了思考被丢掉」这件事在日志里看不见。⑥**`anthropic/encode_request.go` 补上 `DropThinking` 登记**并重写那条「这里不登记」的注释：它原本的理由（R→A 来的 thinking 块正文恒空，丢个空块不值得报警）在出口开始合成之后不成立了——客户端会带着文本回带，还会补一个 `signature:""`，Anthropic 见空签名直接 400（#94）。⑦**两份 Anthropic 上游 thinking 转录入库**（`anthropic-stream-thinking` / `anthropic-thinking-high`）。**它们的 `thinking` 正文整段为空、真内容只有那串 1 KB 签名**——票里写的「thinking_delta + signature_delta 齐」与实采不符，所以「A 源侧带正文的真机转录」这个缺口**没有**被这两份样本销掉，仍记在 §9.6；它们钉住的是「effort-only → 空正文 + 有签名」与「签名不许外漏」。样本 `expect` 独立重算；上游报的 `output_tokens_details.thinking_tokens`（249 / 310）Tap 目前不解，`expect` 按当前实现记 0/false 并在 `source` 里写明 v0.66 那批落地时要改——**这个数同时订正了口径层 v0.66 ③ 的一个前提**（它判 Anthropic 协议没有思考 token 那一格，依据是参考实现的旁证而非自己的字节），收敛见口径层 v0.79 与其 §5 条目 9。⑧三条既有断言按口径改判而非放松：`convert_a2r_test.go` 的禁漏表只摘掉 `thinking_delta`（`encrypted_content` / `gAAAAAB` / `reasoning` 全留），`convert_r2a_test.go` 那条从「推理必须丢」改成「推理必须合成且签名不漏」，`openairesponses` 那条 `…ReasoningSummaryNeverLeaks` 改成 `…BecomesThinking` 并加「恰好一条」。⑨**代码评审的三处修**（两轴并行评审，两轴各自的结论都收在这里）：**a. 一个真洞**——`thinkingParamKeys` 当初按 `DropXxx` 常量的惯例镜像了三份，而 `output_config` **三份里一份都没写**，于是「`output_config.effort` 解不出档位（非字符串 / 空串）」这个形态在**三个**出口（含 A 出口自己）记的都是 `vendor_request`，正是 `thinking_param` 这一档要防的那件事，也与 `LiftNestedEffort` 注释自己的承诺相反。表上提到 `protocol.IsThinkingParamKey`（**只留一份**，`DropXxx` 常量照旧各包一份——那是名字不是规则），补进 `output_config`，加回归用例。镜像三份的代价在这个洞上是**补一次要改三处**（漏一处就还是同一个洞），不是漂移；漂移只现在注释上（anthropic 那份带逐键注释、另两份没有）。惯例覆盖的是名字、不是逻辑表，这条以后照此办理。**b. CC↔R 两格补整链用例**（此前只有 codec 层覆盖，另四格都有），见 §9.6 用例分工表。**c. 两处名字与注释**：`openairesponses` 的 `closeBlocks` 改 `closeOpenItem`（Responses 的域词是 item，`closeBlocks` 是从 anthropic 那边抄的名字），`openairesponses/encode_request.go` 回带那处注释里「与 anthropic 出口不同：那边不登记」已被本批的 ⑥ 推翻，重写。新增 §9.6。**PO 于 2026-08-17 对评审提的两条「超票面」逐条裁定保留**：`reasoning_text.delta` 那一支（理由见 §9.6 缺口清单）与口径层 v0.79 那笔回写（订正的是 v0.66 的**依据**，不写等于让口径层继续挂一条已被本库自己的字节证伪的旁证）。修改人 jinpenga。
> v0.81 变更（口径层 v0.74 落地：request-id 第三档「读错误响应体」的实现，[#12](https://github.com/SimonGino/portage/issues/12)，2026-08-17）：v0.73 只改了文档，这一批把它写成代码。①**取值收敛在一处**：`upstream.RequestID` 拆成 `RequestIDs(h) (official, proxy)`（只按头名取两档候选，不再自己决定先后）+ 新增 `ErrorBodyRequestID(raw)`（顶层 `request_id`，解不出来回空串）；三档的先后由 `callRecord.resolveUpstreamRequestID` 定，**在 `logCall` 里调一次**。选这个落点不是为了好看：`logCall` 是透传与转换两条路唯一的共同收尾，取值挂在这里，验收「两条路行为一致」就是构造性的，不靠两处抄一样的代码；而且第二档要等错误体收完，拿到响应头那一刻还取不到。②**内存上限与落库上限从此是两个数**：`upstreamErrorLimit`（64KB，从 `convert.go` 上提到 `calllog.go` 并统一给两条路用）是收集上限，`errorDetailLimit`（2KB）改成**落库那一刻**才截（`captureWriter.truncatedTo`）。这一条是 v0.73 ④ 那个坑的唯一解：`request_id` 排在 `error` 对象之后，先截再解等于永远解不到。③**成功行走不到第二档是结构性的**，不是加了个 if：`rec.errorDetail` 只在失败时才被挂上（透传按 `status >= 400` 挂旁路、转换在写错误时 `setErrorDetail`），成功行那个字段是 nil。用例钉的也是行为而非计数——成功响应体里明摆一个 `request_id`，记的仍是头里那个。④用例七条对七条验收（`internal/server/requestid_test.go` 六条 + `internal/upstream/headers_test.go` 的 `TestErrorBodyRequestID` 覆盖非 JSON / 截断 / SSE 帧 / 嵌套同名键）。**流式错误帧不伪造样本**（本库无真实转录）：整段字节不是 JSON，按 v0.74 ⑤ 落空串，用例只钉这个结果。⑤客户端拿到的字节一字未动，`§2.7` 错误回显契约未动。修改人 jinpenga。
> v0.80 变更（`image_url.detail` 补转换，2026-08-17）：口径见口径层 v0.78。v0.79 记的那条已知缺口，PO 裁「补转换」，不是补丢弃项。`protocol.Image` 加 `Detail string`，CC / Responses 两个入口读、两个出口写，Anthropic 出口登记 `image_detail`。三条落地要点写进 §9.5：①**Responses 侧「留在 Extras」是假象**——`Extras` 永不外带是三个出口的既定行为（只放行 `cache_control` / `metadata`），所以两个入口其实都丢，只是丢的位置不同，提升成一等字段是唯一出路。②**字段形状不对称**：CC 的 `detail` 在 `image_url` 对象内部，Responses 的在 part 顶层与 `image_url` 同级（后者本身就是字符串），抄错一边发出去的是上游读不到的键。③`DropImageDetail` **只在 anthropic 包**，不像 `DropImageFileID` 那样三份——另两家原生支持，没有丢弃这回事；且 `file_id` 那一档不重复登记。样本 `in-cc-image` / `in-responses-image` 各加 `"detail": "high"`，覆盖表随之加两行。顺带把 `CLAUDE.md` 工程约定里那句「转换路径先备 golden 样本」补上例外分句——PO 2026-08-17 已裁构造样本可放 `fixtures/`，原句按字面读会把本票的实现判成违规。修改人 jinpenga。
> v0.79 变更（#1 的 review 修，2026-08-17）：①**抬图位置的真缺陷**：多个 `tool_result` 挤在同一条 user 消息（Anthropic 并行工具轮的常态，`in-anthropic-parallel-*` 实采可证）且头一个带图时，CC / Responses 两个出口都会把抬出来的 user 夹进两条工具结果中间——CC 那边 `role=tool` 必须紧跟带 `tool_calls` 的 assistant，这是必被上游拒的形态。改为**攒到所有工具结果发完再发一条**；用例钉的是角色序列（`TestEncodeRequestLiftsImagesAfterAllToolMessages` / `…AfterAllToolOutputs`），v0.78 那两条整链用例只发一个 `tool_result`，位置错了照样绿。②`Carrier()` 由裸 `string` 改具名类型 `protocol.ImageCarrier` + 四个常量：这个值在六个半边上被 switch 十来次、其中两处是 `== "file"` 的字面量比较，打错一个字母照样编译，后果是 `image_file_id` **静默不再登记**——而「丢得有声音」正是口径层 v0.39 给它单开一档的全部理由。③`hasConvertibleImage` 在两个出口半边逐字节重复，上提为 `protocol.HasConvertibleImage`（本批已经建了 `protocol/image.go` 当共享落点，只有它漏在外面）；`encodeOutUserParts` / `encodeOutAssistantParts` 除正文部件类型外完全相同，合并为 `encodeOutParts(blocks, textType, drop)`。④`parseAnthropicImage` 里那个「造一个四字段全填的临时 `Image` 只为问一次 `Carrier()`」去掉，判别式优先与字段优先级写成两级显式分支。⑤新增已知缺口一条：CC 入口的 `image_url.detail` 静默丢弃（Responses 侧同名字段反而留在 `Extras`，两个入口不一致），不在本票范围，交 PO。修改人 jinpenga。
> v0.78 变更（#1 收尾：样本归档改正 + 六格全链路补齐，2026-08-17）：①**四份图片入站样本从 `golden/` 挪进 `fixtures/`，meta 由 `verified: true` 改 `synthetic: true`**（PO 2026-08-17 裁）。v0.77 那批把手工构造的样本放进了 golden 库还盖上 verified——`fixtures/README.md` 明写「构造样本不得改名搬进 `golden/`」，而 `verified` 的定义是「人核过的**转录**」，构造样本盖上它就把这道闸稀释成了「人核过」，此后它说不清自己在挡什么。②**`TestCanonicalModelCoversInboundSamples` 改扫两个根**（`golden/in-*` 认 `verified`、`fixtures/in-*` 认 `synthetic`，同名两份直接判红）。不这么做①就落不了地：覆盖表要证的是「canonical 装不装得下」，而**能证明形状的样本不一定是转录**——图片那几行一旦无样本可依，会被那张表自己的陈旧项检查判红。③**新增 §9.5 与 `server/convert_image_test.go`：六个转换格子各补一条整链用例**。v0.77 那批只有三个 codec 包里的单测，而图片的解码在入口协议、编码在渠道协议，**分开看两边都「对」，错的是中间那一环**——这正是 v0.34 ① 为 developer 归一立过的规矩（「钉这条的用例走全链路而不是单测」）。断言比对的是 `tiny.png` 的**原图字节**而非样本里那串 base64：后者只能证明「串搬过去了」。④**新增构造样本 `in-anthropic-toolresult-image`**，钉「`tool_result` 里的图抬成后续独立 user 消息」——这条转换约束 v0.37 就写进范围了，v0.77 实现了却没有任何样本走到它，且覆盖表也缺 `messages[].content[].content[].source*` 那几行（嵌套一层的路径与顶层不是同一格）。用例连**位置**一起钉：抬到工具结果前面去，上游看到的顺序与实际发生的相反。⑤顺带订正口径层：v0.77 那批把 v0.76 那行版本记录（页标题改栏目名，与本票无关）**覆盖掉了**，现已恢复。修改人 jinpenga。
> v0.77 变更（#1 图片内容块跨协议转换落地，2026-08-17）：`BlockImage` 带结构化字段 `{MediaType, Data, URL, FileID}`，三个 codec 的 decode/encode 把 base64 与 url 互转；`file_id` 单独登记 `image_file_id` 后丢，不混进 `vendor_content`。CC / Responses 出口把 `tool_result` 里的图抬成后续 user 消息，Anthropic 出口留在 `tool_result.content` 数组里。§4.6 现状表 ①②④ 划掉。修改人 jinpenga。
> v0.76 变更（口径层 v0.77 落地：图片格式不设白名单，2026-08-17）：**只改文档**。§4.6 补一条实现约束——`MediaType` 原样写出，编码侧不做 jpeg/png/gif/webp 闸、不转码、不因格式丢块；svg 之类让上游 400。PDF 待裁仍不进本票。修改人 jinpenga。
> v0.75 变更（口径层 v0.76 落地，2026-08-17）：页标题改栏目名、去掉 lede、模型页渠道/模型分层 + 组合按钮。修改人 jinpenga。
> v0.74 变更（口径层 v0.75 落地：管理端壳改 A-v2，2026-08-16）：导航五项、去掉概览、侧栏改做；模型页主从改为左栏渠道 / 右栏模型主语 + 设置凭证井。修改人 jinpenga。
> v0.73 变更（口径层 v0.74 落地：request-id 取值加第三档「读错误响应体」，2026-08-15）：**只改文档**，实现另起票。①**§6.1 那段的取值顺序改成三档**：`request-id`（头）→ 错误体里的 `request_id` → `x-request-id`（头）；新档插在中间不是末尾，因为前两档都是 Anthropic 自己写的，而实测那条链路上 `x-request-id` **总是**有值（中转编的 uuid），插末尾就永远轮不到。②**范围只到失败行**——五份真实响应实测，`request_id` 只在错误信封里，成功响应的体流式非流式都没有。③**§7 的 DDL 注释同步**。④**实现约束记了一条别人踩不到就想不到的**：`error_detail` 截 2KB，而 `request_id` 在错误信封里排在 `error` 对象之后，超长原文有把它截掉的可能——取键要在**截断前**的字节上做。⑤这一档不违反「透传路径不做 decode→encode」：失败行的 body 本来就为 `error_detail` 读过一次，这里只是多解一个键。
> v0.72 变更（口径层 v0.73 落地：thinking 出向与请求侧 effort 补上 CC↔R 两格，2026-08-15）：**只改文档**，实现仍在 [#99](https://github.com/SimonGino/portage-legacy/issues/99)。①**§4.2 两处的方向从四条改成六条**：出向合成与 effort 直传都是六条路径全通，CC↔R 两格两端同域（`reasoning_content` ↔ `reasoning_summary_text`；`reasoning_effort` ↔ `reasoning.effort`），不选载体、不跨语义。②**实现上不新增分支**：三个出口的合成本就按「出口协议 × 通道」写，三个解码半边齐了这两格自然通；要盯的是别在某个出口上留「源侧是 CC/R 时才丢」的条件——那正是今天 `openaicc/encode_response.go:78` 与 `openairesponses/encode.go:187` 一刀切 `return nil` 留下的形状。③口径层 v0.62 其余各条与 v0.72 的载体裁决一字未动。
> v0.71 变更（口径层 v0.72 落地：请求侧 effort 四条路径全放开，2026-08-15）：**只改文档，`internal/` 一行未动**——实现在 [#99](https://github.com/SimonGino/portage-legacy/issues/99)。①**§9.3 那条「请求侧 effort 在这条路上丢弃」划掉**：CC 的 `reasoning_effort` 与 Responses 的 `reasoning.effort` 到 Anthropic 出口的落点定为 `output_config.effort`，字符串原样、不认模型。②**不合成第二个键**：实测 `output_config.effort` 单发即开思考（424 个思考 token），所以出口**不写** `thinking:{type:adaptive}`——写了就是替客户端开思考，撞口径层 v0.65 ④；老式 `thinking:{type:enabled,budget_tokens}` 同样一次都不写，本网关只认一个载体。③**§4.2 那行的方向改成四条全通**，并补上 Anthropic 官方值域实测值五档 `low|medium|high|xhigh|max`（原文按参考实现写的三档）；「域外值不钳」不变——钳的理由从来不是不知道值域。④`thinking_param` 登记档**不撤**，只是不再装「CC→A / R→A 方向的整条 effort」；§7 那张表原文本就写着 effort「映得过去、不在此列」，无需改。⑤实测细节与方法（先用两个必被拒的探针标定链路是否吞键、再读数）记在 [#37 评论](https://github.com/SimonGino/portage-legacy/issues/37#issuecomment-5295503462)，口径层 §5 条目 9 随之收敛。**版本号说明**：v0.71 这一号原是留给口径层 v0.71（毛值口径扩到流水输入 token 列）的同步，那一批仍待写，届时顺延取下一号。
> v0.70 变更（口径层 v0.67 落地：上游 request-id 在管理端露出来，[#81](https://github.com/SimonGino/portage-legacy/issues/81)，2026-08-14）：改动很薄，但**改掉了一条判据**。①`store.CallLogRow` 加 `UpstreamRequestID string`（`json:"upstream_request_id"`），`ListCallLogs` 的 SELECT 与 Scan 同批加一列——`/admin/api/logs` 的回包由此带上这个字段（§8.1）。**用 `string` 不用指针**，与 `ErrorDetail` 的 `*string` 正相反：那一列可空、要分开「没存」与「上游回了错但体是空」，这一列不可空（`TEXT NOT NULL DEFAULT ''`，§7 DDL），空串一档吃掉三种情况且它们在对账上是同一件事，`COALESCE` 之类的加工一概不需要，**空串照原样进 JSON 不转 null**。②Web：`CallLog` 加 `upstream_request_id: string`；`LogDetail` 的 `.log-meta` 里加一格，有值走 `CopyCode`（对账要粘给上游，手抄必错）、无值摆「—」；`.log-meta .copycode` 补 `margin-left: -6px` 拉回按钮自带的内边距，否则这一格的值比上面几格右移一截（同 `.model .copycode.model-name` 的成例）。③**「详情」按钮的判据从 `status >= 400` 放宽成 `status >= 400 || upstream_request_id !== ''`**（口径层 v0.67 ②，DESIGN v0.22）；**同批把上游原文那段用 `status >= 400` 包起来**——这个框现在成功行也开得出来，而那段的三句兜底文案（「没有存下上游原文」「上游没有返回任何响应体」）对着一次 200 全是假话。这是本批唯一一处**不改也能跑、但会显示假话**的地方。④测试加 `TestAdminLogsExposeUpstreamRequestID`（`internal/server/requestid_test.go`）：一次上游发头、一次不发，钉接口层字段在、成功行也有 id、且**空串不是 null**（用 `*string` 收 JSON 才分得出「给了空串」与「给了 null / 根本没这个键」）。⑤**不做按 request-id 的筛选**（口径层 v0.67 ⑥），`CallLogFilter` 一字未动。修改人 jinpenga。
> v0.69 变更（#80 的文档回写补漏与一处**错误引证**的订正，2026-08-14）：**只改文档，`internal/` 一行未动。**①**§9.4 第一条缺口的立论整条推翻重写**。#80 初版拿「口径层 v0.10 明令跨协议只能丢、不得伪造」给「两条新路丢弃上游推理」背书，而 v0.10 早在 2026-08-13 就被**口径层 v0.62 推翻**、又被 v0.65 加固：现行口径是**出向一律合成、四条路径对称**，且 v0.65 把丢弃从「体验问题」升格成**错误**（「已发生的成本不得静默吞没」）。同时初版那句技术立论「摘要转 `EvThinkingDelta` 等于拿摘要冒充正文」也不成立——canonical 的 `ThinkingChannel` 自 v0.29 就是 body / summary / signature **三通道**，摘要有自己的位置。**并且挡这一格的样本前提已经没了**：口径层 §2.6 明写「样本已采……合并后这一格只剩实现」，`responses-stream-reasoning-turn1` 随 #79 入库、#80 的解码用例正在消费它。代码现状（丢弃）不改，与另外几条路同步，合成属 wayfinder [#87](https://github.com/SimonGino/portage-legacy/issues/87) / [#93](https://github.com/SimonGino/portage-legacy/issues/93) 那条独立 effort；这里只把账记对——**欠的是实现，不是口径**。②**§7 配置校验那段三处旧闸门文案**（「该转换路径尚未实现」）随 #80 改判后未同步，改为「该端点没有对应的转换路径」并注明语义从「还没做」变「没得做」。③**README 三处**同批补：协议转换矩阵两格（CC→Responses、Anthropic→Responses）仍写「未放开」、状态段仍写「六条开了四条」、矩阵前言仍引旧文案。**这三处是 #80 漏掉的回写**——e67dae7 只回写了展开层 §2/§5/§9.4，README 作为对外第一事实源反而最久失真。④**六条路的真机验收全部收拢到 [#98](https://github.com/SimonGino/portage-legacy/issues/98)**（PO 2026-08-14 两次裁定：先定 #80 直接关闭、验收另开票，再定 #9 关闭、#11 与 #98 合并）。此前它散在**五处**——#12（R→CC）、#25（R→A）、#9（CC→A）、#11（A→CC）、#80（CC→R / A→R）——而 #12 / #25 **早已关闭**，那两条的验收残留一直吊在关掉的票上没人认领；#9 一关又会多两张同样的。§9.1~§9.4 四条末尾同批改指 #98，并补记 harness × 路径分工（口径层 v0.20 必过档：Claude Code 盖 A 入口两条、Codex CLI 盖 R 入口两条、pi 盖 CC 入口两条；以 Anthropic 为出口的 R→A / CC→A 另受 #7 官方凭证制约）。**#9 与 #11 的实现侧早已合入**，关它们不改变任何代码事实，只是把「已做完的实现」与「没跑过的手工验收」这两件被绑在一张票上的事拆开。修改人 jinpenga。
> v0.68 变更（#80 M2-9 `openairesponses` 出口半边落地，CC→R / A→R 放开，九格全开，2026-08-14）：均为实现层，口径不变。①**`openairesponses` 出口半边落地**：`decode_response.go`（`DecodeStream` / `DecodeFullBody`）+ `encode_request.go`（`EncodeRequest` / `EncodeRequestReport`）。三处形态差异源于 Responses 的 output 是**有序 item 列表**而非扁平 delta 流——工具调用的 `call_id` 与 `name` 只在 `output_item.added` 上（增量帧只带 `item_id`，三份真实转录实测），所以 `EvToolCallStart` 只能由 added 帧发；item 严格顺序开合，不必像 CC 那样把分片攒到流末；停因没有 `finish_reason` 可读，只能由 `status` + `incomplete_details.reason` + 「output 里有没有工具项」判出来。②**`Codec` 补嵌 `protocol.StreamReadFlag`**——此前它是三个 codec 里唯一没兑现 `StreamReadReporter` 的，`streamConverted` 的类型断言会静默落空，客户端断连被记成干净的 `ok`。③**`conversionOpen` 换职责**（§2）：九格全开之后它不再是「逐格放开」的临时闸，只挡 `count_tokens` 这一种**没有上游对应端点**的入口端点；判据按端点不按协议的理由随之改写。④**闸门反例集体换靶**。PO 2026-08-14 就 #80 点名的三条裁定「换载体、留断言」；`TestGateStaysClosedForUnimplementedPath`、`TestMessagesRejectsCrossProtocolCandidate`、调用日志那条不在裁决点名之列，是同性质用例按同一处置顺延。`TestFallbackDoesNotOpenAnUnimplementedPath` 与前两条测的行为已不存在，删除；`TestCrossProtocolGateAnswersInInboundFormat` 的 Anthropic 子测试改指 `count_tokens × openai_responses`（永久成立的反例），CC 子测试删除，「错误用入站格式回 + 不泄 key/base_url」两条断言改由新增的 `TestCCInboundErrorKeepsOpenAIShape` 在 503 路径上承载；调用日志那条「闸门拒绝也落日志」同样改指 `count_tokens`。⑤新增 §9.4 记两条路的用例分工与四类已知缺口，其中**「CC / Anthropic 客户端看不到上游推理过程」是用户可感知的退化**，单独点名（与 §9.2 里 R→A 那条对称）。⑥订正 §5 一处错了两票的事实：落地进度行写「`openaicc` 只有出口半边」，而 #9 之后它两半就齐全了。修改人 jinpenga。
> v0.67 变更（Responses 出口半边的样本前提落地 + 一条错了很久的事实来源改掉，[#79](https://github.com/SimonGino/portage-legacy/issues/79)，2026-08-14）：**只改文档与样本，`internal/` 一行未动**。①**§4.2 那句「Responses 侧没有真实上游转录、事件名以 sub2api 为准」删掉**——它写于 M2-1，此后压缩批（#73）与 reasoning 批（#93）早已入库，2026-08-10 的更正只发在 #9 的评论里、从没回写。改成三档复核进度（词表与次序已核、字段级已核、终帧 output 与非流式未核），后两档给出依据。②**五份真实上游转录入库**（`responses-stream-text` / `-tool-turn1` / `-tool-turn2` / `-parallel-turn1` / `-parallel-turn2`，Codex CLI 0.147 `codex exec`）。`expect` 由 `scripts/verify-expect-responses.py` 独立重算核对，五份全对得上。③**字段级比对做掉了**：`EncodeStream` 实发帧对 `tool-turn1` / `parallel-turn1` 逐项比，键集恒为实采子集，`part` / `output_text.done` / 终帧 `usage` 三处逐字相等；三处差异（多发 `call_id`+`name`、不发 `obfuscation`、不发 `phase`）判为 wire-legal 且有意不改，逐条记在 `openairesponses/encode.go` 文件头。**`phase`（实采 `commentary`）要不要在出向按 commentary / final 合成，是待 PO 裁的口径问题**，本票不动；随后 PO 裁定（2026-08-14）**不合成，维持现状**——语义源头没有对应概念，合成即凭空捏造展示语义，现网 Codex 不依赖它，实测出客户端行为差异再立票。④**§9 登记两个缺口**：Responses 非流式无真实样本；终帧 `response.completed.output` 的 item 形状经这个中转验不了（它重组成降级形态）。⑤顺带修 `scripts/redact-upstream-responses.py` 的路径正则（字符类漏排 `\`，会吃掉 `\"` 的转义反斜杠把整帧 JSON 弄废），并按原样补回 `responses-stream-reasoning-turn1` 被它弄坏的两帧。修改人 jinpenga。
> v0.66 变更（口径层 v0.66 落地：思考 token 认这一格，2026-08-14）：**这一批真写代码了**——地图 [#87](https://github.com/SimonGino/portage-legacy/issues/87) 的 planning only 由 PO 在收官票 [#97](https://github.com/SimonGino/portage-legacy/issues/97) 上解除（只解除本票这一块，v0.62/v0.65 攒的出向合成与 `Effort` 仍未开工）。①**`Tap.Summary` 加两格：`ReasoningTokens int` + `HasReasoningTokens bool`**。用 bool 不用 `*int`：Summary 全仓靠 `!=` 整体比对（golden 驱动与各 Tap 用例），指针会退化成地址相等。两个 Tap 判「键在不在」而不是「值非零」——与同一函数里其余字段的「非零才覆盖」相反，因为这里 0 是有意义的取值。②**canonical `Usage` 加 `ReasoningTokens int`，不加 Has**。两侧要答的问题不同：Summary 答「上游说了什么」（落流水，要分三档），canonical 答「往下游写什么」，而「报了 0」与「没报」写出去的字节一样。零值即没报，沿用 `MergeSnapshot` 既有约定。③**出口两侧的写法**故意不一致，各随各的协议契约：CC 出口**有数才写** `completion_tokens_details`（0 会被读成「这次没思考」，而上游多半根本没报）；Responses 出口**键恒在**（`output_tokens_details` 是 Responses usage 的必有项，真实转录里非推理轮也带 `reasoning_tokens:0`，缺键可能被 Codex 判非法）。**顺带修一个既有缺陷**：`openairesponses/encode.go` 的这一格此前是硬编码 `0`——canonical 装不下这个数时还算凑合，装得下之后就是把上游报的思考成本抹成零。④**Anthropic 出口丢弃且不登记**：协议里没有承接位置，可见性由流水那一列兜底。不进丢弃登记表——它每请求必丢，性质同 v0.34 判过的 `DropThinking`（每次刷一条噪声）。⑤**`call_logs.reasoning_tokens` 可空**，存量行落 NULL，与 `upstream_request_id` 那条「不可空、默认空串」正相反：那条的两种情况在排障上是一回事，这条不是。⑥**golden expect 加了 10 份**（`cc-*` 五份 + `responses-*` 五份；其中 `cc-parallel-tools` / `cc-stream-parallel-tools` 两份 M0 样本也带 `reasoning_tokens`，10 与 11——它们采自同一个中转上游，此前没人看这一格）。expect 由 Python 独立按 wire 语义重算，不复用 Go 侧代码；`scripts/verify-expect-responses.py` 同批补了 reasoning 两项，否则加了列之后它对着新 expect 照样打「一致」，守卫是空的。⑦前端只动调用记录一页，判据是 `> 0` 而非非 null：对着一行流水，null 与 0 都没有可看的成本。修改人 jinpenga。
> v0.65 变更（v0.64 的落档扫尾与一处订正，2026-08-14）：仍是只改文档。①**订正 v0.64 ⑥**：那一条把 §9.3 标成「A→CC」（v0.61 ② 的同一处笔误延续下来的），而 §9.3 是 **CC→A**；标错之后结论也跟着错——CC→A 恰恰是 effort **不放开**的那半，§9.3 的缺口表一条也划不掉，反而要**加**一条。已在 §9.3 加上「请求侧的 effort 在这条路上丢弃、登记 `thinking_param`、放开挂 #37」。②§9.2 那条 thinking 缺口的「手上三份转录里一条 delta 都没有」是 #93 之前的状态，原地留痕并补记 `responses-stream-reasoning-turn1` 已录全 `reasoning_summary_part.added` → `text.delta` → `text.done` → `part.done`（转录随 #93 分支入库，合并后这一格只剩实现）；口径层 §2.6 的同一句同批订正。③丢弃表 `thinking_param` 一行补上 **`thinking.type` 本身**（`enabled`/`adaptive`/`disabled` 这个开关）——v0.64 ③ 的枚举漏了它，实现者碰到 `thinking:{"type":"disabled"}` 会不知道该登记哪一档；口径已由 v0.65 ④「不替客户端开思考」覆盖，这里只是指认桶。修改人 jinpenga。
> v0.64 变更（口径层 v0.65 落地：请求侧思考参数只映一维 + 出向合成不加闸，2026-08-14）：**只改文档，代码一行未动**——实现仍另起 effort（wayfinder 地图 #87 是 planning only；裁决票 [#95](https://github.com/SimonGino/portage-legacy/issues/95)）。①**canonical 这次要动了**：`Request` 加一等字段 `Effort string`（v0.61 说的「canonical 不动」只管出向那半边）。理由是 `Extras` 的定义写着「本协议独有、跨协议无处安放」——effort 一旦要跨协议映就不符合这个定义；留在 `Extras` 会让三个 codec 各写一遍「从哪个键里掏 effort」，且 `canonical_coverage_test` 那道闸看不见它。取值是**原样字符串不归一**（域外值不钳，口径层 v0.65 ⑤），零值即「客户端没说」。②**解码侧三处读、编码侧两处写**：读 `output_config.effort`（anthropic）/ `reasoning_effort`（openaicc）/ `reasoning.effort`（openairesponses）；写只放开 `openaicc` 与 `openairesponses` 两个出口，**`anthropic` 出口不写**（口径层 v0.65 ③，CC→A / R→A 那一格挂 #37 实测）。三处入站键各自从 `Extras` 迁出后，`Extras` 里剩下的 `thinking` / `reasoning` / `output_config` 仍原样留着（同协议透传要取回）。③**新增丢弃常量 `DropThinkingParam = "thinking_param"`**，三个出口各一份，登记「思考参数里没映过去的维」：`thinking.display`、`reasoning.summary`、数字预算（Qwen `thinking_budget`）、以及 anthropic 出口整条 effort。**不并进 `DropVendorRequest`**——今天那两处是 `len(Extras)>0` 就打包登记一次，effort 映走 display 没映时，日志上看不出「我的 effort 到底传没传」（同 `DropMetadata` 当初拆出来的理由）。④**`DropThinking`（块级）与 `DropThinkingParam`（参数级）是两档**，别合并：前者是每请求必丢的口径结果（v0.34 已判它不该每次刷一条噪声），后者是「客户端表达了、我们没带过去」，恰恰要看见。⑤**出向合成不加闸**（口径层 v0.65 ⑥）：`thinking.display:"omitted"` 不作为「不合成」的条件，实现侧不要读它——这一条写在这里是因为它读起来像个天然的优化点，而它是错的，理由见口径层。⑥§9.3（A→CC）的缺口表可再划掉半条：A→CC 方向的 effort 从此传得过去，CC→A 那半仍挂着。修改人 jinpenga。
> v0.63 变更（口径层 v0.64 落地 + 一批文档订正，评审 PR [#86](https://github.com/SimonGino/portage-legacy/pull/86) 的结论，2026-08-14）：**代码只动一处注释**，其余全在记录层。①**`UnknownModelLabel` 补口径出处**（口径层 v0.64）：它随 v0.59 那一轮进的仓库，改的是模型维度多一档聚合行、`/logs?model=` 多认一个取值，两处都对外可见，此前只有展开层的一句订正。同批订正 `store/admin.go` 那句「与另外两个维度的 `(未鉴权)`/`(未走到上游)` 同款」——同的只是取名法，那两个是**只出不进**的显示 label，`UnknownModelLabel` 还当查询参数值，是第一个双向哨兵；注释改为写明「哨兵优先于同名真实模型，全角括号是刻意的」，这条规则本身见口径层 v0.64。②**订正 v0.57 ②④**：那两条记的是设计不是已落地的代码，排行列表实际是本 PR 的 `30be033` 才落的（`main` 上仍是七列 `.table-usage` 表）。③**订正 v0.59 ⑤**：「CSS 只动注释与一处间距」不确，`.rank-list` 是本轮新增、那个 14px 从不存在，同轮还动了四处 CSS；并补记 ⑥换渠道回页顶与 `scrollbar-gutter`，此前只在 commit message 里。④**订正 v0.60 ③**：「当前页左右各一」与 `PAGE_WINDOW = 5` 自相矛盾且与 `start = cur - 2` 不符，改「左右各二」。⑤`DESIGN.md` v0.21 同批清两笔：四处「网关 key」改「API Key」（`CONTEXT.md` 术语表 2026-08-12 就把它列进 _Avoid_，而 §2「术语严格一致」那条的括号里一直举着这个旧称）、补记 v0.14 末句「卡片不再与页面同名」随 v0.17 拆页作废。**②③④连同 v0.62 已清的两处，是同一类账**：口径与展开两层都靠「这一轮动了什么」自述，自述失实就等于事实源坏了一格——所以订正一律原地留痕，不抹掉原文。修改人 jinpenga。
> v0.62 变更（口径层 v0.63 落地：转换路径的断流也记 `stream_aborted`，2026-08-13）：**补记一段已落地但两层文档都没有出处的代码**——它随 v0.61 那一轮一起进的仓库，评审 PR [#86](https://github.com/SimonGino/portage-legacy/pull/86) 时才发现没有版本记录。①`protocol` 加**可选接口** `StreamReadReporter`（`StreamReadError() error`）与它的现成实现 `StreamReadFlag`，`anthropic` / `openaicc` 两个 codec 内嵌后在解码读上游失败时置旗。**不进主接口 `Codec`**：只有当解码侧的那个 codec 用得上，塞进主接口等于逼另外两侧实现一个恒返回 nil 的方法（同 `RequestEncodeReporter` 的处置）。②`StreamReadFlag` 上锁不是多余的：编码侧遇到 `EvError` 是**提前 return** 的（上游在流里回错误对象就会这样），那之后解码 goroutine 还在跑，再撞上读失败就是实打实的并发写。③`streamConverted` 在 `EncodeStream` 正常返回之后再问一句，断了就把 `outcome` 改成 `stream_aborted` 并按 v0.53 落脱敏原文，与上游传输错误那一支同源。④测试见 `convert_test.go` 两条（含 `assertNoSecrets`）。
> v0.61 变更（口径层 v0.62 落地：thinking / reasoning 跨协议口径复议，2026-08-13）：**thinking / reasoning 这条只改文档，代码一行未动**——实现另起 effort（wayfinder 地图 #87 的 Destination 就是 planning only）。（**v0.62 订正**：原文写的是没有限定语的「只改文档，代码一行未动」，而同一个提交里带着断流归因的约 100 行 Go——那是另一条线，见 v0.62；就 thinking 本身这句话仍然成立。）①§3 有损转换策略表那条从「跨协议丢弃 + 不做伪映射」改为「出向合成 / 回带一律丢 + 登记」。②§9.2（R→A）与 §9.3（A→CC）两条「上游 thinking 必然丢弃」的**性质分别变了**：前者挡着的从此只剩样本（口径已裁要合成，#93 采样范围随之扩到 reasoning item 的生命周期），后者的立论「CC 没有承接它的位置」被判为错——`reasoning_content` 是 DeepSeek/GLM/Qwen 一路的事实标准载体，四家参考实现全用它。③A→CC 请求侧丢弃表的 `thinking` 行与 §5 坑清单的 `encrypted_content` 条**都只管回带方向**，回带口径未变，原文加限定词以免被读成「响应侧也丢」。④canonical **不动**：`ThinkingChannel` 的 body / summary / signature 三条通道 v0.29 就预留了，Anthropic 解码侧也早在产 signature 通道的事件；这次要改的只有编码侧那两行丢弃（`anthropic/encode.go` 的 `EvThinkingDelta` 分支、`openairesponses/encode.go` 的同名分支），以及 `openaicc` 解码侧新增「产 `EvThinkingDelta`」。⑤实现开工前的两个前置：Claude Code 对无 signature thinking 块的显示行为实测（#94），与 reasoning golden 转录入库（#93）。修改人 jinpenga。
> v0.60 变更（口径层 v0.61 / DESIGN v0.20 落地：调用记录改可跳页码，2026-08-13）：翻页从纯游标改成**锚点窗口 + 页内偏移**，前后端各动一处。①`store`：WHERE 拼装抽成 `callLogWhere`，新增 `CountCallLogs` 与它共用——两处各拼一份的话，某天加了个筛选只改一处，页码就会按另一组条件算出来（「只看失败」下按全部流水算页数，末页翻过去是空的）。`CallLogFilter` 的 `Before` 与 `Offset` 从「二选一」改成**一起用**：`Before` 钉窗口上沿、`Offset` 在窗口内定位，SQL 本来就同时认这两个，一行没改。②`/admin/api/logs` 回包由裸数组改 `{rows, total}`（§8.1），`total` 走 `CountCallLogs`、认同一个 `before`；与 `/usage` 的 `{rows}` 同形。计数与取行不包事务：唯一的窗口是不带 `before` 的第一发，管理端拿到它就把锚点钉住了，下一次点击自动纠正——为这点误差占住那条独苗 SQLite 连接不划算。③Web `useLogFeed` 重写：游标栈、`more`、`LOG_PAGE + 1` 那条探路行全部退场，state 变成 `{page, total}` + 一个 `anchor` ref（只在请求里当参数用，改了不需要重渲染）；筛选变化与「刷新」都松开锚点并回第一页。新增 `pageList(cur, count)` 算页码那一排（首末恒在、**当前页左右各二**、`PAGE_WINDOW = 5`，只隔一页时不摆省略号），排布稳定在 8–9 格。（**v0.63 订正**：原文写的是「当前页左右各一」，与 `PAGE_WINDOW = 5` 自相矛盾，也与实现不符——`start = cur - 2`，中间那段是当前页加左右各二共五格。）④CSS：`.pager` 从 `row-actions` 那套里独立出来（`justify-content: flex-end`、可折行、gap 4），整排钉 30px 高，新增 `.pager-page`（`min-width: 30px`，等宽方块）与 `.pager-gap`，删 `.pager-at`。⑤测试：`TestLogsPaginateWithBeforeCursor` 改写为 `TestLogsPageJumpWithinAnchoredWindow`（翻页途中插两行，直接跳第 3 页，行与 `total` 都不受影响），`TestLogsFilterByModelAndFailure` 补一档「总数按同一组筛选算」。修改人 jinpenga。
> v0.59 变更（口径层 v0.60 落地：观测拆成概览 / 调用记录 / 排行三页，2026-08-13）：**基本是纯前端**（**v0.62 订正**：原文写的是「纯前端，后端与两个用量端点一行未动，`/usage?days&by`、`/usage/daily?days`、`/logs` 的形状与语义都不变」，不确——同一轮为「没解析到模型名」那一档在 `store` 新增了 `UnknownModelLabel = "(未记录模型)"`，`by=model` 下的空 label 与 `/logs?model=` 的筛选语义都跟着变了：401 鉴权失败、请求体不是合法 JSON、缺 `model` 这几条路径在赋值之前就返回了，那一列是空串，而空串在模型下拉里正是「全部模型」的取值，两行会同时显示成选中。回包**形状**确实没变，变的是那一档的取值语义，补 `TestUsageUnknownModelLabelled`）。①`web/src/pages/Usage.tsx` 拆成三个页面组件——`Overview.tsx`（`.stats` 指标条 + `UsageChart`）、`Logs.tsx`（`useLogFeed` 游标翻页 + 九列流水表 + 模型/失败筛选）、`Rankings.tsx`（`.rank-list` 名次列表 + 维度/天数切换），原文件删除。②新增 `pages/usage-common.ts` 放两处**必须同源**的东西：`DAY_OPTIONS`（窗口选项）与 `sumUsage(rows)`（合计）——概览拿合计做指标条、排行拿它做占比分母，各写一份的话早晚两页对不上。`fmtCompact` 上提到 `ui.tsx`，与 `fmtInt` 并排。③**取数的三处变化**：概览固定 `by=model` 取合计（换维度只改分组、不改总量，而它要显示「几个模型」）；排行仍按当前维度取；调用记录页的模型下拉自己拉一次 `by=model`，窗口固定 `MODEL_OPTION_DAYS = 30`（该页没有天数开关），`useList` 的 deps 因此是空数组。天数/维度的 state 各页自持，不跨页共享。④路由与导航（`App.tsx`）：`/overview`、`/logs`、`/rankings` 三条新路由 + `/usage` → `/overview` 的 `<Navigate replace>`；导航六项，`*` 兜底仍跳 `/channels`（默认落地页不变）。⑤CSS（**v0.63 订正**：原文写的是「只动注释与一处间距：`.rank-list` 的上内边距 14px → 6px」，两处都不确——`.rank-list` 这套类是**本轮新增**的，那个 14px 从来不存在（v0.57 声称它已落地，实为空账，见该条的 v0.63 订正）；同轮还新增 `html { scrollbar-gutter: stable }`、`.section-head` 的 `border-bottom`、右栏 `border-left` + `padding-left`、右栏段间距 `gap: 18px → 28px`，删掉 `.table-usage` 与 `.model-cell`。后四项是 DESIGN v0.16 那两条线的落地，有出处；`scrollbar-gutter` 没有，见 ⑥）：「用量页」段名改成「观测三页」。`.stats` 一套类由概览与排行共用，排行只用其中的 `stat-lead`。⑥**换渠道回页顶**（**v0.63 补记**，原文漏了）：`Channels.tsx` 加 `useEffect(() => window.scrollTo(0, 0), [id])`——左栏 sticky，滚到几屏之下也能直接点下一个渠道，而右栏一换文档高度就变，从挂三十个模型的渠道跳到只有两个模型的，浏览器把超出的滚动位置夹回去，看着像页面抖了一下。**只盯 URL 里的 `id`、不盯数据**：盯数据的话写操作后的 reload 会换 `data` 引用，变成「拨一下模型开关整页弹回顶上」。同批 `html { scrollbar-gutter: stable }` 堵住滚动条出现/消失时横移居中内容那一路。修改人 jinpenga。
> v0.58 变更（口径层 v0.59 落地：Codex 压缩 golden 转录入库，#73，2026-08-13）：三段真转录采齐（`responses-stream-compact-turn1/trigger/replay`），§7.7 的验收从「全部手搭 fixture」变成「口径自洽由 fixture 钉、发包形态由转录钉」。①**新增四条 golden 驱动用例**（`compaction_golden_test.go`）：真实压缩 turn 两道判据都认得出且工具被剥干净；回带轮 `CompactionTurn()` 为假、带回来的**真 OpenAI 密文**解不开、降级占位并登记 `compaction`——这条此前只有手搭假密文能演，现在混路场景有真身；压缩前普通轮两道判据都放行（挡的是「判据认位置」一类退化，真 trigger 恰在尾项）；compaction item 的线格形状。②**转录改掉一处实现**：`compactPrompt` 补回 codex-rs 模板的行尾换行（此前照 opencodex 二手抄写丢了），`summaryPrefix` 逐字节一致、依据升级为真客户端转录。③**观察到但不改实现的三件事**，都写进 §7.7：上游发 `output_item.added` + `.done` **两个**事件且两份 `encrypted_content` 不同（676 / 1164 字节），`response.completed.output` 是**空数组**，回带的 item 与 `.done` 逐字节相等。据此复核 codex-rs `collect_compaction_output`（Apache-2.0）：它只数 `OutputItemDone`、判据是 `compaction_count == 1`，`output_item_count` 只进错误文案——所以「只发 done、不发 added」维持原判，且额外知道了「多发别的非 compaction item 不会致命」。④`goldenrec` 的 `recordedHeaders` 收 `x-codex-beta-features`（压缩档位的判据，丢了就分不清 v1/v2）；`scripts/redact-inbound-responses.jq` 补 `internal_chat_message_metadata_passthrough.turn_id`，新增 `scripts/redact-upstream-responses.py`（`response.raw` 的等长 UUID 替换，逐字节保真）与 `scripts/verify-expect-responses.py`（独立重算 `expect`）。修改人 jinpenga。
> v0.57 变更（口径层 v0.58 落地：调用记录置顶 + 汇总表改排行，2026-08-13）：**纯前端，后端一行未动**，两个用量端点与它们的返回形状都不变。①`Usage.tsx` 两张卡换序（调用记录在上、排行在下），「概览」卡改名「排行」，指标行与 `UsageChart` 随它下移、内容不变。②七列汇总表换成 `<ol class="rank-list">`：名次 + 图标（仍只在模型维度画——图标是从模型名猜厂商猜出来的，套在人起的凭证名/key 名上只会猜出一堆首字母块）+ 名字 + token 合计 + 占比，其余四列压成名字底下一行附注；缩写值（`fmtCompact`）的精确数进 `title`，表没了之后那是唯一还能拿到原数的地方。③**名次在前端排**：后端那句 `ORDER BY COUNT(*) DESC` 不动，`Usage.tsx` 按 `input+output` 降序、并列退回 `calls`。放前端是因为同一个 `/usage?by=model` 端点还给下面那个模型下拉供选项，改 SQL 会把两处绑在一起；行数是模型数量级，排序代价可忽略。并列退回调用次数专门接的是「上游整段不报 usage」那一格（sub2api 那类中转），那时 token 全零、次数是唯一还有区分度的数；同一格里占比显示「—」而非 `0.0%`。④CSS：新增 `.rank-*` 一套，删掉只服务旧表的 `.table-usage`（min/max-width 与 padding）与 `.model-cell`；`.table-logs` 那半原样保留。（**v0.63 订正，管 ②与④两条**：这两条记的是**设计而非已落地的代码**——本条随 [#85](https://github.com/SimonGino/portage-legacy/pull/85) 合进 `main` 时，`Usage.tsx` 里仍是七列 `.table-usage` 表，`styles.css` 里既没有 `.rank-*` 也没删 `.table-usage`。排行列表是 [#86](https://github.com/SimonGino/portage-legacy/pull/86) 的 `30be033` 才真落的，DESIGN §6 末条「排行是列表不是表」与 DESIGN v0.15 同样是那一票补写的。本条不改内容——它描述的形态最终一字不差地落了地，错的只是时态。）**不画占比条**——一行一根横条正撞 DESIGN §8「一个数值画一根柱」，右边那个百分数说得更准，参照的 new-api / OpenRouter 排行榜也都没有条。修改人 jinpenga。
> v0.56 变更（#74 第三轮审查：停因全集不完备 + 非流式漏置位 + 丢弃日志发不出来，2026-08-13）：三条都是本 PR 自己立的口径没铺满，PO 2026-08-13 裁定在同一票内修。①**`tool_calls` 补进不产 item 的判据**（§7.7）——`compactionNoItem` 原来只挡 `length` / `content_filter` / 断流 / 空文本，上游先写一段开头再改去调工具时，停在半截的正文会被封成 `completed` 的 item 装回历史。`rewriteAsSummarizer` 剥了工具，合规上游到不了这里；但编码侧那条吞工具事件的分支本就承认「自带服务端工具的兼容网关照调不误」，挡住事件却收下半截正文是同一道防线只修了一半。补上之后判据对 canonical 停因全集完备——**这是修的类，不是修的这一格**。`tool_calls` 不是 `incomplete_details.reason` 的合法取值，走 `response.failed`。②**`anthropic.DecodeFullBody` 补 `Truncated`**（§7.7）——v0.55 只改了流式那半边；`openaicc` 的非流式复用 `streamState.finish` 白捡这一位，`anthropic` 的 `EvDone` 是手搓的，于是非流式压缩 turn 上一份没声明 `stop_reason` 的响应仍会被当成完整摘要。③**丢弃日志从 `CompactionTurn()` 闸里提出来**（§7.7、`relayConverted`）——回带解不开发生在压缩之后的**普通**请求上，那一轮没有 trigger，日志罩在压缩 turn 里等于让混路场景的头一次完全无声；仓库自己的 `TestDecodeCompactionItemRestored` 就断言着回带时 `CompactionTurn()` 为假。三条各补用例（§7.7 验收）。口径层无变化——「绝不产 item / 明确拒绝」是 v0.54 定的，这三条是落实。**golden 仍欠着**（#73）。修改人 jinpenga。
> v0.55 变更（#74 复盘修正：断流误判 + 心跳漏思考段，2026-08-13）：本地合成落地后的两轮审查各挑出一处，都在「压缩 turn 什么时候不许产 item」这条线上。①**canonical 加 `EvDone.Truncated`**（`protocol/event.go`，两个解码器的兜底收尾置位）——原判据是对 `e.stop` 的黑名单（`length` / `content_filter`）加空文本，漏掉了「上游连接干净断在摘要写到一半」这一格：解码器为了给下游一个合法取值，会把没等到 stop reason 的收尾兜成 `StopReason: "stop"`（`anthropic` 的 `emitDone`、`openaicc` 的 `finish`），wire 上与真正的收尾一模一样，于是半截摘要会带着 `completed` 被 Codex 当成**替换历史**装回去，等于永久删掉会话前半程——正是 §7.7 立着要杀的那个形态。**改的是 canonical 事件模型**（此前只有 `StopReason` 一个字段描述收尾），因为这个区分在解码边界上就丢了，编码侧再判也判不出来；普通路径不读这一位，行为中性。编码侧另把 `e.stop == ""` 也算一种不产，给直喂事件的调用方留余量。②**心跳漏了思考段**：吞掉思考/工具增量的那条分支在 `heartbeat()` 之前直接 return，而 `rewriteAsSummarizer` **有意留着 `reasoning`**——开思考的上游先想几十秒再写第一个摘要 token，那段静默是 summarizer turn 的常态而非边角，且本就落在心跳自称覆盖的「增量在流、被我们吞掉」之内。③顺带：`compactionNoItemReason() string` 改 `compactionNoItem() *compactionFailure`（`wireReason` + `message` 两字段分开，免得 `empty_summary` 这类自造哨兵漏进线格 `incomplete_details.reason` 冒充 reason 码）；`responseBody` 对 incomplete 硬写 `max_output_tokens` 与 `finishCompaction` 覆盖它这一对隐式耦合两处都补了注释；v0.54 误删的 `TestHasCompactionTrigger` 判据表补回（透传闸的判据仍记在 §7.6，不该零直测）。**golden 仍欠着**（#73），口径见口径层 v0.57。修改人 jinpenga。
> v0.54 变更（口径层 v0.54 正式修法落地：Codex 压缩本地合成，#74，2026-08-13）：转换路径（R→A / R→CC）的压缩 turn 从「明确拒绝」改为**本地合成**，止血闸只剩透传半边（§7.6 改写，新增 §7.7）。①**信封 `ptg1:` + base64(摘要)**——这串是**长期兼容约束**：它发出去就进了客户端的会话历史，改它等于让所有在途会话的回带摘要解不开、降级成占位；要改只能加新前缀并保留旧前缀的解码（§5 坑清单同条目）。刻意不沿用 opencodex 的 `ocx1:`：两个网关的信封语义各自独立，撞名只会让混路样本更难判。②**摘要 prompt 与回带引导语镜像 codex-rs 模板**（`core/templates/compact/prompt.md` 与 `summary_prefix.md`，经 opencodex 转录，MIT）——引导语必须逐字对齐，Codex 侧靠它认出「这条 user 消息是摘要而不是用户说的话」。③**三种情形绝不产 item**：截断（`length`）、内容过滤（`content_filter`）、上游正常收尾却零字摘要。前两种发 `response.incomplete` + `incomplete_details.reason`，第三种发 `response.failed`——**「completed + 零 item」是 #71 要杀的那个静默 Fatal 形态，任何一支都不许落到它上面**；`content_filter` 因此单列一条（正常路径把它并进 `completed`）。④**G2 回带还原**：`compaction` / `compaction_summary` / `context_compaction` 三种 item 解信封还原成 `summaryPrefix + 摘要` 的 user 消息，解不开降级占位并登记丢弃（`Codec.CompactionDrops`，日志在 `relayConverted` 打）；不带密文的 `context_compaction` 是 codex-rs 本地压缩标记，跳过不占位。opencodex 那条「清悬挂 pendingReasoning」在本仓不存在——我们的 reasoning 就地成块，没有跨 item 的待配对状态。⑤**静默期心跳**：合成期吞掉正文增量时按 15 秒下限发 SSE 注释行；它只盖得住「增量在流、被我们吞掉」这种静默，上游整体卡住仍由上游超时接管。⑥用例：`internal/protocol/openairesponses/compaction_test.go`（信封往返 / 改写 / 还原 / 三种不产 item / 心跳节奏）、`internal/server/compaction_test.go`（端到端合成与回带还原）。**golden 仍欠着**：#74 原本 blocked by #73，PO 于 2026-08-13 裁定先实现，本批用例全是手搭 fixture，钉得住我们自己的口径、钉不住「Codex 真的这么发包」——那笔账在 #73 不销。修改人 jinpenga。
> v0.53 变更（口径层 v0.56 落地：request-id 落流水 + 构造样本另立一档，2026-08-13）：#37 缩范围那一半的实现，该票**不关**——两条要官方直连实测的验收原样挂着。①`call_logs` 加 `upstream_request_id`（TEXT NOT NULL DEFAULT ''，§7 DDL）：取头走 `upstream.RequestID`（官方 `request-id` 优先、`x-request-id` 兜底），在 `resp` 到手处取、透传与转换两条路都记，slog 同名字段只在非空时打（同 retries 的规矩）。**不可空与 `error_detail` 的可空是两种取舍**：那一列要分开「没存」与「上游回了 4xx 但体是空」，这一列上「没走到上游」与「上游没回这个头」都读作「没有可用的 id」，前者看 status 就知道。②§6.1 响应头那段补落地形态：回传是构造性成立的（`CopyResponseHeaders` copy-all，不是白名单），官方文档列出的整套头钉进 `gatewaytest.AnthropicResponseHeaders`，任何一处退化成白名单当场红；原文并列的 `x-request-id / request-id` 收敛成官方名优先。③`testdata/fixtures/` 立档（§9）：`anthropic-cache-hit` 与 `anthropic-stream-cache-hit` 从对应真实转录派生、只改 usage 数字，闸门是 `synthetic: true`（与 golden 的 `verified: true` 反着开，防构造样本改名冒充转录），驱动 Tap 的净值语义与 `decode_response.go` 的毛值归一——缓存两项的 Anthropic 侧解析路径此前没有任何样本走到。用例：`internal/upstream/headers_test.go`、`internal/server/requestid_test.go`、`internal/store/requestid_internal_test.go`、`internal/protocol/cachehit_fixture_test.go`。修改人 jinpenga。
> v0.52 变更（用量图改按天、统计窗口改自然日，2026-08-13）：口径层 v0.55 与 DESIGN v0.14 的落地。①**窗口表达式收进 `store.windowStart(days)`**：`datetime('now','localtime','start of day','-(days-1) days','utc')`。边界在本地日历上算完折回 UTC 再比，而不是写成 `date(created_at,'localtime') >= …`——后者一样对，但整列要过一遍函数，`idx_call_logs_created_at` 就用不上了。`UsageBy` 的 WHERE 换成它，与按天分桶共用同一个下界（同一张卡上两个数必须对得上）。②**新增 `store.UsageDaily` 与 `GET /admin/api/usage/daily?days=N`**：`GROUP BY date(created_at,'localtime')`，**在 Go 里补齐空天**恒返回 days 行（SQL 只吐有行的日子，照那份结果画图会让空着的几天从横轴上消失、剩下的柱子挤在一起）。补齐用 `time.Now()` 的本地日期，与 SQL 里那个 `'localtime'` 同源。**与 `/usage` 分成两个端点**：分桶只跟 days 有关、与聚合维度无关，合在一起每切一次维度都要重算。③Web `UsageChart` 重写：入参从 `UsageRow[]` 换成 `DailyUsage[]`，`CHART_TOP`/`tokensOf` 退场；没有调用那天高度真为 0（有调用但没报 token 的仍留 0.6% 一线），标签隔着摆（`AXIS_LABELS = 8`）而柱子一根不少。CSS 一行没改——`.usage-chart` 那套本来就只关心「几根柱子、各多高」。④卡片标题改「概览」「调用记录」。⑤测试 `logsquery_test.go` 加 `TestUsageDailyBuckets`（恒 days 行、最后一行是本地今天、空天为 0）。修改人 jinpenga。
> v0.51 变更（用量页下拉与流水翻页，2026-08-13）：PO 两条裁决的落地，后端一行未动（DESIGN v0.13 记设计侧立论）。①**模型筛选换 `Picker`**（`fields.tsx` 既有控件），`.select-inline` 随之删除，全站不再有原生 `<select>`。补了一处原生下拉本来就在撒谎的地方：选中的模型不在当前选项集里时（天数从 7 天切到 1 天，这段时间没出现过它，而筛选条件还生效着），补一项带「这段时间没有」注记的选项回去，不让触发器显示成「全部模型」。②**流水的「加载更多」换成上一页/下一页 + 当前页码**：`useLogFeed` 从「增量追加」改成**游标栈**——before 游标没有逆向形式，上一页的起点算不出来，只能是来时压栈的那一个；翻页只留 `go(nextStack)` 一个入口（页码与请求必须同时改），筛选变更与「刷新」都回第一页（时间序的表，最新那批只可能在第一页）。每次多要一条（`limit = LOG_PAGE + 1`，后端 `maxLogLimit` 500 兜得住）专门用来判「还有没有下一页」，多的那条不显示、也不做游标：拿「这页正好拉满」当判据的话，总行数是整页倍数时「下一页」会翻进一页空表。`.load-more` 更名 `.pager`。修改人 jinpenga。
> v0.50 变更（用量页最近调用列改版，2026-08-13）：①`call_logs` 加 `is_stream`（可空 INTEGER，§7 DDL）——同步/流式落库。可空因为 stream 是解析请求体那一步才知道的（与 `model_requested` 同一行赋值），鉴权失败那类行与存量老行停在 NULL 读作「不知道」，给 0 默认值会把它们全说成同步；`store.migrate` 走既有 ALTER 模式，落库判据借 `requestedModel != ""`（两者同源）。②Web 用量页「最近调用」三处列改版：「链路」改「端点」，入站/上游上下排布、每行自带标注（原箭头式单行把同协议折叠成一枚芯片，省宽度但哪枚是哪边靠箭头方向猜，`.route-arrow` 样式随之删除）；API 密钥从时间列的次行独立成列；新增「类型」列显示同步/流式（NULL 显示 —）。`/admin/api/logs` 响应随之多 `is_stream` 字段（`*bool`）。测试 `callrow_test.go` 钉三态（非流式 0 / 流式 1 / 没解析到请求体 NULL）。修改人 jinpenga。
> v0.49 变更（#72 canonical Usage 归一为毛值，2026-08-13）：口径不变（#63 差距清单之 G4 已裁决），本条只记实现层落点。①**`protocol.Usage.InputTokens` 定死为毛值**（§4.2）：`Tap.Summary` 仍保留上游原始语义不动，归一只在 canonical 这一层做。②Anthropic **解码侧**（`anthropic/decode_response.go` 的 `usagePayload.canonical`）把 `cache_read_input_tokens` + `cache_creation_input_tokens` 加进 `InputTokens`，缓存两项照留作明细；加法**按帧做不跨帧累加**——`message_start` 与 `message_delta` 各是一份完整快照（实采两帧都带全套缓存字段）。③Anthropic **出口编码侧**（`anthropic/encode.go` 的 `usageBody`）减回净值：减法与钳零收在 `Usage.NetInput()` 里、与解码侧那个加法互为逆向（两半各自内联会让 §4.2 的毛/净口径在两个文件里各写一遍无名算术）。要减是因为 A 上游契约里 `input_tokens` 与缓存两项互不相交、客户端自己相加，不减等于把缓存重复计一遍；要钳零是因为上游口径不一致时减成负数不是合法 Anthropic 响应。④CC 与 Responses 两侧的 `usageBody` **一字未动、自然变正确**（两边的 input 本就是毛值），`total_tokens` 因此不再低估。⑤新增 `Usage.MergeSnapshot`（非零字段覆盖）与 `Usage.NetInput`（见③），替掉五处 `EvUsage` 的整结构体赋值（anthropic 流式/非流式、openaicc 流式/非流式、openairesponses）与 `openaicc/decode.go` 里手写的那份同构逻辑——`event.go` 早写着「后来者的非零字段覆盖」，五处实现都没兑现，某些只发 `{"output_tokens":N}` 的兼容上游会被清零 input。这两件事**必须同时成立**：归一后只带 output 的那一帧解出 `InputTokens=0`，没有非零合并就会把 `message_start` 的毛值抹掉。⑥测试落 `internal/protocol/usage_test.go`（合并四例）+ 两侧编解码用例 + 三条集成用例的期望值随之改（A→CC 毛值 128、A→R 毛值 128、CC→A 净值 58）。版本号跳过 v0.47/v0.48——那两号已被 #71 分支（PR #76）占用。修改人 jinpenga。
> v0.48 变更（#71 止血档落地：压缩 turn 明确拒绝，2026-08-13）：v0.47 设计的止血半边按原文实现，本条只记实现时补定的五件事，口径不变。①**闸的位置改了**：不落在 `decodeInput` 的未知 item 分支（那会让 `DecodeRequest` 对一份**合法** Responses 字节报错，撞 §5 的「decode 必须是全函数」契约，且 codec 是纯函数、拿不到渠道能力位），改为在 `server.relay` 选完渠道之后、透传/转换分岔之前统一判（`internal/server/compaction.go`）——两条路的判据不同（转换路径无条件拒；透传看能力位）但收场是同一句拒绝，一处判、一条日志、一个错误词。判据函数 `openairesponses.HasCompactionTrigger` 只扫 `input` 一层、逐项单独解，解不动一律返回 false（它是**拒绝**的判据，宁可漏判退回今天的行为，也不能因解析口味差异拒了普通请求）；能力位为是的渠道一个字节都不扫。`decodeInput` 的 default 分支留了一条指路注释。②**形态定为发上游之前的普通 400**（口径层 v0.54 留给实现定）：trigger 在读完请求体时就认得出来，响应头还没发，流式与非流式共用同一条路；流内 `response.failed` 要先承诺 200 再在流里塞失败，没有理由。③**error 词表加第四词 `compaction_unsupported`**（前三词见 v0.44②）：网关拒的、没碰过上游，与 `upstream_error` 分开。④**能力位默认「否」**（PO 2026-08-13 裁定）：`channels.supports_compaction INTEGER NOT NULL DEFAULT 0`，老库走 `store.migrate` 的既有 ALTER 模式。这是这批唯一一处**行为会变**的迁移——今天真支持压缩的 Responses 透传渠道要人去勾一下才继续放行；立论是代价不对称（位错成否 = 一条点名「去渠道页勾上」的 400，位错成是 = 复现本票要杀的静默 Fatal）。管理端 PUT 缺省不动列，哨兵用 `nil` 不用零值——`false` 在这里是有意义的默认取值，借零值当哨兵会让一个勾过的渠道在别处保存一次就被静默关掉（同 v0.44 的 `max_concurrency` 陷阱，但更险）。表单那一栏只在勾了 Responses 时露出、也只有露着才传。⑤`POST /v1/responses/compact` 是一行裸路由（不挂鉴权与流水中间件：无条件拒绝的端点，先撞 401 只会让人以为它存在但没授权），回 501 + 固定文案。用户文档指引（G5）落 README「接 Codex CLI」一节。§7.6 记实现细节。修改人 jinpenga。
> v0.47 变更（口径层 v0.54 落地：Responses 入口压缩兼容裁决，2026-08-13）：只落设计与分票，实现未排期（#71/#72/#73/#74）。①转换路径 compaction 两步走：止血 = `decodeInput` 遇 `compaction_trigger` 明确报错 + drop 日志 + 渠道 compaction 能力位（布尔位，为否的**透传**渠道对压缩 turn 同样拒绝——Responses 形状 wire ≠ 支持压缩）；正式修法 = 本地合成（#74，照 opencodex：summarizer turn 剥工具面注入压缩 prompt、encode 抑制常规 item 累计文本、合成恰好一个信封 item、截断 turn 不产 item），配套回带还原（`compaction`/`compaction_summary`/`context_compaction` 输入项信封可解还原 `SUMMARY_PREFIX + 摘要` user 消息、解不开降级占位 + 日志、清空悬挂 pendingReasoning）与 SSE 静默期心跳。②canonical `Usage.InputTokens` 约定改**毛值**（#72）：Anthropic 解码加回 `cache_read + cache_creation`、A 出口编码减回，`usageBody` 不动自然变正确；EvUsage 整结构体覆盖改按字段非零合并（`encode.go:129-134` 与 `event.go:41-46` 承诺不符的一行 bug）。③`POST /v1/responses/compact` 路由 501 + 文案（#71）。④§5 坑清单补三条：恰好一个 item 与信封长期兼容、合成期 SSE 静默心跳、压缩 turn 排除在 previous_response_id 展开缓存外。⑤golden 前置（#73）：小窗口 config.toml `model_context_window` 人工触发，抓压缩 turn 发包 + 上游 SSE + 回带形态三段，落 `testdata/golden/`。修改人 jinpenga。
> v0.46 变更（#41 CC 出口丢块补登记，2026-08-13）：一处实现层止血，**行为不变**，口径不变（口径层 §2.6 早就写着「丢弃 + 日志警告」，这里是实现没兑现）。`openaicc` 的丢弃常量表补 `DropVendorContent`，`joinBlocks` 补 `default` 分支登记它——原来只 `case` 了 `BlockText` 与 `BlockThinking`，其余块落空即丢，于是 Anthropic 入口发一张图路由到 CC 上游，图在编码时无声消失、上游收到一个被改写成纯文本的请求、还照样 200 回来，正是 #32 在 Anthropic 出口判过「不行」的同一形态漏在另半边。`BlockToolUse` / `BlockToolResult` 显式排除在 `default` 之外并单独用例钉住：它们由调用方各自编成 `tool_calls` 与 `role=tool` 消息，不是在这里丢的，混进去会让每个工具轮都报一条「未知内容块」、这张表就再没人看。§4.6 现状表 ③ 划掉。图片真做转换仍是 #33（还挂着格式白名单与 PDF 两条待裁），届时这一支被图片那一路缩小到「真的没对等形态的那些」。修改人 jinpenga。
> v0.45 变更（口径层 v0.53 落地：用量与观测第二轮 #62，2026-08-13）：①`call_logs` 加 `error_detail`（可空 TEXT，§7 DDL）——上游错误原文前 2KB，三个来源：透传路径在 `status >= 400` 时挂一个限长旁路 observer（不先读后转，那条链路上响应字节属于客户端）、转换路径由 `writeUpstreamError` 顺手交出已读到的原始字节、传输错误那一支存 `upstream.Redact(err)`（没有响应体，不落的话「连不上/握手失败/读超时」这半边恰好永远是空）。**新出现的列组合**：上游透传 4xx 的 `error` 列是空的（v0.28 纪律）而 `error_detail` 有值，故管理端「详情」按钮按 `status >= 400` 出、不按 error 非空——`captureWriter` 因此从写死 64KiB 改为构造时给 limit。②`/admin/api/logs` 加 `model` / `key` / `only=bad` 筛选与 `before=<id>` 游标（§8.1）：筛选下推后端，前端在已拉回的一页里过滤筛出的是「这一页里的失败」；翻页取游标而非 offset，流水新行插在头部，offset 第二页必错位（`offset` 参数保留，无 `before` 时生效）。③`UsageBy` 加 `key` 维度（按 `api_key_name`，空归「(未鉴权)」）。④Web：用量页维度三档（按模型 / 按 API Key / 按上游凭证，末档写全称消歧）、模型下拉单独按模型维度拉一次（维度切走时选项不该跟着变空）、「加载更多」增量追加、失败行可展开摊开上游原文；渠道页凭证行名字定宽 10em、掩码吃掉剩余（DESIGN.md v0.12）。⑤测试落 `internal/server/errordetail_test.go`（两条路径 + 2KB 截断 + 传输错误不带 base_url + 成功行为 NULL）与 `logsquery_test.go`（筛选叠加、游标翻页不重不漏、`by=key`）。修改人 jinpenga。
> v0.44 变更（§7.5 实现落地 #60，2026-08-13）：渠道并发闸按 §7.5 原样实现，本条只记实现时补定的三件事。①配置项名与形态定案：`concurrency_queue` 块下 `factor`（倍数形态：队列上限 = 并发上限 × factor，显式 0 = 不排队，零值陷阱同 `max_retries`）/ `wait: 30s` / `retry_after: 10s`（两个时长兜底、factor 不兜），§7 样例已列。②error 词表补第三词 `queue_abandoned`：排队途中客户端断连，status 记 499、不写错误体——不混进 `upstream_error`（没碰过上游）也不占 v0.52 的两词（那两个是网关拒的，这个是客户端走的）。③信号量手写移交式（`internal/upstream/gate.go`）而非现成库：上限每次获取时从渠道配置带入，改配置即时生效、缩小时自然排空。管理端渠道表单加「并发上限」一栏（`max_concurrency`，PUT 缺省不动列，防 v0.35⑸ 整体覆盖陷阱——0 有意义，哨兵用 null 不用零值）。四断言验收测试照 §7.5 原文落在 `internal/server/concurrency_test.go`。§6 时序与两处 DDL 注记的「实现未排期」字样一并清扫。修改人 jinpenga。
> v0.43 变更（口径层 v0.45~v0.48 落地补记：渠道页两栏与凭证可回读，2026-08-12）：该批同日落地且早于 v0.42 的内容，头部条目当时漏了，终审（#58）点出后补记。界面侧落点（主从两栏、右栏三段、启停开关、地址预览、左栏搜索）在 DESIGN.md v0.5~v0.10 版本记录，本文档动的只有凭证可回读那半（口径层 v0.47/v0.48）：①`api_keys` 加 `key_plain` 列——明文与哈希各存一列，存量行空串读作「原值没存过」，界面提示删了重建、不摆假掩码；鉴权仍走 `key_hash` 唯一索引，与这一列无关。②`key_hash` 裸 SHA-256 的立论加注：「明文只在创建那一个响应里存在过」的前提自 v0.47 不成立，结论不变且更无所谓——明文就在同一张表的隔壁列，加盐慢哈希保护不了任何东西。③§8.1「凭证先删后插作废」条随 v0.47 改写：去掉「值不回读 ⇒ 页面对不齐」那半条立论（已不成立），列表改回名字、凭证值、状态等。④散文两处旧称「网关 key」改「API Key」（v0.48 术语：网关侧一律 API Key，上游侧写全「上游凭证」）。修改人 jinpenga。
> v0.42 变更（口径层 v0.49~v0.52 落地：渠道并发上限设计，2026-08-12）：只落设计，实现未排期。①新增 §7.5：数据模型（`channels.max_concurrency`，0 = 不限）、挂点（`upstream.Client.Do`，内存态信号量）、持有区间（一次 `Do` 内重试与换凭证共占一个闸坑）、排队（队列 ×1 / 超时 30s，config.yaml 全局项，客户端断连即出队）、队满/超时 429、拥塞期零改动（含三个「改到要回头复核 v0.51」的既有事实锚点）、观测与验收（`queue_wait_ms` 一列 + error 词表 `queue_full`/`queue_timeout` 两词、Go 集成测试四断言）。②§7 DDL 两处注记：`channels.max_concurrency` 与 `call_logs.queue_wait_ms`。③§6 时序补渠道并发闸一行。排队两个界与 `Retry-After` 的配置项名与形态实现时定（§7.5），启动配置样例暂不列。修改人 jinpenga。
> v0.41 变更（口径层 v0.43/v0.44 落地：模型级探测 + 选取模式移位，2026-08-12）：口径见口径层 v0.43/v0.44，这里只记实现层落点。①新增 `upstream.ProbeModel`：带模型名的最小真实请求，CC 与 Anthropic 用 `max_tokens: 1`，Responses 用 `max_output_tokens: 16`（OpenAI 给该字段定了 16 的下限）；OpenAI 官方推理系模型（o 系、gpt-5 系）拒收 `max_tokens` 会落成 400 →「说不清」，**不迁就**——兼容型上游对不认识的字段各有脾气，而 400 的固定词表已写明「模型多半存在」。三态摘要用我方固定词表不带上游原文（上游错误文案可能带 base_url），传输错误过 `Redact`。②`store.ChannelProbeTarget` 加 `Models`（启用中的纳管模型 + 各自协议子集，**按存的原样**给出不与渠道集取交——探测答「上游有没有」，与路由取交集是两个问题）。③`POST /channels/:id/probe` 回包加 `models` 与 `model_credential`；矩阵并发压 4（子路径层维持串行防中转按并发判限流，矩阵这层的请求形状就是普通推理流量，串行在 20 模型 × 8s 超时的最坏情形要等三分钟）。④Web：探测结论加模型矩阵段（✓/✗/`? 状态码` 三态，非颜色线索；只有确定的「不通」才把左线转警告色——凭证 401 时整格「说不清」，画警告等于每次喊狼来了）；挑选面板第一组无条件默认展开；`key_mode` 的 `Segmented` 从渠道表单移进凭证池对话框、≥2 把（含停用）才显示，改了立即 PUT；渠道表单提交时**不传** `key_mode`——后端对缺省是整列不写（v0.35 ⑸），比回传 prop 上的旧值安全（凭证池那边刚改过的话表单里的 prop 还是老的）。⑤**矩阵默认不跑，靠 `?models=1` opt-in**（本 PR 自动 review 揪出，口径层 v0.43 ①「只由人手点」）：探测接口是保存渠道后自动调一次的（v0.33，那时它发空 `{}` 不花钱），矩阵直接挂进同一个响应等于每改一次 base_url 就静默打出「模型数 × 协议数」次真实推理；口径层其实早就自洽——v0.33 那句写的是「保存渠道时朝**勾选的子路径**各发一次」，跑偏的是实现。写成 opt-in 而不是 `?models=0` 的 opt-out：将来漏传参数，前者的代价是少一层提示，后者的代价是花钱。修改人 jinpenga。
> v0.40 变更（口径层 v0.42 落地：项目改名 Portage，2026-08-11）：纯标识符改名，无行为变更。①Go module `github.com/SimonGino/ai-gateway` → `github.com/SimonGino/portage`，`cmd/gateway` → `cmd/portage`，构建产物 `bin/portage`、镜像内 `/portage`。②容器侧：镜像名 `portage:local`、配置路径 `/etc/portage/config.yaml`、compose 服务名 `portage`；**数据卷名 `ai-gateway-data` 保持不变**——改名会让下次 `compose up` 建一个空卷而把库留在旧卷里，是这批标识符中唯一改名有真实代价的一个（§11）。③`AIG_ADMIN_PASSWORD` → `PORTAGE_ADMIN_PASSWORD`（§7）、会话 cookie `aig_admin` → `portage_admin`、gin context key `aig.*` → `portage.*`、`GET /v1/models` 的 `owned_by` → `portage`。④网关 key 前缀 `sk-aig-` → `sk-ptg-`（§8）：**存量 key 不失效**，`internal/auth` 拿整串算 SHA-256，全仓没有一处解析前缀，因此不需要迁移也不需要兼容期。⑤`testdata/golden/` 一字未动——那是实采转录存档，改里面的字节等于篡改证据；`scripts/redact-inbound-*.jq` 经查不认前缀（按 header 名脱敏），不需要跟着改。⑥两份文档的历史版本记录不改写。修改人 jinpenga。
> v0.39 变更（PR #42 自动 review 的四条，2026-08-11）：一条走口径（见口径层 v0.41），三条纯实现层。①**`GET /v1/models` 的接入点半边也按交集过滤**（口径层 v0.41）：判据抽在 `store.deadAccessPoints`，返回「每个候选的交集都为空」的接入点 id。写成「有没有一个候选活着」而不是「有没有一个候选死了」——M0~M2 单候选下两种等价，**等价的时候正是把它写对的时候**，M4 放开多候选后一个死候选不该抹掉整个接入点。一个候选都没有的接入点不在这个集合里（照列），那种形状 `checkSingleCandidate` 本来就拒，这里不替它改判。②**`channel_models.protocols` 的值合法性进启动闸**（新增 `checkModelProtocols`，与 `checkChannelFields` 并列）。只拦「值不合法」，**不拦交集为空**——后者是运行期状态（口径层 v0.40 ②），拦了等于让渠道少勾一个协议把进程掀翻。不拦值不合法的后果是 v0.21 通则点名的形态：进程照常起来、第一个打到它的请求才 500，而 `ListChannels` 又把解不动的值吞成空数组显示为「继承」（`admin.go:174`），页面上根本看不出哪里不对；进了启动闸之后这种库起不来，那处显示问题随之消失。③**管理端在单协议渠道上仍须显示模型协议子集**：原条件 `protos.length > 1` 会在「渠道从多协议缩成一个、而这个模型的子集不含它」时把整格藏起来——**那正是它变得不可用的那一刻**，藏了就既没有过期标记也没有清除入口，v0.40 ①「照实显示存量值」在最需要它的场景失效。条件改为 `protos.length > 1 || 存量子集非空`。④**拉取失败的协议侧不得参与推断**：`listedOn` 把「这一侧没拉到」（`models` 为 null：401 / 超时 / 回的不是 JSON）与「拉到了但没列出它」压成同一个「不在里面」，批量添加据此写库，等于凭零证据砍掉一条本来可能原生可走的协议路径、把请求推去做有损转换。新增 `listComplete`（每个协议侧都真拉到了列表），为假时一律留继承；建议气泡与「上游列表里没有它」也一并改吃它——同一份证据只该有一个成色判定。修改人 jinpenga。
> v0.38 变更（口径层 v0.40 落地：纳管模型协议子集 + 拉上游模型列表，2026-08-11）：口径见口径层 v0.40，这里只记实现层落点。①**`channel_models` 加 `protocols` 列**（§7），默认空串；迁移 `store.addModelProtocols` 用 `ALTER TABLE … ADD COLUMN`。**存量行不回填、迁移前后行为一字不变**——ALTER 加的列必须有默认值，而这里默认值的语义（空串=继承渠道全集）恰好就是老库当下的语义，这是这一列敢用 ALTER 加的前提。跑没跑过仍问 `pragma_table_info`（`hasColumn`），不建版本表，与 v0.31 那条一致。②**`protocol.Set.Intersect` + `store.pickProtocol` 收两列**（原收一列）：渠道集与模型子集取交集之后再 `Choose`。空串走继承、**不进 `ParseSet`**——它对空输入是报错的（「支持协议集不能为空」），而这一列的空恰恰是最常见的正常值。③**三种失败分两档，不能混**：两列**解析**失败 → 500（启动闸扫过全部未停用渠道，真走到这儿说明库是运行中被手写 SQL 改坏的）；交集**为空** → `ErrNoUsableCandidate`（503），它与「渠道停用」「凭证归零」同一种「现在用不了」，报 500 会把人引去查数据损坏。接入点与直连两条 resolve 路径同改，各自 SQL 多带一列 `cm.protocols`。④**M4 的顺序约束记在 `resolveAccessPoint` 注释里**：改加权抽取时，交集为空的候选必须在抽取**之前**排除，不能像现在这样抽完了才由 `pickProtocol` 发现——单候选下两者等价，多候选下死候选会白占一份权重。⑤新增 `upstream.ListModels` / `ListModelsFor` 与 `POST /channels/:id/fetch-models`（§8.1）：两家共用 `GET {base_url}/v1/models`、都回 `{"data":[{"id":…}]}`，故不做各家 URL 特判（new-api 为此养的那张渠道类型表是 §6.1 明确不取的复杂度）；`openai` 与 `openai_responses` 共用同一次拉取（分两次打同一个 URL 只是白费一趟）。认证头复用转发路径的 `applyHeaders`，理由同 `Probe`——要问的正是「按我们发请求的方式打过去，上游认为我们能看见什么」；**这也是它能区分协议的原理**：同一个 `/v1/models`，带 Bearer 与带 `x-api-key` 打过去，聚合型中转会回各自视角的列表，`gpt-4o` 出现在前者不出现在后者就是「它只走 openai」的依据。超时 12s（比 `Probe` 宽，几百条的序列化本身就比一个 400 错误体慢）、响应体封顶 2MB。**拉回来的模型名原样保留不归一化**——它要拿去跟纳管模型名逐字比对，大小写与前缀都是语义的一部分。⑥管理端：纳管模型的增/改 body 带 `protocols`，PUT **不传该字段 = 不动它**（`*[]string` 而非 `[]string`，否则「没提到」与「清空」两种意图在 JSON 里长得一样）；渠道卡加拉取按钮，结果只摆进表单、刷新即消失，与探测同档。⑦**`GET /v1/models` 的直连清单同样按交集过滤**：交集为空的限定名当下就是调不通的，列出来等于给 harness 挖坑（「列出来的必须调得通」是口径层 v0.32 ③）。交集判据抽成 `store.usableProtocols`，**路由与清单共用同一个函数**——本批第一版正是漏了清单这一处，而漏得掉的原因就是两处各算各的。解析失败的行也不列（那种行打过去回 500，同样不属于「当下真能打通」），整个清单不因一行脏数据而 500，理由同 `created_at` 那处的 COALESCE。接入点那半边当时留着没滤，理由记成了「口径认下的」——**那句是错的，已由 v0.39 ① 更正**：v0.32 ③ 只管直连半边的理由是「启动闸兜不住它」，而交集为空同样不进启动闸，同一条理由覆盖两边。修改人 jinpenga。
> v0.37 变更（#33 图片跨协议转换的载体与采样定形，2026-08-11）：口径见口径层 v0.39，这里只记实现层落点，**尚未写代码**。①新增 §4.6：`BlockImage` 的载荷用**结构化字段** `{MediaType, Data, URL}`，不用 data URI 字符串。参考实现 sub2api `apicompat/` 走的是 data URI 当枢纽，那在它的点对点架构下只需两个 helper 就兜住六条路；hub-and-spoke 下每个 codec 半边都要重解一次那串，而 Anthropic 侧要的本就是拆开的 `media_type` + `data`。更要紧的是 **URL 得有地方放**——sub2api 的 `AnthropicImageSource` 没有 URL 字段，非 data URI 的图到 Anthropic 方向静默消失（不下载、不报错、不记日志），根在载体表达不了 url 形态，不是编码侧疏忽。②同处记三条可照抄的细节：空 base64 载荷要挡、`media_type` 为空兜底 `image/png` 且用例钉住、**`tool_result` 里的图片要「抬」成后续独立 user 消息**（Responses 的 `function_call_output.output` 只收字符串）——第三条是此前没记过的转换约束，进 canonical 覆盖表。③新增 §4.6 的**现状表**：通读三个 codec 得出四处卡点，其中 `openaicc/encode.go:302` 的 `joinBlocks` 只认 text/thinking、**其余落空且不登记**——Anthropic 入口带图打到 CC 上游今天就是无声消失的，#32 补的登记只落在 `anthropic/encode_request.go` 那一侧，CC 出口这半边漏了。它不必等图片转换整体落地，补一句登记即可先止血。另外三个入口对同一件事给出三种 `Kind`（`"image"` / `"image_url"` / 一律 `BlockText`），Responses 那行连类型判别式都丢了。④`BlockImage` 的字段由 `{MediaType, Data, URL}` 扩为 `{MediaType, Data, URL, FileID}`，对齐 Anthropic `image.source` 的三种形态；`FileID` 跨协议登记后丢弃且**单独一个丢弃项**，不混进 `DropVendorContent`（口径层 v0.39）。⑤图片样本用**真的极小图**（几百字节真 PNG）：不用手写假 base64 串（golden 库口径是真实字节存档，掺假串等于往事实里掺伪造，与「stub 是道具不进转录库」同线），也不截断存 hash（往返验不了，而往返正是这格唯一值得测的东西）。修改人 jinpenga。
> v0.36 变更（#7 Anthropic 侧验收回写，2026-08-11）：均为实现层记录，口径不变，无代码改动。①§9 补「M0 必抓子集补齐」——`anthropic-*` 六个入库、12 个样本零 skip，并记下三处**样本与现实的出入**：中转恒给 `input_tokens` 加 357、cache 计数全 0（`cache_read_input_tokens` 的解析路径仍只有 `cc-*` 走到）、响应头保真度因中转有响应头白名单而**验不了**（`request-id` / `anthropic-ratelimit-*` 要等官方 key）。②§6.1 补 Anthropic 侧白名单实测复核：2026-08-06 那条走的是 Codex + Responses，Anthropic 半边一直靠推断，这次拿 Claude Code 打真网关 + 只打印所见的假上游逐条对过，**白名单原样成立**（私有头丢弃、`anthropic-version` 缺省补 `2023-06-01`、`anthropic-beta` 整条转发、`?beta=true` 照抄、顶层 `model` 翻译且其余字节未动）。③同处记下 `metadata.user_id`（含 `device_id`/`account_uuid`/`session_id`）**原样到达上游**且**不动它**：这是 v0.24「体除顶层 model 外逐字节相等」与「白名单只管头」两条口径的合成结果，为拦它去改写请求体，代价是承认网关可以按自己的判断删客户端字段，比泄露一个 device_id 危险。④`count_tokens` 的调用时机改记为**随 harness 版本变**（08-07 那版每轮先打一次、08-11 这版整轮没打），两条实测并存，网关侧只保留「不能当作启动必经一步」这个结论。⑤`scripts/seed-example.sql` 修两处会让干净库一行都灌不进去的错：列名 `protocol` → `protocols`（v0.31 改过名），`channel_keys` 补 `name`（v0.38 起用量与日志按它归因，留空的行到 M4 再也分不清是哪份凭证）。修改人 jinpenga。
> v0.35 变更（口径层 v0.38 落地：渠道凭证池从 M4 前移至 M3，2026-08-10）：口径见口径层 v0.38，这里只记实现层落点。①**临时闸只拆凭证那一半**——`store.checkSingleCredential` 由「恰好 1 份」改判「≥1 份」，`checkSingleCandidate` 一字不动；`Resolve` 返回的 `Candidate.Credential` 单值扩成凭证列表 + 选取游标，`store` 里那条 JOIN 的 `LIMIT 1` 语义随之改变，改动面止于 `upstream`。②`channel_keys` 加 `name` 与 `UNIQUE(channel_id, name)`，`call_logs` 加 `channel_key_name`（快照冗余，非 id——删凭证是常事，存 id 会把历史 join 空）；两条都进 `store.migrate`，老库的已有凭证补名 `凭证 1`，老流水该列留空串。③**摘除只认 401**（403 换而不摘、429 换而不冷却），落在 `upstream/retry.go` 判定处；摘除写 `disabled_reason`/`disabled_at`，恢复只有管理端那一个按钮，**不加任何定时任务**。④`retry` 配置加 `max_attempts`（默认 6），与 `max_retries` 是两层，零值陷阱同 `rate_limit_qps` 处理。⑤`SetChannelCredential` 的先删后插退役（§8.1 那条实现口径随之划掉），换成 `/admin/api/channels/:id/credentials` 逐条 CRUD + 追加式批量粘贴；GET 只回名字/状态/时间/停用原因，凭证值仍无任何读接口。⑥`ChannelProbeTarget` 由「取一把凭证」改为返回全部凭证（含已停用），`Probe` 结果按凭证分行；仍不落库、不进路由。⑦`ChannelSummary.HasCredential` 布尔改可用/停用计数；`UsageByModel` 旁加一个按 `channel_key_name` 聚合的查询，`/admin/api/usage` 加 `by` 参数。⑧Web：渠道卡凭证区改列表（名字 + 状态 + 停用原因），`key_mode` 用既有的 `Segmented` 单选（不是下拉），日志页加「上游凭证」列，用量页加维度切换。**落地时定的五处细节**：⑴`UNIQUE(channel_id, name)` 落成独立唯一索引（ALTER 加不了约束，见 §7）；⑵`call_logs.retry_count` 由「同候选重试次数」扩义为「全部重打次数，含换凭证」——跨凭证之后前一个语义已经指不到任何东西，而这一列的用途（这次怎么慢了）两者都覆盖；⑶轮询游标挂在 `upstream.Client` 而不是 store 的包级变量，渠道 id 在每个测试库里都从 1 开始，包级 map 会让两个用例共享同一个游标；⑷按凭证聚合时凭证名为空的行**分两档**——渠道名也为空的是真没走到上游（鉴权失败、模型不存在）归「(未走到上游)」，渠道名不空的归「(未记录凭证)」（绝大多数是迁移前的老流水，少数是选出候选后、发出请求前就失败的），一档装两种会把老流水说成没走到上游；⑸`PUT /channels/:id` 请求体里**没有 `key_mode` 时不写该列**（其余字段仍整体覆盖），它 v0.38 才露到表单上，在服务端补默认会把配好 `random` 的渠道静默改回轮询。⑷⑸ 由 #36 的评审发现。修改人 jinpenga。
> v0.34 变更（PR #32 的自动 review 三条，2026-08-10）：均为实现层。①**CC 解码侧补 `developer` → `RoleSystem` 归一**。canonical 没有 `RoleDeveloper` 是已定口径（`protocol/request.go` 的 Role 注释，PO 确认），`openairesponses` 早就这么折，CC 入口漏了。后果实打实：Anthropic 出口只把 `RoleSystem` 上提到顶层 `system`，其余非 assistant 一律当 user，于是一条 developer 系统提示降格成用户内容、还跟紧随其后的 user 合并成一条。钉这条的用例走**全链路**而不是单测——归一在 CC 侧、上提在 Anthropic 侧，分开看两边都「对」，错的是中间那一环。②`cmd/goldenrec` **先 `Normalize` 再 `Valid`**。`Valid` 故意不收旧协议名，而 `GOLDENREC_PROTOCOL` 是手写的、不经过库迁移，`protocol.go` 的注释里本来就点名它是别名要兜的读侧入口，实现却漏了——已有的采集环境会当场被打死。③`anthropic.encodeBlocksFiltered` 的 `default` 分支**补登记 `DropVendorContent`**。认不得的块（CC 的 `image_url` / `input_audio`，由解码侧刻意留住以免带图请求当场 400）此前静默蒸发：客户端发了张图，上游收到一个被改成纯文本的请求，还照样 200 回来，日志一个字都没有。与 `BlockThinking` 那一格的区别单独用例钉住——thinking 是**口径**定的必然丢弃，每次都丢，登记等于每请求一条噪声；这一格是「我不认识这个东西」，恰恰需要看见。修改人 jinpenga。
> v0.33 变更（口径层 v0.36 落地：协议取值改名，2026-08-10）：口径见口径层 v0.36，这里只记实现层的落点。①全仓 `openai_cc` → `openai`：Go 常量 `protocol.OpenAICC` 一并更名为 `protocol.OpenAI`（值与常量名脱节比多改一处更难读），golden `meta.json` 的 `protocol` 字段一并改——它记的是「哪个 codec 录的」，协议改了名记的还是同一件事，证据本身（`request.json` 与 SSE 转录）一字未动。**包名 `openaicc` 与 golden 目录名 `cc-*` 保持不变**：内部标识，跟着改只会搅动全部 import 而换不来任何对外收益。②`protocol.Normalize` 收旧名、`Valid` 不收：别名与枚举分开，混在一起的话某天 `Set.String()` 会把旧名重新写回库里。`ParseSet` 在校验前折一次，顺带解决 `openai,openai_cc` 这种折完重名的去重。③`store.migrate` 新增 `renameOpenAICC`，改 `channels.protocols` 与 `call_logs` 的两列。channels 那条用 `REPLACE` 而非等值比较（集合是逗号分隔的，旧名可能夹在中间），子串替换在这里安全——另两个取值都不含 `openai_cc`。**不设「跑过没有」的标记**：改完库里再没有旧名，第二次跑就是零行命中，幂等本身就是守卫；用例跑两遍钉这一点。原 v0.33 列改名那条迁移的用例种子改回**当时真实写进库**的 `openai_cc`，于是它现在一路串起两次迁移。④管理端：`PROTOCOL_LABEL` 改为 OpenAI / OpenAI-Responses / Anthropic（**Responses 是复数**，参照的截图写成单数是那个产品的笔误，OpenAI 官方端点就是 `/v1/responses`）；新增 `PROTOCOL_SOON` 与 `SegmentedMulti` 的 `soon` 占位项渲染 Gemini（置灰、点不动）。占位项与 `options` 分开传而不是给 `Option` 加 `disabled`：它们的 value 根本不在 `Protocol` 里，混进去就得把类型放宽成 `string`，真正的取值也跟着失去检查。修改人 jinpenga。
> v0.32 变更（#9 M2-7 CC→A 转换落地，2026-08-10）：均为实现层，口径不变。①§2 路径矩阵 CC→A 打勾；`openaicc` 入口半边落地后，剩下的 A→R 与 CC→R 两格**所差的是同一个半边**（`openairesponses` 出口半边），它做完就是 9 格全开。②新增 §9.3 记 CC→A 的用例分工与已知缺口。③`openaicc.Codec` 成为**第二个带每请求状态的 codec**（§5 那条实例生命周期的第三个住户）：`includeUsage` 由 `DecodeRequest` 从 `stream_options.include_usage` 读出、交给 `EncodeStream` 决定发不发流末 usage 帧——事件流里没有这个信息，只能从请求侧传过来，与 `openairesponses.customTools` 同构。④修一处 **#25 遗留的缺陷**：`temperature` 的 clamp 早在 §2 有损转换策略里写死（Anthropic 0~1、OpenAI 0~2），但 R→A 的实现一直原样转发 OpenAI 域的值，客户端发 1.8 就是一个必被上游 400 的请求。clamp 落在 `anthropic/encode_request.go`（**截断不缩放**——缩放会悄悄改掉每个请求的采样行为），一处修好 R→A 与新开的 CC→A 两条路。⑤下行 CC 流的工具调用 `index` **重编成 0..n-1**：canonical 的 `Index` 原样携带上游序号，而 Anthropic 那边它是内容块下标（正文占 0，工具从 1 起），CC 客户端拿它当 `tool_calls` 数组下标用，直接透传会在数组里留一个空洞。⑥闸门反例换靶：`TestFallbackDoesNotOpenAnUnimplementedPath` 与 `openai_test.go` 里那条「CC 入口打到 anthropic 渠道」原本拿 CC→A 当「没落地」的例子，这一格开了之后它们测的是一条不再存在的行为，改指向仍关着的 CC→R。修改人 jinpenga。
> v0.31 变更（口径层 v0.33 + v0.34 落地，2026-08-10）：渠道单值协议 → 支持协议集。①`channels.protocol` 改名 `channels.protocols`，值是逗号分隔的集合；`store.migrate` 做这次改名（`ALTER TABLE … RENAME COLUMN`），值不动——单值在新语义下就是一元集合。**这是本项目第一条迁移，故不建版本表**：迁移跑没跑过直接问 `pragma_table_info` 就知道，比维护一个会和实际形状漂移的 `schema_version` 更可信。②新增 `protocol.Set`（有序切片，非 map——最多三个元素，且顺序要稳定地存回库、显示在管理端）与 `Set.Choose`：入站在集合里就用它，否则按 `fallbackOrder = [cc, responses, anthropic]` 取第一个。③`store.Resolve` 增一个 `inbound protocol.Protocol` 参数，`Candidate.Protocol` 的含义从「渠道的协议」变成「**本次选定的**协议」，下游（子路径拼接、codec 选取、tap 选取、call_logs）一律不用改。④新增 `upstream.Probe` 与 `POST /admin/api/channels/:id/probe`：发空 JSON 体、用 404/405 与其余状态区分子路径存不存在（判据不是 2xx——空体会被任何真实上游拿 400/401 回绝，而那恰恰证明路由存在；也因此不花钱）；结果不落库、不参与路由，前端只放在渠道卡上做提示，刷新即消失。⑤管理端新增 `store.InvalidInput`（携带中文原因的错误类型，映射 400）——协议集填空以前会走成一句「保存失败」的 500。⑥Web：`Segmented` → 新增的 `SegmentedMulti`（多选、至少留一个、按选项顺序而非点击顺序归一），渠道卡列出全部协议短名。顺带修一处早就错的表单提示：Base URL 原文写「到版本段为止，例如 https://api.example.com/v1」，与 §6.1 的「协议子路径之前」矛盾，照着填会拼出 `/v1/v1/chat/completions`。修改人 jinpenga。 ⑦跟随口径层 v0.34：渠道名禁含 `/`（`ChannelInput.normalized` 校验 + `checkChannelFields` 复查），否则限定名 `渠道名/纳管模型名` 有两种拆法而 `resolveDirect` 的 `LIMIT 1` 静默挑一个。⑧删渠道/纳管模型被候选引用时报 `ErrInUse` 并点名接入点，不走外键那条把因果说反的通用文案；不做级联（删渠道顺手抽走候选会让接入点空候选，`checkSingleCandidate` 下次启动即拒）。原编号 v0.30 与 #27 撞号，合并时顺延为 v0.31。
> v0.30 变更（#27 M2-6 入站 CC 样本采集，2026-08-09）：均为实现层，口径不变。本条**叠在 #26（v0.29）之上**（合并时两条记录并存，见上一行）。①§9 入站样本段补第三套语料 `in-cc-*` 六份（opencode 1.18.4 实采），并记下 harness 选型的**事实**：Codex CLI 0.144.1 已不支持 `wire_api = "chat"`（二进制内写死，提示改用 `responses`），CC 入站样本采不到；转而用 opencode，它走 `@ai-sdk/openai-compatible` 直接 POST `/v1/chat/completions`。②采集中发现两条转换约束，均已进 canonical 覆盖表：**CC 的工具结果是每个调用一条独立 `tool` 消息，Anthropic 是全部挤进同一条 user 消息**，CC→A 编码侧要做合并而非逐条平移；**`stream_options.include_usage` 不能丢**，入口半边的 EncodeStream 要靠它决定回程补不补 usage 帧。③§9 stub 应答段补第四条实现口径：`GOLDENREC_SIDECALL=notools` 旁路豁免，**默认关闭**。④脱敏工序补一条**教训**：只隔离 `XDG_CONFIG_HOME` 不够，opencode 会把 `~/.agents/skills/` 下的个人 skill 清单塞进 system prompt，须连 `HOME` 一起换。修改人 jinpenga。
> v0.29 变更（#25 M2-5 R→A 转换落地，2026-08-08）：均为实现层，口径不变。①§2 路径矩阵 R→A 打勾，并重算各格所差的 codec 半边——按**边际成本** ④（A→R，只差 openairesponses 出口半边）比 ③（CC→A / CC→R，各差 openaicc 入口半边，而它一个方法都没有）便宜，与口径层 §2.1 的排序相反，是否调序待 PO 裁决。②修一处 #12 遗留的**缺陷**：custom 工具的包装规则有三个必须逐字对称的面（声明 / 出站包装 / 回程拆包），#12 只做了后两个——发给上游的工具声明是空的，没有任何东西告诉模型该回 `{"input": …}`，模型回个别的形状，回程拆不动只好原样给出去，Codex 拿到一段 JSON 当 JS 跑。三件事收进 `protocol/customtool.go`，往返对称由用例钉住（§5 坑清单同条目）。③§5 新增「第二个住户」：`anthropic.Codec` 的 `DefaultMaxTokens` 走 `codecs.New` 的必填 Options 注入，**不在 `convert.go` 对 canonical 无条件填**——那会让已上线的 R→CC 在客户端没给上限时开始悄悄截断。④订正 §5 一处悬空引用：那条实例生命周期原写「v0.32 定」，而口径层的版本记录只到 v0.31，v0.32 从来不存在——它是实现层决定，本就不该按口径层编号，改为按 issue 引用，两条一并标注「待 PO 追认」。⑤新增 §9.2 记 R→A 的用例分工与五条已知缺口，其中「上游 thinking 必然丢弃、Codex 看不到 Claude 的推理过程」是**用户可感知的退化**，单独点名。修改人 jinpenga。
> v0.28 变更（M1 落地 + 容器打包，2026-08-08）：实现层，口径不变。①新增 §11.1 容器打包——容器只是单二进制的一种分发方式，不改口径层 §2.8 的部署形态；记下三个实测坑（scratch 缺根证书 → 全 502、命名卷属主照搬镜像 → 启动即 `unable to open database file (14)`、容器内 `listen` 必须 `0.0.0.0`）。②M1 实现中两处判断落档：`ttft_ms` 只记流式（非流式填了约等于总耗时，混合流量下「平均首字延迟」失去意义，非流式的首字节耗时仍在 slog）；`error` 列写网关自己的固定词表而非上游原文（上游文案里可能带 base_url），上游自己回 4xx/5xx 的**透传成功**行不算网关侧错误、该列留空。修改人 jinpenga。
> v0.27 变更（M1 开工口径，2026-08-08）：跟随口径层 v0.27 收敛 C6 与 Issue #22 的四条裁决。§7 `api_keys` 表加注：`key_hash` 是 SHA-256 裸哈希、`allowed_models` M1 只建列不校验、**无 `expires_at` 是对的**（v1 不做过期，两份文档就此一致）。新增 §7.1 写明这三条各自的理由——尤其 hash 算法：鉴权是每请求必走的路径，要吃 `key_hash` 唯一索引，加盐则 hash 不可索引须扫全表逐行比，bcrypt 更是每次十毫秒级，而那是为「防拖库后爆破人选密码」付的代价，自生成高熵串没有那个威胁。修改人 jinpenga。
> v0.26 变更（#11 M2-2 A→CC 转换落地，2026-08-08）：均为实现层，口径不变。①§5 接口补 `DecodeFullBody`——v0.25 定稿只有 `EncodeFullBody`，非流式转换路径的解码侧无处落脚，是**定稿漏项**；备选「非流式也向上游发流式再聚合」被否，理由见 §5（上游看到的请求与客户端发的不是一回事；断连时手里只剩半截事件序列而客户端等一个完整 JSON）。PO 拍板并确认（jinpenga）。②新增 §4.5「A→CC 出口的丢弃与代价」，五项各写明后果，其中 `metadata.user_id` 与 `cache_control` 是 #11 验收明列的两项；丢弃一律走 relay 的 warning 日志，不静默。③§5 坑清单补四条实测：工具分片输出按**首次出现**而非 index 数值排（index 不保证从 0 起、不保证连续）、CC 无逐条工具终止符故只能攒到流末尾冲出、上游响应 id 原样下发不重编 `msg_…`、转换路径**不转发客户端 query**（#20 的「整串照抄」只管同协议透传）。修改人 jinpenga。
> v0.25 变更（#10 M2-1 canonical 模型定稿，2026-08-07）：§4 重写、§5 接口定稿并落骨架，均为实现层，口径不变。原 v0.2 的 canonical 草案照协议文档拍，本次拿 9 份真实 harness 入站样本逐字段核过，**草案被证伪四处**（§4.3）：`System string` 装不下带 `cache_control` 断点的 system 数组；role 集合装不下 Anthropic mid-conversation-system beta 塞在 messages 中段的 system 消息；`Tool` 的 name/description/JSON-schema 三件套装不下 Codex 的 lark 文法 custom 工具与 Claude Code 的服务端工具；`EvToolArgsDelta{JSONFragment}` 建立在「工具入参必是 JSON」这个不成立的不变量上（Codex code-mode 的入参是 JS 源码）。同时立两条规矩：①**装得下 ≠ 转得过去**——decode 必须是全函数，跨协议丢什么是 encode 侧的决策，「记为丢弃」与「无处存放」不是一回事，§4.4 列显式丢弃清单及代价；②逐键路径的归宿清单**只存在于 `internal/protocol/canonical_coverage_test.go`**，文档不抄第二份（两份必漂移），该测试双向红，写表时当场逮出漏掉的字段。§5 补两条实测坑（工具入参非 JSON 时编码到 CC 的后果、Codex 并行只发生在 code-mode 内部故不能拿它验交错重组）。其中两处提交 PO 拍板并获确认（jinpenga）：Responses `developer` 角色 decode 归一为 `RoleSystem`（R 出口方向再展开回 `developer`），以及 §4.4 那三项显式丢弃。修改人 jinpenga。
> v0.29 变更（口径层 v0.32 落地，2026-08-10）：纳管模型直连寻址实现化。（本行原误标为 v0.25，与「canonical 模型定稿」那条重号，v0.30 时更正，内容未改。）①`store.Resolve` 改为**先接入点、后直连**的分派器——只有「没有这个接入点」才继续试限定名 `渠道名/纳管模型名`；接入点存在但候选不可用是另一回事，降级去试直连会把「候选停用了」报成「模型不存在」。②限定名的匹配放在 SQL 里拼 `ch.name || '/' || cm.upstream_model = ?`，**不在 Go 里按 `/` 切**：渠道名和纳管模型名本身都可能含 `/`（`anthropic/claude-3` 这类 OpenRouter 风格的模型名很常见），切在哪一刀上没有通用答案，拼起来比对根本不用切。③直连路径不进启动闸（它没有 `candidates` 行），「有这个名字但现在用不了」只能在请求时发现，故 `resolveDirect` 落空后再查一次「忽略 disabled 是否存在」，据此分 404 与 503——一律 404 会让人以为名字打错了。④`callRecord.accessPoint` 更名 `requestedModel`、结构化日志字段 `access_point` 改 `requested_model`：这一列记的是客户端填的那个名字，限定名和接入点名在里面平权（`call_logs.model_requested` 列名本来就是对的，不用动）。⑤§8、§7.1 随之改写。
> v0.24 变更（#20 修复，2026-08-07）：§6.1 补「客户端查询串整串照抄」——透传口径原文只规定了 body（「除顶层 `model` 值外逐字节相等」）与请求头白名单，查询串既不在白名单也不在丢弃清单里，是**漏项**不是裁决过的行为。PO 裁定不过滤、整串照抄（jinpenga）：查询参数不像请求头那样天然带客户端指纹，且各家 harness 的私有参数不可穷举，白名单在这里没有可枚举的对象。
> v0.23 变更（M2-1 入站样本实采回写 #10，2026-08-07）：两条实测观察落档，均为实现层，口径不变。①§6.1 白名单段补**反例**——某些中转站的 Anthropic 端点靠 `user-agent` + `x-app` 判定客户端，白名单转发一律 503。结论仍是**白名单不放宽**（为迎合一家中转站撤掉「不泄露本机指纹」这条口径，代价与收益不对等），绕法在配置层：Anthropic 配一条不设该闸的独立上游。顺带说明 goldenrec「转发照抄、落盘白名单」为何不算双标——防指纹外泄的对象是 git 仓库不是上游。②§9 补 `log_bodies` 的实测量级——Claude Code 2.x 单轮请求体 **185 KB**（42 个 tool 定义占大头），Codex CLI 0.144.1 是 47~50 KB，即 64 KiB 上限对前者是**几乎必截断**而非偶尔越过。不改 `bodyCaptureLimit`（排障日志该有这个上限），改的是读日志时的预期：`truncated` 在真实 harness 下是常态不是故障信号。
> v0.22 变更（M2-1 入站样本采集 #10，2026-08-06）：§9 补「入站样本」这一类——golden 库自此分 `direction: upstream | inbound` 两类，`cmd/goldenrec` 随之加 inbound 模式。新决策一条：**没有对应协议的真实上游时，用手写 stub 应答驱动 harness 走完多轮**（PO 裁定 jinpenga）。依据 = `A→CC` 最难啃的输入是第二轮那个带 `tool_result` 的请求体，而 harness 只有先收到过一个合法 tool 调用响应才会发出它；手上没有 Anthropic / OpenAI 官方 key（#7 仍挂着），纯录制回 501 只能采到第一轮。stub 是道具不是样本：不保真、不进转录库，入库的只有 harness 发出来的真实请求字节。
> v0.21 变更（M2 同候选退避重试落地 #13，2026-08-06）：v0.16 定的口径实现化，均为实现层，口径不变。①§6 重试段的「次数：可配，默认值随 M2 实测定」定稿为 `max_retries: 2` / `base_delay: 500ms` / `max_delay: 10s`（量级对齐 M0 实测的 Codex 5xx 退避 0.22→0.45→0.84→1.62s）；②补两条实现期新决策——退避抖动取**半区间** `[d/2, d)` 而非全区间（全区间会让实际退避短于上游明说的 `Retry-After`），`Retry-After` 超过 `max_delay` 时**不重试**而非照等（照等等于把客户端扣在网关一分钟，不如把那份 429 原样交出去让调用方自己决定）；③§7 配置补 `retry` 块并说明「块缺席 = 用默认、显式写 0 = 关闭」；④§7 `call_logs.retry_count` 注明对应的结构化日志字段 `retries`（仅非 0 时记，0 是常态、每行都背个恒 0 字段没意义）。
> v0.20 变更（M0 遗留修复 #14，口径层 v0.21，2026-08-06）：①§6.1 超时分层补上**拨号**这一层——零值 `http.Transport` 用无超时的 `net.Dialer`，后面几个超时都在 TCP 连上之后才起算，地址被黑洞时请求挂到操作系统放弃。②§7 启动校验补一条——未停用渠道的 `base_url` 必须是带 host 的绝对 http/https 地址、且不带查询串与 fragment（后者会把协议子路径整个吞掉，请求永远打错地方），且该错误**不回显 `base_url` 原值**（可能带 userinfo）。两条均由 #8 / #15 的自动审查提出、人工核实后修复；②是口径层 v0.18「配置能过校验但请求时才炸不算合法状态」的同类缺陷换个写法，口径层同步 v0.21 把该原则从枚举改为通则。
> v0.19 变更（M0 收官，2026-08-06）：§6.1「放弃解析」的粒度定稿为**丢那一帧**（v0.13 提出时标的待裁，PO 裁定 jinpenga），该段由待裁改为定稿。
> v0.18 变更（M0 验收回写，2026-08-06）：三条实测与 #1 规格的出入落档，均为实现层，口径不变。①§6.1 头部白名单加实测复核结论——录下 Codex 全部请求头逐条对照后**不放宽**白名单（私有头丢弃不影响整轮工具调用，且 `X-Codex-Turn-Metadata` 携带 `installation_id`）。②§8 `/v1/models` 注明**不迎合** Codex 期待的 OpenAI 私有目录格式（无公开契约、字段随版本漂移；实测降级路径可用，只多两条 warning）。③§5 坑清单补 Responses reasoning 的 `encrypted_content`——上游侧不透明密文，P1 跨协议转换必然作废，M2 须有用例钉住「带它的 input 不使转换报错」。
> v0.17 变更（口径层 v0.20，2026-08-06）：harness 分档修订——CC 入口的必过 harness 由 Codex CLI 改为 pi（§0 占位假设 7、§10 验收清单）。依据 = Codex CLI 0.144.1 移除 `wire_api = "chat"`（openai/codex#7782），实测 `Error loading config.toml: wire_api = "chat" is no longer supported`；pi 0.83.0 已经网关跑通 CC 整轮工具调用（#6）。
> v0.16 变更（口径层 v0.19，2026-08-06）：恢复同候选退避重试并从 M4 提前到 M2——§6 故障转移流程加最内环（429/5xx/网络错误且未写首字节触发，401/403 与其余 4xx 不重试，退避以 `Retry-After` 为下界）；§9 测试矩阵作废「harness 自身重试逻辑生效」一条；§11 M2/M4 描述随之调整。依据 = M0 验收实测（#6）：网关侧 429 逐字节透传成立，但 Codex 0.144.1 对 429 一次即弃、对 503 才退避，单上游限流时无人自愈。PO 裁定只取「同候选重试」，不放开临时闸单候选、候选间转移仍留 M4（jinpenga）。
> v0.15 变更（M0 联调期，2026-08-06）：§7 启动校验的可达性判定扩到**渠道 / 纳管模型 / 凭证三者**，与 `Resolve` 的 JOIN 逐条对齐。v0.14 只覆盖渠道与凭证，漏了 `channel_models.disabled`——同一类错三种写法只拦两种；PO 裁定「纳入」（jinpenga）。口径层同步 v0.18。
> v0.14 变更（M0 联调期，2026-08-06）：§7 启动校验补一条——未停用接入点的 weight>0 候选，其渠道必须未停用且有启用凭证。原规则字面上不含这条，实测「只停渠道、忘了停接入点」能通过启动、接入点仍挂在 `/v1/models` 上、请求时才 503；PO 裁定改为启动即报（jinpenga）。
> v0.13 变更（M0-4 实现期，2026-08-05）：§6.1 明确超限判定与分块无关、补 Tap 两条硬约束的实现落点；§9 补 golden 样本库的目录结构、`cmd/goldenrec` 录制流程与 `verified` 人工关卡——以上为实现层展开，口径不变。另：§6.1 「放弃解析」的粒度（丢那一帧 vs 丢整条流）是对验收条款的重新解读，已按「丢那一帧」实现并标注**待 PO 裁定**，未按口径变更处理。
> v0.12 变更（M0-3 实现期，2026-08-05）：`GET /v1/models` 实现落在 `internal/server` 而非 §3 列出的 `internal/admin/`，理由与待办见 §3 脚注。仅记实现偏离，口径不变。
> v0.11 变更（M0-1 实现期，2026-08-05）：§6.1 补「模型名翻译走字节级 splice」——PO 裁定接入点对外名 → 纳管模型名的改写在 M0 就做，透传保真口径精确为「除顶层 `model` 值外逐字节相等」。另：路由解析（`Resolve`）实现落在 `internal/store` 而非 §3 列出的 `internal/router/`，理由与待办见 §3 脚注。
> v0.10 变更（M0 开工前实现层展开，2026-08-05）：临时闸校验时机拆为「启动时校验单候选单凭证 / 请求时校验协议匹配」——接入点本身不绑协议，协议匹配只能到请求时才判（§7）；golden 样本子集提前到 M0 采集，因 Tap 测试需真实转录作输入（§9/§11）；新增 §6.1 透传实现细则（上游 URL 拼接、请求/响应头规则、流式转发与超时分层）。均为实现层展开，口径不变。
> v0.9 变更（口径层 v0.17，2026-08-05）：初始渠道集改为 Anthropic / OpenAI / Gemini Vertex AI / 阿里百炼（oMLX、DeepSeek、硅基流动移出）；Vertex 与百炼走 OpenAI 兼容端点（协议矩阵不动）；渠道凭证类型 `api_key` / `service_account` 二选一，key 池泛化为凭证池（§0/§7/§11/§12）。
> v0.8 变更（口径层 v0.13~v0.16，2026-08-05）：harness 验收分档（必过：Claude Code、Codex CLI；顺带：pi、OpenCode）；管理员 session 鉴权细则、全局限流配置项、key 前缀 `sk-aig-` 落定（§0/§7/§8/§10）。
> v0.7 变更（口径层 v0.12，2026-08-05）：候选改绑「渠道纳管的模型」（新增 `channel_models` 表，candidates 引用之）；里程碑重排——多候选分流/候选间转移/key 池聚合实现打包 M4 置于 M3 后，M0~M2 强制单候选单 key；占位假设 #2 关闭（§0/§6/§7/§11）。
> v0.6 变更（口径层 v0.11，2026-08-05）：占位假设 #1 关闭——v1 初始渠道集定为主流五渠道；渠道多 key 聚合（`channel_keys` 表 + key_mode + 401/403 摘 key）；§6 故障转移补 key 层内环（§0/§6/§7）。
> v0.5 变更（口径层 C4 收敛，2026-08-05）：候选间故障转移定稿——429/5xx/网络错误/连接超时且未写首字节触发，剔除失败候选后剩余重新归一化权重再抽，其余 4xx 不切，无同候选重试，A-14 D3 范式无跨请求状态（§1/§6/§11）。
> v0.4 变更（口径层 C2 收敛，2026-08-05）：配置载体由 YAML 改为「最小启动配置 + 业务配置（渠道/接入点/key）全 DB」，React 管理端入 v1（M3，含渠道/接入点写界面——PO 日常高频编辑渠道）；管理端就绪前用 SQL 手工维护。里程碑与口径层统一为 M0~M3（§0/§1/§3/§7/§8/§11）。
> v0.3 变更（口径层 C1/C3 收敛，2026-08-05）：①协议转换确认属 **v1 承诺范围**，P1 = v1 内后续里程碑而非另立项，分批 ①~④ 按口径层 §2.1 优先级；跨协议校验降格为临时闸。②路由模型重写为「接入点 + 候选（渠道，上游模型名，权重）加权随机」，`routes`/「模型映射表」术语退役（§0/§2/§6/§7/§11）。
> v0.2 变更：协议转换从 P0 降为 P1。设计态考虑保留四项交付物：架构 seam（§6）、canonical 事件模型（§4）、codec 接口与坑清单（§5）、golden 素材（§9），均按定稿标准维护。
> 定位：个人使用的 AI 模型网关，参考 new-api 的转发内核重写，只做转发 + 协议转换 + key 鉴权 + 调用日志，不含任何运营功能。

## 0. 待确认的占位假设

以下四项中，#1/#2/#7 是起草时的默认假设（**正式开工前须替换为真实值**），#9 已决策定稿：

| # | 项目 | 当前假设 | 待确认 |
|---|------|---------|--------|
| 1 | ~~上游渠道~~（已决 v0.17） | v1 初始集：Anthropic 官方、OpenAI 官方、Gemini Vertex AI（OpenAI 兼容端点 + SA 凭证）、阿里百炼（OpenAI 兼容）；渠道多凭证聚合见 §7 `channel_keys` | 已决 |
| 2 | ~~接入点清单~~（已决） | 运营数据不冻结。候选 = 渠道纳管模型 + 权重（§7）。M0 验收集：`claude-sonnet-4-5`→Anthropic 官方；一个 CC 透传接入点→百炼（qwen 系）或 OpenAI 官方，各单候选 | 已决 |
| 7 | ~~目标 harness~~（已决） | 必过档：Claude Code（Anthropic 入口）、Codex CLI（Responses 入口）、pi（CC 入口）——三者一人盖一条入口协议，挡里程碑验收；顺带档：OpenCode（不挡，坏了再修）。v0.20 修订：Codex CLI 0.144.1 移除 `wire_api = "chat"`，CC 入口改由 pi 承担 | 已决 |
| 9 | ~~转换方向优先~~（已决） | 协议转换属 v1 承诺，P0 仅同协议透传，P1 按口径层 §2.1 优先级分批；设计态考虑见 §2 | 已决 |

其余采用默认值：单用户（单管理员）、最小启动配置 + 业务配置全 DB、React 管理端（M3）、每 key 可限 allowed_models、默认不记录请求体。

## 1. 目标与非目标

**目标**

- 对外提供 3 种协议入口：OpenAI Chat Completions、OpenAI Responses、Anthropic Messages
- 上游支持同三种协议出口；**P0 仅同协议透传，协议转换为 P1（属 v1 承诺范围，非另立项）**（设计态考虑，见 §2/§4/§5）
- 同候选退避重试（429/5xx/网络错误且未写首字节触发；v0.19，实现在 M2；详见 §6）与候选间故障转移（同触发条件加连接超时；剔除失败候选、重新归一化加权再抽；实现在 M4；详见 §6）
- API key 鉴权（可多张 key，区分调用来源）
- 调用日志（含 token 用量，用于排障与自查用量）
- React 管理端（M3）：渠道 / 接入点（候选+权重）/ key / 用量查询，构建产物 embed 进单二进制

**非目标（明确不做）**

多用户与注册、额度/计费/支付/兑换码、上游模型列表自动同步、批处理、文件上传、音频、图像生成、模型微调。

## 2. 协议支持矩阵与分期

入口 × 出口 共 9 格：

| 入口 ↓ / 出口 → | Anthropic | Chat Completions | Responses |
|---|---|---|---|
| Anthropic Messages | **P0 透传** | P1-① 转换 ✅#11 | P1-④ 转换 ✅#80 |
| Chat Completions | P1-③ 转换 ✅#9 | **P0 透传** | P1-③ 转换 ✅#80 |
| Responses | P1-② 转换 ✅#25 | P1-① 转换 ✅#12 | **P0 透传** |

✅ = 已落地并放开闸（`server/convert.go` 的 `conversionOpen`）。**#80 之后九格全开**，没有哪一格还回「该转换路径尚未实现」。

各格所需的 codec 半边（每个 codec 分「入口半边」`DecodeRequest`+`EncodeStream`+`EncodeFullBody` 与「出口半边」`EncodeRequest`+`DecodeStream`+`DecodeFullBody`）。**三个 codec 的两半都已齐全**：anthropic（入口 #11 / 出口 #25）、openaicc（出口 #11 / 入口 #9）、openairesponses（入口 #12 / 出口 #80）。九格全开，矩阵这一节自此不再有「所差半边」。

`conversionOpen` 也因此换了职责：它不再是「逐格放开」的临时闸，而只挡一件事——**没有上游对应端点的入口端点**。那就是 `/v1/messages/count_tokens`：它与 `/v1/messages` 的入口协议同为 anthropic，命中非 anthropic 渠道时没有可转的上游端点。判据必须按端点而不按协议，原因即在此。

> **口径层 v0.80 改了这一格的收场**：「没有上游对应端点」不再等同于「回 501」——CC / Responses 出口改为**网关本地估算**回 200，只有 Anthropic 出口才转发原生端点。于是 `conversionOpen` 对 `count_tokens` 的返回值不再是「拒绝」而是「换一条本地路」，闸的形状要跟着改。实现见 [#18](https://github.com/SimonGino/portage/issues/18)，**代码尚未落地**；在那之前本节描述的 501 行为仍是现状。

排序曾有争议：按**边际成本**④ 比 ③ 便宜，与口径层 §2.1 的排序（③ 先于 ④）相反。**PO 裁定不调序**（v0.35）：这两个半边同属 M2 收尾的一批，做完就是 9 格全开，边际成本的差别在「两个都要做」的前提下不成立；而 ③ 的输入契约（六份 `in-cc-*` 样本，#27/#28）刚落地，采集时带出的两条约束已进 canonical 覆盖表，趁热做省一次重新进入成本。#9 收敛了 `openaicc` 入口半边，#80 一票把 `openairesponses` 出口半边与最后两格一起落地，两格共用同一个半边，本就没有分两票的理由。

- 分批号 ①~④ 即口径层 §2.1 实现优先级：**①** A→CC、R→CC（主诉求：harness 挂第三方便宜模型）；**②** R→A（Codex 用 Claude）；**③** CC→A、CC→R；**④** A→R（允许滑到最后）。
- 首批特性集 = 纯文本 + tool calls（含并行调用）+ system prompt + 停止原因 + usage；图片、count_tokens 估算、thinking 精细策略等横切增强随 ③④ 批排期。
- Responses 无状态化（`previous_response_id` 处理）随 ① 的 R→CC 一并落地。**（#12 已落地）** 实现是最简形态：`DecodeRequest` 直接丢掉 `previous_response_id`，连 `Extras` 都不进——留着它等于把一个**上一个上游**才认得的句柄带在身上，任何编码侧顺手带出去，上游要么报找不到、要么接到别人的会话上。上下文靠 harness 全量携带的 `input` 重建，与 sub2api 的 `RemovePreviousResponseIDFromBody` 同路子。

**「设计态考虑」落为三条硬约束：**
1. **管线 seam 现在就定型**（§6）：同协议走原始字节透传，异协议走 canonical 编解码；P1 只是填充后一路，seam 位置不变。
2. **canonical 事件模型与 codec 接口现在就按定稿标准写**（§4/§5），P1 开工不重设计。
3. **golden 样本 M1 就采集**（§9），刻意选「同语义、双协议」场景，天然构成 P1 转换的黄金输入对。
- 有损转换策略（已决）：
  - thinking / reasoning（**口径层 v0.62 改，v0.65 再补，v0.73 扩到六条**）：**出向合成**（六条路径对称、流式非流式都做、`signature` 省略、**不看客户端的 `display` 偏好加闸**），**回带一律丢 + 登记**；同协议透传保留。原文「跨协议丢弃 + 不做伪映射」已被推翻，理由见口径层 v0.62 与 v0.65 ⑥。
  - 请求侧思考参数（**口径层 v0.65 定，v0.72/v0.73 放开方向**）：**只映 effort 一维、同域字符串直传、不折算数字**，**六条路径全通**（Anthropic 侧载体统一 `output_config.effort`，实测合法且单发即开思考，不写老式 `thinking.budget_tokens`）；域外值不钳，Anthropic 官方值域实测五档 `low|medium|high|xhigh|max`；没映过去的维登记 `thinking_param`。
  - cache_control：仅「出口为 Anthropic」时保留；转往其他协议时静默剥离。
  - temperature：Anthropic 区间 0~1，OpenAI 0~2，转换时 clamp。
  - count_tokens（**口径层 v0.80 定案**）：Anthropic 出口原样转发；**CC / Responses 出口本地估算回 200 `{"input_tokens":N}`**，不再回 501。估算不承诺与上游一致、不作对账依据、不进计费。原文「P0 回 501 风格错误；P1 做字符估算」的 P1 至此提上来并锁死承诺边界，实现见 [#18](https://github.com/SimonGino/portage/issues/18)（**代码尚未落地**）。

## 3. 模块划分

```
cmd/gateway/main.go        # 装配：config → store → server
internal/config/           # 最小启动配置加载；业务配置读 DB，校验 + 变更热生效
internal/auth/             # API key 中间件：hash 校验、allowed_models 过滤
internal/router/           # 模型名 → 有序渠道列表解析
internal/protocol/         # canonical 事件模型（P0 定稿，§4）；codec 接口（P0 定稿，P1 实现，§5）
  internal/protocol/anthropic/      # 每协议包内两层：Tap（P0）+ Codec（P1）
  internal/protocol/openaicc/
  internal/protocol/openairesponses/
internal/convert/          # canonical 之间的请求级归一（其实是 codec 内部实现细节）
internal/upstream/         # HTTP client、SSE 读取、failover 驱动
internal/logging/          # 调用日志写库、查询
internal/store/            # SQLite：channels、access_points、candidates、api_keys、call_logs、settings
internal/admin/            # 管理面：session 鉴权、渠道/接入点/key CRUD、用量查询、SPA 分发
internal/webui/            # 前端 embed，build tag 二选一（embed.go / stub.go）
web/                       # Vite + React 源码；产物落 internal/webui/dist（不进 git）
```

关键模块职责：

- **`protocol/<proto>`**：包内两层——
  - **`Tap`（P0，最深模块之一）**：旁路解析同协议透传流，只提取 usage / 模型 / stop reason 供日志，只读不改流。透传保真优先级最高——P0 **不做** decode→encode 转码，避免 canonical 模型丢字段。
  - **`Codec`（P1，最深模块）**：实现 §5 接口，协议怪癖（tool call 增量重组、stop reason 映射等）全封在里面；P1 落地时 Tap 复用 Codec 的解码器。
- **`upstream`**：负责把一次「canonical 请求 + 渠道」打成真实 HTTP 调用，返回事件流或错误；驱动 failover。
- **`router`** 与 **`auth`** 保持浅薄，不藏逻辑。

> **实现偏离待裁（v0.11）**：M0-1 把接入点解析（`Resolve`，返回命中候选 + 其渠道连通信息）实现在 `internal/store` 而非本节列出的 `internal/router/`。理由：临时闸下解析就是一条 SQL，单开一个只做转调的包是空壳。代价：`store` 同时管 schema、启动校验与解析，职责在发散。M4 上多候选加权分流时解析会长出真正的逻辑，届时要么拆出 `internal/router/`、要么本节按实际改写——请 PO 在 M4 排期时一并裁定。
>
> ~~**实现偏离待裁（v0.12）**~~ **已裁（口径层 v0.28，2026-08-08）**：`internal/admin` **只收管理面**；`GET /v1/models` 与 `/healthz` 是业务面，留在 `internal/server`。理由即当初提请裁定的那条——`/v1/models` 是 harness 走 API Key 打的业务端点，放 `admin` 会让「业务面 vs 管理面」的边界糊掉，而这条边界正是两套凭证彻底分离的依据。本节模块表已按裁决改写。

## 4. 内部事件模型（canonical events）

> **v0.25 定稿（#10 M2-1）。** P0 透传路径不经过本模型（由 Tap 旁路解析），本节锁定 M2 转换的语义底座。
>
> 原 v0.2 草案照协议文档拍，本次拿 9 份**真实 harness 入站样本**（Claude Code 2.x 五份、Codex CLI 0.144 四份）逐字段核过，草案被证伪四处，见 §4.3。
>
> 代码：`internal/protocol/request.go`、`internal/protocol/event.go`。逐键路径的归宿清单在 `internal/protocol/canonical_coverage_test.go` 的 `coverage` 表，**它是穷举的事实源，本节只讲为什么**——路径清单不在文档里抄第二份，两份必然漂移。该测试双向红：样本冒出表上没有的路径红，表上留了样本已无的路径也红。

贯穿全模型的一条原则：**装得下 ≠ 转得过去**。canonical 层的职责是让 `DecodeRequest` 成为**全函数**——任何该协议的合法入站字节都有地方放；跨协议丢什么，是 encode 侧按口径做的决策。「记为丢弃」和「无处存放」是两件事，前者是决策，后者是缺陷。

四类归宿，`coverage` 表逐路径标注：

| 归宿 | 含义 |
|---|---|
| `field` | 有对应的 canonical 结构体字段，跨协议能转 |
| `extras` | 进 `Extras`，同协议原样取回，跨协议由 encode 侧按口径决定丢不丢 |
| `opaque` | 整棵子树按原始字节保留（JSON Schema、lark 文法、工具入参），不解开也不下钻——键序与数值精度都可能影响上游行为 |
| `dropped` | 显式丢弃，且本节 §4.4 写明后果 |

### 4.1 请求侧

```go
type Request struct {
    Model       string
    System      []Block      // 块序列，不是字符串——块上带 cache_control 断点
    Messages    []Message
    Tools       []Tool
    ToolChoice  ToolChoice   // Mode: ""|auto|none|required|tool
    MaxTokens   int          // Anthropic 必填；OpenAI 来源零值时由 default_max_tokens 补
    Temperature *float64
    Stop        []string
    Stream      bool
    Effort      string       // 思考档位，原样字符串不归一（口径层 v0.65）；零值 = 客户端没说
    Extras      map[string]any
}

type Message struct {
    Role    Role      // system / user / assistant / tool
    Content []Block   // 纯字符串 content 退化为单个 text 块
    Extras  map[string]any
}

type Block struct {
    Kind       BlockKind    // text / thinking / tool_use / tool_result / image
    Text       string
    Image      *Image       // Kind==image：{MediaType, Data, URL, FileID}
    ToolCall   *ToolCall    // Kind==tool_use
    ToolResult *ToolResult  // Kind==tool_result
    Extras     map[string]any  // cache_control / signature / encrypted_content
}

type ToolCall struct {
    ID, Name   string
    Args       string       // 原样载荷，不预设是 JSON
    ArgsIsJSON bool
    Extras     map[string]any
}

type ToolResult struct {
    ToolCallID string        // A tool_use_id / CC tool_call_id / R call_id，原样携带不重编号
    Content    []Block       // 字符串 content 退化为单块
    IsError    bool
}

type Tool struct {
    Kind        ToolKind        // function / custom / server
    Name        string
    Description string
    Schema      json.RawMessage // 仅 function
    Extras      map[string]any  // format(lark) / strict / type / model
}
```

角色映射，三协议对照：

| canonical | Anthropic | CC | Responses |
|---|---|---|---|
| `system` | `messages[].role=="system"`（会话中段，见 §4.3）与顶层 `system` 块 | `role=="system"` | `role=="developer"` |
| `user` | `user` | `user` | `user` |
| `assistant` | `assistant` | `assistant` | 输出侧的 `message` 项 |
| `tool` | 无——工具结果是 user 消息里的 `tool_result` 块 | `role=="tool"` 独立消息 | 无——`custom_tool_call_output` / `function_call_output` 独立 input 项 |

`developer` 是**归一，不是丢弃**（PO 确认 jinpenga，2026-08-07）：decode 收敛成 `RoleSystem`，原字符串不留；R 出口方向收到 `RoleSystem` 一律按 Responses 惯例发 `developer`。这条不对称成立是因为同协议路径根本不进 codec，没有任何链路会把 `developer` 原样转回去。两个方向在 sub2api 上都能对上（收敛 `apicompat/chatcompletions_responses_bridge.go:518`，展开 `apicompat/anthropic_to_responses.go:133`）。

`ToolChoice.Mode` 的 `required` 对应 Anthropic 的 `tool_choice.type=="any"`。本仓 5 份 Anthropic 样本**都不带 `tool_choice`**，这一格取自参考仓库而非实采。

`Temperature` 与 `Stop` 在 9 份样本里**一次都没出现过**（两个 harness 都不发），它们进模型的依据是参考仓库里的标准字段映射（`litellm/llms/*/chat/`、sub2api `apicompat/`），不是实采。`coverage` 表因此不列它们——那张表校的是「样本里的字段有没有被漏掉」，把没采到的字段塞进去只会让它恒红。

### 4.2 事件侧

三个协议的流统一归一到以下事件序列；非流式响应当作「完整事件序列一次性回放」，上下游代码不分流式两套。

```go
const (
    EvMessageStart  // {ID, Model}
    EvTextDelta     // {Text}
    EvThinkingDelta // {Text, Channel}  Channel: ""(正文) / summary / signature
    EvToolCallStart // {Index, ToolID, ToolName, ArgsIsJSON}
    EvToolArgsDelta // {Index, Text}    ——不叫 JSONFragment 了，见 §4.3
    EvToolCallEnd   // {Index}
    EvUsage         // {Usage}          累计快照，后来者非零字段覆盖先前值
    EvDone          // {StopReason}     stop / tool_calls / length / content_filter，未知一律 stop
    EvError         // {Status, Message}
)
```

- `Index` 为序、`ToolID` 为稳定标识。并行调用下 CC 的参数分片按 index **交错**到达，必须按 `Index` 缓存再按序输出。Anthropic 用 content block index，Responses 用 output_index，语义对齐。
- `EvUsage` 一条流里可出现多次：Anthropic 在 `message_start` 给 `input_tokens`、在 `message_delta` 给 `output_tokens`。语义是累计快照，消费方**按非零字段覆盖、不做加法**——统一走 `Usage.MergeSnapshot`，整结构体赋值会让只报 `output_tokens` 的兼容上游把 input 清零（v0.49）。
- `Usage` **在 canonical 归一**（与保留上游原始语义的 `Tap.Summary` 分工不同，v0.49）：`InputTokens` 定死为**毛值**（含缓存读写），`CacheReadTokens`/`CacheWriteTokens` 是它的明细而非另外两笔。CC 的 `prompt_tokens` 与 Responses 的 `input_tokens` 本就是毛值直映；Anthropic 是净值，**解码时加回**缓存两项、**出口编码时减回**（钳零）。理由：不归一则 `total_tokens = input + output` 在缓存非零时低估，Codex 按它判自动压缩触发点，会被推后到先撞上游 400。
- `ThinkingChannel` 要显式判别式而非塞 `Extras`：Responses 同时有 `response.reasoning_text.delta` 与 `response.reasoning_summary_text.delta` 两条流，语义不同（推理正文 vs 面向展示的摘要），codec 必须分支。藏进 map 等于每个 codec 各写一次魔法键查找。

事实来源：Anthropic 侧取自 `testdata/golden/raw/anthropic-*` 五份真实上游 SSE 转录；CC 侧取自 M0 语料 `testdata/golden/cc-stream-*`；**Responses 侧现在也有真实上游转录**（2026-08-14 更正，#79 —— 原文写「没有真实上游转录、事件名以 `sub2api backend/internal/pkg/apicompat/` 为准」，那是 M2-1 时的状况，此后入库了三批共十份，早就不成立了）：压缩批三份（`responses-stream-compact-*`，#73）、reasoning 批两份（`responses-stream-reasoning-*`，#93）、出口半边基础批五份（`responses-stream-text` / `-tool-turn1` / `-tool-turn2` / `-parallel-turn1` / `-parallel-turn2`，#79）。复核进度分三档：

- **已核（词表与次序）**，2026-08-10 对着真实纯文本转录比过：`openairesponses.EncodeStream` 发的事件词表与 happy-path 次序全部对上——`created → in_progress → output_item.added → content_part.added → output_text.delta ×N → output_text.done → content_part.done → output_item.done → completed`，流不发 `data: [DONE]`。
- **已核（字段级，工具与并行两条路径）**，2026-08-14 拿 `responses-stream-tool-turn1` 与 `responses-stream-parallel-turn1` 与 `EncodeStream` 实发帧逐项比：每类事件的键集我方**恒为实采的子集**，只有 `custom_tool_call_input.done` 多发 `call_id` 与 `name` 两格；`content_part` 的 `part`、`output_text.done`、终帧 `usage`（含 `total_tokens = input + output`）三处**键集逐字相等**；工具 item 无 `content_part` 那一层、终态字段名是 `input` 不是 `arguments`、`status` 走 in_progress → completed、`sequence_number` 全流连号——全部对上。逐项结论与「没对上但有意不改」的三处（多发的两格、不发 `obfuscation`、不发 `phase` 与两个 metadata 账本）记在 `internal/protocol/openairesponses/encode.go` 文件头。
- **未核**：① 终帧 `response.completed.output` 的 item 形状——手上三批全经同一个中转，那份列表是它重组的降级形态（工具 item 被改回 `function_call`、丢 `arguments`、reasoning item 不列），**这个渠道验不了**，要等官方直连；② 非流式 `DecodeFullBody` / `EncodeFullBody` 的线格——十份转录全是 `stream: true`，缺口已登记在 §9。

（修改人 jinpenga，2026-08-14。）

### 4.3 样本逼出来的修正（v0.2 草案的四处证伪）

| 草案原文 | 被谁证伪 | 改成 |
|---|---|---|
| `System string` | 5 份 Anthropic 样本的 `system` 都是**数组**，块上带 `cache_control` 断点 | `System []Block`。字符串拼接会抹平断点位置，而断点位置正是要被测的东西——脱敏口径专门保住了它 |
| `Messages []Message // role: user/assistant/tool` | `in-anthropic-tool-turn2`：序列是 `user → system → assistant → user`，一条 `role=system` 的消息在 messages **中段**（Anthropic mid-conversation-system beta），content 是纯字符串 | `Role` 增 `RoleSystem`；`Message.Content` 统一块序列，纯字符串退化为单个 text 块 |
| `Tools []Tool // name/description/parameters(JSON schema)` | 两处：`in-responses-tool-turn2` 的 `exec` 是 **custom 工具**，没有 schema，只有一份 lark 文法的 `format`；Claude Code 声明的 `advisor_20260301` 自带 `type` 与 `model`，是上游服务端工具 | `Tool` 加 `Kind`（function/custom/server）与 `Extras` |
| `EvToolArgsDelta // {Index, JSONFragment}` | 同上——Codex code-mode 的 `exec` 入参是 **JavaScript 源码**，分片拼起来也不是 JSON | 字段改 `Text`；是不是 JSON 由 `EvToolCallStart.ArgsIsJSON` 说了算。编码到 CC 的后果见 §5 坑清单 |

### 4.4 显式丢弃清单

以下三项**能装下但选择不留**，代价已知（PO 确认 jinpenga，2026-08-07）：

| 丢什么 | 为什么 | 代价 |
|---|---|---|
| 正文块边界（`content_block_start/stop`） | canonical 只留拼接后的增量流。要保边界就得在事件流里加一对纯结构事件，而三协议里只有 Anthropic 用得上 | 回编码到 Anthropic 时多块合成单块。对客户端渲染等价 |
| `additional_tools` 的容器位置 | Responses 把工具声明包在一个 `role=developer` 的 input 项里；decode 时提升到 `Request.Tools` | 「它原本是第几条 input 项」丢失。回编 Responses 按首项重建即可；转 CC/Anthropic 时本就没有对应容器 |
| `developer` 角色原字符串 | 归一为 `RoleSystem`，理由见 §4.1 | 无——没有链路需要把它原样转回去 |

不在此列、但**跨协议必然作废**的是 `signature` 与 `reasoning.encrypted_content`：它们在 canonical 层有地方放（`Block.Extras`），只是转到别的协议时无处安放。见 §5 坑清单。

### 4.5 A→CC 出口的丢弃与代价（#11 实测）

上一节是 canonical 层「装得下但不留」；这一节是 **encode 到 CC 时装不下**的。常量定义在 `internal/protocol/openaicc/encode.go`，`EncodeRequestReport` 把本次实际丢掉的项回给 relay，relay 按 `跨协议转换丢弃字段` 打 warning——**丢弃一律有日志，不静默、不假装映射**（口径层 §2.6）。

| 常量 | 丢什么 | 代价 |
|---|---|---|
| `metadata` | Anthropic 请求体的 `metadata.user_id` | 上游以此判定「是否官方 Claude Code 请求」。走本条转换路径的上游是第三方 CC 兼容服务，本就不做该判定，故实际代价为零；但**该字段无法在 CC 协议里保留是事实**，日后若出现认此字段的 CC 上游，只能另开口子。P0 同协议透传不受影响 |
| `cache_control` | system 块与消息块上的缓存断点 | CC 协议没有对应概念。后果是**上游按全量 prompt 计费**，长会话成本高于直连 Anthropic。这是选第三方廉价上游本身的代价，不是转换缺陷；断点位置在 canonical 层留着（`Block.Extras`），换回 Anthropic 出口就恢复 |
| `thinking` | thinking 块正文与 `signature`（**回带方向**，出向已改为合成，见口径层 v0.62） | CC 的 assistant 消息没有推理块位置。回带上一轮 thinking 的客户端（Claude Code 开 extended thinking 时）会让上游丢失该轮推理上下文，表现为**质量下降而非报错** |
| `thinking_param` | 思考参数里没映过去的维：`thinking.display`、`reasoning.summary`、数字预算（Qwen `thinking_budget`）、`thinking.type` 本身（`enabled` / `adaptive` / `disabled` 这个开关跨不过去，归这一档不归 `vendor_request`——它属于思考参数）（**口径层 v0.65**；`output_config.effort` 这一维走 `Request.Effort` **映得过去**，不在此列） | 客户端表达了「回不回思考正文 / 思考多少 tokens」，上游收不到。display 那一维的实际代价为零——上游是否回思考正文由它自己决定，而按 v0.65 网关一律把回来的合成给客户端；数字预算那一维是真丢，客户端只能改用 effort 档位表达。**与 `thinking` 那一档分开**：那是块级、每请求必丢的口径结果，这是参数级、恰恰要看见 |
| `server_tool` | `Tool.Kind` 非空的服务端工具声明 | Claude Code 会声明 `advisor_*` 一类由 Anthropic 服务端执行的工具，第三方 CC 上游既不认也执行不了。声明整条剔除，客户端表现为该工具不可用 |
| `vendor_request` | 入口协议独有的顶层字段（`Request.Extras` 里除已知项外的其余） | 逐项枚举会随上游 beta 漂移，故按「不认识就丢并记名」处理。日志里带得出字段名，出问题时能定位 |

`tool_choice` 的两种非法组合（引用未声明的工具、有 `tool_choice` 无 `tools`）不算丢弃而算**规整**：严格中转的第三方上游会直接拒请求，encode 侧当场消掉。见 §5 坑清单「严格中转的请求校验」。

### 4.6 图片载荷的 canonical 形状（#33，2026-08-11 定，#1 落地）

口径层 v0.37 定了图片要真做转换、v0.39 定了音频与文件类维持登记后丢弃。这里定**载体形状**，因为 hub-and-spoke 下这一个决定影响六个 codec 半边。

**结构化字段，不是 data URI 字符串**：`BlockImage` 携带 `{MediaType, Data, URL, FileID}`，三种来源各填各的一组。这三组对应 Anthropic `image.source` 的三种形态（官方 vision 文档 2026-08-11 核）：

| source.type | 字段 | canonical 落点 | 跨协议 |
|---|---|---|---|
| `base64` | `media_type` + `data` | `MediaType` + `Data` | 真做转换 |
| `url` | `url` | `URL` | 原样转发，**不代下载** |
| `file` | `file_id`（需 `anthropic-beta: files-api-2025-04-14`） | `FileID` | **登记后丢弃，单独一个丢弃项** |

`FileID` 留字段**不是为了转换**——file_id 是上游作用域的句柄，Anthropic Files API 发的 id 到 OpenAI 上游什么都不是，唯一的搬运路径「下载再重传」已被口径排除。留它是为了让丢弃日志说得出「丢的是一张 file_id 引用的图」；混进 `DropVendorContent` 就退化成一句「有个不认识的块」，而这一格恰恰是认识的。

参考实现走的是相反的路：sub2api `apicompat/` 没有 canonical，三套协议 typed struct 点对点互转（六个方向六个文件），唯一的共同货币是 **data URI 字符串**，靠 `anthropicImageToDataURI` / `dataURIToAnthropicImageSource` 两个手写 helper 收发。**那条路在它那儿成立、在我们这儿不成立**：它只有点对点，两个 helper 就把六条路兜住了；我们每加一个 codec 半边都要重解一次那个字符串，而 Anthropic 侧要的本来就是拆开的 `media_type` + `data`。

更要紧的是 **URL 得有地方放**。sub2api 的 `AnthropicImageSource` 根本没有 URL 字段（`types.go`，`Type` 注释写死 `"base64"`），`dataURIToAnthropicImageSource` 首行就把非 `data:` 开头的挡回 nil，调用点拿到 nil 直接跳过——客户端发一张 https 图，到 Anthropic 方向**静默消失**，不下载、不报错、不记日志（全包无 `http.Get`）。载体表达不了 url 形态，是这个缺口的根，不是编码侧的疏忽。载体先留住，编码侧原样转发 `source.type=url`（口径层 v0.39：**不代客户端下载**）。

三条可以照抄的实现细节：

- **空 base64 载荷要挡**——`data:image/png;base64,` 这种只有头没有身子的（含只剩空白）当没有图，别往下传。
- **`media_type` 为空时兜底 `image/png`**，并用例钉住。媒体类型往返本来不丢，这是唯一有损点，得是显式的。
- **`tool_result` 里的图片要「抬」成后续独立的 user 消息**——Responses 的 `function_call_output.output` 只收字符串，图放不进去。这是本项目此前没记过的一条转换约束：`ToolResult` 带图时三个协议的容器形状不一样，进 canonical 覆盖表。
- **`MediaType` 原样写出，不设格式白名单**（口径层 v0.77）：编码侧不做 jpeg/png/gif/webp 闸，不因 mime 丢块、不转码、不自造 400。`image/svg+xml` 转到 Anthropic 就是上游的 400，用例钉「原样发出去」即可，不必模拟上游拒收。

**现状：四处卡点**（2026-08-11 通读三个 codec 得出，动手前照这张表逐个拆）：

| # | 位置 | 现状 |
|---|---|---|
| ① | `protocol/request.go` | ~~`BlockImage` 是裸占位~~ **已落地**（#1）：`Block.Image` 带 `{MediaType, Data, URL, FileID}` |
| ② | `anthropic/decode.go` | ~~`source` 整块进 Extras~~ **已落地**（#1）：解成 `Block.Image` |
| ② | `openaicc/decode_request.go` | ~~`image_url` 全字段进 Extras~~ **已落地**（#1）：解成 `Block.Image` |
| ② | `openairesponses/decode.go` | ~~一律 `BlockText`~~ **已落地**（#1）：`input_image` → `BlockImage` |
| ③ | `openaicc/encode.go` `joinBlocks` | ~~只认 `BlockText` / `BlockThinking`，其余落空且不登记~~ **已止血**（#41，v0.46）：补了 `default` 登记 `DropVendorContent`；图片转换由 #1 另路发出 |
| ④ | `openairesponses` 出口 | ~~`EncodeRequest` 仍 `ErrNotImplemented`~~ **已落地**（#80 出口半边 + #1 图片 part） |

③ 曾是**当下就在发生的静默丢弃**：Anthropic 入口带图打到 CC 上游，图无声消失。#32 补的 `DropVendorContent` 只落在 `anthropic/encode_request.go:254` 那一侧，CC 出口这半边漏了——正是本项目判过「不行」的那种失败模式，在自己代码里。#41 单独补了这句登记，行为不变；真做转换时这一支会被图片那一路缩小到「真的没对等形态的那些」。

② 的三行不一致也要一并抹平：三个入口对同一件事给出三种 `Kind`，其中 Responses 那行连判别式都丢了，是三者里最难补的。

**样本采集**：用一张真的极小图（几百字节真 PNG），base64 进 `request.json`。不用手写假串（sub2api 测试里全是 `"aGVsbG8="`、`"iVBOR"` 这类编不出图的串——它测的是字段搬运，够用；我们的 golden 库口径是真实字节存档，掺假串等于往事实里掺伪造，与「stub 是道具不进转录库」同一条线），也不截断存 hash（往返验不了，而往返正是图片这格唯一值得测的东西）。体积不是问题，`cc-*` 单个样本比它大得多。

## 5. 转换器（codec）接口

> **v0.26 修订（#11）、v0.29 更新（#25）、v0.68 更新（#80）。** 代码在 `internal/protocol/codec.go`。落地进度：三个 codec **两半均已齐全**——`anthropic`（入口 #11 / 出口 #25）、`openaicc`（出口 #11 / 入口 #9）、`openairesponses`（入口 #12 / 出口 #80）。（v0.68 顺带订正：本行此前写「`openaicc` 只有出口半边」，那在 #9 之后就已经不成立了。）

```go
type Codec interface {
    DecodeRequest(body []byte, stream bool) (*Request, error)   // 入口请求 → canonical，必须是全函数
    EncodeRequest(req *Request, stream bool) ([]byte, error)    // canonical → 出口请求
    DecodeStream(r io.Reader) (<-chan Event, error)             // 上游 SSE → 事件流，实现负责关 channel
    DecodeFullBody(body []byte) ([]Event, error)                // 上游非流式响应体 → 完整事件序列（v0.26 补）
    EncodeStream(w io.Writer, events <-chan Event) error        // 事件流 → 下行 SSE（含分帧与 flush）
    EncodeFullBody(events []Event) ([]byte, error)              // 非流式响应聚合
    EncodeError(w http.ResponseWriter, status int, msg string)  // 协议原生错误格式
}
```

- **`DecodeFullBody` 是 v0.26 补进来的**（PO 裁定 jinpenga，2026-08-08）：v0.25 定稿只有 `EncodeFullBody`，非流式转换路径的**解码侧因此无处落脚**。备选方案是「非流式也向上游发流式请求再自行聚合」，被否——上游看到的请求与客户端发的不是一回事（计费与限流口径可能不同），且流中途断连时手里只剩半截事件序列，而客户端等的是一个完整 JSON，无法收场。实现上两侧共用同一台状态机（`openaicc` 的 `message` 与 `delta` 结构同形），解析逻辑只存在一处。
- 可选接口 `RequestEncodeReporter`（`EncodeRequestReport` 额外回一串丢弃字段名）不进主接口：只有转换路径需要它，同协议透传路径拿不到也用不上。丢弃项由 relay 侧写 warning 日志，见 §4.5。
- 可选接口 `StreamReadReporter`（`StreamReadError` 交出「这次流式解码是不是读上游读断了」）同理不进主接口。**为什么需要它**：转换路径上读断是带内往下传的——`DecodeStream` 放一条 `EvError` 就收摊，入口 codec 照常把错误帧写给客户端然后正常返回，`streamConverted` 从返回值里看不出这次断在半路，流水就会把它记成一次干净的 `200/ok`（透传路径没有这个问题：那边读错误是 `relayBody` 的返回值，直接记 `stream_aborted`）。客户端自己提前断开（Ctrl-C、超时）走的就是这条，实测在观测页上是一串「200 / ok / 0 token」——CC 的 usage 只在最后一帧，断在它之前就是 0/0。**只报传输失败，不报上游在流里回的错误对象**：后者透传路径同样记 `ok`（上游把话说完了，只是说的是坏消息），硬要在转换路径降级会让两条路的收场词表再次分叉。实现是 `protocol.StreamReadFlag` 内嵌进各 codec（带锁：编码侧遇 `EvError` 是提前 return 的，那之后解码 goroutine 还在跑）。用例：`TestConvertedStreamAbortIsLoggedAsAborted` 与 `TestConvertedInStreamErrorObjectKeepsOkOutcome`。

- 骨架统一返回 `protocol.ErrNotImplemented` 而**不 panic**：转换闸门一放开这些方法就会被真实请求打到，panic 带走整个进程，而一个能被 relay 转成 5xx 的错误只坏这一条请求。骨架期的正确行为是「明确地不支持」，不是「崩给你看」。`EncodeError` 例外——它直接委托 M0 就已落地的 `Protocol.WriteError`，错误格式不是转换逻辑。
- `EncodeError` 收 `http.ResponseWriter` 而非 `io.Writer`（草案原文如此）：它要设 Content-Type 与状态码，且这条路径只在**首字节写出之前**走得通。流一旦开头，错误就只能以 `EvError` 的形态走在流里，那是 `EncodeStream` 的活。msg 由调用方保证已脱敏——上游 key 与 base_url 严禁出现在错误回显里。
- 「协议 → Codec」的表在 `internal/protocol/codecs`，与 `internal/protocol/taps` 同构同理由：`protocol` 不能反向导入自己的三个子包。转换路径要**两个** Codec（入口协议解出 canonical、渠道协议编回去）；两者相等时不该走这条路——同协议透传不做 decode→encode 转码。

- 每个协议一个包实现 `Codec`；「A→B 转换」= CodecA 解码 + CodecB 编码，**不存在两两互转的转换器**。实证依据：网桥式（逐对状态机）并非不可行——sub2api `apicompat/` 在三协议六方向上做成了生产级；但其 CC→A 流式路径是 `CC→Responses + R→Anthropic` 链式二次转换，恰说明无统一中枢时方向组合退化为拼凑链。枢纽式对新增协议保持 O(n) 扩展，本设计取枢纽。
- `EncodeStream` 内部管理：SSE 分帧、index 追踪（OpenAI 工具调用按 index 分片需按出现顺序重建）、`[DONE]` 终止符、Anthropic 的 `message_start/stop` 包裹。

#### Codec 实例的生命周期：每请求一个，Decode 与 Encode 共用同一个（#12 R→CC 实现时定，待 PO 追认）

`codecs.New` 返回的实例**每请求一个，不可缓存、不可跨请求复用、不可并发共享**；调用方（`internal/server/convert.go`）拿到入口 codec 之后必须一路用到响应编码，不许在编码时另 `New` 一个。这不是接口变更，是把「实例可以带每请求状态」这条隐含许可写成明文约束。

被逼出来的原因是 R 出口方向的一处非局部依赖：**Responses 的响应形态取决于请求里怎么声明的工具**。同一个上游 function-call 回来，声明成 `custom` 的要发 `custom_tool_call` + 自由文本入参，声明成 `function` 的要发 `function_call` + JSON 入参；而 `EncodeStream(w, events)` 只看得见事件流，事件是 CC 上游解出来的，那边根本不知道客户端当初声明了什么。这份知识只有 `DecodeRequest` 见过。

三个备选都排除了：① 把 kind 塞进 `Event`——CC 解码侧无从得知，它看到的 `arguments` 一律是 JSON；② 按形状猜（能拆出 `{"input":"…"}` 就当是包装）——一个真的只收 `input` 字符串参数的 JSON 工具会被误拆，形状不足以区分意图；③ 给 `Codec` 接口多传一个 `*Request`——六条路径里只有 R 出口用得上，等于让另外两个 codec 各背一个恒为 nil 的参数。sub2api 遇到的是同一个问题、解法同构（`ResponsesClientToolMapping.CustomTools` 从请求抽出来显式传给响应侧），差别只在我们的接口固定，状态改挂实例上。

**第三个住户（#9 CC→A 实现时加）：`openaicc.Codec` 的 `includeUsage`。** CC 的流末 usage 帧是**可选的**，发不发取决于客户端请求里的 `stream_options.include_usage`；而 `EncodeStream(w, events)` 只看得见事件流，事件是从另一个协议的上游解出来的，那边根本不知道客户端要过什么。所以这份知识只能由 `DecodeRequest` 存下来传给编码侧——与 `openairesponses.customTools` 是同一个问题的同构解，`openaicc.Codec` 也因此从无状态变成每请求一个。备选「一律补 usage 帧」被否：CC 的默认行为就是不发，凭空补一帧会让严格按 OpenAI SDK 写的客户端多解一个它没预期的结构。

**第二个住户（#25 R→A 实现时加，待 PO 追认）：`anthropic.Codec` 的 `DefaultMaxTokens`。** Anthropic 的 `max_tokens` 是必填，而 canonical 那边它可以是零值——Responses 的 `max_output_tokens` 与 CC 的 `max_tokens` 都允许缺省，都是合法请求。所以补默认这件事只能发生在 anthropic 的编码侧，**不能在 `convert.go` 里对 canonical 无条件填**：那会波及所有出口协议，让已经上线的 R→CC 在客户端没给上限时开始悄悄截断（行为变了，还不报错）。注入走 `codecs.New(proto, codecs.Options{...})` 的**必填**参数而非可变参数——字段少但每个都会改变发给上游的字节，漏传一个是静默的行为变化，让编译器替我们记着。配置项仍是 `default_max_tokens`（默认 8192）；连它都被显式设成 0 时，anthropic 包内还有一层 4096 兜底：这个分支不该发生，真发生了宁可截断也不要发一个注定 400 的请求。

代价记在明处：三个 codec 里 `openairesponses` 与 `anthropic` 带状态，`openaicc` 仍是纯函数。**这两条合起来就是「codec 实例可以带每请求状态」这条许可的全部现存用法**，新增 codec 前先看这里。

### 转换坑清单（codec 实现时的验收关注点）

| 坑 | 说明 |
|---|---|
| tool call 增量重组 | OpenAI 按 index 分发参数分片；Anthropic `input_json_delta`；并行调用下 index 交错出现，必须按 Index 缓存再按序输出。**输出顺序按「首次出现」而非 index 数值排**（#11 实现）：index 不保证从 0 起、不保证连续，按数值排会在上游从 1 起编号时错位 |
| CC 工具调用无逐条终止符 | CC 流里没有「这一路 tool_call 说完了」的信号，只有整流的 `finish_reason`。故工具分片只能**攒到流末尾一次性冲出**；而 Anthropic 侧同一时刻只允许开一个 content block，encode 侧要把每路缓存成 start/delta*/stop 一个整体再写。正文 delta 不受影响，仍逐字下发 |
| 响应 id 形态 | 上游 CC 的 `chatcmpl-…` **原样**当作 Anthropic `message.id` 下发，不重编 `msg_…`（#11 决策）：网关日志、上游账单、客户端看到的是同一个 id，排障能对上；Anthropic 客户端不校验 id 形态，也不需要把它回带给下一轮 |
| 转换路径不转发原始 query | 客户端打过来的 `?beta=true` 是 Anthropic 方言，原样贴到 CC 上游 URL 上会被严格上游拒。#20 定的「query 整串照抄」只管**同协议透传**；转换路径发空 query |
| `metadata.user_id` | 上游以此判定「是否官方 Claude Code 请求」，中间层重序列化丢弃会被归入第三方 app。策略：**不可转但须保留**——A 入口的请求体 metadata 原样随请求携带；P0 透传天然不受影响（sub2api 实证坑） |
| 严格中转的请求校验 | 第三方 OpenAI 兼容上游会拒绝：消息 content 为数组（须拼纯文本）、`tool_choice` 引用未声明的 tool、有 tool_choice 无 tools——编码侧做规整，别指望上游宽容 |
| stop_reason 合法性 | Anthropic 非流式响应 stop_reason 不允许 null/空串，映射表必须给出合法默认值 |
| 工具入参不保证是 JSON | Codex CLI 0.144 code-mode 只声明一个 `custom` 工具 `exec`，入参是 **JavaScript 源码**（`in-responses-tool-turn2` 实测），`ToolCall.ArgsIsJSON` 为 false。编码到 CC 时 `function.arguments` 按契约必须是 JSON 字符串，encode 侧只能自行合成包装对象——**合成规则须与解包侧对称**，否则工具结果对不回去。**（#12 落地包装与拆包，#25 补上缺的第三件并把三件收进 `protocol/customtool.go`）** 这件事有**三个**必须逐字对称的面，不是两个：① 声明侧 `CustomToolSchema()` 告诉上游「收一个叫 `input` 的字符串」，② 出站 `WrapCustomToolArgs` 包成 `{"input":"<原文>"}`，③ 回程 `UnwrapCustomToolArgs` 拆回来。#12 只做了 ②③——**声明侧是空的**：Responses 的 custom 工具用 `format`（lark 文法）描述入参、没有 `parameters`，`openaicc` 照抄就抄了个空，于是上游收到一个不带 `parameters` 的 function 声明。后果不是报错而是更隐蔽的东西：没有任何东西告诉模型该回 `{"input": …}`，模型回个 `{"cmd": …}`，回程拆不动只好原样给出去，Codex 拿到一段 JSON 当 JS 跑。三件散在三个包里各写一份迟早漂，而漂移的症状是**工具结果对不回去且不报错**，所以收进一个文件，往返对称由 `protocol/customtool_test.go` 钉。文法约束本身带不过去（CC 与 Anthropic 都没有对应能力），登记为 `DropToolGrammar`。同规则见 sub2api 的 `extractCustomToolCallInput`。解包**只对请求里声明为 custom 的工具做**——按形状猜会把一个真的只收 `input` 字符串参数的 JSON 工具误拆；这份「谁是 custom」的知识由 codec 实例从 Decode 带到 Encode（见上文实例生命周期）。拆不动就原样返回，不报错：第三方中转会重写 arguments，模型也可能换结构 |
| custom 工具的入参没法逐片下发 | JSON 字符串的转义没法按分片增量解，所以要**攒满整串再拆包**，上游的分片节奏在这里必然丢。这条路上本来也没有节奏可丢：CC 流没有逐条工具终止符，解码侧早已把分片攒到流末尾一次性冲出（见上一条「CC 工具调用无逐条终止符」）|
| 并行只在 code-mode 内部 | 同一实测：Codex 的并行工具调用发生在那段 JS 的 `Promise.all` 里，线上永远只有一个 `custom_tool_call`，`parallel_tool_calls` 恒 false。别拿 Codex 样本去验证「多路 tool_call 交错重组」——那条路径要用 CC 语料（`testdata/golden/cc-stream-parallel-tools`）验 |
| 厂商私有推理字段 | DeepSeek 系 `reasoning_content` 等非标字段不建模，走 `Request.Extras` 透传 |
| Responses reasoning 的 `encrypted_content`（M0 实测） | Codex CLI 的 `/v1/responses` 请求会在 `input` 里回带上一轮的 reasoning item，其 `encrypted_content` 是**上游侧不透明密文**，只有原上游解得开。P0 透传无影响；**P1 一旦跨协议转换就必然作废**——转成 CC/Anthropic 时它无处安放，转回来也已换了上游。落到口径上：这就是「thinking **回带**跨协议丢弃」的具体形态之一，转换路径不得伪造或复用该字段，只能丢（口径层 v0.62 只改出向，回带这一半原样保留），且丢了会让 Codex 失去上一轮的推理上下文（表现为质量下降而非报错）。M2 做 R→CC / R→A 时须有专门用例钉住「带 `encrypted_content` 的 input 不使转换报错」 |
| Responses 无状态化（P1-①，R 入口转换即需） | `previous_response_id` / store 语义需自行承接；参考 `sub2api backend/internal/pkg/apicompat/responses_namespace.go` |
| Responses SSE 线格式（#12 拿真实上游转录复核） | 事件名与 sub2api 一致，无出入。三条实测细节：① 正文 item 比工具 item **多一层 `content_part`**（`output_item.added → content_part.added → output_text.delta* → output_text.done → content_part.done → output_item.done`），工具 item 没有；② 每帧 data 里都带 `sequence_number`，**从 0 起全流连号**（102 帧无一例外），客户端拿它判丢帧；③ 流**不发 `data: [DONE]`**——那是 Chat Completions 的收尾，Responses 以 `response.completed` 为终点。截断另发 `response.incomplete`（`status: incomplete` + `incomplete_details.reason`），流内错误发 `response.failed`。转录在 `testdata/golden/raw/resp-{text,tool,parallel}`（未脱敏，未纳入 git；用例照它定形状后把期望写死在测试里，不回放文件——`raw/` 在 .gitignore 里，回放式用例在 CI 上会集体 skip 成假绿）|
| 解码侧的丢弃没有告警通道（#12 记账，待 PO 裁决） | 口径层 §2.6 要求跨协议丢弃须有日志警告，但现有的 `RequestEncodeReporter` 只挂在**编码**侧。`openairesponses.decodeInput` 遇到认不得的 input item 类型（如 `web_search_call`）是静默跳过：不报错、不进 Extras、不留日志。跳过本身是对的（decode 必须是全函数；同协议路径不进 codec，未知 item 在转换路径上的唯一去向就是被丢），缺的是那条日志。**建议先保持静默**——decode 层刻意不持有 logger，而对称补一个 `RequestDecodeReporter` 属于只有一个消费方的机械。跳过时不 flush 攒消息缓冲，否则未知 item 会把前后两条同侧 item 劈成两条同 role 消息，撞上严格 CC 上游的连发限制（用例：`TestDecodeRequestSkipsUnknownItemWithoutSplittingMessages`）|
| Anthropic 必填 max_tokens | OpenAI 可缺省；转 Anthropic 出口时必须填默认（配置项 `default_max_tokens`） |
| 角色交替约束 | Anthropic 要求 user/assistant 交替；OpenAI 允许多条连续同角色；转 Anthropic 前需合并相邻同角色消息 |
| assistant 空 content | 纯 tool_calls 的 assistant 消息 content 可能为 null，转 Anthropic 时空块要剔除 |
| tool_result id 对齐 | OpenAI `tool_call_id` ↔ Anthropic `tool_use_id`，互转时 id 原样携带 |
| streaming usage | OpenAI 需 `stream_options.include_usage` 才在流末尾给 usage；向 OpenAI 出口发流式请求时**强制注入该参数**，否则日志拿不到 token 数 |
| stop reason 映射 | `end_turn`↔`stop`、`tool_use`↔`tool_calls`/`function_call`、`max_tokens`↔`length` 查表，未知值统一 `stop` |
| Codex 压缩：恰好一个 compaction item（v0.47 记，v0.54 落地，#74） | Codex `collect_compaction_output` 要求压缩 turn 的响应里 compaction item **count==1**，0 个或多个都 `Fatal` 且不重试不降级。本地合成时截断 / 内容过滤 / **改去调工具** / 零字摘要 / **上游断流兜底收尾**的 turn **绝不产出 item**（宁可报错也不把残缺摘要装成替换历史，opencodex #422 同款裁定），且这五支一律不许发 `response.completed`。判据要对停因全集完备（v0.56）：非 `stop` 的停因漏掉哪个，都等于把「上游没写完」当成「上游写完了」。断流那支最阴：解码器把它兜成 `stop_reason=stop`，wire 上与正常收尾同形，靠 `EvDone.Truncated` 才分得开（v0.55；非流式两条路 v0.56 补齐）——**「completed + 零 item」就是那个静默 Fatal 形态本身**。自造信封定为 **`ptg1:` + base64(摘要)**，这是**长期兼容约束**：它进了客户端的会话历史，改前缀等于让所有在途会话的回带摘要解不开、降级成占位；要改只能加新前缀、旧前缀的解码永远留着 |
| Codex 压缩：合成期 SSE 静默（v0.47 记，v0.54 落地，#74） | 本地合成期间下行零字节直到摘要生成完；portage 无 wire keepalive 层（`writeDeadline` 不发心跳），前置反代的空闲超时会掐线，故按 15 秒下限发 SSE 注释行心跳（sub2api #3887 教训；其 failover 判定须扣心跳字节的坑一并留意）。**只盖得住「增量在流、被我们吞掉」这一种静默**——正文增量与思考增量都算（思考那段往往最长，v0.55 补），但上游整体卡住时心跳也不发，那种情况该由上游超时接管 |
| 压缩 turn 排除在 previous_response_id 展开缓存外（v0.47） | 若将来做 `previous_response_id` 本地展开（按 id 拼回前缀），压缩 turn 必须显式排除在缓存外——否则把刚被替换掉的旧长历史重新灌回来（opencodex 实测坑） |

## 6. 主链路时序

```
client (harness)
  │ POST /v1/messages | /v1/chat/completions | /v1/responses
  ▼
auth 中间件：key hash 校验 → 取出 allowed_models
  ▼
入口协议识别（路径匹配）
  ▼
router：接入点（对外模型名）→ 命中候选（渠道纳管模型；M0~M2 单候选直连，M4 起加权随机；过滤 key 的 allowed_models）
  ▼
出站协议选定（v0.33）：入口协议 ∈ 渠道支持协议集 ──► 就用它（能透传就透传）
                        否则 ──► 按固定序 cc > responses > anthropic 取集合中第一个
  ▼
协议分流（seam，P0 定型）：
  选定协议 == 入口协议 ──► 原始字节透传，Tap 旁路提取 usage（P0）
  选定协议 != 入口协议 ──► codec 转换路径（P1；P0 期配置校验保证不命中，见 §7）
  ▼
upstream 驱动候选间故障转移（C4 已决语义；A-14 D3：不探测、不记忆、不摘除。**候选间转移实现在 M4，key 层内环实现在 M3**；M0~M2 单候选单凭证退化：失败不切换，直接按入口协议原生格式回错）：
  候选集 = 该接入点 weight>0 的候选
  loop：对未试过的候选重新归一化权重，加权随机抽一个
      渠道并发闸（口径层 v0.49/v0.50，细节见 §7.5）：设了上限的渠道先占闸坑，闸满在网关侧有界排队
          （队列 ×1 / 超时 30s），队满或等超时按入口协议原生格式回网关自产 429；一次 Do 的重试与换凭证共占同一个闸坑
      渠道内按 key_mode 选启用凭证（key 层内环，v0.11，口径层 v0.38 修订，实现在 M3）：
          请求上游成功 ──► 透传 / 转换下行（写出首字节后不再切换）
          429/5xx/网络错误（未写首字节）──► 同候选同凭证退避重试（最内环，v0.19，实现在 M2）
          429/401/403（未写首字节，同候选重试耗尽后）──► 渠道内换未试过的启用凭证重试；
                                                    **只有 401 同时摘除该凭证**（记原因与时刻，只人工恢复）；
                                                    403 换而不摘；429 换而不摘、也不冷却
          5xx/网络错误/连接超时（同上，重试耗尽后）──► 不换凭证，跳出内环
      全局尝试上限 `retry.max_attempts` 耗尽 ──► 立即停止，按入口协议原生格式回最后一次上游错误
      渠道内凭证耗尽 或 5xx/网络错误/连接超时 ──► 剔除该候选，继续 loop
      其余 4xx ──► 不切换，也不重试，按入口协议原生错误格式直接返回
      候选耗尽 ──► 最后一次上游错误按入口协议原生格式返回
  同候选退避重试（v0.19，推翻 C4 的「无同候选重试」）：
      触发：429 / 5xx / 网络错误，且未向 client 写出首字节；401/403 与其余 4xx 不重试（确定性失效，重试必然同样失败）
      退避：指数退避 + 抖动；上游给了 `Retry-After` 就以它为下界
            抖动取半区间 `[d/2, d)`，不是全区间 `[0, d)`——全区间会让实际退避短于 `Retry-After`，下界就白设了
            `Retry-After` 超过 `max_delay` 时**不重试**，把那份 429 原样交给 client：照等等于把客户端在网关扣一分钟
      次数：可配（`retry.max_retries`，见 §7），默认 2；退避 `base_delay: 500ms` / `max_delay: 10s`（v0.21 定稿）
      超时不重试：刚超时的对端原地重试大概率再超时，只会把客户端的等待翻倍（与候选间转移的触发条件不同，那里超时要切候选）
      客户端中途取消：不再打上游；退避途中那份响应的 body 已被读空丢弃，不能当结果交出去
      依据：C4 原本把重试留给 harness，M0 验收实测（#6）证伪一半——Codex 0.144.1 对 5xx 会退避重试，
            对 429 一次即弃（带不带 `Retry-After`、把 request_max_retries/stream_max_retries 调到 4 都只打一次）。
            单候选单上游被限流时无人自愈，故网关必须自己补这一环。
      429 仍原样透传（§10）：重试耗尽后回给 client 的仍是上游那份字节，网关不改写不吞
  ▼
logging：无论成败异步落 call_logs
```

**关键约束：failover 边界 = 向 client 写出第一个字节之前。** 一旦开始下行写流，格式承诺已生效，再失败只能：终止连接或注入出口协议的错误事件，并记日志；**不得切换渠道重发**（new-api 同原则）。

### 6.1 透传实现细则（v0.10 定，M0 落地）

**上游 URL 拼接**：`channels.base_url` 存「协议子路径之前」的前缀，网关按**本次选定的**出站协议（v0.33，见 §6 的选定规则）追加固定后缀（`/v1/messages`、`/v1/messages/count_tokens`、`/v1/chat/completions`、`/v1/responses`），尾部斜杠归一化。代价是百炼这类自带路径前缀的兼容端点须填 `https://dashscope.aliyuncs.com/compatible-mode`，而非官方文档里带 `/v1` 的那串；换来的是不按厂商特判拼 URL。new-api 走 base_url 存根域名 + 各家 adaptor 特判，该复杂度不取。**建渠道的示例 SQL 必须写明这条**，否则填错是必踩的坑。

**客户端查询串整串照抄**（v0.24 定，#20，PO 裁定 jinpenga）：入站 URL 上的 query 原样接在拼好的上游 URL 后面，不过滤、不重排、不解码再编码。原实现只拼固定后缀，查询串被静默丢弃——实测 Claude Code 发的是 `POST /v1/messages?beta=true`，上游收到的是另一个请求，而丢没丢不看日志根本发现不了。不做白名单是因为这里没有可枚举的对象：各家 harness 的私有参数不可穷举，而查询参数不像请求头那样天然带客户端指纹（那条是请求头白名单的立论，不能照搬）。若日后发现某个参数确实泄露信息，再按「哪个参数、泄露什么」逐个拦。拼接顺序只能是 `base + 固定后缀 + "?" + query`——§7 的启动校验已拦掉带查询串的 `base_url`，所以不会拼出两个 `?`；query 为空时不产生裸 `?`。

**模型名翻译走字节级 splice，不是整体重编码**（v0.11 PO 裁定）：接入点对外模型名 → 纳管模型名的翻译（口径层 §2.3）必须发生，否则接入点在 M0 退化成没有翻译能力的空壳——对外叫 `qwen-fast`、上游叫 `qwen3-max-2025-09-23` 的接入点会带着对外名打到百炼被拒。做法是用 JSON 词法定位**顶层** `model` 值的字节区间，只替换那一段，其余字节一个不碰；嵌套对象里的同名键不受影响，顶层键重复时改最后一个（与 `encoding/json` 的 last-wins 一致）。这不违反「不做 decode→encode 转码」——整体重编码会打乱键序、改写数字字面量、丢掉未建模的厂商字段，splice 都不会。对外名与纳管名相同时原样返回，连 splice 都不做。因此透传路径的保真口径精确表述为：**除顶层 `model` 值外逐字节相等**。

**请求头（网关 → 上游）重建而非复制**，默认丢弃客户端全部请求头，白名单构造：

- `Content-Type` 取自客户端；`Accept` 取自客户端，未给且流式时补 `text/event-stream`。
- 凭证注入按**本次选定的**出站协议：`anthropic` → `x-api-key: <凭证>`；`openai` / `openai_responses` → `Authorization: Bearer <凭证>`。（同一个渠道两种协议都说时，两次请求的认证头因此可能不同——这是对的，头跟协议走不跟渠道走。）
- Anthropic 渠道额外：`anthropic-version` 取自客户端、未给时默认 `2023-06-01`；`anthropic-beta` 客户端给了就原样转发（Claude Code 靠它开 1M 上下文、computer use 等能力，丢了会静默退化）。
- 一律不转发：hop-by-hop 头（`Connection`/`Keep-Alive`/`TE`/`Trailer`/`Transfer-Encoding`/`Upgrade`/`Proxy-*`）、`Host`、`Content-Length`（Go 按 body 重设）、`Cookie`，以及**客户端自带的 `Authorization` / `x-api-key`——M1 起那里放的是网关下发的 API Key，绝不能漏到上游**。
- `Accept-Encoding` 不转发客户端值，流式请求显式设 `identity`（避免上游压缩引入分块缓冲、拖长首字延迟）；不注入 `X-Forwarded-*`（个人自用零收益且泄露内网信息）。

> **白名单实测复核（M0 验收，2026-08-06）**：用一次性反代录下 Codex CLI 实际发出的全部请求头，逐条对照上面的白名单。结论是**白名单不放宽**，依据两条：① Codex 的私有头（`X-Codex-*` 一族、session/turn 标识等）全部被丢弃，整轮工具调用照样跑通——上游不需要它们；② 其中 `X-Codex-Turn-Metadata` 携带 `installation_id`，属于客户端安装标识，转发出去等于把本机指纹泄露给上游，个人自用场景零收益。若日后某个 harness 因缺头而降级，按「哪个头、丢了坏什么」逐个加白，不做整类放行。

> **反例：认客户端指纹的上游（v0.23 实测，M2-1 采集，2026-08-07）**。上面那条结论有个前提——上游不关心你是谁。实测遇到不成立的一类：**某些中转站的 Anthropic 端点限死「只服务 Claude Code 客户端」，靠 `user-agent` 与 `x-app` 两个头一起判定**，白名单转发不带这两个，于是每个请求都回 503（同样的请求绕过网关直连则 200）。
>
> **白名单仍不放宽。** 把客户端指纹加白等于为迎合一家中转站的判定方式，把「不泄露本机指纹」这条口径整体撤掉，代价与收益不对等。绕法是配置层的：给 Anthropic 配一条不设这种闸的上游（示例见 `scripts/seed-example.sql`）。这也是那份示例把 Anthropic 拆成独立渠道、而 CC 与 Responses 共用中转站的原因。
>
> 需要区分的是 `cmd/goldenrec`：它**转发时照抄入站头、落盘时才走白名单**（`8de1dab`）。看着像双标，其实是两件事——采集要的是「让 harness 与真上游把整轮跑通」，防指纹外泄的对象是 git 仓库而不是上游。网关不同，它的对象就是上游，所以按白名单构造。

> **Anthropic 侧白名单实测复核（#7 验收，2026-08-11）**：上面那条 2026-08-06 的复核走的是 Codex CLI + Responses，Anthropic 这半边一直是**照参考仓库推断**的。这次拿 Claude Code 打真网关、上游换成一个只打印所见的假上游，逐条对出来的结果是白名单**照原样成立，一个字不改**：
>
> - 客户端自带的 `user-agent`、自定义 `x-client-fingerprint` 一类头到不了上游（与 §6.1 的「重建而非复制」一致）。
> - 客户端没给 `anthropic-version` 时，上游收到的是网关补的 `2023-06-01`。
> - `anthropic-beta` 原样转发，Claude Code 那串多值的 beta 列表整条到达，没有被拆开或重排。
> - `?beta=true` 查询串照抄（v0.24 那条的 Anthropic 侧实证）。
> - 顶层 `model` 被翻译成纳管模型名，其余字节未动。
>
> **`metadata.user_id` 原样到达上游**，内含 `device_id`（稳定机器指纹）、`account_uuid`、`session_id`。这不是白名单漏了，而是两条既有口径的**合成结果**：v0.24 定的是请求体除顶层 `model` 外逐字节相等，而白名单管的只是请求头。**不动它**——要拦就得改写请求体，那等于承认网关会按自己的判断删客户端的字段，比泄露一个 device_id 危险得多（今天删指纹，明天删的就是某个没建模的厂商参数）。记在这里是为了下次有人问「白名单挡住指纹了吗」时，答案是「头挡住了，体没挡也不该挡」。
>
> **`count_tokens` 的调用时机随 harness 版本变**：`testdata/golden/README.md` 记的 2026-08-07 那版 Claude Code 是每轮先打一次，而 08-11 这版跑完一整轮工具调用一次都没打。两条都是当时的实测，都不作废——网关这侧的结论是它**不能被当作启动必经的一步**（M0 起就实现了该端点，两种时机都跑得通）。

**响应头（上游 → 客户端）**：除 `Content-Length` 外原样回传（流式下无意义，非流式由 Go 按实际写入量重设），状态码原样。上游 `request-id` 既回传客户端也记日志——个人自用场景下能拿它去找上游对账，比藏起来有用。

> **落地形态（v0.56，#37 缩范围那一半）**：回传这半边是**构造性**的——`upstream.CopyResponseHeaders` 是 copy-all（只跳 `Content-Length`），不是白名单，所以不存在「漏了某个头」的可能；`anthropic-ratelimit-*` 一族与 `request-id` 随之原样过去。记日志这半边此前**根本没做**（表里没有这一列），v0.55 补 `call_logs.upstream_request_id` + slog `upstream_request_id`，透传与转换两条路都记（转换路径不回传上游响应头，但对账与走哪条路无关）。
>
> 头名以官方文档为准：`request-id`（platform.claude.com/docs/en/api/errors §Request ID，值形如 `req_018Ee…`，错误体里的 `request_id` 字段同值），兜底 `x-request-id` 给中转与自建上游。原文的 `x-request-id / request-id` 并列因此收敛成「官方名优先、x- 名兜底」——两个都在时取官方那个，中转给自己编的号不是要找的那个。
>
> **取值加第三档（口径层 v0.74，v0.73 落地）**：`request-id`（头）→ **错误响应体里的 `request_id`** → `x-request-id`（头）。上一段那句「错误体里的 `request_id` 字段同值」从旁注升成取值来源，因为实测撞见了「头被中转裁掉、体里还在」这个形状。三条实现约束：①**只在失败行走这一档**，成功响应的体里没有这个字段（2026-08-15 五份真实响应实测，流式非流式都没有）；②**复用 `error_detail` 已经读进来的那份字节**（v0.53 截 2KB 存原文），不新增一次 body 读、不重序列化、不改写回客户端的字节——这是它不违反「透传路径不做 decode→encode」的原因；③解不出来落空串，不失败不告警。注意 2KB 截断：`request_id` 在 Anthropic 错误信封里排在 `error` 对象之后，超长错误原文有把它截掉的可能，取键要在**截断前**的字节上做。
>
> **落地形态（v0.81，[#12](https://github.com/SimonGino/portage/issues/12)）**：`upstream.RequestIDs(h)` 只按头名取两档候选（`request-id` / `x-request-id`），先后不在它手里；第二档是 `upstream.ErrorBodyRequestID(raw)`，只认**顶层** `request_id`；三档的取舍是 `callRecord.resolveUpstreamRequestID`，由 `logCall` 调用**一次**——那是透传与转换两条路唯一的共同收尾，「两条路一致」因此是构造性的，且第二档要等错误体收完（透传路径上那份字节是边转发边攒的）。两个上限从此分开：错误原文在内存里收到 `upstreamErrorLimit`（64KB，两条路共用一个常量），落库那一刻才截到 `errorDetailLimit`（2KB）；先截再解等于永远解不到那个键。成功行**结构上**走不到第二档：`rec.errorDetail` 只在失败时才被挂上，成功行那个字段是 nil。
>
> 用例（`internal/upstream/headers_test.go`、`internal/server/requestid_test.go`）把官方文档列出的整套头钉死在 `gatewaytest.AnthropicResponseHeaders` 里，任何一处退化成白名单转发当场红。**但它验的是「网关有没有改动」，不是「官方真的这么发」**——后者仍要拿官方 key 实测，是 #37 剩下的那半。

**流式转发按字节块复制，永不按帧切分**：循环 `Read`（32KB 量级）→ `Write` → `Flush` 直到 EOF。不用 `bufio.Scanner` 按行读再重组——会引入换行/空行的重写风险，且 Scanner 的 token 上限会变成透传路径的截断上限。SSE 帧解析**只发生在 Tap 内**，Tap 从 `io.TeeReader` 拿同一份字节自组帧、自管缓冲上限（MB 级，并行工具调用的 JSON 参数单帧可以很大），**超限即放弃解析并降级**：Tap 的上限只影响日志字段完整性，绝不截断转发字节。（new-api 按行 Scanner 读、靠把 token 上限调到 64MB 躲大参数帧截断，本设计不取该路径。）

> **「放弃解析」的粒度＝丢那一帧**（v0.13 提出，v0.19 定稿，jinpenga 裁定）：本节原文与 Issue #5 只说「超限即放弃解析并降级」，没说放弃的是**那一帧**还是**整条流**。取前者——丢掉超限帧后在下一个帧边界重新对齐、继续解析。理由是 Anthropic 的 `output_tokens` 与 stop_reason 都在流最末的 `message_delta` 里，按后者读，一个畸形大帧会把整次调用的 usage 全带走。两种读法下 `Degraded` 都会置位，日志的可信度标记不受影响。
>
> 超限判定与分块无关：整帧连同结尾空行落在同一个 TCP 块里到达时同样按帧长判超限，否则「这帧算不算超限」会取决于上游恰好怎么切块。
>
> Tap 的两条硬约束落在实现上是：`Write` 恒定返回 `(len(p), nil)`（返回错误会被 `io.TeeReader` 变成读错误、直接打断转发），提取逻辑外包一层 `recover`（panic 只降级日志字段）。

**超时分层，不设 `http.Client.Timeout`**——它覆盖整个 body 读取周期，长流必被拦腰掐断。改设 `DialContext` 的 `net.Dialer.Timeout`（TCP 拨号，10s 量级）、`TLSHandshakeTimeout`、`ResponseHeaderTimeout`（等上游首个响应头，120s 量级）、`IdleConnTimeout`。**拨号这一层不能漏（v0.20 补）**：零值 `http.Transport` 用的是无超时的 `net.Dialer`，而后面几个超时都在 TCP 连上之后才起算——渠道地址被黑洞（丢包不回 RST）时，请求会一直挂到操作系统放弃（75s 量级）而不是及时回 502。向客户端写出前用 `http.NewResponseController(w).SetWriteDeadline` 每次推进（30s 量级），防慢客户端把 handler 永久挂住。客户端断连靠把 `c.Request.Context()` 传给上游 request 自动传播取消。

## 7. 配置与数据模型

### 启动配置（config.yaml，最小）

```yaml
listen: "127.0.0.1:8317"          # 公网暴露时改 0.0.0.0 并配合 nginx 反代/限流（§11.3）
db_path: "./gateway.db"
admin_password: "change-me"        # 仅首启初始化管理员；改密后此项失效（可用 PORTAGE_ADMIN_PASSWORD 覆盖）
default_max_tokens: 8192
log_bodies: false                  # 排障开关；默认不记请求体
rate_limit_qps: 10                 # 全局令牌桶（v0.15，M3 落地）；写 0 即关闭
rate_limit_burst: 20               # 只写 qps 时兜底 20；超限回 429 + Retry-After: 1
retry:                             # 同候选退避重试（v0.19 口径，v0.21 定稿）
  max_retries: 2                   # **重试**次数，不含首次尝试；每份凭证各自一份
  max_attempts: 6                  # 一次请求的全局上游尝试上限（口径层 v0.38），跨凭证累计；写 0 即不封顶
  base_delay: 500ms
  max_delay: 10s
concurrency_queue:                 # 渠道并发闸的有界排队（口径层 v0.50）；只对设了 max_concurrency 的渠道生效
  factor: 1                        # 队列上限 = 并发上限 × factor；显式写 0 = 不排队，闸满立即拒（零值陷阱同 max_retries）
  wait: 30s                        # 排队等待超时；两个时长 <= 0 都兜回默认（同 base_delay，停在 0 不是任何人想要的形态）
  retry_after: 10s                 # 队满/超时 429 的 Retry-After，落头时换算成整秒、不足 1 秒顶成 1
```

> **唯一的环境变量是 `PORTAGE_ADMIN_PASSWORD`**（口径层 v0.28）：env 优先于文件，空串等于没写；配置文件整个缺席时也生效（`docker run` 不挂配置是常态）。仍然只用于**初始化**——库里已有密码就一概不动。其余配置项不做 env 覆盖：它们不是凭证，走文件更能一眼看全。

> **`max_attempts` 与 `max_retries` 是两层，不是一件事**（口径层 v0.38）：内层 `max_retries` 管同一份凭证上的抖动重试，外层 `max_attempts` 管一次请求最多打多少次上游、跨凭证累计。两层都要，因为只留内层时最坏耗时随凭证数线性增长（凭证是运营数据随时会加，配置里没有任何地方提示「加第 6 把会让超时翻倍」），而只留外层、跨凭证共享一份预算时会出现「换到第二份时预算耗尽、第三份根本没试过」——那份是好的却没被用上。`max_attempts` 与 `max_retries` 同一个零值陷阱，处理方式相同。

> **`retry` 块缺席 = 用默认（重试 2 次），显式写 `max_retries: 0` = 关闭**。两者在 YAML 里都解出 0，靠「先填默认值再 Unmarshal 覆盖」区分：加载后不许再给 `max_retries` 补零值，否则「写了 0」被悄悄改回 2，重试就关不掉了。两个退避间隔反过来必须兜底——只写 `max_retries` 时不补就退了个寂寞。

业务配置（渠道/接入点/key）全部落 DB，由管理端维护（M3）；管理端就绪前（M0~M2）用 SQL 手工维护（口径层 C2 收敛，v0.8）。

> **配置校验规则（临时闸，随转换批次逐步放开）**：候选渠道协议与入口协议不同、且对应转换路径尚未实现时报错。这不是 v1 边界——全互转属 v1 承诺（口径层 C1 已收敛）。**#80 之后这道闸已无「尚未实现」可拦**：九格全开，剩下的只有 `count_tokens` 这个没有上游对应端点的入口，见下方 §2 与 §9.4。
>
> **校验时机拆两处**（v0.10）：接入点本身不绑定协议，入口协议要到请求时（由路径）才知道，因此「协议必须一致」无法在启动时判。
> - **启动加载时 + 管理端保存时**：每个未停用接入点有且仅有一个 weight>0 的候选；每个未停用渠道**至少有一份**未停用凭证（上限那半已于口径层 v0.38 随凭证池放开，M3 起临时闸只剩单候选那一半）；每个候选引用的纳管模型确实属于存在的渠道；**未停用接入点的 weight>0 候选必须真的可达——其渠道、纳管模型、凭证均未停用**（v0.15，判定条件逐条对齐 `Resolve` 的 JOIN）；**未停用渠道的 `base_url` 必须是带 host 的绝对 http/https 地址，且不带查询串与 fragment**（v0.20——schema 只要求非 NULL，而配置是手写 SQL 灌进来的，空串 / 漏 scheme 的裸域名 / `ftp://` 都存得进去，过得了校验却每次请求才在 `http.Client.Do` 里失败回 502；查询串与 fragment 更隐蔽，`buildURL` 是字符串拼接，`https://h/p?x=1` 接上 `/v1/messages` 后 Go 解出来是 `path=/p`、`query=x=1/v1/messages`——协议子路径被整个吞进查询串，请求永远打到 `/p`，启动、日志、响应三处都看不出异常）。违规即拒绝启动，报错须点名违规记录的 id/name。

> **这条错误信息不回显 `base_url` 本身**：`cmd/gateway` 会把 `Validate` 的错误直接落 stderr，而 `base_url` 可以带 userinfo（`https://user:pw@host`），回显等于把上游密码打进日志。按 CLAUDE.md「错误回显严禁泄露上游 key 与 base_url」，只报「哪里不对」加渠道 name/id，让运维自己查 `channels` 表——可诊断性不靠回显原值。
> - **请求时**：入口端点在本次选定的出站协议下没有对应路径 → 按入口协议原生格式回错，文案为「该端点没有对应的转换路径」（#80 前的文案是「该转换路径尚未实现」，九格全开后语义从「还没做」改为「没得做」）。
>
> **v0.33 追加**：未停用渠道的 `protocols` 必须非空且逐项合法（`store.checkChannelFields` 调 `protocol.ParseSet`）。空集合选不出出站协议，每次请求才 500——同属 v0.21 通则要拦的形态。`count_tokens` 不需要额外的闸：`conversionOpen` 的判据是**端点**而非协议，渠道不说 `anthropic` 时它必然落进「没有对应的转换路径」这一格，不会被回退顺序送去 `/v1/chat/completions`。（口径层 v0.80 之后这一格的收场由 501 改成本地估算回 200，判据本身不变——仍是按端点分流。）

### SQLite 表

```sql
CREATE TABLE channels (            -- 渠道只管连通性，不承担路由职责
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  protocols TEXT NOT NULL,         -- 支持协议集（v0.33）：逗号分隔，取值 anthropic | openai | openai_responses；单值即一元集合
  base_url TEXT NOT NULL,
  credential_type TEXT NOT NULL DEFAULT 'api_key',  -- api_key | service_account（Vertex：SA JSON→token 刷新，v0.17）
  key_mode TEXT NOT NULL DEFAULT 'polling',  -- polling | random：凭证池选取模式
  max_concurrency INTEGER NOT NULL DEFAULT 0,  -- 渠道并发上限（口径层 v0.49）：in-flight 上限，0 = 不限；老库靠 store.migrate 的既有 ALTER 模式补列。见 §7.5
  supports_compaction INTEGER NOT NULL DEFAULT 0,  -- 上游认不认 Codex 的 compaction_trigger（口径层 v0.54）：默认 0 = 不认，存量行同。只在 Responses 透传路径上被问到。见 §7.6
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE channel_keys (        -- 渠道凭证池（new-api 密钥聚合的建表版，不用 blob+JSON 状态 map）
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  name TEXT NOT NULL,              -- 人写的凭证名（v0.35/口径层 v0.38），日志与用量归因用；不填由管理端给 `凭证 N`
  credential TEXT NOT NULL,        -- 静态 key 或 SA JSON（按渠道 credential_type）；仅存服务端，错误回显严禁泄露
  disabled INTEGER NOT NULL DEFAULT 0,
  disabled_reason TEXT,            -- 仅 401 自动摘除（口径层 v0.38：403 换而不摘）；429/5xx 不摘；只人工恢复
  disabled_at DATETIME,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  -- 渠道内唯一（日志里两行都叫「主号」就废掉了归因本身）落成**独立唯一索引**
  -- idx_channel_keys_name，建在 store.migrate 里而不是这张表内：老库要靠 ALTER 补
  -- name 这一列，而 ALTER 加不了约束；索引则新老库都能建，两条路长出同一个形状。
  -- 也不能挪进 schema.sql——它在 migrate 之前跑，那时老库还没有 name 列。
);

CREATE TABLE access_points (       -- 接入点：对外模型名（客户端 model 字段）
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  model TEXT NOT NULL UNIQUE,
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE channel_models (      -- 渠道纳管的可用模型（上游模型名）；候选只能引用纳管条目
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  upstream_model TEXT NOT NULL,
  protocols TEXT NOT NULL DEFAULT '',  -- 这个模型自己能走的协议子集（v0.38/口径层 v0.40）：逗号分隔，取值同 channels.protocols。
                                       -- **空串 = 继承渠道全集**，绝大多数模型都该是空的；存原样、不校验它是不是渠道集的子集，
                                       -- 路由时取交集（store.pickProtocol）
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(channel_id, upstream_model)
);

CREATE TABLE candidates (          -- 候选 =（渠道纳管模型，权重）；weight=0 临时摘除
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  access_point_id INTEGER NOT NULL REFERENCES access_points(id) ON DELETE CASCADE,
  channel_model_id INTEGER NOT NULL REFERENCES channel_models(id),
  weight INTEGER NOT NULL DEFAULT 100,
  UNIQUE(access_point_id, channel_model_id)
);
-- 临时闸：M0~M2 强制每接入点单候选 + 每渠道单凭证；**M3 起只剩单候选那一半**（口径层 v0.38），
-- 凭证那半放开为「≥1 份启用凭证」，多候选分流与候选间转移仍在 M4

CREATE TABLE api_keys (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  key_hash TEXT NOT NULL UNIQUE,             -- SHA-256(明文) 的小写十六进制，不加盐，见下
  key_plain TEXT NOT NULL DEFAULT '',        -- 明文（口径层 v0.47）。存量行永远是空串，见下
  allowed_models TEXT NOT NULL DEFAULT '*',  -- JSON 数组或 *；M1 只建列不校验，一律当 *
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
-- 无 expires_at：v1 不做过期（口径层 v0.27 收敛 C6），停用走 disabled。

CREATE TABLE call_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  api_key_name TEXT NOT NULL,
  client_protocol TEXT NOT NULL,       -- anthropic | openai | openai_responses
  upstream_protocol TEXT NOT NULL,
  model_requested TEXT NOT NULL,
  model_upstream TEXT NOT NULL,
  channel_name TEXT NOT NULL,
  channel_key_name TEXT NOT NULL DEFAULT '',  -- 本次真正发请求的那份凭证名（换过则记最后一份，失败亦然）；
                                              -- 快照冗余而非 channel_key_id：删凭证是常事，存 id 会把历史 join 空。
                                              -- 没走到上游时为空串（迁移前的老行同）
  status INTEGER NOT NULL,             -- 最终对 client 的状态
  retry_count INTEGER NOT NULL DEFAULT 0,  -- 这次调用向上游重打了几次，**含换凭证之后的那些**（v0.35 扩义，
                                           -- 原为「同候选重试次数」）：这一列回答的是「这次怎么慢了三秒」，
                                           -- 而换凭证同样会慢。结构化日志里对应 retries 字段（v0.21）
  is_stream INTEGER,                   -- 同步/流式（0/1，v0.50）。可空：stream 是解析请求体才知道的，
                                       -- 鉴权失败那类行没走到那一步，与迁移前的老行一样留 NULL——
                                       -- 「不知道」与「同步」不是一回事
  ttft_ms INTEGER,                     -- 首字节耗时（流式）
  queue_wait_ms INTEGER NOT NULL DEFAULT 0,  -- 渠道并发闸排队等待（口径层 v0.52）：没闸/没等为 0；配套 error 词表
                                             -- 加 queue_full / queue_timeout / queue_abandoned 三词。见 §7.5
  total_ms INTEGER NOT NULL,
  input_tokens INTEGER, output_tokens INTEGER,
  cache_read_tokens INTEGER, cache_write_tokens INTEGER,
  reasoning_tokens INTEGER,            -- 思考 token（口径层 v0.66，#97）。是 output_tokens 的**明细**不是另一笔，
                                       -- 别把两者相加。**可空且必须可空**：NULL = 上游不报这个数（Anthropic
                                       -- 一路、迁移前的老行），0 = 上游报了、这次没思考。抹成 0 会让前者显示成
                                       -- 确凿的零思考成本，正是口径层 v0.65「已发生的成本不得静默吞没」要防的
  error TEXT,                          -- 网关自己的**固定词表**（可枚举、可 group by）
  error_detail TEXT,                   -- 上游错误原文前 2KB（口径层 v0.53），只在失败时写，其余为 NULL。
                                       -- 与 error 不同步出现：上游透传 4xx 的 error 是空的（透传成功不算
                                       -- 网关侧错误，v0.28 纪律），detail 却有值——管理端「可展开」的判据
                                       -- 因此是 status >= 400。可空是为了分开「没存」与「上游回了 4xx 但
                                       -- 体是空的」（存空串），后者本身就是排障信息
  upstream_request_id TEXT NOT NULL DEFAULT ''  -- 上游响应头 request-id 的原样快照（口径层 v0.56，#37）：
                                       -- 拿它去找上游对账，官方文档报障时要的就是这个 id。取头名 `request-id`
                                       -- （Anthropic 官方拼写），兜底 `x-request-id`（中转常用）。**不可空**：
                                       -- 这一列上「没走到上游」与「上游没回这个头」都读作「没有可用的 id」，
                                       -- 分开没有排障价值（前者看 status 就知道），与 error_detail 那条的取舍不同。
                                       -- 管理端在「详情」里露出并可复制（口径层 v0.67，v0.70 落地）：**成功行也有值**，
                                       -- 这正是那颗按钮的判据不能只看 status >= 400 的原因
                                       -- 取值三档（口径层 v0.74）：request-id 头 → 错误体里的 request_id → x-request-id 头。
                                       -- 中间那档只有失败行有货，且复用 error_detail 已读进来的字节
);
CREATE INDEX idx_call_logs_created_at ON call_logs(created_at);

CREATE TABLE settings (            -- 管理端自己的状态，M3 起只有一行 admin_password_hash
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

> **为什么密码不回写 `config.yaml`**：口径层 §2.7 要求「登录后可改，改后配置项失效」，改到哪儿就得存到哪儿；而配置文件在容器里是只读挂载的，回写根本写不进去。单开一张 kv 表比为一个字段建一张专表更省——管理端往后要存的零碎状态都归这里。

注：若未来改 MySQL，表须显式 `CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`（与团队 DDL 规范一致）。

### 7.1 key 鉴权的三条实现口径（M1，PO 拍板 jinpenga 2026-08-08）

**`key_plain` = 明文，与哈希各存一列（口径层 v0.47）。** 管理端要能看能复制（PO 裁定），而明文此前从未落库，只能加列。**存量行补不回来**：`ALTER TABLE` 的默认值是空串，而哈希不可逆，加列之前建的 key 谁也还原不了——读侧把空串读作「原值没存过」，界面据此提示删了重建，不摆假掩码。鉴权仍走 `key_hash` 的唯一索引，与这一列无关。

**`key_hash` = SHA-256 裸哈希，不加盐、不用 bcrypt/argon2。** 注：下面这段理由里「明文只在创建那一个响应里存在过」的前提自 v0.47 起不成立，但结论不变且更无所谓——明文就在同一张表的隔壁列，加盐慢哈希保护不了任何东西。原理由：key 是网关自己生成的高熵随机串（`sk-ptg-` + 随机），不是人选的密码，字典攻击与彩虹表都不成立；而鉴权是**每个转发请求都要走一遍**的路径，要按 hash 精确匹配吃 `key_hash` 上的唯一索引。加盐意味着盐各行不同、hash 不可索引，每次鉴权得扫全表逐行比；bcrypt 更是每次比对十毫秒级——那是为「防拖库后爆破人选密码」付的代价，本场景没有那个威胁。

**`allowed_models` M1 只建列不校验，一律当 `*`。** 现在启用也没有界面可配，只能 SQL 手改；改错的表现是请求 403，而排查「为什么 403」还得自己翻表。等 M3 管理端能配了再启用校验，届时 `internal/auth` 取出该列、比对请求体顶层 `model`。

> **已于 M3 启用**（口径层 v0.28）：`auth.Key.Allows` 做精确匹配（逗号分隔，`*` 与空串都是不限；空串出现在手写 SQL 漏填的行上，按「没设限制」处理而不是把那把 key 锁死）。**校验点在 `relay()` 里、解析出 `head.Model` 之后**，不在鉴权中间件——那一层跑的时候请求体还没读，不知道要判哪个模型。越权回 **403**（按入口协议原生错误格式）而不是 404：这把 key 不能用它，不是它不存在，说成 404 会把人引去查配置。`GET /v1/models` 不按白名单过滤（PO 裁定校验只在转发端），因此一把受限 key 能列出它调不了的名字。

> **两种名平权（口径层 v0.32）**：`Allows` 比的就是客户端填的 `model` 字符串本身，接入点名与纳管模型限定名都能写进白名单。函数本身一行没改——变的是可写的值域。只认接入点名的话，一把受限 key 走直连就能打到同一个上游，白名单等于形同虚设。

**不做过期时间。** 见 `api_keys` 表注释与口径层 v0.27。

其余 M1 细则（取 key 的两个头、401 走 `protocol.WriteError`、鉴权失败也落 `call_logs`、落库失败不得影响请求）见 Issue [#22](https://github.com/SimonGino/portage-legacy/issues/22)。

### 7.2 全局限流的实现口径（M3，兑现口径层 v0.15）

`internal/server/ratelimit.go`，`golang.org/x/time/rate` 的令牌桶。口径层只裁了语义（两只桶、10 QPS / 突发 20、429 带 Retry-After、不分维度），以下是实现侧的决定：

- **两只桶，都在 `New()` 里造**（口径层 v0.81，#16）：生成面那三个端点（`/v1/messages`、`/v1/chat/completions`、`/v1/responses`）共用 `s.genLim`，`count_tokens` 独占 `s.countTokensLim`，选桶在 `pickLimiter(ep)`。写成在 `rateLimit(ep)` 闭包里逐端点 new 的话，四个端点各得一只，全局 10 QPS 悄悄变成 40——而且从代码上看不出来。`TestRateLimitBucketIsSharedAcrossGenerationEndpoints` 是这条的哨兵：它把生成面另外**两个**端点逐个点到（刻意不用 `count_tokens`，后者已不共用），只点一个的话将来谁再给 `/v1/responses` 单开一只桶也照样全绿。
  - **选桶的判据是端点不是入口协议**：`count_tokens` 与 `/v1/messages` 的 `ep.Proto` 同为 anthropic，按协议判会把 `/v1/messages` 一并分到 count_tokens 那只桶里。同 `conversionOpen` 踩过的坑。
  - **拆桶的理由**（真机数据与被否掉的两档见口径层 v0.81）：#16 观测到 CC 开场 26 次 `count_tokens` 打空共用桶、紧随其后 11 条真实请求 429。选桶在 `rateLimit` 返回闭包**之前**算一次——端点是路由注册时就定死的，没有必要每个请求再判一遍。
  - **`count_tokens` 自己那只桶照样限**：它在 Anthropic 出口是真打上游的，`TestCountTokensIsStillRateLimited` 盯着这条；`TestCountTokensStormDoesNotStarveMessages` 盯 30 连打不饿死生成面，用 `burst=1` 而不是 2——桶只有一个令牌时，共用桶的旧行为下第一次 `count_tokens` 就把它拿走了，断言不必再靠「30 连打跑得比 1 秒快」（`qps=1`，一秒回一个令牌），机器一慢不会让被推翻的旧实现全绿。
- **挂在鉴权之后**（`callLog → authRelay → rateLimit → relay`）。限流的目的是「钳制上游账单损失」，而没过鉴权的请求根本到不了上游；放在鉴权之前，被扫时扫描流量会把令牌吃光，把合法请求一起饿死——那是把防账单的闸变成了一个 DoS 放大器。代价是被扫时网关自己仍要为每个请求查一次 key，那是 SQLite 的一次索引命中，不是一个量级。副作用是好的：429 那行流水带得上 `api_key_name`，排查泄露时看得见是哪把 key 在刷。
- **只挂转发面那四个 POST**。`/healthz` 被限会让监控在最忙的时候先报警；`/v1/models` 不打上游；`/admin` 走另一套凭证，把自己限出管理端毫无意义。
- **`Retry-After: 1` 固定值**。这个头的单位是整秒，而 10 QPS 下一个令牌 100ms 就回来，算出来的真值一律不足 1 秒、只能向上取整成 1。用 `Reserve()` 拿精确延迟还得记得 `Cancel()` 把令牌还回去（漏了等于每次被拒再扣一个），为一个恒等于 1 的结果不值当，所以用 `Allow()`。
- **`rate_limit_qps: 0` 即关闭**，与 `retry.max_retries` 同一个陷阱：在 `config.Load` 里顺手补零值会让「写了 0」被悄悄改回 10，配置项形同虚设。`burst <= 0` 反过来必须兜底成 20——桶容量 0 时 `Allow` 恒假，整个转发面直接瘫掉。
- **测试里默认关闭**（`gatewaytest.Options` 的零值），否则任何连打二十几个请求的用例会莫名变红，而且是间歇性的。要测限流的用例显式传 `qps=1`。

### 7.3 `X-Accel-Buffering: no`（M3，PO 裁决 jinpenga 2026-08-08，口径层 v0.30）

SSE 响应上盖 `X-Accel-Buffering: no`。nginx 认这个头，见到就对本次响应关掉 `proxy_buffering`。

**为什么值得**：§11.3 的实测里，「关掉缓冲」单独就足以让被攒住的 SSE 恢复逐条下发。网关多半跑在一份不由我们维护的 nginx 后面（别人写的那份、机器上本来就有的那份），这个头等于把那一下做进网关自己，不必指望前面的配置写对了。

**为什么要 PO 拍板而不是实现侧自决**：它是透传路径上唯一一处「上游没发、我们加上」的响应头，与「透传保真优先」有张力。

实现细节：

- 两条路径都设。转换路径在 `convert.go` 跟其余 SSE 头一起写；透传路径在 `CopyResponseHeaders` **之后**按上游 `Content-Type` 前缀判 `text/event-stream` 再补——放在之后是有意的，上游若自己发了这个头（见过发 `yes` 的中转），以我们的为准。
- **非流式不加**。无差别盖上去就成了「透传路径永远多一个上游没发的头」，与保真的张力比换来的好处大。
- 对不认它的反代与直连客户端是一个无害的多余头。

### 7.4 响应 id 的透传与兜底（M2，PO 裁决 jinpenga 2026-08-08，口径层 v0.31）

跨协议转换时，上游响应 id 原样透传，不改写成目标协议的形态。A→CC 路径上客户端拿到的就是 `"id": "chatcmpl-…"`。

裁决依据见口径层 v0.31，此处只记实现形态：

- **透传是「什么都不做」**：`openaicc/decode.go` 把 chunk 的 `id` 收进 canonical `Event.ID`，`anthropic/encode.go` 原样写出。没有转换代码，所以真正要防的是以后有人「顺手规范化一下」——`TestEncodeKeepsUpstreamResponseID` 是那道锁。
- **空 id 兜底在编码侧**（`fallbackMessageID`），不在解码侧。`msg_` 是 Anthropic 线格式的知识，归写这个格式的人管；将来 R 编码器要补自己的前缀，各管各的。
- 触发条件是**上游发了 model 但没发 id**：CC 解码侧 `message_start` 的门槛是两者有一个非空，所以这条流真能走到编码侧，不是造出来的边界。此前会输出 `"id": ""`，而 id 在 Anthropic 响应里是必填字段。
- 两个调用点：流式的 `ensureStarted`（覆盖「连 `EvMessageStart` 都没有、由首条正文触发」的情形）与 `EncodeFullBody`。
- 兜底值 `msg_` + `crypto/rand.Text()`。用 crypto/rand 不为安全——这个 id 不承担鉴权语义——是因为它没有失败分支要写。前缀选 `msg_` 而非照抄 `chatcmpl-`：正好与透传形成对照，`chatcmpl-` 即「上游给的」、`msg_` 即「网关补的」，排障省一次翻日志。
- 兜底值每次不同，有测试盯着（写死常量能过前缀断言，但会让同一时间窗内所有缺 id 的响应共用一个 id）。

**没做**：给 `call_logs` 加上游响应 id 列。它会让这条决策的第二条依据失效（「响应体是唯一关联句柄」），但那是另一个范围的事，真需要时再单独提。

### 7.5 渠道并发上限（口径层 v0.49~v0.52，#60 落地）

口径层已裁：渠道级 in-flight 并发上限，手填正整数、空/0 = 不限（默认）；只做并发不做 RPM/TPM；粒度只到渠道级；与全局令牌桶（§7.2）保留并存；闸满走网关侧有界排队，队满/超时回 429（v0.50）；拥塞期在此之外**不加机制**——无熔断、无自动探活恢复、重试逻辑不动（v0.51）。实现侧已定的形态：

- **数据模型**：`channels` 加 `max_concurrency INTEGER NOT NULL DEFAULT 0`（0 = 不限），老库走 `store.migrate` 的既有 ALTER 模式，默认值保证存量渠道行为零变化。
- **挂点**：in-flight 计数是**内存态**（按 channel id 一只计数器/信号量，重启归零，与「不落库的时间态状态」无涉），挂在 `upstream.Client.Do`（`internal/upstream/upstream.go`）——它是唯一的上游出口，透传/转换两条路径都过这里。
- **持有区间**：「向上游发出 → 响应体读完/流结束」；一次 `Do` 内的同凭证重试与换凭证都在同一次持有内，不重复计数也不中途释放——重试打的是同一个上游，占的是同一份容量。
- **与全局桶的先后**：全局桶在入口限速率、本闸在出口限存量，一个请求先过桶后占闸，互不感知、互不替代。
- **排队（v0.50）**：闸满时在信号量获取处等待，带两个界——队列上限（默认 = 并发上限 ×1）与等待超时（默认 30s），都是 config.yaml 全局项，**不进渠道表**；配置项名与形态已定（#60）：`concurrency_queue` 块下 `factor`（倍数形态，队列上限 = 并发上限 × factor，显式 0 = 不排队）/ `wait` / `retry_after`，样例见 §7 顶部。等待用带 ctx 的获取：**客户端断连即出队释放**，不转发也不占位；不承诺严格 FIFO（等待者按到达序移交即可，个人网关无公平性诉求）。排队发生在向上游转发之前、任何字节写回客户端之前，SSE 无关。信号量不用现成库而是手写移交式（`internal/upstream/gate.go`）：上限每次获取时从渠道配置带入，改配置不用重启，缩小上限靠「移交前先查新上限」自然排空——通用库的固定容量做不到。
- **队满/超时的 429**：复用「按入口协议原生格式回错」的既有路径，文案我方固定词表（如「渠道并发已满」），`Retry-After` 固定默认 10s（config 可调）。这个 429 是网关自产的，与上游透传的 429 在流水里要分得开——归因字段随观测票落。
- **ttft 不动**：`rec.start` 在 callLog 中间件（请求到达）就打了，排队时间天然计入 `ttft_ms` 与 `total_ms`，这正是 v0.50 要的体感语义，一行代码都不用改；「纯上游耗时」等观测票加排队时长字段后相减。
- **拥塞期零改动（v0.51）**：熔断/探活/重试收敛都不做，`retry.go` 一行不动。支撑这个「零」的三个既有事实，改到任何一个都要回头复核 v0.51 的立论：①`Transport.ResponseHeaderTimeout = 120s`（`upstream.go:53`）是「卡死请求最多占闸坑 120s」的兜底——若调大或删掉，拥塞期闸坑可能被永久占满；②超时不重试（`retry.go:59`）是「拥塞无重试放大」的前提；③重试在同一闸坑内（本节「持有区间」条）是「503 重试放大被闸封顶」的前提。

**观测与验收（v0.52，口径已收敛，本节可整体开工）**：

- `call_logs` 加 `queue_wait_ms INTEGER NOT NULL DEFAULT 0`——过闸请求都记（没闸/没等为 0），等到超时被拒的行记实际等待（≈30000）；老库 ALTER 补列，老行的 0 语义无损。流式非流式都记：`ttft_ms`「只记流式」的限制（v0.28 变更）是因为非流式的它约等于总耗时，排队时长没有这个问题。
- error 固定词表加两词：`queue_full`（队满即拒）/ `queue_timeout`（等到超时）。**不加 outcome 列**（#22 的判断复核仍成立：收场词短且可枚举，error 列承载够用）。三种 429 的归因从此齐了：`rate_limited` = 全局桶、`queue_full`/`queue_timeout` = 渠道闸、status 429 且 error 列空 = 上游透传（透传成功行 error 留空，v0.28 变更注记的既有纪律；`upstream_error` 只在拿不到上游响应时落，`server.go` 的 502 路径）。实现时补了第三个词 `queue_abandoned`（#60）：排队途中客户端自己断连，status 记 499（nginx 惯例码）、不写错误体——这种请求一个字节都没碰过上游，混进 `upstream_error` 是冤枉渠道，而它在拥塞期恰恰是常态收场。
- 管理端零改动：不做实时 in-flight/拒绝率展示（内存态，要新开接口读信号量，后加成本低），不拉上游 Prometheus 指标（口径层 §3 非目标）。
- 验收（Go 集成测试，§9 既有形式，httptest 假上游 + 可阻塞的 handler）四条断言：①并发打超过上限的请求，假上游观察到的最大同时 in-flight ≤ 上限；②队满立即 429，流水 error = `queue_full`；③等待超时 429，error = `queue_timeout` 且 `queue_wait_ms` ≈ 超时值；④排队中客户端断连即出队、不向上游转发。不往仓库放压测脚本；**真机对照是部署检查项**（#53 标定并设上限后，压测看 `sglang:num_running_reqs` 是否被压在上限内），不属于本仓库的测试。

### 7.6 Codex 压缩止血闸（口径层 v0.54，#71 落地）

口径层已裁：转换路径遇 `compaction_trigger` 明确报错 + drop 日志；渠道加 compaction 能力位，为否的**透传**渠道对压缩 turn 同样拒绝；`POST /v1/responses/compact` 回 501。本节是止血半边的实现落点，正式修法（本地合成）见 #74。

- **判据**：`openairesponses.HasCompactionTrigger(body)` —— 只解顶层 `input`、逐项取 `type`，见到 `compaction_trigger` 即真。`input` 是字符串、缺失、整体不是 JSON、数组里混了解不动的元素，都只让那一项落空而不影响其余项；判不出来一律 false。位置不当判据（尾项是 Codex 的实采形态，但没有哪条协议承诺只能在尾部）。
- **闸点**：`server.relay` 在 `store.Resolve` 之后、透传/转换分岔之前调 `rejectCompaction`（`internal/server/compaction.go`）。放在这里而不是 `decodeInput`：一份带 trigger 的 Responses 请求体是**合法**的，让 `DecodeRequest` 对它报错会撞 §5 的全函数契约；而且 codec 是纯函数、看不见渠道能力位，透传路径根本不进 codec。
- **只剩透传半边**（v0.54 起）：转换路径原本无条件拒，本地合成（§7.7）落地后它自己产得出那个 item，不再是拒绝的理由；`rejectCompaction` 现在见到非透传渠道直接返回 false。留下的判据是 `cand.SupportsCompaction`，为是则一个字节都不扫、原样透传（trigger 逐字节到达上游）。
- **收场**：发上游之前的普通 400，按入口协议原生错误形状回，文案我方固定词表（「去渠道页把能力位勾上」），不带 base_url 与上游 key。流水 `error` 列记 `compaction_unsupported`，日志一行 `拒绝 Codex 压缩 turn：透传渠道未声明支持 compaction`，带 `channel` / `channel_protocol`——这行就是口径要的 drop 日志。（v0.53 及以前这行还带 `path`（passthrough | convert）；转换那一支消失后这个字段恒为 passthrough，一并删了。）
- **能力位**：`channels.supports_compaction`，默认否（§7 DDL）。管理端 PUT 缺省不动列（哨兵 `nil`，不借零值），表单那一栏只在勾了 Responses 时露出、也只有露着才传；取消勾 Responses 不去清那一列（清了也读不到，勾回来还得再想一遍）。
- **v1 compact**：`POST /v1/responses/compact` 一行裸路由回 501 + 文案，不挂鉴权与流水中间件。此前它落在管理端的 SPA fallback 上，客户端拿到的是一页 HTML 或裸 404，两样都读不出「网关不做这件事」。
- **验收**（`internal/server/compaction_test.go`）：透传能力位为否时压缩 turn 回 400、**假上游收到 0 个请求**、流水词与日志对；能力位为是时放行且请求体除顶层 model 外逐字节保真；同一渠道上的普通 turn 不受影响（这一位不是「Responses 透传总开关」）；v1 compact 带不带 key 都 501。管理端 PUT 缺省不动列另有一条（`admin_test.go`），老库 ALTER 一条（`store/compaction_internal_test.go`）。压缩 turn 的真实 golden 转录是 #73，本节的用例是合成 JSON——拒绝路径不是转换路径，不欠 golden。

### 7.7 Codex 压缩本地合成（口径层 v0.54 正式修法，#74 落地）

转换路径上没有 compact 端点可转发（Anthropic / CC 都没有这个概念），唯一能让压缩真正可用的路子是本地合成：把压缩 turn 改写成一次普通的总结请求打给上游，再把上游吐出来的摘要装进一个自造信封当成 compaction item 发回去。与 opencodex `src/responses/compaction.ts` 同构（MIT，PO 2026-08-13 裁定照抄）。落点全在 `internal/protocol/openairesponses/compaction.go` 与同包的 decode/encode 两侧。

- **识别**：`decodeInput` 见到 `compaction_trigger` 置 `Codec.compaction`，item 本身不进消息序列（它不是内容，是一句「这一轮请你总结」）。透传半边那个 `HasCompactionTrigger` 仍独立存在——透传根本不进 codec。
- **改写（summarizer turn）**：`rewriteAsSummarizer` 剥掉 `Tools` / `ToolChoice` 与 Extras 里的 `text`（结构化输出与 verbosity），末尾追一条带压缩 prompt 的 user 消息。剥工具是必须的：留着上游多半去调工具，这一轮一个字的摘要都没有。`reasoning` **不剥**——压缩 turn 照样要推理。图片不用剥：压缩轮照样要把图带给上游。
- **合成**：编码侧在合成模式下抑制全部正常 output item，把 assistant 正文攒进缓冲，收尾时发**恰好一个** `response.output_item.done`（`{"type":"compaction","id":"cmp_…","encrypted_content":"ptg1:"+base64(摘要)}`）+ `response.completed`。**只发 done、不发配套的 `output_item.added`**（同 opencodex）：这个 item 不是逐步生成出来的，added 描述的那个「开始了」的时刻并不存在。**真实转录（#73，2026-08-13）与这条不一致但不推翻它**：官方上游 added 与 done **两个都发**，且两份 `encrypted_content` 不同（676 / 1164 字节，added 那份短得多），`response.completed.output` 反而是**空数组**。决定权在客户端——codex-rs `collect_compaction_output` 只在 `ResponseEvent::OutputItemDone` 上累加，`compaction_count != 1` 才报 Fatal，`output_item_count` 仅进错误文案（Apache-2.0，2026-08-13 核）；回带轮里那个 item 的 id 与密文也正是 done 那份。所以只发 done 是安全的，而「多发一个非 compaction 的 item 会不会致命」的答案是不会——**但正文仍不以普通消息形态下发**，理由从「怕致命」换成下面这句：摘要正文不以普通消息形态下发——发了 Codex 会把它连同 item 里那份记两遍。
- **五种不产 item**（`compactionNoItem`）：截断（`length`）→ `response.incomplete` + `reason: max_output_tokens`；内容过滤（`content_filter`）→ `response.incomplete` + `reason: content_filter`；**改去调工具**（`tool_calls`，v0.56 补）、**上游断流兜底收尾**（下条）与正常收尾却零字摘要 → `response.failed` + 一句给人看的原因。前四支合起来对 canonical 的停因全集（`stop` / `length` / `content_filter` / `tool_calls`）是**完备**的：只有 `stop` 走得到产 item 那一步，漏掉任何一个非 `stop` 停因都等于把「上游没写完」当成「上游写完了」。**判据的要害不是「不产 item」而是「不许发成 completed」**——`completed` + 零 item 正是 #71 要杀的静默 Fatal 形态。返回值是 `*compactionFailure`（`wireReason` + `message`）而不是一个 string：`wireReason` 非空才是线格 `incomplete_details.reason` 的合法取值，混成一个 string 的话 `empty_summary` 这类自造哨兵会漏进线格字段冒充 reason 码。非流式那条路同理：`EncodeFullBody` 在压缩模式下产不出 item 时直接报错，不回一份空 output 的 200。
- **断流那一种在 wire 上看不出破绽**，所以 canonical 加了一位（v0.55）：两个解码器为了给下游一个合法取值，都会把「上游没声明 stop reason 就断了」兜成 `StopReason: "stop"`（`anthropic` 的 `emitDone`、`openaicc` 的 `finish`），与真正的收尾同形；光看 `e.stop` 的话，一段写到一半的摘要会带着 `completed` 装回历史，正是本节要杀的形态。`protocol.Event` 因此在 `EvDone` 上加 `Truncated bool`，兜底收尾时置位，普通路径不读它。编码侧另外把 `e.stop == ""` 也算一种不产——今天两个解码器都会兜，这条是给直喂事件的调用方与将来不兜的解码器留的余量。**非流式两条路同样置位**（v0.56 补）：`openaicc` 的 `DecodeFullBody` 复用 `streamState.finish`，白捡这一位；`anthropic` 的 `DecodeFullBody` 不经 `respState`（没有 channel、没有跨帧状态），`EvDone` 是手搓的，v0.55 只改了流式那半边。解得开的 JSON body 里 `stop_reason` 缺失不是「流断了」而是「上游没声明这轮是怎么收的」，对压缩合成是同一个失格理由——不置位的话，一份没声明收尾的响应会被当成完整摘要装回 Codex 的历史。收尾这一处是两条路径唯一各写一遍的地方（内容块共用一处解析），加字段时两边都要改。
- **G2 回带还原**：`compaction` / `compaction_summary` / `context_compaction` 三种 item 走 `compactionItemText`——信封解得开还原成 `summaryPrefix + 摘要` 的 user 消息，解不开（真 OpenAI 密文、别家信封）降级成固定占位并把 type 记进 `CompactionDrops`，日志由 `relayConverted` 打。**那行日志不能罩在「本次是压缩 turn」里面**（v0.56 修）：Codex 只在需要压缩的那一轮发 trigger，回带解不开发生在**之后的普通请求**上，`CompactionTurn()` 为假；罩着的话混路场景的头一次——也正是最该归因的那次——完全无声。不带 `encrypted_content` 的 `context_compaction` 是 codex-rs **本地**压缩的标记，没有摘要可还原，跳过而不占位。这一条单独也治混路失忆（先经透传渠道压缩成功、后续被路由到 A/CC 渠道）。
- **静默期心跳**：合成期吞掉增量时按 15 秒下限发一行 SSE 注释（`: portage compaction in progress`）。注释行是 SSE 规范里的合法帧、合规解析器忽略，不进 `sequence_number` 连号。**正文增量与思考增量都发**（v0.55 补）：`rewriteAsSummarizer` 有意留着 `reasoning`，开思考的上游会先想上几十秒才写第一个摘要 token，那段静默通常是整轮里最长的一截，恰恰落在这条心跳自称覆盖的范围里。它盖得住的仍只有「增量在流、被我们吞掉」这种静默——上游整体卡住时它也不发，那种情况该由上游超时接管，不该由一条假装还活着的心跳掩盖。
- **验收**：`internal/protocol/openairesponses/compaction_test.go`（信封往返与拒收外来信封、改写、还原三态、五种不产 item、心跳节奏用注入时钟含思考段、透传闸判据表）+ `internal/server/compaction_test.go` 三条端到端（合成、回带还原、回带解不开的丢弃日志）+ 两个解码器各一条流式 `Truncated` 置位用例、`anthropic` 另一条非流式的（含 `stop_reason` 缺失与 `null` 两格）。**golden 已补**（#73，2026-08-13，口径层 v0.59）：`compaction_golden_test.go` 四条拿真转录（`responses-stream-compact-turn1/trigger/replay`）驱动解码半边——压缩 turn 认得出且工具剥净、回带轮不是压缩 turn 且真 OpenAI 密文降级占位并登记、压缩前普通轮放行、compaction item 线格形状。手搭 fixture 原样留着：**两者钉的不是同一件事**，fixture 钉我们的口径自洽（五种不产 item、心跳节奏这些上游不会主动演给你看），转录钉「Codex 真的这么发包」。**采样的前置条件在口径层 v0.59**：客户端 provider 的 `name` 必须是 `"OpenAI"`，否则 `/compact` 静默走本地压缩。

## 8. 最小管理接口

- `GET /healthz`
- `GET /v1/models`：返回**全部可路由的模型名**（harness 启动时会拉），格式为 OpenAI 公开的 `{"object":"list","data":[{"id":…}]}`

> **两半、两者都可路由（口径层 v0.32）**：未停用的接入点名，加上可用纳管模型的限定名 `渠道名/纳管模型名`。实现是 `store.ListExposedModels` 里一条 `UNION ALL`，`ORDER BY direct, id` 把接入点排在前面，Go 侧按 id 先到先得去重——于是「接入点优先」在列表和 `store.Resolve` 里是同一条规则。
> 直连那半边只列**当下真能打通**的（渠道启用 + 模型启用 + 渠道有启用凭证）；接入点那半边不做这层过滤，它归启动闸管（v0.18/v0.21）。直连没有 `candidates` 行，启动闸看不见它，所以这道过滤只能长在列表查询里。
> 这张表唯一的契约是**列出来的都调得通**，因此它认的名字集合必须和 `store.Resolve` 逐字一致，改一边就得改另一边。

> **不迎合 harness 的私有目录格式（M0 验收实测，2026-08-06）**：Codex CLI 拉的其实是 OpenAI 的**私有**模型目录——`{fetched_at, etag, client_version, models:[{slug, supported_reasoning_levels, apply_patch_tool_type, …}]}`，与公开的 `/v1/models` 不是一个东西。拿不到时 Codex 打两条 warning（`Model metadata for X not found. Defaulting to fallback metadata`、`service tier priority is not advertised…`）后**照常工作**，整轮工具调用不受影响。故本项目**不实现该私有格式**：它无公开契约、字段随 Codex 版本漂移，为它建一张模型能力表要长期跟着上游跑，而收益只是消掉两条 warning。降级路径已实测可用，就停在降级上。
- Anthropic 出口/入口的 `count_tokens`：上游为 Anthropic 时原样转发，否则**本地估算回 200**（口径层 v0.80；原「否则 501」已废，实现见 [#18](https://github.com/SimonGino/portage/issues/18)，代码尚未落地）

**以上是业务面，全在 `internal/server`**（口径层 v0.28：`/healthz` 与 `/v1/models` 不归 `admin`）。

### 8.1 管理端 API（M3，`internal/admin`）

全部挂在 `/admin/api` 下，认 cookie 会话；`/admin` 下的其余路径发 SPA。错误统一 `{"error":"…"}`，写成功回 204 或一个小 JSON。

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/admin/api/login` | 验密码发会话；未设密码回 503 并说明补救动作（跟「密码错」分开说） |
| POST | `/admin/api/logout` | |
| GET | `/admin/api/session` | `{authenticated, password_set}`，前端加载时问一句 |
| POST | `/admin/api/password` | 改密码；**已登录也要验旧密码**（cookie 可能是别人留下的），成功后吊销全部会话 |
| GET POST | `/admin/api/channels`、PUT DELETE `/channels/:id` | 渠道 CRUD；创建时可选带一把凭证 |
| ~~PUT~~ | ~~`/admin/api/channels/:id/credential`~~ | 整把替换，**v0.35 起作废**（口径层 v0.38 放开多凭证） |
| GET POST | `/admin/api/channels/:id/credentials` | 凭证逐条 CRUD；GET 只回名字/状态/时间/停用原因，**永不回凭证值**；POST 支持一次贴多份（语义为追加） |
| PUT DELETE | `/admin/api/credentials/:id` | 改名 / 停用 / 启用 / 删除；改凭证值也走 PUT，同样没有对应的读 |
| POST | `/admin/api/channels/:id/models` | 加纳管模型；PUT DELETE `/channel-models/:id` 停用/删除。两者的 body 都可带 `protocols`（v0.38，协议子集；PUT 不传该字段=不动它，传空数组=清成继承） |
| POST | `/admin/api/channels/:id/fetch-models` | 拉上游 `/v1/models` 给表单做预勾选（v0.38）。**POST 而非 GET**：它朝上游发真请求、花上游的配额，不该被浏览器或中间层当可缓存的读操作重放。回一组 `{protocols, models, status, detail}`，**不落库、不进路由** |
| GET POST | `/admin/api/access-points`、PUT DELETE `/access-points/:id` | 接入点 + 候选一起写（见下） |
| GET POST | `/admin/api/keys`、PUT DELETE `/keys/:id` | 创建回 `{id, key}`，明文**只这一次** |
| GET | `/admin/api/logs?limit=&offset=&before=&model=&key=&only=bad` | 近期流水，limit 上限 500，回 `{rows, total}`（v0.60；此前是裸数组）。筛选**全在后端**（v0.53）：前端在已拉回的一页里过滤，筛出的是「这一页里的失败」。**翻页 = `before` 钉窗口上沿 + `offset` 在窗口内定位**（v0.60）：单用 offset 会错位（流水是时间序、新行插在头部，翻到第二页时已被推着往后错），单用游标跳不了页（没有逆向形式、也没有「往前数 60 条」这种形式）。管理端进页时拿第一发的最大 id 当上沿、之后每发都带 `before=anchor+1`。`total` 按**同一组条件、同一个 before** 数，于是它是页码的分母且翻页途中不变；两条语句不在一个事务里，唯一的窗口是不带 before 的第一发，下一次点击自动纠正。行里带 `upstream_request_id`（v0.70）：不可空，**空串照原样给不转 null**，前端只判空串。**没有按它筛的参数**——它逐次唯一，筛出来永远只有一行（口径层 v0.67 ⑥） |
| GET | `/admin/api/usage?days=&by=model\|key\|credential` | 汇总，`by` 选维度：按模型（默认）、按**网关 API Key**（v0.53）或按**上游凭证**（v0.35）。后两者是两件事，标签写全称——只写「按凭证」两边都像 |

三条实现口径：

- **能保存下去的配置，一定是能启动的配置**：每个写接口都在**同一个事务里**跑一遍 `store.Validate`，不过就回滚并把校验原文原样回给前端（400）。这要求 `Validate` 及其全部子检查收 `store.Queryer`（`*sql.DB` 与 `*sql.Tx` 的公共只读面）而不是 `*sql.DB`——连接池是 1，事务开着时再拿 `*sql.DB` 查会等一条永远回不来的连接，**自锁不报错**，表现是保存请求直接挂住。
- **接入点与它的候选一起建**：分两个接口意味着中间必然存在一个「零候选」的瞬间，而那个瞬间会被上面的校验判为非法，于是第一步永远保存不了。
- ~~**凭证先删后插**，不用 UPDATE~~：立论是临时闸的「恰好 1 份启用凭证」，**该闸已于口径层 v0.38 放开，此条随之作废**。改为**逐条 CRUD**（加 / 删 / 停用 / 启用），另给一个语义为**追加**的批量粘贴入口。整把替换在多凭证下讲不清楚：覆盖会连带清掉已停用的凭证，而那是 401 摘除的现场。列表回名字、**凭证值**、状态、创建时间、停用原因与时刻——值自口径层 v0.47 起回读（推翻 v0.28），页面上默认掩码、给「显示」与「复制」；原立论里「值不回读 ⇒ 页面上对不齐」那半条随之作废，摘除现场那半条仍然成立。

## 9. Golden 测试方案

**样本采集（M0 抓子集、M1 补全，别等写完代码）**：用真实渠道抓下列 SSE 转录（raw 字节存档）。样本 1~6 刻意选「同语义、双协议」场景，天然构成 P1 转换测试的黄金输入对。场景清单可对照 sub2api `apicompat/` 的测试文件命名（`tool_pairing`、`parallel_tool`、`stream_lifecycle`、`codex_events` 等）补漏：

| # | 样本 | 场景 |
|---|------|------|
| 1 | Anthropic streaming | 纯文本长回复 |
| 2 | Anthropic streaming | 单次 tool_use |
| 3 | Anthropic streaming | 并行多 tool_use 交错增量 |
| 4 | OpenAI CC streaming | 纯文本 |
| 5 | OpenAI CC streaming | 单次 tool_calls（参数跨 chunk） |
| 6 | OpenAI CC streaming | 并行 tool_calls，index 交错 |
| 7 | 以上 1~6 的非流式版本 | |
| 8 | OpenAI Responses streaming | function_call 事件序列（P1 备料） |
| 9 | 上游 429 / 500 / 流中途断连 | failover 与流内错误注入 |

**M0 必抓子集（v0.10）**：样本 1~3（Anthropic 流式：文本 / 单 tool_use / 并行 tool_use）、4~6（CC 流式：文本 / 单 tool_calls 参数跨 chunk / 并行 index 交错）及其非流式版本（样本 7 的对应部分）。理由是 Tap 的测试要真实转录作输入，M0 就得有，等不到 M1。样本 8（Responses）与 9（上游异常）仍留 M1。**raw 字节存档前须人工过一遍，去掉真实凭证与个人对话内容。**

> **M0 必抓子集补齐（#7，2026-08-11）**：`anthropic-*` 六个入库，`golden_test.go` 12 个样本零 skip。采自**第三方 Anthropic 协议中转**而非官方直连（PO 2026-08-10 裁定可当真实上游用），依据是先核了透传：中转跑的是 `sub2api`，响应体按行原样回写、只旁路解析 usage，佐证是响应里的 `usage.iterations`、`inference_geo` 在它源码里根本不存在。采集时要绕的三个雷（假响应顶包、`session_` 前缀工具名被改写、请求体注入）与操作坑记在 `testdata/golden/README.md`，这里不抄第二份。
>
> 两处**样本与现实的出入**要跟着样本走：①**`InputTokens` 恒偏大 357**——中转往每个请求塞一段固定内容，两个不同长度的 prompt 差值一致。不影响样本作数（`golden_test.go` 只喂 `response.raw`，`request.json` 从不参与断言，数值前后自洽），但**别拿这批样本推请求体与 token 的关系**。②**cache 计数全 0**：`cc-*` 那批特意补过缓存命中，Anthropic 这侧还没有，`cache_read_input_tokens` 的解析路径目前只有 CC 样本走到。③**响应头保真度这里验不了**——中转有响应头白名单，`request-id`、`anthropic-ratelimit-*` 到不了，要验得等官方 key。

> **构造样本另立一档（v0.56，#37 缩范围那一半）**：上面②那条缺口先补形状——`testdata/fixtures/anthropic-cache-hit`、`anthropic-stream-cache-hit` 从对应真实转录派生，**只改 usage 数字**，取值依据官方文档（`input_tokens` 只算最后一个缓存断点之后的量，与两项缓存互不相交）。它驱动两条此前无 Anthropic 样本走过的路：Tap 的原始语义（净值）与 canonical 的毛值归一（`decode_response.go` 那段加法，`internal/protocol/cachehit_fixture_test.go`）。
>
> **`fixtures/` 与 `golden/` 是两档，闸门也反着开**：golden 认 `verified: true`（人核过的真实转录，拦「没人核过就当事实源」），fixtures 认 `synthetic: true`（拦「构造样本改名搬进 golden 冒充转录」）。构造样本能证明「我们的解析对这个形状是对的」，证明不了「上游真的这么发」——后者仍是 #37 的验收，拿官方 key 按 `request.json` 里的形状打两遍、取第二遍，录进 `golden/`。对照表见 `testdata/fixtures/README.md`。

> **样本 8（Responses 流式）落地（#79，2026-08-14）**：五份真实上游转录入库——`responses-stream-text` / `-tool-turn1` / `-tool-turn2` / `-parallel-turn1` / `-parallel-turn2`，采法与脱敏见 `testdata/golden/README.md`。**票面写的 `function_call` 事件序列没采到、也不必再采**：这版 Codex 把 `exec` 声明成 custom 工具，线上走的是 `custom_tool_call` 那套事件（`custom_tool_call_input.delta/.done`），`function_call_arguments.*` 是同形状的另一半，`EncodeStream` 两条分支共用一段代码。**「并行」这一格采到的是 code-mode 形态**——线上仍只有一个 `custom_tool_call`，并行在工具入参 JS 的 `Promise.all` 里；线级两个并行工具项真上游逼不出来，仍由 `in-responses-parallel-turn2` 那个 stub 覆盖。
>
> 跟着这批留下的两个**已知缺口**：①**Responses 非流式无任何真实样本**——十份 Responses 转录（压缩批 3 + reasoning 批 2 + 本批 5）全是 `stream: true`，`DecodeFullBody` / `EncodeFullBody` 的线格只有参考仓库背书。要补就拿录下来的 `request.json` 改 `"stream":false` 重放一遍，不必新造场景。②**终帧 `response.completed.output` 的 item 形状在这个渠道验不了**——三批全经同一个中转，那份列表是它重组的降级形态（工具 item 改回 `function_call`、丢 `arguments`、reasoning item 不列、item 上连 `id`/`status` 都没有），要等官方直连才验得了。

**采集与存放（v0.13 落地）**：录制反代 `cmd/goldenrec`（刻意在 `internal/` 之外——它只为喂测试库存在）转发到真实上游并把每次调用的原始字节落盘。样本库在仓库根 `testdata/golden/<样本名>/`，含 `meta.json`（protocol / stream / endpoint / status / source / expect / verified）、`request.json`、`response.raw`；不放在某个包的 `testdata/` 下，是因为同一份样本到 P1 还要喂给 codec 的跨协议用例。

`meta.json` 的 `expect` 由 goldenrec 用 Tap 自己预填，**只是草稿**：出自被测代码的期望值等于让实现给自己判卷，因此 `golden_test.go` 拒绝一切 `verified: false` 的样本。把 `verified` 置 true 是人工关卡，与「脱敏时人工过一遍」是同一道工序——核对脱敏、核对 expect 与原始字节相符，一起做。未采集的样本按名字逐个 skip，目录空着不会一路绿灯。

**入站样本（v0.22，M2 起）**：上面说的是**上游响应**转录，驱动 Tap 与 codec 的编码侧；codec 的**解码**侧要的是另一端——harness 发出来的入站请求字节。两类样本同库不同 `direction`（`upstream` / `inbound`），后者无 `response.raw` 与 `expect`，另有 `headers`（白名单）与 `stub`。

采集不能靠 gateway 的 `log_bodies`：那条路是排障日志，单侧 body 有 64 KiB 上限（`internal/server/calllog.go` 的 `bodyCaptureLimit`），而 Claude Code 带全套 tool 定义与长上下文时轻易越过它，半截样本看着还像回事、喂给 codec 才发现是坑。改由 `cmd/goldenrec` 的 **inbound 模式**全量落盘（超限报错，绝不截断）。

> **实测量级（v0.23，M2-1 采集，2026-08-07）**：Claude Code 2.x 无头单轮请求体 **185 KB**，其中 42 个 tool 定义占大头；Codex CLI 0.144.1 是 47~50 KB。也就是说 `log_bodies` 对 Claude Code 是**几乎必截断**，不是「长上下文时偶尔越过」。
>
> 这不改 `bodyCaptureLimit` —— 64 KiB 对排障日志是对的上限，一条长流的完整 body 进日志只会把日志冲垮。要改的是别处的预期：`log_bodies` 的截断标记（`truncated`）在真实 harness 下是常态而非异常，读日志时别把它当故障信号；要完整字节一律走 goldenrec。

> **stub 应答（v0.22 新决策，PO 裁定 jinpenga）**：inbound 模式不碰上游、不要凭证，按脚本回**手写的假响应**。
>
> 理由是采集目标里最有价值的那份是**第二轮**请求——带 `tool_result`（Anthropic）或 `function_call_output`（Responses）的那个包，`tool_use → tool_calls` 的映射全靠它钉住。而 harness 只有先收到过一个合法的 tool 调用响应才会去执行工具、才会发出第二轮；手上没有 Anthropic / OpenAI 官方 key（#7 仍挂着），纯录制回 501 的话对话在第一轮就断了。
>
> 边界要划清：**stub 是道具，不是样本**。它手写、不保真、不进 `testdata/golden/`——一旦混进转录库就是往事实里掺伪造。入库的只有 harness 发出来的 `request.json`，仍是 100% 真实字节。有真实上游时一律走 proxy 模式，那边顺带把出站样本也采了。
>
> 四条实现口径：脚本按文件名顺序一请求消耗一个，**发完报 503 不循环重放**（静默重放会让 harness 原地打转）；`count_tokens` 就地估算**不消耗脚本**（Claude Code 每轮都打它，吃掉一格会把后面全串位）；未预料的端点回 404 且不消耗脚本；**`GOLDENREC_SIDECALL=notools`（v0.30，默认关闭）** 把「没声明 tools 的请求」当旁路调用——照录、给个最短的合法应答、不消耗脚本。脚本与调参见 `testdata/goldenstub/README.md`。
>
> 第四条为什么是开关而不是默认行为：它冲着 opencode 每开一个会话先发的那条「给这段对话起个标题」去——那是同一个端点上的旁路请求，`count_tokens` 那种「换个端点」的办法在 CC 上不成立，只能靠请求体判别。而「没声明 tools」是 **harness 的癖性，不是协议事实**：一个不带工具的纯对话 harness，它的 agent 轮本来就没有 tools，默认吞掉就等于采不到那种样本。判错的方向也不对称——误判成旁路，症状是 harness 收到一句废话且脚本一格没走，日志里看得见；漏判才是灾难，串位之后 harness 收到的是形状对而内容驴唇不对马嘴的回复，不报错。

**入站 CC 语料（v0.30，#27）**：`in-cc-*` 六份，opencode 1.18.4 实采。

harness 选型是被逼出来的：**Codex CLI 0.144.1 已经不支持 `wire_api = "chat"`**（二进制里写死了这句话，并提示改用 `responses`），拿它采不到 CC 入站字节。手上原生说 CC 且带原生工具调用的是 opencode——走 `@ai-sdk/openai-compatible`，直接 POST `/v1/chat/completions`，还有 `opencode run` 非交互模式可脚本化。**这件事本身是 ③/④ 排序的需求侧证据**：PO 日常用的两个 harness（Claude Code、Codex CLI）没有一个说 CC。

| 样本 | 形状 | 钉住什么 |
|---|---|---|
| `in-cc-text` | system + user + 10 tools | agent 轮即便被要求「别调工具」也照发全套声明 |
| `in-cc-tool-turn1` | 同上 | 触发工具调用的那一轮 |
| `in-cc-tool-turn2` | + assistant(tool_calls) + tool | **主目标**：`tool_calls` ↔ `tool_call_id` 的对应 |
| `in-cc-parallel-turn1` | 同 turn1 | |
| `in-cc-parallel-turn2` | + assistant(2 个 tool_calls) + **两条** tool 消息 | 见下 |
| `in-cc-consecutive-user` | system + user + user | 相邻同 role，且不声明 tools |

采集中撞出两条转换约束，逐键归宿见 `canonical_coverage_test.go`（文档不抄第二份）：

- **工具结果的容器形状两边相反。** CC 是每个调用一条独立 `tool` 消息（实采 `in-cc-parallel-turn2` 两条），Anthropic 要求所有 `tool_result` 挤进**同一条** user 消息。CC→A 的编码侧要做合并，不是逐条平移。
- **`stream_options.include_usage` 不能丢。** CC 独有的开关，不给就不该发那个 usage chunk。入口半边的 `EncodeStream` 要靠它决定回程补不补 usage 帧——丢了只能猜，两个方向各错一半。它进 Extras 而非 canonical 字段，因为 Anthropic / Responses 没有对应开关（usage 恒发）。

> **脱敏工序补一条教训（v0.30）**：采集环境要连 `HOME` 一起换，只隔离 `XDG_CONFIG_HOME` 不够。第一轮只换 XDG 时，opencode 把 `~/.agents/skills/` 下的**个人 skill 清单（名称 + 描述 + 本机路径）**塞进了 system prompt——52 处本机用户名，system prompt 27.8 KB。换掉 HOME 后降到 9.5 KB，只剩 harness 自带内容。
>
> 一般化的那条：**harness 的 system prompt 是本机环境的函数**，不是常量。它会把插件、skill、项目配置、git 状态卷进去，而这些正是「个人内容」最容易漏网的地方——凭证有形状好 grep，个人配置没有。采集前先拿一份看看它到底装了什么，比事后 grep 可靠。

**测试方法**：样本 → DecodeStream → 内存事件序列 → （跨协议用例再过 EncodeStream+对方 DecodeStream）→ 语义比对（忽略空白与顺序无关差异，比对文本全文、工具调用 name/参数解析后相等、usage、stop reason）。字节级 diff 只用于透传回归。

### 9.1 R→CC 的用例分工与已知缺口（#12，2026-08-08）

三层，各管各的，不重叠：

| 层 | 位置 | 输入 | 钉什么 |
|---|---|---|---|
| 解码 | `openairesponses/decode_test.go` | 4 份真实入站样本 `in-responses-*` | 全函数、工具 kind 分类、连续同侧 item 并成一条消息、密文不进 Text、顶层独有字段进 Extras |
| 编码 | `openairesponses/encode_test.go` | 手写事件序列 | 线格式（帧序 / `sequence_number` / 无 `[DONE]`）、对称拆包、item 类型随请求声明而变 |
| 整链 | `server/convert_responses_test.go` | Codex 形态请求 + 假 CC 上游 | 出站请求是合法 CC（含 JS 入参被包成 JSON）、下行流是 Responses 且拆了包、非流式聚合、密文丢弃不报错、闸门只开这一格 |

编码层**不回放** `raw/resp-*` 转录：`raw/` 在 `.gitignore` 里，CI 上那些文件根本不存在，回放式用例会集体 skip 成一片假绿。转录的作用是定形状，定完把期望写死在测试里。要让 CI 真的跑转录，得先把它们过一遍脱敏 + `verified: true` 的人工关卡再提升出 `raw/`——那是**上游侧** Responses（③下半 CC→R、④ A→R）才真正需要的事，留到那一刀。

**已知缺口，不装作没有**：

- 四份入站样本**全是 `stream: true`**（Codex CLI 就没有非流式模式）。非流式 R→CC 与字符串形态的 `input` 只有手写用例，没有真实样本背书。
- `parallel_tool_calls` 在 Codex 侧恒 false（并行发生在那段 JS 的 `Promise.all` 里，线上永远只有一个 `custom_tool_call`），所以「多路 tool_call 交错重组」这条在 R 入口方向**验不到**，只能靠 CC 语料在 A→CC 那边验。
- `response.reasoning_summary_text.delta` 没实现：CC 解码侧根本不产 `EvThinkingDelta`，而手上三份 Responses 转录里的 reasoning item 只有 `encrypted_content`、一条 delta 都没有。等 A→R（优先级④）拿到真实转录再补，现在写等于照文档猜。
- 解码侧丢弃未知 item 时没有日志（口径层 §2.6 要日志警告）。见 §5 坑清单同名条目，待 PO 裁决。
- 全链只对着**假** CC 上游跑过。真机验收（Codex CLI → 网关 → 第三方 CC 上游整轮工具调用）挂 [#98](https://github.com/SimonGino/portage-legacy/issues/98)，PO 手动清单，不作为合并闸。（原挂 #12，PO 2026-08-14 裁定六条路的真机验收合并到 #98。）

### 9.2 R→A 的用例分工与已知缺口（#25，2026-08-08）

分工同 §9.1 的三层，只列与 R→CC 不同的部分：入口那半边（`openairesponses.DecodeRequest`）两条路共用，断言不重复；这边验的是 **anthropic 出口半边**。

| 层 | 位置 | 输入 | 钉什么 |
|---|---|---|---|
| 编码 | `anthropic/encode_request_test.go` | 手搭 canonical | Anthropic 协议自己的硬约束：`max_tokens` 必填与三级兜底、`RoleSystem` 上提到 `system`、相邻同角色合并、`input_schema` 必填与 custom 工具的合成、非 JSON 入参对称包装、孤儿 `tool_result` 丢弃并登记 |
| 解码 | `anthropic/decode_response_test.go` | 手抄 SSE（形状照 5 份真实转录） | 帧序、`ping` 与脏帧容忍、`usage` 两次是累计快照、thinking/signature 分走两条通道、只有工具块发 `EvToolCallEnd`、截断兜底收尾保留 `stop_reason`、停止原因两条映射互逆 |
| 整链 | `server/convert_r2a_test.go` | Codex 形态请求 + 假 Anthropic 上游 | 出站是合法 Messages（JS 入参包成对象、`input_schema` 合成出来、Responses 独有字段一个不漏）、下行是 Responses 线格式（Anthropic 事件名不漏、不发 `[DONE]`、拆包回裸 JS）、thinking 丢弃不炸且 signature 不漏进正文、非流式聚合、闸门开这一格 |

编码层与解码层**都不回放** `raw/anthropic-*`：`raw/` 在 `.gitignore` 里，CI 上不存在，回放式用例会集体 skip 成假绿（#12 已踩过）。转录的作用是定形状，定完把期望写死在测试里。手抄时刻意保留了两处真实特征——data 负载尾部的空格填充（上游抗缓冲手段）、中途插入的 `ping` 帧——解码器必须无视这两样。

**已知缺口，不装作没有**：

- ~~**上游的 thinking 在这条路上今天仍丢弃，Codex 看不到 Claude 的推理过程。**~~ **已销账（#4，2026-08-17）**：出向合成落地，摘要走 `reasoning_summary_*` 交给 Codex，签名照旧不外漏；实现与用例见 §9.6。以下是销账前的原文，留着是因为它记着「挡着的从来是样本、不是口径」这个判断过程。
  - **上游的 thinking 在这条路上今天仍丢弃，Codex 看不到 Claude 的推理过程。** 不是疏忽：Responses 有 `response.reasoning_summary_text.delta` 可以承接，但手上三份 Responses 转录里的 reasoning item 只有 `encrypted_content`、一条 delta 都没有，照文档猜着造 item 比明着丢更危险。**这条缺口的性质在口径层 v0.62 之后变了**：口径已裁「Responses 出向也要合成」，挡着的只剩样本，不再是「不做伪映射」的口径结果。**样本已由 #93 采到**（`responses-stream-reasoning-turn1` 录全了 `reasoning_summary_part.added` → `text.delta` → `text.done` → `part.done`，上面那句「一条 delta 都没有」只描述 #93 之前的状态；转录随该票分支入库），合并后这一格只剩实现。**这是 UX 层面的可感知退化，不只是内部细节。**
- 五份 Anthropic 转录**全是 `stream: true`**（Claude Code 恒发流式），`DecodeFullBody` 没有真实样本背书，形状是照协议文档 + 与流式那半边的对称性写的（同 §9.1 里 R→CC 那条的性质）。
- `error` SSE 帧五份转录里一次都没出现（都是 200 正常流），照协议文档实现，用例手写。
- 中段的 `RoleSystem` 消息上提之后丢掉「它插在哪」这个信息。实采里 Responses 侧的 developer 消息全在最前，没有中段用例，按简单规则先来，不为没见过的形态提前设计。
- 全链只对着**假** Anthropic 上游跑过。真机验收（Codex CLI → 网关 → 真实 Anthropic 上游）挂 [#98](https://github.com/SimonGino/portage-legacy/issues/98)，PO 手动清单，不作合并闸；它同时受 #7 制约（需要官方凭证）。（原挂 #25，PO 2026-08-14 裁定合并。）

### 9.3 CC→A 的用例分工与已知缺口（#9，2026-08-10）

分工同 §9.1 的三层。出口那半边（`anthropic.EncodeRequest` / `DecodeStream`）与 R→A 共用，断言不在这边重复；这边验的是 **openaicc 入口半边**，外加两条只有 CC 入口才走得到的出口分支。

| 层 | 位置 | 输入 | 钉什么 |
|---|---|---|---|
| 解码 | `openaicc/decode_request_test.go` | 六份 `in-cc-*` 真实发包 | 全函数（一份都不许解不动）、`role=system` 不在 decode 侧上提、`tool_calls` → `tool_use` 块、`role=tool` → `RoleTool` + `tool_result` 块（`tool_call_id` 从消息级落到块级）、连发 user 不合并、`stream_options` 留在 Extras、工具声明两层嵌套拍平且 schema 存原始字节、`tool_choice` 两种线上形态、`max_completion_tokens` 与老名字都认、多模态 part 与 `content:null` 不炸 |
| 编码 | `openaicc/encode_response_test.go` | 手搭事件序列 | 线格式（只有 `data:` 行、首帧只带 role、正文逐字不合并、finish_reason 单独一帧、usage 帧 choices 为空数组、`[DONE]` 收尾）、`include_usage` 没要就不发也不凭空造零值、工具 index 重编号、error 帧之后不补 `[DONE]`、缺 id 补 `chatcmpl-`、非流式聚合与空入参补 `{}` |
| 出口补漏 | `anthropic/encode_request_cc_test.go` | 手搭 canonical | 两条 R→A 走不到的分支：连着的 `RoleTool` 消息并进同一条 user 消息（靠既有的「非 assistant 当 user」+ 相邻同角色合并叠出来，不是专门逻辑）、`temperature` 截断 |
| 整链 | `server/convert_cc2a_test.go` | 真实 `in-cc-*` 发包（只换 model）+ 假 Anthropic 上游 | 出站是合法 Messages（system 上提、角色严格交替、两条 tool 消息并成一条 user 两个块、`input_schema` 必填、CC 独有字段一个不漏）、temperature clamp、下行是 CC 线格式（Anthropic 事件名不漏、`[DONE]` 收尾、finish_reason 映成 `tool_calls`、usage 帧补上、上游 id 原样透传）、非流式聚合成 `chat.completion` |

整链用例的入站字节直接读 `testdata/golden/in-cc-*/request.json`，只把 `model` 换成接入点名——手搭的请求体只会长成我以为的样子。

**已知缺口，不装作没有**：

- 六份 `in-cc-*` 样本**全是 `stream: true`**（opencode 恒发流式），非流式 CC 入口没有真实样本背书，只有手写用例。同 §9.1 / §9.2 那两条的性质。
- 真实 harness 入站样本的 `content` 仍全是字符串；图片转换靠 **`testdata/fixtures/` 下四份构造样本**（`in-cc-image` / `in-anthropic-image` / `in-responses-image` / `in-anthropic-toolresult-image`，tiny.png 真字节）和手写用例钉。**#1 已落地**：base64 / url 跨协议转换，`file_id` 单独登记 `image_file_id` 后丢；六格全链路见 `server/convert_image_test.go`。样本**不在 `golden/`**（PO 2026-08-17 裁），理由与覆盖表怎么同时扫两个根见 §9.5。
- 样本里 `tool_choice` 只出现过 `"auto"` 一种取值，其余四种形态靠手写用例。
- ~~上游的 thinking 在这条路上必然丢弃~~ **口径层 v0.62 推翻**：CC→A 出向要把 Anthropic 的 thinking 合成为 `reasoning_content`（反向同理），流式非流式都做，signature 省略。原文的立论「CC 没有承接它的位置」是错的——`reasoning_content` 是 DeepSeek/GLM/Qwen 一路的事实标准载体，四家参考实现全用它。实现另起 effort，在那之前这条缺口仍是现状。
- ~~请求侧的 effort 在这条路上丢弃~~ **口径层 v0.72 放开**：CC 的 `reasoning_effort` 直接写成 Anthropic 出口的 `output_config.effort`，字符串原样、不认模型、不带第二个键。原文说的「没有无条件落点」已被实测消掉（#37 评论，2026-08-14）：这个载体对 claude-sonnet-5 合法且生效，且**单发就能开思考**——所以不需要补 `thinking:{type:adaptive}`（补了才是替客户端开思考，撞口径层 v0.65 ④）。老式 `thinking:{type:enabled,budget_tokens}` 一次都不写。实现见 [#99](https://github.com/SimonGino/portage-legacy/issues/99)，落地前这条仍是登记可见的丢弃。
- 并行工具调用的**交错**分片在这条路上验不到：Anthropic 上游的块是严格顺序的（一个 `content_block_stop` 之后才轮到下一个），交错只可能出现在 CC 上游那边，由 A→CC 方向的用例覆盖。
- 全链只对着**假** Anthropic 上游跑过。真机验收（pi → 网关 → 真实 Anthropic 上游）挂 [#98](https://github.com/SimonGino/portage-legacy/issues/98)，PO 手动清单，不作合并闸；同受 #7 制约。（原挂 #9，PO 2026-08-14 裁定合并。）

### 9.4 CC→R / A→R 的用例分工与已知缺口（#80，2026-08-14）

两条路共用 **`openairesponses` 出口半边**，所以分工先按半边切、再按入口切：出口半边的规则（developer 项、扁平工具、`store:false`、采样参数不发）在 `openairesponses/encode_request_test.go` 与 `decode_response_test.go` 里钉一次，两条整链各只验自己那半特有的形态，不重复。

| 层 | 位置 | 输入 | 钉什么 |
|---|---|---|---|
| 解码 | `openairesponses/decode_response_test.go`（golden 段） | 五份 `responses-stream-*` 真实上游转录 | 纯文本整条事件序、`custom_tool_call` 的 Start 取自 `output_item.added`（增量帧只带 `item_id`）、51/107 片入参包成**一条** `{"input":…}`、reasoning item 与 `reasoning_summary_*` 四种帧一个事件都不产、usage 对上样本 `expect`、停因由 output 里有没有工具项判出 `tool_calls`（Responses 没有 `finish_reason`）、回带轮不误判成工具轮 |
| 解码 | 同上（手搭 fixture 段） | 手搭 SSE / 完整响应体 | 真实样本到不了的形态：`function_call` 流（线上 Codex 只用 custom 工具，九份转录里一份都没有）且入参**逐片透传**、`response.incomplete` → `length`、`response.failed` 与裸 `error` 帧 → `EvError`、传输断流置 `StreamReadFlag`、断在终帧前兜 `EvDone{stop, Truncated}`、custom 入参攒到一半断流照样冲出、非流式全部形态 |
| 编码 | `openairesponses/encode_request_test.go` | 手搭 canonical + 真实 `in-cc-*` / `in-anthropic-*` 走半链 | system → developer 项（**不写顶层 `instructions`**）、中段 system 原位不上提、assistant 用 `output_text` / user 用 `input_text`、工具调用与结果摊成顶层 `function_call` / `function_call_output`（`output` 是纯字符串，空则 `(empty)`）、孤儿结果登记丢弃、工具声明扁平、custom 工具补 `CustomToolSchema`、`tool_choice` 六种情形、`store:false` 恒发 / `max_output_tokens` 零值省略 / 采样参数登记丢弃、thinking 与 Extras 一个字节都不外带 |
| 整链 | `server/convert_cc2r_test.go`、`server/convert_a2r_test.go` | 真实 `in-cc-*` / `in-anthropic-*` 发包（只换 model）+ **真实 `responses-stream-*/response.raw` 当上游回话** | 出站打 `/v1/responses`、模型翻译、developer 项、扁平工具、`store:false`、顶层不出现采样参数与两侧的独有字段；下行是**入口协议**的线格式（CC：`chat.completion.chunk` + `[DONE]` + `finish_reason`；A：`message_start` / `content_block_*` / `message_delta`），Responses 事件名一个不漏；usage 对上样本 `expect`（A 侧还要把缓存读拆回净值）；上游那串 `encrypted_content` 一个字符都不许出现在客户端字节里；`custom_tool_call` 的自由文本入参到两边都是**解得开的 JSON**；非流式聚合；闸确实开着 |

整链的上游回话直接读 `testdata/golden/responses-stream-*/response.raw` 的真实字节——手搭的 SSE 只会长成我以为的样子，而这条链真正要扛的是上游实际发出来的形态（同 §9.3 对入站字节的做法，方向相反）。

**已知缺口，不装作没有**：

- ~~**跨协议的推理在这两条路上一律丢弃，CC / Anthropic 客户端看不到上游的推理过程。**~~ **已销账（#4，2026-08-17）**：三个出口的合成半边与两个缺失的解码半边一并落地，见 §9.6。**唯一还看不见推理的情形**是 CC→R / A→R 上上游自己不回摘要（R 出口按 v0.65 ⑥ 不写 `reasoning.summary`），那是口径定的，不是实现欠的。以下是销账前的原文。
  - **跨协议的推理在这两条路上一律丢弃，CC / Anthropic 客户端看不到上游的推理过程——这是欠的账，不是合规的取舍。** 单独点名且置顶，因为它是**用户可感知的退化**，而且性质比「已知缺口」更重一档。本条**订正 #80 初版的错误引证**：初版写的是「口径层 v0.10 明令跨协议只能丢、不得伪造」，而 v0.10 早在 2026-08-13 就被**口径层 v0.62 推翻**、又被 v0.65 加固——现行口径是**出向（上游 → 客户端）一律合成，四条路径对称**，且 v0.65 把丢弃的性质从「体验问题」升格成**错误**（「已发生的成本不得静默吞没」：上游已生成并计过费的思考内容，网关必须让它对用户可见）。
  - 更要紧的是**挡着这一格的东西已经没了**：口径层 §2.6 明写「Responses 出向那一格：口径已定「要合成」，样本已采（#93 兑现）……合并后这一格只剩实现」，而 `responses-stream-reasoning-turn1`（`reasoning_summary_part.added` → `text.delta` → `text.done` → `part.done` 整条生命周期）已随 #79 入库，#80 的解码用例正在消费它。所以这不是「等样本」，是**待实现**。
  - 初版那条技术立论也站不住：「摘要转成 `EvThinkingDelta` 等于拿摘要冒充推理正文」忽略了 canonical 的 `ThinkingChannel` 自 v0.29 就是 **body / summary / signature 三通道**，摘要本来就有自己的通道装，不必冒充正文。真正不能搬的只有 `encrypted_content`（上游侧密文，解不开也不该复用）。
  - 现状照旧丢弃，与另外几条路同步——合成是 wayfinder #87 / #93 那条独立 effort 的活，#80 不夹带。此处只把账记清楚：**欠着的是实现，不是口径**。思考量仍由 `usage.reasoning_tokens` 带走。请求侧反方向（canonical thinking → Responses 出口）同批欠着，现登记 `thinking` 后丢弃。
- **非流式 `DecodeFullBody` 没有真实样本背书。** 九份 Responses 转录**全是 `stream: true`**（Codex CLI 恒发流式）；golden 终帧里那个 `output` 数组**不能当线格真值用**——它是中转侧的降级重建，`responses-stream-tool-turn1` 里流上明明是 `custom_tool_call`，终帧却重建成了一个没有入参的 `function_call`。这条路径由手搭 fixture 覆盖，形状照协议文档 + 与流式那半边的对称性写。性质同 §9.1~§9.3 各自那条。
- **`custom_tool_call` 的入参攒满再放，丢掉了上游的分片节奏。** 全仓的响应侧不变式是「工具入参一律是 JSON」（两个既有解码侧都硬写 `ArgsIsJSON: true`，两个入口编码侧都不看这一位、把分片原样透下去），而 JS 源码逐片放出去就是往 CC / Anthropic 客户端灌一串解不动的东西；JSON 字符串的转义又没法按分片增量生成。代价只是节奏。线上这两条路的客户端都只声明 function 工具，上游回 `custom_tool_call` 实际不会发生——这一支存在是为了形态完备，不是热路径。
- **采样参数（`temperature` / `top_p` / `stop`）一律不发。** 照 sub2api 两个 `to_responses` 转换器的先例：gpt-5.x 推理模型收到采样参数就 400，而 `stop` 更是 Responses 请求里根本没有的参数（其 `ResponsesRequest` 无此字段，实现前已核）。**不发不等于没发生**——登记 `sampling_params` 由 relay 打警告，客户端设的采样行为确实没到上游。
- `response.incomplete` / `response.failed` 与裸 `error` 帧在九份转录里一次都没出现（全是 `status: "completed"`、`incomplete_details: null`），照协议文档实现，用例手搭。同 §9.2 里 `error` 帧那条的性质。
- 线级**两个并行工具项**逼不出真上游：`responses-stream-parallel-turn1` 的并行发生在入参那段 JS 的 `Promise.all` 里，output 层仍只有一个 item。该形态由 `in-responses-parallel-turn2` 那个 stub 样本覆盖（§9 场景表已记）。
- 全链只对着**假** Responses 上游跑过。真机验收（Claude Code → A→R、pi → CC→R，打真实 Responses 上游）挂 [#98](https://github.com/SimonGino/portage-legacy/issues/98) 作 PO 手动清单，不作合并闸——**六条路的真机验收已全部收拢到那一票**（PO 2026-08-14 裁定，原先散在 #9 / #11 / #12 / #25 / #80 五处，其中三处的票已经关了）。harness 分工按口径层 v0.20 的必过档，**不是 Codex**——它走 Responses 入口，配 Responses 渠道即同协议透传，一格转换都不碰。PO 2026-08-14 裁定 #80 直接关闭、验收另票。

### 9.5 图片跨协议转换的用例分工与已知缺口（#1，2026-08-17）

图片这件事横跨两个半边——解码在入口协议、编码在渠道协议，**分开看两边都「对」，错的是中间那一环**。所以这一格的钉法与 v0.34 ① 给 developer 归一立的规矩同线：单测钉形状，整链钉「图真的到了上游」。

| 层 | 位置 | 输入 | 钉什么 |
|---|---|---|---|
| 载体 | `protocol/image.go` | —— | data URI 拆/拼、空 base64 判定、`media_type` 空值兜底 `image/png`、`Carrier()` 判 data/url/file |
| 解码 ×3 | `anthropic/decode_test.go`、`openaicc/decode_request_test.go`、`openairesponses/decode_test.go` | 手搭 | 三种 `source` 形态各落对字段；`type=file` 是判别式（带残留 data 也只认 FileID）；空载荷整块跳过；`input_audio` 等认不得的 part 仍原样进 Extras |
| 编码 ×3 | `anthropic/encode_request_cc_test.go`、`openaicc/encode_test.go`、`openairesponses/encode_request_test.go` | 手搭 canonical | base64 ↔ data URI 互转、`url` 形态原样转发、`file_id` 登记 `image_file_id` 后丢、纯文本消息**仍发字符串 content**、`tool_result` 带图时 CC/R 抬成后续 user 消息而 A 留在 `content` 数组里 |
| 整链 ×6 | `server/convert_image_test.go` | `fixtures/in-*-image` 发包（只换 model）+ 假上游 | 六个格子各验一次：base64 载荷与 `tiny.png` **逐字节一致**（比对的是原图，不是样本里那串——后者只证明「串搬过去了」）、同条消息的正文不被图挤掉、抬出来的 user 消息**排在**工具结果之后 |

**样本放 `fixtures/` 不放 `golden/`**（PO 2026-08-17 裁）。四份都是构造样本——真实 harness 至今没发过带图的轮次，实采样本的 `content` 全是字符串。此前它们放在 `golden/` 且 meta 写 `verified: true`，既违反 `fixtures/README.md` 那句「构造样本不得改名搬进 golden/」，也把 `verified` 从「人核过的转录」稀释成「人核过」。代价是 `TestCanonicalModelCoversInboundSamples` 扫两个根、各走各的闸；**这是刻意的**：那张表要证的是「canonical 装不装得下」，而能证明形状的样本不一定是转录，把构造样本挡在表外，图片那几行会当场被判成「表随样本烂掉」的陈旧项。

**抬图的位置：一条 user 消息挂多个 `tool_result` 时，抬出来的那条排在所有工具结果之后**（v0.79 修）。初版把它写在遍历 `tool_result` 的循环里，逐个结果抬一条——头一个结果带图时，出站字节是 `[tool, user(图), tool]`。**这在 CC 那边是硬错**：`role=tool` 必须紧跟带 `tool_calls` 的 assistant，中间插一条 user 就是一个必被上游拒的请求；Responses 那边则是凭空在调用与结果之间多出一轮对话。而多 `tool_result` 挤在同一条 user 消息里正是 Anthropic 并行工具轮的常态形态（`in-anthropic-parallel-*` 实采可证），不是边角。用例 `TestEncodeRequestLiftsImagesAfterAllToolMessages` / `TestEncodeRequestLiftsImagesAfterAllToolOutputs` 钉的是**角色序列**，不只是「图在不在」——初版那两条整链用例只发了一个 `tool_result`，位置错了照样绿。

**`detail` 提升成 canonical 的一等字段**（v0.80 补，PO 2026-08-17 裁「补转换」）。`Image` 加 `Detail string`，两个入口读进来、两个出口写出去，Anthropic 出口登记 `image_detail`。三点值得记：

- **为什么非提升不可。** Responses 入口的 `detail` 此前落在 `Block.Extras` 里，看着像「留住了」，其实一样死——**Extras 永不外带**是三个出口一致的既定行为（只放行 `cache_control` / `metadata`）。所以「CC 丢、Responses 留」这个不一致是假象：两边都丢，只是丢的位置不同。
- **字段形状两边不对称，是这条最容易写反的地方。** CC 的 `detail` 在 `image_url` **对象内部**；Responses 的 `image_url` 本身就是字符串，`detail` 是它在 part 顶层的**同级兄弟**。抄错一边就是发出去一个上游读不到的键。
- **取值原样转发，`auto` 也照发。** 不设白名单、不把 `auto` 当成「等于没指定」抹掉——同口径层 v0.77 对 `MediaType` 的裁定：网关不替上游校验，不认就让它 400。往 Anthropic 去时只要 `detail` 非空就登记 `image_detail`；`file_id` 那一档**不重复登记**（图整个都没了，`image_file_id` 已经说明问题）。`DropImageDetail` 只在 `anthropic` 包里有，不像 `DropImageFileID` 那样三个 codec 各一份——CC 与 Responses 都原生支持这个字段，没有丢弃这回事。

**已知缺口，不装作没有**：

- **没有一份真实 harness 的带图转录。** 形状照三家官方文档写，证得了「我们对这个形状是对的」，证不了「客户端真的这么发」。等实采到带图的轮次，在 `golden/` 下新开目录走 `verified` 那道闸，本目录这份留着当形状回归。
- **`source.type=url` 的整链只在单测里走过。** 构造样本用的都是 base64——远程 URL 形态网关只做原样转发（口径层 v0.39：不代客户端下载），整链上验不出比单测更多的东西。
- **格式白名单不设，域外 mime 的 400 没有实测。** 口径层 v0.77 裁定 `MediaType` 原样转发、上游不认就让它 400；用例只钉「原样发出去」，不模拟上游拒收。
- **流式响应侧不涉及。** 三个协议的上游都不在响应里回图，图只走请求侧。

### 9.6 thinking 出向合成与请求侧 effort 直传的用例分工与已知缺口（#4，2026-08-17）

口径见口径层 v0.62（出向合成）与 v0.65（请求侧档位直传）。这一格销掉了 §9.2 与 §9.4 各自置顶的那条「用户可感知的退化」——上游已生成并计过费的推理内容，现在对客户端可见了。

**改的是两个半边，判定收在一处**。三个出口此前是整类 `EvThinkingDelta` 一刀切丢，现在丢弃条件下沉到通道判别，由 `protocol.OutboundThinkingText` 单点持有（流式与非流式那六个半边都调它）。落点是一张 **通道 × 出口** 表：

| canonical 通道 | → Anthropic | → CC | → Responses |
|---|---|---|---|
| `ThinkingBody` | `thinking` 块，**不带 `signature` 字段** | `delta.reasoning_content` / 非流式 `message.reasoning_content` | `reasoning_summary_*` 整套生命周期 |
| `ThinkingSummary` | 同上（R→A 唯一实例，§9.4 已明确不算「摘要冒充正文」） | 同上 | 同上 |
| `ThinkingSignature` | **丢** | **丢** | **丢** |

解码侧同批补齐两个此前不存在的半边：CC 的 `reasoning_content`（DeepSeek 起头的事实标准，流式与非流式同名同义，所以两条路共用 `streamState.body`）与 Responses 的 `reasoning_summary_text.delta`（外加 `reasoning_text.delta` 与非流式 `summary[]`）。**只认 `.delta`，不认 `.done`/`output_item.done` 里那份 `summary[]`**：同一段摘要在上游流里出现三遍，三处都收就发三遍。

Responses 出口的帧序照 `responses-stream-reasoning-turn1` 与 opencodex `src/bridge.ts` 的合成路径：`output_item.added` → **`reasoning_summary_part.added`** → `text.delta*` → `text.done` → `part.done` → `output_item.done`。`part.added` 不是可省的装饰——Codex 侧靠它把 `summary[0]` 那个槽位立起来，缺了它后面的 delta 索引到一个不存在的 part（与正文那侧 `content_part.added` 同一个坑，sub2api 在正文侧踩过）。**item 上不写 `encrypted_content`**：我们手里没有上游的封装，写空串等于声称「有一个空封装」；opencodex 同样只在真拿到封装时才写这一键。

请求侧 `Effort` 提成 canonical 一等字段（理由与 `Image.Detail` 同：`Extras` 永不外带，留在那里等于每个出口都拿不到它）。三个入口读、三个出口写，**六条路全开**；`LiftNestedEffort` 提完即从 `Extras` 里删（`output_config` / `reasoning` 被掏空则连键一起删）——不删的话出口会为一个**其实转发出去了**的键登记一次 `vendor_request`，账本就说了假话。新增登记档 `thinking_param`，与 `DropThinking`（内容块）和 `DropVendorRequest`（其余顶层字段）三分：住户是思考开关本身、`reasoning.summary`、各家数值预算，以及**档位解不出来时（非字符串 / 空串）整个留下的载体** `output_config` / `reasoning`。这张键表住在 `protocol.IsThinkingParamKey`、**只有一份**：三个出口的分类规则必须字字一样，而镜像三份的代价在本批评审里已经付过一次——`output_config` 三份表里一份都没写，于是解不出档位的 effort 在三个出口全记成了 `vendor_request`，正好是这一档要防的那件事；补它要改三处，漏一处就还是同一个洞，收成一份之后新键不可能半落地。`DropXxx` 常量仍各包一份（那是**名字**，不是**规则**）。

| 层 | 位置 | 输入 | 钉什么 |
|---|---|---|---|
| 判定 | `protocol/event.go` | —— | `OutboundThinkingText`：签名通道与空文本都不写；出口侧不设开关（`thinking.display` / `reasoning.summary` 是请求侧参数，登记后即丢） |
| 解码 ×2 | `openaicc/thinking_test.go`、`openairesponses/decode_response_test.go` | 手搭帧 + `responses-stream-reasoning-turn1` | `reasoning_content` 落 `ThinkingBody`、`reasoning_summary_*` 落 `ThinkingSummary` 且**恰好一条**、`reasoning_text` 落 `ThinkingBody`、密文一个字节不进事件流 |
| 编码 ×3 | `anthropic/encode_test.go`、`openaicc/thinking_test.go`、`openairesponses/thinking_encode_test.go` | 同一份事件序（正文 + 摘要 + 签名 + 空串 + 回答） | **同一份断言跑两遍**（流式 / 非流式，兑现 v0.62 ①）：正文与摘要都进、空串不进、签名与 `signature` 键都不出现；A 侧另钉块边界（thinking 先收口再开正文）、R 侧另钉整条帧序与 `part.added` |
| 档位 | `protocol/effort_test.go` | 三协议请求体 × 三出口 | 六条路各送到、域外值（`xhigh`/`max`/生造词）原样不钳、没发就一个字节不加、数值预算与思考开关登记 `thinking_param` 后丢且**不**归进 `vendor_request`、A 出口只写 `output_config.effort` 且出向体里**没有 `thinking` 键**；`TestUnliftableEffortStillRegistersAsThinkingParam` 单钉「档位解不出来时载体整个留下」这个形态在三个出口都记 `thinking_param`（评审揪出的真洞的回归位） |
| 回带 | `anthropic/encode_request_cc_test.go` | canonical 带 `BlockThinking` | 丢，**但登记 `DropThinking`**；正文与签名都不外带 |
| 真机字节 | `protocol/thinking_golden_test.go` | 两份新入库的 `anthropic-{stream-thinking,thinking-high}` | 签名整串与前 32 字符都不出现在三个出口的输出里；正文恒空 → 谁都不许凭空开出推理块 |
| 整链 ×5 | `server/convert_a2r_test.go`、`convert_r2a_test.go`、`convert_cc2a_test.go`、`convert_cc2r_test.go`、`convert_responses_test.go` | 真实入站样本 + `responses-stream-reasoning-turn1` / `cc-stream-reasoning-text` / 手搭上游帧 | A→R 的档位落 `reasoning.effort` 而 `thinking` 仍在禁发表；A 侧摘要变 thinking 块且密文不漏、thinking 排在正文之前；R 侧摘要走 `reasoning_summary` 线而不混进 `output_text`；CC 侧走 `reasoning_content` 而不混进 `content`。**CC↔R 那两格也各钉一条**（v0.73 ② 补的格，评审指出此前只有 codec 层覆盖）：CC→R 用真实 Responses 转录验「摘要下来、密文与 Responses 线格式都不下来」，R→CC 用真实 CC 转录验「整条 `reasoning_summary_*` 帧序齐、`reasoning_content` 这个 CC 字段名不漏给 Codex」 |

**已知缺口，不装作没有**：

- **A 源侧带正文的真机转录仍然没有。** 新入库那两份（`anthropic-stream-thinking` / `anthropic-thinking-high`，2026-08-15 采）是 `output_config.effort=high` 单发触发的思考，**`thinking` 正文整段为空、真内容只有那串 1 KB 签名**。它们钉住的是「effort-only → 空正文 + 有签名」这个形态与「签名不许外漏」；「推理正文到得了客户端」那一半仍由手写帧覆盖。成因两种可能（上游默认不回正文 / 中转剥掉了正文），从现有字节分不出来——要分得开得再采一次带 `thinking.display` 的。低档（`low`）那一份也没采到。
- **R 出口不写 `reasoning.summary`。** 「思考多少」与「展不展示」是正交两维（v0.65 ⑥：网关不替客户端开思考），客户端只说了前者。后果是 CC→R / A→R 上上游可能因此不回摘要，那两条路的推理仍然看不见——**不是这一批的实现漏了，是口径这么定的**。
- **上游报的 `output_tokens_details.thinking_tokens` 还没进账本。** 那两份新样本的字节里明明有（249 / 310），而 Anthropic 侧的 Tap 不解这一格，所以样本 `expect` 记的是 `0/false`。这是口径层 v0.66（思考 token 账本）那批的活，落地时两份 `expect` 要跟着改。
- **压缩 turn 里推理照旧一个帧都不发。** `encode.go` 的合成分支在主 switch 之前就把 `EvThinkingDelta` 吞了——一个推理 item 混进 output，「恰好一个 compaction item」就不成立（#74）。用例 `TestCompactionSwallowsThinking` 钉住这条。
- **`response.reasoning_text.delta`（推理正文流）没有样本。** 九份 Responses 转录里一次都没出现（线上两条 R 上游只回摘要），那一支只保形态完备，覆盖靠构造帧。评审判这一支是 #4 票面之外的多做，**PO 于 2026-08-17 裁定保留**：它与 `reasoning_summary_text.delta` 是同一入口的姊妹事件，不认它就是遇上时静默丢内容。
- **真机验收挂 [#3](https://github.com/SimonGino/portage/issues/3)。** 按票裁定，本批不把真机跑通当合并闸。

## 10. harness 验收清单

必过档挡里程碑验收；顺带档不挡、坏了再修（#7 已决）。

| harness | 档位 | 协议 | 必过项 |
|---|---|---|---|
| Claude Code | 必过 | Anthropic | `/v1/messages` 流式工具调用整轮跑通；cache_control 透传（Anthropic 出口）；`count_tokens` 不阻塞启动 |
| Codex CLI | 必过 | Responses | Responses 模式整轮工具调用与透传（P0）。**CC 模式已不可用**——0.144.1 移除 `wire_api = "chat"`（openai/codex#7782） |
| pi | 必过 | CC | `/v1/chat/completions` 整轮工具调用、streaming usage。用 `PI_CODING_AGENT_DIR` 指向验收专用配置目录，避免动开发者全局 `~/.pi` |
| OpenCode | 顺带 | CC | `/v1/models` 列表 + 工具调用 |
| 全部 | — | 429 原样透传不被网关吞掉（M0 已验：状态码/头部/body 逐字节，见 #6）。**「harness 自身重试逻辑生效」这条已作废**——Codex 0.144.1 对 429 不重试，改由网关侧同候选退避重试兜底（v0.19，M2 验收） |

## 11. 里程碑

与口径层统一为 M0~M3（C2 收敛后统一编号）：

| 里程碑 | 内容 | 粗估 |
|---|---|---|
| M0 透传骨架 | 骨架 + 三协议原始字节透传 + SSE + Tap usage 提取（细则见 §6.1）；渠道/接入点 SQL 手工建；golden 样本必抓子集（§9）；对 Anthropic 官方跑通 Claude Code、对百炼/OpenAI 官方跑通 CC 透传。规格见 Issue [#1](https://github.com/SimonGino/portage-legacy/issues/1) | 1~2 个周末 |
| M1 Key + 日志 | key 鉴权中间件 + key CRUD（SQL 手工）+ call_logs 落库；上游错误按入口协议原生回错 + 错误注入打磨；harness 透传实机验收 | 1 个周末 |
| M2 协议转换（P1-①~④ 按序） | ① A→CC、R→CC（含 Responses 无状态化）→ ② R→A → ③ CC→A、CC→R → ④ A→R 与横切增强；每批 golden 全绿 + 真实 harness 验收。成本锚点：sub2api `apicompat/` 六方向全量 ≈ 7k 行实现 + 9k 行测试，测试为实现 1.3 倍。**另含同候选退避重试**（v0.19 从 M4 提前，见 §6；不依赖多候选，临时闸不放开） | ① ≥2~3 个周末（主工作量在 tool call 增量重组），后续批次随复盘排期 |
| M3 管理端 + 部署 | React 管理端：渠道（模型纳管、凭证池）/ 接入点（候选+权重）/ key / 用量查询，embed 单二进制（细则见 §8.1、§11.2）；**另含凭证池聚合与 key 层内环**（口径层 v0.38 从 M4 前移：凭证逐条 CRUD、多凭证临时闸放开、401 摘除与人工恢复、按凭证归因的日志列与用量视图、逐把凭证探测）；公网部署（nginx TLS 反代见 §11.3 + 全局限流）。全局限流已落地（§7.2）。**反代配置样例已用桩上游实测四条行为（§11.3），但未接真网关/harness** | 待估 |
| M4 分流与转移 | 多候选加权随机分流 + 候选间故障转移（C4）；语义均已决，纳管成熟后实现，管理端配权重实测验收。**渠道凭证池聚合与 key 层内环（v0.11）已于口径层 v0.38 提前到 M3**、**同候选退避重试已于 v0.19 提前到 M2**，均不在本里程碑 | 待估 |

### 11.1 容器打包（2026-08-08）

口径层 §2.8 的部署形态是「构建产物 embed 进单二进制」，容器只是**这个二进制的一种分发方式**，不改口径：镜像里就是那一个二进制加一份配置，没有另起一套运行时。加它的动机是把网关搬到另一台机器上试跑，不必在那台机器上装 Go 工具链。

- `Dockerfile`：多阶段，`CGO_ENABLED=0` 静态编译 → `scratch`。**能用 scratch 的前提是 SQLite 走 `modernc.org/sqlite`（纯 Go）**，换成 `mattn/go-sqlite3` 就得改成 alpine + libc。镜像 18 MB。
- 三个实测踩到的坑，都写进了各自文件的注释：
  - **根证书**：`scratch` 里没有，上游全是 HTTPS，缺了的症状是每个请求 502，看不出是证书问题。从 build 阶段拷 `ca-certificates.crt`。
  - **`/data` 属主**：Docker 建命名卷时照搬镜像里同路径的属主。镜像里不预建 `/data`，卷就归 root，而进程以 65532 跑，启动即 `apply schema: unable to open database file (14)`——看着像 SQLite 坏了，其实是权限。解法是在镜像里预建一个属主正确的空 `/data`。
  - **`listen` 必须是 `0.0.0.0`**：宿主上的默认 `127.0.0.1` 在容器里只有容器自己看得见，端口映射永远连不上。边界因此从「进程绑哪个地址」挪到「端口发布给谁」——compose 里默认 `127.0.0.1:8317:8317`，改成 `8317:8317` 就是整个局域网，那时只有 key 鉴权挡着，TLS 与全局限流都还在 M3。
- 灌配置在**宿主侧**做：scratch 里既没有 shell 也没有 sqlite3。`deploy/docker-compose.yml` 顶部写了对着卷跑 sqlite3 容器的命令，`--user 65532:65532` 不能省——身份不对只能只读，报的是 `attempt to write a readonly database`。
- 健康检查刻意留空：为探活往镜像里塞一个 shell 或 curl，等于为一件外部就能做的事把攻击面加回来。

**M3 更新**：镜像多了一层 `node:22-slim` 前端构建，Go 那层改用 `-tags webui`；灌配置不再需要 sqlite3 容器，起来直接开 `/admin` 配（命令行那条路留着没删）。管理密码走 `PORTAGE_ADMIN_PASSWORD` 环境变量，见 §7 与口径层 v0.28。镜像 25 MB。

### 11.2 前端 embed 策略（M3）

- **build tag 二选一**：`internal/webui/embed.go`（`//go:build webui` + `//go:embed all:dist`）与 `stub.go`（`//go:build !webui`，返回「没有」）。不带 tag 的构建照样能过 `go build ./...`——CI 没有 Node，本地首次 clone 也没跑过 `npm build`，而 embed 失败的报错是「pattern dist: no matching files」，看不出跟前端有关。不带前端的二进制访问 `/admin` 会看到一页说明，转发不受影响。
- **产物落 `internal/webui/dist`，不落 `web/dist`**：`//go:embed` 只能读自己包目录下的文件。选 `internal/webui/` 而不是把 Go 文件挪进 `web/`，是为了让 Go 工具链永远不用走 `node_modules`。`all:` 前缀不能省，否则 Vite 的点开头目录会被静默跳过。
- **`base: '/admin/'`（vite.config.ts）+ `basename="/admin"`（Router）**：默认 base 会让 index.html 去请求 `/assets/…`，而静态文件只在 `/admin` 下发——**这个故障只在 embed 后出现，`npm run dev` 一切正常**。`internal/server/webui_test.go`（`//go:build webui`）就是这条的哨兵：断言 index.html 引用的资源全在 `/admin/` 下且能取到、`.js` 的 Content-Type 是 `text/javascript`。
- **SPA 走 `r.NoRoute` 而不是 `r.Static`**：深链接（`/admin/keys` 直接刷新）必须回同一份 index.html，而 gin 不允许 `/admin/*filepath` 与已注册的 `/admin/api/…` 并存——**注册时就 panic**，不是运行期 404。NoRoute 里三路分流：非 `/admin` → 普通 404；`/admin/api/…` 未知 → JSON 404（回 HTML 会让前端在 `JSON.parse` 上炸，报的错跟真正原因毫无关系）；其余 → SPA。
- **Content-Type 自己判，不用 `mime.TypeByExtension`**：后者读 `/etc/mime.types`，同一份二进制在两台机器上可能给出不同结果，`.js` 被判成 `text/plain` 时浏览器直接拒绝执行模块。`http.ServeContent` 只在头里没有 Content-Type 时才去猜，所以要先写好再调它。
- index.html 发 `no-cache`，带 hash 的资源发 `immutable`：反过来的话，改完前端浏览器还拿着旧 index 去引用已经不存在的文件名，白屏。

### 11.3 反向代理（口径层 v0.29 定 nginx 为主、Caddy 备用）

样例：`deploy/nginx.conf.example`，逐条注释写的是「漏了会看到什么现象」。

**选型的技术账**（口径层裁的是运维现实——机器上已有 nginx、443 只能有一个主人、既有的同套配置习惯——不是技术优势；这里如实记下代价，免得以后重新去翻文档）：

| | Caddy | nginx |
|---|---|---|
| SSE 缓冲 | `Content-Type: text/event-stream` 或 `Content-Length` 未知时**自动立即 flush**，`flush_interval` 被忽略 | 默认 `proxy_buffering on`；单独不致命，但一旦父配置开了 gzip 就整条流攒住（实测见下） |
| 长流空档 | 无对应默认掐断 | 默认 `proxy_read_timeout 60s` |
| 证书 | 内建 ACME，自动续期 | certbot 另配，多一条要维护的续期链路 |

**实测记录（nginx 1.31.3 容器 + 桩上游，2026-08-08）**。桩每秒推一条 SSE、共 5 条；同一份桩，只换 nginx 的配置：

| 配置 | 首字节 | 结论 |
|---|---|---|
| 默认（`proxy_buffering on`）+ `gzip_proxied any` | **5.02s** | 攒到整条流结束才吐第一个字节 |
| 只关 `proxy_buffering`，gzip 仍开 | 0.002s | 恢复逐条 |
| 只关 `gzip`，buffering 仍默认 on | 0.003s | 恢复逐条 |
| 纯 `proxy_pass`，没有 gzip | 0.003s | 逐条 |
| 样例这份（两个都关） | 0.035s | 逐条 |

**结论修正了一条常见说法**：`proxy_buffering on` 单独并不会攒住 SSE——小事件逐条转发，nginx 收一块发一块。真正攒住的是 **buffering 与 gzip 同时开**，任意关掉一个都恢复。样例里两个都关是冗余的，冗余的理由是那行 gzip 常常写在父配置里、不在这份文件里，改不改得动不由你说了算。（严格说父配置还得让 `gzip_types` 覆盖 `text/event-stream` 才压得到——默认只有 `text/html`。但这属于「别人怎么配」，不是能依赖的保护。上表 C 组已证 location 级 `gzip off` 压得住父配置。）

**读超时那条则完全成立**：桩把两条事件的间隔拉到 70s，默认 `proxy_read_timeout 60s` 的 nginx **在第 60 秒把流掐了**，客户端只拿到第一条、然后流「正常结束」——没有错误码、没有异常断连。样例的 600s 拿到了第二条。这是四条里最难查的一种：模型思考或工具调用的空档超过 60s 就会踩到，而现象是「回答说了一半就没了」。

`client_max_body_size` 同样实测确认：2MB 的 POST，默认 1m 的 nginx 回 **413**，样例的 64m 回 200。**请求根本到不了网关**，日志里查不到任何痕迹。

其余一条不是坑而是版本兼容：`proxy_http_version 1.1` + `proxy_set_header Connection ""`——nginx 1.29.7 起前者默认已是 1.1，老版本默认 1.0，那种版本下 chunked 会被降级处理。另外独立的 `http2 on;` 指令要 1.25.1+，老版本得写 `listen 443 ssl http2;`。

**这次验证的边界**：证的是这份 nginx 配置对 SSE 的行为，用的是桩上游，没有接真网关、没有跑 harness。真机上线仍要按 §10 的 harness 清单再走一遍。

**只放行 `/v1`（+ 可选 `/healthz`）**，兑现口径层 §2.7「反代只放行转发面」：`/admin` 认的是 cookie 会话，公网上多一个可爆破的登录页没必要，要用走内网直连或 SSH 端口转发。样例末尾的 `location / { return 404; }` 是兜底，防以后加 location 时漏掉。

**全局限流不在这一层**：口径层 §2.7 已裁定全局令牌桶（10 QPS / 突发 20，v0.81 起两只——生成面一只、`count_tokens` 一只）做在网关自己里，nginx 的 `limit_req` 会变成重复一层。实现见 §7.2。

**网关自己会发 `X-Accel-Buffering: no`**（口径层 v0.30，见 §7.3）：样例里的 `proxy_buffering off` 因此是双保险，真正的用处是覆盖那些不由我们维护的 nginx。

## 12. 参考对照

仓库索引与各仓库定位、许可证注意事项见本仓库 `CLAUDE.md`「参考仓库」一节，**可参考的仓库集合以那一节为准**（下表只是逐文件路径对照，不必逐仓库补齐）；下表 new-api 路径均为 `~/Code/GitHub/new-api`（上游）相对路径，已逐一核实存在。

| 参考仓库 / 位置 | 参考什么 |
|---|---|
| `new-api/relaykit/dto/`（claude.go、openai_request.go） | canonical 请求模型的现实形态（注意：上游在 `relaykit/dto/`，非 fork 的 `dto/`） |
| `new-api/relay/channel/claude/adaptor.go`、`relay/claude_handler.go` | Claude 编解码 |
| `new-api/relay/chat_completions_via_responses.go`、`relay/responses_handler.go` | CC ↔ Responses 互转 |
| `new-api/relay/helper/`、`relay/common/` | SSE 工具函数 |
| `new-api/relay/relay_adaptor.go`、`model/channel*.go` | failover / 渠道选择思路（不取其复杂度）；`model/channel.go` 多 key 聚合语义（我们改为建表实现） |
| `new-api/relay/channel/vertex/service_account.go` | Vertex service account → access token 刷新（credential_type=service_account 参考） |
| `new-api/model/log.go` | 日志落库 |
| `litellm/litellm/llms/*/chat/`（各 provider transformation） | P1 协议转换字段映射的交叉对照（thinking、tool calling、usage 语义） |
| `sub2api/backend/internal/pkg/apicompat/` | **P1 转换的 Go 实现首要参考**：自包含转换库（A↔CC、Responses↔CC/A bridge、Responses SSE 事件线格式，含 Codex 事件流测试）；注意 LGPL-3.0，参考思路可、整包复制需评估义务 |
| `sub2api/backend/` 其余 | Anthropic 协议侧处理与 Go 工程结构参考（订阅池/计费不抄） |
| `opencodex/src/responses/compaction.ts` | Codex 自动压缩（remote compaction v2）：`compaction_trigger` 判定、summarizer 改写、`ocx1:` 信封编解码——§7.6/§7.7 本地合成的同构参考（MIT，PO 2026-08-13 裁定照抄） |
| `opencodex/src/bridge.ts` | Responses SSE 事件线合成：adapter 事件 → `output_item.added/done` 等事件序列与收尾判据；压缩 turn 攒正文、收尾时合成恰好一个 compaction item |
| `opencodex/src/responses/reasoning-envelope.ts` | reasoning 回放（跨协议）：Anthropic thinking signature 藏进 `encrypted_content` 的 `ocxr1:` 信封往返；文件头注释是「signature 缺失回放即 400」的一手证词 |
| `opencodex/src/responses/reasoning-replay-cache.ts` | reasoning 回放（工具轮）：`reasoning_content` 的有状态回放兜底（按会话隔离、TTL 有界）——本项目无状态路线不抄，作成本对照 |
| `CLIProxyAPI/internal/translator/openai/claude/` | thinking 出向合成：`reasoning_content` → `thinking` 块（不带 signature）；请求侧回带处置同目录 |
| `CLIProxyAPI/internal/signature/provider_compatibility.go` | 回带侧按 signature provenance 的逐 provider 决策表（本项目取「一律丢」，此表作对照，见 #91） |
| `CLIProxyAPI/internal/thinking/` | 思考参数 canonical 层：`ThinkingConfig{Mode,Budget,Level}` 把「思考多少」与「展示与否」拆成正交两维 |

## 附录：开放问题记录

- ~~上游清单与各渠道 key 数~~（#1 已决 v0.17：Anthropic/OpenAI/Vertex/百炼四渠道 + 渠道多凭证聚合，见 §0/§7）
- ~~真实接入点与候选清单~~（#2 已决：运营数据不冻结，M0 验收集见 §0）
- ~~harness 清单增删~~（#7 已决：必过档 Claude Code、Codex CLI；顺带档 pi、OpenCode）
- ~~转换方向优先级~~（#9 已决：协议转换属 v1、透传先行、按口径层 §2.1 分批，设计态考虑落 §2/§4/§5/§6）
- ~~**转换方向集合**~~（已决：口径层 v0.3 裁定为三协议全互转，6 转换 + 3 透传，全集为准；分期之争已随 C1 收敛：属 v1 承诺、节奏透传先行）
- 公网暴露与否 → 决定 TLS / 限流 / listen 地址默认值
- ~~thinking 同协议透传是否进 P0~~（已随 P0 原始字节透传自动解决；跨协议丢弃仍是 P1 已决策略）
