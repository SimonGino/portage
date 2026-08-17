-- 用 SQL 建配置的示例。
--
-- **平时不该走这条路**：起来之后开 http://127.0.0.1:8317/admin，渠道、上游凭证、
-- 纳管模型、接入点、网关 key 都在页面上配（M3 起）。这份留着是给三种情形：
-- 不带 UI 的部署（`go build` 没加 `-tags webui`）、自动化建库、以及管理端登不进去
-- 时的救急（忘了管理密码就是典型，密码只在库为空时由配置初始化）。
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
-- protocols 是**协议集**（口径层 v0.33）：逗号分隔，如 `openai,openai_responses`。
-- 下面三条各自只供一种协议，写的就是一元集合。列名是复数，别照老写法写 protocol
-- ——那样 sqlite 直接报 `table channels has no column named protocol`，一行都灌不进去。
INSERT INTO channels (name, protocols, base_url) VALUES
  ('anthropic-upstream', 'anthropic',        'https://你的-anthropic-上游'),
  ('relay-cc',           'openai',           'https://你的中转站'),
  ('relay-resp',         'openai_responses', 'https://你的中转站');

-- Anthropic 那条用它自己的凭证；CC 与 Responses 共用中转站的那份。
--
-- name 是人写的凭证名（口径层 v0.38），用量与日志按它归因。这里每条渠道只有一份
-- 凭证，看着像多余，但**留空的代价在事后**：call_logs 存的是当时那份凭证的名字，
-- 空名字的行到 M4 凭证池铺开后再也分不清是哪一份。起个名最省事。
INSERT INTO channel_keys (channel_id, name, credential) VALUES
  ((SELECT id FROM channels WHERE name = 'anthropic-upstream'), '主号',
   'sk-ant-把这里换成 Anthropic 上游的凭证');

INSERT INTO channel_keys (channel_id, name, credential)
  SELECT id, '中转主号', 'sk-把这里换成中转站凭证' FROM channels
   WHERE name IN ('relay-cc', 'relay-resp');

-- ---------------------------------------------------------------------------
-- 纳管模型：填**上游认得**的那个名字。中转站的模型名常带前缀或后缀，不确定就问它要：
--   curl -s https://你的中转站/v1/models -H 'Authorization: Bearer sk-…' | jq -r '.data[].id'
--
-- gemini 走 openai 而不是单独一种协议：口径层 v0.17 已定 Gemini 用 OpenAI 兼容
-- 端点接入，渠道协议仍是 openai，协议矩阵不动。中转站供的 gemini 同理。
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

-- ---------------------------------------------------------------------------
-- 网关 key（M1 起转发端必须带，不带一律 401）。这一节不能省：干净库起来后
-- api_keys 是空表，所有请求都是 401。
--
-- 有管理端的话在页面上建更省事（明文会存进 key_plain，之后随时能「显示 / 复制」）。
-- 这里手算 hash 灌进去的 key **不会**留下明文：管理端那一行的值只显示
-- 「原值没存过，只能删了重建」（`web/src/pages/Keys.tsx`）。所以自己留好底下这串。
--
-- 表里存的是 hash，不是明文。生成一把：
--   KEY="sk-ptg-$(openssl rand -hex 16)"; echo "$KEY"
--   printf %s "$KEY" | shasum -a 256      # 这串填 key_hash
-- printf 而不是 echo：echo 会多一个换行，算出来的 hash 对不上，表现是永远 401。
--
-- 算法是 SHA-256 裸哈希、小写十六进制、不加盐（展开层 §7.1）：key 是高熵随机串
-- 不是人选密码，而鉴权每请求都要走一遍、要吃 key_hash 上的唯一索引。
--
-- allowed_models 这列 M1 只建不校验，一律当 `*`，填了也不生效（口径层 v0.27）。
-- 没有 expires_at：v1 不做过期，停用走 disabled = 1。
-- ---------------------------------------------------------------------------
INSERT INTO api_keys (name, key_hash) VALUES
  ('laptop', '把上面算出来的 64 位十六进制填这里');

-- 验证：
--   curl -s http://127.0.0.1:8317/v1/messages \
--     -H 'content-type: application/json' \
--     -H "x-api-key: $KEY" \
--     -d '{"model":"claude-sonnet-5","max_tokens":64,
--          "messages":[{"role":"user","content":"ping"}]}'
-- 对外名与纳管模型名这里恰好同名，所以看不出改写；换成 gpt-5.6-luna-resp 打
-- /v1/responses，网关会把 model 改写成 gpt-5.6-luna 再发给上游。
--
-- 两个凭证头都认：Anthropic 系客户端发 x-api-key，OpenAI 系发
-- `Authorization: Bearer $KEY`，随便哪个对上就放行。/v1/models 同样要带；
-- 只有 /healthz 不鉴权（探活没地方放 key）。
--
-- 每条请求会在 call_logs 落一行（含 401 的），看用量直接查：
--   sqlite3 gateway.db 'SELECT created_at, api_key_name, model_requested, status,
--                              input_tokens, output_tokens FROM call_logs
--                        ORDER BY id DESC LIMIT 20;'
--
-- listen 仍默认绑 127.0.0.1。有了 key 鉴权也别顺手改成 0.0.0.0：公网暴露还差
-- TLS 与全局限流（口径层 §2.7，M3）。
