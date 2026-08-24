package providerprofile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCompleteClaudeOnboardingPreservesProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(path, []byte(`{"oauthAccount":{"accountUuid":"keep-me"},"hasCompletedOnboarding":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CompleteClaudeOnboarding(dir); err != nil {
		t.Fatal(err)
	}
	if err := CompleteClaudeOnboarding(dir); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var profile struct {
		OAuthAccount struct {
			AccountUUID string `json:"accountUuid"`
		} `json:"oauthAccount"`
		HasCompletedOnboarding bool `json:"hasCompletedOnboarding"`
	}
	if err := json.Unmarshal(contents, &profile); err != nil {
		t.Fatal(err)
	}
	if !profile.HasCompletedOnboarding || profile.OAuthAccount.AccountUUID != "keep-me" {
		t.Fatalf("profile = %#v", profile)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("profile mode = %v", info.Mode().Perm())
	}
}
