package protocol

import (
	"log/slog"
	"strconv"
	"strings"
)

// Drops 是一次 canonical → 出口编码的丢弃登记清单（RequestEncodeReporter 交出的那份）。
//
// 从 []string 扩成带名单的结构（口径层 v1.14 ⑨）：按种类去重的清单里「55 个工具丢
// 45 个」与「丢一个 web_search」长得一模一样，排障时分不出是客户端真没声明工具还是
// 我们丢光了。工具类三档（server_tool / tool_grammar / tool_choice）附被丢的工具名；
// 其余档位维持只报种类——明细是正文内容，既大又涉隐私，日志里不该有。
//
// 谁附名单谁决定：codec 调 Add 时给不给 names。本类型不知道档位常量（那些住在各
// codec 包里），也不替调用方判断「这一档该不该带名字」。
type Drops []Drop

// Drop 是一条丢弃登记。
type Drop struct {
	// Kind 是档位（各 codec 包的 DropXxx 常量）。
	Kind string
	// 这一档下被丢的工具名，去重、按登记序、封顶 MaxDropNames 个；tool_choice
	// 那一档记的是落空的 mode（auto / none）而不是工具名——它落空时本来就没有工具
	// 可点。非工具类档位恒为空名单。
	NameList
}

// MaxDropNames 是单份名单的上限。
//
// 取 64：与 Anthropic / CC 单个工具名的长度上限同数量级，一条 Warn 最多几 KB；
// ADE 那类 55 个工具的客户端整批丢也装得下，看日志的人不必再猜后面还有谁。
const MaxDropNames = 64

// NameList 是一份封顶的去重名单：按登记序留名，同名只留一条，超过 MaxDropNames
// 之后只计数不留名。
//
// 抽出来是因为「谁被丢了 / 谁被救治了」这类登记在本项目里有四份形状相同的（出口侧
// Drops 的工具名、入口侧的残缺入参与折算字段、响应侧放不出去的 item），封顶的理由
// 也是同一个：它们直接进一行 Warn，而长度由**入站请求或上游响应**说了算——55 个
// 工具的客户端、一条流里几十种认不得的事件名，无上限等于让对方决定我们的日志有
// 多大。超出的只计数：「封顶了」与「就这么多」得分得开。
type NameList struct {
	names   []string
	omitted int
}

// Add 登记若干个名字。空名跳过，同名只留一条，封顶之后的计进 Omitted。
func (l *NameList) Add(names ...string) {
	for _, n := range names {
		if n == "" || hasName(l.names, n) {
			continue
		}
		if len(l.names) >= MaxDropNames {
			l.omitted++
			continue
		}
		l.names = append(l.names, n)
	}
}

// Names 是留住的那些名字，按登记序。调用方只读，不改。
func (l NameList) Names() []string { return l.names }

// Omitted 是封顶后没能留名的个数。
func (l NameList) Omitted() int { return l.omitted }

// Empty 报告这份名单一条都没登记过。
func (l NameList) Empty() bool { return len(l.names) == 0 && l.omitted == 0 }

// String 渲染成 `[a,b,c]`，封顶后尾随 ` +N`。空名单渲染成空串——好让 Drop.String
// 直接把它接在档位后面。
func (l NameList) String() string {
	if len(l.names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('[')
	b.WriteString(strings.Join(l.names, ","))
	if l.omitted > 0 {
		b.WriteString(" +")
		b.WriteString(strconv.Itoa(l.omitted))
	}
	b.WriteByte(']')
	return b.String()
}

// LogValue 让 slog 把名单打成一格 `calls="[a,b +6]"`，与 Drops 那份里名单的一截同形。
func (l NameList) LogValue() slog.Value { return slog.StringValue(l.String()) }

// Add 登记一条丢弃。同一 Kind 只留一条，names 并进那条并去重。
func (ds *Drops) Add(kind string, names ...string) {
	d := ds.find(kind)
	if d == nil {
		*ds = append(*ds, Drop{Kind: kind})
		d = &(*ds)[len(*ds)-1]
	}
	d.NameList.Add(names...)
}

func (ds Drops) find(kind string) *Drop {
	for i := range ds {
		if ds[i].Kind == kind {
			return &ds[i]
		}
	}
	return nil
}

// Has 报告某一档有没有登记。
func (ds Drops) Has(kind string) bool { return ds.find(kind) != nil }

// Names 取某一档的名单；没登记或没名单即 nil。
func (ds Drops) Names(kind string) []string {
	if d := ds.find(kind); d != nil {
		return d.Names()
	}
	return nil
}

// Kinds 只取档位，按登记序——给只关心「丢了哪几类」的调用方与老用例用。
func (ds Drops) Kinds() []string {
	if len(ds) == 0 {
		return nil
	}
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Kind)
	}
	return out
}

// String 把一条登记渲染成日志用的一格：没名单时就是档位本身，有名单时
// `kind[a,b,c]`，封顶后尾随 ` +N`。
func (d Drop) String() string { return d.Kind + d.NameList.String() }

// Strings 逐条渲染，供日志与测试用。
func (ds Drops) Strings() []string {
	if len(ds) == 0 {
		return nil
	}
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.String())
	}
	return out
}

// LogValue 让 slog 把清单打成一组字符串（`dropped="[thinking server_tool[a,b]]"`），
// 与扩成结构之前的形态只差名单那一截；不打成结构体字面量——那种 `{server_tool [a b] 0}`
// 没人读得出哪个是档位哪个是 Omitted。
func (ds Drops) LogValue() slog.Value {
	return slog.AnyValue(ds.Strings())
}

func hasName(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
