package server

import (
	"net/http/httptest"
	"testing"

	"github.com/SimonGino/portage/internal/calllog"

	"github.com/gin-gonic/gin"
)

// recorderFrom 取不到记录时的兜底分支，此前零覆盖：网关全链路上走不到它（走到了
// 就说明 callLog 中间件没挂上，也就是路由被改坏了），所以只能在这一层直接验。
//
// 是包内测试（package server 而不是 server_test）：recorderFrom 与 ctxCallRecord
// 都不导出，也不该为了测试导出——外面看得见的是那三层中间件，不是它们之间的键。
//
// 这里钉的是「回一个黑洞而不是 nil，也不 panic」：回 nil 的话调用方随即空指针，
// panic 的话一次路由配置错误会变成客户端眼里的 500——而它本该只是日志少一行。
func TestRecorderFromFallsBackToADetachedRecorder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name string
		set  func(*gin.Context)
	}{
		{"上下文里什么都没有：callLog 中间件没挂上", func(*gin.Context) {}},
		{"键被别的东西占了：类型断言过不去", func(c *gin.Context) { c.Set(ctxCallRecord, "不是 *Recorder") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			tc.set(c)

			rec := recorderFrom(c)
			if rec == nil {
				t.Fatal("recorderFrom 回了 nil，调用方随即会空指针")
			}
			// 沿途动词照常收，收尾哪儿也不写——不 panic 本身就是断言的一部分。
			rec.Authenticated("k", nil)
			rec.Refused(calllog.Unauthorized)
			rec.Finish(401)
		})
	}
}
