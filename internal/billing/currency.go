package billing

import (
	"fmt"
	"math"
)

// Currency is the ISO 4217 code of an amount. Custom prices and plans choose
// theirs; builtin and reference prices are in USD. A request's cost keeps the
// currency of the price it used.
type Currency string

const (
	USD Currency = "USD"
	CNY Currency = "CNY"
)

func (c Currency) valid() bool { return c == USD || c == CNY }

// Format writes an amount for messages and logs, such as $1.2500.
func (c Currency) Format(amount float64) string {
	symbol := "$"
	if c == CNY {
		symbol = "¥"
	}
	return fmt.Sprintf("%s%.4f", symbol, amount)
}

// ExchangeRate is the number of CNY one USD buys. Amounts are stored in their
// own currency and converted at the configured rate only when they are read
// together, so changing the rate never rewrites recorded costs.
type ExchangeRate float64

const DefaultExchangeRate ExchangeRate = 7

func (r ExchangeRate) valid() bool {
	return r > 0 && !math.IsInf(float64(r), 0)
}

// Convert expresses an amount in from as an amount in to.
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
