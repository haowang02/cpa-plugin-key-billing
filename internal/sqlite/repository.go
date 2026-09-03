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
		d.loadKeys,
		d.loadPlans,
		d.loadPrices,
		d.loadRoutes,
		d.loadCredentials,
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
		if err := replaceKeys(tx, state); err != nil {
			return err
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
	}
	if changes.Prices {
		if err := replacePrices(tx, state); err != nil {
			return err
		}
	}
	if changes.Routes {
		if err := replaceRoutes(tx, state); err != nil {
			return err
		}
	}
	if changes.Credentials {
		return replaceCredentials(tx, state)
	}
	return nil
}

var _ billing.Repository = (*DB)(nil)
