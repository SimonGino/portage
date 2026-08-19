# README 截图

README 里的截图段落目前是注释掉的（`README.md` / `README.zh-CN.md` 的「管理端」一节）。
把下面三张图放进本目录，再把那段注释取消掉即可。

| 文件名 | 页面 | 要点 |
| --- | --- | --- |
| `admin-models.png` | 模型页（`/admin/channels`） | 左栏渠道列表 + 一个渠道的纳管模型与凭证池计数 |
| `admin-logs.png` | 调用记录 | 九列流水，看得出模型／渠道／凭证／token／耗时 |
| `admin-rankings.png` | 排行 | 节律带 + 环形图，最能体现「表格即证据」那套排版 |

**入库前必须脱敏。** 真机截图带真实渠道名与掩码 key，`.gitignore` 里
`design-demos/redesign/shots/live-*.png` 那条挡的就是这个。截图前把渠道改成
示例名（`anthropic-official`、`openai-relay` 之类），或者直接用 `seed.db` 起一份干净实例。

宽度建议 1600px 以内，PNG；三张并排放在表格里，GitHub 会自动缩到列宽。
