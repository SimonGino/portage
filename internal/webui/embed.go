//go:build webui

package webui

import (
	"embed"
	"io/fs"
)

// all: 前缀不能省：Vite 的产物里有 .vite/ 这类点开头的目录，默认的 embed 规则会
// 把它们悄悄跳过。
//
//go:embed all:dist
var dist embed.FS

func files() (fs.FS, bool) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// dist 目录一定在（否则上面的 embed 指令编不过），走不到这里。
		return nil, false
	}
	return sub, true
}
