-- M0 手工建配置示例（管理端在 M3，之前都用 SQL 维护）
--
-- 用法：
--   sqlite3 ./gateway.db < scripts/seed-example.sql
-- 先起一次 gateway 让它建表，再灌这个文件。
--
-- 本文件在 git 里，**别把真凭证填进来**。要用就拷一份到未跟踪的位置改：
--   cp scripts/seed-example.sql /tmp/seed-mine.sql   # 改完灌进 gateway.db（*.db 已 gitignore）
--
-- 注意：sqlite3 CLI 默认 foreign_keys=OFF，写错 id 不会当场报错，
-- 会留到网关启动校验时才被抓出来。下面这行把它打开。
PRAGMA foreign_keys = ON;

-- ---------------------------------------------------------------------------
-- 这份示例按实际用法写：CC 与 Responses 走一个**中转站**（一个上游供多家模型），
-- Anthropic 单独一条上游（理由见下面渠道那节）。官网直连只是把 base_url 换掉、
-- 凭证换成各家自己的，渠道结构一模一样。
--
-- base_url 填法（展开层 §6.1）：存「协议子路径之前」的前缀，网关自己追加
-- /v1/messages、/v1/chat/completions、/v1/responses 等固定后缀。这是最容易填错
-- 的一项——中转站给的地址通常长这样：
--   https://你的中转站/v1        ← 这是**端点**地址，不是这里要填的
--   https://你的中转站           ← 填这个
-- 判断方法：拿 https://你的中转站/v1/v1/models 试一下，返回 404 就说明多了一层。
--
-- 参考（官网直连时的填法，同样不带 /v1）：
--   Anthropic 官方  https://api.anthropic.com
--   OpenAI 官方     https://api.openai.com
--   阿里百炼        https://dashscope.aliyuncs.com/compatible-mode
--                   （官方文档给的是带 /v1 的那串，照抄会变成 .../v1/v1/chat/completions）
-- ---------------------------------------------------------------------------

-- ---------------------------------------------------------------------------
-- 渠道：协议是**渠道**的属性（§7）。同一个上游供多条协议时按协议拆渠道——
-- 同 base_url、同凭证，只有 protocol 不同。临时闸下每条渠道各带一份凭证。
--
-- 这里 Anthropic 单独一条上游、CC 与 Responses 共用中转站，是实测逼出来的分法：
-- 有些中转站把 Anthropic 端点限死「只服务 Claude Code 客户端」，靠 user-agent
-- 与 x-app 两个头一起判定，而网关按白名单转发（口径层，防客户端指纹外泄）不带这
-- 两个，于是每个请求都回 503。给 Anthropic 配一条不设这种闸的上游最省事，也不用
-- 为一家中转站的判定方式动网关口径。
--
-- 建渠道前先确认那个上游到底供哪几个协议端点：
--   curl -s -o /dev/null -w '%{http_code}\n' https://上游/v1/messages  -X POST -H 'Authorization: Bearer sk-…'
--   curl -s -o /dev/null -w '%{http_code}\n' https://上游/v1/responses -X POST -H 'Authorization: Bearer sk-…'
-- 回 404 就说明它不供那条协议，对应渠道别建（建了启动校验过得去，请求打过去才
-- 失败，错得太晚）。
-- ---------------------------------------------------------------------------
INSERT INTO channels (name, protocol, base_url) VALUES
  ('anthropic-upstream', 'anthropic',        'https://你的-anthropic-上游'),
  ('relay-cc',           'openai_cc',        'https://你的中转站'),
  ('relay-resp',         'openai_responses', 'https://你的中转站');

-- Anthropic 那条用它自己的凭证；CC 与 Responses 共用中转站的那份。
INSERT INTO channel_keys (channel_id, credential) VALUES
  ((SELECT id FROM channels WHERE name = 'anthropic-upstream'), 'sk-ant-把这里换成 Anthropic 上游的凭证');

INSERT INTO channel_keys (channel_id, credential)
  SELECT id, 'sk-把这里换成中转站凭证' FROM channels
   WHERE name IN ('relay-cc', 'relay-resp');

-- ---------------------------------------------------------------------------
-- 纳管模型：填**上游认得**的那个名字。中转站的模型名常带前缀或后缀，不确定就问它要：
--   curl -s https://你的中转站/v1/models -H 'Authorization: Bearer sk-…' | jq -r '.data[].id'
--
-- gemini 走 openai_cc 而不是单独一种协议：口径层 v0.17 已定 Gemini 用 OpenAI 兼容
-- 端点接入，渠道协议仍是 openai_cc，协议矩阵不动。中转站供的 gemini 同理。
-- ---------------------------------------------------------------------------
INSERT INTO channel_models (channel_id, upstream_model) VALUES
  ((SELECT id FROM channels WHERE name = 'anthropic-upstream'), 'claude-sonnet-5'),
  ((SELECT id FROM channels WHERE name = 'relay-cc'),           'gpt-5.6-luna'),
  ((SELECT id FROM channels WHERE name = 'relay-cc'),           'gemini-3-flash-preview'),
  ((SELECT id FROM channels WHERE name = 'relay-resp'),         'gpt-5.6-luna');

-- ---------------------------------------------------------------------------
-- 接入点：对外模型名，客户端 model 字段填它。
--
-- 对外名**全网关唯一**，它本身即确定路由入口（口径层 v0.22）；没注册成接入点的
-- model 直接报错，不做「按模型名扫渠道」的兜底。所以同一个上游模型经两条协议供出去
-- 时，两个对外名必须不同——下面 gpt-5.6-luna 与 gpt-5.6-luna-resp 就是这么来的：
--   claude-sonnet-5         → POST /v1/messages
--   gpt-5.6-luna            → POST /v1/chat/completions
--   gemini-3-flash-preview  → POST /v1/chat/completions
--   gpt-5.6-luna-resp       → POST /v1/responses
-- 打错入口会被临时闸挡下并回 501，不会静默发到上游。
--
-- 对外名与纳管模型名可以不同，网关会把请求体里的顶层 model 值翻译成纳管模型名。
-- 这里三个同名一个不同名，是为了两种情形都有个样子可照。
-- ---------------------------------------------------------------------------
INSERT INTO access_points (model) VALUES
  ('claude-sonnet-5'), ('gpt-5.6-luna'), ('gemini-3-flash-preview'), ('gpt-5.6-luna-resp');

INSERT INTO candidates (access_point_id, channel_model_id, weight)
  SELECT ap.id, cm.id, 100
    FROM access_points ap
    JOIN channels ch ON ch.name = CASE ap.model
           WHEN 'claude-sonnet-5'        THEN 'anthropic-upstream'
           WHEN 'gpt-5.6-luna'           THEN 'relay-cc'
           WHEN 'gemini-3-flash-preview' THEN 'relay-cc'
           WHEN 'gpt-5.6-luna-resp'      THEN 'relay-resp' END
    JOIN channel_models cm ON cm.channel_id = ch.id
                          AND cm.upstream_model = CASE ap.model
           WHEN 'claude-sonnet-5'        THEN 'claude-sonnet-5'
           WHEN 'gpt-5.6-luna'           THEN 'gpt-5.6-luna'
           WHEN 'gemini-3-flash-preview' THEN 'gemini-3-flash-preview'
           WHEN 'gpt-5.6-luna-resp'      THEN 'gpt-5.6-luna' END
   WHERE ap.model IN ('claude-sonnet-5', 'gpt-5.6-luna', 'gemini-3-flash-preview', 'gpt-5.6-luna-resp');

-- ---------------------------------------------------------------------------
-- 临时闸（M0~M2）：每个接入点恰好一个 weight>0 的候选、每个渠道恰好一份启用凭证。
-- 多候选加权分流与凭证池聚合在 M4；现在多插一条，网关会拒绝启动并点名。
--
-- 中转站不供某条协议时，把那条渠道连同它的接入点一起停用。以 Responses 为例：
--   UPDATE channels      SET disabled = 1 WHERE name  = 'relay-resp';
--   UPDATE access_points SET disabled = 1 WHERE model = 'gpt-5.6-luna-resp';
--
-- 两条都要，少停一条网关就不启动：启用接入点的 weight>0 候选必须真的可达——它背后
-- 的渠道、纳管模型、凭证任一停用，启动校验都会点名接入点与渠道。这是刻意的：否则
-- 那个接入点仍挂在 /v1/models 上，请求打过去才回 503「没有可用候选」，错得太晚。
-- ---------------------------------------------------------------------------

-- 验证：
--   curl -s http://127.0.0.1:8317/v1/messages \
--     -H 'content-type: application/json' \
--     -d '{"model":"claude-sonnet-5","max_tokens":64,
--          "messages":[{"role":"user","content":"ping"}]}'
-- 对外名与纳管模型名这里恰好同名，所以看不出改写；换成 gpt-5.6-luna-resp 打
-- /v1/responses，网关会把 model 改写成 gpt-5.6-luna 再发给上游。
-- M0 还没有网关 key 鉴权（M1 才有），所以不带 Authorization 也能打通；
-- 也正因如此 listen 默认绑 127.0.0.1，别改成 0.0.0.0。
