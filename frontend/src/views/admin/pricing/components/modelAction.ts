import type { ModelRow } from '../pricingModel'

/** 模型抽屉里点的动作：上线（草稿或未登记）、下线、重新上线。 */
export interface ModelAction {
  kind: 'launch' | 'retire' | 'reactivate'
  model: ModelRow
}
