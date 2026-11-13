package billing

import (
	"fmt"
	"math"
)

// Builtin and reference prices are in USD; a request's cost keeps the currency of its price.
type Currency string

const (
	USD Currency = "USD"
	CNY Currency = "CNY"
)

func (c Currency) valid() bool { return c == USD || c == CNY }

func (c Currency) Format(amount float64) string {
	symbol := "$"
	if c == CNY {
		symbol = "¥"
	}
	return fmt.Sprintf("%s%.4f", symbol, amount)
}

// ExchangeRate is CNY per USD. Amounts convert only when read together, so a new rate never rewrites costs.
type ExchangeRate float64

const DefaultExchangeRate ExchangeRate = 7

func (r ExchangeRate) valid() bool {
	return r > 0 && !math.IsInf(float64(r), 0)
}

func (r ExchangeRate) Convert(amount float64, from, to Currency) float64 {
	switch {
	case from == to:
		return amount
	case to == CNY:
		return amount * float64(r)
	default:
		return amount / float64(r)
	}
}
