package billing

import "strings"

// Builtin prices ship with the plugin and are never persisted.
var builtinPrices = map[string]PriceRates{
	"codex-auto-review":      {InputPer1M: 0, OutputPer1M: 0},
	"gpt-image-1.5":          builtinImageRates(10, 32),
	"gpt-image-2":            builtinImageRates(30, 30),
	"gpt-image-2.5":          builtinImageRates(30, 30),
	"gpt-image-2.5-flare":    builtinImageRates(30, 30),
	"gpt-image-2.5-sunburst": builtinImageRates(30, 30),
	"gpt-image-2.6":          builtinImageRates(30, 30),
	"gpt-image-2.6-flare":    builtinImageRates(30, 30),
	"gpt-image-2.6-sunburst": builtinImageRates(30, 30),
	"deepseek-flash": {
		InputPer1M:     0.15,
		OutputPer1M:    0.6,
		CacheReadPer1M: float64Ptr(0.003),
	},
}

func builtinImageRates(textOutput, imageOutput float64) PriceRates {
	return PriceRates{
		InputPer1M: 5, OutputPer1M: textOutput, CacheReadPer1M: float64Ptr(1.25),
		ImageInputPer1M:     float64Ptr(8),
		ImageCacheReadPer1M: float64Ptr(2),
		ImageOutputPer1M:    float64Ptr(imageOutput),
	}
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
