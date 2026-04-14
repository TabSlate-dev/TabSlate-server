// Package captcha provides Prosopo procaptcha server-side verification.
package captcha

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Verifier verifies Prosopo procaptcha-response tokens.
// If Secret is empty, verification is disabled (all tokens pass).
type Verifier struct {
	// Secret is the Prosopo site secret for server-side verification.
	Secret string

	// ServerURL is the Prosopo verification endpoint.
	// Defaults to "https://api.prosopo.io/siteverify" but can be overridden
	// for self-hosted Prosopo deployments.
	ServerURL string

	client *http.Client
}

// New creates a Verifier. If secret is empty, the verifier becomes a no-op
// (Verify always returns nil), which is useful for local development.
func New(secret, serverURL string) *Verifier {
	if serverURL == "" {
		serverURL = "https://api.prosopo.io/siteverify"
	}
	return &Verifier{
		Secret:    secret,
		ServerURL: serverURL,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Enabled reports whether captcha verification is active.
func (v *Verifier) Enabled() bool {
	return v.Secret != ""
}

type verifyRequest struct {
	Secret string `json:"secret"`
	Token  string `json:"token"`
}

type verifyResponse struct {
	// Prosopo API returns { "verified": true/false }.
	Verified bool `json:"verified"`
}

// Verify validates a procaptcha-response token against the Prosopo API.
// Returns nil if verification passes or is disabled (no secret configured).
// Returns a descriptive error if the token is invalid or the API call fails.
func (v *Verifier) Verify(ctx context.Context, token string) error {
	if !v.Enabled() {
		return nil
	}

	if token == "" {
		return fmt.Errorf("captcha token is required")
	}

	body, err := json.Marshal(verifyRequest{
		Secret: v.Secret,
		Token:  token,
	})
	if err != nil {
		return fmt.Errorf("captcha marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.ServerURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("captcha request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("captcha verify: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("captcha verify: unexpected status %d", resp.StatusCode)
	}

	var result verifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("captcha decode: %w", err)
	}

	if !result.Verified {
		return fmt.Errorf("captcha verification failed")
	}

	return nil
}
