package linear

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Setup is the operator-written setup file (paths.LinearSetupFile): the
// Linear OAuth application installed as an app actor, its webhook signing
// secret, the tokens it was granted, and the ATC Project every session
// runs in. The server rewrites only the token fields, when they renew.
// Keys are snake_case like config.toml; unknown keys refuse the file so a
// typo cannot silently disable a check.
type Setup struct {
	// OrganizationID is the Linear workspace the app is installed in;
	// deliveries from any other workspace are refused.
	OrganizationID string `json:"organization_id"`
	// ClientID and ClientSecret identify the OAuth application; the
	// client id is also the app identity a delivery must carry.
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	// WebhookSigningSecret signs every delivery.
	WebhookSigningSecret string `json:"webhook_signing_secret"`
	// AccessToken calls the API as the app; it expires, and RefreshToken
	// renews it unattended. AccessTokenExpiresAt is maintained by the
	// server once it has refreshed; zero means unknown (used until
	// refused).
	AccessToken          string    `json:"access_token"`
	RefreshToken         string    `json:"refresh_token,omitempty"`
	AccessTokenExpiresAt time.Time `json:"access_token_expires_at,omitzero"`
	// ProjectID is the ATC Project every session runs in.
	ProjectID string `json:"project_id"`
}

// ErrNotConfigured reports a missing setup file.
var ErrNotConfigured = errors.New("the Linear Integration is not configured")

// loadSetup reads and validates the setup file.
func loadSetup(path string) (Setup, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Setup{}, fmt.Errorf("%w: no setup file at %s", ErrNotConfigured, path)
	}
	if err != nil {
		return Setup{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var setup Setup
	if err := decoder.Decode(&setup); err != nil {
		return Setup{}, fmt.Errorf("%s: %w", path, err)
	}
	var missing []string
	for key, value := range map[string]string{
		"organization_id":        setup.OrganizationID,
		"client_id":              setup.ClientID,
		"client_secret":          setup.ClientSecret,
		"webhook_signing_secret": setup.WebhookSigningSecret,
		"access_token":           setup.AccessToken,
		"project_id":             setup.ProjectID,
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		// Sorted, so the message is stable.
		slices.Sort(missing)
		return Setup{}, fmt.Errorf("%s is missing %s", path, strings.Join(missing, ", "))
	}
	return setup, nil
}

// saveTokens rewrites the setup file with renewed tokens, keeping every
// other field as the file has it now (the operator may have edited it
// since it was loaded). Atomic and 0600, like the credential it is.
func saveTokens(path string, accessToken, refreshToken string, expiresAt time.Time) error {
	current, err := loadSetup(path)
	if err != nil {
		return err
	}
	current.AccessToken = accessToken
	if refreshToken != "" {
		current.RefreshToken = refreshToken
	}
	current.AccessTokenExpiresAt = expiresAt.UTC()
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".linear-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	// Durable before it replaces the file: a rename of unsynced contents
	// can leave an empty setup file after a power loss, and with it the
	// refresh token.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
