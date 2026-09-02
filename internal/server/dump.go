package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SimonGino/portage/internal/protocol"

	"github.com/gin-gonic/gin"
)

// dumper 是 `PORTAGE_DUMP_DIR` 开的**排障采样**：每次转发把三样东西按文件落到目录里
// ——入站原始请求体、发给上游的请求体（透传是改过 model 的那份，转换是重编出来的
// 那份）、回给客户端的字节（流式就是整段 SSE）。
//
// 它存在的理由是采 golden：真实 harness（ADE）只能打公网部署，没有第二种办法拿到
// 它第二轮回带的请求体（口径层 v1.14 ⑤，#95）。所以它刻意只做这一件事：
//   - 不进 config.yaml，只认环境变量——它是「这几分钟开一下」的开关，不是配置项；
//   - 不脱敏、不截断：采的就是全文，脱敏了就不是样本。请求头**不采**（里面有网关
//     key）；上游 key 与 base_url 本来就不在这三份字节里（口径层硬约束）；
//   - 写盘失败只 Warn 一次不影响转发：采样坏了不能把请求搞坏。
//
// 目录不存在会建。文件名 `<时间>-<毫秒>-<序号>-<端点>.<in.json|out.json|resp>`，同一次请求
// 三份同前缀。
type dumper struct {
	dir  string
	log  *slog.Logger
	seq  atomic.Uint64
	once sync.Once
}

// dumpKey 是 dumpRecord 挂在 gin.Context 上的键：relay 起记录，relayConverted 接着写。
const dumpKey = "portage.dump"

func dumpFrom(c *gin.Context) *dumpRecord {
	if v, ok := c.Get(dumpKey); ok {
		return v.(*dumpRecord)
	}
	return nil
}

func newDumper(dir string, log *slog.Logger) *dumper {
	if dir == "" {
		return nil
	}
	return &dumper{dir: dir, log: log}
}

// begin 给一次请求起一个采样记录。dumper 为 nil（没开）时返回 nil，dumpRecord 的
// 方法全部 nil 安全——relay 里不用到处判开关。
func (d *dumper) begin(ep protocol.Endpoint) *dumpRecord {
	if d == nil {
		return nil
	}
	slug := strings.ReplaceAll(strings.Trim(ep.Path, "/"), "/", "_")
	// 时间到毫秒，序号保证同毫秒不撞；名字里不含 `.`，后缀之前全是 `-` 分隔的。
	stamp := strings.ReplaceAll(time.Now().Format("20060102-150405.000"), ".", "-")
	name := fmt.Sprintf("%s-%06d-%s", stamp, d.seq.Add(1), slug)
	return &dumpRecord{d: d, prefix: filepath.Join(d.dir, name)}
}

type dumpRecord struct {
	d      *dumper
	prefix string
	mu     sync.Mutex
	resp   *os.File
}

// write 落一份完整字节（请求体一类，一次到手的）。
func (r *dumpRecord) write(suffix string, body []byte) {
	if r == nil {
		return
	}
	if err := os.MkdirAll(r.d.dir, 0o755); err != nil {
		r.d.warn(err)
		return
	}
	if err := os.WriteFile(r.prefix+"."+suffix, body, 0o644); err != nil {
		r.d.warn(err)
	}
}

// tee 把 gin 的响应 writer 换成一份带抄写的：写给客户端的每一段同时追加进
// `.resp`。流式边到边落，进程中途死了也留下已发的那截。
func (r *dumpRecord) tee(w gin.ResponseWriter) gin.ResponseWriter {
	if r == nil {
		return w
	}
	return &teeWriter{ResponseWriter: w, rec: r}
}

func (r *dumpRecord) append(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resp == nil {
		if err := os.MkdirAll(r.d.dir, 0o755); err != nil {
			r.d.warn(err)
			return
		}
		f, err := os.OpenFile(r.prefix+".resp", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			r.d.warn(err)
			return
		}
		r.resp = f
	}
	if _, err := r.resp.Write(p); err != nil {
		r.d.warn(err)
	}
}

// close 收掉响应文件；handler 退出时 defer 调。
func (r *dumpRecord) close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resp != nil {
		r.resp.Close()
		r.resp = nil
	}
}

func (d *dumper) warn(err error) {
	d.once.Do(func() {
		d.log.Warn("排障采样写盘失败（只报这一次，转发不受影响）", "dir", d.dir, "err", err)
	})
}

// teeWriter 只截 Write / WriteString 两个口；Flush、Header、WriteHeader 与
// http.ResponseController 要的 Unwrap 都交给里面那层，写超时纪律不受影响。
type teeWriter struct {
	gin.ResponseWriter
	rec *dumpRecord
}

func (t *teeWriter) Write(p []byte) (int, error) {
	n, err := t.ResponseWriter.Write(p)
	if n > 0 {
		t.rec.append(p[:n])
	}
	return n, err
}

func (t *teeWriter) WriteString(s string) (int, error) {
	n, err := t.ResponseWriter.WriteString(s)
	if n > 0 {
		t.rec.append([]byte(s[:n]))
	}
	return n, err
}

// Unwrap 让 http.NewResponseController 穿过这一层找到真正的 writer（SetWriteDeadline）。
func (t *teeWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }
