import { api } from '../../api'
import type { MyModel } from '../../api'
import { Card, CopyIconButton, Empty, ErrorBar, useList } from '../../ui'
import { ModelIcon } from '../../icons'
import { fmtFourTitle, fmtPrice, isUnpriced } from '../../prices'

/** 单价格（口径层 v1.10）：定价惯用形 $入/$出（sans，同 §5.1 定价芯片的字形），
 *  四价全文进 title；四价全 null 显「未定价」——那不是 $0，用量页照这价记的账，
 *  别把「没记价」读成「免费」。 */
function PriceCell({ m }: { m: MyModel }) {
  const four = {
    input: m.price_input,
    output: m.price_output,
    cache_read: m.price_cache_read,
    cache_write: m.price_cache_write,
  }
  if (isUnpriced(four)) {
    return <span className="muted">未定价</span>
  }
  return (
    <span title={fmtFourTitle(four)}>
      {fmtPrice(four.input)}/{fmtPrice(four.output)}
    </span>
  )
}

/**
 * 「模型」页（DESIGN §12）：全量开放的可用模型清单，只读。与 /v1/models 背后是
 * 同一份可路由谓词——列出来的都调得通。名字可复制：它就是请求里 model 字段该填
 * 的那个字符串。
 */
export default function MyModels() {
  const models = useList(() => api.get<{ models: MyModel[] | null }>('/my/models'))

  if (models.loading && models.data === null) return <div className="boot">加载中…</div>
  const list = models.data?.models ?? []

  return (
    <Card title="可用模型">
      <ErrorBar message={models.error} />
      <p className="muted">
        当前可调用的全部模型名，填进请求的 <code>model</code> 字段即可。清单与网关的{' '}
        <code>/v1/models</code> 一致；key 上另设了白名单的话以白名单为准。
      </p>
      {list.length === 0 ? (
        <Empty>当前没有可路由的模型——管理员还没配好渠道或接入点。</Empty>
      ) : (
        <table className="table">
          <thead>
            <tr>
              <th>模型</th>
              <th>类型</th>
              <th>单价（USD/百万 token）</th>
              <th className="col-actions" />
            </tr>
          </thead>
          <tbody>
            {list.map((m) => (
              <tr key={m.id}>
                <td>
                  <span className="chip">
                    <ModelIcon model={m.id} size={14} />
                    <code>{m.id}</code>
                  </span>
                </td>
                <td className="muted">{m.direct ? '直连（渠道/模型）' : '接入点'}</td>
                <td>
                  <PriceCell m={m} />
                </td>
                <td className="col-actions">
                  <CopyIconButton value={m.id} title="复制模型名" />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  )
}
