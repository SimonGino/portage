# 只做「构建」这一件事。测试、vet、fmt 直接敲 go 命令，包一层没有增益。
#
# 关键约定：前端产物落在 internal/webui/dist，而**不是** web/dist。
# //go:embed 只能读自己包目录下的文件，产物待在 web/ 里 embed 根本看不见。

UI_DIST := internal/webui/dist

.PHONY: build build-ui clean-ui dev-ui

# build 出来的二进制自带管理端。不带 -tags webui 的话 `go build ./cmd/portage`
# 也能过，只是访问 /admin 会看到一页「前端未构建」的说明——CI 与没装 Node 的
# 机器走的就是那条路。
build: build-ui
	go build -tags webui -trimpath -o bin/portage ./cmd/portage

build-ui:
	cd web && npm ci && npm run build

# 前端开发时用：Vite 起在 5173，/admin/api 反代到 8317 上跑着的网关。
dev-ui:
	cd web && npm install && npm run dev

clean-ui:
	rm -rf $(UI_DIST) web/node_modules
