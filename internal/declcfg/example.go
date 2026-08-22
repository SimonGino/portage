package declcfg

import "github.com/SimonGino/portage/internal/store"

// Example 是 channels.example.yaml 的数据源：一份假但**能真跑通**的配置。
//
// 示范文件由导出器生成而不是手写（口径层 §2.9 #34）。手写的示范没有闸护着，字段
// 改名时会静默过期——而在纯转发形态下它是次要路径唯一的参考，过期的参考比没有参考
// 更坏。让它走导出器，往返闸就顺带钉住了它：字段改了名而这里没跟上，测试当场红。
//
// 里面那把 API Key 是 PlaceholderAPIKey，于是**这份文件直接拿去挂会被拒启**
// （§2.9 #28）。这是要的效果：它公开在仓库里，能用就等于把门钥匙印在了 README 上。
func Example() *File {
	yes, no := true, false
	w := func(n int) *int { return &n }
	return &File{
		Channels: []Channel{
			{
				Name: "anthropic",
				// 每协议一份出站根地址（口径层 v0.96）：给哪个协议填了地址就是声明
				// 了哪个协议，不再单写 protocols。
				BaseURL:                   store.BaseURLs{Anthropic: "https://api.anthropic.com"},
				CredentialType:            "api_key",
				KeyMode:                   "polling",
				MaxConcurrency:            0,
				SupportsCompaction:        false,
				SupportsStatefulResponses: &no,
				Disabled:                  false,
				// 凭证池：同一个渠道下多把 key 轮询。名字是它在文件里的身份，
				// 改名等于删旧建新（§2.9 #30）。
				Credentials: []Credential{
					{Name: "主号", Credential: "sk-ant-把这里换成你自己的上游-key"},
					{Name: "备号", Credential: "sk-ant-第二把-可以只有一把"},
				},
				Models: []Model{
					// protocols 留空表示继承渠道全集，这是最常见的正常值。
					{UpstreamModel: "claude-sonnet-4-20250514", Protocols: []string{}, Disabled: false},
				},
			},
			{
				Name: "自建-vllm",
				// 两个协议共用前缀就是两个键填同一个值；挂在别的根下就各填各的。
				BaseURL: store.BaseURLs{
					OpenAI:          "https://vllm.example.internal",
					OpenAIResponses: "https://vllm.example.internal",
				},
				CredentialType: "api_key",
				KeyMode:        "polling",
				// max_concurrency > 0 才有并发闸；自建推理服务超发会排队甚至拖垮。
				MaxConcurrency:            8,
				SupportsCompaction:        false,
				SupportsStatefulResponses: &yes,
				Disabled:                  false,
				Credentials: []Credential{
					{Name: "内网", Credential: "sk-把这里换成你自建服务的-token"},
				},
				Models: []Model{
					// 这个模型只走 openai，虽然渠道两个协议都支持。
					{UpstreamModel: "Qwen3-32B", Protocols: []string{"openai"}, Disabled: false},
					// 停用的纳管模型留在文件里，只是不参与路由。
					{UpstreamModel: "Qwen2.5-72B-旧的", Protocols: []string{}, Disabled: true},
				},
			},
		},
		// 接入点是客户端看见的模型名，候选指向 `渠道名/纳管模型名`。
		// M4 之前每个启用的接入点只能有 1 个权重大于 0 的候选。
		AccessPoints: []AccessPoint{
			{
				Model:      "claude-sonnet-4",
				Disabled:   false,
				Candidates: []Candidate{{Target: "anthropic/claude-sonnet-4-20250514", Weight: w(100)}},
			},
			{
				Model:      "qwen",
				Disabled:   false,
				Candidates: []Candidate{{Target: "自建-vllm/Qwen3-32B", Weight: w(100)}},
			},
		},
		APIKeys: []APIKey{
			{
				Name:          "默认",
				Key:           PlaceholderAPIKey,
				AllowedModels: []string{"*"},
				Disabled:      false,
			},
			{
				// allowed_models 给具体名单就只放行名单里的接入点。
				Name:          "只放行-qwen",
				Key:           "sk-ptg-换成你自己生成的第二把",
				AllowedModels: []string{"qwen"},
				Disabled:      false,
			},
		},
	}
}
