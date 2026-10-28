package sqlite

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"cpa-key-billing/internal/billing"
)

// A scope reads its fewer requests through its own index; the unary + keeps
// SQLite off the auth index.
func (d *DB) CredentialUsageStats(authIndexes []string, scope string, from, to time.Time, rate billing.ExchangeRate) (map[string]billing.CredentialUsageStats, error) {
	filter, args := authIndexFilter(authIndexes)
	filter += " AND at >= ? AND at < ?"
	args = append(args, nanos(from), nanos(to))
	if scope != "" {
		filter = "+" + filter + " AND scope = ?"
		args = append(args, scope)
	}
	rows, err := d.db.Query(`SELECT auth_index, count(*), coalesce(sum(failed != 0), 0),
		coalesce(sum(uncached_input_tokens + cache_read_tokens + cache_write_tokens), 0),
		coalesce(sum(cache_read_tokens), 0), coalesce(sum(billed_output_tokens), 0), coalesce(`+usdSumSQL("total_cost", rate)+`, 0),
		`+costCurrenciesSQL+` FROM request_events r WHERE `+filter+` GROUP BY auth_index`, args...)
	if err != nil {
		return nil, fmt.Errorf("Read credential usage: %w", err)
	}
	defer rows.Close()
	result := make(map[string]billing.CredentialUsageStats)
	for rows.Next() {
		var authIndex string
		var stats billing.CredentialUsageStats
		var currencies costCurrencies
		if err := rows.Scan(&authIndex, &stats.Requests, &stats.Failed, &stats.InputTokens,
			&stats.CacheReadTokens, &stats.OutputTokens, &stats.CostUSD, &currencies.usd, &currencies.cny); err != nil {
			return nil, fmt.Errorf("Read credential usage: %w", err)
		}
		stats.ChargedIn = currencies.chargedIn()
		result[authIndex] = stats
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("Read credential usage: %w", err)
	}
	return result, nil
}

func authIndexFilter(authIndexes []string) (string, []any) {
	args := make([]any, len(authIndexes))
	for i, authIndex := range authIndexes {
		args[i] = authIndex
	}
	return "auth_index IN (" + placeholders(len(args)) + ")", args
}

func placeholders(count int) string { return strings.TrimSuffix(strings.Repeat("?,", count), ",") }

// usdSumSQL sums an amount column of request events r in USD.
func usdSumSQL(column string, rate billing.ExchangeRate) string {
	return "sum(CASE r.currency WHEN '" + string(billing.CNY) + "' THEN r." + column + " / " +
		strconv.FormatFloat(float64(rate), 'g', -1, 64) + " ELSE r." + column + " END)"
}

// costCurrenciesSQL reads into costCurrencies whether request events r cost
// anything in USD and in CNY.
const costCurrenciesSQL = "coalesce(max(r.total_cost != 0 AND r.currency != '" + string(billing.CNY) + "'), 0), " +
	"coalesce(max(r.total_cost != 0 AND r.currency = '" + string(billing.CNY) + "'), 0)"

type costCurrencies struct{ usd, cny bool }

func (c costCurrencies) with(other costCurrencies) costCurrencies {
	return costCurrencies{usd: c.usd || other.usd, cny: c.cny || other.cny}
}

func (c costCurrencies) chargedIn() billing.Currency {
	switch {
	case c.usd && !c.cny:
		return billing.USD
	case c.cny && !c.usd:
		return billing.CNY
	}
	return ""
}

// usageFilter selects the requests r of a usage query; a non-empty scope
// limits them to one API key's.
func usageFilter(scope string, query billing.UsageQuery) (string, []any) {
	where := "r.at >= ? AND r.at < ?"
	args := []any{nanos(query.From), nanos(query.To)}
	equal := func(expression, value string) {
		if value != "" {
			where += " AND " + expression + " = ?"
			args = append(args, value)
		}
	}
	equal("r.scope", scope)
	equal("r.auth_index", query.AuthIndex)
	equal("lower(r.provider)", strings.ToLower(query.Provider))
	for _, filter := range []struct {
		expression string
		values     []string
	}{{"r.scope", query.KeyScopes}, {eventModelSQL, query.Models}, {"(" + requestEventSourceName + ")", query.Sources}} {
		if len(filter.values) > 0 {
			where += " AND " + filter.expression + " IN (" + placeholders(len(filter.values)) + ")"
			for _, value := range filter.values {
				args = append(args, value)
			}
		}
	}
	matchModels := func(fragments []string) string {
		terms := make([]string, len(fragments))
		for i, fragment := range fragments {
			terms[i] = "instr(lower(r.upstream_model), ?) > 0"
			args = append(args, strings.ToLower(fragment))
		}
		return "(" + strings.Join(terms, " OR ") + ")"
	}
	if len(query.Include) > 0 {
		where += " AND " + matchModels(query.Include)
	}
	if len(query.Exclude) > 0 {
		where += " AND NOT " + matchModels(query.Exclude)
	}
	return where, args
}

func (d *DB) UsageTotal(scope string, query billing.UsageQuery, rate billing.ExchangeRate) (billing.UsageTotal, error) {
	where, args := usageFilter(scope, query)
	var usage billing.UsageTotal
	var currencies costCurrencies
	if err := d.db.QueryRow(`SELECT
		coalesce(sum(r.uncached_input_tokens + r.cache_read_tokens + r.cache_write_tokens + r.billed_output_tokens), 0),
		coalesce(`+usdSumSQL("total_cost", rate)+`, 0), `+costCurrenciesSQL+`
		FROM request_events r WHERE `+where, args...).Scan(&usage.Tokens, &usage.CostUSD, &currencies.usd, &currencies.cny); err != nil {
		return usage, fmt.Errorf("Read usage: %w", err)
	}
	usage.ChargedIn = currencies.chargedIn()
	return usage, nil
}

// UsageByModel lists the most tokens first. InputTokens include cached ones.
func (d *DB) UsageByModel(scope string, query billing.UsageQuery, rate billing.ExchangeRate) ([]billing.ModelUsage, error) {
	where, args := usageFilter(scope, query)
	rows, err := d.db.Query(`SELECT `+eventModelSQL+` AS model, count(*),
		coalesce(sum(r.uncached_input_tokens + r.cache_read_tokens + r.cache_write_tokens), 0) AS input,
		coalesce(sum(r.billed_output_tokens), 0) AS output, coalesce(sum(r.cache_read_tokens), 0),
		coalesce(sum(r.cache_write_tokens), 0), coalesce(`+usdSumSQL("total_cost", rate)+`, 0), `+costCurrenciesSQL+`
		FROM request_events r WHERE `+where+` GROUP BY model ORDER BY input + output DESC, model`, args...)
	if err != nil {
		return nil, fmt.Errorf("Read usage by model: %w", err)
	}
	defer rows.Close()
	models := []billing.ModelUsage{}
	for rows.Next() {
		var usage billing.ModelUsage
		var currencies costCurrencies
		if err := rows.Scan(&usage.Model, &usage.Requests, &usage.InputTokens, &usage.OutputTokens,
			&usage.CacheReadTokens, &usage.CacheWriteTokens, &usage.CostUSD, &currencies.usd, &currencies.cny); err != nil {
			return nil, fmt.Errorf("Read usage by model: %w", err)
		}
		usage.ChargedIn = currencies.chargedIn()
		models = append(models, usage)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("Read usage by model: %w", err)
	}
	return models, nil
}

func (d *DB) UsageSources(scope string, query billing.UsageQuery) ([]string, error) {
	where, args := usageFilter(scope, query)
	rows, err := d.db.Query(`SELECT DISTINCT `+requestEventSourceName+` FROM request_events r WHERE `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("Read usage sources: %w", err)
	}
	defer rows.Close()
	var sources []string
	for rows.Next() {
		var source string
		if err := rows.Scan(&source); err != nil {
			return nil, fmt.Errorf("Read usage sources: %w", err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("Read usage sources: %w", err)
	}
	return sources, nil
}
