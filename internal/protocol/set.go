package protocol

import (
	"errors"
	"fmt"
	"strings"
)

// Set 是一个渠道能说的上游协议集合（口径层 v0.33「支持协议集」）。
//
// 取代此前 `channels.protocol` 那个单值列。同一个上游账号只建一个渠道：`base_url`
// 存的是协议子路径之前的前缀，CC 与 Responses 共用它、只差 `/v1/chat/completions`
// 与 `/v1/responses` 这个后缀，拆两个渠道等于把同一份凭证存两遍，还把协议名顶到了
// 对外限定名 `渠道名/模型名` 的最外层。
//
// 有序切片而不是 map：元素最多三个，`Has` 的线性扫描比哈希快；而顺序要稳定——它要
// 原样存回库、原样显示在管理端，用 map 每次 String() 出来的次序都不一样。
type Set []Protocol

// fallbackOrder 是入站协议不在集合里时的转换目标优先级（口径层 v0.33）。
//
// CC 优先：三者里字段最贫，转过去的信息损失最可预测，且 6 条转换路径里 CC 侧最先
// 落地、golden 样本最全。**复议触发条件**见口径层 §2.3——A→Responses 若日后能保住
// reasoning（Responses 有 reasoning item 而 CC 没有），这个顺序要重排。
var fallbackOrder = []Protocol{OpenAI, OpenAIResponses, Anthropic}

// ParseSet 读库里那一列：逗号分隔，容忍空格，保序去重。
//
// 单值解出来就是一元集合——v0.33 之前写进库的单值 `protocol` 列因此不用改值，含义
// 一字不变。旧协议名（v0.36 之前的 `openai_cc`）在这里折成现名：库里的存量行由
// store.migrate 一次性改写，这一步兜的是迁移之外的入口（手写配置、环境变量），以及
// 去重——`openai,openai_cc` 折完是同一个值，得只留一个。
func ParseSet(s string) (Set, error) {
	var out Set
	for _, part := range strings.Split(s, ",") {
		p := Normalize(Protocol(strings.TrimSpace(part)))
		if p == "" {
			continue
		}
		if !p.Valid() {
			return nil, fmt.Errorf("协议 %q 不是 anthropic/openai/openai_responses 之一", p)
		}
		if !out.Has(p) {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("支持协议集不能为空")
	}
	return out, nil
}

// String 是 ParseSet 的逆：写回库、写进日志都用它。
func (s Set) String() string {
	parts := make([]string, len(s))
	for i, p := range s {
		parts[i] = string(p)
	}
	return strings.Join(parts, ",")
}

func (s Set) Has(p Protocol) bool {
	for _, q := range s {
		if q == p {
			return true
		}
	}
	return false
}

// Intersect 求两个集合的交集，保 s 的顺序（口径层 v0.40）。
//
// 用途只有一个：纳管模型声明了自己的协议子集时，本次请求真正能走的是「渠道支持的」
// 与「模型支持的」都有的那些。保 s 的顺序是因为调用方传的 s 是渠道集——它是存回库、
// 显示在管理端的那一份，交集只服务于本次路由，跟着它走读起来最不意外。
//
// 结果可能为空，且**空是合法结果**，不是错误：渠道协议集缩小之后，模型上那份没跟着
// 改的子集就会与它不再重合。调用方据此把候选当「现在用不了」排除（与渠道停用、凭证
// 归零同一档），不是当库坏了报 500——见 store.pickProtocol。
func (s Set) Intersect(other Set) Set {
	var out Set
	for _, p := range s {
		if other.Has(p) {
			out = append(out, p)
		}
	}
	return out
}

// Choose 定这次请求打上游哪个协议（口径层 v0.33 的出站协议选择规则）。
//
// 入站协议在集合里就用它——**能透传就透传**。这不是性能优化：跨协议转换按 v0.10
// 丢弃 thinking，同一个模型走透传能留 reasoning、走转换就丢，所以「用哪个上游协议」
// 对用户可感知，不能随机挑，也不该逼用户把协议写进模型名里自己标。
//
// 入站不在集合里才回退，按 fallbackOrder 取第一个命中的。集合非空由 ParseSet 与启动
// 闸共同保证，ok=false 只会在调用方自己造了个零值 Set 时出现。
func (s Set) Choose(inbound Protocol) (Protocol, bool) {
	if s.Has(inbound) {
		return inbound, true
	}
	for _, p := range fallbackOrder {
		if s.Has(p) {
			return p, true
		}
	}
	return "", false
}
