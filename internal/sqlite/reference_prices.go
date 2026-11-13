package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"cpa-key-billing/internal/billing"
)

// Reference prices use a separate connection so queries do not take the billing
// Store lock. Downloads and parsing finish before the write transaction starts.
func (d *DB) OpenReferencePrices() (billing.ReferencePriceRepository, error) {
	return connect(d.path)
}

func (d *DB) LoadReferencePriceMetadata(ctx context.Context) (billing.ReferencePriceMetadata, error) {
	var metadata billing.ReferencePriceMetadata
	var fetchedAt, lastAttemptAt, retryAfter int64
	err := d.db.QueryRowContext(ctx, `
        SELECT source_url, content_hash, version, fetched_at, model_count,
               last_attempt_at, retry_after, last_error, consecutive_failures
        FROM reference_prices_metadata WHERE id = 1
    `).Scan(&metadata.SourceURL, &metadata.ContentHash, &metadata.Version,
		&fetchedAt, &metadata.ModelCount, &lastAttemptAt, &retryAfter,
		&metadata.LastError, &metadata.ConsecutiveFailures)
	if err == sql.ErrNoRows {
		return metadata, nil
	}
	if err != nil {
		return metadata, fmt.Errorf("Read reference price metadata: %w", err)
	}
	metadata.FetchedAt = timeAt(fetchedAt)
	metadata.LastAttemptAt = timeAt(lastAttemptAt)
	metadata.RetryAfter = timeAt(retryAfter)

	var modelCount, pricedCount int
	err = d.db.QueryRowContext(ctx, `
        SELECT count(*), count(rates_json) FROM reference_prices
    `).Scan(&modelCount, &pricedCount)
	if err != nil {
		return metadata, fmt.Errorf("Check reference price integrity: %w", err)
	}
	metadata.Usable = modelCount == metadata.ModelCount && pricedCount > 0
	return metadata, nil
}

// A nil prices slice touches metadata only. A replacement and its metadata
// always commit together; empty reference price data is not a valid replacement.
func (d *DB) SaveReferencePrices(ctx context.Context, metadata billing.ReferencePriceMetadata, prices []billing.ReferencePrice) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if prices != nil {
		if err := replaceReferencePrices(ctx, tx, prices); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
        INSERT INTO reference_prices_metadata (
            id, source_url, content_hash, version, fetched_at, model_count,
            last_attempt_at, retry_after, last_error, consecutive_failures
        ) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            source_url = excluded.source_url,
            content_hash = excluded.content_hash,
            version = excluded.version,
            fetched_at = excluded.fetched_at,
            model_count = excluded.model_count,
            last_attempt_at = excluded.last_attempt_at,
            retry_after = excluded.retry_after,
            last_error = excluded.last_error,
            consecutive_failures = excluded.consecutive_failures
    `, metadata.SourceURL, metadata.ContentHash, metadata.Version,
		nanos(metadata.FetchedAt), metadata.ModelCount, nanos(metadata.LastAttemptAt),
		nanos(metadata.RetryAfter), metadata.LastError, metadata.ConsecutiveFailures)
	if err != nil {
		return fmt.Errorf("Save reference price metadata: %w", err)
	}
	return tx.Commit()
}

func replaceReferencePrices(ctx context.Context, tx *sql.Tx, prices []billing.ReferencePrice) error {
	if len(prices) == 0 {
		return fmt.Errorf("Reference price data is required")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM reference_prices"); err != nil {
		return err
	}
	priceStatement, err := tx.PrepareContext(ctx, `
        INSERT INTO reference_prices(provider_id, model_id, match_key, is_canonical, lab_id, lab_name, rates_json)
        VALUES (?, ?, ?, ?, ?, ?, ?)
    `)
	if err != nil {
		return err
	}
	defer priceStatement.Close()

	for _, price := range prices {
		if err := price.Validate(); err != nil {
			return err
		}
		var ratesJSON any
		if price.PriceRates != nil {
			encoded, err := json.Marshal(price.PriceRates)
			if err != nil {
				return err
			}
			ratesJSON = string(encoded)
		}
		if _, err := priceStatement.ExecContext(ctx, price.ProviderID, price.ModelID, billing.ReferenceModelKey(price.ModelID),
			price.IsCanonical, price.Lab.ID, price.Lab.Name, ratesJSON); err != nil {
			return fmt.Errorf("Save reference price %s/%s: %w", price.ProviderID, price.ModelID, err)
		}
	}
	return nil
}

// Candidate queries use the indexed match keys, without selecting a provider.
func (d *DB) ReferencePriceCandidates(ctx context.Context, keys []string) ([]billing.ReferencePrice, error) {
	arguments := make([]any, 0, len(keys))
	placeholders := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if len(key) > 1024 {
			return nil, fmt.Errorf("Model ID is too long")
		}
		arguments = append(arguments, key)
		placeholders = append(placeholders, "?")
	}
	if len(arguments) == 0 {
		return []billing.ReferencePrice{}, nil
	}
	rows, err := d.db.QueryContext(ctx, `
        SELECT provider_id, model_id, is_canonical, lab_id, lab_name, rates_json
        FROM reference_prices
        WHERE match_key IN (`+strings.Join(placeholders, ",")+`)
        ORDER BY provider_id, model_id
    `, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReferencePrices(rows)
}

func (d *DB) SearchReferencePrices(ctx context.Context, query string, limit int) ([]billing.ReferencePrice, error) {
	query = billing.NormalizeModelID(query)
	if query == "" {
		return []billing.ReferencePrice{}, nil
	}
	key := billing.ReferenceModelKey(billing.ModelWithoutThinkingSuffix(query))
	rows, err := d.db.QueryContext(ctx, `
        SELECT provider_id, model_id, is_canonical, lab_id, lab_name, rates_json FROM reference_prices
        WHERE rates_json IS NOT NULL AND (
            match_key = ? OR instr(lower(provider_id || '/' || model_id), ?) > 0
        )
        ORDER BY CASE
            WHEN lower(provider_id || '/' || model_id) = ? THEN 0
            WHEN match_key = ? THEN 1
            ELSE 2
        END, is_canonical DESC, provider_id, model_id
        LIMIT ?
    `, key, query, query, key, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReferencePrices(rows)
}

func scanReferencePrices(rows *sql.Rows) ([]billing.ReferencePrice, error) {
	prices := []billing.ReferencePrice{}
	for rows.Next() {
		var price billing.ReferencePrice
		var ratesJSON sql.NullString
		if err := rows.Scan(&price.ProviderID, &price.ModelID, &price.IsCanonical, &price.Lab.ID, &price.Lab.Name, &ratesJSON); err != nil {
			return nil, err
		}
		if ratesJSON.Valid {
			if err := json.Unmarshal([]byte(ratesJSON.String), &price.PriceRates); err != nil {
				return nil, err
			}
		}
		prices = append(prices, price)
	}
	return prices, rows.Err()
}
