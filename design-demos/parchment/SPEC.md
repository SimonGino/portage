# 羊皮纸方案（Parchment）——表面换血规格

PO 2026-08-18 给了六条设计取向，并对四个岔口逐项裁定。本文是**唯一事实源**：改 `web/` 与改 `DESIGN.md` 的两条活都照这份写，hex 与职责清单不许各自发挥。

## 0. PO 给的六条取向（原话）

1. 背景固定为暖羊皮纸色 `#f5f4ed`。
2. 唯一主强调色是墨蓝 `#1B365D`，原则上不超过页面面积的 5%。
3. 灰色全部带黄褐暖调，拒绝常见 SaaS 冷蓝灰。
4. 主要依靠衬线字体、字号、间距建立层级，而不是彩色卡片和图标。
5. 禁止硬阴影、渐变、3D、装饰性图表和过量色彩。
6. 深度主要依靠边框、极弱阴影和留白。

## 1. PO 四项裁定（2026-08-18）

| # | 岔口 | 裁定 |
|---|---|---|
| 1 | 壳是否跟着换 | **壳不动，只换表面**。248px 左栏 / 五项导航 / 模型页主从（v0.75、DESIGN §2）一字不动，改动限于色彩、字体、边框、圆角、留白 |
| 2 | 暗色主题 | ~~**保双主题**，暗色不做羊皮纸，做同一族的暖调深墨。§1.5「亮/暗同等可读、跟随系统」不动~~ **⚠️ 已作废**：PO 同日（2026-08-18）改判**只做亮色，暗色主题整个删掉**，见 `DESIGN.md` v0.29 与改写后的 §1.5。原文留档，不作为实现依据 |
| 3 | 衬线范围 | **只给标题**。页标题 / 区块标题 / 渠道名走衬线；正文仍 IBM Plex Sans，标识符仍 IBM Plex Mono。**不加载中文衬线 webfont**，中文落系统宋体 |
| 4 | 墨蓝职责 | **焦点环 + 链接 + 关键数字**三件。**不**给主按钮（仍实心墨色）、**不**给导航选中态（仍 `--sunken` + 字重）、**不**做参考图那种小徽章（§9 禁令不动） |

## 2. Token 表（照抄，不要重新调色）

### 2.1 亮色

```
--paper:      #edece3   /* 左栏与页面底，比画布深一档 */
--canvas:     #f5f4ed   /* 主画布 —— PO 指定的羊皮纸色，人眼待的地方 */
--ink:        #1c1a15   /* 暖近黑，不是纯黑 */
--ink-2:      #3f3a2f
--mute:       #6b6252
--faint:      #8a8172
--line:       #ddd9c9
--line-soft:  #e7e3d5
--sunken:     #eae7da   /* 展开井 / 渠道身份条 / 导航选中底 */
--chip:       #efece0
--hover:      rgba(28, 26, 21, 0.045)
--shadow:     0 1px 2px rgba(62, 52, 30, 0.05), 0 10px 30px rgba(62, 52, 30, 0.07)
--accent:     #1B365D   /* 墨蓝，职责见 §3 */
```

语义色按暖灰基底重调（现行那套是冷调，直接放到羊皮纸上会发紫）：

```
--ok-bg:   #eaf0e0   --ok-line:   #b9c8a4   --ok-text:   #41633c
--warn-bg: #f7efd9   --warn-line: #dcc48a   --warn-text: #7a5715
--err-bg:  #f6e5df   --err-line:  #d8a89e   --err-text:  #9c2f26
--info-bg: #e9edf3   --info-line: #b6c2d2   --info-text: #1b365d
```

「信息」这一档 DESIGN §4 一直写着（语义色四个），CSS 里此前只落了三个，本次补齐。**它与强调色同源**——这套暖底上唯一的蓝就是墨蓝，信息没有理由再发明第二抹，两者靠 bg / line 与文案区分。目前没有消费者（`.field-hint` / `.picker-hint` 都是纯灰文字，不是信息条），先把档位定下来。

图表数据色（`--data-in` / `--data-out` 仍被 `Overview.tsx` 用着，虽然概览已不在导航上）改成暖调低饱和，不要 teal/magenta：

```
--data-in: #4a6b52   --data-out: #8a6a3c
```

### 2.2 暗色（`prefers-color-scheme: dark`）

> **⚠️ 本节整节已作废（2026-08-18）**：被 `DESIGN.md` v0.29 推翻——PO 同日改判管理端**只做亮色**，暗色主题连同 `styles.css` 的 `@media (prefers-color-scheme: dark)` 两块、20 份 `*.dark.svg` 与 `icons/index.tsx` 的 `DARK` 机制一并删掉。下面这张表**不再有实现落点**，只作历史留档；唯一还带暗色的是 `web/public/portage-mark-v2.svg`（favicon 在标签栏上，与页面主题是两件事，见 `DESIGN.md` §11）。

暖调深墨，色相跟亮色同一族（黄褐向），不许回到冷灰。

```
--paper:      #17150f
--canvas:     #1e1c15
--ink:        #f0ede2
--ink-2:      #ccc6b6
--mute:       #9c9585
--faint:      #7b7566
--line:       #332f24
--line-soft:  #2b281f
--sunken:     #282419
--chip:       #2c281d
--hover:      rgba(240, 237, 226, 0.055)
--shadow:     0 1px 2px rgba(0, 0, 0, 0.4), 0 10px 30px rgba(0, 0, 0, 0.45)
--accent:     #9db8dd   /* 墨蓝在深底上提亮，保住对比度 */
--ok-bg:   #1c2a18   --ok-line:   #3c5434   --ok-text:   #9bc48f
--warn-bg: #2e2408   --warn-line: #6b5314   --warn-text: #e0c078
--err-bg:  #331a14    --err-line:  #6e332a   --err-text:  #e8a094
--info-bg: #1c232c   --info-line: #3a4757   --info-text: #9db8dd
--data-in: #86a98d   --data-out: #c39a63
```

### 2.3 圆角与阴影

纸媒化收圆角——20px 大圆角是「白卡片浮在纸灰上」那套语言的一部分，和纸面不搭：

```
--r:      10px   （原 20）
--r-sm:    8px   （原 16）
--radius-sm: 6px （原 12，页头控件与按钮）
```

`--shadow` 只给**浮起来的东西**（对话框、Picker 弹层）。画布、井、身份条、表格一律无阴影——深度靠边框与留白（PO 第 6 条）。

### 2.4 字体

```
--serif: "Source Serif 4", "Songti SC", "Noto Serif CJK SC", Georgia, serif
--sans:  不变（IBM Plex Sans + 中文系统字）
--mono:  不变（IBM Plex Mono）
```

- `--display`（Outfit）**整个替换成 `--serif`**，Outfit 从 `web/index.html` 的 Google Fonts 链接里删掉，换 `Source Serif 4`（字重 400;600，含 italic 不必要就不加）。
- 中文衬线只走本机（Songti SC / Noto Serif CJK SC），**不加载中文衬线 webfont**（几 MB，PO 裁定 3 的理由）。
- 落点：页标题、区块标题、渠道身份条上的渠道名、空态首句。**表格、表单、按钮、导航一律无衬线。**

## 3. 墨蓝 `--accent` 的落点白名单

**只有这三处**，超出的一律回墨色：

1. **焦点环** `:focus-visible` —— 从 `2px solid var(--ink)` 换成 `2px solid var(--accent)`。
2. **文字链接与行内跳转** —— 可点的文字（详情、渠道名跳转之类），默认墨蓝无下划线，悬停加下划线。
3. **关键数字** —— **页面级合计 / 主指标那一个数**（排行页的 token 合计与每行主指标）。表格内的普通数字（耗时、状态码、每行 token）**仍是墨色**，不许整列刷蓝。

明确不给：主按钮（实心墨色不变）、导航选中态（`--sunken` + 字重不变）、开关、Segmented 选中、页码当前页、任何徽章/胶囊。

自查：截一屏用取色器估面积，墨蓝像素超过 5% 就是超了。

## 4. 品牌标记

`web/public/portage-mark.svg` 里的两个 hex 写死、且 `serveSPA` 对非 html 发一年 immutable —— 换色**必须同时换文件名**（DESIGN §11 自己写着这条）。

- 新文件名：`web/public/portage-mark-v2.svg`，`web/index.html` 的 `<link rel="icon">` 跟着改。旧文件删掉。
- 新颜色：亮 `#1c1a15`、暗 `#f0ede2`（新的 ink 对）。
- `path` 一个字都不动，`web/src/brand.tsx` 的 `MARK_PATH` 也不动（它走 `currentColor`，跟着 `--ink` 自动变）。

## 5. 不要做

- 不动 Go 后端、API、schema、路由。
- 不动壳结构（左栏宽度、五项导航、模型页主从、展开井的存在与默认收起）。
- 不引 UI 框架 / tailwind。
- 不引进参考图的全大写小标签（`ROLE` / `ACTIONS`）—— DESIGN §9「全大写小标题」禁令不动，中文界面本来也没有大写。
- 不加渐变、发光、毛玻璃、纹理、装饰性阴影、彩色底砖、彩色侧边条。
- 不改术语（渠道 / 纳管模型 / 接入点 / 上游凭证 / API Key）。
