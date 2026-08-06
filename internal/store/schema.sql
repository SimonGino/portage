CREATE TABLE IF NOT EXISTS channels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  protocol TEXT NOT NULL,
  base_url TEXT NOT NULL,
  credential_type TEXT NOT NULL DEFAULT 'api_key',
  key_mode TEXT NOT NULL DEFAULT 'polling',
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS channel_keys (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  credential TEXT NOT NULL,
  disabled INTEGER NOT NULL DEFAULT 0,
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
  allowed_models TEXT NOT NULL DEFAULT '*',
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
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
  status INTEGER NOT NULL,
  retry_count INTEGER NOT NULL DEFAULT 0,
  ttft_ms INTEGER,
  total_ms INTEGER NOT NULL,
  input_tokens INTEGER, output_tokens INTEGER,
  cache_read_tokens INTEGER, cache_write_tokens INTEGER,
  error TEXT
);

CREATE INDEX IF NOT EXISTS idx_call_logs_created_at ON call_logs(created_at);
