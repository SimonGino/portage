# Research：参考仓库对含 base64 请求的估算与限制做法（#79）

调研日期 2026-08-31。事实源全部为本机参考仓库源码（名单见 `docs/agents/reference-repos.md`，本机其他 fork 未参考），逐条带文件路径与代码片段；查不到的明说查不到。服务于 #77「输入上限（估算）对 base64 大段的估法升级」裁决（前置口径 v0.99 ②字节÷4 不解析、⑦图片高估为已知边界）。

## 总览：五种流派

| 仓库 | 对 base64 图的 token 估法 | 是否解码 | 兜底值 | 许可证 |
| --- | --- | --- | --- | --- |
| litellm | detail=auto/low → **恒定 85**；仅 high 解码走 tile 公式 | 仅 high | 尺寸解不出 → 按 300×300；开关兜底 250 | MIT（`enterprise/` 目录例外） |
| new-api | detail 归一为 high → 解码取宽高 → tile/patch 公式 | 是（默认仅流式） | 解不开 → 3×base（255）；**非 OpenAI 模型固定 520** | **AGPL-3.0** |
| sub2api | **只计 data URI 头，base64 载荷整段不计** | 否 | 无（图≈0） | LGPL-3.0 |
| opencodex | **嗅前 64KB 取尺寸 → pixels/750；嗅不出 → 解码字节÷512；保底 256** | 只解前 64KB | min 256 | MIT |
| CLIProxyAPI | image/audio/video 块**整块跳过计 0**，优先走上游 count_tokens | 否 | 无 | MIT |

| 仓库 | 请求大小闸 |
| --- | --- |
| litellm | 有但 enterprise-gated 默认关（`max_request_size_mb`，先看 Content-Length） |
| new-api | **全局有**：`http.MaxBytesReader`，`MAX_REQUEST_BODY_MB` 默认 128MB（解压后）；匿名 `/api/` 路由另有 512KB |
| sub2api | **双档**：`max_body_size` 默认 256MB（可带图端点）、`text_max_body_size` 默认 32MB（纯文本端点） |
| opencodex | 数据面解压后 256MB（Content-Length 提前拒 + 流式兜底），管理面 4MB |
| CLIProxyAPI | 基本没有（仅一个 handler 16MB LimitReader，主 relay 无界） |

## 一、litellm：默认档对图恒定 85，根本不看 base64

入口链：`litellm/litellm_core_utils/token_counter.py` — `token_counter`（:360）→ `_count_content_list`（:705，`type=="image_url"` 分发）→ `_count_image_tokens`（:580，读 `detail`，缺省 `"auto"`；字符串形式 image_url 一律按 auto）→ `calculate_img_tokens`（:280）：

```python
def calculate_img_tokens(data, mode="auto", base_tokens: int = 85, use_default_image_token_count=False):
    if use_default_image_token_count:
        return DEFAULT_IMAGE_TOKEN_COUNT          # 250
    if mode == "low" or mode == "auto":
        return base_tokens                        # 恒定 85，不解码不下载
    elif mode == "high":
        width, height = get_image_dimensions(data=data)
        ...
        tile_tokens = (base_tokens * 2) * tiles_needed_high_res
        total_tokens = base_tokens + tile_tokens  # 85 + 170 × tiles
```

- **默认路径（auto）对 base64 图与 http 图完全一致：恒定 85 token**。只有显式 `detail:"high"` 才解码/下载。litellm 在默认档对「base64 撑爆估算」天然免疫。
- base64 判定是「else 兜底」不是前缀匹配：`get_image_dimensions`（:202）只判 `data.startswith(("http://","https://"))`，其余一律 `data.split(",", 1)` 取逗号后段 `base64.b64decode`，data URI 头丢弃。**整段解码**（无长度截断），再按魔数嗅 PNG/GIF/JPEG/HEIC/WEBP（`get_image_type`，:179）。
- 尺寸解不出 → 返回 `DEFAULT_IMAGE_WIDTH/HEIGHT = 300×300`（constants.py:78-79），走 high 档合 `85+170×4=765`。解码抛异常由 `_count_content_list` 的 `except Exception` 接，仅传了 `default_token_count` 才有兜底否则 raise。
- 常量（`litellm/constants.py`）：`DEFAULT_IMAGE_TOKEN_COUNT=250`、`MAX_TILE_WIDTH/HEIGHT=512`、`MAX_SHORT_SIDE_FOR_IMAGE_HIGH_RES=768`、`MAX_LONG_SIDE_FOR_IMAGE_HIGH_RES=2000`、`MAX_IMAGE_URL_DOWNLOAD_SIZE_MB=50`。
- 请求大小闸：`litellm/proxy/auth/auth_utils.py:621` `check_if_request_size_is_safe` — `max_request_size_mb`，先信 `Content-Length` 没有才读 body；**enterprise-gated，非付费直接放行**。

### litellm 的三个 base64 识别器（MIT，可直接用）

1. 严格 data URI 正则（`litellm_core_utils/logging_utils.py:42`）：
   ```python
   _DATA_URI_RE = re.compile(r"data:([^;]+);base64,([A-Za-z0-9+/=]+)")
   ```
   配套日志截断阈值 `MAX_BASE64_LENGTH_FOR_LOGGING = 64` 字符，字节估算 `num_bytes = num_chars * 3 / 4`。
2. 限 MIME 大类版（`integrations/custom_logger.py:63`、`integrations/sqs.py:35`）：
   ```python
   re.compile(r"data:(?:application|image|audio|video)/[a-zA-Z0-9.+-]+;base64,[A-Za-z0-9+/=\s]+", re.MULTILINE)
   ```
3. 布尔判定器 `is_base64_encoded`（`litellm/utils.py:7741`）：**强制要求 `data:` 前缀**（注释：防 `s='Dog'` 这类误判），再 `b64decode(validate=True)` + 重编码比对。

### 许可证

根 `LICENSE`：`enterprise/` 目录下内容归 BerriAI Enterprise License（目录确实存在，`enterprise/LICENSE.md`，生产使用需订阅），**其余 MIT**。本次涉及的 `token_counter.py`、`utils.py`、`constants.py`、`logging_utils.py`、`integrations/*` 全在 enterprise 之外，MIT 适用。

## 二、new-api：全局 128MB 闸 + 非 OpenAI 图固定 520（AGPL，只当事实源不抄码）

### 大小闸（三道）

1. **relay 全链路解压后上限**：`middleware/gzip.go:26` `DecompressRequestMiddleware`，挂在 `router/relay-router.go:15` 所有 relay 路由；gzip/未压缩一律 `http.MaxBytesReader(c.Writer, body, maxBytes)`；`MAX_REQUEST_BODY_MB` 默认 **128**（`common/init.go:181`，注释明写防 zip bomb / 内存暴涨）。
2. body 落存储时二道闸：`common/gin.go:59` + `body_storage.go:167`，`io.LimitReader(reader, maxBytes+1)` 的「+1 探测」法，超限报 `ErrRequestBodyTooLarge`。
3. 匿名 `/api/` 路由独立小闸：`middleware/request_body_limit.go`，默认 **512KB**（`common/request_body_limit.go:5`），超限 413。

### token 计数

`service/token_counter.go:171` `EstimateRequestToken`。**媒体固定值兜底**（:276-296）：

```go
case types.FileTypeImage:
    if common.IsOpenAITextModel(model) { token, err := getImageToken(...) ... } else { tkm += 520 }
case types.FileTypeAudio: tkm += 256
case types.FileTypeVideo: tkm += 4096 * 2
case types.FileTypeFile:  tkm += 4096
default:                  tkm += 4096
```

**非 OpenAI 模型的图直接固定 520，完全不看 base64 内容**；音频 256、视频 8192、文件/未知 4096。

- `getImageToken`（:22）：detail=low → 85；`GET_MEDIA_TOKEN=false` 或非流式（`GET_MEDIA_TOKEN_NOT_STREAM` 默认 false）→ **3×base 固定值**；否则 **`auto`/空归一成 `high` 去解码**（与 litellm 的 auto≈low 正相反，两家最大分歧）。尺寸解不出 → 3×base（255）或直接报错。tile 公式与 OpenAI 文档一致（fit 2048 → 短边 768 → 512px tiles，`tiles*tileTokens + baseTokens`）；各模型 base/tile 表（:60-77）：4o-mini 2833/5667、gpt-5* 70/140、o1/o3 75/150、4.1/4o/4.5 85/170；patch 类（4.1-mini ×1.62、4.1-nano ×2.46、o4-mini ×1.72 等）32×32 patch 上限 1536。
- base64 识别同样是「非 http/https 前缀即 base64」（`relaykit/types/file_source.go:134` `NewFileSourceFromData`）。data URI 头解析用 `strings.Index(s, ",")` 切逗号（`service/file_service.go:318`；`service/image.go:20` 更宽松连 `data:` 都不判）。日志标识符截断：base64 超 50 字符截断（`file_source.go:104`）。
- 非 tiktoken 文本估算器 `service/token_estimator.go`：按厂商字符类权重表（Claude：CJK 1.21/Word 1.13/Symbol 0.4…），`CountTextToken` 对非 OpenAI 模型走估算。

### 许可证

根 `LICENSE` 为 **GNU AGPL-3.0**（五仓库最严，含网络分发条款 §13）。**只作公式与常量的事实源（tile 公式、固定值本身是 OpenAI 公开定价事实），不复制其 Go 代码。**

## 三、sub2api：载荷整段不计 + 双档大小闸 + 最好的启发式识别器（LGPL，识别器需重写）

- **token 策略**：`backend/internal/service/openai_gateway_count_tokens.go:517` `estimateOpenAIInputImageText` — data URI **只保留逗号前的头**（`data:image/png;base64`）过 tokenizer，**base64 载荷直接不计**；普通 URL 整串计。前缀判断用 `strings.ToLower` 大小写不敏感。Gemini 侧同策略：`estimateGeminiCountTokens` 只遍历 text parts，inlineData 整个不看；文本估算 `estimateTokensForText`（`gemini_messages_compat_service.go:2534`）：ASCII 占比 ≥0.8 → 字符÷4，否则 CJK 按 1 rune≈1 token。
- **大小闸**：15 行中间件 `middleware/request_body_limit.go`（`http.MaxBytesReader`）双档挂载（`routes/gateway.go:36`）：可带图端点 `max_body_size` 默认 **256MB**、纯文本端点 `text_max_body_size` 默认 **32MB**（`config.go:2295`），校验强制 text 档 ≤ 通用档。错误消息格式化 `handler/request_body_limit.go`（`errors.As` 出 `*http.MaxBytesError`）。上游响应侧 128MB 上限的注释给了膨胀率事实：**base64 膨胀 33%，单张 4K PNG 最坏约 67MB base64**（`config.go:60`）。

### 识别器（本题 Q4 最佳答案，LGPL——建议独立重写）

**(a) 阈值 + 字符类判定** `looksLikeMediaPayload`（`internal/securityaudit/prompt_snapshot.go:374`）：

```go
if strings.HasPrefix(lower, "data:image/") || strings.HasPrefix(lower, "data:video/") ||
    strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") { return true }
if len(trimmed) >= 256 {
    for _, r := range trimmed {
        alphaNumeric := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
        if !alphaNumeric && r != '+' && r != '/' && r != '=' { return false }
    }
    return true
}
return false
```

要点：**长度阈值 256 字符**（短于此即使全是 base64 字符也不判定，防误伤普通单词）；手写字符范围而非正则（零分配）；data URI/URL 前缀快速通道在前。

**(b) 流式扫描器**（`internal/pkg/geminicli/sanitize.go`）：循环 `strings.Index(s, ";base64,")` 找锚点，从锚点后连续吃 base64 字符（`isBase64Char`：`A-Z a-z 0-9 + / =`），超 50 字符截断。**锚点用 `;base64,` 而非 `data:`，命中任意 MIME**。`isBase64Char` 是最干净的可复用字符类谓词。

**(c)** 朴素全量 `base64.StdEncoding.DecodeString` 验证（`gemini_messages_compat_service.go:2859`）——大载荷有成本，不如 (a)(b)。

### 许可证

**LGPL-3.0**。直接复制源码片段构成派生作品；`looksLikeMediaPayload`/`isBase64Char` 这类几行字符类谓词表达方式极其有限，建议独立重写而非复制。

## 四、opencodex：与 #77 完全同题的现成修复（MIT，首选参考）

**唯一明确针对「base64 撑爆 char-based 估算」做过分析并修复的仓库**，注释自带量级论证（`src/server/claude-messages.ts:891`）：

> one 2MB screenshot is ~2.7M base64 chars, which the plain chars/token divide reports as hundreds of thousands of tokens versus a real cost around 1.6k

（与 #77 起因样本同构：误差约两个数量级。）

### 估算器（`src/server/claude-messages.ts:881-940`）

```typescript
function estimateBase64AttachmentTokens(data: string): number {
  const dims = sniffImageDimensions(data);
  if (dims) return Math.max(256, Math.ceil((dims.width * dims.height) / 750));   // Anthropic ≈ pixels/750
  const unpadded = data.endsWith("==") ? data.length - 2 : data.endsWith("=") ? data.length - 1 : data.length;
  return Math.max(256, Math.ceil(Math.floor((unpadded * 3) / 4) / 512));         // 解码字节 ÷ 512
}
```

三层：嗅尺寸 → pixels/750；嗅不出 → **解码字节数÷512**（padding 精确处理）；一律 **min 256** 保底。同形估算器 `estimateKiroImageTokens`（`src/adapters/kiro.ts:170`）。

`estimateClaudeRequestTokens` 的「挖空再 stringify」模式：把 image/document 块的 `source.data` 置空、按附件估算另计，再整体 JSON.stringify 按字符估。**关键区分：只在协议内容位挖空**（message content blocks + `tool_result.content` 递归）；`tool_use.input` 与 tool schema 里长得像附件的 JSON **继续按文本计**——那些字节确实会序列化进下游 function_call arguments。这个区分容易漏，值得照抄。

### 尺寸嗅探器（`src/adapters/anthropic-image-guard.ts:118`）

只 slice **前 64KB base64 字符**（对齐到 4 的倍数防 `atob` 炸）再解码，纯 TS 无依赖嗅 PNG/JPEG/GIF/WebP（含 VP8X/VP8/VP8L；JPEG 按段长跳 EXIF/APPn 找 SOF）。嗅不出返回 null，**调用方按「未知即在限内」处理，绝不因证明不了超限而丢图**。比 litellm 整段解码高效数量级。

### 上游硬闸复刻（同文件 :19-45，每条带上游依据）

- **Anthropic 单图 5MiB 上限的计量单位是 base64 字符串长度而非解码字节**（`MAX_IMAGE_BASE64_LENGTH = 5*1024*1024`；按解码字节比会放过 base64 长度在 (5.24MiB, 6.99MiB] 的图吃 400）。
- **base64 是单字节 ASCII → base64 字符数 ≈ 序列化后 body 字节数**——用 base64 长度估 body 占比是精确而非近似。总图预算 20MiB（对 Anthropic ~32MB/413 上限留余量）。
- 超限图替换为说明性文本占位（`[image omitted: ...]`），不静默丢。
- 其他量级事实：**约 12 张全分辨率截图的 base64 即超 64MB 解压后大小**（`src/server/request-decompress.ts:16`）。

### 大小闸与 data URI 正则

- 数据面 `MAX_DECOMPRESSED_BODY_BYTES = 256MB`（解压后，防 zstd bomb/OOM）；`readBoundedJsonRequestBody`：Content-Length 诚实超限提前拒 + 流式 reader 兜住撒谎声明；管理面 4MB。
- data URI 正则四种写法（`src/adapters/image.ts:9` 最严格）：`/^data:([^;,]+);base64,(.*)$/s` ——**全部带 `s`（dotAll）标志：base64 载荷可能含换行，不加 `s` 的 `.` 匹配失败**，实际坑。
- 字符数反推字节：`Math.floor(len * 3 / 4)`（粗）或 `(normalized.length/4)*3 - padding`（精，`src/images/artifacts.ts:124`）。
- 文本估算 `src/lib/token-estimate.ts`：默认 4 字符/token，CJK 占比 >0.3 时降到 2.5；`cjkRatio` 对长文本 stride 抽样（上限 2048 次）保 O(1)——对含大段 base64 的输入很关键。

### 许可证

标准 **MIT**（Copyright (c) 2026 opencodex contributors）。最宽松，最适合直接借鉴。

## 五、CLIProxyAPI：媒体块计 0 + 无请求闸（本题无可抄先例）

- `internal/runtime/executor/helps/claude_input_tokens.go:172`：`case "image", "input_audio", "audio", "video", "redacted_thinking": return` —— **媒体块整块跳过计 0**；document 块 `source.type != "text"` 整块跳过（**白名单 type**，对未知 base64 承载默认不计，比黑名单安全）。架构上**能用上游真 count_tokens 就不本地估**（`claude_executor_tokens.go:21`，仅 Anthropic 一方 base URL 走上游），本地估算只是第三方网关降级路径。隐患：未知块类型落 `case "":` 会整段 `json.Compact` 计入。
- 请求大小闸**基本没有**：全仓库无 `MaxBytesReader`；唯一入站 LimitReader 是单个 handler 16MB（`internal/api/server_routes.go:317`），主 relay 无界。
- base64 无识别器，只有 translator 机械拆分 `strings.SplitN(trimmed, ";base64,", 2)`。
- 许可证：标准 **MIT**。

## 六、对 #77 裁决的输入要点

1. **固定值量级**：各家对「不解码的图」取值——litellm 85（auto/low）/ 250（全局开关）、new-api 520（非 OpenAI 图）/ 256（音频）/ 4096（文件）、opencodex min 256 + 字节÷512、sub2api 与 CLIProxyAPI 计 0。#77 起因样本（真实开销 2~4k token 的 2048×1227 PNG）落在 opencodex 公式 `2048×1227/750 ≈ 3351` 的正中；若不嗅尺寸，其 1.42MB base64 按「解码字节÷512」≈ 2080,同量级。**「解码字节÷512 + 保底 256」是唯一不解码也能给出正确量级的现成公式**（÷4 对同一段给 ~372k）。
2. **识别形态**：两条现成路线可组合——data URI 锚点（`;base64,` 找锚 + 连续吃 base64 字符类,命中任意 MIME,sub2api 形态）与裸大段启发式（**长度 ≥256 全为 `A-Za-z0-9+/=` 判定**,sub2api 形态）。与 v0.99 ②「不解析」兼容：都是对原始字节的线性扫描,无需 JSON 解析。
3. **大小闸先例**：网关普遍有解压后 body 上限（128MB / 256MB+32MB 双档 / 256MB），机制统一是 `http.MaxBytesReader` 或 LimitReader+1 探测；这与 token 估算闸互补而非替代。
4. **许可证义务**：可放心抄的是 opencodex 与 CLIProxyAPI（MIT，保留版权声明与许可全文即可，不因静态链接传染）与 litellm 非 enterprise 部分（MIT）；sub2api（LGPL-3.0）的几行字符类谓词独立重写；new-api（AGPL-3.0）只当事实源不复制代码。
