# Research：GoReleaser 发版流水线与版本注入的接入事实（#151，地图 #150）

调研日期 2026-09-29。只挖事实与坑，不裁决；草案里不得不选的地方单独标「待裁」。所有结论回溯一手来源（官方文档或 GoReleaser 源码 `main` 分支），查不到的点明说查不到。

## 事实源

1. GoReleaser — Builds (Go)：<https://goreleaser.com/customization/builds/go/>
2. GoReleaser — Name Templates：<https://goreleaser.com/customization/templates/>
3. GoReleaser — Archives：<https://goreleaser.com/customization/archive/>；源码 `internal/pipe/archive/archive.go`、`internal/archivefiles/archivefiles.go`
4. GoReleaser — Checksums：<https://goreleaser.com/customization/checksum/>；源码 `internal/pipe/checksums/checksums.go`
5. GoReleaser — Changelog：<https://goreleaser.com/customization/changelog/>
6. GoReleaser — Global Hooks：<https://goreleaser.com/customization/hooks/>
7. GoReleaser — Release：<https://goreleaser.com/customization/release/>
8. GoReleaser — Dist：<https://goreleaser.com/customization/dist/>；Errors: dirty：<https://goreleaser.com/errors/dirty/>
9. GoReleaser — Snapshots：<https://goreleaser.com/customization/snapshots/>；Quick Start：<https://goreleaser.com/quick-start/>；v2 博文：<https://goreleaser.com/blog/goreleaser-v2/>；配置文件查找顺序：<https://goreleaser.com/customization/>
10. GoReleaser — GitHub Actions：<https://goreleaser.com/ci/actions/>；goreleaser/goreleaser-action README：<https://github.com/goreleaser/goreleaser-action>
11. GoReleaser 源码：`internal/builders/golang/build.go`（默认 ldflags / env / `-tags` 拼法）、`internal/pipeline/pipeline.go`（管线顺序）、`internal/git/git.go`（git 输出解码）、`cmd/release.go`（`--clean`/`--snapshot` 帮助文案）、仓库自身 `.goreleaser.yaml`（checksums.txt 命名先例）
12. docker/build-push-action README：<https://github.com/docker/build-push-action>；docker/metadata-action README：<https://github.com/docker/metadata-action>
13. Dockerfile reference — ARG：<https://docs.docker.com/reference/dockerfile/#arg>
14. GitHub Actions — Contexts：<https://docs.github.com/en/actions/reference/workflows-and-actions/contexts>；Workflow syntax：<https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax>；Trigger a workflow：<https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow>
15. GitHub REST — Get the latest release：<https://docs.github.com/en/rest/releases/releases#get-the-latest-release>；Rate limits：<https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api>；Linking to releases：<https://docs.github.com/en/repositories/releasing-projects-on-github/linking-to-releases>
16. actions/setup-node README：<https://github.com/actions/setup-node>
17. Go 1.26.4 `go doc cmd/link`（本机）——`-X` 语义
18. 本仓库：`Dockerfile`、`Makefile`、`web/vite.config.ts`、`.github/workflows/docker.yml`、`.github/workflows/ci.yml`、`.dockerignore`、`.gitignore`、`go.mod`、`internal/webui/embed.go`、`git log` 全史标题

---

## 一、前端产物与 Go 构建：`tags` / `flags` / `ldflags` / `.Version`

| 项 | 事实 | 来源 |
| --- | --- | --- |
| `before.hooks` 是否走 shell | **不走**。每条 hook 是一个命令串，`cd web && npm ci` 这种写法不成立；官方给两条路：`dir:` 字段指定工作目录，或 `sh -c "..."`（官方原话「that gets messy quickly」）。任一 hook 失败整个发版中止 | 6 |
| hook 字段 | `cmd` / `dir` / `env` / `output`（总是打印输出）/ `if`（v2.7+） | 6 |
| `tags` 怎么传 | `tags: [webui]` → 源码 `cmd = append(cmd, "-tags="+strings.Join(tags, ","))`，即 `-tags=webui`；不要再在 `flags` 里重复写 `-tags` | 1、11 |
| `flags` 怎么传 | 逐条模板展开后直接 append 到 `go build` 后面，不加任何前缀；`-trimpath` **不是默认**，要自己写进 `flags` | 1、11 |
| `ldflags` 默认值 | `-s -w -X main.version={{.Version}} -X main.commit={{.Commit}} -X main.date={{.Date}} -X main.builtBy=goreleaser`——一旦自己写 `ldflags` 就**整体替换**默认值，不是追加 | 1、11 |
| `CGO_ENABLED=0` 是否默认 | **不是**。源码只透传 ctx 环境 + 配置的 `env`，没有自动关 CGO；必须显式 `env: [CGO_ENABLED=0]` | 11 |
| `.Version` 带不带 `v` | **不带**。`.Version`=「The version being released」，去掉 `v` 前缀；`.Tag` 才是原样 tag（`v0.4.10`）；`.RawVersion`=`Major.Minor.Patch`。快照构建时 `.Version` 被替换成 `snapshot.version_template`，默认 `{{ .Version }}-SNAPSHOT-{{.ShortCommit}}` | 2、9 |
| `-X main.version` 生效条件 | `go doc cmd/link`：只对「未初始化或用常量字符串初始化」的包级 string 变量生效，初始化里有函数调用或引用别的变量就静默不生效。`cmd/portage` 是 `package main`，importpath 写 `main` 即可 | 17 |
| 三平台矩阵怎么写 | `goos: [linux, darwin]` + `goarch: [amd64, arm64]` 会出 4 个组合，用 `ignore: [{goos: darwin, goarch: amd64}]` 剔掉 darwin/amd64。GoReleaser 默认 goos 含 windows、goarch 含 386，必须显式覆盖 | 1、11 |
| 管线顺序 | `git.Pipe`（含 dirty 校验）→ `semver` → … → `before.Pipe`（全局 hooks）→ … → `build.Pipe`。即 **dirty 校验发生在 hooks 之前**；且 hooks 产出的 `internal/webui/dist`、`web/node_modules`、`web/tsconfig.tsbuildinfo` 都在 `.gitignore` 里，不会触发 dirty | 8、11、18 |
| 本地验证命令 | `goreleaser check` 校验配置；`goreleaser release --snapshot --clean` 全量走一遍但不发布（`--snapshot` = 「implies --skip=announce,publish,validate」，不需要 tag）；`--clean` = 「Removes the 'dist' directory」 | 9、11 |
| 配置文件名 | 查找顺序 `.config/goreleaser.yml` → `.config/goreleaser.yaml` → `.goreleaser.yml` → `.goreleaser.yaml` → `goreleaser.yml` → `goreleaser.yaml`。v2 配置顶部要 `version: 2`，缺了会告警 | 9 |

**对本仓库的含义**：Vite 的 `outDir` 是 `../internal/webui/dist`（`web/vite.config.ts`），`npm run build` = `tsc -b && vite build`；hooks 用 `dir: web` 分两条写 `npm ci`、`npm run build`，产物自然落到 embed 位置，`tags: [webui]` 再让 `internal/webui/embed.go` 的 `//go:embed all:dist` 生效。Go 版本由 runner 上 PATH 里的 `go` 决定（`gobinary` 默认 `go`），所以 GHA 里要先 `setup-go` 到 `go.mod` 的 1.26.4。

**待裁（版本号字面）**：注入 `{{ .Version }}` 得到 `0.4.10`，注入 `{{ .Tag }}` 得到 `v0.4.10`。现有 `docker.yml` 的 `metadata-action` 用 `type=semver,pattern={{version}}`，镜像 tag 已是去 `v` 的 `0.4.10`；GitHub API `releases/latest` 返回的 `tag_name` 是带 `v` 的。草案取 **去 `v`**（与镜像 tag 同源），面板比对时把 `tag_name` 去 `v` 即可。建议就这么定，理由：`.Version` 是 GoReleaser 所有命名模板的默认变量，不去 `v` 反而要处处 `.Tag`。

---

## 二、archive 命名与 `checksums.txt`

| 项 | 事实 | 来源 |
| --- | --- | --- |
| archive 默认 `name_template` | `{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}{{ with .Arm }}v{{ . }}{{ end }}{{ with .Mips }}_{{ . }}{{ end }}{{ if not (eq .Amd64 "v1") }}{{ .Amd64 }}{{ end }}`；默认 `formats: [tar.gz]` | 3 |
| 字段名变更 | `format`（单数）→ `formats`（v2.6 起）；`builds` → `ids`（v2.8 起）。新写就用复数 | 3 |
| 默认打进包的文件 | `license*` `LICENSE*` `readme*` `README*` `changelog*` `CHANGELOG*`，标记为 Default 的 glob 匹配不到时**静默跳过**。本仓库根有 `README.md`、`README.zh-CN.md`（无 LICENSE），默认会打进去；只要二进制用官方 hack `files: [none*]` | 3、18 |
| `.Os` / `.Arch` 取值 | 等于 GOOS / GOARCH：`linux`/`darwin`、`amd64`/`arm64`。**`uname -m` 给的是 `x86_64`/`aarch64`（macOS 给 `arm64`）**，install.sh 要自己映射到 `amd64`/`arm64` | 2 |
| checksums 默认文件名 | `{{ .ProjectName }}_{{ .Version }}_checksums.txt`，默认算法 sha256；`split: true` 时每个产物一份 `{{ .ArtifactName }}.{{ .Algorithm }}` | 4 |
| checksums 行格式 | 源码 `"%v  %v\n"`：`<hex>␠␠<文件名>`，**两个空格**，按文件名排序，即 `sha256sum -c` / `shasum -a 256 -c` 能直接吃的格式 | 4 |
| 固定名先例 | GoReleaser 自己的 `.goreleaser.yaml` 写 `checksum: name_template: "checksums.txt"`，其 release 里资产名就是 `checksums.txt`（`gh api repos/goreleaser/goreleaser/releases/latest` 实测 v2.18.2） | 11 |
| 资产 URL 格式 | 钉版：`https://github.com/<owner>/<repo>/releases/download/<tag>/<asset>`（实测 `…/releases/download/v2.18.2/checksums.txt`）；最新：`…/releases/latest/download/<asset>`（官方文档只写了这一种后缀） | 15 |

**由此固定下来的一套名字**（install.sh 与 `portage upgrade` 共用，草案已按此写）：

```
https://github.com/SimonGino/portage/releases/download/v0.4.10/portage_0.4.10_linux_amd64.tar.gz
https://github.com/SimonGino/portage/releases/download/v0.4.10/portage_0.4.10_linux_arm64.tar.gz
https://github.com/SimonGino/portage/releases/download/v0.4.10/portage_0.4.10_darwin_arm64.tar.gz
https://github.com/SimonGino/portage/releases/download/v0.4.10/checksums.txt
```

坑：`releases/latest/download/portage_<版本>_…tar.gz` 里含版本号，「装最新」必须先知道版本——两条路：先 `GET /repos/SimonGino/portage/releases/latest` 拿 `tag_name`（未鉴权 60 次/小时/IP），或先拉 `releases/latest/download/checksums.txt` 从行里解析文件名。把版本号从 archive 名里去掉（`portage_linux_amd64.tar.gz`）能绕过，但 GoReleaser 默认模板与生态惯例都带版本，且 checksums 行本就按文件名对——**待裁**，草案保留版本号。

---

## 三、changelog：`use`、过滤正则、中文

| 项 | 事实 | 来源 |
| --- | --- | --- |
| `use` 取值 | `git`（默认，本地 `git log`）/ `github`（compare API，行尾追加作者 login）/ `gitlab` / `gitea` / `github-native`（GitHub 自动生成 release notes，**禁用 groups/sort/format**） | 5 |
| 匹配范围 | `filters.exclude` / `include` 与 `groups.regexp` **只匹配提交标题首行**；正则是 RE2 语法（`(?i)` 可用） | 5 |
| `include` 优先 | `include` 非空时只留匹配项，覆盖 `exclude` | 5 |
| `format` 默认 | `{{.SHA}}: {{.Message}}{{ if .Logins }} ({{ .Logins \| englishJoin }}){{ end }}`；`abbrev: -1` 可去掉 hash | 5 |
| groups | `title` / `regexp` / `order`；没 `regexp` 的组是兜底。官方示例 `^.*?feat(\([[:word:]]+\))??!?:.+$`——注意 `[[:word:]]` 是 ASCII 类，scope 里写中文（`feat(转换):`）匹配不上，改 `\(.+\)` 更稳（GoReleaser 自己的配置就是 `\(.+\)`） | 5、11 |
| 中文编码 | `git` 实现：源码直接 `stdout.String()`，字节原样进 Go string，无转码；发到 GitHub 走 JSON（UTF-8）。**没有编码坑**。唯一前提是 runner 上 git 没被配 `i18n.logOutputEncoding`（GHA 默认没有） | 11 |
| 需要完整历史 | `git` 实现要算「上一个 tag..当前 tag」，`actions/checkout` 默认浅克隆拿不到上一 tag，必须 `fetch-depth: 0`（官方原话「required for the changelog to work correctly」） | 10 |

**本仓库提交标题实况**（`git log` 全史 220 条）：`feat` 61、`docs` 61、`fix` 33、`refactor` 32、`test` 10、`Merge …` 6、`ui` 4、`chore` 3、`ci` 2、无前缀 5、`build`/`init` 各 1。全部是 ASCII 半角冒号 + 空格，没有全角冒号变体，`^docs:` 这类前缀正则直接可用。无前缀的 5 条和 `refactor`/`ui` 会落到兜底组——草案把 `docs` `chore` `test` `ci` `build` `Merge` 排掉，其余分「新功能 / 修复 / 其他」三组。`use: git` 还是 `github`：单人仓库追加 `(@SimonGino)` 只是噪音，且 `git` 不吃 API 限流，草案取 `git`。

---

## 四、GHA：action 版本、权限、与 `docker.yml` 并存、`ARG VERSION`

| 项 | 事实 | 来源 |
| --- | --- | --- |
| `goreleaser/goreleaser-action` 主版本 | **v7**。inputs：`distribution`（默认 `goreleaser`）、`version`（默认 `~> v2`，「max satisfying SemVer」）、`args`、`workdir`、`install-only` | 10 |
| 权限 | `permissions: contents: write`——官方原话「if you wish to upload archives as GitHub Releases」；`GITHUB_TOKEN` 限本仓库，本仓库没有 tap/跨仓推送，不需要 PAT | 10、14 |
| 官方推荐流程 | `actions/checkout@v7`（`fetch-depth: 0`）→ `actions/setup-go@v7` → `goreleaser-action@v7`（`args: release --clean`，env `GITHUB_TOKEN`） | 10 |
| Node | hooks 里的 `npm ci` 用 runner 的 Node；`actions/setup-node@v7` 支持 `cache: npm` + `cache-dependency-path: web/package-lock.json` 锁文件在子目录 | 16 |
| tag 触发 | `on.push.tags: ["v*"]` glob；`github.ref_name` 在 tag 推送时是短名 `v1.2.3`，`github.ref` 是 `refs/tags/v1.2.3`，`github.ref_type` 是 `tag` | 14 |
| 与 `docker.yml` 并存 | 两个 workflow 文件各自被同一次 tag 推送触发，各跑各的 runner、各自的 checkout，磁盘互不相见；GoReleaser 用 `GITHUB_TOKEN` 建 Release 属于「events triggered by the `GITHUB_TOKEN` will not create a new workflow run」，不会反过来再触发 `docker.yml`（`release` 事件本仓库也没监听）。**没有耦合点**，唯一共识是版本号字面要同源（见第一节待裁） | 14、18 |
| `build-args` 写法 | `docker/build-push-action` 的 `build-args` 是多行 `KEY=value` 列表；action 当前主版本 v7（本仓库在 v6，改不改与本票无关） | 12 |
| tag → 版本号 | `docker/metadata-action` 的 `steps.meta.outputs.version` 取优先级最高的 tag 的版本：`type=semver,pattern={{version}}`（优先级 900）在 `v0.4.10` 上产出 `0.4.10`（去 `v`），压过 `type=raw,value=latest`（200）；`workflow_dispatch` 时只剩 `test` 那条，version 就是 `test`。所以 **`VERSION=${{ steps.meta.outputs.version }}` 一行搞定**，不用再 `${GITHUB_REF_NAME#v}` | 12 |
| Dockerfile `ARG` 作用域 | `FROM` 之前声明的 ARG 只对 `FROM` 行可见；stage 内声明的 ARG 出 stage 即失效，要在**编译那个 stage 里**声明；`ARG VERSION=dev` 给默认值；ARG 被 `RUN` 用到时其值参与缓存键 | 13 |
| 本仓库 Dockerfile 现状坑 | 第 46 行 `-ldflags='-s -w'` 是**单引号**，shell 不展开 `${VERSION}`，改成双引号才能注入 | 18 |

---

## 五、GoReleaser 的 `dist/` 与 `.dockerignore` 的 `/dist`

- GoReleaser 默认产物目录 `./dist`（仓库根），可用顶层 `dist:` 改名；官方 dirty 错误页明说「The `dist` folder needs to be added to `.gitignore`」，`--clean` 会先删掉它（来源 8、11）。
- 本仓库 `.gitignore` 已有 `/dist/`，`.dockerignore` 已有 `/dist`——两条本来是给别的历史产物留的，如今**正好对上**：前者让 GoReleaser 不报 dirty，后者让本地跑完 `goreleaser --snapshot` 后再 `docker build` 时几十 MB 的 tar.gz 不进构建上下文。
- Vite 的产物目录是 `internal/webui/dist`，不是根 `/dist`，两者路径不同，`.dockerignore` 对 `internal/webui/dist` 的排除另有一条（第 28 行），互不影响。
- GHA 上 `release.yml` 与 `docker.yml` 各自 runner，不存在同盘同名。**结论：同名无冲突，不用改 `dist:`。**

---

## 六、草案

### 6.1 `.goreleaser.yaml`（落仓库根）

```yaml
# GoReleaser 发版：push v* tag → 三个 tar.gz + checksums.txt + 自动 changelog 的 GitHub Release。
# 本地过一遍（不发布、不需要 tag）：goreleaser release --snapshot --clean
version: 2
project_name: portage

before:
  hooks:
    # hook 不走 shell，`cd web && …` 写不了，用 dir 分两条。
    # 产物落 internal/webui/dist（见 web/vite.config.ts），下面 tags: webui 才 embed 得到。
    - cmd: npm ci
      dir: web
    - cmd: npm run build
      dir: web

builds:
  - main: ./cmd/portage
    binary: portage
    env:
      # GoReleaser 不默认关 CGO；modernc sqlite 纯 Go，关掉才是静态二进制。
      - CGO_ENABLED=0
    tags:
      - webui
    flags:
      - -trimpath
    # 自己写 ldflags 会整体替换默认值（默认还注 commit/date/builtBy，口径只要版本号一个字段）。
    # {{ .Version }} 不带 v：v0.4.10 → 0.4.10，与 docker.yml 的镜像 tag 同源。
    ldflags:
      - -s -w -X main.version={{ .Version }}
    goos: [linux, darwin]
    goarch: [amd64, arm64]
    ignore:
      - goos: darwin
        goarch: amd64

archives:
  - formats: [tar.gz]
    # 与默认模板等价（没有 arm/mips/amd64 变体就没有后缀），写死是为了让 install.sh 与
    # `portage upgrade` 拼 URL 的人不用去查 GoReleaser 默认值：
    #   portage_0.4.10_linux_amd64.tar.gz / portage_0.4.10_linux_arm64.tar.gz / portage_0.4.10_darwin_arm64.tar.gz
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    # 包里只放二进制。默认会把根 README* 也塞进去；none* 是官方文档给的「什么都不加」写法。
    files:
      - none*

checksum:
  # 默认名带版本号（portage_0.4.10_checksums.txt），固定成 checksums.txt 才能用
  # releases/latest/download/checksums.txt 这条不带版本的 URL 先拿到文件名再定版本。
  # 行格式 `<sha256>  <文件名>`（两个空格），sha256sum -c / shasum -a 256 -c 直接可用。
  name_template: checksums.txt
  algorithm: sha256

changelog:
  use: git
  sort: asc
  # 只匹配提交标题首行，RE2 语法。仓库标题全是半角冒号 + 空格。
  filters:
    exclude:
      - "^docs"
      - "^chore"
      - "^test"
      - "^ci"
      - "^build"
      - "^Merge "
  groups:
    - title: 新功能
      regexp: '^.*?feat(\(.+\))??!?:.+$'
      order: 0
    - title: 修复
      regexp: '^.*?fix(\(.+\))??!?:.+$'
      order: 1
    - title: 其他
      order: 999

release:
  github:
    owner: SimonGino
    name: portage
  # 预发布 tag（v1.0.0-rc.1）自动标 prerelease，releases/latest 不会指到它。
  prerelease: auto
```

### 6.2 `.github/workflows/release.yml`

```yaml
# GitHub Release：与 docker.yml 同一个 v* tag 触发、各跑各的，互不依赖。
#   - push v* tag → GoReleaser 建前端、编三平台 tar.gz + checksums.txt，建 Release，说明从提交标题生成
#   - workflow_dispatch → 只走 --snapshot（不发布、不需要 tag），把 dist/ 传成 artifact 供下载验证
name: Release

on:
  push:
    tags: ["v*"]
  workflow_dispatch:

jobs:
  goreleaser:
    runs-on: ubuntu-latest
    permissions:
      # 建 Release、传资产要 write；其余默认。
      contents: write
    steps:
      - uses: actions/checkout@v7
        with:
          # changelog 要算「上一个 tag..这个 tag」，浅克隆拿不到上一个 tag。
          fetch-depth: 0

      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
          cache: true

      # before.hooks 里的 npm ci 用的就是这里装的 Node；22 与 Dockerfile 的 node:22-slim 对齐。
      - uses: actions/setup-node@v7
        with:
          node-version: 22
          cache: npm
          cache-dependency-path: web/package-lock.json

      - uses: goreleaser/goreleaser-action@v7
        with:
          distribution: goreleaser
          version: "~> v2"
          args: release --clean ${{ github.event_name == 'workflow_dispatch' && '--snapshot' || '' }}
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}

      # 手动跑时把产物留下来看一眼；tag 触发时 Release 上已经有了，不重复传。
      - if: github.event_name == 'workflow_dispatch'
        uses: actions/upload-artifact@v4
        with:
          name: snapshot
          path: dist/*.tar.gz
```

> `actions/upload-artifact` 的主版本本次未核对（非本票问题），落地前看一眼 README。

### 6.3 `Dockerfile` diff（`ARG VERSION` + 分发形态标记）

```diff
 ARG TARGETOS
 ARG TARGETARCH
-RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
-    go build -tags webui -trimpath -ldflags='-s -w' -o /out/portage ./cmd/portage
+# 版本号由 docker.yml 经 --build-arg VERSION 传入（metadata-action 已去掉 v 前缀，与 GoReleaser
+# 的 {{ .Version }} 同源）；本地裸 docker build 不传就是 dev。ARG 必须声明在这个 stage 里，
+# 写在第一个 FROM 之前只对 FROM 行可见。
+# main.distro=docker 是分发形态标记（地图 #150 裁定）：面板据此不出升级按钮、只给 compose 命令。
+ARG VERSION=dev
+RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
+    go build -tags webui -trimpath \
+    -ldflags="-s -w -X main.version=${VERSION} -X main.distro=docker" \
+    -o /out/portage ./cmd/portage
```

注意原行的 `-ldflags='-s -w'` 是单引号，`${VERSION}` 不会展开——必须换双引号。

### 6.4 `.github/workflows/docker.yml` diff

```diff
       - uses: docker/build-push-action@v6
         with:
           context: .
           platforms: linux/amd64,linux/arm64
           push: true
           tags: ${{ steps.meta.outputs.tags }}
           labels: ${{ steps.meta.outputs.labels }}
+          # 版本号进二进制：semver 那条 tag 规则优先级最高，v0.4.10 → 0.4.10；
+          # workflow_dispatch 只有 test 那条，version 就是 test，正好标出这不是发版。
+          build-args: |
+            VERSION=${{ steps.meta.outputs.version }}
```

### 6.5 Go 侧配套（本票只记事实，不落代码）

`cmd/portage/main.go` 需要包级 `var version = "dev"`、`var distro = "binary"`（或空串）——**必须是常量字符串初始化**，否则 `-X` 静默不生效（来源 17）。GoReleaser 路径不注 `distro`，二进制默认即 binary 形态；Docker 路径注 `docker`。

---

## 七、坑清单（给实现票 #154 直接抄）

1. hooks 不走 shell：`cd web && npm ci` 写不了，用 `dir: web` 分两条。
2. `tags:` 自动拼成 `-tags=webui`；`flags:` 原样 append，`-trimpath` 要自己加；`CGO_ENABLED=0` 要自己写。
3. 自定义 `ldflags` **整体替换**默认值，不是追加。
4. `{{ .Version }}` 不带 `v`（`0.4.10`），`{{ .Tag }}` 带；快照时 `.Version` 变成 `0.4.9-SNAPSHOT-<sha>`。
5. 默认 goos 含 windows、goarch 含 386——必须显式 `goos/goarch` + `ignore` darwin/amd64。
6. archive 默认名 `portage_0.4.10_linux_amd64.tar.gz`；默认会把根 `README*` 塞进包，`files: [none*]` 只留二进制。
7. checksums 默认名带版本 `portage_0.4.10_checksums.txt`，要固定成 `checksums.txt` 自己写 `name_template`；行格式两个空格。
8. `uname -m` 是 `x86_64`/`aarch64`/`arm64`，install.sh 要映射成 `amd64`/`arm64`。
9. changelog 过滤只看标题首行，RE2；中文无编码坑；`[[:word:]]` 不匹配中文 scope，用 `\(.+\)`。
10. `fetch-depth: 0` 少了 changelog 算不出区间。
11. `goreleaser-action@v7` + `version: "~> v2"` + `permissions: contents: write`；不需要 PAT。
12. Dockerfile 现有 `-ldflags='-s -w'` 单引号不展开变量；`ARG VERSION` 要声明在编译 stage 内。
13. `build-args: VERSION=${{ steps.meta.outputs.version }}` 直接得 `0.4.10`（手动触发得 `test`）。
14. 根 `dist/` 与 `.gitignore`/`.dockerignore` 的 `/dist` 同名是好事不是冲突；GHA 两个 workflow 各自 runner。
15. GitHub API `releases/latest` = 最近的非 prerelease 非 draft；未鉴权 60 次/小时/IP——`prerelease: auto` 让 rc tag 不污染 latest。

## 八、未核对 / 查不到

- GoReleaser `release.mode` 的默认值与「同一 tag 重跑时已有资产是否覆盖」——文档列了 `keep-existing`/`append`/`prepend`/`replace` 四档但本次没抓到默认值说明，落地前跑一次 `--snapshot` 之外再看一遍 <https://goreleaser.com/customization/release/>。
- `git` 实现的 `git log` 是否自带 `--no-merges`——未看源码，草案直接 `^Merge ` 排掉不依赖它。
- `actions/upload-artifact` 当前主版本。
