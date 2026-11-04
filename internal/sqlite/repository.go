package sqlite

import (
	"database/sql"
	"time"

	"cpa-key-billing/internal/billing"
)

func (d *DB) Load(requestEventCutoff, pluginLogCutoff time.Time) (billing.Snapshot, error) {
	if err := pruneRequestEvents(d.db.Exec, requestEventCutoff); err != nil {
		return billing.Snapshot{}, err
	}
	if err := prunePluginLogs(d.db.Exec, pluginLogCutoff); err != nil {
		return billing.Snapshot{}, err
	}

	state := billing.NewState()
	for _, load := range []func(*billing.State) error{
		d.loadPlans,
		d.loadRoutes,
		d.loadKeys,
		d.loadPrices,
		d.loadConfigCredentials,
	} {
		if err := load(state); err != nil {
			return billing.Snapshot{}, err
		}
	}
	count, err := d.requestEventCount()
	if err != nil {
		return billing.Snapshot{}, err
	}
	return billing.Snapshot{State: state, RequestEventCount: count}, nil
}

func (d *DB) Save(state *billing.State, changes billing.Changes) error {
	return d.transact(func(tx *sql.Tx) error {
		if err := saveStateTables(tx, state, changes); err != nil {
			return err
		}
		for _, event := range changes.NormalRequestEvents {
			event.Failed = false
			if _, err := appendRequestEvent(tx, event); err != nil {
				return err
			}
		}
		for _, event := range changes.RequestErrorEvents {
			if err := appendRequestErrorEvent(tx, event); err != nil {
				return err
			}
		}
		return pruneRequestEvents(tx.Exec, changes.RequestEventCutoff)
	})
}

func saveStateTables(tx *sql.Tx, state *billing.State, changes billing.Changes) error {
	if changes.AllKeys {
		for scope, key := range state.Keys {
			if err := saveKey(tx, scope, key); err != nil {
				return err
			}
		}
	} else {
		for _, scope := range changes.Keys {
			if err := saveKey(tx, scope, state.Keys[scope]); err != nil {
				return err
			}
		}
	}
	if changes.Plans {
		if err := replacePlans(tx, state); err != nil {
			return err
		}
	} else {
		for _, id := range changes.PlanCycles {
			if plan := state.FindPlan(id); plan != nil {
				if err := savePlanCycles(tx, plan); err != nil {
					return err
				}
			}
		}
	}
	if changes.Routes {
		if err := replaceRoutes(tx, state); err != nil {
			return err
		}
	}
	if changes.ConfigCredentials {
		return replaceConfigCredentials(tx, state)
	}
	return nil
}

var _ billing.Repository = (*DB)(nil)
