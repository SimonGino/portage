# Domain Docs

engineering skills 探索代码前先读这两处。

## 先读

- 根 **`CONTEXT.md`** —— 术语表。
- **`docs/adr/`** —— 读与你要动的区域相关的 ADR。该目录按需创建，不存在就**静默略过**：不提示缺失，也不建议提前补建。`/domain-modeling`（经 `/grill-with-docs` 与 `/improve-codebase-architecture` 到达）会在术语或决策真的收敛时才落文件。

## 用术语表的词

输出里出现领域概念（issue 标题、重构提案、假设、测试名）就用 `CONTEXT.md` 里定义的那个词，不飘到 _Avoid_ 列点名的同义词。

需要的概念不在表里，这是个信号：要么你在造项目不用的说法（重新想），要么真有缺口（记下来交给 `/domain-modeling`）。

## 与 ADR 冲突就摆出来

输出与既有 ADR 相抵触时显式点名，不要默默覆盖：

> _与 ADR-00XX（<标题>）相抵触——但值得重开，因为……_

## 本仓库补充

文档层级、口径决策与 ADR 的分工见 [`doc-layers.md`](doc-layers.md)。
