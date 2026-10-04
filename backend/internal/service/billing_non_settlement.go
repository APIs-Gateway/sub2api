package service

import "context"

// 「非结算调用」标记。
//
// 计费计算函数（calculateRecordUsageCost 及其下游）既被结算用（RecordUsage 写用量行、扣费），
// 也被不产生用量行的调用复用，例如转发前的余额预占估算（ReserveBillingInflight）。后者对同一个
// 无价模型会反复走到「算不出价」的分支，如果也计数、打日志，计数就会和 usage_logs 对不上
// （一个无价请求按尝试次数被计好几次，没有产生用量的请求也被计入）。
//
// 约定：任何不对应一行用量记录的计费计算，调用前先用 WithBillingNonSettlement 给 ctx 打标记，
// 无价观测（noteUnpricedBilling）看到标记就直接跳过。后续新增的同类调用（如价格影子重算、对账）
// 复用这对函数，不要另造标记。

type billingNonSettlementCtxKey struct{}

// WithBillingNonSettlement 返回带「非结算调用」标记的 ctx。标记只影响观测（计数与日志），
// 不影响任何计费结果。
func WithBillingNonSettlement(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, billingNonSettlementCtxKey{}, true)
}

// IsBillingNonSettlement 判断 ctx 是否带「非结算调用」标记。
func IsBillingNonSettlement(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, ok := ctx.Value(billingNonSettlementCtxKey{}).(bool)
	return ok && marked
}
