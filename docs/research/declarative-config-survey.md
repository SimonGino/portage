# Research：五个参考仓库的声明式配置形态（#25）

本票只取**事实**，不下裁决——裁决归 #24 图下游各票。

调研对象与被读的工作副本版本（全部本地只读，无网络，行号均指下列 HEAD）：

| 仓库 | 上游 | 语言 | HEAD | 日期 |
|---|---|---|---|---|
| `litellm` | BerriAI/litellm | Python | `f318ef03bd` | 2026-05-04 |
| `CLIProxyAPI` | router-for-me/CLIProxyAPI | Go | `d757063c` | 2026-08-13 |
| `new-api` | QuantumNous/new-api | Go + React | `ccd535ef8` | 2026-08-10 |
| `sub2api` | Wei-Shaw/sub2api | Go + 前端 | `5a6143097` | 2026-07-29 |
| `opencodex` | lidge-jun/opencodex | TS/Bun | `4e0ffb2c` | 2026-08-12 |

**一句话总览**：五个仓库在「配置从哪来」上占了三种形态——纯文件（CLIProxyAPI、opencodex）、纯 DB（new-api、sub2api）、文件与 DB 并存（litellm）。**并存的那一个不是优先级模型，是物理并集**（详见 §2.1）；两个纯文件的仓库都把配置文件做成了**双向可写**的；两个纯 DB 的仓库都**没有任何文件/env 预置业务实体的口子**。

---

## 1. 横向对比表（五仓 × 六问）

| | **litellm** | **CLIProxyAPI** | **new-api** | **sub2api** | **opencodex** |
|---|---|---|---|---|---|
| **① 事实源关系** | **并存，不是优先级**。config 的 `model_list` 永不写 DB，DB 的 model 也不回写 yaml，二者在内存 Router 里按 id 取并集；同名 model 变成两个 deployment 参与负载均衡。非 model 配置（`general_settings` 等四块）则 **DB 深合并覆盖文件**。UI 只写 DB | **文件为准，且文件可写**。config.yaml 是唯一源；management API 改配置**回写同一文件**（保留注释）；启动会为 bcrypt 原地改写一次；fsnotify 热加载 | **DB 为准，无配置文件层**。`.env` 只管基础设施；优先级 = Go 编译期默认值 < DB 行，env 不参与（唯一例外见 §2.3）。管理端只写 DB | **DB 为准**；`config.yaml` 只管进程级 + 全局策略，不含任何业务实体。少数重叠键（OAuth、S3）是 **DB 设置覆盖文件/env**。文件被写回的唯一路径是首次安装向导 | **文件为准，UI 是文件的客户端**。`~/.opencodex/config.json` 是唯一源，无配置 DB；**UI / CLI / 手改三条通道写同一个文件**；无 watch |
| **② 秘密怎么进文件** | `os.environ/XXX` 前缀（白名单位置解析，非通用模板）；另有 8 种外部 secret manager、`oidc/` 前缀、`environment_variables` 块直接 setenv。DB 里的上游 key 用 SecretBox 加密，密钥 = SHA256(salt key)，salt 未设时**退化成 master key** | 静态上游 key **明文写 config.yaml**；OAuth token 走独立 `auth-dir`，一账号一 JSON（0700/0600）。**不支持 `${ENV}` 插值**。唯一被哈希的是 management key（bcrypt） | 上游 key 在 DB 里**明文**，无加密、无 Serializer；env 侧秘密只有基础设施类（DSN / SESSION_SECRET / CRYPTO_SECRET）。**无预置渠道 key 的口子** | 上游凭证只能 UI/API 录入进 DB，**明文 JSONB**（迁移注释写「加密存储」，与实现不符）。**不支持 `${VAR}`**，但 viper `AutomaticEnv` 支持扁平大写 env 覆盖 | `apiKey` 可明文，也可写 `${VAR}` / `$VAR`；**在「用的时候」解析，不在 load 时**，文件里永不落秘密。`ocx init` 留空时默认写 env 引用。OAuth/forward token 不进 config.json |
| **③ 实体身份键** | `model_name` 是**可重复的组名**；身份是 `model_info.id`，缺省 = `sha256(model_group + 解析后的全部 litellm_params)`。**env 解析在 hash 之前**，故轮换 key 也换实体。改名 = 删旧建新。另有一条随机 uuid4 旁路 | **四套并存**：运行期 = 内容哈希 `sha256(kind‖parts)[:12]`；哈希碰撞按**数组顺序**加 `-N`；管理 API 用**数组下标**；OAuth 凭证 = auth-dir 相对文件路径；`openai-compatibility.name` 是路由身份 | **自增 id**，`name` 无唯一约束；`BatchInsertChannels` 无 `OnConflict`，重复执行不断新建重复渠道。仓内有自然键 upsert 范式，但没用在渠道上 | **自增 `BIGSERIAL`**。唯一 name 约束只有 groups 与 channels；accounts / proxies / api_keys 的 name 不唯一。导出文件里 proxy 用**自然键拼串**，account **没有身份键** | 三种：provider = **对象 key 名**（且 key 名参与内置注册表匹配，改名静默降级，无 rename 接口）；combo / routingProfile = 对象 key，但有显式 `renameFrom` 协议；数组类实体 = **显式 id 字段**，从不用下标 |
| **④ 启动校验失败模式** | **无 schema 校验、无严格模式**，四档混杂：YAML 语法错 / 文件缺失 / include 缺失 / model 缺 `litellm_params` → 启动即拒；pydantic 能构造但校验失败的条目 → 跳过继续（`ignore_invalid_deployments=True` 硬编码，无开关）；未知 key → 静默忽略；`api_key` 引用未设 env → None，跑到那条请求才报。**`CONFIG_FILE_PATH` 路径不存在则静默空跑** | 三档。硬失败是 `log.Errorf` + `return`，**退出码 0**（仓规禁用 `log.Fatal`）。文件缺失 / YAML 语法错 / weight 非法 → 拒；**未知字段静默忽略**（未启用 `KnownFields`）；缺 `base-url` 的 key 条目**静默丢弃**；坏 auth JSON 跳过 + warn；热加载写坏则保留旧配置 | 混合：少数 `os.Exit(1)`（SESSION_SECRET 照抄示例值、DB 连不上、TRUSTED_PROXIES 非法等），多数「打日志 + 用默认值继续」，个别**完全静默**；DB 里的坏 option 基本不校验，`strconv.Atoi` 丢弃 error，坏值静默变 0 | **启动即拒**（`log.Fatalf`），但只对能解析出来的错：**配置文件缺失是合法的**（走默认 + env），**未知键静默忽略**（裸 `viper.Unmarshal`，无 `ErrorUnused`）。少数软失败只 warn | **任何配置错误都不拒绝启动**。三级降级：字段级 warn 保留 → 整份 schema 失败则 merge defaults 重试 → 仍失败则**备份原文件、用出厂默认起**。引用不存在的 provider 属 schema 硬失败 → 落到第三级，用户所有 provider 静默消失 |
| **⑤ 导出成文件的反向路径** | **没有**。只有反方向的 `litellm-proxy models import <yaml>`（yaml → DB）。`/config/yaml` 是返回 `{"hello":"world"}` 的 mock 端点。最接近的是 `GET /v1/model/info` 的 JSON，非可回灌格式 | 不适用「从 DB 导出」（无 DB）；但有 `GET /v0/management/config.yaml` 原样返回文件字节、`GET /v0/management/config` 返回结构化 JSON（**含明文上游 key**）。另有 **git store：跑起来的实例把自己的配置改动 commit + push 回 git 仓库** | **没有**。39 条 `/api/channel/` 路由逐条看过，无 import/export/dump。三个名字像批量的都不是文件路径（`mode:"batch"` 是「一个模板 + key 按换行切」，`/batch` 是批量删除） | **有，但不等价于声明式配置**：账号 + 代理的备份 JSON（`GET/POST /admin/accounts/data`）。它是 append-only import——账号无身份键、无更新/删除语义、不含分组绑定、排除 spark 影子账号。另有 S3 上的 pg_dump 全库备份 | **有真正的往返**：`ocx config export / import / validate / set` 四件套。`export` 导出当前真实配置且**不打码**（`show`/`get` 才打码），这是它能被 `import` 吃回去的前提 |
| **⑥ 无 UI / 只跑转发的部署形态** | **没有一等公民形态**。UI 静态资源在模块导入期**无条件 mount**；`DISABLE_ADMIN_UI` 只是前端自觉 + 堵 SSO 登录，`/login` 三个版本都不检查它。**不配 `DATABASE_URL` 可以跑**且是代码显式照顾的路径，但虚拟 key 全废、管理端点 20+ 处返回 500 | **本来就没有内置 Web UI**——`management.html` 运行时从 GitHub Release 下载。三个开关：`disable-control-panel` / `disable-auto-update-panel` / `secret-key: ""`（后者让整个 `/v0/management/*` 404）。注意 `MANAGEMENT_PASSWORD` env 非空会**强制打开**管理路由 | **不存在**。`//go:embed web/dist/index.html` 是包级无条件的编译期硬依赖，缺文件直接编译失败；无 build tag、无「只跑转发」开关。`NODE_TYPE=slave` 只关迁移与定时任务，管理 API 一条不少 | **有一档，编译期 build tag**：不加 `-tags embed` 编出的二进制无任何前端，非 API 路径返回「Frontend not embedded」。仓库自带的 `make build` 就不加。但这只是「不发 UI 资产」，**业务配置仍只能走带 JWT 的 admin API 或直接写 DB** | **它有 UI**，且**没有关掉 dashboard 的配置开关**。三种事实上的无 UI：`gui/dist` 未构建则静态资源不服务；`unauthenticatedLoopbackListener` 第二监听器只服务数据面（**完全无凭证**，文档挂 danger 警告）；管理面凭证不合规时自行 fail-closed 而数据面照常起 |

---

## 2. 逐仓展开

### 2.1 litellm——「文件 + DB 并存」的唯一先例，但并存规则不是优先级

#### ① 事实源关系

**model 层面是物理并集，不是覆盖。**

- config.yaml 的 `model_list` **永远不会**被写进 DB。`save_config` 在 DB 分支里显式 `config_to_save.pop("model_list", None)` 才写（`litellm/proxy/proxy_server.py:3230-3231`）；`_update_config_from_db` 只从 DB 拉 `general_settings` / `router_settings` / `litellm_settings` / `environment_variables` 四个 key，**不含 `model_list`**（`litellm/proxy/proxy_server.py:5286-5290`）。
- `litellm_proxymodeltable` 的写操作全仓只有三处，没有一处是「启动时把 config 的 model_list 灌进去」：`litellm/proxy/management_endpoints/model_management_endpoints.py:320`（`/model/new`）、同文件 `:248` / `:1188`（update）、`litellm/proxy/management_endpoints/key_management_endpoints.py:3744-3746`（**master key 轮转时的重新加密**，见 `:3700-3711` 注释）。
- 两边的 model 在**内存 Router 的 `model_list` 上按 `model_info.id` 合并**：config model 的 id 是内容哈希（`litellm/router.py:6995-7002`），DB model 的是 `@default(uuid())`（`schema.prisma:50`），正常不碰撞——所以**同名 `model_name` 的 config model 与 DB model 会同时存在为两个 deployment 参与负载均衡**，而不是谁覆盖谁。

**非 model 配置是 DB 覆盖文件。** `get_config` 先读文件再叠 DB（`litellm/proxy/proxy_server.py:3421-3428`），合并规则 `_deep_merge_dicts` 注释写明「On conflicts, src (DB) wins, but empty lists are treated as "no value"」（`litellm/proxy/proxy_server.py:5209-5228`），非 dict 时直接 `current_config[param_name] = db_param_value`（`:5265-5267`）。`router_settings` 另走 `_add_router_settings_from_db_config`，同样 DB 覆盖（`:4948-4950`）。

**UI 只写 DB，config 里的 model 在 UI 上不可编辑、不可删。**

- `/model/new`：`prisma_client is None` 直接 500（`model_management_endpoints.py:970-976`）；`store_model_in_db is not True` 直接 500 提示设 `STORE_MODEL_IN_DB`（`:1019-1026`）。
- `/model/update`：查不到 DB 记录但 Router 里有 → 显式拒绝，错误文案 `"Can't edit model. Model in config. Store model in db via /model/new. to edit."`（`model_management_endpoints.py:1129-1139`）。
- `/model/delete`：`model_in_db is None` → 400（`:765-772`）。
- 注意**文档/代码冲突**：`/model/new` 的 `description` 仍写着 `"Allows adding new models to the model list in the config.yaml"`（`model_management_endpoints.py:917`），与实现不符，以代码为准；`/model/delete` 同样过时（`:732`）。`deploy/charts/litellm-helm/README.md:188-193` 的「Admin UI is unable to add models … it would need to update the config.yaml」也是过时说法。

**唯一一条 yaml 回写路径实际是死路，且若触发是泄密点。** `save_config` 的 else 分支 `yaml.dump(new_config, config_file, ...)`（`litellm/proxy/proxy_server.py:3237`，全仓唯一的 `yaml.dump`）只在「`STORE_MODEL_IN_DB=True` 但 `prisma_client is None`」这个畸形组合下才走到——所有调用方都先 gate 了 `store_model_in_db is not True → 500`（`proxy_server.py:13818-13823`、`ui_crud_endpoints/proxy_setting_endpoints.py:327-333`、`model_management_endpoints.py:1285-1291` 等）。若真触发：调用方传进来的 config 已经跑过 `_check_for_os_environ_vars`（`proxy_server.py:3432`），`os.environ/XXX` 全部被替换成**明文真值**，而 yaml 分支**不 pop `model_list`**。

**周期性拉取 + 一个不对称的热加载行为。** `scheduler.add_job(proxy_config.add_deployment, "interval", seconds=30, ...)`，被 `if store_model_in_db is True:` 包着（`proxy_server.py:6971-6987`）。这个循环里 `_delete_deployment` 会重新读一次 config 文件（`:4537`），把 config model 的 id 并进 `combined_id_list`（`:4546-4568`），然后删掉既不在 DB 也不在 config 的 deployment（`:4570-4574`）；而 `_add_deployment` 只加 DB model。结果：**运行中从 config.yaml 删掉一个 model，30 秒内从 Router 消失；往 config.yaml 加一个 model，永远不会热加载**（必须重启）。

**`STORE_MODEL_IN_DB` 默认 `False`**（`proxy_server.py:1753`），语义是「是否启用 DB 作为第二事实源」。为 False 时 `_update_config_from_db` 直接 return（`:5278-5282`），所有写类管理端点 500。env 只在一处被读（`:6943-6945`），且**在 `load_config` 之后**——首次 `load_config` 只吃文件，DB 覆盖是启动后期第一次 `add_deployment` 才补的，故 `load_config` 里那个逐 key 处理 `litellm_settings` 的长循环（`:3600-3903`）**不会重跑**，DB 里的 `litellm_settings` 只有 `LITELLM_SETTINGS_SAFE_DB_OVERRIDES` 白名单内的会生效（`:5250-5254`）。

**一个直接相关的官方逃生口**：`general_settings.supported_db_objects`（类型 `SupportedDBObjectType`，`litellm/proxy/_types.py:71-86`，字段声明 `:2419`）一旦设为列表，就**只**从 DB 加载列表里的对象类型（判定 `_should_load_db_object`，`proxy_server.py:5312-5346`；model 的调用点 `:5386-5391`）。这是「DB 存在，但让文件独占 model 事实源」的现成机制。

#### ② 秘密

`os.environ/` 是**纯前缀约定**，只在若干硬编码白名单位置识别，且只认「整个值是 str 且以该前缀开头」（不支持内嵌拼接、不支持 `${}`）。

- 统一递归入口 `ProxyConfig._check_for_os_environ_vars`（`proxy_server.py:3239`），核心三行在 `:3271-3273`；递归深度有上限（`:3240`），超限只 warn（`:3253-3258`）。它在 `get_config` 末尾被调一次（`:3434`）。
- 除此之外还有约二十处**各自为政的重复实现**，没有共用函数：`litellm/router.py:7493-7496`、`:7633-7634`；`proxy_server.py:3287-3288`、`3508-3509`、`3563-3571`、`3675-3676`、`3884-3885`、`3938-3941`、`3947-3948`、`4161-4164`、`4176-4177`、`4549-4551`、`6711`；`litellm/_redis.py:186-189`；`litellm/proxy/proxy_cli.py:856-866`；`litellm/proxy/guardrails/guardrail_registry.py:443-447`；`litellm/proxy/pass_through_endpoints/pass_through_endpoints.py:90-114`；`litellm/proxy/vector_store_endpoints/management_endpoints.py:187-196, 287-296`。
- 剥前缀与取值：`get_secret`（`litellm/secret_managers/main.py:159`，`:167-168` 剥前缀）。**env 不存在时静默返回 `None`，不抛异常、不原样返回字符串**（`main.py:319-327`）；于是 config 里那个 key 变成 `None`（`proxy_server.py:3273`），报错点漂移到 provider 调用处。
- **类型强转陷阱**：env 值是 `"true"`/`"false"` 时 `get_secret` 返回的是 **bool 而非 str**（`main.py:321-325`）；走 secret manager 那条路更激进，用 `ast.literal_eval` 试探（`:310-318`）。
- **一条值得抄的安全反向逻辑**：请求体里带 `os.environ/` 会被显式拒绝，防止借健康检查读服务端 env（`litellm/proxy/health_endpoints/_health_endpoints.py:47-68`，注释在 `:1736`）。
- 解析时机：启动一次。周期性重跑只在 `store_model_in_db=True` 时存在（`proxy_server.py:6971-6983`）；纯 config 部署改 env 必须重启。本副本**没有 `/config/reload` 路由**（只有 `/reload/model_cost_map`（`:14078`）和 `/reload/anthropic_beta_headers`（`:14439`））。
- 其它进秘密的路子：`environment_variables` 块可写明文并就地 setenv（`proxy_server.py:3563-3576`，有 `_BLOCKED_ENV_KEYS` 黑名单在 `:3528`）；八种 secret manager（枚举 `litellm/types/secret_managers/main.py:6-16`，分发 `proxy_server.py:4430-4476`，非法值 `raise ValueError` 在 `:4475-4476`）；`oidc/<provider>/<audience>` 前缀（`secret_managers/main.py:171-278`，**失败语义与 `os.environ/` 相反，一律 raise**）；config 文件本身可从 S3/GCS 拉（`proxy_server.py:3399-3411`）。
- `.env` 加载受 `LITELLM_MODE` 门控（默认 `DEV` 即加载，设 `PRODUCTION` 则不）：`litellm/__init__.py:17-20`、`litellm/proxy/proxy_cli.py:27-30`。
- DB 里的上游 key **确实加密**：libsodium SecretBox，密钥 = `SHA256(salt_key)`（`litellm/proxy/common_utils/encrypt_decrypt_utils.py:79-91`）。但 `salt_key` **只从 env 读，不支持从 config.yaml 走 `os.environ/`**，且未设时**退化成 master key**（`encrypt_decrypt_utils.py:8-16`）——轮换 master key 会连带解不开 DB 里的凭证，UI 文案明写「Can NOT CHANGE THIS ONCE SET」（`litellm/proxy/common_utils/admin_ui_utils.py:78`）。解密失败**不 raise**，按 `return_original_value` 决定（`:62-76`），而 `_add_deployment` / `decrypt_model_list_from_db` 都传 `True`（`proxy_server.py:4592-4597`、`4629-4634`）——**这意味着 DB 里存的可以是密文，也可以是明文 `os.environ/XXX`，解密失败时原样透传**。

#### ③ 身份键

- `Deployment` 三字段：`model_name` / `litellm_params` / `model_info`（`litellm/types/router.py:400-403`）。`model_name` 是**可重复的组名**，索引结构就是一对多 `Dict[str, List[int]]`（`litellm/router.py:7877-7881`）。
- 身份是 `model_info.id`；缺省时 `_generate_model_id(model_group, litellm_params)` = `sha256(model_group + 所有 litellm_params 拼串)`（`litellm/router.py:6995-7024`），调用点 `:7500-7502`。
- **关键顺序问题**：env 解析在 `router.py:7493-7495`，hash 在 `:7502`——**hash 的输入是解析后的明文 api_key**。写 `api_key: os.environ/OPENAI_API_KEY` 并不能让 id 免疫密钥轮换。
- **随机 uuid 旁路**：`ModelInfo.__init__` 在 `id is None` 时 `str(uuid.uuid4())`（`litellm/types/router.py:136-141`），而 `Deployment.__init__` 在 `model_info is None` 时会构造 `ModelInfo()`（`:414-415`）。稳定 hash 只在 `set_model_list` / `_build_model_id_to_deployment_index_map` / `_delete_deployment` 三条路径上成立。
- 改 `model_name` 或任一 param → 新 hash → 新 id → **旧 deployment 被移除、新的加入**，等价于删旧建新，旧 id 上的 cooldown / 用量关联全部断裂（清理逻辑 `proxy_server.py:4546-4574`；`upsert_deployment` 也是 pop 旧再 add，`router.py:7899-7929`；`add_deployment` 遇到已存在的 id **直接 return None 不更新**，`router.py:7747-7749`）。
- 两个防误删保护：`db_models` 为空直接 return 0（`proxy_server.py:4527-4528`）；config 读取失败时跳过清理而不是删空（`:4536-4544`）。

#### ④ 启动校验

**无 schema 校验，无严格模式。** `ConfigYAML`（`litellm/proxy/_types.py:2461-2484`）看起来像 config 的 schema，但全仓只被用作 `/config/update` 的 request body（`proxy_server.py:13277`）和 mock 端点的入参（`:14057`），**从没被用来校验读进来的 yaml**。

- 异常传播：`proxy_startup_event` 是 FastAPI lifespan（`proxy_server.py:720-721`，挂载 `:1048`），`load_config` 的三个调用点（`:779` / `:792` / `:802`）无 try/except，函数体内也无顶层 try——任何异常直接让 uvicorn 拒绝启动。
- 文件级：语法错直接抛（`:3135-3140`）；文件不存在 `raise Exception(f"Config file not found: {file_path}")`（`:3143-3144`）；空文件 `raise Exception("Config cannot be None or Empty.")`（`:3148-3149`）；include 缺失 `raise FileNotFoundError`（`:3182-3183`）。测试佐证 `tests/proxy_unit_tests/test_proxy_config_unit_test.py:52-59`、`:149`。
- **⚠️ 一个静默失败的大坑**：配置若通过 `CONFIG_FILE_PATH` 环境变量给（Docker/K8s 常规姿势），路径不存在或后缀不是 `.yaml/.yml` 时 **整个 load_config 被静默跳过、代理带 0 个 model 起来**——`if env_config_yaml is not None: if os.path.isfile(...) and proxy_config.is_yaml(...):` 没有 else（`proxy_server.py:769-780`，`is_yaml` 只看后缀，`:3101-3106`）。走 `litellm --config x.yaml` 则会 fail fast（`proxy_cli.py:802-803` 有一次未捕获的预解析）。**同一份坏配置，两种启动姿势的失败模式相反。**
- model 条目级：`for k, v in model["litellm_params"].items()`（`:4162`）缺 `litellm_params` → KeyError；`model["litellm_params"]["model"]`（`:4166`）缺 `model` → KeyError；`model: os.environ/UNSET` → `get_secret` 返回 None → `"ollama" in None` → `TypeError: argument of type 'NoneType' is not iterable`（`:4168`），**报错信息跟 config 半点关系都没有**。三处都在 try 之外。
- 只有「能被 pydantic 构造但校验失败」的条目才被跳过，且发生在 Router 侧：`if self.ignore_invalid_deployments: ... return None`（`litellm/router.py:7114-7121`）。proxy 两处建 Router 都**硬编码** `ignore_invalid_deployments=True`（`proxy_server.py:4233`、`:4695`），**没有开关能改**（Router 自身默认是 `False`，`router.py:320`）。
- 未知 key：`litellm_settings` 兜底 `setattr(litellm, key, value)`，什么都吞（`:3900-3903`）；`general_settings` 逐个 `.get()`，不认识的不看；**唯一告警**在 `router_settings`（`:4218-4225`），且其中把 `health_check_interval` 放错 section 会 **raise**。`litellm_params` / `Deployment` 都是 `extra="allow"`（`litellm/types/router.py:296`、`:405`），写错字段名无任何提示。

#### ⑤ 导出

**没有。** `yaml.dump` 全仓只有 `proxy_server.py:3237` 那一处（另一处 `litellm/integrations/dotprompt/prompt_manager.py:350` 无关）。`/config/yaml` 的 docstring 自陈是 mock，返回 `{"hello": "world"}`（`proxy_server.py:14051-14073`）。

- 主 CLI 无导出选项（`proxy_cli.py` 只有一个 `@click.command()` 在 `:394`，逐条看完 `:395-600` 的全部 option）；`litellm-proxy` 子 CLI 的 `models` 组穷举为 list/add/delete/get/info/update/**import**（`litellm/proxy/client/cli/commands/models.py:96/143/175/185/201/312/433`）——**有 import，无 export**。`import` 是 yaml → DB 单向（`:393-404` 读文件，`:433-470` 逐条走 `/model/new`）。
- 最接近的是 `GET /v1/model/info`（`proxy_server.py:11568`，返回 `:11699`）与 `litellm-proxy models info --format json`（`commands/models.py:201-225`），但输出是 model_info 富化结构，不是可回灌的 config.yaml。
- UI 侧无导出；唯一提到「等价 yaml 片段」的是 alias 一小节的只读预览（`ui/litellm-dashboard/src/components/model_group_alias_settings.tsx:317`）。
- 查过但未找到：`proxy_cli.py`、`client/cli/main.py` 及 `commands/` 全部 9 个文件、7 条 `/config/*` 路由（`:13271`/`13424`/`13503`/`13565`/`13714`/`13787`/`14052`）、`config_management_endpoints/`、`common_utils/load_config_utils.py`（只有 S3/GCS 的读）、`pyproject.toml` 的 `[project.scripts]`、全仓 `yaml.dump`/`yaml.safe_dump`。

#### ⑥ 无 UI

- UI 是 committed 进 Python 包的 Next.js static export：`litellm/proxy/_experimental/out/`，683 个已跟踪文件。挂载在**模块导入期的一个裸 try 块**里（`proxy_server.py:1248` 起，`:1559-1560` `except Exception: pass`），三处 `app.mount`（`:1490-1502`），路径可被 `LITELLM_UI_PATH` 覆盖（`:1367`）。**这些 mount 无条件执行，不读任何开关。**
- `DISABLE_ADMIN_UI` 全仓 Python 侧只有 3 个命中点：`litellm/proxy/discovery_endpoints/ui_discovery_endpoints.py:27`（吐给前端）、`litellm/proxy/management_endpoints/ui_sso.py:598-602`（堵 SSO，返回说明页 `common_utils/admin_ui_utils.py:99`）、前端自觉（`ui/litellm-dashboard/src/app/(dashboard)/hooks/useAuthorized.ts:21`、`src/app/login/LoginPage.tsx:41`）。**`/login`（`proxy_server.py:12235-12237`）、`/v2/login`（`:12287`）、`/v3/login`（`:12362`）都不检查它**。未找到 `--no-ui` CLI 参数或 `general_settings.disable_admin_ui`。
- **不配 `DATABASE_URL` 能跑**且是显式照顾的路径：`_setup_prisma_client` 里 `if database_url is not None:` 才建 client（`proxy_server.py:7403-7407`）；CLI 侧 prisma migrate 整块被 `if os.getenv("DATABASE_URL") is not None or ...` 包着（`proxy_cli.py:888-891`）。此时虚拟 key 全废（`litellm/proxy/auth/user_api_key_auth.py:1212-1221`，只有 master key 本身能过），管理端点 20+ 处返回 `db_not_connected_error`（`litellm/proxy/_types.py:3613-3615`）。另有一个显式命名的「No-auth dev mode」（`user_api_key_auth.py:1755-1766`）。
- **判定**：config-only + 无 DB 是**被支持但功能残缺**的形态，不是产品化的部署模式——没有专门的启动模式开关、管理端点照常注册照常 500、UI 照常挂着。
- **文档未找到**：本工作副本的 `docs/` 只有 2 个文件（`docs/my-website/docs/providers/crusoe.md`、`docs/my-website/docs/proxy/guardrails/xecguard.md`），proxy 的 config/DB 优先级文档不在这个 checkout 里。退而查过 `README.md`、`ARCHITECTURE.md:186/211/240`、`deploy/charts/litellm-helm/README.md:101-108` 与 `values.yaml:247-253`、`docker/README.md`、全仓 112 个 md，均无「no DB / config-only」口径说明。

### 2.2 CLIProxyAPI——纯文件、无 DB 的对照组，但文件是双向的

#### ① 事实源

唯一 CLI flag 是 `-config <path>`（`cmd/server/main.go:48,103`），默认空时回落 `$PWD/config.yaml`（`:527`）。**没有「用 env 覆盖某个配置字段」的机制**，env 只决定配置存放后端（`main.go:211-271`：`PGSTORE_DSN` / `GITSTORE_GIT_URL` / `OBJECTSTORE_ENDPOINT` / `HOME_JWT` / `DEPLOY`）；`.env` 从 CWD 自动加载（`:187`）。配置值里**没有 `${ENV}` 插值**（`grep -rn "os.ExpandEnv|ExpandEnv|resolveSecret"` 零命中）；唯一的 env 间接引用是插件商店 token（`config.example.yaml:95-101` 的 `token-env`）。

四个存储后端只是**把同一份 config.yaml 搬到别处**，本地始终落一个真实文件路径给下游用（`main.go:402,466,513,518-528`）。Postgres 后端尤其说明问题：schema 里 config 表就是 `id TEXT PRIMARY KEY, content TEXT`，整份 YAML 存成一个字符串（`internal/store/postgresstore.go:129-137`）；auth 表 `content JSONB`、cooldown 表 `content JSONB`（`:139-163`）。**它没有把业务配置规范化进 DB，只是把文件当 blob 存。**

**management API 会回写 config.yaml**：约 90 个端点在 `/v0/management/*`（`internal/api/server_management.go:24-176`）。回写实现 `SaveConfigPreserveComments()` 把原文件解析成 `yaml.Node` 树、merge 后覆写（`internal/config/config_yaml.go:14,34,58-79`），目的是保住注释与 key 顺序；调用点 `internal/api/handlers/management/handler.go:179`、`:410`、`plugins.go:384`、`plugin_store.go:328`。另有整份覆写路径 `PUT /v0/management/config.yaml`——先写临时文件用 `LoadConfigOptional(tmp, false)` 校验，通过才覆盖真文件（`config_basic.go:107-160`，`WriteConfig` 在 `:94`）。

**启动即回写**：`remote-management.secret-key` 若是明文，启动时 bcrypt 后原地写回该 key（`internal/config/config_load.go:104-113`，用 `SaveConfigPreserveCommentsUpdateNestedScalar`，`config_yaml.go:86`）。**只读挂载 config.yaml 会让这一步静默失败**（返回值被 `_ =` 丢弃）。

**热加载：有。** fsnotify 同时 watch 配置文件与 auth 目录（`internal/watcher/events.go:30,36`，事件位 `:69,74`），150ms 去抖（`internal/watcher/watcher.go:86`、`config_reload.go:29-40`），用 SHA256 内容哈希去重避免自己回写触发循环（`config_reload.go:50-83`），失败则打 error 保留旧配置（`:88-92`）。热生效范围很宽，甚至包括 **management 路由本身**（secret-key 从空变非空即动态注册路由，反之关闭，`internal/api/server_reload.go:122-154`）。

#### ② 秘密

两套完全分开的存储：

- 静态上游 key **明文写 config.yaml**：`gemini-api-key` / `codex-api-key` / `xai-api-key` / `claude-api-key` / `vertex-api-key` / `openai-compatibility[].api-key-entries[].api-key`（`internal/config/config.go:107-150`、`config_types.go:601-650`）；下游客户端认证的 `api-keys` 同样明文（`internal/config/sdk_config.go:52`）。
- OAuth token 走 `auth-dir: "~/.cli-proxy-api"`（`config.example.yaml:35-36`，字段 `internal/config/config.go:35`；Docker 挂 `./auths:/root/.cli-proxy-api`，`docker-compose.yml:26`），一账号一 JSON，目录 0700 / 文件 0600（`sdk/auth/filestore.go:76,101,147`），文件名如 `claude-<email>.json`（`sdk/auth/claude.go:203`）、`kimi-<unixmilli>.json`（`sdk/auth/kimi.go:111`），内容是明文 `id_token`/`access_token`/`refresh_token`（`internal/auth/claude/token.go:20-26`）。
- 唯一被哈希的秘密是 management key（bcrypt，`config_load.go:100-113`、`config_validation.go:66-77`）。
- `.gitignore` 把 `config.yaml`、`.env`、`auths/*`、`static/*`、三个 store 目录全部排除（`.gitignore:6-8,22-27`）。
- 一个强化提示值得注意：`api-keys` 若仍是模板值（`your-api-key-1` 之类），**转发面直接被禁用**并返回警告 HTML 页（`internal/safemode/example_api_keys.go:40-65`、`cmd/server/main.go:594-599`）。

#### ③ 身份键——四套并存，是它最明显的设计债

| 层 | 身份 | 出处 |
|---|---|---|
| 运行期凭证 | `sha256(kind ‖ \0 ‖ parts…)[:12]` 内容哈希 | `internal/watcher/synthesizer/helpers.go:29-51` |
| 哈希碰撞消歧 | **YAML 数组顺序**（第 N 个加 `-N` 后缀） | `helpers.go:44-49` |
| 管理 API 定位 | **数组下标** `{"index": N}` / `?index=N` | `internal/api/handlers/management/config_lists.go:66-76,105,192,203-205,311,489` |
| OAuth 凭证 | auth-dir 下的相对文件路径（Windows 还 lowercase） | `internal/watcher/synthesizer/file.go:141-151,156` |
| provider 路由 | `openai-compatibility[].name` 小写加前缀 | `internal/util/provider.go:18-27`，用在 `synthesizer/config.go:283,320` |

各家参与哈希的字段：`gemini:apikey` = api-key + base-url（`synthesizer/config.go:73,86`）；`claude:apikey` 同（`config_apikey_disable.go:61`）；`openai-compatibility:<name>` = api-key + base-url + proxy-url（`synthesizer/config.go:293-294`）；`vertex:apikey` 同（`config_apikey_disable.go:85`）。

含义：**改 api-key 或 base-url 就等于换了一个凭证**（旧的消失、新的出现，运行期 cooldown / 统计全丢）；改 weight / priority / prefix / excluded-models 不换身份；**调换两条相同条目的 YAML 顺序会让它们的运行期状态互换**；并发改配置 + 按 index 定位是经典 TOCTOU 错位风险；改 `name` 会同时改变模型路由归属**和**该 provider 下所有凭证的 ID。它还把 `config_index`（YAML 数组下标）塞进 auth 的 attributes（`synthesizer/config.go:92,150,221,300,345,400`，常量 `sdk/cliproxy/auth/classification.go:19`），被 `internal/runtime/executor/openai_compat_executor.go:931` 读来反查 config 条目。

#### ④ 启动校验

仓规明确禁用 `log.Fatal`（`AGENTS.md`「Code Conventions」第 6 条），所以坏配置的表现是打一条 error 然后 `return` 出 main（`cmd/server/main.go:530-533`）——**退出码是 0**，Docker/systemd 的重启策略与健康判断会看到「正常退出」。

| 情况 | 行为 | 出处 |
|---|---|---|
| 文件不存在 / 读不了 | 硬失败退出 | `config_load.go:34-43` → `main.go:530` |
| YAML 语法错 | 硬失败退出 | `config_load.go:78-87` |
| **字段拼错** | **完全静默忽略**（未启用 `KnownFields`，全仓 grep 零命中） | `config_load.go:78` |
| weight 非法 | 硬失败，错误带下标 | `weight.go:110-141`、`config_load.go:93-95`；另有 unmarshal 前的节点级预检 `weight.go:22-51` |
| credential-in-flight / codex live-media-relay 非法 | 硬失败 | `config_load.go:88-92` |
| plugins 目录解析失败且 enabled | 硬失败；enabled=false 时忽略 | `config_load.go:141-144` |
| `codex-api-key` / `xai-api-key` 缺 `base-url` | **静默丢弃该条，无日志** | `config_normalization.go:129-165` |
| `gemini-api-key` 缺 `api-key` | 静默丢弃该条 | `config_normalization.go:180-186` |
| payload raw 规则 JSON 非法 | 丢弃该条 + warn | `config_validation.go:20-51` |
| auth-dir 不存在 | 自动 MkdirAll + Info | `sdk/cliproxy/service_lifecycle.go:353-362` |
| auth-dir 是文件 | 启动失败 | `service_lifecycle.go:365-367` |
| 单个 auth JSON 损坏 | 跳过该文件 + warn，继续起 | `synthesizer/file.go:51-58` |
| `api-keys` 是模板值 | **起得来但转发面禁用** | `safemode/example_api_keys.go:40`、`main.go:594-599` |
| `DEPLOY=cloud` 且配置缺失 | 降级为空配置待命，不起 API server | `config_load.go:36-51,79-84`；`main.go:539-559`；`internal/cmd/run.go:110-120` |
| **热加载时**配置写坏 | 打 error，**保留旧配置继续跑** | `internal/watcher/config_reload.go:88-92` |

#### ⑤ 反向路径

不适用「从 DB 导出」（无 DB）。但有几条相关路径：

- `GET /v0/management/config.yaml` 原样返回文件字节，注释与格式全保留（`config_basic.go:163-180`）。
- `GET /v0/management/config` 返回结构化 JSON（`config_basic.go:26-31`）。`RemoteManagement` 整块打了 `json:"-"`（`internal/config/config.go:29`），secret-key 不外泄；但 `api-keys` 与各家上游 key 有 json tag，**会明文出现在响应里**。
- `GET /v0/management/auth-files/download?name=x.json` 原样下载 OAuth token 文件（`auth_files_crud.go:26-48`）。
- bootstrap 方向：`config.example.yaml` 复制成 config.yaml（`main.go:392-402,455-466,496-509`）；`Dockerfile:26` 只塞 example，不塞 config.yaml。
- **git store 在配置变更后 commit + push**（`internal/store/gitstore.go:1796-1818`，`commitAndPushLocked("Update config", rel)`），由 watcher 的 `persistConfigAsync()` 触发（`config_reload.go:82`）——**跑起来的实例把自己的配置改动 push 回 git 仓库**，是五个仓库里最接近 GitOps 的形态。

#### ⑥ 无 UI

**它本来就没有内置 Web UI。** 全仓只有三处 `go:embed`，都是模型目录与提示词，没有任何前端资源被 embed（`internal/misc/claude_code_instructions.go:12`、`internal/registry/codex_client_models.go:15`、`internal/registry/model_updater.go:27`）。唯一 UI 路由 `GET /management.html`（`internal/api/server_routes.go:54`）从磁盘 `静态目录/management.html` 直接 `c.File()`，文件不存在就当场去 GitHub 拉（`server_management.go:284-312`），资源来自 `https://api.github.com/repos/router-for-me/Cli-Proxy-API-Management-Center/releases/latest`，兜底 `https://cpamc.router-for.me/`（`internal/managementasset/updater.go:28-29`），后台每 3 小时自动更新（`:33,74-101`）。

三个开关（`internal/config/config_types.go:182-195`）：`disable-control-panel: true` → `/management.html` 404 且更新器跳过（`server_management.go:286-289`、`updater.go:107-109`）；`disable-auto-update-panel: true` → 只首次访问下载（`updater.go:110-112`）；`secret-key: ""` → **整个 `/v0/management/*` 全部 404，管理路由压根不注册**（`internal/api/server.go:235-241`、`server_management.go:190-200`；`config.example.yaml:22-23` 明确写了这点），同时 usage 队列也跟着关（`server.go:236`）。

**一个需要留意的后门**：`MANAGEMENT_PASSWORD` env 非空会**强制打开**管理路由，即使 config 里 secret-key 为空（`internal/api/server.go:166-168,235`；`server_reload.go:127-133`）。

TUI 是显式 opt-in 的第三形态：`-tui` / `-tui -standalone`（`cmd/server/main.go:109-110`，`internal/tui/`），默认不开。

「只跑转发」的最小配置是：`secret-key: ""` + `disable-control-panel: true` + 不设 `MANAGEMENT_PASSWORD`。此时进程只暴露转发路由，不下载外部资源，也不回写 config.yaml（bcrypt 那步因 secret-key 为空而不触发）。

#### 额外：无 DB 时运行期状态放哪（本票对 CLIProxyAPI 的重点追问）

**凭证摘除 / 禁用 / 冷却，三种落点各不相同：**

1. **人工禁用 OAuth 凭证** → 写进 auth JSON 的 `metadata.disabled`，重启保留（`sdk/auth/filestore.go:112-115`，读回 `synthesizer/file.go:172-176`）。
2. **人工禁用 config 里的 api-key 条目** → 没有 `disabled` 字段可用，做法是往该条目的 `excluded-models` 里塞一个 `"*"`，然后**回写 config.yaml**（`internal/api/handlers/management/config_apikey_disable.go:12-30,44-50`，落盘 `auth_files_fields.go:77-92`）。**运行期状态借配置文件当数据库**，这是无 DB 架构最直接的代价。
3. **401/429 自动冷却** → 默认纯内存。关键证据是 auth store 对 config 合成凭证的硬编码排除：

   ```go
   if IsConfigAPIKeyAuth(auth) {
       return nil
   }
   ```
   `sdk/cliproxy/auth/conductor_lifecycle.go:258-260`（`runtime_only` 与 plugin virtual auth 同理，`:261-268`）。冷却字段 `Unavailable` / `NextRetryAfter` / `Quota` / `ModelState` 全在内存结构上（`sdk/cliproxy/auth/types.go:62-92,181-192`）。

   **唯一的落盘开关是 `save-cooldown-status`，默认 false**（`internal/config/config.go:68-69`，默认值 `config_load.go:73`）。打开后每个 auth 一个 `.cds` 文件写在 auth-dir，`CreateTemp` + `Rename` 原子替换（`sdk/cliproxy/auth/cooldown_state.go:136-227`），记录 provider/auth_id/model/status/next_retry_after/reason/quota/last_error/updated_at（`:18-29`，构造 `conductor_cooldown.go:641-675`），**只保存还没过期的**（`:642`），启动时 `RestoreCooldownStates` 恢复（`service_lifecycle.go:81-86`，实现 `conductor_cooldown.go:271-323`），与集群模式互斥（`service_auth.go:384`）。Postgres 后端有对应的 cooldown 表（`internal/store/postgres_cooldown_store.go`，DDL `postgresstore.go:150-163`，`PRIMARY KEY (auth_id, model)`）。

   **即：默认配置下，一个 key 被 429 冷却 30 分钟，重启进程立刻清零重新去撞上游。**

**用量 / 统计，两套都不落盘：**

- 每凭证的成功/失败计数 = 固定长度环形缓冲，20 个桶 × 每桶 10 分钟 ≈ 3.3 小时窗口（`sdk/cliproxy/auth/types.go:147-148,157-159`），字段声明为 ``recentRequests recentRequestRing `json:"-"` ``（`types.go:99`）——**显式排除在序列化之外，任何持久化路径都带不走它**。读出走 `GET /v0/management/api-key-usage`（`internal/api/handlers/management/api_key_usage.go:56-80`）。
- 明细事件队列 = 内存 slice + head 指针的伪 ring（`internal/redisqueue/queue.go:19-36,127,226`），`redis-usage-queue-retention-seconds` 默认 60 秒、上限 3600（`queue.go:10-11`、`internal/config/config.go:60-63`、`config_load.go:132-138`）。消费是**破坏性 pop**：`GET /v0/management/usage-queue?count=N`（`usage.go:23-42`，`PopOldest` 在 `queue.go:88`），另暴露一个 RESP 端口给外部采集器（`internal/api/redis_queue_protocol.go`）。默认 `usage-statistics-enabled: false`（`config_load.go:69`）。
- README 把这个取舍写明了：`README.md:143` —— *Since v6.10.0, CLIProxyAPI and CPAMC no longer ship built-in usage statistics.*；官方推荐**外接一个带 SQLite 的第三方管理端**消费这个内存队列（`README.md:151`：*persists events in SQLite*）。

### 2.3 new-api——DB + UI 的同构对照组，无任何文件/env 预置口子

（本节事实由子 agent 直读副本取回，行号均相对 `new-api` 根。）

#### ① 事实源

DB 是业务配置唯一事实源；**无配置文件层**；env 与 DB 基本不相交；管理端只写 DB，不回写文件。

- 唯一文件型配置是 `.env`：`main.go:287` `godotenv.Load(".env")`，失败仅 debug 打日志。**全仓无 viper / `config.yaml` / `ReadInConfig`**；`yaml` 依赖只被 i18n 用（`i18n/i18n.go:40`）。`.env.example` 全 120 行**无任何渠道/模型项**。
- 渠道/令牌/模型元数据都是 GORM 表：`model/channel.go:23-58`、`model/token.go:17`、`model/model_meta.go:26`。站点配置走 `options` 表（`model/option.go:18-21`）。
- 启动序：`common.InitEnv()`（`main.go:295`）→ `model.InitDB()`（`:307`）→ `model.InitOptionMap()`（`:325`）。`InitOptionMap`（`model/option.go:30`）先用编译期 Go 变量填内存 `OptionMap`（`:35-183`，144 个 key），末尾 `loadOptionsFromDatabase()`（`:190`）用 DB 行覆盖。**优先级 = Go 默认值 < DB 行，env 不参与。** 覆盖持续：`SyncOptions`（`:200-207`）每 `SYNC_FREQUENCY` 秒重读整表。
- 结构化设置以 `name.field` 为 option key 存 DB（`setting/config/config.go:42-69` LoadFromDB、`:71-91` SaveToDB）。管理端 `PUT /api/option` → `controller/option.go:124` → `model.UpdateOption`（`model/option.go:218-236`）FirstOrCreate + Save + 更新内存，**无文件写回**。
- **唯一 env-覆盖-DB 的例外**：`setting/operation_setting/monitor_setting.go:34-42`，`GetMonitorSetting()` 每次读取时用 `CHANNEL_TEST_FREQUENCY` / `CHANNEL_TEST_ENABLED` 覆盖 DB，**env 永远赢**。其余 env（`common/init.go:87-136`、`:176-227`）设置的变量不在 `OptionMap` 里，管理端改不了；反之 `OptionMap` 的 key（如 `RetryTimes`，`common/constants.go:133`）无 env 入口。

#### ② 秘密

上游渠道 key 在 DB 里**明文**：`model/channel.go:26` ``Key string `gorm:"not null"` `` —— 普通字符串列，无 Serializer、无加密 hook。`common/` `model/` `service/` 下 grep `encrypt|aes|cipher` 零命中；`common/crypto.go` 只有 HMAC-SHA256（`:11-21`）与 bcrypt（`:23-31`）。明文可原样读回：`controller/channel.go:421-454` `GetChannelKey`（RootAuth + 2FA + 审计，`router/channel-router.go:23-29`），列表接口默认 `Omit("key")`（`model/channel.go:373,383,407,428,934,1126`）。

env 侧秘密只有基础设施类：`SQL_DSN`/`LOG_SQL_DSN`（`model/main.go:128,171,213`）、`SQLITE_PATH`（`common/init.go:69-71`）、`REDIS_CONN_STRING`（`common/redis.go:25`）、`SESSION_SECRET`（`common/init.go:50-59`）、`CRYPTO_SECRET`（`:60-64`），后两者默认启动时随机 uuid（`common/constants.go:35-36`）。`GENERATE_DEFAULT_TOKEN`（`common/init.go:194`）只是 bool，注册时发**随机**下游令牌（`controller/user.go:291-316`），不能预置 key 值。

#### ③ 身份键

渠道身份是**自增 id**，`name` 无唯一约束（`model/channel.go:24` Id 为 GORM 自增主键；`:30` Name 只有普通 index，非 unique）。`BatchInsertChannels`（`model/channel.go:436-463`）用裸 `tx.Create(&chunk)`，**无 `clause.OnConflict`**——重复执行不断新建重复渠道。派生表 `abilities` 才有自然键（`model/ability.go:19-21` 复合主键 (group, model, channel_id)，写入 `OnConflict{DoNothing}`，`:229`、`:295`），但它是 channel 的投影，非配置入口。

仓内**确有**按自然键幂等 upsert 的范式，只是没用在渠道上：`service/authz/seed.go:11-34`（按 key 列 `OnConflict DoUpdates`）、`model/option.go:227` FirstOrCreate、`model/system_instance.go:46-70`。唯一「复制渠道」是库内克隆 `POST /api/channel/copy/:id`（`controller/channel.go:1405`），仍生成新自增 id。

#### ④ 启动校验

混合：少数硬 `os.Exit(1)`（`common.FatalLog` 即 `os.Exit(1)`，`common/sys_log.go:31-37`），多数「打日志 + 默认值继续」，个别静默吞。

- 硬失败：`SESSION_SECRET` 照抄示例值（`common/init.go:50-58`）；`SESSION_COOKIE_*` 组合非法（`common/session_cookie.go:43-84` → `common/init.go:65-67`）；log-dir 建不出（`:72-84`）；DB 连不上/迁移失败（`model/main.go:171-201` → `main.go:307-311`）；ClickHouse DSN 用作主库（`model/main.go:126-129`）；`TRUSTED_PROXIES` 非法（`middleware/trusted_proxies.go:21-50` → `main.go:175-178`）；`CHANNEL_UPDATE_FREQUENCY` 非数字（`main.go:117-123`，唯一 fatal 的频率变量）；MySQL 字符集不支持中文（`model/main.go:177-181` panic）。
- 软失败：`common/env.go:9-38` 的 `GetEnvOrDefault{,String,Bool}` 解析失败一律 SysError + 默认值（绝大多数 env 走这条）；`common/init.go:167-174` 非正数告警 + 默认，`:155-164` 会话窗口自动钳制；`monitor_setting.go:36-38,44-47` 解析失败**完全静默**；`SQL_DSN` 未知前缀**兜底当 MySQL**（`model/main.go:145-157`），空则退 SQLite（`:160-163`）；DB 里的坏 option 基本不校验——`validateOptionValue`（`model/option.go:208-216`）只覆盖 2 个 key，`updateOptionMap`（`:275+`）用 `strconv.Atoi` 丢弃 error，坏值静默变 0。

#### ⑤ 反向路径

**没有。** 既无渠道导出，也无渠道文件导入。`router/channel-router.go:39-78` 全部 39 条 `/api/channel/` 路由逐条看过，无 import/export/dump；`docs/openapi/api.json` 里 22 个 `/api/channel*` 同样没有。三个名字像批量的都不是文件路径：`POST /api/channel/` 的 `mode:"batch"`（`controller/channel.go:612-714`，请求体 `:573-578`）是「一个渠道模板 + key 按 `\n` 切开，一行一个渠道」（`:659-672`、`:683-698`）；`POST /api/channel/batch`（`router/channel-router.go:59`）是批量**删除**；`/batch/tag` 是批量打 tag。`DataExportEnabled` / `/api/data/*` 是用量看板统计导出（`controller/misc.go:85-86`），与配置无关。

#### ⑥ 无 UI

**不存在。** `main.go:42-46` 的 `//go:embed web/dist` 与 `//go:embed web/dist/index.html` 是包级无条件的编译期硬依赖，index.html 缺失**直接编译失败**（`makefile:42-43` 注释亦点明）。全仓 `//go:build` 只有 `common/system_monitor_{windows,unix}.go` 两条，与前端无关。`router/main.go:15-33` 的 SetApiRouter / SetDashboardRouter / SetRelayRouter / SetVideoRouter **无条件注册**；只有静态资源可切——`FRONTEND_BASE_URL` 非空且非 master 时不挂 SetWebRouter，改 301 重定向，**二进制内资源仍在**。`NODE_TYPE=slave`（`common/init.go:89`）只关 DB 迁移（`model/main.go:197,241`）、RBAC seeding（`service/authz/enforcer.go:34,57`）与定时任务，**管理 API 一条不少**。`Dockerfile:1-41` 产出唯一二进制。

#### ⑦ 关键否定事实：有没有预置渠道的口子

**没有。** 渠道只能由已登录管理员调 HTTP API（即 React 管理端）写进 DB。逐条查绝：

1. **env**：全仓 40 处 `os.Getenv` 逐条看过，与 channel/key/token 沾边的只有 3 个频率开关（`CHANNEL_UPDATE_FREQUENCY`（`main.go:117`）、`CHANNEL_TEST_FREQUENCY`、`CHANNEL_TEST_ENABLED`）。grep `INITIAL_` / `ROOT_TOKEN` / `ADMIN_PASSWORD` / `ROOT_PASSWORD` / `DEFAULT_CHANNEL` / `CHANNEL_KEY`：**零命中**。
2. **种子数据**：`InitDB`（`model/main.go:171-201`）只做 chooseDB + migrateDB，不插业务行。`createRootAccountIfNeed`（`model/main.go:57-75`，硬编码 root/123456）**已是死代码，全仓无调用点**；取而代之是 HTTP 安装向导 `POST /api/setup`（`router/api-router.go:23`、`controller/setup.go:46-148`），**必须走 HTTP，无 env 旁路**。`model/channel.go` 无 `init()`、无默认渠道常量。
3. **启动期唯一代码种子是 RBAC**：`service/authz/seed.go:11-34` + `:44-60`，仅 master 跑；另有编译期定价默认表（`model/pricing_default.go`、`setting/ratio_setting/model_ratio.go:336-345`）。均非渠道。
4. **批量导入**：`AddChannel`（`controller/channel.go:612`）只收 4 个字段（`:573-578`），mode 只有 single / batch / multi_to_single（`:631-681`）。全部 channel 路由在 AdminAuth + RequirePermission 之下（`router/channel-router.go:20-36`），敏感写还要 ChannelSensitiveWrite。
5. **部署清单**：`docker-compose.yml:28-53` env 清单无一涉及渠道；Dockerfile 无业务 ENV、无种子脚本；`.env.example` 无渠道项。

### 2.4 sub2api——DB + UI，无 UI 那一档是编译期 build tag

（注意：它用的是 **PostgreSQL**，不是 SQLite。）

#### ① 事实源

业务配置（provider/渠道、账号池、API Key、分组）**只落 DB**；`config.yaml` 是纯进程级 + 全局策略层，不含任何单条业务实体。

- 业务实体全在 ent schema / SQL 迁移里：`backend/ent/schema/account.go:28`、`backend/ent/schema/api_key.go:34`、`backend/ent/schema/group.go:34`、`backend/ent/schema/proxy.go:32`、`backend/migrations/081_create_channels.sql:8`；accounts 建表 DDL 在 `backend/migrations/001_init.sql:60-77`（`id BIGSERIAL PRIMARY KEY`）。
- `deploy/config.example.yaml` 的顶层键全表（server / webauthn / run_mode / cors / security / gateway / log / sora / token_refresh / … / image_storage）**没有 accounts/channels/keys/model-mapping 之类的实体列表**。`default:` 段（`deploy/config.example.yaml:1083-1112`）只是新建实体的默认值（`user_concurrency`、`api_key_prefix: "sk-"`、`rate_multiplier`），不是实体本身。
- 运行时可变设置走 DB 的 `settings` KV 表（`backend/ent/schema/setting.go:32-38`）。**DB 覆盖文件**有明确注释：`backend/internal/service/setting_oauth.go:474`「若对应系统设置键存在，则覆盖 config.yaml/env 的值；否则回退到 config.yaml/env」；`backend/internal/service/image_storage_settings.go:57`「fallback 是 config.yaml 里的配置」。
- **文件被写回的唯一路径是安装向导**：`backend/internal/setup/setup.go:462-527` 的 `writeConfigFile()`，只序列化 server/database/redis/jwt/default/rate_limit/timezone 七段，`os.WriteFile(GetConfigFilePath(), data, 0600)`；由 `Install()`（`:300,335`）或 `AutoSetupFromEnv()` 调用，安装后 `.installed` 锁文件阻止重跑（`:349-352, 162-179`）。管理端的 setting handler 只写 DB（`backend/internal/handler/admin/setting_handler_update.go`），`grep GetConfigFilePath` 在 `backend/internal` 非测试代码里只命中 setup 包。

#### ② 秘密

上游凭证只能 UI/HTTP API 录入进 DB，**明文 JSONB 存储，没有加密**：

```go
field.JSON("credentials", map[string]any{}).
    Default(func() map[string]any { return map[string]any{} }).
    SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
```
`backend/ent/schema/account.go:79-81`；写库路径无加密（`backend/internal/repository/account_repo.go:110`、`:472`）。`001_init.sql:64` 的注释写着「凭证信息（加密存储）」，**与实现不符**——迁移里 grep `encrypt` 只命中 `044_add_user_totp.sql` 和 `125_add_channel_monitors.sql`。

加密器只服务四类秘密：TOTP 密钥（`backend/internal/service/totp_service.go:50-51`）、渠道监控模板的 API Key（`channel_monitor_service.go:67,82`）、备份 S3 SecretAccessKey（`backup_service.go:122`）、图片存储 / Prompt Audit 配置（`image_storage_settings.go:53`、`backend/internal/securityaudit/prompt_config_store.go:31`）。实现 `backend/internal/repository/aes_encryptor.go:16-59`（AES-256-GCM）。**密钥来源是 `totp.encryption_key`，未配置时每次启动随机生成并降级标记**（`backend/internal/config/config.go:1782-1793`），因此有专门守卫拒绝在自动生成密钥下持久化 S3 secret（`backup_service.go:40-49`，`ErrSecretEncryptionKeyNotConfigured`）。

env 机制（`backend/internal/config/config.go:1652-1665`）：viper `AutomaticEnv` + `SetEnvKeyReplacer(".", "_")`，**没有 `SetEnvPrefix`**，所以是裸 `DATABASE_PASSWORD` / `JWT_SECRET` / `TOTP_ENCRYPTION_KEY`（见 `deploy/docker-compose.yml:60-107`）。少数切片型键手工读 env（`config.go:1673-1677, 1681-1686`）。**`${VAR}` 插值未找到**——查过 `deploy/config.example.yaml`（grep `\${` 只命中注释）、`backend/internal/config/config.go`（无 `os.ExpandEnv`）、`backend/internal/setup/`。配置文件路径：`CONFIG_FILE` 直指，否则按 `DATA_DIR` → `/app/data` → `.` → `./config` → `/etc/sub2api` 顺序搜（`config.go:1829-1843`）。

**一条值得单列的工程约束**：`backend/internal/config/env_reachability_test.go:52-80` 的 `TestConfigKeysAreEnvReachable`——因为 `viper.Unmarshal` 只解码 `AllKeys()`（= SetDefault ∪ 配置文件已有键 ∪ BindEnv），`AutomaticEnv` **只能覆盖已注册的键、不能引入新键**，所以任何没注册默认值的字段在「无 config.yaml、纯 env 部署」下会被静默丢弃。这个 bug 真实发生过（image_storage 凭证丢失），他们用反射遍历 Config 结构体做门禁。

#### ③ 身份键

DB 内身份一律是**自增 `BIGSERIAL` int64 id**，无 uuid（`001_init.sql:61`；ent 侧全部 `field.Int64` 外键，`account.go:91`、`api_key.go:36`）。名字唯一性**只有 groups 和 channels 有**：`backend/migrations/016_soft_delete_partial_unique_indexes.sql:30-33`（`groups_name_unique_active ON groups(name) WHERE deleted_at IS NULL`，软删后可复用名字）、`backend/migrations/081_create_channels.sql:18`。accounts 的索引清单里没有 name 唯一（`001_init.sql:78-84`、`ent/schema/account.go:232-250`）；API Key 唯一的是 **key 本身**而非 name（`ent/schema/api_key.go:37-40`）。

导出文件里的身份键：proxy 用**自然键拼串** `fmt.Sprintf("%s|%s|%d|%s|%s", protocol, host, port, username, password)`（`backend/internal/handler/admin/account_data.go:96-98`），导入时按此键在 DB 里查已有代理，命中即复用（`:288-325`，`ProxyReused++`）；备用代理这一层引用按 **name** 跨实例反查（`:49, 337-350`，查不到降级 `fallback_mode=none` 并记 warning）。**account 则没有身份键**——`DataAccount` 结构（`:60-73`）只有 name/platform/type/credentials/…，无 id、无唯一键。

#### ④ 启动校验

**启动即拒（`log.Fatalf`）**，但只对能解析出来的错；**配置文件缺失是合法的**；**未知键静默忽略**。

- `backend/cmd/server/main.go:134-141`：`cfg, err := config.LoadForBootstrap()`，err 则 `log.Fatalf`；紧接着 `initializeApplication` 失败也是 `log.Fatalf`（`:151-154`）。
- 文件不存在不算错（`backend/internal/config/config.go:1666-1672`：只有非 `ConfigFileNotFoundError` 才返回 error）——即 **YAML 语法错 → 退出；文件缺失 → 继续**。
- 语义校验 `cfg.Validate()`（`:1801-1803` 调用，`:2475+` 定义）硬失败项包括 `server.read_header_timeout` 越界（`:2482`）、`jwt.secret` 为空或 < 32 字节（`:2525-2533`）、`oidc_connect.allowed_signing_algs` 缺失（`:2801`）。启动阶段对 jwt.secret 有显式豁免：`LoadForBootstrap` 用 32 个 `0` 顶替再校验，通过后还原为空（`:1647-1650, 1794-1807`）。
- 软失败（只 warn）：非法 `user_message_queue.mode` 清空（`:1774-1779`）、`run_mode` 非法值静默归为 `standard`（`:1629-1636`）、`security.url_allowlist.enabled=false` 只 warn（`:1808-1813`）、TOTP 密钥缺失只 warn（`:1790`）。
- **未知/拼错的键静默忽略**：`grep UnmarshalExact|ErrorUnused|DecoderConfigOption backend/internal/config/*.go` 无命中，用的是裸 `viper.Unmarshal(&cfg)`（`:1678`）。

#### ⑤ 反向路径

**有，但只是「账号 + 代理的备份 JSON」，不等价于声明式配置。**

- 端点：`backend/internal/server/routes/admin.go:393-394`（`GET /data` 走 step-up 二次认证，因为**明文回显凭证**——`account_data.go:53-55` 注释「故意不走 dto.Account 的脱敏路径，Credentials 原文返回」；`POST /data` 导入）。载荷格式 `account_data.go:20-24`（`type: "sub2api-data"`、`version: 1`）、`:27-36`、`:38-73`。
- **为什么不是声明式配置**：账号导入无条件新建、不查重、不更新（`:449` `CreateAccount`，失败只累计 `AccountFailed`，`:450-458`）——重复 POST 同一份文件会造出重复账号；**分组绑定不在备份里**（`:443` `GroupIDs: nil`，默认 `SkipDefaultGroupBind = true`，`:246-249`）；spark 影子账号被显式排除并注明「不做完整往返」（`:56-59, 115-131`）；没有删除/收敛语义，也不含 users/groups/channels/api_keys/settings。
- 其它口子都不是声明式配置：`POST /accounts/batch`（`routes/admin.go:391`，实现 `account_handler.go:1698-1815`，逐条 create 无去重）、`POST /accounts/import/codex-session`（`:359`）、`POST /accounts/sync/crs`（`:360-361`）。
- 全库备份是另一条线：`backend/internal/service/backup_service.go:54-58`（`DBDumper`）+ `:75-83`（S3/R2）+ `:88-95`（cron），`BackupType: "postgres"`（`:104`）——二进制 dump，不是人可编辑的配置。

#### ⑥ 无 UI

**有一档，编译期 build tag。** 双文件：`backend/internal/web/embed_on.go:1` `//go:build embed` + `:29` `//go:embed all:dist`；`backend/internal/web/embed_off.go:1` `//go:build !embed`，其 `Middleware()` / `ServeEmbeddedFrontend()` 直接返回 404 `"Frontend not embedded. Build with -tags embed to include frontend."`（`embed_off.go:32-44`），`HasEmbeddedFrontend()` 恒 false（`:46-48`）。

- 谁加 tag：`Dockerfile:94`、`.goreleaser.yaml:15`、`.goreleaser.simple.yaml:16`。**谁没加**：`backend/Makefile:6-7` 的 `build:` 目标——**仓库自带的 `make build` 产物就是「只跑转发面 + API、无 UI」的形态**。
- README 明写这是一档形态：`README_CN.md:524`「`-tags embed` 参数会将前端嵌入到二进制文件中。不使用此参数编译的程序将不包含前端界面。」（`README.md:510`、`README_JA.md:507` 同）。
- 前端产物直接落进后端包目录：`frontend/vite.config.ts:107` `outDir: '../backend/internal/web/dist'`；Dockerfile 三阶段把它拷进后端源树再编译（`Dockerfile:83`）。**没有独立静态站部署形态**——`deploy/Caddyfile:29` 只是 `reverse_proxy localhost:8080`。
- **无 UI 时配置怎么进来**：进程级可以完全靠 env——`AUTO_SETUP=true`（`backend/cmd/server/main.go:78-91` → `setup.AutoSetupFromEnv()`，`backend/internal/setup/setup.go:567-640`），`deploy/docker-compose.yml:10-11` 明说「All configuration is done via environment variables. No Setup Wizard needed」；另有 `--setup` 交互式终端向导（`main.go:60,70-75` → `backend/internal/setup/cli.go:49-78`）与 `SKIP_SETUP`（`setup.go:151-158`）。首启向导在无 embed 构建下退化为纯 REST，可以 curl 完成安装（`main.go:97-109`，`backend/internal/setup/handler.go:22-37`）。
- **但业务级（账号池/分组/key）没有任何 env/文件通道**：唯一入口是 `backend/internal/server/routes/admin.go:349-399` 这批需登录的 admin API，或直接写 Postgres。`run_mode: "simple"`（`deploy/config.example.yaml:95-101`，`backend/internal/config/config.go:22-23`）只是隐藏 SaaS 计费功能，不是无 UI 档。

### 2.5 opencodex——文件是事实源，UI 是文件的一个客户端

#### ① 事实源

单一文件事实源 `$OPENCODEX_HOME/config.json`（默认 `~/.opencodex/config.json`，`opencodex/src/config.ts:567-577`；没有 `--config` CLI flag，`ocx start` 只有 `--port`，`src/cli/registry.ts:23`）。

- **有 UI**：`structure/05_gui-and-management-api.md:5-6`——bundled React dashboard 打进 `gui/dist`，由同一个 Bun proxy 服务。
- **UI 回写同一个文件**：管理端路由直接调 `saveConfigPreservingClaudeCode(config)`（`src/server/management/config-routes.ts:312`、`:512`、`:555`；provider 删除同理 `provider-routes.ts:659-661`）。`docs-site/src/content/docs/reference/configuration.md:22`：「The dashboard, management API, and mutating CLI commands all persist to the same file.」
- **没有配置 DB**：全仓 `bun:sqlite` 命中 17 处，全部是锁与运行时状态——`src/config.ts:6`（跨进程配置写事务锁，`src/config.ts:2424-2440` `withConfigMutationLockSync`，`BEGIN IMMEDIATE` + `busy_timeout=0` 故意让请求路径立刻失败而不是卡住事件循环）、`src/codex/history-*.ts`、`src/routing/history/indexer.ts`、`src/storage/*`、`src/lab/*`。
- **没有 watch**：`grep -rn "fs.watch|watchFile|chokidar|FSWatcher" src gui/src` 零命中。运行中进程把配置常驻内存，文档明确警告：「A running process keeps configuration in memory, so a later live save can rewrite unrelated hand edits from its snapshot… Prefer those channels, or **stop the proxy before hand-editing**.」（`docs-site/src/content/docs/reference/configuration.md:22-25`）
- 有一套「协作写回填」补偿（不是热加载，是防覆盖）：`src/config.ts:2703-2736` 的注释解释 issue #488——任何 service-time save 会整对象序列化冲掉手改，于是用 `WeakMap` 记 baseline（`claudeCodeBaseline` / `liveConfigBaseline`，`:2726-2735`），只对 `claudeCode` 子树与 listener 绑定字段 rebase。**覆盖面不全**，文档自陈「that protection does not cover every subtree」。

#### ② 秘密

```ts
export function resolveEnvValue(value: string | undefined): string | undefined {
  if (!value) return undefined;
  const match = value.match(/^\$\{(\w+)\}$/);
  if (match) return process.env[match[1]];
  if (value.startsWith("$")) return process.env[value.slice(1)];
  return value;
}
```
`opencodex/src/config.ts:3123-3129`。只认两种全串形式，不支持拼接（`sk-${VAR}` 不行），不匹配就**原样返回**（明文 key 直接可用）。

**调用点全在使用侧而非加载侧**：`src/router.ts:245-248`（`usableResolvedApiKey`）、`src/providers/quota.ts`（十余处）、`src/server/responses/compact.ts:186`、`src/codex/catalog/provider-fetch.ts:1020`、`src/config.ts:3139`（`config.proxy`）。因此**文件里永不落秘密**。

`ocx init` 对 key 类 provider 留空时**默认生成 env 引用**：`src/cli/init.ts:72` `envKeyFor(id) = ${id.toUpperCase()}_API_KEY`，`:148` `...(p.kind === "key" ? { apiKey: apiKey || \`\${${env}}\` } : ...)`。key 池条目同样支持（`src/providers/api-keys.ts:22-24` `isEnvReference`，`:26-30` 掩码时 env 引用原样显示）。引用悬空只报 warning 不阻止启动（`src/cli/doctor.ts:336-374`）。

其它凭证不在 config.json：`docs-site/src/content/docs/reference/configuration.md:56-57`「OAuth and forward-provider tokens are stored in separate credential stores rather than in `config.json`」；Codex 池账号密钥在 `codex-accounts.json`（`.../configuration/providers.md:18`）；另有 `~/.opencodex/auth.json`（`src/config.ts:1988` 对其权限收紧）与 OS keyring（`@napi-rs/keyring`）。配置目录 `chmod 0700`（`src/config.ts:1554`），`ocx config export` 写文件用 `mode: 0o600`（`src/cli/config-command.ts:169`）。

#### ③ 身份键

**(a) provider = 对象 key 名，且 key 名本身有语义。** 形状 `providers: Record<string, OcxProviderConfig>`（`src/types.ts:619`）。key 名被拿去匹配内置注册表：`PROVIDER_REGISTRY.find(entry => entry.id === providerName)`（`src/router.ts:250-252`）——**改名 = 注册表 miss = 丢掉 authKind、endpoint、reasoning effort 映射、context window 元数据等一整套默认值**（`:253-280`）。另有若干处按字面 id 分支（`src/config.ts:1445-1452`、`src/router.ts:538`、`:604`）。

**没有 rename 接口**（`grep -rln "renameProvider" src` 零命中；管理端只有 POST 和 DELETE，`src/server/management/provider-routes.ts:627`）。手改 key 名的后果，仓库内部一个未暴露的重写器把危害写得很清楚：

```
// opencodex/src/providers/provider-id-rewrite.ts:17-32
 * Three shapes exist and the difference matters: routed model strings
 * ("<provider>/<model>"), bare provider ids (customModels[].provider,
 * combos[*].targets[].provider), and keys that ARE provider ids or routes
 * (providerContextCaps, claudeCode.desktopProfile.assignments). A rewrite
 * that handles only the first leaves an orphaned context cap and — worse — a
 * combo target naming a provider that no longer exists, which fails validation
 * in src/combos/types.ts and makes loadConfig discard the whole config.
```
且该函数**非事务性**（`:9-14`：碰到 collision 时前面的站点已改，调用方必须整份丢弃）。

DELETE 有引用完整性兜底（改名没有）：默认 provider 被删时自动改选第一个 enabled（`provider-routes.ts:635-644`），无可选则 409 `last_provider`，有 combo 依赖则 409 `provider_has_dependent_combos`（`:645-655`），再 `dropProviderCustomModels` + 清 context cap（`:660-662`）。

**(b) combo / routingProfile 也是对象 key，但有显式 rename 协议**：`POST` body 带 `renameFrom`（`src/server/management/combo-routes.ts:94-104` 校验、`:156` `delete nextCombos[renameFrom]`、`:164-168` 同步重写引用、`:218-220` 清选择状态与冷却）；routing profile 同理（`routing-profile-routes.ts:200`、`:292`）。

**(c) 数组类实体用显式 id 字段，从不用下标**：`apiKeys: OcxApiKeyEntry[]` = `{id, name, key, createdAt}`；加载时 `normalizeApiKeyIds` 先把所有显式 id 收进 `reserved` 再补合成 id，注释写明「An id the user already has is the one thing this repair must never take away」，重复 id 改成 `salvaged-N`（`src/config.ts:1848` 有对应 warning）。`apiKeyPool[]` 的 id 是**内容派生哈希** `sha256(key).slice(0,8)`（`src/providers/api-keys.ts:32-34`，注释「re-adding the same key upserts instead of duplicating」）。provider 名合法性约束见 `src/config.ts:670-675`（保留名含 `openai` / `combo` / `policy`）。

#### ④ 启动校验

**任何配置错误都不拒绝启动。** `loadConfig` 主干（`src/config.ts:1983-2038`）：

1. 文件不存在 → 默认配置（`:1989-1991`）。
2. 读入 → 剥 BOM → `JSON.parse` → 两个宽容化预处理（`:1993-1996`）。
3. `configSchema.safeParse` 成功 → 一串 `warnDegraded*` 打到 console（`:1999-2009`）。
4. 失败 → `{...defaults, ...parsed}` 且 providers 两侧合并，再 parse 一次（`:2013-2022`），成功则 `warnConfigRepaired`（`:3244`）。
5. 再失败 → `warnAndBackupInvalidConfig` + `return getDefaultConfig()`（`:2032-2036`），备份名 `config.json.invalid-<ISO时间戳>`（`:3490`）。

| 错法 | 行为 |
|---|---|
| JSON 语法错 | 备份 + 出厂默认起。**你的所有 provider 静默消失**，只剩默认 `openai` |
| 单字段类型/取值错 | 多数跳过坏条目继续。`streamMode` 非法回落 `"auto"`（`:1580`）；`retryOn429` 逐字段丢弃、全丢光则整个 policy 删掉（注释：「an empty policy would enable retries with defaults」，`:1644`）；`hostname` 不可绑回落 `127.0.0.1`（`:1731`）；`apiKeys` 不是数组则忽略整段（`:1820`）；`modelCosts` 坏行丢行（`:1713`）。都只 `console.warn` |
| **引用不存在的 provider** | **schema 硬失败**，merge-defaults 救不回来 → 落到第 5 步，整份配置被出厂默认顶替。`defaultProvider` 不存在（`src/config.ts:1462-1467`）；combo target 指向不存在的 provider（`src/combos/types.ts:283-287`，经 `:1487-1494` superRefine 汇入） |

校验是 zod（`src/config.ts:7` `import * as z from "zod/v4"`）+ 一层手写边界校验先跑短路（`validateConfigCandidate`，`:2222-2233`）。注意 `validateConfigCandidate` 比 `loadConfig` **严格**——不做 merge-defaults 挽救，直接返回错误；CLI 的 `set`/`import`/`validate` 走这条（`src/cli/config-command.ts:3`），**所以写入侧严、加载侧宽**。

#### ⑤ 反向路径

**有真正的状态导出**，四件套构成完整往返：`ocx config export <path|-> / import <path|-> --yes / validate / set`（`src/cli/config-command.ts:15-16`）。

- `export` = `JSON.stringify(readConfigDiagnostics().config, null, 2)`，`-` 走 stdout，落文件 `mode: 0o600`（`:163-171`）。**重要差别**：`show` / `get` 会 `redact(...)` 打码（`:114`、`:125`），**`export` 不打码**——导出物含明文密钥，这是它能被 `import` 吃回去的前提，也是它必须 0600 的原因。
- `import` = 校验后整份覆盖，强制 `--yes`（`:172-181`），提示语 `Imported config from X. Restart or run ocx sync if needed.`，再次印证无热加载。
- `validate` = 离线预检，可对任意路径的候选文件跑，不落盘、非 0 退出码（`:150-162`；对应 `src/config.ts:2221` 注释）。
- `set/unset` = 路径级增量写：clone → setPath → validate → saveConfig（`:128-147`）。

**一条方向相反、容易混淆的路径**：`ocx export` / `src/clients/config-export.ts` 是**给下游客户端生成指向本代理的配置**（OpenCode、Pi、Hermes 等），不是导出 opencodex 自身状态。它的两条不变量对本票有参考价值：

```
opencodex/src/clients/config-export.ts:11-14
 * - **No secret is ever serialized.** Configs carry only the client's documented env
 *   reference ({env:VAR} for OpenCode, $VAR for Pi); the real admission key travels
 *   through the environment. AGENTS.md treats token serialization as a release blocker.
```
且 `:20-21`「This module never writes a file. `destination` names the canonical path for a human」——从不代写用户的客户端配置，只打印路径让人自己合并（`src/cli/registry.ts:225`）。

**没有**「生成带注释的样例/模板配置」这类命令；`ocx init` 是交互式向导，直接把问答结果 `saveConfig` 成真配置（`src/cli/init.ts:174-179`）。

#### ⑥ 无 UI

**它有 UI，且没有任何「关掉 dashboard / 管理面」的配置开关或 CLI flag**（`grep -rn "no-gui|nogui|--headless|disable-gui|OPENCODEX_DISABLE" src bin scripts` 零命中）。三种事实上的无 UI 运行方式：

1. **`gui/dist` 缺失即静态资源不服务**：`src/server/gui-static.ts:108-113` 的 `serveGuiFile` 先 `findGuiDist()`，找不到返回 `null`，根路径退回 JSON 状态体 `rootFallbackPayload()`（`:133+`）。注意 npm 包**默认打包** `gui/dist`，所以这是「不构建」而非「配置关闭」。
2. **`unauthenticatedLoopbackListener` 第二监听器，只服务数据面**：`docs-site/src/content/docs/reference/configuration/server.md:93-94`——「The listener serves only `POST /v1/responses`, its WebSocket upgrade, `POST /v1/responses/compact`, and `GET /v1/models`. Everything else, including `/api/*` and the dashboard, returns `404`.」但它是**主监听器之外的附加 socket**（主监听器仍带 UI），且**完全无凭证**，文档挂了 `:::danger`「Do not enable it on a shared or multi-tenant host」（同文档 `:96-100`）。设计理由是直接 spawn 的 `codex app-server` 拿不到 token（`:70-76`）。
3. **管理面自行 fail-closed**：`structure/05_gui-and-management-api.md:25`——「A management credential that equals any configured data-plane credential does not enable management access. **The data plane may continue to start, but `/api/*` remains closed.**」凭证文件权限不达标同样 503（`docs-site/.../management-api.md:22-24`）。

「无 UI 操作」在 opencodex 里是**一等公民但走 CLI**：`docs-site/.../management-api.md:6-8`「The dashboard … is one client of it; **headless** `ocx` provider, model, combo, account, settings, diagnostics, and lifecycle commands are clients too.」`:278`「For headless hosts and automation, use the corresponding `ocx` commands: they call this API.」——CLI 是管理 API 的客户端，不直接改文件；`ocx config set/import` 是例外，它直接 `saveConfig`。

#### 附：配置文件全貌

**仓库里没有 checked-in 的样例 `config.json`。** 最接近的真实样本是 devlog 证据文件：`devlog/_fin/260717_openai_single_provider_option/evidence/020_desktop_en_pool_config.json`（及同目录 `020_mobile_ko_pool_config.json`、`020_desktop_de_direct_config.json`、`020_mobile_zh_disabled_config.json`）。权威定义：形状 `src/types.ts:616`（`OcxConfig`，约 670 行到 `:1290`），加载器 `src/config.ts`，出厂默认 `getDefaultConfig()` 在 `src/config.ts:3097-3120`，维护者视角分组表 `structure/02_config-and-codex-home.md:137-153`。

**扁平单层 JSON，约 70 个顶层键，没有 `version` 字段**（迁移靠 `openaiProviderTierVersion` 这类一次性 marker）。按 `structure/02_config-and-codex-home.md:142-150` 的分组：

```
监听      port, hostname, unauthenticatedLoopbackListener{enabled,port}, corsAllowOrigins
路由      providers{<name>: OcxProviderConfig}, defaultProvider,
          combos{<id>:{alias,targets[{provider,model,weight}],strategy,...}},
          routingProfiles{<id>:{alias,require,optimize,limits,candidates[...]}}
目录      disabledModels[], customModels[], providerContextCaps{<provider>:number}, ...
子代理    subagentModels[], subagentModelFallback[], injectionModel, effortCap, ...
传输      streamMode, stallTimeoutSec, connectTimeoutMs, proxy, websockets, ...
凭证      apiKeys[{id,name,key,createdAt}]        # 数据面入站 key，与上游 key 不同
Codex池   codexAccounts[], codexAccountPriorities{}, accountPoolStrategy, tokenGuardian, ...
生命周期  codexAutoStart, storageCleanupPolicy, cacheRetention, ...
子系统    claudeCode{}, clientIntegrations{}, images{}, search{}, compatibility{}, ...
迁移标记  openaiProviderTierVersion, googleAntigravityStaticCatalogVersion, ...
```

单个 provider（`OcxProviderConfig`，`src/types.ts:1289+`）主要字段：`adapter`、`baseUrl`、`responsesPath`、`authMode`（`key`/`oauth`/`forward`）、`apiKey`、`apiKeyTransport`、`apiKeyPool[{id,key,label,addedAt}]`、`disabled`、`defaultModel`、`selectedModels`、`liveModels`、`modelCosts{}`、`retryOn429{}`、`defaultMaxOutputTokens`/`modelMaxOutputTokens`、`modelContextWindows`、各类 reasoning-effort 映射、`codexAccountMode`（仅 `openai` 合法）。

---

## 3. 对 portage 的直接启示

**只列可迁移的事实与代价，不替 PO 做选择。** portage 侧的现状引用均指本工作树。

### 3.1 portage 现在的形状（供对照，非结论）

- 启动配置只覆盖进程级：`internal/config/config.go` 的 `Config` 结构体（listen / db_path / log_bodies / retry / concurrency_queue / admin_password / rate_limit / default_max_tokens），头部注释自陈「业务配置（渠道 / 纳管模型 / 接入点 / 候选 / 凭证）全部落 DB，不在这里」（`internal/config/config.go:1-3`）。文件缺失是合法的，走 `Default()` + `applyEnv`（`internal/config/config.go:89-96`）；YAML 语法错则 `return Config{}, err`（`:101-103`）。env 只有 `PORTAGE_ADMIN_PASSWORD` 一项（`:145-150`）。
- 编译期无 UI 已经通了：`internal/webui/embed.go:1` `//go:build webui` vs `internal/webui/stub.go:1` `//go:build !webui`（后者 `files()` 恒返回 `nil, false`）。
- 运行期唯一的无 UI 配置入口是 `scripts/seed-example.sql`，其头部注释自陈「平时不该走这条路」，兼着「不带 UI 的部署 / 自动化建库 / 忘密码救急」三件事（`scripts/seed-example.sql:1-6`）。

### 3.2 身份键：portage 的起点比四个对照仓都好

portage 的 SQLite schema **已经有一套自然键**，这是做声明式配置最贵的那部分：

| 实体 | 自然键 | 出处 |
|---|---|---|
| 渠道 | `name TEXT NOT NULL UNIQUE` | `internal/store/schema.sql:3` |
| 接入点 | `model TEXT NOT NULL UNIQUE` | `internal/store/schema.sql:50` |
| 纳管模型 | `UNIQUE(channel_id, upstream_model)` | `internal/store/schema.sql:69` |
| 候选 | `UNIQUE(access_point_id, channel_model_id)` | `internal/store/schema.sql:77` |
| API Key | `name TEXT NOT NULL UNIQUE` | `internal/store/schema.sql:82` |
| 上游凭证 | 渠道内唯一（靠 `idx_channel_keys_name` 索引而非表内约束） | `internal/store/schema.sql:36-39`、`internal/store/store.go:369` |

对照组的代价：

- **litellm** 用内容哈希当身份，且 hash 输入是**解析后的明文 api_key**（`litellm/router.py:7493-7502`）——写 `os.environ/XXX` 也挡不住「轮换 key 即换实体」，旧实体上的 cooldown 与用量关联全断。
- **CLIProxyAPI** 四套身份并存，还靠 **YAML 数组顺序**给哈希碰撞消歧（`internal/watcher/synthesizer/helpers.go:44-49`），管理 API 又用**数组下标**定位（`config_lists.go:66-76`）。
- **new-api** 渠道是自增 id、`name` 非 unique、`BatchInsertChannels` 无 `OnConflict`（`model/channel.go:436-463`）——重复执行只会不断新建重复渠道。它自己仓里**有**按自然键 upsert 的范式（`service/authz/seed.go:11-34`），只是没用在渠道上。
- **sub2api** 的导出物里 account **根本没有身份键**（`backend/internal/handler/admin/account_data.go:60-73`），导致 import 只能 append 不能 apply。
- **opencodex** 的 provider 身份是对象 key 名且 key 名参与注册表匹配，**改名静默降级、无 rename 接口**（`src/router.ts:250-252`、`src/providers/provider-id-rewrite.ts:17-32`）；它给 combo/routingProfile 补了 `renameFrom` 协议，provider 一直没补。

**代价提示**：自然键存在不等于改名安全。portage 的渠道名同时是**限定名的一半**（`CONTEXT.md`「限定名」= `渠道名/纳管模型名`），声明文件里按 name 做身份时，改名等价于「删旧建新 + 所有引用它的限定名失效」，与 litellm 改 `model_name` 的后果同构。

### 3.3 配置与运行期状态在 portage 是**同一张表**——这是最直接的冲突面

portage 的表里，配置列与运行期可变列混在一起：

- `channel_keys.disabled` / `disabled_reason` / `disabled_at`——**仅 401 自动摘除，只人工恢复**（`internal/store/schema.sql:42-45`）。
- `candidates.weight`——`weight=0` 即临时摘除（`internal/store/schema.sql:76`，`CONTEXT.md`「候选」）。
- `channels.disabled`、`access_points.disabled`、`channel_models.disabled`、`api_keys.disabled`。
- `api_keys.key_plain`——v0.47 起明文回读（`internal/store/schema.sql:84-93`）。
- `settings` 表的 `admin_password_hash`，注释自陈「单独一张 kv 表而不是往 config.yaml 里回写：口径层 §2.7 要求密码登录后可改，改到哪儿就得存到哪儿，而配置文件在容器里是只读挂载的」（`internal/store/schema.sql:98-101`）。

三个对照组各自付了不同的账：

- **CLIProxyAPI（无 DB）**：人工禁用 config 里的 api-key 条目，做法是往 `excluded-models` 塞 `"*"` **并回写 config.yaml**（`internal/api/handlers/management/config_apikey_disable.go:12-30`）——**运行期状态借配置文件当数据库**。自动冷却则对 config 合成的凭证**硬编码不持久化**（`sdk/cliproxy/auth/conductor_lifecycle.go:258-260`），默认重启即清零（落盘开关 `save-cooldown-status` 默认 false，`internal/config/config.go:68-69`）。用量是 3.3 小时环形缓冲且 `json:"-"` 不可序列化（`sdk/cliproxy/auth/types.go:99,147-159`），官方 v6.10.0 起干脆移除内置统计、推荐外接带 SQLite 的第三方（`README.md:143,151`）。
- **opencodex（无 DB、有 UI 写文件）**：三条写通道共用一个 SQLite `BEGIN IMMEDIATE` 跨进程事务锁（`src/config.ts:2424-2440`），且因无 watch 而必须靠 baseline rebase 防止 live save 冲掉手改，**且补不全**（`src/config.ts:2703-2736`）。
- **CLIProxyAPI 的另一半代价**：config.yaml 是**双向**的——启动会为 bcrypt 改写（`internal/config/config_load.go:104-113`，只读挂载会让这步**静默失败**）、management 会改、禁用凭证会改、git store 还会 push 回远端（`internal/store/gitstore.go:1796-1818`）。要做真正的只读声明式部署，必须同时关掉 management（`secret-key: ""`），并接受「运行期禁用凭证」这个能力的丧失。

**给 #24 「配置文件写错了的失败模式」那一格的输入**：五仓在这一问上没有共识——litellm 四档混杂且**同一份坏配置在 `--config` 与 `CONFIG_FILE_PATH` 两种启动姿势下失败模式相反**（`proxy_server.py:769-780` vs `proxy_cli.py:802-803`）；CLIProxyAPI 硬失败**退出码是 0**（`cmd/server/main.go:530-533`）；sub2api 启动即拒但**未知键静默忽略**（裸 `viper.Unmarshal`，`backend/internal/config/config.go:1678`）；opencodex **任何错误都不拒绝启动**，最坏是备份原文件后拿出厂默认起（`src/config.ts:2032-2036`）；new-api 混合。**唯一在五仓里一致的一点是：没有一个仓库对「未知字段」报错**（CLIProxyAPI 未启 `KnownFields`、sub2api 未用 `ErrorUnused`、litellm `extra="allow"`）。

### 3.4 秘密进文件：两种成熟做法，代价不同

- **引用式（opencodex）**：`${VAR}` / `$VAR`，**在使用时解析而非加载时**（`src/config.ts:3123-3129`，调用点全在 `src/router.ts:245-248` 等使用侧）——文件里永不落秘密，`ocx init` 留空时**默认生成 env 引用**（`src/cli/init.ts:148`）。代价：引用悬空只在 doctor 里 warning（`src/cli/doctor.ts:336-374`），不阻止启动。
- **引用式（litellm）**：`os.environ/XXX` 是**白名单前缀**而非通用模板，统一入口只有 `_check_for_os_environ_vars`（`proxy_server.py:3239`），另有约二十处各自为政的重复实现。代价：解析在**加载时**且结果写回内存 config，于是 `get_config` 之后任何序列化都会吐出明文（`save_config` 的 yaml 分支正是这个坑，`proxy_server.py:3237`）；env 未设时**静默变 None**（`secret_managers/main.py:319-327`），报错点漂移。
- **明文式（CLIProxyAPI / new-api / sub2api）**：静态 key 明文进文件或 DB。CLIProxyAPI 靠 `.gitignore` 排除（`.gitignore:6-8,22-27`）与 auth 文件 0600（`sdk/auth/filestore.go:101,147`）兜底；new-api（`model/channel.go:26`）与 sub2api（`backend/ent/schema/account.go:79-81`）DB 里都是明文，sub2api 的迁移注释甚至写着「加密存储」与实现不符（`001_init.sql:64`）。
- **加密式的坑（litellm / sub2api 共有）**：加密密钥的来源与轮换是真问题。litellm 的 `LITELLM_SALT_KEY` 未设时**退化成 master key**（`encrypt_decrypt_utils.py:8-16`），UI 文案写「Can NOT CHANGE THIS ONCE SET」（`admin_ui_utils.py:78`）；sub2api 的 `totp.encryption_key` 未配置时**每次启动随机生成**（`backend/internal/config/config.go:1782-1793`），并因此炸过 S3 secret，最后加了「密钥未固定就拒绝持久化」的守卫（`backup_service.go:40-49`）。
- **一条与「导出」耦合的口径（opencodex）**：`export` **不打码**而 `show`/`get` 打码（`src/cli/config-command.ts:114,125` vs `:163-171`）——它把「可往返的声明式文件」与「可展示的诊断输出」当成两个东西。对照 portage 的既有口径「上游 key 只存服务端，错误回显严禁泄露上游 key 与 base_url」（`CLAUDE.md` 工程约定），以及 `api_keys.key_plain` 已经明文可回读（`internal/store/schema.sql:84`），这两条在「导出」这个动作上会正面相撞。

### 3.5 导出反向路径：五仓里只有一个做成了真往返

- **opencodex 是唯一的完整往返**：`export` / `import` / `validate` / `set` 四件套（`src/cli/config-command.ts:15-16`），`validate` 还能对任意路径的候选文件离线预检、不落盘、非 0 退出码（`:150-162`）。
- **litellm 只有单向 import**（`litellm-proxy models import <yaml>`，`client/cli/commands/models.py:433`），**无 export**；`/config/yaml` 是返回 `{"hello":"world"}` 的 mock（`proxy_server.py:14051-14073`）。
- **new-api 两个方向都没有**（39 条渠道路由逐条查过）。
- **sub2api 有导出但不是声明式配置**——append-only、账号无身份键、无更新/删除语义、不含分组绑定（`account_data.go:449`、`:443`、`:56-59`）。
- **CLIProxyAPI 不适用（无 DB），但 `GET /v0/management/config` 会明文吐出上游 key**（`internal/config/config.go:29` 只给 `RemoteManagement` 打了 `json:"-"`）。

**代价提示**：sub2api 那条「导出物含明文凭证，所以导出端点要 step-up 二次认证」的做法（`backend/internal/server/routes/admin.go:393`、`account_data.go:53-55`）与 opencodex 那条「export 不打码所以文件必须 0600」（`src/cli/config-command.ts:169`）是同一个约束的两种收场。

### 3.6 无 UI 形态：portage 的编译期开关已经是五仓里最干净的一档

- **sub2api 与 portage 同构**：`//go:build embed` / `//go:build !embed` 双文件（`backend/internal/web/embed_on.go:1`、`embed_off.go:1`），对照 portage 的 `internal/webui/embed.go:1` / `stub.go:1`。sub2api 的 `make build` 默认**不加** tag（`backend/Makefile:6-7`），portage 的 `Makefile` 与 `Dockerfile` 则固定带 `-tags webui`。
- **new-api 做不到**：`//go:embed web/dist/index.html` 是包级无条件的编译期硬依赖（`main.go:42-46`），缺文件直接编译失败。
- **litellm 做不到**：UI mount 在模块导入期无条件执行（`proxy_server.py:1490-1502`），`DISABLE_ADMIN_UI` 只是前端自觉 + 堵 SSO，`/login` 三个版本都不检查（`:12235`、`:12287`、`:12362`）。
- **CLIProxyAPI 用配置开关**：`secret-key: ""` 让整个 `/v0/management/*` 不注册（`internal/api/server.go:235-241`），但 `MANAGEMENT_PASSWORD` env 非空会**强制打开**（`server.go:166-168`）。
- **opencodex 没有开关**，只有「不构建 gui/dist」「另开一个无凭证的数据面监听器」「管理面自行 fail-closed」三种事实上的形态。

**但四个有管理面的仓库都指向同一个未解问题**：编译期去掉 UI **不等于**有了无 UI 的配置通道。sub2api 明确停在这里——无 embed 构建下业务配置**仍只能走带 JWT 的 admin API 或直接写 DB**（`backend/internal/server/routes/admin.go:349-399`），它的 `AUTO_SETUP=true` env 通道只覆盖进程级配置（`backend/internal/setup/setup.go:567-640`）。这与 portage 现状同形：不带 `webui` tag 编出来后，唯一的配置入口就是 `scripts/seed-example.sql`。

### 3.7 三条可直接借用的工程手法（与形态选择无关）

1. **env-reachability 门禁**（sub2api，`backend/internal/config/env_reachability_test.go:52-80`）：viper 系配置里，没 `SetDefault` 注册过的键在「纯 env、无配置文件」部署下会被静默吞掉。他们用反射遍历 Config 结构体对照 `viper.AllKeys()` 做单测门禁。portage 若给声明文件加 env 覆盖层，这类门禁是同一类问题的现成解法。
2. **拒绝请求体里的秘密引用**（litellm，`litellm/proxy/health_endpoints/_health_endpoints.py:47-68`）：请求体里带 `os.environ/` 会被显式拒绝，防止借健康检查读服务端 env。
3. **「密钥未固定就拒绝持久化」守卫**（sub2api，`backend/internal/service/backup_service.go:40-49`）：加密密钥若是启动时随机生成的，就直接拒绝写入需要加密的秘密，而不是先写下去再等下次启动解不开。

### 3.8 本票查过但没有答案的两处

- **litellm 的官方文档口径**：本工作副本的 `docs/` 只有 2 个文件（`docs/my-website/docs/providers/crusoe.md`、`docs/my-website/docs/proxy/guardrails/xecguard.md`），`docs/my-website/docs/proxy/*.md` 那一整套不在这个 checkout 里。退而查过 `README.md`、`ARCHITECTURE.md:186/211/240`、`deploy/charts/litellm-helm/README.md:101-108` 与 `values.yaml:247-253`、`docker/README.md`、全仓 112 个 md，**均无 config vs DB 优先级的文字说明**，本票 §2.1 的结论全部来自代码。仓内唯一相关的 md（helm README 的「Admin UI is unable to add models」）**与代码冲突且过时**。
- **litellm 的实机复现**：本机未装 litellm 运行依赖（`No module named 'dotenv'`，仓内无 venv），§2.1 ④ 的失败模式结论来自代码路径推演，未做实机复现。

---

## 版本记录

| 日期 | 变更 | 依据 |
|---|---|---|
| 2026-08-19 | 建档：五仓 × 六问事实盘点（#25） | 本地只读五个参考仓库工作副本，HEAD 见文首表 |
