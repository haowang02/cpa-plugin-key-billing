package billing

import (
	"maps"
	"strings"
	"time"
)

type ConfigCredential struct {
	ConfigCredentialDetails
	Balance *CredentialBalance `json:"balance"`
	Note    string             `json:"note"`
}

type ConfigCredentialDetails struct {
	// ConfigRef identifies the configured entry even when its runtime ref changes.
	ConfigRef  string `json:"config_ref"`
	Provider   string `json:"provider"`
	KeyPreview string `json:"key_preview"`
	Disabled   bool   `json:"disabled"`
	AuthIndex  string `json:"auth_index"`
	Type       string `json:"type"`
	Name       string `json:"name"`
	BaseURL    string `json:"base_url"`
	Priority   int    `json:"priority"`
	Weight     int64  `json:"weight"`
}

type CredentialBalance struct {
	FetchedAt time.Time         `json:"fetched_at"`
	Balances  []CurrencyBalance `json:"balances"`
}

type CurrencyBalance struct {
	Currency string        `json:"currency"`
	Amount   float64       `json:"amount"`
	Depleted bool          `json:"depleted"`
	Parts    []BalancePart `json:"parts"`
}

type BalancePart struct {
	Label   string    `json:"label"`
	Amounts []float64 `json:"amounts"`
}

func (s *Store) ConfigCredentials() map[string]ConfigCredential {
	var credentials map[string]ConfigCredential
	s.read(func(state *State) { credentials = maps.Clone(state.ConfigCredentials) })
	return credentials
}

// SyncConfigCredentials replaces the configured entries. A note belongs to its
// entry, and a balance to the upstream account behind the host credential.
func (s *Store) SyncConfigCredentials(details map[string]ConfigCredentialDetails) error {
	_, err := editConfiguration(s, func(state *State) (struct{}, Changes, error) {
		notes := make(map[string]string)
		balances := make(map[string]*CredentialBalance)
		for _, credential := range state.ConfigCredentials {
			notes[credential.ConfigRef] = credential.Note
			if credential.AuthIndex != "" && credential.Balance != nil {
				balances[credential.AuthIndex] = credential.Balance
			}
		}
		next := make(map[string]ConfigCredential, len(details))
		for ref, item := range details {
			next[ref] = ConfigCredential{ConfigCredentialDetails: item, Balance: balances[item.AuthIndex], Note: notes[item.ConfigRef]}
		}
		if maps.Equal(state.ConfigCredentials, next) {
			return struct{}{}, Changes{}, nil
		}
		state.ConfigCredentials = next
		return struct{}{}, Changes{ConfigCredentials: true}, nil
	})
	return err
}

func (s *Store) SetCredentialBalance(authIndex string, balance *CredentialBalance) error {
	_, err := editConfiguration(s, func(state *State) (struct{}, Changes, error) {
		next := maps.Clone(state.ConfigCredentials)
		found := false
		for ref, credential := range next {
			if authIndex != "" && credential.AuthIndex == authIndex {
				credential.Balance = balance
				next[ref] = credential
				found = true
			}
		}
		if !found {
			return struct{}{}, Changes{}, &Error{Kind: KindNotFound, Msg: "Configured credential does not exist"}
		}
		state.ConfigCredentials = next
		return struct{}{}, Changes{ConfigCredentials: true}, nil
	})
	return err
}

func (s *Store) SetCredentialNote(ref, note string) error {
	note = strings.TrimSpace(note)
	_, err := editConfiguration(s, func(state *State) (struct{}, Changes, error) {
		credential, ok := state.ConfigCredentials[ref]
		if !ok {
			return struct{}{}, Changes{}, notFoundf("Configured credential does not exist")
		}
		if credential.Note == note {
			return struct{}{}, Changes{}, nil
		}
		credential.Note = note
		state.ConfigCredentials = maps.Clone(state.ConfigCredentials)
		state.ConfigCredentials[ref] = credential
		return struct{}{}, Changes{ConfigCredentials: true}, nil
	})
	return err
}
