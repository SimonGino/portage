package declcfg

import (
	"fmt"
	"sort"
	"strings"

	"github.com/SimonGino/portage/internal/auth"
	"github.com/SimonGino/portage/internal/store"
)

// selfCheck 是 apply 前的那道闸：**只看文件自己**，不碰库。
//
// 一次报全（口径层 §2.9 #31 ①）：所有静态可判定的问题攒齐一次报出，不首错即停。
// 这不是附赠是必需——纯转发形态下唯一的修复通道是改文件重启，首错即停会把一次重启
// 变五次。store.Validate 早有成例。
//
// **与 store.Validate 分工**：这里只管「文件自身的形状」——名字、唯一性、引用、
// 秘密纪律；base_url 合法性、protocols 值域、候选可达性那六项归 Validate，它们要看
// 的是**库里的行**，而 apply 之后那些行就是文件的物化副本，重复实现一遍只会让两处
// 判据慢慢漂移。两道闸因此不合并，但 problems 由 Apply 合并报。
func selfCheck(f *File) []string {
	var p []string
	p = append(p, checkChannels(f)...)
	p = append(p, checkAccessPoints(f)...)
	p = append(p, checkAPIKeys(f)...)
	return p
}

func checkChannels(f *File) []string {
	var p []string
	seen := map[string]bool{}
	for i, ch := range f.Channels {
		where := fmt.Sprintf("channels[%d]", i)
		name := strings.TrimSpace(ch.Name)
		switch {
		case name == "":
			p = append(p, fmt.Sprintf("%s 没有 name。名字即身份——它是自然键，也是限定名 `渠道名/上游模型名` 的前半", where))
		case seen[name]:
			p = append(p, fmt.Sprintf("渠道名 %q 重复。名字是自然键，一份文件里只能出现一次", name))
		default:
			seen[name] = true
		}
		// key_mode 的值域在这里判而不留给 Validate：Validate 的六项里没有它
		// （管理端写入时由 store.ChannelInput.keyMode 挡着，而声明文件绕过了那条路）。
		if m := strings.TrimSpace(ch.KeyMode); m != "" && m != store.KeyModePolling && m != store.KeyModeRandom {
			p = append(p, fmt.Sprintf("渠道 %q 的 key_mode=%q 不合法，只能是 %s（轮询）或 %s（随机）",
				name, m, store.KeyModePolling, store.KeyModeRandom))
		}
		// 负数并发上限同样只在这里判。拒而不是当 0 用：写 -1 的人多半以为它是某种
		// 「不限」的暗号，静默蒙对语义会让他下次写 -5 时困惑（同 store 的既有立论）。
		if ch.MaxConcurrency < 0 {
			p = append(p, fmt.Sprintf("渠道 %q 的 max_concurrency=%d 是负数：0 表示不限，正整数才是上限", name, ch.MaxConcurrency))
		}
		p = append(p, checkCredentials(name, ch.Credentials)...)
		p = append(p, checkModels(name, ch.Models)...)
	}
	return p
}

func checkCredentials(channel string, creds []Credential) []string {
	var p []string
	seen := map[string]bool{}
	for i, c := range creds {
		name := strings.TrimSpace(c.Name)
		switch {
		case name == "":
			// 凭证必须带名字（口径层 §2.9 #29 从 #28 转出的那条）：日志与用量按凭证名
			// 归因，两条都叫空串就废掉了归因本身。
			p = append(p, fmt.Sprintf("渠道 %q 的 credentials[%d] 没有 name；日志与用量按凭证名归因，不能留空", channel, i))
		case seen[name]:
			p = append(p, fmt.Sprintf("渠道 %q 里凭证名 %q 重复。凭证名在渠道内唯一（idx_channel_keys_name）", channel, name))
		default:
			seen[name] = true
		}
		if strings.TrimSpace(c.Credential) == "" {
			p = append(p, fmt.Sprintf("渠道 %q 的凭证 %q 没有值。秘密明文写进文件是这套形态的前提（口径层 §2.9 #28），没有第二个地方能补", channel, name))
		}
	}
	return p
}

func checkModels(channel string, models []Model) []string {
	var p []string
	seen := map[string]bool{}
	for i, m := range models {
		name := strings.TrimSpace(m.UpstreamModel)
		switch {
		case name == "":
			p = append(p, fmt.Sprintf("渠道 %q 的 models[%d] 没有 upstream_model", channel, i))
		case seen[name]:
			p = append(p, fmt.Sprintf("渠道 %q 里 upstream_model %q 重复（自然键是 (渠道, 上游模型名)）", channel, name))
		default:
			seen[name] = true
		}
	}
	return p
}

func checkAccessPoints(f *File) []string {
	var p []string
	// 先把文件里所有限定名收齐，候选的引用要拿它对。**只认文件、不认库**：库在
	// apply 之后就是文件的副本，拿库里的旧行放行一条候选，等于让上一版配置替这一版
	// 背书——而那一版正要被这次 apply 删掉。
	targets := map[string]bool{}
	for _, ch := range f.Channels {
		for _, m := range ch.Models {
			targets[store.QualifiedName(strings.TrimSpace(ch.Name), strings.TrimSpace(m.UpstreamModel))] = true
		}
	}
	seen := map[string]bool{}
	for i, ap := range f.AccessPoints {
		model := strings.TrimSpace(ap.Model)
		switch {
		case model == "":
			p = append(p, fmt.Sprintf("access_points[%d] 没有 model。它是客户端 model 字段里写的那个名字，也是自然键", i))
		case seen[model]:
			p = append(p, fmt.Sprintf("接入点 %q 重复。对外模型名是自然键，一份文件里只能出现一次", model))
		default:
			seen[model] = true
		}
		seenTarget := map[string]bool{}
		for j, c := range ap.Candidates {
			target := strings.TrimSpace(c.Target)
			switch {
			case target == "":
				p = append(p, fmt.Sprintf("接入点 %q 的 candidates[%d] 没有 target（限定名 `渠道名/上游模型名`）", model, j))
				continue
			case seenTarget[target]:
				p = append(p, fmt.Sprintf("接入点 %q 重复引用了候选 %q（自然键是 (接入点, 渠道纳管模型)）", model, target))
			default:
				seenTarget[target] = true
			}
			// 限定名必须 apply 前校验（口径层 §2.9 #29）：它是文件里唯一一处跨实体
			// 引用，拼错了在库里就是一条指不到东西的候选，而那要到请求时才 500。
			if !targets[target] {
				p = append(p, fmt.Sprintf("接入点 %q 的候选 %q 在这份文件里找不到对应的渠道纳管模型；"+
					"限定名是 `渠道名/上游模型名`，两半都要与上面 channels 里写的逐字相同", model, target))
			}
			if c.Weight != nil && *c.Weight < 0 {
				p = append(p, fmt.Sprintf("接入点 %q 的候选 %q 权重是负数 %d：0 表示临时摘除，正整数才是权重", model, target, *c.Weight))
			}
		}
	}
	return p
}

func checkAPIKeys(f *File) []string {
	var p []string
	// 空 api_keys 在**挂了文件**的形态下是静态可判定的错（口径层 v0.92）。
	//
	// main.go 那条「只警告不拒启」是 v0.21 通则的一个刻意例外，立论是「配 key 得先有
	// 个跑着的网关（开 /admin 或对着它建的库灌 SQL）」——挂了声明文件之后这两条路同时
	// 消失：管理面按密码闸不注册，灌 SQL 被「文件是唯一事实源」封掉。例外失去依据。
	//
	// 落在 selfCheck 而不是 main 里单独早退，就是为了跟其余问题一起一次报全：否则
	// 「一把 API Key 都没写」会盖住同一份文件里的其余错，把一次重启变两次。
	if len(f.APIKeys) == 0 {
		p = append(p, "声明文件里一把 API Key 都没有。挂了文件就意味着它是唯一事实源——"+
			"没有管理端能补建，这台网关起来之后每个转发请求都会 401")
	}
	seenName := map[string]bool{}
	// byHash 收「同一个明文被两条不同的 key 用了」。这一格必须在这里点名，不能留给
	// SQLite 的 UNIQUE(key_hash)（展开层 §7.9）：**自然键(name)不冲突而 key_hash 冲突**，
	// 约束错误那句话里只有索引名没有实体名，而纯转发形态下没有第二个地方能查是哪两把。
	byHash := map[string][]string{}
	for i, k := range f.APIKeys {
		name := strings.TrimSpace(k.Name)
		switch {
		case name == "":
			p = append(p, fmt.Sprintf("api_keys[%d] 没有 name", i))
		case seenName[name]:
			p = append(p, fmt.Sprintf("API Key 名 %q 重复。名字是自然键，一份文件里只能出现一次", name))
		default:
			seenName[name] = true
		}
		key := strings.TrimSpace(k.Key)
		switch {
		case key == "":
			// 无管理面就没有「生成后看一眼」的地方（口径层 §2.9 #28 ②）：写进日志撞
			// §2.7 的秘密纪律，只落库则让「文件是唯一事实源」当场破功。
			p = append(p, fmt.Sprintf("API Key %q 没有给值。这套形态下没有别处能看到它——"+
				"自己生成一把（例如 sk-ptg-$(openssl rand -hex 16)）写进文件", name))
		case key == PlaceholderAPIKey:
			// 占位 API Key 是**公开在仓库里的门钥匙**，抄了示范文件直接部署就是把网关
			// 对全世界开着。闸只管 API Key 不管上游凭证：占位凭证只是自伤且当场可见。
			p = append(p, fmt.Sprintf("API Key %q 还是 channels.example.yaml 里那个出厂占位值。"+
				"那串明文公开在仓库里，等于没有鉴权——换成你自己生成的一把", name))
		default:
			byHash[auth.Hash(key)] = append(byHash[auth.Hash(key)], name)
		}
		// allowed_models 那一列是**逗号分隔**的（`auth.Key.Allows` 按逗号切），于是
		// 名字里带逗号在这个格式里根本表达不出来——存进去会被切成两段谁都对不上，
		// 而闸全过、页面上也看着正常。静态可判定，当场点名。
		for _, m := range k.AllowedModels {
			if strings.Contains(m, ",") {
				p = append(p, fmt.Sprintf("API Key %q 的 allowed_models 里 %q 含逗号。"+
					"这一列在库里是逗号分隔的，带逗号的名字会被切开、谁都匹配不上", name, m))
			}
		}
	}
	var dup []string
	for _, names := range byHash {
		if len(names) > 1 {
			sort.Strings(names)
			dup = append(dup, fmt.Sprintf("API Key %s 用的是同一个明文；key 值本身唯一（api_keys.key_hash 上有 UNIQUE），"+
				"两把一样的 key 在流水里也分不出是谁打的", quoteAll(names)))
		}
	}
	sort.Strings(dup)
	return append(p, dup...)
}

func quoteAll(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(out, " 与 ")
}

// parseProtocols 把文件里的协议列表拼成 DDL 那一列的逗号分隔形态。
//
// **不在这里校值域**：protocols 的合法性归 store.Validate 的 checkChannelFields /
// checkModelProtocols，它们已经逐项 ParseSet 过且措辞讲究（尤其纳管模型那一列，空串
// 是「继承渠道全集」这个最常见的正常值）。这里只做形态转换，把非法值原样送进库让
// 那道闸去报——两道闸的 problems 由 Apply 合并报，人看到的仍是一次报全。
func parseProtocols(list []string) string {
	trimmed := make([]string, 0, len(list))
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			trimmed = append(trimmed, s)
		}
	}
	return strings.Join(trimmed, ",")
}
