/**
 * 单元格写入的「预览 → 确认 → 提交」流程：矩阵批量操作和模型上线共用。
 * 预览只登记凭证，不改价格；提交时原样带回预览时的请求。
 */
import { computed, ref } from 'vue'
import { adminAPI } from '@/api/admin'
import type { CellOp, CellsCommitResult, CellsRequest, CellsTicket } from '@/api/admin/pricing'
import { toWriteError, type WriteError } from './pricingErrors'
import { usePricingData } from './usePricingData'
import { buildRequest } from './pricingWrite'

export type PlanPhase = 'idle' | 'previewing' | 'ready' | 'committing' | 'done'

export function useCellPlan() {
  const { state } = usePricingData()
  const phase = ref<PlanPhase>('idle')
  const ticket = ref<CellsTicket | null>(null)
  const request = ref<CellsRequest | null>(null)
  const error = ref<WriteError | null>(null)
  const result = ref<CellsCommitResult | null>(null)

  const busy = computed(() => phase.value === 'previewing' || phase.value === 'committing')

  function reset() {
    phase.value = 'idle'
    ticket.value = null
    request.value = null
    error.value = null
    result.value = null
  }

  /** 用当前读到的基线生成请求并预览。 */
  async function preview(ops: CellOp[]): Promise<boolean> {
    return previewRequest(buildRequest(ops, state.derives))
  }

  async function previewRequest(req: CellsRequest): Promise<boolean> {
    phase.value = 'previewing'
    error.value = null
    ticket.value = null
    request.value = req
    try {
      ticket.value = await adminAPI.pricing.previewCells(req)
      phase.value = 'ready'
      return true
    } catch (err) {
      error.value = toWriteError(err)
      phase.value = 'idle'
      return false
    }
  }

  /** 凭证过期、内容不一致之类：用同一份请求重新预览。 */
  async function repreview(): Promise<boolean> {
    return request.value ? previewRequest(request.value) : false
  }

  /** 涉价写入必须带预览凭证；不涉价的写入 approval_id 为 0 也可以，这里一律带预览返回的值。 */
  async function commit(): Promise<boolean> {
    if (!ticket.value || !request.value) return false
    phase.value = 'committing'
    error.value = null
    try {
      result.value = await adminAPI.pricing.commitCells(ticket.value.approval_id, request.value)
      phase.value = 'done'
      return true
    } catch (err) {
      error.value = toWriteError(err)
      phase.value = 'ready'
      return false
    }
  }

  return { phase, ticket, request, error, result, busy, preview, repreview, commit, reset }
}
