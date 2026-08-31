# 部署

[English](deploy.md) · [简体中文](deploy.zh-CN.md)

同一个二进制跑两种形态。这份文档一次讲完：形态怎么分、正经路子的三步、次要的手写路径、源码构建、公网暴露，以及两份配置文件各管什么。客户端怎么指过来（Claude Code / Codex CLI）不在这里，见 [README](../README.zh-CN.md#接-claude-code)。

## 两种形态

分界只有一条：有没有设管理密码。

| | 管理密码 | 业务配置 | 用在哪 |
| --- | --- | --- | --- |
| **带管理端** | 设了 | 落在库里，点着改 | 你**用来配**的那台 |
| **纯转发** | 哪儿都没设 | 来自声明文件 | 你**部署到**的那台 |

管理密码的**值只用于初始化，存在性决定形态**：没设密码时，`/panel` 与 `/panel/api/*`——连登录和会话在内——压根不注册，404 是路由给的，不是鉴权给的。没有登录表单可以爆破，也没有管理面会被不小心暴露出去。还听着的只剩 `/v1` 和 `/healthz`。

正经路子是两台都要，三步：**本地开着管理端配好 → 导出一份 `channels.yaml` → 把这份文件部署到一台纯转发实例上。**

## 1. 本地配，带管理端

```bash
PORTAGE_ADMIN_PASSWORD='想好的密码' \
  docker compose -f deploy/docker-compose.yml up -d --build
```

起来后开 <http://127.0.0.1:8317/panel> 登录，依次配渠道 → 纳管模型 → 接入点 → API Key。
上游也在这台上测通：纯转发实例不主动探任何东西，你在这台上没验过的，就没人验了。

这台永远不挂声明文件——库就是事实源，管理端保持可写。所以库一开始是空的，配出第一把 key
之前日志里会一直有那句「`api_keys` 表是空的，所有转发请求都会回 401」：这是警告不是失败，
因为这个形态下配 key 本来就得先有个跑着的网关。挂了文件的那台上同样的情况直接拒绝启动，
见第 3 步。

管理密码走环境变量而不是配置文件：镜像里那份配置是烤进镜像层历史的。它只用于初始化，库里
已有密码后改它不生效。

上面这条命令是给有仓库检出的机器的（本机构建）。没有检出、又要跑管理端形态的机器，改用
[`deploy/docker-compose.admin.yml`](../deploy/docker-compose.admin.yml)：拉 GHCR 镜像，
拷它和一份 `config.yaml`（以 `deploy/config.example.yaml` 为底改）到部署目录即可，密码
同样走环境变量，用法见文件头注释。

## 2. 导出 `channels.yaml`

管理端左栏底下那一个按钮。它把整份业务配置写成一个文件——渠道、纳管模型、凭证、接入点、
API Key 全在里面——运行期状态一样不带，所以你在本地撞出来的 401 不会变成「这把凭证已停用」
跟着跑到部署机上。

**文件里是明文秘密**，上游凭证和 `sk-ptg-…` 都写原值：打了码的文件部署不了，而部署正是它
存在的全部理由。落盘权限是 0600，就保持这样，并且永远别提交——`.gitignore` 里已经有
`channels.yaml` 那一行了。这个形状的文件里唯一进 git 的是 `channels.example.yaml`，那是参
考，不是配置。

## 3. 部署，纯转发

镜像不用在部署机上构建：CI 在每次打 `v*` tag 后把双架构（amd64/arm64）镜像发到 GHCR，公开可
拉，不用登录。`latest` 指向最新发布版，`1.2.3` 这样的版本号 tag 钉具体版本。国内服务器拉
GHCR 不稳的话，同一次构建也双推了阿里云 ACR，tag 集合完全一致，`PORTAGE_IMAGE` 指过去即可：
`crpi-02g5kpg27o6b5u8n.cn-hangzhou.personal.cr.aliyuncs.com/simongino/portage`。

部署机上只要一个目录、三个文件，全部从仓库或第 2 步来，不需要检出：

```text
portage/                         ← 部署目录，名字随意
├── docker-compose.forward.yml   ← 从 deploy/ 拷来
├── config.yaml                  ← 以 deploy/config.example.yaml 为底改（全局限流在这儿）
└── channels.yaml                ← 第 2 步导出的那份
```

```bash
mkdir -p data && sudo chown 65532:65532 data   # Linux 必做：库落这儿，属主必须是容器运行身份
docker compose -f docker-compose.forward.yml up -d
```

`chown 65532` 只在 **Linux** 服务器上需要——容器以 uid 65532 跑，绑定挂载直通宿主权限，
属主不对启动就报 `unable to open database file (14)`：是权限，不是库坏了。macOS 的
Docker Desktop 对绑定挂载自己做了权限映射，这步可以省。库落在 `./data/gateway.db`，
备份就是拷这个目录。

**换镜像源 / 钉版本**都走 `PORTAGE_IMAGE` 环境变量，不用改 compose 文件。默认是 GHCR 的
`latest`；国内服务器换 ACR、或钉具体版本：

```bash
# 国内源 + 钉版本（推荐）
PORTAGE_IMAGE=crpi-02g5kpg27o6b5u8n.cn-hangzhou.personal.cr.aliyuncs.com/simongino/portage:0.1.0 \
  docker compose -f docker-compose.forward.yml up -d

# 每次都带前缀太长的话，写进同目录 .env 文件（compose 自动读）：
# PORTAGE_IMAGE=crpi-02g5kpg27o6b5u8n.cn-hangzhou.personal.cr.aliyuncs.com/simongino/portage:0.1.0
```

compose 里 `PORTAGE_CHANNELS` 已指向挂进去的 `channels.yaml`，管理密码一个都没设——这正
是纯转发形态。不在容器里跑就是 `-channels` 参数，`PORTAGE_CHANNELS` 覆盖它。容器里必须走
环境变量，因为镜像的 `ENTRYPOINT` 把 `-config` 写死了。没有隐式默认路径，也不做搜索——文
件名敲错就得当场炸给你看，而不是悄悄降级成「拿库里那份凑合着转」。

挂了文件之后，这份文件就是业务配置的唯一事实源：启动时 apply 进库，文件里没有的实体会被删
掉；又没设管理密码，也就没有管理端能再去写它。凡是静态就能判错的——候选指向一个不存在的渠
道、冒出个不认识的字段、`api_keys` 是空的——一律拒绝启动，退出码 1，而且**一次把问题全报出
来**，不在第一个上停：反正修的办法只有「改文件重启」这一条。容器里这表现为重启循环，这正是
要的效果——另一种活法是 exit 0 然后无声无息地消失。

## 后面要改配置

在本地那台上改，重新导出，重新部署文件，重启。两台之间什么都不共享，也没有文件热加载——声
明文件只在启动时读一次。

## 升级注记

**老镜像升级迁移可能报 `disk I/O error (6410)`**。scratch 镜像里没有 /tmp，SQLite 在存量
大表上建索引要落临时文件，找不到可写目录就报这个错（6410 = GETTEMPPATH），症状是启动即
败、容器重启循环——看着像盘坏了，其实是没地方放临时文件。新镜像已内置
`ENV SQLITE_TMPDIR=/data`；跑在旧镜像上撞到的话，在 compose 的 `environment:` 里加一行
`SQLITE_TMPDIR: /data` 再 `up -d` 即可，升级到带修复的镜像后这行删不删都行。

**面板前缀从 `/admin` 换成了 `/panel`**（#76）。旧前缀的 GET 会 302 到新前缀的同一路径，
收藏夹和存量邮件里的验证 / 重置链接都不用管。**唯一救不回来的是 OAuth 回调**：GitHub /
Google 只回调后台里登记过的那个地址，302 帮不上忙。配过 OAuth 登录的话，升级后去两家后台把
回调地址从 `…/admin/oauth/<provider>/callback` 改成 `…/panel/oauth/<provider>/callback`——
面板「用户 → 登录与邮件」里拼好待复制的就是新地址，照抄即可。改完之前，走 GitHub / Google
的登录会在上游那一步被拒。

## 手写 `channels.yaml`（次要路子）

[`channels.example.yaml`](../channels.example.yaml) 是拿假数据跑真导出器出来的，另有一条往返测
试钉着（导出 → apply → 再导出，字节相等），所以它当字段参考是可信的——手工维护的样例，字段
一改名就过期，而且没人会发现。

它**不是**一份能直接跑的配置。里面那把 API Key 是出厂占位值，占位 key 启动时会被拒：那等于
把大门钥匙贴在公开仓库里。不认识的字段同样拒。导出的文件里这两样都不会出现，所以严格解析对
主路径零成本，只在这条路上拦人——而且拦在启动那一刻，拦在你人还站在旁边的那台机器上。

## 从源码构建（需要 Go 1.26+ 与 Node）

```bash
make build          # 前端产物 embed 进 bin/portage
./bin/portage
```

不带 `webui` build tag 直接 `go build ./cmd/portage` 也能过，只是 `/panel` 会显示一页
「前端未构建」——CI 和没装 Node 的机器走的就是那条路。注意这个 tag 只管打包进去的前端资产；
管理面到底存不存在，看的是上面那道密码闸，不是这个 tag。

## 公网暴露

容器端口只发布给本机，前面挂 nginx 收 TLS 并只放行 `/v1`，样例见
[`deploy/nginx.conf.example`](../deploy/nginx.conf.example)。**nginx 对 SSE 的几个默认值必须
显式改**，漏了不报错，只表现为卡住或断流。

## 配置文件

两份文件，各答各的问题。

`config.yaml` 只管启动：监听地址、库路径、管理密码初始化、重试参数、全局令牌桶（出厂
10 QPS / 突发 20，只挂转发面）、流水保留期。整个文件都可以省，每个键都有默认值；解析是宽
松的，老部署里留着早已删掉的键也照样起得来。容器里镜像烤了一份默认值（就是
[`deploy/config.example.yaml`](../deploy/config.example.yaml)），要改就以它为底拷一份
`config.yaml` 挂进去盖掉——forward/admin 两份 compose 已经写好了这个挂载。

业务配置——渠道、纳管模型、接入点、候选、凭证、API Key——只有一个事实源，是哪一个取决于有
没有挂声明文件：

- **没挂声明文件**：库是事实源，管理端直接改它。这些东西不落在任何文件里，也就没有「重新部
  署」这回事。
- **挂了 `channels.yaml`**（`-channels` 或 `PORTAGE_CHANNELS`）：文件是事实源。启动时 apply
  进库，文件里没有的实体会被删掉，不认识的字段拒绝，管理端（如果这个形态下还有的话）对业务
  配置全只读。

两种情况下，每次请求真正读的都是库——进程里任何地方都没有配置缓存。文件永远不描述的是运行
期状态：哪把凭证被 401 摘了、什么时候摘的、为什么。那是网关自己该写的，不是你该声明的。
