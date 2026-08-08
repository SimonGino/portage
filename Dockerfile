# 单二进制装进 scratch。
#
# 能用 scratch 是因为 SQLite 走的是 modernc.org/sqlite（纯 Go 实现），CGO_ENABLED=0
# 编出来的二进制没有任何动态链接依赖。换成 mattn/go-sqlite3 这一层就得改成
# alpine + libc，别顺手换驱动。
FROM golang:1.26 AS build

WORKDIR /src

# 先只拷依赖清单：源码一改就重下依赖太亏，这一层的缓存值这个额外的 COPY。
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# -trimpath 去掉构建机的绝对路径；-s -w 去符号表与调试信息，镜像小一半。
# 目标平台由 buildx 通过 TARGETOS/TARGETARCH 注入，交叉编译不需要额外工具链。
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags='-s -w' -o /out/gateway ./cmd/gateway

# 空的 /data 也得在镜像里先存在且属主正确：Docker 建命名卷时会照搬镜像里同路径的
# 属主，镜像里没有这个目录，卷就归 root，而我们以 65532 跑——症状是启动即
# `apply schema: unable to open database file (14)`，看着像 SQLite 坏了，其实是权限。
RUN mkdir -p /out/data

FROM scratch

# 上游全是 HTTPS，没有根证书连不上——症状是每个请求都 502，看不出是证书问题。
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/gateway /gateway
COPY --from=build /src/deploy/config.docker.yaml /etc/ai-gateway/config.yaml
COPY --from=build --chown=65532:65532 /out/data /data

# 非 root。scratch 里没有 /etc/passwd，所以只能给数字 uid；65532 是 distroless
# 的 nonroot 惯例。
USER 65532:65532

# DB 落在 /data，容器删了数据还在。没有这一句的话 gateway.db 写进容器可写层，
# docker rm 一执行，渠道、key、全部流水一起没。
VOLUME ["/data"]
EXPOSE 8317

ENTRYPOINT ["/gateway", "-config", "/etc/ai-gateway/config.yaml"]
