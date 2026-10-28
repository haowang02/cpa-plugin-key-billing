package sqlite

import (
	"database/sql"
	"fmt"

	"cpa-key-billing/internal/billing"
)

func (d *DB) UpsertPrice(price billing.CustomPrice) error {
	if err := price.Validate(); err != nil {
		return err
	}
	var (
		threshold                                  any
		tierInput, tierOutput, tierRead, tierWrite any
	)
	if tier := price.LongContext; tier != nil {
		threshold = tier.ThresholdInputTokens
		tierInput = tier.InputPer1M
		tierOutput = tier.OutputPer1M
		tierRead = optionalPrice(tier.CacheReadPer1M)
		tierWrite = optionalPrice(tier.CacheWritePer1M)
	}
	_, err := d.db.Exec(`
        INSERT INTO prices (
            model_id, currency, input_per_1m, output_per_1m, cache_read_per_1m, cache_write_per_1m,
            long_context_threshold, long_context_input_per_1m, long_context_output_per_1m,
            long_context_cache_read_per_1m, long_context_cache_write_per_1m
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(model_id COLLATE NOCASE) DO UPDATE SET
            currency = excluded.currency,
            input_per_1m = excluded.input_per_1m,
            output_per_1m = excluded.output_per_1m,
            cache_read_per_1m = excluded.cache_read_per_1m,
            cache_write_per_1m = excluded.cache_write_per_1m,
            long_context_threshold = excluded.long_context_threshold,
            long_context_input_per_1m = excluded.long_context_input_per_1m,
            long_context_output_per_1m = excluded.long_context_output_per_1m,
            long_context_cache_read_per_1m = excluded.long_context_cache_read_per_1m,
            long_context_cache_write_per_1m = excluded.long_context_cache_write_per_1m
    `, price.ModelID, string(price.Currency), price.InputPer1M, price.OutputPer1M,
		optionalPrice(price.CacheReadPer1M), optionalPrice(price.CacheWritePer1M),
		threshold, tierInput, tierOutput, tierRead, tierWrite)
	if err != nil {
		return fmt.Errorf("Save pricing for model %s: %w", price.ModelID, err)
	}
	return nil
}

func (d *DB) DeletePrice(modelID string) error {
	if _, err := d.db.Exec("DELETE FROM prices WHERE model_id = ? COLLATE NOCASE", modelID); err != nil {
		return fmt.Errorf("Delete pricing for model %s: %w", modelID, err)
	}
	return nil
}

func (d *DB) loadPrices(state *billing.State) error {
	rows, errQuery := d.db.Query(`
		SELECT model_id, currency, input_per_1m, output_per_1m, cache_read_per_1m, cache_write_per_1m,
			long_context_threshold, long_context_input_per_1m, long_context_output_per_1m,
			long_context_cache_read_per_1m, long_context_cache_write_per_1m
		FROM prices ORDER BY position`)
	if errQuery != nil {
		return fmt.Errorf("Read model pricing: %w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			price                                      billing.CustomPrice
			cacheRead, cacheWrite                      sql.NullFloat64
			threshold                                  sql.NullInt64
			tierInput, tierOutput, tierRead, tierWrite sql.NullFloat64
		)
		if errScan := rows.Scan(&price.ModelID, &price.Currency, &price.InputPer1M, &price.OutputPer1M, &cacheRead, &cacheWrite,
			&threshold, &tierInput, &tierOutput, &tierRead, &tierWrite); errScan != nil {
			return fmt.Errorf("Read model pricing: %w", errScan)
		}
		price.CacheReadPer1M = priceOrNil(cacheRead)
		price.CacheWritePer1M = priceOrNil(cacheWrite)
		if threshold.Valid {
			price.LongContext = &billing.LongContextPrice{
				ThresholdInputTokens: threshold.Int64,
				InputPer1M:           tierInput.Float64,
				OutputPer1M:          tierOutput.Float64,
				CacheReadPer1M:       priceOrNil(tierRead),
				CacheWritePer1M:      priceOrNil(tierWrite),
			}
		}
		state.Prices[billing.NormalizeModelID(price.ModelID)] = price
	}
	if errRows := rows.Err(); errRows != nil {
		return fmt.Errorf("Read model pricing: %w", errRows)
	}
	return nil
}
