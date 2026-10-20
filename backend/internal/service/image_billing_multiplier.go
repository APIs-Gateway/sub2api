package service

func resolveImageRateMultiplier(apiKey *APIKey, effectiveGroupMultiplier float64) float64 {
	if apiKey != nil && apiKey.Group != nil {
		return resolveImageRateMultiplierFromFields(apiKey.Group.ImageRateIndependent, apiKey.Group.ImageRateMultiplier, effectiveGroupMultiplier)
	}
	return effectiveGroupMultiplier
}

// resolveImageRateMultiplierFromFields 按显式的组 image 费率字段计算图像倍率。
// 稳定优先兜底到其他档位组时，须用实际服务组的 image 费率策略（而非 home 组的 apiKey.Group）。
func resolveImageRateMultiplierFromFields(imageRateIndependent bool, imageRateMultiplier float64, effectiveGroupMultiplier float64) float64 {
	if imageRateIndependent {
		if imageRateMultiplier < 0 {
			return 0
		}
		return imageRateMultiplier
	}
	return effectiveGroupMultiplier
}
