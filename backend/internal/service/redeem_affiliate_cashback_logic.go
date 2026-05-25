package service

func shouldAccrueAffiliateCashbackForRedeem(redeemCode *RedeemCode) bool {
	if redeemCode == nil {
		return false
	}
	switch redeemCode.Type {
	case RedeemTypeBalance:
		return redeemCode.Value > 0
	case RedeemTypeSubscription:
		return redeemCode.GroupID != nil && redeemCode.ValidityDays > 0
	default:
		return false
	}
}
