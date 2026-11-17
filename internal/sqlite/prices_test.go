package sqlite

import (
	"path/filepath"
	"reflect"
	"testing"

	"cpa-key-billing/internal/billing"
)

func TestCustomPriceUpdatesTouchOnlyTheirModelAndPublishAfterCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store := billing.NewStore(func(path string) (billing.Repository, error) { return Open(path, nil) }, nil)
	defer store.Close()
	cfg := billing.DefaultConfig()
	cfg.StateFile = path
	if err := store.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"Model-A", "untouched"} {
		if _, err := store.UpsertPrice(billing.CustomPrice{
			ModelID: model, PriceRates: billing.PriceRates{InputPer1M: 1, ImageInputPer1M: price(8), ImageCacheReadPer1M: price(0)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	database := openDatabase(t, path)
	defer database.Close()
	if _, err := database.db.Exec(`
        CREATE TABLE price_writes (operation TEXT, model_id TEXT);
        CREATE TRIGGER price_insert AFTER INSERT ON prices BEGIN
            INSERT INTO price_writes VALUES ('insert', NEW.model_id);
        END;
        CREATE TRIGGER price_update AFTER UPDATE ON prices BEGIN
            INSERT INTO price_writes VALUES ('update', NEW.model_id);
        END;
        CREATE TRIGGER price_delete AFTER DELETE ON prices BEGIN
            INSERT INTO price_writes VALUES ('delete', OLD.model_id);
        END;
    `); err != nil {
		t.Fatal(err)
	}
	var originalPosition int
	if err := database.db.QueryRow("SELECT position FROM prices WHERE model_id='Model-A'").Scan(&originalPosition); err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpsertPrice(billing.CustomPrice{
		ModelID: " MODEL-A ", PriceRates: billing.PriceRates{
			InputPer1M: 7, ImageInputPer1M: price(9), ImageOutputPer1M: price(32),
			LongContext: &billing.LongContextPrice{ThresholdInputTokens: 100, InputPer1M: 9},
		},
	})
	if err != nil || updated.ModelID != "Model-A" {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if _, err := store.UpsertPrice(billing.CustomPrice{ModelID: "new-model"}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeletePrice(" NEW-MODEL "); err != nil {
		t.Fatal(err)
	}
	var updates, inserts, deletes, unrelated, currentPosition int
	if err := database.db.QueryRow(`SELECT
        count(*) FILTER (WHERE operation='update'), count(*) FILTER (WHERE operation='insert'),
        count(*) FILTER (WHERE operation='delete'), count(*) FILTER (WHERE model_id='untouched')
        FROM price_writes`).Scan(&updates, &inserts, &deletes, &unrelated); err != nil {
		t.Fatal(err)
	}
	if updates != 1 || inserts != 1 || deletes != 1 || unrelated != 0 {
		t.Fatalf("writes: update=%d insert=%d delete=%d unrelated=%d", updates, inserts, deletes, unrelated)
	}
	if err := database.db.QueryRow("SELECT position FROM prices WHERE model_id='Model-A'").Scan(&currentPosition); err != nil || currentPosition != originalPosition {
		t.Fatalf("updated model was replaced: position=%d, err=%v", currentPosition, err)
	}
	if _, err := database.db.Exec(`
        CREATE TRIGGER reject_price_update BEFORE UPDATE ON prices BEGIN
            SELECT RAISE(ABORT, 'simulated update failure');
        END;
        CREATE TRIGGER reject_price_delete BEFORE DELETE ON prices BEGIN
            SELECT RAISE(ABORT, 'simulated delete failure');
        END;
    `); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertPrice(billing.CustomPrice{ModelID: "Model-A", PriceRates: billing.PriceRates{InputPer1M: 99}}); err == nil {
		t.Fatal("write failure was ignored")
	}
	if err := store.DeletePrice("model-a"); err == nil {
		t.Fatal("delete failure was ignored")
	}
	price, model, err := store.ResolveModelPrice("upstream", "model-a(high)", false)
	if err != nil || model != "Model-A" || price.InputPer1M != 7 || price.LongContext == nil || price.LongContext.InputPer1M != 9 {
		t.Fatalf("failed mutation changed live price: %+v, %q, %v", price, model, err)
	}
	persisted := mustLoad(t, database).State.Prices
	if cached := persisted["untouched"].ImageCacheReadPer1M; cached == nil || *cached != 0 {
		t.Fatal("explicit zero image rate was lost")
	}
	if stored := persisted["model-a"]; !reflect.DeepEqual(stored.PriceRates, updated.PriceRates) {
		t.Fatalf("failed mutation changed persisted price: %+v", stored)
	}
	store.Close()
	if err := store.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	price, model, err = store.ResolveModelPrice("upstream", "MODEL-A(high)", false)
	if err != nil || model != "Model-A" || price.InputPer1M != 7 || price.ImageInputPer1M == nil || *price.ImageInputPer1M != 9 ||
		price.ImageCacheReadPer1M != nil || price.ImageOutputPer1M == nil || *price.ImageOutputPer1M != 32 {
		t.Fatalf("restart lost indexed price: %+v, %q, %v", price, model, err)
	}
}
