-- M0 手工建配置示例（管理端在 M3，之前都用 SQL 维护）
--
-- 用法：
--   sqlite3 ./gateway.db < scripts/seed-example.sql
-- 先起一次 gateway 让它建表，再灌这个文件。
--
-- 注意：sqlite3 CLI 默认 foreign_keys=OFF，写错 id 不会当场报错，
-- 会留到网关启动校验时才被抓出来。下面这行把它打开。
PRAGMA foreign_keys = ON;

-- ---------------------------------------------------------------------------
-- base_url 填法（展开层 §6.1）：存「协议子路径之前」的前缀，网关自己追加
-- /v1/messages、/v1/chat/completions 等固定后缀。这是最容易填错的一项：
--   Anthropic 官方  https://api.anthropic.com                        （不带 /v1）
--   阿里百炼        https://dashscope.aliyuncs.com/compatible-mode   （不带 /v1，
--                   官方文档给的是带 /v1 的那串，照抄会变成 .../v1/v1/chat/completions）
--   OpenAI 官方     https://api.openai.com                          （不带 /v1）
-- ---------------------------------------------------------------------------

-- 渠道一：Anthropic 官方（Claude Code 走这条）
INSERT INTO channels (name, protocol, base_url) VALUES
  ('anthropic-official', 'anthropic', 'https://api.anthropic.com');

INSERT INTO channel_keys (channel_id, credential) VALUES
  ((SELECT id FROM channels WHERE name = 'anthropic-official'), 'sk-ant-把这里换成真凭证');

INSERT INTO channel_models (channel_id, upstream_model) VALUES
  ((SELECT id FROM channels WHERE name = 'anthropic-official'), 'claude-sonnet-4-5-20250929');

-- 接入点：对外模型名，客户端 model 字段填它。
-- 对外名与纳管模型名可以不同，网关会把请求体里的顶层 model 值翻译成纳管模型名。
INSERT INTO access_points (model) VALUES ('claude-sonnet-4-5');

INSERT INTO candidates (access_point_id, channel_model_id, weight) VALUES (
  (SELECT id FROM access_points WHERE model = 'claude-sonnet-4-5'),
  (SELECT cm.id FROM channel_models cm
     JOIN channels ch ON ch.id = cm.channel_id
    WHERE ch.name = 'anthropic-official' AND cm.upstream_model = 'claude-sonnet-4-5-20250929'),
  100
);

-- ---------------------------------------------------------------------------
-- 临时闸（M0~M2）：每个接入点恰好一个 weight>0 的候选、每个渠道恰好一份启用凭证。
-- 多候选加权分流与凭证池聚合在 M4；现在多插一条，网关会拒绝启动并点名。
-- ---------------------------------------------------------------------------

-- 验证：
--   curl -s http://127.0.0.1:8317/v1/messages \
--     -H 'content-type: application/json' \
--     -d '{"model":"claude-sonnet-4-5","max_tokens":64,
--          "messages":[{"role":"user","content":"ping"}]}'
-- 网关会把 model 改写成 claude-sonnet-4-5-20250929 再发给上游。
-- M0 还没有网关 key 鉴权（M1 才有），所以不带 Authorization 也能打通；
-- 也正因如此 listen 默认绑 127.0.0.1，别改成 0.0.0.0。
