# SGLang 过载行为与自带保护参数调研（#52）

调研基准：sgl-project/sglang **main 分支源码**（最新 release v0.5.17，2026-08-08）+ 官方文档。所有源码结论均附文件路径；文档结论附 URL。事实核对日期：2026-08-12。

背景：PO 自部署 SGLang 于 B300 两组机器，LB 后单 base_url；过载时 Prometheus 观测到 100+ 请求排队不返回、客户端超时；稳态并发 30–40；模型进程不崩，靠重启清队列。

---

## 1. 请求调度与排队机制

链路：HTTP 入口（FastAPI/uvicorn，`python/sglang/srt/entrypoints/http_server.py`）→ TokenizerManager → **Scheduler**（`python/sglang/srt/managers/scheduler.py`）。Scheduler 维护两个核心结构：

- **`waiting_queue`**：进程内存里的 Python list。新请求 tokenize 后进入该队列（`scheduler.py`，`waiting_queue.append(req)`）。
- **running batch**：continuous batching 的运行批。每个调度循环按 `schedule_policy`（默认 `fcfs`；另有 `lpm`/`lof`/`random`/`dfs-weight`/`priority`/`routing-key`，见 `server_args.py`）从 waiting_queue 挑请求组 prefill batch；准入的量由 KV cache 剩余 token 预算与 `max_running_requests` 决定，SGLang 整体是 prefill 优先。

### 队列默认无上限——PO 观测现象的直接机制

`--max-queued-requests` 默认 `None`（`server_args.py`：`max_queued_requests: Optional[int] = None`），此时 `_abort_on_queued_limit()` 直接返回 False、不做任何限制（`scheduler.py`）。**即：默认配置下 waiting_queue 无界，过载时请求全部堆在队列里，服务端不主动返回任何错误，客户端一直挂着等首 token，直到自己超时。** 100+ 请求排队不返回正是默认行为，不是故障。

配套事实：

- **排队请求无服务端超时**：环境变量 `SGLANG_REQ_WAITING_TIMEOUT` / `SGLANG_REQ_RUNNING_TIMEOUT` 默认 `-1`（关闭，`python/sglang/srt/environ.py`）。不设置就永不因等待过久被服务端剔除。
- **客户端断连会 abort，但依赖真正断开 TCP**：`tokenizer_manager.py` 会周期性 `await request.is_disconnected()`，对已断连请求（waiting 或 running 状态均可）发 abort。若客户端/中间层超时后仍保持连接（或断连检测未触发），请求留在队列。PO 观测"靠重启清队列"与此一致——队列在 Scheduler 进程内存中，无持久化，重启即清空；断连清理是否生效未在 PO 部署版本上核实（标注：不确定，与版本和客户端行为有关）。
- **retract（撤回）加剧过载表现**：decode 阶段 KV pool 满时，running batch 里的请求会被"撤回"到队列重排（日志 `KV cache pool is full. Retract requests.`；指标 `sglang:num_retracted_reqs`）。过载时吞吐进一步坍缩、排队时间更长。官方建议 retract 频繁时调大 `--schedule-conservativeness`（见下）。

来源：
- `python/sglang/srt/managers/scheduler.py`（`_abort_on_queued_limit`、`_abort_on_waiting_timeout`）：https://github.com/sgl-project/sglang/blob/main/python/sglang/srt/managers/scheduler.py
- `python/sglang/srt/managers/tokenizer_manager.py`（断连 abort）：https://github.com/sgl-project/sglang/blob/main/python/sglang/srt/managers/tokenizer_manager.py
- 调度机制文档：https://docs.sglang.ai/（Continuous Batching / Scheduling Policies 章节）；DeepWiki 解读 https://deepwiki.com/sgl-project/sglang/3.3-scheduling-policies-and-batch-formation

## 2. 自带保护参数清单（main / v0.5.x 口径）

定义均见 `python/sglang/srt/server_args.py`；文档 https://docs.sglang.ai/ Server Arguments 页。

| 参数 | 默认 | 语义 | 超限/相关行为 |
|---|---|---|---|
| `--max-running-requests` | `None`（由 KV 显存与 req pool 自动推导，`model_runner.py`） | running batch 并发上限 | 超出的请求**不报错，留在 waiting_queue 排队** |
| `--max-queued-requests` | `None`（**无上限**） | waiting_queue 长度上限 | 超限时 abort，**HTTP 503**（见下）；**disaggregation（PD 分离）模式下该参数被忽略**（源码 docstring 原话） |
| `--max-total-tokens` | `None`（由 `mem-fraction-static` 推导） | KV cache 内存池 token 总数上限 | 主要用于调试/压内存，不是准入拒绝机制 |
| `--mem-fraction-static` | 自动（按 GPU 显存启发式） | （模型权重 + KV pool）/ 显存 的占比 | 决定 KV 容量上限，间接决定可承载并发 |
| `--chunked-prefill-size` | 自动（按显存启发式；`-1` 关闭） | 单次 prefill chunk 的 token 数 | 防长 prompt prefill OOM/独占；过载保护意义有限 |
| `--schedule-conservativeness` | `1.0` | 调度保守程度，越大越保守 | retract 频繁时官方建议调大（如 1.3）；`token usage < 0.9` 且有排队时可调小（如 0.3） |
| `--schedule-policy` | `fcfs` | 出队策略 | 只影响顺序，不影响准入/拒绝 |
| env `SGLANG_REQ_WAITING_TIMEOUT` | `-1`（关闭） | 排队等待超时（秒） | 超时请求被 abort，同样走 503（`scheduler.py` `_abort_on_waiting_timeout`：`"Request waiting timeout reached."`） |
| env `SGLANG_REQ_RUNNING_TIMEOUT` | `-1`（关闭） | 运行超时（秒） | 同上机制 |

### 重点：队列超限时返回什么——**HTTP 503，不是 429**

源码（`scheduler.py` `_abort_on_queued_limit`）：队列满时对被拒请求发 `AbortReq`，`finished_reason = {"type": "abort", "status_code": HTTPStatus.SERVICE_UNAVAILABLE, "message": "The request queue is full."}`。官方测试 `test/registered/core/test_request_queue_validation.py` 注释里的期望状态码为 `[200, 200, 503, 503, ...]`——**非流式请求确认返回 HTTP 503 + 错误消息，连接正常关闭（不是拒连/RST）**。流式请求在首 token 前被拒时同样以错误结束，确切的线上表现（503 头 or SSE 错误帧）未逐行核对，标注不确定。

**纠偏**：部分二手资料称队列满返回 429，与源码不符；429 语义出现在 sglang-router / model gateway 的重试逻辑里（router 对 429/503 重试），不是 SGLang server 本身的返回码。

**版本门槛**：`--max-queued-requests` 由 PR #7565（"throttle requests at scheduler based on --max_queued_requests"，2025-07-28 合入）引入，**首个包含它的 release 是 v0.4.9.post6（v0.4.9.post5 及更早没有此参数）**（用 GitHub compare API 对 tag 逐一核对）。PO 需先确认部署版本 ≥ v0.4.9.post6，否则该参数不存在。

来源：
- `server_args.py`：https://github.com/sgl-project/sglang/blob/main/python/sglang/srt/server_args.py
- `scheduler.py` 队列满 abort：https://github.com/sgl-project/sglang/blob/main/python/sglang/srt/managers/scheduler.py
- 503 测试：https://github.com/sgl-project/sglang/blob/main/test/registered/core/test_request_queue_validation.py
- PR #7565：https://github.com/sgl-project/sglang/pull/7565
- 调参指南（retract / schedule-conservativeness / mem-fraction-static 原文）：https://github.com/sgl-project/sglang/blob/main/docs/docs/advanced_features/hyperparameter_tuning.mdx

## 3. Prometheus 指标（队列/并发相关，确切名字）

定义见 `python/sglang/srt/observability/metrics_collector.py`（需启动加 `--enable-metrics`；文档 `docs/docs/references/production_metrics.mdx`）：

| 指标 | 类型 | 含义（源码 documentation 原文） |
|---|---|---|
| `sglang:num_running_reqs` | Gauge | The number of running requests |
| `sglang:num_queue_reqs` | Gauge | The number of requests in the waiting queue（**即 waiting_queue 深度，PO 看到的 100+ 应对应此指标**） |
| `sglang:queue_time_seconds` | Histogram | 排队时长分布 |
| `sglang:num_retracted_reqs` / `sglang:num_retracted_requests_total` | Gauge / Counter | 被撤回请求数（KV 满的信号） |
| `sglang:token_usage` | Gauge | KV pool 利用率（>0.9 为高） |
| `sglang:num_aborted_requests_total` | Counter | 被 abort 的请求总数（含队列满/超时拒绝） |
| `sglang:gen_throughput` | Gauge | 生成吞吐 token/s |
| `sglang:time_to_first_token_seconds` / `sglang:e2e_request_latency_seconds` / `sglang:inter_token_latency_seconds` | Histogram | TTFT / 端到端 / token 间延迟 |

日志对应物：`#running-req` / `#queue-req`（与上面两个 gauge 同源）。

来源：https://github.com/sgl-project/sglang/blob/main/python/sglang/srt/observability/metrics_collector.py

## 4. 结论：网关侧限流选型与两侧分工

**限"最大并发 in-flight"对症。** 因果链：SGLang 自身准入只按显存/token 预算算（`max_running_requests` 由 KV 容量推导），默认队列无界、无任何等待时长维度的保护——它保证"不 OOM、不崩"，但不保证"及时回答或及时拒绝"。PO 观测的病症（100+ 排队、客户端超时、只能重启）正是"无界队列 + 无服务端等待超时"的组合。网关按渠道限 in-flight 并发，恰好补上 SGLang 缺失的这一维：把稳态之外的请求在网关快速失败/短排队，而不是灌进上游无界队列变成僵尸请求。

**两侧分工建议：**

1. **网关（portage）：按渠道限最大 in-flight 并发**，阈值设在稳态并发（30–40）附近略高（如 1.2–1.5 倍），超限快速失败（对客户端返回 429/503 类可重试错误）或短队列+短超时。这是主保护层，也是唯一能"按渠道"聚合控制的层。
2. **SGLang：`--max-queued-requests` 做 per-instance 兜底**（如设为 max_running_requests 的 1–2 倍）。关键理由：两组机器在 LB 后共享一个 base_url，网关只能限"总量"；LB 不均时单组仍可能过载，SGLang 侧队列上限是唯一的 per-instance 保护。超限返回 503，网关应把上游 503 识别为过载信号（计入熔断/摘除判断），且**错误回显不泄露上游 base_url**（项目既有口径）。前提：确认部署版本 ≥ v0.4.9.post6；若用 PD 分离部署该参数无效。
3. **SGLang：设 `SGLANG_REQ_WAITING_TIMEOUT`**（如 60–120s），让积压请求由服务端超时清理，替代"重启清队列"。
4. **观测对齐**：网关侧并发水位与上游 `sglang:num_running_reqs` / `sglang:num_queue_reqs` 对照；`num_queue_reqs` 持续 > 0 且增长即上游饱和；`num_retracted_reqs` 上升说明 KV 压力大，可考虑调大 `--schedule-conservativeness` 或降 `--max-running-requests`。
5. **不建议**把 `--max-running-requests` 压到稳态值来"限流"：它只控 running batch，不拒绝请求，压低只会让排队更长；限拒绝要靠 `--max-queued-requests` 和网关。

**不确定项（已标注）**：流式请求被 503 拒绝时的确切线上格式；客户端断连清理在 PO 部署版本上是否生效；PO 部署版本号未知（决定 `--max-queued-requests` 是否可用）。
