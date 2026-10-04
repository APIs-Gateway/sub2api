/**
 * 给 AI 读的文档链接上的 ?endpoint= 参数。
 *
 * 单独放一个文件：接入弹窗（keys 页）也要拼这个参数，而 docsMachine.ts 会把全部文档章节和 Markdown 渲染器带进来，
 * keys 页不该为了一个查询串去加载整份文档。docsMachine.ts 从这里再导出，原来的引用不用改。
 */

/** 备用地址在 URL 里的参数名：值是端点的 API 根地址，必须是站点设置里存在的一个。 */
export const ENDPOINT_QUERY_PARAM = 'endpoint'

/** ?endpoint=… 查询串。冒号和斜杠不转义，链接读起来像地址；后端的 machineEndpointQuery 同规则。 */
export function endpointQuery(base: string): string {
  return `?${ENDPOINT_QUERY_PARAM}=${encodeURIComponent(base).replace(/%3A/gi, ':').replace(/%2F/gi, '/')}`
}
