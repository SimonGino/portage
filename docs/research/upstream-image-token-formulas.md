# Research：四家上游对图片/PDF 输入的计 token 公式（#78）

调研日期 2026-08-31。所有结论回溯一手来源；查不到的点明说查不到。

## 事实源

官方文档（抓取日期均为 2026-08-31）：

1. Anthropic — Vision（图片限制、28px patch 公式、分辨率档位与示例表）：<https://platform.claude.com/docs/en/build-with-claude/vision>（docs.claude.com 同路径 302 至此）
2. Anthropic — Coordinates and bounding boxes（精确缩放算法与参考实现、28px padding）：<https://platform.claude.com/docs/en/build-with-claude/vision-coordinates>
3. Anthropic — PDF support（每页图+文、1500–3000 token/页、页数限制）：<https://platform.claude.com/docs/en/build-with-claude/pdf-support>
4. OpenAI — Images and vision（Calculating costs：patch 法与 tile 法全部数值、detail 档位、各模型 sizing 表）：<https://developers.openai.com/api/docs/guides/images-vision>（platform.openai.com/docs/guides/images-vision 301 至此；`.md` 后缀取原文）
5. OpenAI — File inputs（PDF＝抽取文本+每页转图、detail 对 PDF 页图的作用、50MB 限制）：<https://developers.openai.com/api/docs/guides/file-inputs>
6. Google — Gemini API Image understanding（384px/258 token、768×768 tile、crop unit 公式、3600 张上限）：<https://ai.google.dev/gemini-api/docs/image-understanding>
7. Google — Gemini API Document processing（PDF 每页 258 token、3072/768 缩放、1000 页/50MB）：<https://ai.google.dev/gemini-api/docs/document-processing>
8. Google — Gemini API Tokens（多模态计 token 汇总：图片同 6，视频 263 token/s、音频 32 token/s）：<https://ai.google.dev/gemini-api/docs/tokens>
9. 智谱 — 开放平台 FAQ 费用问题（GLM-4V 系列「单张图片大约消耗 1047 个 tokens」）：<https://docs.bigmodel.cn/cn/faq/fee-issues>
10. 智谱 — 对话补全 API 参考（image_url 限制：单图 ≤5M、像素 ≤6000×6000；usage 返回 `image_tokens`/`video_tokens`）：<https://docs.bigmodel.cn/api-reference/模型-api/对话补全>（`.md` 后缀取原文）
11. 智谱 — GLM-4.5V / GLM-4.6V 模型指南（无 token 公式，仅指向定价页）：<https://docs.bigmodel.cn/cn/guide/models/vlm/glm-4.5v>、<https://docs.bigmodel.cn/cn/guide/models/vlm/glm-4.6v>；docs.z.ai 对应页同样无公式：<https://docs.z.ai/guides/vlm/glm-4.5v>

---

## 一、Anthropic（Claude）

### 现行官方公式（patch 法，已取代旧的 (宽×高)/750）

- **注意：官方文档已改版**。旧口径「tokens ≈ (width × height)/750、长边 1568px、单图约 1600 token 上限」在当前官方文档中**已不存在**；现行口径（来源 1、2）如下。本文全部按现行口径计算。
- 图片按 **28×28 像素 patch** 计：`tokens = ⌈width/28⌉ × ⌈height/28⌉`（来源 1 原文："Each patch is a 28×28-pixel block… An image, therefore, costs ⌈width / 28⌉ × ⌈height / 28⌉ visual tokens."）。
- 两档分辨率上限（超限自动等比降采样到「同时满足两条限制的最大尺寸」）：

| 档位 | 适用模型 | 长边上限 | 单图 token 上限 |
| --- | --- | --- | --- |
| Standard | Claude 4.7 之前所有模型 | 1568 px | **1568** |
| High-resolution | Claude 4.7 及之后 | 2576 px | **4784** |

- 精确缩放算法（来源 2 给出参考实现）：找满足「⌈边/28⌉×28 ≤ 长边上限」且「patch 数 ≤ token 上限」的最大等比尺寸（长边二分搜索，短边 round-half-to-even）；随后向右下 padding 到 28 的倍数（padding 不加 token、不算内容）。**常见照片/截图由 token 上限决定final尺寸而非长边上限**（官方例：1920×1080 缩到 1456×819 而不是 1568×882）。
- 其他限制：单图最大 8000×8000 px；单图 ≤10MB（API 直连）；单请求 >20 张图时降为更严的单图尺寸限制（≤2000px）。

### PDF（来源 3）

- 每页服务端转成一张图，同时抽取该页文本，**图 + 文都计入**："The system converts each page of the document into an image. The text from each page is extracted and provided alongside each page's image."
- 官方费用估算："Each page typically uses **1,500–3,000 tokens per page**"（文本部分，视密度而定）+ 页图按上面 vision 规则计。无额外 PDF 费用。
- 限制：单请求 ≤32MB、≤600 页（上下文窗口 <1M token 的模型 ≤100 页）。页图由服务端栅格化、尺寸不可控。

### 各尺寸精确计算（用来源 2 的参考实现逐一算出）

| 原始尺寸 | Standard 档缩放后 | Standard tokens | High-res 档缩放后 | High-res tokens |
| --- | --- | --- | --- | --- |
| 1920×1080 | 1456×819 | **1560** | 不缩放 | **2691** |
| 2560×1440 | 1456×819 | **1560** | 不缩放（92×52 patch 恰好 = 4784 上限） | **4784** |
| 4032×3024 | 1270×952 | **1564** | 2212×1659 | **4740** |
| 4000×3000 | 1270×952 | **1564** | 2212×1659 | **4740** |
| **2048×1227（校准图）** | 1402×840（50×30 patch） | **1530** | 不缩放（⌈2048/28⌉=74 × ⌈1227/28⌉=44） | **3256** |

（1920×1080→1456×819→1560 与 2000×1500→1564 两行与官方示例表逐项吻合，来源 1。）

---

## 二、OpenAI

官方把模型分成两套算法（来源 4），**gpt-5 与 gpt-5.1 是 tile 法**，gpt-5-mini/nano 与 gpt-5.2/5.4/5.5/5.6 是 patch 法。

### Tile 法（gpt-5.1、gpt-5、gpt-4o、gpt-4.1、gpt-4o-mini、o1/o1-pro/o3）

`detail: high/auto`（这几个模型 auto 走 tile 规则）：

1. 等比缩进 2048×2048 方框（小图不放大）；
2. 若短边 >768，缩到短边=768，另一边向下取整；
3. tile 数 = 覆盖所需的 512px 方块数；`tokens = base + tiles × tile_tokens`。

`detail: low` 固定只收 base tokens。各模型数值（官方表）：

| 模型 | base | 每 tile |
| --- | --- | --- |
| gpt-5.1、gpt-5* | 70 | 140 |
| gpt-4o、gpt-4.1 | 85 | 170 |
| gpt-4o-mini | **2833** | **5667** |
| o1*、o1-pro*、o3* | 75 | 150 |

（* 官方标注 deprecated。gpt-4o-mini 官方没写「乘数」，直接给出等效 base/tile 数值——即约为 gpt-4o 的 33.3 倍。）

### Patch 法（gpt-5.2、gpt-5.4/-mini/-nano、gpt-5.5、gpt-5.6 系列；已弃用的 gpt-5-mini、gpt-5-nano、gpt-4.1-mini、gpt-4.1-nano、o4-mini）

1. 先按所选 detail 档的**像素上限**等比缩放（不放大）；
2. `patch_count = ⌈w/32⌉ × ⌈h/32⌉`（32×32 patch）；
3. 超出该档 **patch 预算**时按官方公式缩：`shrink = sqrt(32²×budget/(w×h))`，再乘 `min(⌊w·s/32⌋/(w·s/32), ⌊h·s/32⌋/(h·s/32))` 修正、宽高向下取整；
4. `tokens = ⌈patch_count × multiplier⌉`。

detail 档位与预算（官方 sizing 表）：gpt-5.4 系列 `high`（=auto）2048px + **2500 patch**、`low` 2048px + 6144 patch、`original` 6000px + 10000 patch；gpt-5.2 与 gpt-4.1-mini 各档一律 2048px + **6144 patch**；gpt-5.6（sol/terra/luna）`original/auto` **无 patch 预算**（仅 65535px 上限，大图会爆量）。乘数：gpt-5.2/5.4/5.5/5.6 系列与 gpt-5-mini 均 **1.2**；gpt-5-nano 1.5；gpt-4.1-mini **1.62**；gpt-4.1-nano 2.46；o4-mini 1.72。官方注明计费与估算可差 ±1 token。旧文档的「1536 patch 上限」在现行文档中已不存在，被上述各档预算取代。

### PDF（file input，来源 5）

- "the API extracts both text and page images and sends both to the model"——**每页转图 + 抽取文本都计费**，页图按上面 vision 规则计 token；`detail`（auto/low/high）只影响页图处理（gpt-5.6 及之后 auto=high，更早模型 auto=low），文本恒计。
- 限制：单文件与单请求合计 ≤50MB；需带视觉能力的模型（gpt-4o 及之后）。

### 各尺寸精确计算

Tile 法（detail high；三个尺寸经两步缩放后分别为 1365×768 / 1024×768 / 1281×768）：

| 原始尺寸 | tiles | gpt-5 / gpt-5.1 | gpt-4o / gpt-4.1 | gpt-4o-mini |
| --- | --- | --- | --- | --- |
| 1920×1080 | 6 | **910** | 1105 | 36835 |
| 2560×1440 | 6 | **910** | 1105 | 36835 |
| 4032×3024 | 4 | **630** | 765 | 25501 |
| 4000×3000 | 4 | **630** | 765 | 25501 |
| **2048×1227** | 6 | **910** | **1105** | **36835** |

Patch 法（按官方四步算法逐项计算）：

| 原始尺寸 | gpt-5.4（high，2500 预算，×1.2） | gpt-5.2（6144 预算，×1.2） | gpt-4.1-mini（6144 预算，×1.62） |
| --- | --- | --- | --- |
| 1920×1080（2040 patch） | **2448** | 2448 | 3305 |
| 2560×1440（→2048×1152，2304 patch） | **2765** | 2765 | 3733 |
| 4032×3024（→2048×1536；5.4 再缩到 1824×1368=2451 patch） | **2942** | 3687（3072 patch） | 4977 |
| 4000×3000（同上） | **2942** | 3687 | 4977 |
| **2048×1227**（64×39=2496 patch，无需再缩） | **2996** | **2996** | **4044** |

---

## 三、Google Gemini

### 图片（来源 6，官方原文）

- "**258 tokens if both dimensions <= 384 pixels.** Larger images are tiled into **768x768** pixel tiles, each costing **258 tokens**."
- tile 数官方给的是**粗略公式**（原文自称 "A rough formula"）：`crop_unit = floor(min(w,h)/1.5)`，每边除以 crop_unit 后相乘。官方例：960×540 → crop_unit 360 → 3×2 = 6 tiles。
- 该规则未按 2.0/2.5 分版本叙述（页面适用于现行 Gemini 模型）；Gemini 3 另引入 `media_resolution` 参数控制每图 token 上限（低/中/高档具体数值见其 Media resolution 指南，此处不展开）。
- 单请求最多 3600 张图。

### PDF（来源 7，官方原文）

- "**Each document page is equivalent to 258 tokens.**"
- 页面缩放：大页缩到 ≤3072×3072、小页放大到 768×768（等比），分辨率高低**不改变计费**。
- 限制：≤50MB 或 ≤1000 页。Gemini 3 起 PDF 中抽取的原生文本不计费、只计图像处理 token。

### 各尺寸计算（按官方 rough formula，每 tile 258）

| 原始尺寸 | crop_unit | tiles | tokens |
| --- | --- | --- | --- |
| 1920×1080 | 720 | 3×2=6 | **1548** |
| 2560×1440 | 960 | 3×2=6 | **1548** |
| 4032×3024 | 2016 | 2×2=4 | **1032** |
| 4000×3000 | 2000 | 2×2=4 | **1032** |
| **2048×1227** | 818 | 3×2=6（2048/818≈2.50 进为 3） | **1548** |

（因公式本身「rough」，实际计量以 `countTokens` API 返回为准；量级与档位不会偏离上表。）

---

## 四、智谱 GLM（bigmodel.cn / docs.z.ai）

**查不到精确公式。** 逐处核对结果：

- 官方 FAQ（来源 9）是唯一给出数字的地方："调用 **GLM-4V 系列**图像识别模型时，**单张图片大约消耗 1047 个 tokens**。"——仅覆盖 GLM-4V 系列，且是「大约」的固定值口径，未给按分辨率的公式。
- GLM-4.5V / GLM-4.6V / GLM-5.3-Flash 的模型指南（来源 11，bigmodel.cn 与 docs.z.ai 双站核对）均**无 token 计法**，只指向定价页（按 token 单价计费，输入 $0.6/M 输出 $1.8/M——GLM-4.5V @ z.ai）。
- 对话补全 API（来源 10）可回溯的客观口径：单图 **≤5MB、像素 ≤6000×6000**（jpg/png/jpeg）；GLM-5.3-Flash/GLM-5V-Turbo/GLM-4.6V/GLM-4.5V 系列单请求 ≤50 张（GLM-4V-Plus-0111 ≤5 张、GLM-4V-Flash 1 张）。Tokenizer/对话接口的 usage 会返回 **`image_tokens`**、`video_tokens` 明细字段——说明按图片内容动态计量，但换算规则官方未公开。
- PDF/文件输入：`type: file` 单文件 ≤50MB，最多 50 个；无每页 token 口径。另有独立计费的 GLM-OCR 文档解析服务（非对话链路）。
- 结论：**除「GLM-4V ≈1047/张」外，智谱没有公开任何图片 token 公式**；4.5V/4.6V 的每图开销只能靠实测 usage 或按 1047 量级近似，无官方依据外推。

---

## 五、汇总表：常见尺寸的每图 token 开销

各家取「当前主力模型」列（Anthropic 双档、OpenAI 取 gpt-5/5.1 tile 与 gpt-5.4 patch 两代表、Gemini 现行规则、智谱只有 GLM-4V 固定值）：

| 尺寸 | Claude std（≤4.6） | Claude hi-res（4.7+） | gpt-5/5.1（tile,high） | gpt-5.4（patch,high） | gpt-4.1-mini | gpt-4o-mini | Gemini | GLM-4V（智谱） |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1920×1080 | 1560 | 2691 | 910 | 2448 | 3305 | 36835 | 1548 | ≈1047 |
| 2560×1440 | 1560 | 4784 | 910 | 2765 | 3733 | 36835 | 1548 | ≈1047 |
| 4032×3024 | 1564 | 4740 | 630 | 2942 | 4977 | 25501 | 1032 | ≈1047 |
| 4000×3000 | 1564 | 4740 | 630 | 2942 | 4977 | 25501 | 1032 | ≈1047 |
| **2048×1227** | **1530** | **3256** | **910** | **2996** | **4044** | **36835** | **1548** | **≈1047** |

各家理论单图上限（由公式推得）：Claude std **1568**；Claude hi-res **4784**；OpenAI tile 法上限 8 tiles → gpt-5 **1190** / gpt-4o **1445** / gpt-4o-mini **48169**；OpenAI patch 法 → gpt-5.4 high **3000**（2500×1.2）、gpt-5.2 **7373**（6144×1.2）、gpt-4.1-mini **9954**（6144×1.62）、gpt-5.6 original **无上限**；Gemini 常规比例（≤2:1）≤6 tiles → **1548**（极端长图更多）；智谱未知。

---

## 六、结论：单一固定值能否盖住？

**数据事实（不做决策）：**

1. **2000 token/张盖不住。** 上表中超过 2000 的格子：Claude hi-res 全部（2691–4784）、OpenAI patch 系全部（2448–4977）、gpt-4o-mini 全部（25501–36835）。2000 只够盖 Claude std（≤1568）、OpenAI tile 主力（≤1190/1445 理论上限）、Gemini 常规图（≤1548）、GLM-4V（≈1047）。
2. **差异幅度：** 同一张 2048×1227，各家主力模型间从 910（gpt-5）到 4044（gpt-4.1-mini），差 **4.4 倍**；把 gpt-4o-mini 算进来是 36835，差 **40 倍**。同一家内部：Claude 双档差 1530→3256（2.1 倍）；OpenAI tile 与 patch 两代差 910→2996（3.3 倍）。尺寸维度反而最平：除 Claude hi-res 外，各家在 1080p→手机照片区间波动都在 ±60% 以内（缩放上限把大图压平了）。
3. **若取「保守上界固定值」：** 排除 gpt-4o-mini（异常值，任何合理固定值都盖不住），主力模型的最大观测值是 4977（gpt-4.1-mini 手机照片）、理论上限 4784（Claude hi-res）/ 9954（gpt-4.1-mini 极限），**≈5000 可盖住表中全部主力观测值**；但对 gpt-5（630–910）这类便宜端会高估 5–8 倍。
4. **若按家分档：** 每家自身相当稳定——Claude std ≈1550、Claude hi-res ≈2700–4800、OpenAI tile ≈600–1100、OpenAI patch ≈2400–5000、Gemini ≈1000–1550、GLM ≈1047（仅 4V 有据）。按「协议家 + 模型代际」两键分档即可把误差从数倍压到 ±50% 以内；智谱是唯一无官方公式、只能实测回填的一家（其 usage 有 `image_tokens` 字段可直接记账，无需估算）。
5. **PDF 口径三家三样：** Anthropic 每页 ≈1500–3000（文）+ 页图 vision 计法；OpenAI 每页文本 + 页图（detail 可调）；Gemini 每页固定 258；智谱无口径。按图估 PDF 时 Gemini 最便宜且恒定，Anthropic/OpenAI 每页开销与一张中等截图同量级或更高。
