# 个人 AI 模型网关 MVP 设计草案

> 状态：草案 v0.46
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
| Anthropic Messages | **P0 透传** | P1-① 转换 ✅#11 | P1-④ 转换 |
| Chat Completions | P1-③ 转换 ✅#9 | **P0 透传** | P1-③ 转换 |
| Responses | P1-② 转换 ✅#25 | P1-① 转换 ✅#12 | **P0 透传** |

✅ = 已落地并放开临时闸（`server/convert.go` 的 `conversionOpen`）。其余格仍回 501。

各格所需的 codec 半边（每个 codec 分「入口半边」`DecodeRequest`+`EncodeStream`+`EncodeFullBody` 与「出口半边」`EncodeRequest`+`DecodeStream`+`DecodeFullBody`）。**已实现：anthropic 两半齐全、openaicc 两半齐全（入口半边 #9）、openairesponses 入口半边。** 剩下两格所差同一个半边：

- **A→R**（④）、**CC→R**（③下半）：都只差 `openairesponses` 出口半边。它做完就是 9 格全开。

排序曾有争议：按**边际成本**④ 比 ③ 便宜，与口径层 §2.1 的排序（③ 先于 ④）相反。**PO 裁定不调序**（v0.35）：这两个半边同属 M2 收尾的一批，做完就是 9 格全开，边际成本的差别在「两个都要做」的前提下不成立；而 ③ 的输入契约（六份 `in-cc-*` 样本，#27/#28）刚落地，采集时带出的两条约束已进 canonical 覆盖表，趁热做省一次重新进入成本。#9 就此收敛，`openaicc` 入口半边已落地。

- 分批号 ①~④ 即口径层 §2.1 实现优先级：**①** A→CC、R→CC（主诉求：harness 挂第三方便宜模型）；**②** R→A（Codex 用 Claude）；**③** CC→A、CC→R；**④** A→R（允许滑到最后）。
- 首批特性集 = 纯文本 + tool calls（含并行调用）+ system prompt + 停止原因 + usage；图片、count_tokens 估算、thinking 精细策略等横切增强随 ③④ 批排期。
- Responses 无状态化（`previous_response_id` 处理）随 ① 的 R→CC 一并落地。**（#12 已落地）** 实现是最简形态：`DecodeRequest` 直接丢掉 `previous_response_id`，连 `Extras` 都不进——留着它等于把一个**上一个上游**才认得的句柄带在身上，任何编码侧顺手带出去，上游要么报找不到、要么接到别人的会话上。上下文靠 harness 全量携带的 `input` 重建，与 sub2api 的 `RemovePreviousResponseIDFromBody` 同路子。

**「设计态考虑」落为三条硬约束：**
1. **管线 seam 现在就定型**（§6）：同协议走原始字节透传，异协议走 canonical 编解码；P1 只是填充后一路，seam 位置不变。
2. **canonical 事件模型与 codec 接口现在就按定稿标准写**（§4/§5），P1 开工不重设计。
3. **golden 样本 M1 就采集**（§9），刻意选「同语义、双协议」场景，天然构成 P1 转换的黄金输入对。
- 有损转换策略（已决）：
  - thinking / reasoning：跨协议**丢弃 + 记日志警告**，不做伪映射；同协议透传保留。
  - cache_control：仅「出口为 Anthropic」时保留；转往其他协议时静默剥离。
  - temperature：Anthropic 区间 0~1，OpenAI 0~2，转换时 clamp。
  - count_tokens：P0 上游非 Anthropic 时返回 501 风格错误；P1 做字符估算。

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
    Extras      map[string]any
}

type Message struct {
    Role    Role      // system / user / assistant / tool
    Content []Block   // 纯字符串 content 退化为单个 text 块
    Extras  map[string]any
}

type Block struct {
    Kind       BlockKind    // text / thinking / tool_use / tool_result / image(M2 未实现)
    Text       string
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
- `EvUsage` 一条流里可出现多次：Anthropic 在 `message_start` 给 `input_tokens`、在 `message_delta` 给 `output_tokens`。语义是累计快照，消费方**不做加法**。
- `Usage` 与 `Tap.Summary` 一致，**保留各协议原始语义不归一**（Anthropic 的 `input_tokens` 不含缓存命中，OpenAI 的 `prompt_tokens` 含）。
- `ThinkingChannel` 要显式判别式而非塞 `Extras`：Responses 同时有 `response.reasoning_text.delta` 与 `response.reasoning_summary_text.delta` 两条流，语义不同（推理正文 vs 面向展示的摘要），codec 必须分支。藏进 map 等于每个 codec 各写一次魔法键查找。

事实来源：Anthropic 侧取自 `testdata/golden/raw/anthropic-*` 五份真实上游 SSE 转录；CC 侧取自 M0 语料 `testdata/golden/cc-stream-*`；**Responses 侧没有真实上游转录**（入站采集走的是 stub，stub 是道具不是样本），事件名以 `sub2api backend/internal/pkg/apicompat/` 为准，M2 拿到真实上游流后须复核。

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
| `thinking` | thinking 块正文与 `signature` | CC 的 assistant 消息没有推理块位置。回带上一轮 thinking 的客户端（Claude Code 开 extended thinking 时）会让上游丢失该轮推理上下文，表现为**质量下降而非报错** |
| `server_tool` | `Tool.Kind` 非空的服务端工具声明 | Claude Code 会声明 `advisor_*` 一类由 Anthropic 服务端执行的工具，第三方 CC 上游既不认也执行不了。声明整条剔除，客户端表现为该工具不可用 |
| `vendor_request` | 入口协议独有的顶层字段（`Request.Extras` 里除已知项外的其余） | 逐项枚举会随上游 beta 漂移，故按「不认识就丢并记名」处理。日志里带得出字段名，出问题时能定位 |

`tool_choice` 的两种非法组合（引用未声明的工具、有 `tool_choice` 无 `tools`）不算丢弃而算**规整**：严格中转的第三方上游会直接拒请求，encode 侧当场消掉。见 §5 坑清单「严格中转的请求校验」。

### 4.6 图片载荷的 canonical 形状（#33，2026-08-11 定，尚未实现）

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

**现状：四处卡点**（2026-08-11 通读三个 codec 得出，动手前照这张表逐个拆）：

| # | 位置 | 现状 |
|---|---|---|
| ① | `protocol/request.go:44` | `BlockImage` 是裸占位，`Block` 无任何图片字段 |
| ② | `anthropic/decode.go:146` | `image` 块 → `Kind="image"`，`source` 整块进 Extras（**字节在**） |
| ② | `openaicc/decode_request.go:244` | `image_url` part → `Kind="image_url"`，全字段进 Extras（**字节在**） |
| ② | `openairesponses/decode.go:240` | **所有 part 一律造成 `BlockText`**，不看 `type`——`input_image` 进来之后连「这原本是张图」都不知道了 |
| ③ | `openaicc/encode.go` `joinBlocks` | ~~只认 `BlockText` / `BlockThinking`，其余落空且不登记~~ **已止血**（#41，v0.46）：补了 `default` 登记 `DropVendorContent`；**块仍然丢**，只是这次出声 |
| ④ | `openairesponses/codec.go:45` | `EncodeRequest` 仍 `ErrNotImplemented` |

③ 曾是**当下就在发生的静默丢弃**：Anthropic 入口带图打到 CC 上游，图无声消失。#32 补的 `DropVendorContent` 只落在 `anthropic/encode_request.go:254` 那一侧，CC 出口这半边漏了——正是本项目判过「不行」的那种失败模式，在自己代码里。#41 单独补了这句登记，行为不变；真做转换时这一支会被图片那一路缩小到「真的没对等形态的那些」。

② 的三行不一致也要一并抹平：三个入口对同一件事给出三种 `Kind`，其中 Responses 那行连判别式都丢了，是三者里最难补的。

**样本采集**：用一张真的极小图（几百字节真 PNG），base64 进 `request.json`。不用手写假串（sub2api 测试里全是 `"aGVsbG8="`、`"iVBOR"` 这类编不出图的串——它测的是字段搬运，够用；我们的 golden 库口径是真实字节存档，掺假串等于往事实里掺伪造，与「stub 是道具不进转录库」同一条线），也不截断存 hash（往返验不了，而往返正是图片这格唯一值得测的东西）。体积不是问题，`cc-*` 单个样本比它大得多。

## 5. 转换器（codec）接口

> **v0.26 修订（#11）、v0.29 更新（#25）。** 代码在 `internal/protocol/codec.go`。落地进度：`anthropic` 两半齐全（入口 #11 / 出口 #25），`openaicc` 只有出口半边，`openairesponses` 只有入口半边（#12）。

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
| Responses reasoning 的 `encrypted_content`（M0 实测） | Codex CLI 的 `/v1/responses` 请求会在 `input` 里回带上一轮的 reasoning item，其 `encrypted_content` 是**上游侧不透明密文**，只有原上游解得开。P0 透传无影响；**P1 一旦跨协议转换就必然作废**——转成 CC/Anthropic 时它无处安放，转回来也已换了上游。落到口径上：这就是「thinking 跨协议丢弃」的具体形态之一，转换路径不得伪造或复用该字段，只能丢，且丢了会让 Codex 失去上一轮的推理上下文（表现为质量下降而非报错）。M2 做 R→CC / R→A 时须有专门用例钉住「带 `encrypted_content` 的 input 不使转换报错」 |
| Responses 无状态化（P1-①，R 入口转换即需） | `previous_response_id` / store 语义需自行承接；参考 `sub2api backend/internal/pkg/apicompat/responses_namespace.go` |
| Responses SSE 线格式（#12 拿真实上游转录复核） | 事件名与 sub2api 一致，无出入。三条实测细节：① 正文 item 比工具 item **多一层 `content_part`**（`output_item.added → content_part.added → output_text.delta* → output_text.done → content_part.done → output_item.done`），工具 item 没有；② 每帧 data 里都带 `sequence_number`，**从 0 起全流连号**（102 帧无一例外），客户端拿它判丢帧；③ 流**不发 `data: [DONE]`**——那是 Chat Completions 的收尾，Responses 以 `response.completed` 为终点。截断另发 `response.incomplete`（`status: incomplete` + `incomplete_details.reason`），流内错误发 `response.failed`。转录在 `testdata/golden/raw/resp-{text,tool,parallel}`（未脱敏，未纳入 git；用例照它定形状后把期望写死在测试里，不回放文件——`raw/` 在 .gitignore 里，回放式用例在 CI 上会集体 skip 成假绿）|
| 解码侧的丢弃没有告警通道（#12 记账，待 PO 裁决） | 口径层 §2.6 要求跨协议丢弃须有日志警告，但现有的 `RequestEncodeReporter` 只挂在**编码**侧。`openairesponses.decodeInput` 遇到认不得的 input item 类型（如 `web_search_call`）是静默跳过：不报错、不进 Extras、不留日志。跳过本身是对的（decode 必须是全函数；同协议路径不进 codec，未知 item 在转换路径上的唯一去向就是被丢），缺的是那条日志。**建议先保持静默**——decode 层刻意不持有 logger，而对称补一个 `RequestDecodeReporter` 属于只有一个消费方的机械。跳过时不 flush 攒消息缓冲，否则未知 item 会把前后两条同侧 item 劈成两条同 role 消息，撞上严格 CC 上游的连发限制（用例：`TestDecodeRequestSkipsUnknownItemWithoutSplittingMessages`）|
| Anthropic 必填 max_tokens | OpenAI 可缺省；转 Anthropic 出口时必须填默认（配置项 `default_max_tokens`） |
| 角色交替约束 | Anthropic 要求 user/assistant 交替；OpenAI 允许多条连续同角色；转 Anthropic 前需合并相邻同角色消息 |
| assistant 空 content | 纯 tool_calls 的 assistant 消息 content 可能为 null，转 Anthropic 时空块要剔除 |
| tool_result id 对齐 | OpenAI `tool_call_id` ↔ Anthropic `tool_use_id`，互转时 id 原样携带 |
| streaming usage | OpenAI 需 `stream_options.include_usage` 才在流末尾给 usage；向 OpenAI 出口发流式请求时**强制注入该参数**，否则日志拿不到 token 数 |
| stop reason 映射 | `end_turn`↔`stop`、`tool_use`↔`tool_calls`/`function_call`、`max_tokens`↔`length` 查表，未知值统一 `stop` |

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

**响应头（上游 → 客户端）**：除 `Content-Length` 外原样回传（流式下无意义，非流式由 Go 按实际写入量重设），状态码原样。上游 `x-request-id` / `request-id` 既回传客户端也记日志——个人自用场景下能拿它去找上游对账，比藏起来有用。

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

> **配置校验规则（临时闸，随转换批次逐步放开）**：候选渠道协议与入口协议不同、且对应转换路径尚未实现时报错。这不是 v1 边界——全互转属 v1 承诺（口径层 C1 已收敛）。
>
> **校验时机拆两处**（v0.10）：接入点本身不绑定协议，入口协议要到请求时（由路径）才知道，因此「协议必须一致」无法在启动时判。
> - **启动加载时 + 管理端保存时**：每个未停用接入点有且仅有一个 weight>0 的候选；每个未停用渠道**至少有一份**未停用凭证（上限那半已于口径层 v0.38 随凭证池放开，M3 起临时闸只剩单候选那一半）；每个候选引用的纳管模型确实属于存在的渠道；**未停用接入点的 weight>0 候选必须真的可达——其渠道、纳管模型、凭证均未停用**（v0.15，判定条件逐条对齐 `Resolve` 的 JOIN）；**未停用渠道的 `base_url` 必须是带 host 的绝对 http/https 地址，且不带查询串与 fragment**（v0.20——schema 只要求非 NULL，而配置是手写 SQL 灌进来的，空串 / 漏 scheme 的裸域名 / `ftp://` 都存得进去，过得了校验却每次请求才在 `http.Client.Do` 里失败回 502；查询串与 fragment 更隐蔽，`buildURL` 是字符串拼接，`https://h/p?x=1` 接上 `/v1/messages` 后 Go 解出来是 `path=/p`、`query=x=1/v1/messages`——协议子路径被整个吞进查询串，请求永远打到 `/p`，启动、日志、响应三处都看不出异常）。违规即拒绝启动，报错须点名违规记录的 id/name。

> **这条错误信息不回显 `base_url` 本身**：`cmd/gateway` 会把 `Validate` 的错误直接落 stderr，而 `base_url` 可以带 userinfo（`https://user:pw@host`），回显等于把上游密码打进日志。按 CLAUDE.md「错误回显严禁泄露上游 key 与 base_url」，只报「哪里不对」加渠道 name/id，让运维自己查 `channels` 表——可诊断性不靠回显原值。
> - **请求时**：入口协议 ≠ 本次选定的出站协议、且该转换路径未放开 → 按入口协议原生格式回错，文案明确为「该转换路径尚未实现」。
>
> **v0.33 追加**：未停用渠道的 `protocols` 必须非空且逐项合法（`store.checkChannelFields` 调 `protocol.ParseSet`）。空集合选不出出站协议，每次请求才 500——同属 v0.21 通则要拦的形态。`count_tokens` 不需要额外的闸：`conversionOpen` 的判据是**端点**而非协议，渠道不说 `anthropic` 时它必然落进「转换路径尚未实现」回 501，不会被回退顺序送去 `/v1/chat/completions`。

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
  ttft_ms INTEGER,                     -- 首字节耗时（流式）
  queue_wait_ms INTEGER NOT NULL DEFAULT 0,  -- 渠道并发闸排队等待（口径层 v0.52）：没闸/没等为 0；配套 error 词表
                                             -- 加 queue_full / queue_timeout / queue_abandoned 三词。见 §7.5
  total_ms INTEGER NOT NULL,
  input_tokens INTEGER, output_tokens INTEGER,
  cache_read_tokens INTEGER, cache_write_tokens INTEGER,
  error TEXT,                          -- 网关自己的**固定词表**（可枚举、可 group by）
  error_detail TEXT                    -- 上游错误原文前 2KB（口径层 v0.53），只在失败时写，其余为 NULL。
                                       -- 与 error 不同步出现：上游透传 4xx 的 error 是空的（透传成功不算
                                       -- 网关侧错误，v0.28 纪律），detail 却有值——管理端「可展开」的判据
                                       -- 因此是 status >= 400。可空是为了分开「没存」与「上游回了 4xx 但
                                       -- 体是空的」（存空串），后者本身就是排障信息
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

其余 M1 细则（取 key 的两个头、401 走 `protocol.WriteError`、鉴权失败也落 `call_logs`、落库失败不得影响请求）见 Issue [#22](https://github.com/SimonGino/portage/issues/22)。

### 7.2 全局限流的实现口径（M3，兑现口径层 v0.15）

`internal/server/ratelimit.go`，`golang.org/x/time/rate` 的令牌桶。口径层只裁了语义（单桶、10 QPS / 突发 20、429 带 Retry-After、不分维度），以下是实现侧的决定：

- **一只桶，在 `New()` 里造、四条转发路由共用**。写成在 `rateLimit(ep)` 闭包里 new 的话，每个端点各得一只，全局 10 QPS 悄悄变成 40——而且从代码上看不出来。`TestRateLimitBucketIsSharedAcrossEndpoints` 是这条的哨兵。
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

## 8. 最小管理接口

- `GET /healthz`
- `GET /v1/models`：返回**全部可路由的模型名**（harness 启动时会拉），格式为 OpenAI 公开的 `{"object":"list","data":[{"id":…}]}`

> **两半、两者都可路由（口径层 v0.32）**：未停用的接入点名，加上可用纳管模型的限定名 `渠道名/纳管模型名`。实现是 `store.ListExposedModels` 里一条 `UNION ALL`，`ORDER BY direct, id` 把接入点排在前面，Go 侧按 id 先到先得去重——于是「接入点优先」在列表和 `store.Resolve` 里是同一条规则。
> 直连那半边只列**当下真能打通**的（渠道启用 + 模型启用 + 渠道有启用凭证）；接入点那半边不做这层过滤，它归启动闸管（v0.18/v0.21）。直连没有 `candidates` 行，启动闸看不见它，所以这道过滤只能长在列表查询里。
> 这张表唯一的契约是**列出来的都调得通**，因此它认的名字集合必须和 `store.Resolve` 逐字一致，改一边就得改另一边。

> **不迎合 harness 的私有目录格式（M0 验收实测，2026-08-06）**：Codex CLI 拉的其实是 OpenAI 的**私有**模型目录——`{fetched_at, etag, client_version, models:[{slug, supported_reasoning_levels, apply_patch_tool_type, …}]}`，与公开的 `/v1/models` 不是一个东西。拿不到时 Codex 打两条 warning（`Model metadata for X not found. Defaulting to fallback metadata`、`service tier priority is not advertised…`）后**照常工作**，整轮工具调用不受影响。故本项目**不实现该私有格式**：它无公开契约、字段随 Codex 版本漂移，为它建一张模型能力表要长期跟着上游跑，而收益只是消掉两条 warning。降级路径已实测可用，就停在降级上。
- Anthropic 出口/入口的 `count_tokens`：P0 仅在上游为 Anthropic 时透传，否则 501

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
| GET | `/admin/api/logs?limit=&before=&model=&key=&only=bad` | 近期流水，limit 上限 500。筛选**全在后端**（v0.53）：前端在已拉回的一页里过滤，筛出的是「这一页里的失败」。翻页用 `before=<id>` 游标而非 `offset`——流水是时间序、新行插在头部，offset 翻到第二页就已错位（`offset` 参数仍在，无 `before` 时生效） |
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
- 全链只对着**假** CC 上游跑过。真机验收（Codex CLI → 网关 → 第三方 CC 上游整轮工具调用）是 #12 上的 PO 手动清单，与 #11 同性质，不作为合并闸。

### 9.2 R→A 的用例分工与已知缺口（#25，2026-08-08）

分工同 §9.1 的三层，只列与 R→CC 不同的部分：入口那半边（`openairesponses.DecodeRequest`）两条路共用，断言不重复；这边验的是 **anthropic 出口半边**。

| 层 | 位置 | 输入 | 钉什么 |
|---|---|---|---|
| 编码 | `anthropic/encode_request_test.go` | 手搭 canonical | Anthropic 协议自己的硬约束：`max_tokens` 必填与三级兜底、`RoleSystem` 上提到 `system`、相邻同角色合并、`input_schema` 必填与 custom 工具的合成、非 JSON 入参对称包装、孤儿 `tool_result` 丢弃并登记 |
| 解码 | `anthropic/decode_response_test.go` | 手抄 SSE（形状照 5 份真实转录） | 帧序、`ping` 与脏帧容忍、`usage` 两次是累计快照、thinking/signature 分走两条通道、只有工具块发 `EvToolCallEnd`、截断兜底收尾保留 `stop_reason`、停止原因两条映射互逆 |
| 整链 | `server/convert_r2a_test.go` | Codex 形态请求 + 假 Anthropic 上游 | 出站是合法 Messages（JS 入参包成对象、`input_schema` 合成出来、Responses 独有字段一个不漏）、下行是 Responses 线格式（Anthropic 事件名不漏、不发 `[DONE]`、拆包回裸 JS）、thinking 丢弃不炸且 signature 不漏进正文、非流式聚合、闸门开这一格 |

编码层与解码层**都不回放** `raw/anthropic-*`：`raw/` 在 `.gitignore` 里，CI 上不存在，回放式用例会集体 skip 成假绿（#12 已踩过）。转录的作用是定形状，定完把期望写死在测试里。手抄时刻意保留了两处真实特征——data 负载尾部的空格填充（上游抗缓冲手段）、中途插入的 `ping` 帧——解码器必须无视这两样。

**已知缺口，不装作没有**：

- **上游的 thinking 在这条路上必然丢弃，Codex 看不到 Claude 的推理过程。** 不是疏忽：Responses 有 `response.reasoning_summary_text.delta` 可以承接，但手上三份 Responses 转录里的 reasoning item 只有 `encrypted_content`、一条 delta 都没有，照文档猜着造 item 比明着丢更危险。等 ④（A→R）拿到真实转录再补。**这是 UX 层面的可感知退化，不只是内部细节。**
- 五份 Anthropic 转录**全是 `stream: true`**（Claude Code 恒发流式），`DecodeFullBody` 没有真实样本背书，形状是照协议文档 + 与流式那半边的对称性写的（同 §9.1 里 R→CC 那条的性质）。
- `error` SSE 帧五份转录里一次都没出现（都是 200 正常流），照协议文档实现，用例手写。
- 中段的 `RoleSystem` 消息上提之后丢掉「它插在哪」这个信息。实采里 Responses 侧的 developer 消息全在最前，没有中段用例，按简单规则先来，不为没见过的形态提前设计。
- 全链只对着**假** Anthropic 上游跑过。真机验收（Codex CLI → 网关 → 真实 Anthropic 上游）挂在 #25 上作 PO 手动清单，与 #11 / #12 同性质，不作合并闸；它同时受 #7 制约（需要官方凭证）。

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
- 样本里 `content` 全是字符串，**没有一份带多模态 part**。数组形态与图片 part 的处理（认得的进 Text、认不得的整份进 Extras）只有手写用例，且 M2 本就不实现图片——钉的是「带图片的请求不被拒收」，不是「图片能转过去」。**图片到 Anthropic 出口是丢弃的，但已登记 `vendor_content`**（v0.34），日志会说出来；真做转换是单独一批（PO 已裁定要做，见口径层 v0.37 与 #33）。
- 样本里 `tool_choice` 只出现过 `"auto"` 一种取值，其余四种形态靠手写用例。
- 上游的 thinking 在这条路上必然丢弃：CC 没有承接它的位置，塞进 `content` 会把推理过程混进正文。与 §9.2 那条同因不同向，都是口径层 §2.6「不做伪映射」的结果。
- 并行工具调用的**交错**分片在这条路上验不到：Anthropic 上游的块是严格顺序的（一个 `content_block_stop` 之后才轮到下一个），交错只可能出现在 CC 上游那边，由 A→CC 方向的用例覆盖。
- 全链只对着**假** Anthropic 上游跑过。真机验收（opencode / pi → 网关 → 真实 Anthropic 上游）挂在 #9 上作 PO 手动清单，性质同 #11 / #12 / #25，不作合并闸。

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
| M0 透传骨架 | 骨架 + 三协议原始字节透传 + SSE + Tap usage 提取（细则见 §6.1）；渠道/接入点 SQL 手工建；golden 样本必抓子集（§9）；对 Anthropic 官方跑通 Claude Code、对百炼/OpenAI 官方跑通 CC 透传。规格见 Issue [#1](https://github.com/SimonGino/portage/issues/1) | 1~2 个周末 |
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

**全局限流不在这一层**：口径层 §2.7 已裁定单个全局令牌桶（10 QPS / 突发 20）做在网关自己里，nginx 的 `limit_req` 会变成重复一层。实现见 §7.2。

**网关自己会发 `X-Accel-Buffering: no`**（口径层 v0.30，见 §7.3）：样例里的 `proxy_buffering off` 因此是双保险，真正的用处是覆盖那些不由我们维护的 nginx。

## 12. 参考对照

仓库索引与各仓库定位、许可证注意事项见本仓库 `CLAUDE.md`「参考仓库」一节；下表是逐文件的路径对照。**本项目只参考上表这三个仓库**；下表 new-api 路径均为 `~/Code/GitHub/new-api`（上游）相对路径，已逐一核实存在。

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

## 附录：开放问题记录

- ~~上游清单与各渠道 key 数~~（#1 已决 v0.17：Anthropic/OpenAI/Vertex/百炼四渠道 + 渠道多凭证聚合，见 §0/§7）
- ~~真实接入点与候选清单~~（#2 已决：运营数据不冻结，M0 验收集见 §0）
- ~~harness 清单增删~~（#7 已决：必过档 Claude Code、Codex CLI；顺带档 pi、OpenCode）
- ~~转换方向优先级~~（#9 已决：协议转换属 v1、透传先行、按口径层 §2.1 分批，设计态考虑落 §2/§4/§5/§6）
- ~~**转换方向集合**~~（已决：口径层 v0.3 裁定为三协议全互转，6 转换 + 3 透传，全集为准；分期之争已随 C1 收敛：属 v1 承诺、节奏透传先行）
- 公网暴露与否 → 决定 TLS / 限流 / listen 地址默认值
- ~~thinking 同协议透传是否进 P0~~（已随 P0 原始字节透传自动解决；跨协议丢弃仍是 P1 已决策略）
