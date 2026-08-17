CREATE TABLE IF NOT EXISTS channels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  -- 支持协议集（口径层 v0.33）：逗号分隔，如 `openai,openai_responses`。取值见
  -- internal/protocol；v0.36 之前写的 `openai_cc` 由 store.migrate 一次性改写。
  -- 单值写法仍然合法，就是一元集合——v0.33 之前的行不用改数据。
  protocols TEXT NOT NULL,
  base_url TEXT NOT NULL,
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

CREATE TABLE IF NOT EXISTS api_keys (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
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
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 管理端自己的状态，目前只有一行 admin_password_hash。
--
-- 单独一张 kv 表而不是往 config.yaml 里回写：口径层 §2.7 要求密码「登录后可改，
-- 改后配置项失效」，改到哪儿就得存到哪儿，而配置文件在容器里是只读挂载的。
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS call_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  api_key_name TEXT NOT NULL,
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
  error TEXT,
  -- 上游错误原文（口径层 v0.53），截前 2KB，只在失败时写，其余行为 NULL。
  -- 与 error 是两件事：error 是网关的**固定词表**（可枚举、可 group by），这一列
  -- 是上游自己说的那段话（不可控文本，只给人看）。可空正因为「没存」与「存了空串」
  -- 要分得开——上游回 4xx 但响应体是空的，也是一条排障信息。
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
  upstream_request_id TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_call_logs_created_at ON call_logs(created_at);
