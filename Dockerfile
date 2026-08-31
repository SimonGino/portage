# 单二进制装进 scratch。
#
# 能用 scratch 是因为 SQLite 走的是 modernc.org/sqlite（纯 Go 实现），CGO_ENABLED=0
# 编出来的二进制没有任何动态链接依赖。换成 mattn/go-sqlite3 这一层就得改成
# alpine + libc，别顺手换驱动。

# 前端单独一层。放在最前面而不是塞进 Go 那层：web/ 不动的时候整层走缓存，
# 改 Go 代码不会连带重跑一次 npm ci。
FROM --platform=$BUILDPLATFORM node:22-slim AS webbuild
WORKDIR /web
# 同样先只拷依赖清单。用 npm ci 而不是 install：锁文件说了算，构建才可复现。
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
# Vite 的 outDir 是 ../internal/webui/dist（见 web/vite.config.ts），
# 所以产物落在 /internal/webui/dist，不在 /web 底下。
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26 AS build

WORKDIR /src

# 先只拷依赖清单：源码一改就重下依赖太亏，这一层的缓存值这个额外的 COPY。
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 前端产物必须在 COPY . . **之后**进来，否则会被整包覆盖掉。
# 位置是 internal/webui/dist，因为 //go:embed 只能读自己包目录下的文件。
COPY --from=webbuild /internal/webui/dist ./internal/webui/dist

# -trimpath 去掉构建机的绝对路径；-s -w 去符号表与调试信息，镜像小一半。
# 目标平台由 buildx 通过 TARGETOS/TARGETARCH 注入，交叉编译不需要额外工具链。
# -tags webui 才会真的把上面那份 dist embed 进去；不带这个 tag 编出来的二进制
# 转发照常，但 /admin 只回一页「管理端未编译进此二进制」的说明。
#
# 上面两个 FROM 的 `--platform=$BUILDPLATFORM` 是这句能成立的前提，别顺手删。
# 少了它，buildx --platform linux/amd64 会把**整个构建阶段**当 amd64 跑——在
# arm64 机器上就是 QEMU 模拟，于是这句 GOARCH 变成「在被模拟的 amd64 里原生编
# amd64」，慢一个数量级。不报错，只是构建从两分钟变成二十分钟，看不出原因。
# 前端那层同理，且 JS 产物本就与平台无关。
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -tags webui -trimpath -ldflags='-s -w' -o /out/portage ./cmd/portage

# 空的 /data 也得在镜像里先存在且属主正确：Docker 建命名卷时会照搬镜像里同路径的
# 属主，镜像里没有这个目录，卷就归 root，而我们以 65532 跑——症状是启动即
# `apply schema: unable to open database file (14)`，看着像 SQLite 坏了，其实是权限。
RUN mkdir -p /out/data

FROM scratch

# 上游全是 HTTPS，没有根证书连不上——症状是每个请求都 502，看不出是证书问题。
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/portage /portage
COPY --from=build /src/deploy/config.example.yaml /etc/portage/config.yaml
COPY --from=build --chown=65532:65532 /out/data /data

# 非 root。scratch 里没有 /etc/passwd，所以只能给数字 uid；65532 是 distroless
# 的 nonroot 惯例。
USER 65532:65532

# scratch 里没有 /tmp、/var/tmp，工作目录 / 对 65532 也不可写，而 SQLite 建索引
# 要落临时文件（迁移在存量大表上建索引必踩）——症状是启动报
# `disk I/O error (6410)`（SQLITE_IOERR_GETTEMPPATH），看着像盘坏了，其实是没地方
# 放临时文件。指到 /data：唯一保证可写的路径，临时文件与库同盘还省一次跨盘拷贝。
ENV SQLITE_TMPDIR=/data

# DB 落在 /data，容器删了数据还在。没有这一句的话 gateway.db 写进容器可写层，
# docker rm 一执行，渠道、key、全部流水一起没。
VOLUME ["/data"]
EXPOSE 8317

ENTRYPOINT ["/portage", "-config", "/etc/portage/config.yaml"]
