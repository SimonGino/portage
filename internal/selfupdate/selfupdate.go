// Package selfupdate 是 binary 形态的「下载 → 校验 → 替换」（口径层 v1.38 ⑥⑦，展开层 §7.11）：
// 面板升级接口与 `portage upgrade` 子命令共用这一段，替换之后的收场与重启归调用方。
//
// 手写不引库：三个现成库只覆盖 rename 那几十行，且都带签名 / 多 SDK 这些已否的东西。
package selfupdate

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// 错误词表（展开层 §7.11）：Error() 就是回给前端的那个词，细节靠 %w 包在外面进日志。
var (
	ErrDownload    = errors.New("download_failed")
	ErrChecksum    = errors.New("checksum_mismatch")
	ErrNotWritable = errors.New("not_writable")
)

// releases 是 GitHub Release 的根；PORTAGE_DOWNLOAD_BASE 作为前缀拼在它前面（口径层 v1.38 ⑪）。
const releases = "https://github.com/SimonGino/portage/releases"

var versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// Valid 判发版号：只认纯 x.y.z（口径层 v1.39）。版本号要拼进 URL，入口处必须先过这道。
func Valid(v string) bool { return versionRe.MatchString(v) }

// Asset 是本平台的包名，与 .goreleaser.yaml 的 archives.name_template 一致。
func Asset(version string) string {
	return fmt.Sprintf("portage_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
}

func get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDownload, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDownload, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: GET %s: %s", ErrDownload, url, resp.Status)
	}
	return resp, nil
}

// sums 取一份 checksums.txt，回「文件名 → sha256」。行格式 `<sha256>␠␠<文件名>`。
func sums(ctx context.Context, url string) (map[string]string, error) {
	resp, err := get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() {
		if h, name, ok := strings.Cut(sc.Text(), "  "); ok {
			out[name] = h
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: 读 checksums.txt: %v", ErrDownload, err)
	}
	return out, nil
}

// Latest 从 releases/latest/download/checksums.txt 的本平台那行解析最新版本号——
// 不打 GitHub API（未鉴权 60 次/时），与 install.sh 同一个做法。
func Latest(ctx context.Context, base string) (string, error) {
	m, err := sums(ctx, base+releases+"/latest/download/checksums.txt")
	if err != nil {
		return "", err
	}
	suffix := fmt.Sprintf("_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	for name := range m {
		if v, ok := strings.CutPrefix(name, "portage_"); ok {
			if v, ok = strings.CutSuffix(v, suffix); ok && Valid(v) {
				return v, nil
			}
		}
	}
	return "", fmt.Errorf("%w: 最新 Release 里没有 %s/%s 的包", ErrDownload, runtime.GOOS, runtime.GOARCH)
}

// Apply 把 exe 原子替换成 version 的本平台包。失败时 exe 一字不动、临时文件已删。
//
// 顺序即语义：先拿 checksums（缺本平台条目就不必动盘）→ 同目录建临时文件（同目录避
// EXDEV；目录不可写在这一步报出来）→ 下载边算哈希边解包 → 哈希不符删临时文件 →
// Chmod / Sync / **先 Close 再 rename**（不 Close 就 exec 会 ETXTBSY）。一次 rename 覆盖：
// Linux 原地写运行中的文件报 ETXTBSY、macOS 原地写撞签名缓存，rename 两边都对，
// 旧 inode 由运行中的进程持着，旧代码继续跑。
func Apply(ctx context.Context, base, version, exe string) error {
	dir := base + releases + "/download/v" + version + "/"
	asset := Asset(version)
	m, err := sums(ctx, dir+"checksums.txt")
	if err != nil {
		return err
	}
	want, ok := m[asset]
	if !ok {
		return fmt.Errorf("%w: checksums.txt 里没有 %s", ErrDownload, asset)
	}

	// 符号链接启动时替换它指向的文件，别把链接本身覆盖成普通文件。
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return fmt.Errorf("%w: %v", ErrNotWritable, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".portage-*.new")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotWritable, err)
	}
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()

	resp, err := get(ctx, dir+asset)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// checksums.txt 校的是整个 tar.gz：哈希喂压缩字节流，解包从同一条流上读。
	h := sha256.New()
	body := io.TeeReader(resp.Body, h)
	xerr := extract(body, tmp)
	if _, err := io.Copy(io.Discard, body); err != nil {
		return fmt.Errorf("%w: %v", ErrDownload, err)
	}
	// 哈希先于解包错误：下载被截断时 gzip 也会报错，那该说「校验失败」而不是「包不对」。
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%w: %s 期望 %s 实得 %s", ErrChecksum, asset, want, got)
	}
	if xerr != nil {
		return fmt.Errorf("%w: %s: %v", ErrDownload, asset, xerr)
	}

	// CreateTemp 是 0600，rename 不继承旧文件权限。
	if err := tmp.Chmod(0o755); err != nil {
		return fmt.Errorf("%w: %v", ErrNotWritable, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("%w: %v", ErrNotWritable, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%w: %v", ErrNotWritable, err)
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		return fmt.Errorf("%w: %v", ErrNotWritable, err)
	}
	done = true
	return nil
}

// extract 只认包里恰好一个名为 portage 的普通文件：多出来的条目、路径穿越、目录都拒。
func extract(r io.Reader, w io.Writer) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	hdr, err := tr.Next()
	if err != nil {
		return err
	}
	if hdr.Name != "portage" || hdr.Typeflag != tar.TypeReg {
		return fmt.Errorf("包里只该有一个 portage，见到 %q", hdr.Name)
	}
	if _, err := io.Copy(w, tr); err != nil {
		return err
	}
	if hdr, err := tr.Next(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("包里多出条目 %q", hdr.Name)
		}
		return err
	}
	return nil
}
