// Portage 标记：1b「hull off the water」——船体离水，下面两段水道被切断，货从缺口上面过去。
// 缺口是主语，正对名字（在两段不通航的水道之间，把船和货扛过陆地）。
//
// 一条 path、`fill="currentColor"`：颜色跟着上下文走，亮/暗主题不需要两份资产，也不需要
// 在改主题时回来复查这里（DESIGN.md §3 的单色约束对标识同样成立）。
//
// 这份是**页面内**用的。favicon 是另一份 `web/public/portage-mark.svg`：那份是独立文档，
// currentColor 在里面没有上下文可继承，只能把两个主题的颜色写死在 SVG 自己的 <style> 里。
// 两处的 path 数据必须一致，改一处记得改另一处。设计源在 Claude Design 项目
// 「Portage Logo Design Brief」的 assets/mark-hull.svg（见 issue #46）。

/** MARK_PATH 与 web/public/portage-mark.svg 里的那条必须逐字一致。 */
const MARK_PATH = 'M3,6 L21,6 L17,13 L7,13 Z M0,17 L9,17 L9,21 L0,21 Z M15,17 L24,17 L24,21 L15,21 Z'

export function PortageMark({ size = 16 }: { size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="currentColor"
      role="img"
      aria-label="Portage"
      // 它总是紧挨着一段文字，不锁 shrink 会在窄屏上被压成一条。
      style={{ flexShrink: 0 }}
    >
      <path d={MARK_PATH} />
    </svg>
  )
}
