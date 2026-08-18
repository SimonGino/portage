package calllog

// 本文件是「边转发边攒一段字节」这件事：两个用途（log_bodies 的请求/响应体、
// 上游错误原文），三个上限，一条截断约定。上限差两个数量级，所以由构造处给。

// bodyCaptureLimit 是 log_bodies 打开时单侧 body 的记录上限。开这个开关是为了排障，
// 不是为了留档；一条长流的完整响应进日志只会把日志冲垮。
const bodyCaptureLimit = 64 << 10

// errorDetailLimit 是**落库**的上游错误原文上限（口径层 v0.53）。比 body 那个小两个
// 数量级：错误体是给人读的一段话，2KB 之后基本只剩上游自己的请求快照与堆栈；而
// 这一列每条失败流水都占一份，长期躺在库里。
const errorDetailLimit = 2 << 10

// upstreamErrorLimit 是上游错误原文的**内存**上限：转换路径读到这么多就停，透传路径的
// 旁路收集器收到这么多就停。它防着一个巨大的 HTML 错误页把内存吃满。
//
// 比落库那个上限大两个数量级是口径层 v0.74 要的：`request_id` 在 Anthropic 错误信封里
// 排在 `error` 对象**之后**，超长错误原文按 2KB 截完正好把它截在外面，而取键必须在
// 截断前的字节上做。所以内存里收完整的一段，落库那一刻再截（captureWriter.truncatedTo）。
const upstreamErrorLimit = 64 << 10

// truncationMark 是截断说出口的那句。截断这件事本身要说出来——否则一段被砍掉一半的
// JSON 看起来像上游发了个坏包。
const truncationMark = "…[truncated]"

// captureWriter 是挂在旁路上的定量收集器。和 Tap 一样：永不报错，否则 io.MultiWriter
// 会把错误变成读错误、打断转发。
//
// limit 由构造处给，因为两种用途的合理上限差两个数量级（见 bodyCaptureLimit 与
// errorDetailLimit）。
type captureWriter struct {
	limit     int
	buf       []byte
	truncated bool
}

func newCapture(limit int) *captureWriter { return &captureWriter{limit: limit} }

func (c *captureWriter) Write(p []byte) (int, error) {
	if room := c.limit - len(c.buf); room > 0 {
		if len(p) > room {
			c.buf = append(c.buf, p[:room]...)
			c.truncated = true
		} else {
			c.buf = append(c.buf, p...)
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

func (c *captureWriter) String() string {
	if c.truncated {
		return string(c.buf) + truncationMark
	}
	return string(c.buf)
}

// collected 是收下的原始字节，未经二次截断。给 request_id 取键用（口径层 v0.74）：
// 那个键排在错误信封末尾，只有完整字节上取得到。
func (c *captureWriter) collected() []byte { return c.buf }

// truncatedTo 按一个更小的上限二次截断。错误原文用得到它：内存里要留完整字节给
// request_id 取键（v0.74），落库只留 2KB（v0.53）。
func (c *captureWriter) truncatedTo(limit int) string {
	if len(c.buf) > limit {
		return string(c.buf[:limit]) + truncationMark
	}
	return c.String()
}
