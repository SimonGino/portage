# Coding standards

Review 时读的规则集，沉淀自历次 review 裁定。CLAUDE.md 的硬约束（透传保真、保密、参考仓库、golden 闸）仍然生效，此处不重复。

- **错误分类归 adapter**：深层包（`upstream`、`exchange`、`protocol`）自立错误类型只报事实（「参数对不上渠道现状」），翻成 `store` 错误类型或 HTTP 状态码是 `admin`/`server` adapter 的事——深层包不铸 store 层错误类型。（2026-08-25，#51 复审裁定，成例 `upstream.SelectionError`。）
