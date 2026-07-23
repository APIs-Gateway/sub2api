package service

import "strings"

// selectCompositeBillingModel keeps an administrator's explicit channel price
// for a composite alias, but otherwise bills by the concrete model that was
// actually forwarded to the selected provider.
func selectCompositeBillingModel(group *Group, billingModel, concreteModel string, hasExplicitChannelPricing func() bool) string {
	if group == nil || group.Platform != PlatformComposite {
		return billingModel
	}
	billingModel = strings.TrimSpace(billingModel)
	concreteModel = strings.TrimSpace(concreteModel)
	if billingModel == "" || concreteModel == "" || billingModel == concreteModel {
		return billingModel
	}
	if hasExplicitChannelPricing != nil && hasExplicitChannelPricing() {
		return billingModel
	}
	return concreteModel
}
