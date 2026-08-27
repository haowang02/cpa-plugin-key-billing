package billing

import (
	"slices"
	"strings"
	"testing"
)

func newAccessStore(t *testing.T) *Store {
	t.Helper()
	store := newStore(t)
	store.ReplaceAll(func(state *State) {
		state.Prices = []PriceRule{
			{Pattern: "chat/fast"}, {Pattern: "chat/slow"}, {Pattern: "claude-sonnet-4-5"},
		}
		state.Keys["a"] = &KeyState{InConfig: true}
	})
	return store
}

func mustCreateGroup(t *testing.T, store *Store, name string, models ...string) ModelGroup {
	t.Helper()
	group, errCreate := store.CreateModelGroup(ModelGroup{Name: name, Models: models})
	if errCreate != nil {
		t.Fatalf("CreateModelGroup error = %v", errCreate)
	}
	return group
}

// The all-models grant is the absence of a selection rather than a group with
// every model in it, so nothing has to be written when a key is created and
// nothing goes stale when the proxy learns a new model.
func TestKeyWithoutSelectionMayCallEveryModel(t *testing.T) {
	store := newAccessStore(t)

	if !store.AuthorizeModel("a", "chat/fast", "chat/fast").Allowed {
		t.Fatal("a key with no selection was refused")
	}
	views := store.KeyViews()
	if len(views) != 1 || !views[0].AllModels {
		t.Fatalf("views = %+v, want the key reported as unrestricted", views)
	}

	group := mustCreateGroup(t, store, "基础", "chat/fast")
	if errSet := store.SetKeyModels("a", []string{group.ID}, nil); errSet != nil {
		t.Fatalf("SetKeyModels error = %v", errSet)
	}
	if store.AuthorizeModel("a", "chat/slow", "chat/slow").Allowed {
		t.Fatal("a model outside the key's only group was allowed")
	}

	// Selecting the all-models group is the way back, and it clears whatever
	// else the request carried alongside it.
	if errSet := store.SetKeyModels("a", []string{AllModelsGroupID, group.ID}, []string{"chat/slow"}); errSet != nil {
		t.Fatalf("SetKeyModels error = %v", errSet)
	}
	store.Read(func(state *State) {
		key := state.Keys["a"]
		if len(key.ModelGroupIDs) != 0 || len(key.Models) != 0 {
			t.Fatalf("selection = %+v / %+v, want both cleared", key.ModelGroupIDs, key.Models)
		}
	})
	if !store.AuthorizeModel("a", "chat/slow", "chat/slow").Allowed {
		t.Fatal("the all-models group did not restore access")
	}
}

func TestAllowedModelsUnionsGroupsAndSingleModels(t *testing.T) {
	store := newAccessStore(t)
	fast := mustCreateGroup(t, store, "快", "chat/fast", "claude-sonnet-4-5")
	slow := mustCreateGroup(t, store, "慢", "chat/slow", "chat/fast")

	if errSet := store.SetKeyModels("a", []string{fast.ID, slow.ID}, []string{"chat/slow", "extra-model"}); errSet != nil {
		t.Fatalf("SetKeyModels error = %v", errSet)
	}
	store.Read(func(state *State) {
		models, restricted := state.AllowedModels(state.Keys["a"])
		want := []string{"chat/fast", "chat/slow", "claude-sonnet-4-5", "extra-model"}
		if !restricted || !slices.Equal(models, want) {
			t.Fatalf("AllowedModels = %v (restricted %t), want %v", models, restricted, want)
		}
	})
}

// The identity enforcement tests is the one billing records, so what an operator
// grants is what they read back in the log.
func TestAuthorizeModelUsesTheBillingIdentity(t *testing.T) {
	store := newAccessStore(t)
	group := mustCreateGroup(t, store, "基础", "CHAT/Fast")
	if errSet := store.SetKeyModels("a", []string{group.ID}, nil); errSet != nil {
		t.Fatalf("SetKeyModels error = %v", errSet)
	}

	cases := []struct {
		name          string
		upstreamModel string
		routeModel    string
		want          bool
	}{
		{"exact", "chat/fast", "chat/fast", true},
		{"case folded", "chat/fast", "Chat/Fast", true},
		// A thinking suffix is a request option rather than a model, so it
		// neither needs granting of its own nor opens a way around a grant.
		{"thinking suffix", "chat/fast", "chat/fast(high)", true},
		{"thinking suffix on a refused model", "chat/slow", "chat/slow(high)", false},
		{"strongest thinking suffix on a refused model", "chat/slow", "chat/slow(max)", false},
		{"suffix on a refused model the price table has not seen", "", "unlisted-model(high)", false},
		// The suffix travels on the upstream model when a request names no
		// route of its own.
		{"suffix on the upstream model alone", "chat/slow(high)", "", false},
		{"another model", "chat/slow", "chat/slow", false},
		// An alias resolving onto a granted model must not carry a request the
		// key was never granted: the upstream model is not a candidate.
		{"alias onto a granted model", "chat/fast", "chat/slow", false},
		// A model the price table has not caught up with is still named by the
		// route, which is what an operator selected.
		{"unpriced route", "", "chat/fast", true},
		// Nothing to name, nothing to refuse.
		{"unnamed model", "", "", true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			decision := store.AuthorizeModel("a", testCase.upstreamModel, testCase.routeModel)
			if decision.Allowed != testCase.want {
				t.Fatalf("Allowed = %t, want %t (decision %+v)", decision.Allowed, testCase.want, decision)
			}
			// A refusal names the model the billing log would have named, which
			// is the one without the request's thinking suffix.
			if !decision.Allowed && strings.Contains(decision.Model, "(") {
				t.Fatalf("Model = %q, want the suffix left out of it", decision.Model)
			}
		})
	}
}

// A proxy may also expose a suffixed name as a model of its own. That one is a
// separate row in the price table and therefore a separate grant: it is the
// suffix in the configured model ID, not an option the client asked for.
func TestAuthorizeModelSeparatesAConfiguredSuffixedModel(t *testing.T) {
	store := newAccessStore(t)
	store.ReplaceAll(func(state *State) {
		state.Prices = append(state.Prices, PriceRule{Pattern: "chat/fast(high)"})
	})
	group := mustCreateGroup(t, store, "基础", "chat/fast")
	if errSet := store.SetKeyModels("a", []string{group.ID}, nil); errSet != nil {
		t.Fatalf("SetKeyModels error = %v", errSet)
	}

	if decision := store.AuthorizeModel("a", "chat/fast", "chat/fast(high)"); decision.Allowed {
		t.Fatal("a configured model whose ID carries the suffix was granted along with its base name")
	}
	if errSet := store.SetKeyModels("a", []string{group.ID}, []string{"chat/fast(high)"}); errSet != nil {
		t.Fatalf("SetKeyModels error = %v", errSet)
	}
	if decision := store.AuthorizeModel("a", "chat/fast", "chat/fast(high)"); !decision.Allowed {
		t.Fatalf("granting the configured model did not admit it: %+v", decision)
	}
}

func TestAuthorizeModelFailsOpen(t *testing.T) {
	store := newAccessStore(t)
	group := mustCreateGroup(t, store, "基础", "chat/fast")
	if errSet := store.SetKeyModels("a", []string{group.ID}, nil); errSet != nil {
		t.Fatalf("SetKeyModels error = %v", errSet)
	}

	if !store.AuthorizeModel("", "chat/slow", "chat/slow").Allowed {
		t.Fatal("an unattributable request was refused")
	}
	if !store.AuthorizeModel("never-seen", "chat/slow", "chat/slow").Allowed {
		t.Fatal("a key with no record was refused")
	}

	// A selection that resolves to nothing — an empty group here — grants every
	// model rather than none.
	empty := mustCreateGroup(t, store, "空分组")
	if errSet := store.SetKeyModels("a", []string{empty.ID}, nil); errSet != nil {
		t.Fatalf("SetKeyModels error = %v", errSet)
	}
	if !store.AuthorizeModel("a", "chat/slow", "chat/slow").Allowed {
		t.Fatal("a key holding only an empty group was refused")
	}

	// A group removed out of band leaves a binding pointing at nothing. The key
	// falls back to every model rather than to none.
	if errSet := store.SetKeyModels("a", []string{group.ID}, nil); errSet != nil {
		t.Fatalf("SetKeyModels error = %v", errSet)
	}
	store.ReplaceAll(func(state *State) { state.ModelGroups = nil })
	if !store.AuthorizeModel("a", "chat/slow", "chat/slow").Allowed {
		t.Fatal("a key whose only group vanished was refused")
	}
}

func TestDeleteModelGroupReleasesKeys(t *testing.T) {
	store := newAccessStore(t)
	group := mustCreateGroup(t, store, "基础", "chat/fast")
	other := mustCreateGroup(t, store, "其他", "chat/slow")
	if errSet := store.SetKeyModels("a", []string{group.ID, other.ID}, nil); errSet != nil {
		t.Fatalf("SetKeyModels error = %v", errSet)
	}

	released, errDelete := store.DeleteModelGroup(group.ID)
	if errDelete != nil || released != 1 {
		t.Fatalf("DeleteModelGroup = %d, %v, want 1 released key", released, errDelete)
	}
	store.Read(func(state *State) {
		if got := state.Keys["a"].ModelGroupIDs; !slices.Equal(got, []string{other.ID}) {
			t.Fatalf("binding = %v, want only %q", got, other.ID)
		}
	})
	if _, errMissing := store.DeleteModelGroup(group.ID); KindOf(errMissing) != KindNotFound {
		t.Fatalf("deleting twice = %v, want a not-found error", errMissing)
	}

	// The last group leaving returns the key to every model.
	if _, errDelete := store.DeleteModelGroup(other.ID); errDelete != nil {
		t.Fatalf("DeleteModelGroup error = %v", errDelete)
	}
	if !store.AuthorizeModel("a", "chat/slow", "chat/slow").Allowed {
		t.Fatal("a key left with no selection was refused")
	}
}

func TestModelGroupIdentifiersAndValidation(t *testing.T) {
	store := newAccessStore(t)

	first := mustCreateGroup(t, store, "Fast models", "chat/fast", " chat/fast ", "", "CHAT/FAST")
	if !slices.Equal(first.Models, []string{"chat/fast"}) {
		t.Fatalf("models = %v, want duplicates and blanks dropped", first.Models)
	}
	if second := mustCreateGroup(t, store, "Fast models"); second.ID == first.ID {
		t.Fatalf("ID = %q, want the taken one to make way", second.ID)
	}
	// The all-models group is not a record, so its identifier must stay free of
	// one that would answer to it.
	if third := mustCreateGroup(t, store, "全部模型"); third.ID == AllModelsGroupID {
		t.Fatalf("ID = %q, want the reserved identifier left alone", third.ID)
	}
	if _, errReserved := store.CreateModelGroup(ModelGroup{ID: AllModelsGroupID, Name: "冒名"}); KindOf(errReserved) != KindInvalid {
		t.Fatalf("creating %q = %v, want it refused", AllModelsGroupID, errReserved)
	}
	if _, errTaken := store.CreateModelGroup(ModelGroup{ID: first.ID, Name: "重复"}); KindOf(errTaken) != KindConflict {
		t.Fatalf("reusing an ID = %v, want a conflict", errTaken)
	}
}

func TestUpdateModelGroupReplacesMembership(t *testing.T) {
	store := newAccessStore(t)
	group := mustCreateGroup(t, store, "基础", "chat/fast")
	if errSet := store.SetKeyModels("a", []string{group.ID}, nil); errSet != nil {
		t.Fatalf("SetKeyModels error = %v", errSet)
	}

	name := "改名"
	models := []string{"chat/slow"}
	updated, errUpdate := store.UpdateModelGroup(ModelGroupPatch{ID: group.ID, Name: &name, Models: &models})
	if errUpdate != nil {
		t.Fatalf("UpdateModelGroup error = %v", errUpdate)
	}
	if updated.Name != name || !slices.Equal(updated.Models, models) {
		t.Fatalf("group = %+v, want the patch applied", updated)
	}
	if store.AuthorizeModel("a", "chat/fast", "chat/fast").Allowed {
		t.Fatal("a model dropped from the group is still allowed")
	}
	if !store.AuthorizeModel("a", "chat/slow", "chat/slow").Allowed {
		t.Fatal("a model added to the group is not allowed")
	}

	if _, errMissing := store.UpdateModelGroup(ModelGroupPatch{ID: "nope"}); KindOf(errMissing) != KindNotFound {
		t.Fatalf("patching a missing group = %v, want a not-found error", errMissing)
	}
}

func TestSetKeyModelsRejectsUnknownGroupAndKey(t *testing.T) {
	store := newAccessStore(t)

	if errGroup := store.SetKeyModels("a", []string{"nope"}, nil); KindOf(errGroup) != KindNotFound {
		t.Fatalf("unknown group = %v, want a not-found error", errGroup)
	}
	store.Read(func(state *State) {
		if len(state.Keys["a"].ModelGroupIDs) != 0 {
			t.Fatal("a rejected selection was applied anyway")
		}
	})
	if errKey := store.SetKeyModels("never-seen", nil, []string{"chat/fast"}); KindOf(errKey) != KindNotFound {
		t.Fatalf("unknown key = %v, want a not-found error", errKey)
	}
}
