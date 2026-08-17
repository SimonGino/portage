# 供应商图标资产

`svg/` 下是各家 AI 供应商的品牌方块，取自 [cherry-studio](https://github.com/CherryHQ/cherry-studio)
的 `packages/ui/icons/providers/{light,dark}`。

## 为什么可以取

cherry-studio 仓库整体是 AGPL-3.0，但 `packages/ui` 这个 workspace 包在自己的
`package.json` 里声明 `"license": "MIT"`。这里**只取该包下的 SVG 资产**，`index.tsx`
里的匹配表、组件、回退逻辑都是本项目自己写的——没有搬它的 `registry.ts`，也没有搬
`getModelLogoRef` 那套 IconRef 机制。

图形本身是各家的商标。用作「指代该供应商」的标识属于指称性使用，不改形、不改色、
不拿它们标注与该供应商无关的东西。

## 取的时候做了什么筛

- **只取 providers，不取 models**。理由写在 `index.tsx` 顶部注释里。
- **超过 16 KB 的丢掉**。有些图标是几百个 path 的写实描摹（`dangbei` 700 KB、
  `gemini` 254 KB），一个顶得上其余全部。管理端是 embed 进单二进制的，为一枚图标
  换半兆不划算。落选的用最近的替身（Gemini → `ai-studio`，智谱 → `z-ai`）。
- **深色版只有少数几个有**。浅色版是纯黑描摹的那些才需要，带底色的品牌方块两种主题
  下通用。

## 加一枚新图标

1. 从 cherry-studio 的 `packages/ui/icons/providers/light/<name>.svg` 复制到 `svg/`；
   有 `dark/<name>.svg` 就一并复制成 `svg/<name>.dark.svg`。
2. 渠道图标加 `CHANNEL_HOSTS` / `CHANNEL_NAMES`（只给官方那几家）；模型图标加
   `MODEL_PATTERNS`。两套不许互为兜底。**顺序即优先级，特化的排前面。**
3. 不用改别的：`svg/*.svg` 是 glob 进来的。
