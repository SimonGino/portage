import { api } from '../../api'
import type { CatalogModel } from '../../api'
import { ErrorBar, useList } from '../../ui'
import { ModelCatalog } from '../../catalog'

/**
 * 「模型」页（DESIGN §12，v0.76，#189）：现有表整个换成 §5.2 的模型目录组件——
 * 无来源列、不跨页、只有复制键，仍只读。数据来自 /my/models：与 /v1/models 同一份
 * 可路由谓词，行上带目录胶囊的事实（协议子集 / 输入上限 / 图片模态 / 单价）。
 */
export default function MyModels() {
  const models = useList(() => api.get<{ models: CatalogModel[] | null }>('/my/models'))

  if (models.loading && models.data === null) return <div className="boot">加载中…</div>
  return (
    <>
      <ErrorBar message={models.error} />
      <ModelCatalog rows={models.data?.models ?? []} space="my" />
    </>
  )
}
