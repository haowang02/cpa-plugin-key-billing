package billing

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"
)

type CustomPrice struct {
	ModelID  string   `json:"model_id"`
	Currency Currency `json:"currency"`
	PriceRates
}

type PriceRow struct {
	ModelID  string   `json:"model_id"`
	Currency Currency `json:"currency,omitempty"`
	PriceRates
	Source             PriceSource `json:"source"`
	InModels           bool        `json:"in_models"`
	CustomPriceModelID string      `json:"custom_price_model_id,omitempty"`
}

// The browser supplies the current CPA model list for display only.
// Display reads do not populate the call LRU.
func (s *Store) ModelPriceRows(models []string, includeCustom bool) ([]PriceRow, error) {
	var custom map[string]CustomPrice
	var references *referencePriceManager
	s.read(func(state *State) {
		custom = maps.Clone(state.Prices)
		references = s.referencePrices.Load()
	})
	names := append([]string{}, models...)
	if includeCustom {
		customModels := make([]string, 0, len(custom))
		for _, price := range custom {
			customModels = append(customModels, price.ModelID)
		}
		sort.Strings(customModels)
		names = append(names, customModels...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	available := map[string]bool{}
	for _, model := range models {
		available[NormalizeModelID(model)] = true
	}
	pricingState := &State{Prices: custom}
	seen := make(map[string]bool, len(names))
	rows := make([]PriceRow, 0, len(names))
	missing := []string{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		key := NormalizeModelID(name)
		if name == "" || seen[key] {
			continue
		}
		if len(name) > 1024 {
			return nil, invalidf("Model ID is too long")
		}
		seen[key] = true
		row := PriceRow{ModelID: name, Source: PriceSourceNone, InModels: available[key]}
		billingModel := pricingState.ResolveBillingModel(name, name)
		if price, found := custom[NormalizeModelID(billingModel)]; found {
			row.Currency, row.PriceRates = price.Currency, price.PriceRates
			row.Source = PriceSourceCustom
			row.CustomPriceModelID = price.ModelID
		} else if rates, found := resolveBuiltinRates(billingModel); found {
			row.Currency, row.PriceRates = USD, rates
			row.Source = PriceSourceBuiltin
		} else {
			missing = append(missing, name)
		}
		rows = append(rows, row)
	}
	matched, err := references.lookupModels(ctx, missing)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if price, found := matched[rows[i].ModelID]; found {
			rows[i].Currency, rows[i].PriceRates = USD, *price.PriceRates
			rows[i].Source = PriceSourceReference
		}
	}
	return rows, nil
}

func (price CustomPrice) Validate() error {
	modelID := strings.TrimSpace(price.ModelID)
	if modelID == "" {
		return invalidf("Model ID is required")
	}
	if len(modelID) > 1024 {
		return invalidf("Model ID is too long")
	}
	if !price.Currency.valid() {
		return invalidf("Model %q: currency must be USD or CNY", modelID)
	}
	return price.PriceRates.validate(modelID)
}

// Custom price changes publish only after the database write succeeds. A
// price without a currency is in USD.
func (s *Store) UpsertPrice(price CustomPrice) (CustomPrice, error) {
	price.ModelID = strings.TrimSpace(price.ModelID)
	if price.Currency == "" {
		price.Currency = USD
	}
	if err := price.Validate(); err != nil {
		return CustomPrice{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := NormalizeModelID(price.ModelID)
	if existing, found := s.state.Prices[key]; found {
		// Preserve the stored spelling when updating the same model.
		price.ModelID = existing.ModelID
	}
	if s.repo != nil {
		if err := s.repo.UpsertPrice(price); err != nil {
			return CustomPrice{}, err
		}
	}
	s.state.Prices[key] = price
	return price, nil
}

func (s *Store) DeletePrice(model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := NormalizeModelID(model)
	price, found := s.state.Prices[key]
	if !found {
		return notFoundf("Custom price does not exist")
	}
	if s.repo != nil {
		if err := s.repo.DeletePrice(price.ModelID); err != nil {
			return err
		}
	}
	delete(s.state.Prices, key)
	return nil
}

// Admission may refresh synchronously; usage passes false to avoid network I/O.
func (s *Store) ResolveModelPrice(upstream, requested string, refresh bool) (Price, string, error) {
	var model string
	var price Price
	var references *referencePriceManager
	s.read(func(state *State) {
		model = state.ResolveBillingModel(upstream, requested)
		price = state.ResolveCustomPrice(model)
		if price.Source == PriceSourceNone {
			price = ResolveBuiltinPrice(model)
		}
		references = s.referencePrices.Load()
	})
	if price.Source != PriceSourceNone {
		return price, model, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), referencePriceOperationTimeout)
	defer cancel()
	match, err := references.lookup(ctx, upstream, requested)
	if err == nil && refresh && referencePricesNeedRefresh(match.metadata, match.found, s.Now()) {
		_, refreshErr := references.refresh(ctx, s.Now, false, match.refreshSequence, match.found)
		if refreshErr == nil {
			match, err = references.lookup(ctx, upstream, requested)
		}
		var storageErr *referencePriceStorageError
		if !match.found && errors.As(refreshErr, &storageErr) {
			err = refreshErr
		}
	}
	// Recheck custom prices after reference I/O, including failed operations.
	var switched bool
	s.read(func(state *State) {
		switched = s.referencePrices.Load() != references
		model = state.ResolveBillingModel(upstream, requested)
		price = state.ResolveCustomPrice(model)
	})
	if switched {
		return Price{Source: PriceSourceNone}, model, fmt.Errorf("The reference price database changed; please retry")
	}
	if price.Source == PriceSourceCustom {
		return price, model, nil
	}
	if price = ResolveBuiltinPrice(model); price.Source != PriceSourceNone {
		return price, model, nil
	}
	if err != nil {
		return price, model, err
	}
	if match.found {
		price = match.price.resolve(PriceSourceReference, USD)
	}
	return price, model, nil
}

// ResolveCustomPrice uses only the client-visible billing model. A provider's
// upstream model must not select a different custom price during usage billing.
func (s *State) ResolveCustomPrice(billingModel string) Price {
	if price, ok := s.Prices[NormalizeModelID(billingModel)]; ok {
		return price.PriceRates.resolve(PriceSourceCustom, price.Currency)
	}
	return Price{Source: PriceSourceNone}
}

// ResolveBillingModel returns the stable client-visible model used for pricing
// and aggregation. A trailing CPA thinking suffix is a request option unless an
// existing custom price includes it in the model ID.
func (s *State) ResolveBillingModel(upstreamModel, routeModel string) string {
	upstreamModel = ModelWithoutThinkingSuffix(upstreamModel)
	routeModel = strings.TrimSpace(routeModel)
	if routeModel == "" {
		return upstreamModel
	}

	base := ModelWithoutThinkingSuffix(routeModel)
	if strings.EqualFold(base, "auto") {
		return upstreamModel
	}
	if price, ok := s.Prices[NormalizeModelID(routeModel)]; ok {
		return price.ModelID
	}
	if price, ok := s.Prices[NormalizeModelID(base)]; ok {
		return price.ModelID
	}
	if strings.EqualFold(base, upstreamModel) {
		return upstreamModel
	}
	return base
}

// ModelWithoutThinkingSuffix removes the trailing CPA request option, preserving
// the model spelling and routing prefix.
func ModelWithoutThinkingSuffix(model string) string {
	model = strings.TrimSpace(model)
	if open := strings.LastIndex(model, "("); open >= 0 && strings.HasSuffix(model, ")") {
		return strings.TrimSpace(model[:open])
	}
	return model
}

func NormalizeModelID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}
