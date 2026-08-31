// Package webui carries the built admin SPA, or nothing at all.
//
// 单独一个包、且用 build tag 分成两份实现，是为了让「有没有前端」不影响 `go build
// ./...` 能不能过。web/dist 是构建产物，不进 git；如果直接在 admin 包里写
// `//go:embed dist`，那么 CI（没有 Node）、本地首次 clone、以及任何没跑过 npm build
// 的地方都会同时编译失败——而失败信息是「pattern dist: no matching files」，看不出
// 跟前端有关。
//
// 默认（不带 tag）编出来的二进制不含管理端，访问 /panel 会拿到一句说明。
// 发布构建（Dockerfile、Makefile release）带 -tags webui，那时才真 embed。
package webui

import "io/fs"

// Files 返回构建好的管理端静态文件。ok 为 false 表示这个二进制没带前端。
func Files() (fs.FS, bool) { return files() }
