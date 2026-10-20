package service

import (
	"math"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/shopspring/decimal"
)

const defaultBalanceRechargeMultiplier = 1.0

func normalizeBalanceRechargeMultiplier(multiplier float64) float64 {
	if math.IsNaN(multiplier) || math.IsInf(multiplier, 0) || multiplier <= 0 {
		return defaultBalanceRechargeMultiplier
	}
	return multiplier
}

func calculateCreditedBalance(paymentAmount, multiplier float64) float64 {
	return decimal.NewFromFloat(paymentAmount).
		Mul(decimal.NewFromFloat(normalizeBalanceRechargeMultiplier(multiplier))).
		Round(2).
		InexactFloat64()
}

func calculateGatewayPaymentAmountForCreditedValue(creditedValue, multiplier float64, currency string) float64 {
	if creditedValue <= 0 {
		return 0
	}
	fractionDigits := int32(payment.CurrencyMaxFractionDigits(currency))
	return decimal.NewFromFloat(creditedValue).
		Div(decimal.NewFromFloat(normalizeBalanceRechargeMultiplier(multiplier))).
		RoundUp(fractionDigits).
		InexactFloat64()
}

func calculateGatewayRefundAmount(orderAmount, payAmount, refundAmount float64, currency string) float64 {
	if orderAmount <= 0 || payAmount <= 0 || refundAmount <= 0 {
		return 0
	}
	fractionDigits := int32(payment.CurrencyMaxFractionDigits(currency))
	if math.Abs(refundAmount-orderAmount) <= paymentAmountToleranceForCurrency(currency) {
		return decimal.NewFromFloat(payAmount).Round(fractionDigits).InexactFloat64()
	}
	return decimal.NewFromFloat(payAmount).
		Mul(decimal.NewFromFloat(refundAmount)).
		Div(decimal.NewFromFloat(orderAmount)).
		Round(fractionDigits).
		InexactFloat64()
}

func calculateGatewayRefundBreakdown(orderAmount, payAmount, refundAmount, feeRate float64, currency string) (baseAmount, feeAmount, gatewayAmount float64) {
	baseAmount = calculateGatewayRefundAmount(orderAmount, payAmount, refundAmount, currency)
	if baseAmount <= 0 {
		return 0, 0, 0
	}
	if math.IsNaN(feeRate) || math.IsInf(feeRate, 0) || feeRate <= 0 {
		return baseAmount, 0, baseAmount
	}
	if feeRate > 100 {
		feeRate = 100
	}
	fractionDigits := int32(payment.CurrencyMaxFractionDigits(currency))
	fee := decimal.NewFromFloat(baseAmount).
		Mul(decimal.NewFromFloat(feeRate)).
		Div(decimal.NewFromInt(100)).
		RoundUp(fractionDigits)
	gateway := decimal.NewFromFloat(baseAmount).Sub(fee)
	if gateway.IsNegative() {
		gateway = decimal.Zero
	}
	return baseAmount, fee.InexactFloat64(), gateway.Round(fractionDigits).InexactFloat64()
}
