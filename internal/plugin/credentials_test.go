package plugin

import (
	"strings"
	"testing"

	"cpa-key-billing/internal/billing"
)

func TestAIProviderCredentialDisplayNameMasksKeysAndAccounts(t *testing.T) {
	name := safeCredentialName("codex-sk-secret1234-private@example.com.json", "private@example.com", "codex", billing.CredentialFingerprint("id"))
	for _, secret := range []string{"sk-secret1234", "private@example.com"} {
		if strings.Contains(name, secret) {
			t.Fatalf("safe name %q leaked %q", name, secret)
		}
	}
}

func TestAuthFileCredentialDisplayNameFollowsCPAMC(t *testing.T) {
	const fileName = "codex-1dfbc38b-wrong@example.com-pro.json"
	for _, test := range []struct {
		file hostAuthFile
		want string
	}{
		{hostAuthFile{Name: fileName, Label: "private label", Email: "user@example.com", ProjectID: "project-1"}, "user@example.com"},
		{hostAuthFile{Name: fileName, ProjectID: "project-1"}, "project-1"},
		{hostAuthFile{Name: fileName, Account: "sk-upstream-secret-value"}, "codex-1dfbc38b-wrong@example.com-pro"},
	} {
		if got := credentialDisplayName(test.file, billing.CredentialSourceAuthFiles, "codex", billing.CredentialFingerprint("id")); got != test.want {
			t.Fatalf("name = %q, want %q", got, test.want)
		}
	}
}
