# responses-siwc-evidence —— Sign in with ChatGPT 通道的真机证据（#205，2026-10-08）

不是 Tap 样本（`golden_test.go` 不扫这里），是口径裁决要引用的**原始字节**：OpenAI 官方 `api.openai.com`，
动态注册 client + `chatgpt.tokens.use.direct` scope 的 Bearer 直打。token 值已剥、长度保留。

| 文件 | 状态 | 说明 |
| --- | --- | --- |
| `models.json` | 200 | `GET /v1/models`：顶层是 `models[]` 不是 `data[]`；`slug` 作请求名、`display_name` 展示、`visibility` ∈ {list, hide}；`context_window`、`input_modalities`、`supported_reasoning_levels` 可用 |
| `reject-stream-false.json` | 400 | `{"detail":"Stream must be set to true"}` |
| `reject-store-true.json` | 400 | `{"detail":"Store must be set to false"}` |
| `reject-system-role.json` | 400 | `{"detail":"System messages are not allowed"}` |
| `reject-temperature.json` | 400 | `{"detail":"Unsupported parameter: temperature"}` |
| `reject-previous-response-id.json` | 400 | `{"detail":"Unsupported parameter: previous_response_id"}` |
| `reject-input-string.json` | 400 | `{"detail":"Input must be a list"}`——`input` 字符串形态也拒 |
| `token-authorization-code.redacted.json` | 200 | 换 code 的响应：`access_token`(3600s) / `refresh_token` / `id_token` / `scope` / `token_type` / `earliest_refresh_at` |
| `token-refresh.redacted.json` | 200 | refresh 响应：**refresh_token 轮换**；`earliest_refresh_at` = 签发后 55 分钟，但签发后立刻 refresh 也成功（它是建议值不是闸） |
| `callback-query.redacted.json` | — | 回调 query 的键：`code` / `scope` / `state` / **`client_id`**（动态注册签发，形如 `oaiapp_…`） |

**拒绝形态不是标准 OpenAI 错误体**：全部是 `{"detail": "<句子>"}`，没有 `error.type` / `error.code`，
`Content-Type: application/json`。`max_output_tokens=50` **没被拒**（200），与官方 D6「丢弃」清单不符——按清单丢仍是对的，但它不是硬拒项。
