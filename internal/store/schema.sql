CREATE TABLE IF NOT EXISTS channels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  -- 每协议出站根地址（口径层 v0.96 ②）：空串 = 未声明该协议。支持协议集由「哪些
  -- 列非空」推导，不再单独存列——「勾了协议但那一侧没有地址」这种自相矛盾从此在
  -- 形状上表达不出来。至少一列非空由启动闸把守（checkChannelFields）。v0.96 之前的
  -- 单一 base_url × protocols 由 store.migrate 一次性展开（旧地址复制给每个协议）。
  base_url_openai TEXT NOT NULL DEFAULT '',
  base_url_openai_responses TEXT NOT NULL DEFAULT '',
  base_url_anthropic TEXT NOT NULL DEFAULT '',
  credential_type TEXT NOT NULL DEFAULT 'api_key',
  key_mode TEXT NOT NULL DEFAULT 'polling',
  -- 渠道级最大并发（in-flight）上限（口径层 v0.49）：0 = 不限（默认）。闸在网关
  -- 内存态（upstream.Client），这一列只是配置来源；有界排队与 429 见口径层 v0.50。
  max_concurrency INTEGER NOT NULL DEFAULT 0,
  -- 这个渠道的上游认不认 Codex 的 `compaction_trigger`（口径层 v0.54）：0 = 不认
  -- （默认，PO 2026-08-13 裁定）。它**保护的是透传路径**——Responses 形状的 wire
  -- 不等于支持压缩，把一个不认 trigger 的兼容网关配成透传渠道，trigger 原样过去、
  -- 上游回 0 个 compaction item，Codex 照样 Fatal。为否时压缩 turn 明确拒绝。
  -- 转换路径（R→A / R→CC）与这一列无关：那条路上 trigger 根本到不了上游，一律拒。
  supports_compaction INTEGER NOT NULL DEFAULT 0,
  -- 这个渠道的上游认不认 Responses 的有状态语义 `previous_response_id`（口径层
  -- v0.88）：1 = 认（**默认**，与上一列相反）。同样只保护透传路径，为否时带
  -- previous_response_id 的请求回 400（code previous_response_not_found）。
  -- 默认取是的立论：这一位配错成是，上游自己会回一句明确的 unsupported/not_found，
  -- 客户端看得见；配错成否则会打断一条本来正常工作的续链。而 supports_compaction
  -- 配错成是的代价是 Codex 静默 Fatal，两者不对称的方向正相反。
  -- 转换路径（R→A / R→CC）与这一列无关：那条路上有状态语义物理不成立，一律拒。
  supports_stateful_responses INTEGER NOT NULL DEFAULT 1,
  -- models.dev 的 provider id 标注（口径层 §2.10 计价，#74）：只服务填价时的建议价
  -- 与图标分组，**不参与路由**。可选，空串 = 未标注；取值不校验——快照随发版才更新，
  -- 拿它当闸等于让一份会过期的世面数据拦人保存配置。
  provider TEXT NOT NULL DEFAULT '',
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS channel_keys (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  -- 人写的凭证名（口径层 v0.38），日志与用量归因用；不填由管理端给 `凭证 N`。
  -- 渠道内唯一——日志里两行都叫「主号」就废掉了归因本身。唯一性靠下面那条索引，
  -- 不写成表内 UNIQUE：老库要靠 ALTER 补这一列，而 ALTER 加不了约束，
  -- 两条路都走索引才能让新老库长成同一个形状（见 store.migrate）。
  name TEXT NOT NULL DEFAULT '',
  credential TEXT NOT NULL,
  disabled INTEGER NOT NULL DEFAULT 0,
  -- 仅 401 自动摘除（口径层 v0.38：403 换而不摘）；429/5xx 不摘；只人工恢复。
  disabled_reason TEXT,
  disabled_at DATETIME,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS access_points (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  model TEXT NOT NULL UNIQUE,
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS channel_models (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  upstream_model TEXT NOT NULL,
  -- 这个模型自己能走的协议子集（口径层 v0.40）：逗号分隔，取值同 channels.protocols。
  -- **空串 = 继承渠道全集**，绝大多数模型都该是空的——只有「渠道说自己会 anthropic
  -- 和 openai，但这个模型只在 openai 那条子路径上存在」这种例外才填。
  --
  -- 存的是原样，不校验它是不是渠道集的子集：渠道协议集缩小时级联改这一列，等于拿
  -- 「配置任何时刻自洽」换「你改渠道时我替你删配置」，而删掉的填法回不来。路由时
  -- 取交集即可（见 store.pickProtocol），渠道把协议勾回来这一行自动重新生效。
  protocols TEXT NOT NULL DEFAULT '',
  -- 输入上限（估算）（口径层 v0.99）：0 = 不限（默认）。判据是入站原始请求体字节数
  -- ÷ 4，不解析不分词——透传路径不解析 body 是硬约束，字节估算是唯一统一两条路的
  -- 算法。超限 413 + 流水词 request_too_large；闸在 server.relay，Resolve 之后。
  max_input_tokens INTEGER NOT NULL DEFAULT 0,
  -- 四价（口径层 §2.10 计价，#65/#74）：单价挂**纳管条目**不挂全局模型名——同名模型
  -- 在不同渠道的进价本来就不同。单位 USD/百万 token。**NULL = 未定价、0 = 真免费**，
  -- 两态必须分开：未定价条目有用量时管理端要提醒补价（判据 = 四价全 NULL 且有用量，
  -- 提醒挂条目不挂流水行），真免费则什么都不欠。改价不追溯——流水在落库时点计价。
  price_input REAL,
  price_output REAL,
  price_cache_read REAL,
  price_cache_write REAL,
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(channel_id, upstream_model)
);

CREATE TABLE IF NOT EXISTS candidates (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  access_point_id INTEGER NOT NULL REFERENCES access_points(id) ON DELETE CASCADE,
  channel_model_id INTEGER NOT NULL REFERENCES channel_models(id),
  weight INTEGER NOT NULL DEFAULT 100,
  UNIQUE(access_point_id, channel_model_id)
);

-- api_keys 的 DDL 挪到 users 表之后（#73 加了指向 users 的外键）；老库的重建迁移
-- 见 store.rebuildAPIKeysOwner，两条路必须长出同一个形状。

-- 用户（口径层 §2.10，#61）：网关的登录主体，邮箱即标识。多用户体系对无 webui /
-- 纯转发形态零负担——这张表在无 admin 的库上就是空表，转发链路一个字都不读它。
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  email TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL DEFAULT '',
  -- 可空而不是默认空串：OAuth-only 账号**没有**密码，与「密码是空串」必须分得开——
  -- bcrypt 对空串也能算出合法哈希，抹成空串会让「无密码」变成一个可登录的密码。
  password_hash TEXT,
  -- 两档 admin / user（#61），单列可任免、多 admin 允许。
  role TEXT NOT NULL DEFAULT 'user',
  -- 停用即对系统一切访问冻结：key 与 session 走热路径联查这一列，当场失效。
  -- 用户删除 v1 不做，停用是唯一的「请出去」。
  disabled INTEGER NOT NULL DEFAULT 0,
  email_verified INTEGER NOT NULL DEFAULT 0,
  -- 月度 USD 限额（#65）：**NULL = 不限额（默认）、0 = 封停**，两个语义都要，
  -- 所以这一列必须可空——默认 0 会把每个新用户都封停。配额闸在 #74 落地。
  monthly_quota_usd REAL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS api_keys (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  key_hash TEXT NOT NULL UNIQUE,
  -- 明文（口径层 v0.47：管理端要能回读并复制）。**存量行永远是空串**——加这一列
  -- 之前建的 key 只留下哈希，哈希不可逆，那些 key 的原值谁也拿不回来，界面上如实
  -- 说明并让人删了重建。
  --
  -- key_hash 保留且仍是唯一的校验依据：转发热路径按哈希查，跟这一列无关。它用裸
  -- SHA-256 的原始理由（「明文只在创建那一个响应里存在过」）从这一版起不成立了，
  -- 但也不再有意义——明文就在同一张表的隔壁列，加盐慢哈希保护不了任何东西。
  key_plain TEXT NOT NULL DEFAULT '',
  allowed_models TEXT NOT NULL DEFAULT '*',
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  -- 归属用户（口径层 §2.10，#63/#66 修订）。**可空**：NULL = 无主 key，是无用户
  -- 体系形态（声明文件所建、从未有 admin 的库）的合法形态，不是残缺数据——出现
  -- 第一个 admin 后由启动认领（store.claimOrphanKeys）幂等归其名下。
  user_id INTEGER REFERENCES users(id),
  -- 名字从全局唯一改按用户唯一（#63）：两个人各有一把「笔记本」是多用户的常态。
  -- SQLite 的 UNIQUE 对 NULL 逐行视为不同，无主 key 的名字唯一性因此靠不上这条，
  -- 由 idx_api_keys_unowned_name 补上（建在 migrate 里，理由同 idx_channel_keys_name：
  -- schema 跑在 migrate 之前，老库那时还没有 user_id 这一列）。
  UNIQUE(user_id, name)
);

-- 登录会话（#61）：落库替换内存实现，单 cookie 一套体系。
--
-- 落库推翻了旧内存版「重启即全吊销是特性」的立论（口径层 §2.7 时代）：多用户下
-- 重启踢掉所有人代价不再是「一个人重登一次」。密码泄露的补救从「重启」改成
-- 「改密码吊销全部会话」（DeleteAllSessions），语义没丢，只是换了扳机。
CREATE TABLE IF NOT EXISTS sessions (
  -- token 即主键：32 字节 crypto/rand 的 hex，猜不出来所以不需要另一层 id。
  token TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  -- unix 秒而不是 DATETIME：过期判断要在 Go 里比大小，DATETIME 文本比较依赖
  -- 驱动的解析与格式一致性（ListExposedModels 为此绕过一回），整数没这个问题。
  -- TTL 两档滑动——user 30 天 / admin 12 小时（#61），每次有效校验都往后推。
  expires_at INTEGER NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 邀请码（口径层 §2.10，#62 决议 1）：一次性注册凭证，admin 生成、可选有效期、
-- 未使用可撤销（撤销即删行）、一码一人、用后作废并记录使用者。OAuth 首登同样要码。
CREATE TABLE IF NOT EXISTS invite_codes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  code TEXT NOT NULL UNIQUE,
  -- 可选有效期，unix 秒；NULL = 不过期。unix 而不是 DATETIME 的理由同 sessions。
  expires_at INTEGER,
  -- 用后作废靠这两列：used_by 非空即已用。不删已用的行——「这个码是谁用掉的」
  -- 正是要记录的东西。
  used_by INTEGER REFERENCES users(id),
  used_at DATETIME,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- OAuth 身份（#62 决议 4）：provider + provider_user_id → user。身份主键用上游的
-- 不可变 id（GitHub 数字 id / Google sub），不用邮箱——邮箱在上游是可换绑的。
CREATE TABLE IF NOT EXISTS oauth_identities (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  provider TEXT NOT NULL,
  provider_user_id TEXT NOT NULL,
  user_id INTEGER NOT NULL REFERENCES users(id),
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  -- 一个上游账号只能挂一个用户；一个用户对每家 provider 也只挂一个上游账号——
  -- 「绑定/解绑」是按 provider 说的，允许一人多绑会让解绑说不清解的是哪一个。
  UNIQUE(provider, provider_user_id),
  UNIQUE(user_id, provider)
);

-- 一次性动作 token（#62 决议 2/5/6）：邮箱验证（24h）、重置密码（30min）、OAuth
-- 完成注册（回调与填邀请码之间的接力棒）。都是「发出去、用一次、过期作废」的同一
-- 形状，收在一张表里；用途靠 purpose 分开，消费时必须带上它——拿验证 token 去重置
-- 密码必须不成立。
CREATE TABLE IF NOT EXISTS auth_tokens (
  -- token 即主键，32 字节 crypto/rand 的 hex，同 sessions：猜不出来就不需要另一层 id。
  token TEXT PRIMARY KEY,
  purpose TEXT NOT NULL,
  -- 可空：OAuth 完成注册那一档发生在用户存在之前，挂不到任何人名下。
  user_id INTEGER REFERENCES users(id),
  -- 用途自带的负载（OAuth 完成注册存 provider/上游 id/邮箱的 JSON），其余用途空串。
  payload TEXT NOT NULL DEFAULT '',
  expires_at INTEGER NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 管理端自己的状态：admin_password_hash，以及 #72 起的 SMTP / OAuth client /
-- 站点外部 URL（键名清单见 store/settings.go）。
--
-- 单独一张 kv 表而不是往 config.yaml 里回写：口径层 §2.7 要求密码「登录后可改，
-- 改后配置项失效」，改到哪儿就得存到哪儿，而配置文件在容器里是只读挂载的。
-- #62 决议 7 把 SMTP 与 OAuth 配置也钉在这里（config.yaml 不加新键、改完即生效），
-- 是同一条理由的延伸。
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS call_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  api_key_name TEXT NOT NULL,
  -- 这次打的是哪个转发端点（#17），取值就是 protocol.Endpoint 的那四条路径原样。
  -- client_protocol 分不开它：`/v1/messages` 与 `/v1/messages/count_tokens` 的入站
  -- 协议同为 anthropic，而 count_tokens 命中非 Anthropic 渠道回 501、限流回 429 的
  -- 行既没有模型也没有耗时，不记端点的话它们在流水里长得一模一样，「这波 501 是
  -- count_tokens 还是别的」只能靠时间戳猜。
  --
  -- 不可空、默认空串：值由最外层的 callLog 中间件在任何事情失败之前就写死（见
  -- internal/server/auth.go），新行一律有值，空串只有一个意思——加列之前的老行。
  endpoint TEXT NOT NULL DEFAULT '',
  -- 这次**打到上游的**是哪条路径（#20），与上面那条入站端点成对。跨协议时两者不
  -- 相等：Anthropic 入口进来、落到 openai 渠道，出站打的是 /v1/chat/completions；
  -- 同协议透传时两者相等（count_tokens 透传到 anthropic 渠道即 count_tokens 原样）。
  -- upstream_protocol 也分不开它，理由同上面那条：一个协议对一条路径是巧合不是恒等，
  -- count_tokens 就没有出口对应物。
  --
  -- 不可空、默认空串，但空串在这一列上有两个意思，靠 endpoint 那列分辨：
  -- endpoint 也空 = 加列之前的老行；endpoint 非空而这列空 = **这次请求从没发到上游**
  -- （401 停在鉴权、429 停在限流、count_tokens 撞非 anthropic 渠道就地 501、并发闸
  -- 队满/超时），空就是事实，不从 upstream_protocol 推导补值。
  upstream_endpoint TEXT NOT NULL DEFAULT '',
  client_protocol TEXT NOT NULL,
  upstream_protocol TEXT NOT NULL,
  model_requested TEXT NOT NULL,
  model_upstream TEXT NOT NULL,
  channel_name TEXT NOT NULL,
  -- 本次真正发请求的那份凭证名（换过则记最后一份，失败亦然）；口径层 v0.38。
  -- 快照冗余而非 channel_key_id：删凭证是常事，存 id 会把历史 join 空。
  -- 没走到上游时为空串（迁移前的老行同）。
  channel_key_name TEXT NOT NULL DEFAULT '',
  status INTEGER NOT NULL,
  retry_count INTEGER NOT NULL DEFAULT 0,
  -- 同步/流式（0/1）。可空：stream 是解析请求体才知道的，鉴权失败、body 不是合法
  -- JSON 那些行根本没走到那一步——「不知道」与「同步」不是一回事，迁移前的老行同。
  is_stream INTEGER,
  ttft_ms INTEGER,
  total_ms INTEGER NOT NULL,
  input_tokens INTEGER, output_tokens INTEGER,
  cache_read_tokens INTEGER, cache_write_tokens INTEGER,
  -- 思考 token（口径层 v0.66）。是 output_tokens 的**明细**不是另一笔，别把两者相加。
  -- 可空且必须可空：NULL 是「上游不报这个数」（Anthropic 一路、迁移前的老行），
  -- 0 是「上游报了，这次没思考」。抹成 0 会让前者显示成确凿的零思考成本。
  reasoning_tokens INTEGER,
  -- 并发闸排队耗时（口径层 v0.52）：没排队就是 0，不可空——「没排」与「排了 0ms」
  -- 在这一列上就是一回事。排队被拒的行靠 error 归因（queue_full / queue_timeout）。
  queue_wait_ms INTEGER NOT NULL DEFAULT 0,
  -- 网关自己的固定词表（可枚举、可 group by），10 个词，口径层 v0.70。
  -- 可空性规则的唯一定义处在 Go 侧：internal/calllog 的 `Outcome.Column`（写向，
  -- NULL 即「这行没有错误词」）与 `ErrorWord`（读向）。这里不复述，抄一份就会漂。
  error TEXT,
  -- 上游错误原文（口径层 v0.53），截前 2KB，只在失败时写，其余行为 NULL。
  -- 与 error 是两件事：那一列是可枚举的词，这一列是上游自己说的那段话（不可控
  -- 文本，只给人看）。可空正因为「没存」与「存了空串」要分得开——上游回 4xx 但
  -- 响应体是空的，也是一条排障信息。
  --
  -- 注意一个新组合：上游透传 4xx 的 error 列**是空的**（透传成功不算网关侧错误，
  -- v0.28 纪律），但会有 error_detail。管理端的「可展开」判据因此是 status >= 400。
  error_detail TEXT,
  -- 上游响应头 request-id 的原样快照（口径层 v0.56，#37）。个人自用场景下拿它去
  -- 找上游对账：官方文档要求报障时附这个 id。取头名 `request-id`（Anthropic 官方
  -- 文档 §Request ID 的拼写），兜底 `x-request-id`（中转常用）。
  --
  -- 不可空、默认空串：这一列上「没走到上游」与「上游没回这个头」都读作「没有可用
  -- 的 id」，分开没有排障价值——前者看 status 就知道。它同时也回传给客户端（响应头
  -- 原样透传，见 upstream.CopyResponseHeaders），这里只是留一份可查询的副本。
  upstream_request_id TEXT NOT NULL DEFAULT '',
  -- 这一次调用的成本（口径层 §2.10 计价，#65/#74），USD。落库时点按选中渠道纳管
  -- 条目的四价算死：净 input（毛值减缓存两项）、output、cache_read、cache_write
  -- 各乘各的单价 ÷ 1e6 求和；reasoning_tokens 是 output 的明细不另计。之后改价
  -- **不追溯**，这一列就是当时的账。
  --
  -- 可空且必须可空：NULL = 没有用量可计（未鉴权、没到上游、加列前的老行），
  -- 0 = 有用量但当时未定价（或真免费）。抹成 0 会把「没打上游」说成「免费打了一次」。
  cost REAL
);

-- 用量查询与保留期清理（口径层 v0.93 的 DELETE）共用这一个索引。
CREATE INDEX IF NOT EXISTS idx_call_logs_created_at ON call_logs(created_at);
