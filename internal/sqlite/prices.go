package sqlite

import (
	"database/sql"
	"fmt"

	"cpa-key-billing/internal/billing"
)

func replacePrices(tx *sql.Tx, state *billing.State) error {
	if _, errClear := tx.Exec("DELETE FROM prices"); errClear != nil {
		return fmt.Errorf("保存模型定价：%w", errClear)
	}
	for position, rule := range state.Prices {
		var (
			threshold                                  any
			tierInput, tierOutput, tierRead, tierWrite any
		)
		if tier := rule.LongContext; tier != nil {
			threshold = tier.ThresholdInputTokens
			tierInput = tier.InputPer1M
			tierOutput = tier.OutputPer1M
			tierRead = optionalPrice(tier.CacheReadPer1M)
			tierWrite = optionalPrice(tier.CacheWritePer1M)
		}
		_, errPrice := tx.Exec(`
			INSERT INTO prices (
				position, pattern, input_per_1m, output_per_1m, cache_read_per_1m, cache_write_per_1m,
				long_context_threshold, long_context_input_per_1m, long_context_output_per_1m,
				long_context_cache_read_per_1m, long_context_cache_write_per_1m
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			position, rule.Pattern, rule.InputPer1M, rule.OutputPer1M,
			optionalPrice(rule.CacheReadPer1M), optionalPrice(rule.CacheWritePer1M),
			threshold, tierInput, tierOutput, tierRead, tierWrite)
		if errPrice != nil {
			return fmt.Errorf("保存模型 %s 的定价：%w", rule.Pattern, errPrice)
		}
	}
	return nil
}

func (d *DB) loadPrices(state *billing.State) error {
	rows, errQuery := d.db.Query(`
		SELECT pattern, input_per_1m, output_per_1m, cache_read_per_1m, cache_write_per_1m,
			long_context_threshold, long_context_input_per_1m, long_context_output_per_1m,
			long_context_cache_read_per_1m, long_context_cache_write_per_1m
		FROM prices ORDER BY position`)
	if errQuery != nil {
		return fmt.Errorf("读取模型定价：%w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			rule                                       billing.PriceRule
			cacheRead, cacheWrite                      sql.NullFloat64
			threshold                                  sql.NullInt64
			tierInput, tierOutput, tierRead, tierWrite sql.NullFloat64
		)
		if errScan := rows.Scan(&rule.Pattern, &rule.InputPer1M, &rule.OutputPer1M, &cacheRead, &cacheWrite,
			&threshold, &tierInput, &tierOutput, &tierRead, &tierWrite); errScan != nil {
			return fmt.Errorf("读取模型定价：%w", errScan)
		}
		rule.CacheReadPer1M = priceOrNil(cacheRead)
		rule.CacheWritePer1M = priceOrNil(cacheWrite)
		if threshold.Valid {
			rule.LongContext = &billing.LongContextPrice{
				ThresholdInputTokens: threshold.Int64,
				InputPer1M:           tierInput.Float64,
				OutputPer1M:          tierOutput.Float64,
				CacheReadPer1M:       priceOrNil(tierRead),
				CacheWritePer1M:      priceOrNil(tierWrite),
			}
		}
		state.Prices = append(state.Prices, rule)
	}
	if errRows := rows.Err(); errRows != nil {
		return fmt.Errorf("读取模型定价：%w", errRows)
	}
	return nil
}
