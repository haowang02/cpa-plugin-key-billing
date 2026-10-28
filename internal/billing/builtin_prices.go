package billing

import "strings"

// Builtin prices are shipped with the plugin and are intentionally not persisted.
var builtinPrices = map[string]PriceRates{
	NormalizeModelID("codex-auto-review"): {InputPer1M: 0, OutputPer1M: 0},
	NormalizeModelID("gpt-image-1.5"): {
		InputPer1M:     5,
		OutputPer1M:    32,
		CacheReadPer1M: float64Ptr(1.25),
	},
	NormalizeModelID("gpt-image-2.5"): {
		InputPer1M:     5,
		OutputPer1M:    30,
		CacheReadPer1M: float64Ptr(1.25),
	},
	NormalizeModelID("deepseek-flash"): {
		InputPer1M:     0.15,
		OutputPer1M:    0.6,
		CacheReadPer1M: float64Ptr(0.003),
	},
}

func float64Ptr(value float64) *float64 {
	return &value
}

func resolveBuiltinRates(modelID string) (PriceRates, bool) {
	key := NormalizeModelID(modelID)
	if slash := strings.LastIndexByte(key, '/'); slash >= 0 {
		key = key[slash+1:]
	}
	rates, found := builtinPrices[key]
	return rates, found
}

func ResolveBuiltinPrice(modelID string) Price {
	if rates, found := resolveBuiltinRates(modelID); found {
		return rates.resolve(PriceSourceBuiltin, USD)
	}
	return Price{Source: PriceSourceNone}
}
