package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type entry struct {
	name string
	typ  byte
	body string
}

func tarGz(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o755, Size: int64(len(e.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// fakeRelease 起一个假的 GitHub：base 与 PORTAGE_DOWNLOAD_BASE 同为前缀语义，所以请求路径
// 就是 `/https://github.com/SimonGino/portage/releases/...`。files 的键是 releases 之后那段。
func fakeRelease(t *testing.T, files map[string][]byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[strings.TrimPrefix(r.URL.Path, "/"+releases)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/"
}

// release 摆一个 v0.1.1：本平台的包 + 一份 checksums.txt（两个空格分隔，同 GoReleaser）。
func release(pkg []byte, sumLine string) map[string][]byte {
	asset := Asset("0.1.1")
	if sumLine == "" {
		sumLine = sum(pkg) + "  " + asset
	}
	sums := []byte("0000  portage_0.1.1_plan9_mips.tar.gz\n" + sumLine + "\n")
	return map[string][]byte{
		"/download/v0.1.1/checksums.txt": sums,
		"/download/v0.1.1/" + asset:      pkg,
		"/latest/download/checksums.txt": sums,
	}
}

func oldExe(t *testing.T) (dir, exe string) {
	t.Helper()
	dir = t.TempDir()
	exe = filepath.Join(dir, "portage")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, exe
}

// 失败路径的共同要求：旧文件一字未动，目录里不留 .portage-*.new。
func untouched(t *testing.T, dir, exe string) {
	t.Helper()
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Errorf("旧可执行文件被改成了 %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".portage-*.new")); len(left) > 0 {
		t.Errorf("临时文件没删：%v", left)
	}
}

func TestApplyReplacesExecutable(t *testing.T) {
	dir, exe := oldExe(t)
	base := fakeRelease(t, release(tarGz(t, entry{"portage", tar.TypeReg, "new"}), ""))

	v, err := Latest(t.Context(), base)
	if err != nil || v != "0.1.1" {
		t.Fatalf("Latest = %q, %v", v, err)
	}
	if err := Apply(t.Context(), base, "0.1.1", exe); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	b, _ := os.ReadFile(exe)
	fi, _ := os.Stat(exe)
	if string(b) != "new" || fi.Mode().Perm() != 0o755 {
		t.Fatalf("替换后内容 %q 权限 %v，期望 new / 0755", b, fi.Mode().Perm())
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".portage-*.new")); len(left) > 0 {
		t.Errorf("临时文件没收：%v", left)
	}
}

func TestApplyChecksumMismatchRemovesTemp(t *testing.T) {
	dir, exe := oldExe(t)
	base := fakeRelease(t, release(tarGz(t, entry{"portage", tar.TypeReg, "new"}),
		strings.Repeat("0", 64)+"  "+Asset("0.1.1")))

	if err := Apply(t.Context(), base, "0.1.1", exe); !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v，期望 checksum_mismatch", err)
	}
	untouched(t, dir, exe)
}

func TestApplyReadOnlyDirIsNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 无视目录权限")
	}
	dir, exe := oldExe(t)
	base := fakeRelease(t, release(tarGz(t, entry{"portage", tar.TypeReg, "new"}), ""))
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	if err := Apply(t.Context(), base, "0.1.1", exe); !errors.Is(err, ErrNotWritable) {
		t.Fatalf("err = %v，期望 not_writable", err)
	}
	untouched(t, dir, exe)
}

func TestApplyMissingPlatformEntry(t *testing.T) {
	dir, exe := oldExe(t)
	files := release(tarGz(t, entry{"portage", tar.TypeReg, "new"}), "")
	files["/download/v0.1.1/checksums.txt"] = []byte("0000  portage_0.1.1_plan9_mips.tar.gz\n")
	base := fakeRelease(t, files)

	if err := Apply(t.Context(), base, "0.1.1", exe); !errors.Is(err, ErrDownload) {
		t.Fatalf("err = %v，期望 download_failed", err)
	}
	untouched(t, dir, exe)
}

// 包只认一个名为 portage 的普通文件：多文件、路径穿越、同名目录都拒——哪怕哈希对得上。
func TestApplyRejectsUnexpectedTarEntries(t *testing.T) {
	for name, entries := range map[string][]entry{
		"多文件":  {{"portage", tar.TypeReg, "new"}, {"README.md", tar.TypeReg, "x"}},
		"路径穿越": {{"../portage", tar.TypeReg, "new"}},
		"同名目录": {{"portage", tar.TypeDir, ""}},
	} {
		t.Run(name, func(t *testing.T) {
			dir, exe := oldExe(t)
			base := fakeRelease(t, release(tarGz(t, entries...), ""))
			if err := Apply(t.Context(), base, "0.1.1", exe); !errors.Is(err, ErrDownload) {
				t.Fatalf("err = %v，期望 download_failed", err)
			}
			untouched(t, dir, exe)
		})
	}
}

func TestAssetMatchesGoReleaser(t *testing.T) {
	want := fmt.Sprintf("portage_0.1.1_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	if got := Asset("0.1.1"); got != want {
		t.Fatalf("Asset = %q，期望 %q（.goreleaser.yaml 的 name_template）", got, want)
	}
}
