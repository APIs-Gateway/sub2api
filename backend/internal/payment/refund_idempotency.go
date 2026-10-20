package payment

import (
	"crypto/sha256"
	"encoding/hex"
)

// DeterministicRefundNo 生成确定性的退款请求号（网关幂等键）：同一订单 + 同一退款额多次重试
// 得到【相同】的值，使支付宝(out_request_no)/微信(out_refund_no) 等把重试识别为同一笔退款并
// 幂等去重；不同退款额得到不同值（允许同单分次退款）。
//
// 关键：绝不能掺入 time.Now()/随机值——否则退款重试（如网关已退成功但响应丢失后管理员重试）会
// 被网关当作【新的一笔退款】再次到账，造成双重退款（真金损失）。
//
// 形如 "{orderID}-refund-{12位hex}"，比旧的 "{orderID}-refund-{UnixNano}" 更短，
// 不会触碰网关对该字段的长度上限（支付宝/微信均 ≤64）。
func DeterministicRefundNo(orderID, amount string) string {
	sum := sha256.Sum256([]byte(orderID + "\x00" + amount))
	return orderID + "-refund-" + hex.EncodeToString(sum[:])[:12]
}
