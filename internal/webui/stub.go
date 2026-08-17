//go:build !webui

package webui

import "io/fs"

func files() (fs.FS, bool) { return nil, false }
