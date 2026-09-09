package upstream

import "github.com/SimonGino/portage/internal/protocol"

// Route 是 Do 发一次请求所需的**全部**渠道侧输入：根地址 + 协议 + 凭证池 + 认证头
// 写法 + 并发上限（#55，架构评审卡 7）。
//
// 此前 Do 直接收 store.Candidate——一整张 DB 行，连四价、输入上限、能力位这些与发
// 请求无关的列一起带着。后果是 key 层内环、跨凭证预算、并发闸这几段全仓最深的并发
// 码只能从 HTTP + SQLite 外面测（server 包的集成用例），本包一个用例都没有。收成
// 一个值之后用例在本包就地造得出来（见 do_internal_test / gate_internal_test），
// 而 store 那边解析候选的逻辑一个字不动——它只多了一次映射（server.routeOf）。
type Route struct {
	// ChannelID 是并发闸按渠道聚合的 key（口径层 v0.49）。用 id 不用名字：渠道改名
	// 不该把在闸上排着的请求劈成两个池子。
	ChannelID int64
	// ChannelName 只服务日志、错误文案与轮询游标的 key。
	ChannelName string
	// Protocol 是这次选定的上游协议：拼子路径、挑协议头都只看它。
	Protocol protocol.Protocol
	// BaseURL 是协议子路径之前的那一段（见 buildURL）。
	BaseURL string
	// AuthScheme 是认证头写法（口径层 v1.13）：default / bearer / raw，词表在 store。
	AuthScheme string
	// KeyMode 是凭证选取模式：polling（默认）/ random，词表在 store。
	KeyMode string
	// Credentials 是渠道当下全部启用凭证，按 id 升序（口径层 v0.38）。用哪一份、
	// 失败换不换由本包定。
	Credentials []Credential
	// MaxConcurrency 是渠道级 in-flight 并发上限（口径层 v0.49）：0 = 不限。
	MaxConcurrency int
}

// Credential 是一份可用的上游凭证。只带名与值：名进流水的按凭证归因，值进认证头。
type Credential struct {
	Name  string
	Value string
}
