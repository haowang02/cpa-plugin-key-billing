package sqlite

import (
	"database/sql"
	"fmt"
	"time"

	"cpa-key-billing/internal/billing"
)

func (d *DB) Load(cutoff time.Time) (billing.Snapshot, error) {
	if errPrune := pruneLog(d.db.Exec, cutoff); errPrune != nil {
		return billing.Snapshot{}, errPrune
	}
	if errPrune := pruneEvents(d.db.Exec, cutoff); errPrune != nil {
		return billing.Snapshot{}, errPrune
	}
	state := billing.NewState()
	if errKeys := d.loadKeys(state); errKeys != nil {
		return billing.Snapshot{}, errKeys
	}
	if errPlans := d.loadPlans(state); errPlans != nil {
		return billing.Snapshot{}, errPlans
	}
	if errPrices := d.loadPrices(state); errPrices != nil {
		return billing.Snapshot{}, errPrices
	}
	if errGroups := d.loadModelGroups(state); errGroups != nil {
		return billing.Snapshot{}, errGroups
	}
	if errCredentials := d.loadCredentials(state); errCredentials != nil {
		return billing.Snapshot{}, errCredentials
	}
	snapshot := billing.Snapshot{State: state}
	if errCount := d.db.QueryRow("SELECT count(*) FROM usage_log").Scan(&snapshot.LogEntries); errCount != nil {
		return billing.Snapshot{}, fmt.Errorf("读取计费日志条数：%w", errCount)
	}
	return snapshot, nil
}

// Save is the single write path. Everything one mutation touched lands in one
// transaction, so a crash cannot leave a charged request without its log entry
// or a plan without its bindings.
func (d *DB) Save(state *billing.State, changes billing.Changes) error {
	return d.transact(func(tx *sql.Tx) error {
		if changes.AllKeys {
			if errKeys := replaceKeys(tx, state); errKeys != nil {
				return errKeys
			}
		} else {
			for _, scope := range changes.Keys {
				if errKey := saveKey(tx, scope, state.Keys[scope]); errKey != nil {
					return errKey
				}
			}
		}
		if changes.Plans {
			if errPlans := replacePlans(tx, state); errPlans != nil {
				return errPlans
			}
		}
		if changes.Prices {
			if errPrices := replacePrices(tx, state); errPrices != nil {
				return errPrices
			}
		}
		if changes.ModelGroups {
			if errGroups := replaceModelGroups(tx, state); errGroups != nil {
				return errGroups
			}
		}
		if changes.Credentials {
			if errCredentials := replaceCredentials(tx, state); errCredentials != nil {
				return errCredentials
			}
		}
		return appendLog(tx, changes)
	})
}

const insertKey = `
INSERT INTO api_keys (
	scope, preview, label, in_config, deleted_at, plan_id, concurrency_limit,
	cycle_plan_id, cycle_start_at, cycle_end_at, cycle_spent_usd,
	cost_usd, requests, uncached_input_tokens, output_tokens,
	reasoning_tokens, cache_read_tokens, cache_creation_tokens
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(scope) DO UPDATE SET
	preview = excluded.preview, label = excluded.label, in_config = excluded.in_config,
	deleted_at = excluded.deleted_at, plan_id = excluded.plan_id,
	concurrency_limit = excluded.concurrency_limit,
	cycle_plan_id = excluded.cycle_plan_id, cycle_start_at = excluded.cycle_start_at,
	cycle_end_at = excluded.cycle_end_at, cycle_spent_usd = excluded.cycle_spent_usd,
	cost_usd = excluded.cost_usd, requests = excluded.requests,
	uncached_input_tokens = excluded.uncached_input_tokens, output_tokens = excluded.output_tokens,
	reasoning_tokens = excluded.reasoning_tokens, cache_read_tokens = excluded.cache_read_tokens,
	cache_creation_tokens = excluded.cache_creation_tokens`

// A named scope the state no longer holds was dropped by the mutation.
func saveKey(tx *sql.Tx, scope string, key *billing.KeyState) error {
	if key == nil {
		if _, errDelete := tx.Exec("DELETE FROM api_keys WHERE scope = ?", scope); errDelete != nil {
			return fmt.Errorf("删除 API Key %s：%w", scope, errDelete)
		}
		return nil
	}
	_, errKey := tx.Exec(insertKey,
		scope, key.Preview, key.Label, key.InConfig, nanos(key.DeletedAt), key.PlanID, key.ConcurrencyLimit,
		key.Cycle.PlanID, nanos(key.Cycle.StartAt), nanos(key.Cycle.EndAt), key.Cycle.SpentUSD,
		key.Lifetime.CostUSD, key.Lifetime.Requests, key.Lifetime.UncachedInputTokens,
		key.Lifetime.OutputTokens, key.Lifetime.ReasoningTokens, key.Lifetime.CacheReadTokens,
		key.Lifetime.CacheCreationTokens)
	if errKey != nil {
		return fmt.Errorf("保存 API Key %s：%w", scope, errKey)
	}
	// Rewriting the per-model rows is how one that vanished from the key
	// disappears from the database too.
	if _, errClear := tx.Exec("DELETE FROM key_models WHERE scope = ?", scope); errClear != nil {
		return fmt.Errorf("保存 API Key %s 的模型用量：%w", scope, errClear)
	}
	for model, totals := range key.ByModel {
		if totals == nil {
			continue
		}
		_, errModel := tx.Exec(`
			INSERT INTO key_models (
				scope, billing_model, cost_usd, requests, uncached_input_tokens,
				output_tokens, reasoning_tokens, cache_read_tokens, cache_creation_tokens
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			scope, model, totals.CostUSD, totals.Requests, totals.UncachedInputTokens,
			totals.OutputTokens, totals.ReasoningTokens, totals.CacheReadTokens, totals.CacheCreationTokens)
		if errModel != nil {
			return fmt.Errorf("保存 API Key %s 的模型用量：%w", scope, errModel)
		}
	}
	return saveKeyModelAccess(tx, scope, key)
}

// The grant is rewritten in full, which is how a group or a model the key no
// longer selects leaves the database with it.
func saveKeyModelAccess(tx *sql.Tx, scope string, key *billing.KeyState) error {
	for _, table := range []string{"key_model_groups", "key_allowed_models"} {
		if _, errClear := tx.Exec("DELETE FROM "+table+" WHERE scope = ?", scope); errClear != nil {
			return fmt.Errorf("保存 API Key %s 的可用模型：%w", scope, errClear)
		}
	}
	for position, group := range key.ModelGroupIDs {
		_, errGroup := tx.Exec(`
			INSERT INTO key_model_groups (scope, position, group_id) VALUES (?, ?, ?)`,
			scope, position, group)
		if errGroup != nil {
			return fmt.Errorf("保存 API Key %s 的模型分组：%w", scope, errGroup)
		}
	}
	for position, model := range key.Models {
		_, errModel := tx.Exec(`
			INSERT INTO key_allowed_models (scope, position, model) VALUES (?, ?, ?)`,
			scope, position, model)
		if errModel != nil {
			return fmt.Errorf("保存 API Key %s 的可用模型：%w", scope, errModel)
		}
	}
	return nil
}

func replaceKeys(tx *sql.Tx, state *billing.State) error {
	if _, errClear := tx.Exec("DELETE FROM api_keys"); errClear != nil {
		return fmt.Errorf("保存 API Key 列表：%w", errClear)
	}
	for scope, key := range state.Keys {
		if errKey := saveKey(tx, scope, key); errKey != nil {
			return errKey
		}
	}
	return nil
}

func (d *DB) loadKeys(state *billing.State) error {
	rows, errQuery := d.db.Query(`
		SELECT scope, preview, label, in_config, deleted_at, plan_id, concurrency_limit,
			cycle_plan_id, cycle_start_at, cycle_end_at, cycle_spent_usd,
			cost_usd, requests, uncached_input_tokens, output_tokens,
			reasoning_tokens, cache_read_tokens, cache_creation_tokens
		FROM api_keys`)
	if errQuery != nil {
		return fmt.Errorf("读取 API Key 列表：%w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			scope                           string
			key                             billing.KeyState
			deletedAt, cycleStart, cycleEnd int64
		)
		if errScan := rows.Scan(&scope, &key.Preview, &key.Label, &key.InConfig, &deletedAt, &key.PlanID, &key.ConcurrencyLimit,
			&key.Cycle.PlanID, &cycleStart, &cycleEnd, &key.Cycle.SpentUSD,
			&key.Lifetime.CostUSD, &key.Lifetime.Requests, &key.Lifetime.UncachedInputTokens,
			&key.Lifetime.OutputTokens, &key.Lifetime.ReasoningTokens, &key.Lifetime.CacheReadTokens,
			&key.Lifetime.CacheCreationTokens); errScan != nil {
			return fmt.Errorf("读取 API Key 列表：%w", errScan)
		}
		key.DeletedAt = timeAt(deletedAt)
		key.Cycle.StartAt = timeAt(cycleStart)
		key.Cycle.EndAt = timeAt(cycleEnd)
		key.ByModel = make(map[string]*billing.Totals)
		state.Keys[scope] = &key
	}
	if errRows := rows.Err(); errRows != nil {
		return fmt.Errorf("读取 API Key 列表：%w", errRows)
	}
	if errModels := d.loadKeyModels(state); errModels != nil {
		return errModels
	}
	return d.loadKeyModelAccess(state)
}

func (d *DB) loadKeyModelAccess(state *billing.State) error {
	groups, errGroups := d.loadKeyStrings("key_model_groups", "group_id")
	if errGroups != nil {
		return errGroups
	}
	models, errModels := d.loadKeyStrings("key_allowed_models", "model")
	if errModels != nil {
		return errModels
	}
	for scope, key := range state.Keys {
		key.ModelGroupIDs = groups[scope]
		key.Models = models[scope]
	}
	return nil
}

func (d *DB) loadKeyStrings(table, column string) (map[string][]string, error) {
	rows, errQuery := d.db.Query("SELECT scope, " + column + " FROM " + table + " ORDER BY scope, position")
	if errQuery != nil {
		return nil, fmt.Errorf("读取 API Key 的可用模型：%w", errQuery)
	}
	defer rows.Close()
	values := make(map[string][]string)
	for rows.Next() {
		var scope, value string
		if errScan := rows.Scan(&scope, &value); errScan != nil {
			return nil, fmt.Errorf("读取 API Key 的可用模型：%w", errScan)
		}
		values[scope] = append(values[scope], value)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("读取 API Key 的可用模型：%w", errRows)
	}
	return values, nil
}

func replaceModelGroups(tx *sql.Tx, state *billing.State) error {
	for _, table := range []string{"model_groups", "model_group_models"} {
		if _, errClear := tx.Exec("DELETE FROM " + table); errClear != nil {
			return fmt.Errorf("保存模型分组：%w", errClear)
		}
	}
	for position, group := range state.ModelGroups {
		_, errGroup := tx.Exec(`
			INSERT INTO model_groups (position, id, name) VALUES (?, ?, ?)`,
			position, group.ID, group.Name)
		if errGroup != nil {
			return fmt.Errorf("保存模型分组 %s：%w", group.ID, errGroup)
		}
		for index, model := range group.Models {
			_, errModel := tx.Exec(`
				INSERT INTO model_group_models (group_id, position, model) VALUES (?, ?, ?)`,
				group.ID, index, model)
			if errModel != nil {
				return fmt.Errorf("保存模型分组 %s 的模型：%w", group.ID, errModel)
			}
		}
	}
	return nil
}

func (d *DB) loadModelGroups(state *billing.State) error {
	members, errMembers := d.loadModelGroupMembers()
	if errMembers != nil {
		return errMembers
	}
	rows, errQuery := d.db.Query("SELECT id, name FROM model_groups ORDER BY position")
	if errQuery != nil {
		return fmt.Errorf("读取模型分组：%w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var group billing.ModelGroup
		if errScan := rows.Scan(&group.ID, &group.Name); errScan != nil {
			return fmt.Errorf("读取模型分组：%w", errScan)
		}
		group.Models = members[group.ID]
		state.ModelGroups = append(state.ModelGroups, group)
	}
	if errRows := rows.Err(); errRows != nil {
		return fmt.Errorf("读取模型分组：%w", errRows)
	}
	return nil
}

func (d *DB) loadModelGroupMembers() (map[string][]string, error) {
	rows, errQuery := d.db.Query("SELECT group_id, model FROM model_group_models ORDER BY group_id, position")
	if errQuery != nil {
		return nil, fmt.Errorf("读取模型分组的模型：%w", errQuery)
	}
	defer rows.Close()
	members := make(map[string][]string)
	for rows.Next() {
		var group, model string
		if errScan := rows.Scan(&group, &model); errScan != nil {
			return nil, fmt.Errorf("读取模型分组的模型：%w", errScan)
		}
		members[group] = append(members[group], model)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("读取模型分组的模型：%w", errRows)
	}
	return members, nil
}

func (d *DB) loadKeyModels(state *billing.State) error {
	rows, errQuery := d.db.Query(`
		SELECT scope, billing_model, cost_usd, requests, uncached_input_tokens,
			output_tokens, reasoning_tokens, cache_read_tokens, cache_creation_tokens
		FROM key_models`)
	if errQuery != nil {
		return fmt.Errorf("读取模型用量：%w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			scope, model string
			totals       billing.Totals
		)
		if errScan := rows.Scan(&scope, &model, &totals.CostUSD, &totals.Requests,
			&totals.UncachedInputTokens, &totals.OutputTokens, &totals.ReasoningTokens,
			&totals.CacheReadTokens, &totals.CacheCreationTokens); errScan != nil {
			return fmt.Errorf("读取模型用量：%w", errScan)
		}
		if key := state.Keys[scope]; key != nil {
			key.ByModel[model] = &totals
		}
	}
	if errRows := rows.Err(); errRows != nil {
		return fmt.Errorf("读取模型用量：%w", errRows)
	}
	return nil
}

func replacePlans(tx *sql.Tx, state *billing.State) error {
	if _, errClear := tx.Exec("DELETE FROM plans"); errClear != nil {
		return fmt.Errorf("保存订阅计划：%w", errClear)
	}
	for position, plan := range state.Plans {
		_, errPlan := tx.Exec(`
			INSERT INTO plans (position, id, name, amount_usd, period_kind, period_seconds)
			VALUES (?, ?, ?, ?, ?, ?)`,
			position, plan.ID, plan.Name, plan.AmountUSD, string(plan.Period.Kind), plan.Period.Seconds)
		if errPlan != nil {
			return fmt.Errorf("保存订阅计划 %s：%w", plan.ID, errPlan)
		}
	}
	return nil
}

func (d *DB) loadPlans(state *billing.State) error {
	rows, errQuery := d.db.Query(`
		SELECT id, name, amount_usd, period_kind, period_seconds FROM plans ORDER BY position`)
	if errQuery != nil {
		return fmt.Errorf("读取订阅计划：%w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			plan billing.Plan
			kind string
		)
		if errScan := rows.Scan(&plan.ID, &plan.Name, &plan.AmountUSD, &kind, &plan.Period.Seconds); errScan != nil {
			return fmt.Errorf("读取订阅计划：%w", errScan)
		}
		plan.Period.Kind = billing.PeriodKind(kind)
		state.Plans = append(state.Plans, plan)
	}
	if errRows := rows.Err(); errRows != nil {
		return fmt.Errorf("读取订阅计划：%w", errRows)
	}
	return nil
}

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

// The display name is stored beside the credential it belongs to so that a log
// query can search and show it without this package restating how a credential
// is named.
func replaceCredentials(tx *sql.Tx, state *billing.State) error {
	if _, errClear := tx.Exec("DELETE FROM credentials"); errClear != nil {
		return fmt.Errorf("保存上游凭据：%w", errClear)
	}
	for index, credential := range state.Credentials {
		_, errCredential := tx.Exec(`
			INSERT INTO credentials (auth_index, provider, account, name) VALUES (?, ?, ?, ?)`,
			index, credential.Provider, credential.Account, credential.Name())
		if errCredential != nil {
			return fmt.Errorf("保存上游凭据 %s：%w", index, errCredential)
		}
	}
	return nil
}

func (d *DB) loadCredentials(state *billing.State) error {
	rows, errQuery := d.db.Query("SELECT auth_index, provider, account FROM credentials")
	if errQuery != nil {
		return fmt.Errorf("读取上游凭据：%w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			index      string
			credential billing.Credential
		)
		if errScan := rows.Scan(&index, &credential.Provider, &credential.Account); errScan != nil {
			return fmt.Errorf("读取上游凭据：%w", errScan)
		}
		state.Credentials[index] = credential
	}
	if errRows := rows.Err(); errRows != nil {
		return fmt.Errorf("读取上游凭据：%w", errRows)
	}
	return nil
}
