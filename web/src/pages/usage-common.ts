import type { UsageRow } from '../api'

/**
 * 概览与排行共用的统计窗口选项。
 *
 * 「7 天」= 今天往前数 7 个自然日、按本地时区（口径层 v0.55），不是滚动 7×24 小时。
 * 两页各自持有自己的选中值，不共享：它们现在是两个页面，一个页面上的选择没有理由
 * 改另一个页面的取数（口径层 v0.60 拆页）。
 */
export const DAY_OPTIONS = [
  { value: '1' as const, label: '1 天' },
  { value: '7' as const, label: '7 天' },
  { value: '30' as const, label: '30 天' },
]

/**
 * 把 `/usage` 回来的那几行加成合计。后端没有单独的汇总接口，前端加一遍就够——
 * 行数是模型数量级。
 *
 * 抽出来是因为拆页之后两边都要它，而且要的是**同一个数**：概览拿它做指标条，
 * 排行拿它做每行占比的分母。各写一份的话，早晚有一天两页上的合计对不上。
 */
export function sumUsage(rows: UsageRow[]) {
  return rows.reduce(
    (a, r) => ({
      calls: a.calls + r.calls,
      errors: a.errors + r.errors,
      input: a.input + r.input_tokens,
      output: a.output + r.output_tokens,
      cacheRead: a.cacheRead + r.cache_read_tokens,
      cacheWrite: a.cacheWrite + r.cache_write_tokens,
    }),
    { calls: 0, errors: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
  )
}
