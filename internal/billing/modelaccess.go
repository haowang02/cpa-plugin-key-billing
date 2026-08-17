package billing

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// AllModelsGroupID names the group every key starts in. It is not stored: a
// group holding a copy of every model would go stale on every model list
// synchronization, so "every model" is what an empty selection means instead,
// which is also what makes a key nobody has configured unrestricted rather than
// locked out. The identifier only travels — the panel offers it as a checkbox,
// and SetKeyModels normalizes a selection carrying it back to the empty one,
// which is the whole of the exclusivity rule.
const AllModelsGroupID = "all"

// modelSampleSize bounds how many models a rejection names, so that an error
// body does not bury the one thing the client needs to read.
const modelSampleSize = 5

func (s *Store) ModelGroups() []ModelGroup {
	groups := []ModelGroup{}
	s.Read(func(state *State) {
		for _, group := range state.ModelGroups {
			groups = append(groups, group.clone())
		}
	})
	return groups
}

func (g ModelGroup) clone() ModelGroup {
	return ModelGroup{ID: g.ID, Name: g.Name, Models: slices.Clone(g.Models)}
}

func (s *State) findModelGroupIndex(id string) int {
	id = strings.TrimSpace(id)
	if id == "" {
		return -1
	}
	return slices.IndexFunc(s.ModelGroups, func(group ModelGroup) bool { return group.ID == id })
}

func (s *State) FindModelGroup(id string) (ModelGroup, bool) {
	index := s.findModelGroupIndex(id)
	if index < 0 {
		return ModelGroup{}, false
	}
	return s.ModelGroups[index], true
}

// AllowedModels lists what one key may call, as the union of its groups and its
// individually selected models.
//
// restricted is false for a key granted every model. A selection that resolves
// to nothing — only empty groups, or only groups deleted out of band — is such a
// key too: reading it as a denial would lock a key out of the proxy over a
// record nobody meant that way.
func (s *State) AllowedModels(key *KeyState) ([]string, bool) {
	if key == nil || (len(key.ModelGroupIDs) == 0 && len(key.Models) == 0) {
		return nil, false
	}
	models := slices.Clone(key.Models)
	for _, id := range key.ModelGroupIDs {
		if group, exists := s.FindModelGroup(id); exists {
			models = append(models, group.Models...)
		}
	}
	models = dedupe(models)
	if len(models) == 0 {
		return nil, false
	}
	sort.Strings(models)
	return models, true
}

// ModelDecision is why a request was turned away. Model is the identity the
// decision was made on, which is the string the billing log would have recorded
// for the request; Models is what the key may call instead.
type ModelDecision struct {
	Allowed bool
	Model   string
	Models  []string
}

// AuthorizeModel decides whether one key may call the model a request names. It
// reads and never writes: a request turned away here must not open a
// subscription period it was never admitted into.
//
// Like quota enforcement it fails open. A key nobody has configured, a model the
// plugin cannot name, and a group deleted out of band all resolve to "allowed":
// a billing plugin that starts rejecting traffic over its own missing state is
// worse than one that briefly grants too much.
func (s *Store) AuthorizeModel(scope, upstreamModel, routeModel string) ModelDecision {
	allowed := ModelDecision{Allowed: true}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return allowed
	}
	decision := allowed
	s.Read(func(state *State) {
		key := state.Keys[scope]
		if key == nil {
			return
		}
		models, restricted := state.AllowedModels(key)
		if !restricted {
			return
		}
		// The identity is resolved exactly as billing resolves it, and nothing
		// else is tested against the grant. That single reading is what makes
		// the panel's list mean what it says:
		//
		//	a thinking suffix the client asked for is trimmed, so granting a
		//	model covers every reasoning effort of it
		//	a suffix that is part of a configured model ID is not, so such a
		//	model is granted separately, exactly as it is priced separately
		//	an alias is judged by the model the client named, so a route
		//	resolving onto a granted model cannot carry a request the key was
		//	never granted
		billingModel := state.ResolveBillingModel(upstreamModel, routeModel)
		if strings.TrimSpace(billingModel) == "" {
			return
		}
		for _, model := range models {
			if strings.EqualFold(model, billingModel) {
				return
			}
		}
		decision = ModelDecision{Model: billingModel, Models: models}
	})
	return decision
}

// Describe names what the key may call instead, for the client and the plugin
// log alike.
func (d ModelDecision) Describe() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "该 API Key 可用 %d 个模型：", len(d.Models))
	builder.WriteString(strings.Join(d.Models[:min(len(d.Models), modelSampleSize)], "、"))
	if len(d.Models) > modelSampleSize {
		builder.WriteString(" 等")
	}
	return builder.String()
}

func (g ModelGroup) Validate() error {
	if strings.TrimSpace(g.ID) == "" {
		return invalidf("模型分组 ID 不能为空")
	}
	if g.ID == AllModelsGroupID {
		return invalidf("模型分组 ID %q 由「全部模型」保留", AllModelsGroupID)
	}
	if strings.TrimSpace(g.Name) == "" {
		return invalidf("模型分组名称不能为空")
	}
	return nil
}

func (s *Store) CreateModelGroup(group ModelGroup) (ModelGroup, error) {
	group.ID = strings.TrimSpace(group.ID)
	group.Name = strings.TrimSpace(group.Name)
	group.Models = dedupe(group.Models)

	var errApply error
	stored := updateResult(s, func(state *State) (ModelGroup, Changes) {
		if group.ID == "" {
			group.ID = state.freeModelGroupID(group.Name)
		}
		if errValidate := group.Validate(); errValidate != nil {
			errApply = errValidate
			return ModelGroup{}, Changes{}
		}
		if _, exists := state.FindModelGroup(group.ID); exists {
			errApply = conflictf("模型分组 %q 已存在", group.ID)
			return ModelGroup{}, Changes{}
		}
		state.ModelGroups = append(state.ModelGroups, group)
		return group, Changes{ModelGroups: true}
	})
	return stored, errApply
}

type ModelGroupPatch struct {
	ID     string    `json:"id"`
	Name   *string   `json:"name,omitempty"`
	Models *[]string `json:"models,omitempty"`
}

func (s *Store) UpdateModelGroup(patch ModelGroupPatch) (ModelGroup, error) {
	patch.ID = strings.TrimSpace(patch.ID)
	if patch.ID == "" {
		return ModelGroup{}, invalidf("模型分组 ID 不能为空")
	}

	var errApply error
	stored := updateResult(s, func(state *State) (ModelGroup, Changes) {
		index := state.findModelGroupIndex(patch.ID)
		if index < 0 {
			errApply = notFoundf("模型分组 %q 不存在", patch.ID)
			return ModelGroup{}, Changes{}
		}
		updated := state.ModelGroups[index].clone()
		if patch.Name != nil {
			updated.Name = strings.TrimSpace(*patch.Name)
		}
		if patch.Models != nil {
			updated.Models = dedupe(*patch.Models)
		}
		if errValidate := updated.Validate(); errValidate != nil {
			errApply = errValidate
			return ModelGroup{}, Changes{}
		}
		state.ModelGroups[index] = updated
		// Every key bound to this group may now reach a different set of models,
		// so a rejection already reported is no longer the state of affairs.
		s.denied.forget(state.groupMembers(updated.ID)...)
		return updated, Changes{ModelGroups: true}
	})
	return stored, errApply
}

// DeleteModelGroup drops a group and releases the keys holding it, reporting how
// many. A key left with nothing selected returns to every model, which is the
// same grant it had before anything was configured.
func (s *Store) DeleteModelGroup(id string) (int, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return 0, invalidf("模型分组 ID 不能为空")
	}

	var errApply error
	released := updateResult(s, func(state *State) (int, Changes) {
		index := state.findModelGroupIndex(id)
		if index < 0 {
			errApply = notFoundf("模型分组 %q 不存在", id)
			return 0, Changes{}
		}
		state.ModelGroups = slices.Delete(state.ModelGroups, index, index+1)

		scopes := state.groupMembers(id)
		for _, scope := range scopes {
			key := state.Keys[scope]
			key.ModelGroupIDs = slices.DeleteFunc(key.ModelGroupIDs, func(bound string) bool { return bound == id })
		}
		s.denied.forget(scopes...)
		return len(scopes), Changes{ModelGroups: true, Keys: scopes}
	})
	return released, errApply
}

func (s *State) groupMembers(id string) []string {
	var scopes []string
	for scope, key := range s.Keys {
		if key != nil && slices.Contains(key.ModelGroupIDs, id) {
			scopes = append(scopes, scope)
		}
	}
	sort.Strings(scopes)
	return scopes
}

// SetKeyModels replaces what one key may call. Selecting the all-models group
// clears everything else, which is how that group stays exclusive no matter
// which client sends the request.
func (s *Store) SetKeyModels(scope string, groups, models []string) error {
	scope = normalizeScope(scope)
	if scope == "" {
		return invalidf("API Key 标识不能为空")
	}
	groups = dedupe(groups)
	models = dedupe(models)
	if slices.Contains(groups, AllModelsGroupID) {
		groups, models = nil, nil
	}

	var errApply error
	updateResult(s, func(state *State) (struct{}, Changes) {
		key := state.liveKey(scope)
		if key == nil {
			errApply = notFoundf("API Key %q 不存在，请先同步 Key 列表", scope)
			return struct{}{}, Changes{}
		}
		for _, id := range groups {
			if _, exists := state.FindModelGroup(id); !exists {
				errApply = notFoundf("模型分组 %q 不存在", id)
				return struct{}{}, Changes{}
			}
		}
		key.ModelGroupIDs = groups
		key.Models = models
		s.denied.forget(scope)
		return struct{}{}, Changes{Keys: []string{scope}}
	})
	return errApply
}

// The reserved identifier counts as taken, so a group named "全部模型" cannot
// take the name of the group that is not a record.
func (s *State) freeModelGroupID(name string) string {
	return freeID(name, "group", func(id string) bool {
		_, exists := s.FindModelGroup(id)
		return exists || id == AllModelsGroupID
	})
}
