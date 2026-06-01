package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"time"
)

// KeygenAPIURL and KeygenAccountID are set at build time via:
//
//	go build -ldflags "-X 'github.com/tabslate/server/billing/local.KeygenAPIURL=...'
//	                    -X 'github.com/tabslate/server/billing/local.KeygenAccountID=...'"
//
// They cannot be overridden at runtime to prevent pointing the binary at a fake
// keygen.sh instance.
var (
	KeygenAPIURL    = "https://api.keygen.sh"
	KeygenAccountID = ""
)

// ErrMachineLimitExceeded is returned by ActivateMachine when the license is
// already activated on another machine (HTTP 422 from keygen.sh).
var ErrMachineLimitExceeded = errors.New("license already activated on another machine")

type keygenClient struct {
	baseURL   string
	accountID string
	licKey    string
	http      *http.Client
}

func newKeygenClient(baseURL, accountID, licKey string) *keygenClient {
	if baseURL == "" {
		baseURL = KeygenAPIURL
	}
	return &keygenClient{
		baseURL:   baseURL,
		accountID: accountID,
		licKey:    licKey,
		http:      &http.Client{Timeout: 10 * time.Second},
	}
}

// keygenLicense holds parsed license data from keygen.sh.
type keygenLicense struct {
	MaxUsers int
	Status   string
	Expiry   *time.Time
}

type keygenLicenseResp struct {
	Data struct {
		Attributes struct {
			Status   string                 `json:"status"`
			Expiry   *string                `json:"expiry"`
			Metadata map[string]interface{} `json:"metadata"`
		} `json:"attributes"`
	} `json:"data"`
}

// FetchLicense calls GET /v1/accounts/{id}/licenses/{key} and returns parsed data.
func (c *keygenClient) FetchLicense(ctx context.Context) (*keygenLicense, error) {
	url := fmt.Sprintf(
		"%s/v1/accounts/%s/licenses/%s",
		c.baseURL,
		neturl.PathEscape(c.accountID),
		neturl.PathEscape(c.licKey),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("keygen FetchLicense: %w", err)
	}
	req.Header.Set("Authorization", "License "+c.licKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keygen FetchLicense: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("keygen FetchLicense: status %d: %s", resp.StatusCode, body)
	}

	var result keygenLicenseResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("keygen FetchLicense: decode: %w", err)
	}

	lic := &keygenLicense{Status: result.Data.Attributes.Status}
	if result.Data.Attributes.Expiry != nil {
		t, err := time.Parse(time.RFC3339, *result.Data.Attributes.Expiry)
		if err == nil {
			lic.Expiry = &t
		}
	}
	if v, ok := result.Data.Attributes.Metadata["max_users"]; ok {
		switch n := v.(type) {
		case float64:
			lic.MaxUsers = int(n)
		case int:
			lic.MaxUsers = n
		}
	}

	return lic, nil
}

type keygenMachineReq struct {
	Data struct {
		Type       string `json:"type"`
		Attributes struct {
			Fingerprint string `json:"fingerprint"`
			Name        string `json:"name"`
		} `json:"attributes"`
		Relationships struct {
			License struct {
				Data struct {
					Type string `json:"type"`
					ID   string `json:"id"`
				} `json:"data"`
			} `json:"license"`
		} `json:"relationships"`
	} `json:"data"`
}

// ActivateMachine registers this machine fingerprint against the license.
// Returns nil on 201 (created) and 409 (already activated for this fingerprint).
// Returns ErrMachineLimitExceeded on 422 (another machine already holds the license).
func (c *keygenClient) ActivateMachine(ctx context.Context, fingerprint, hostname string) error {
	var payload keygenMachineReq
	payload.Data.Type = "machines"
	payload.Data.Attributes.Fingerprint = fingerprint
	payload.Data.Attributes.Name = hostname
	payload.Data.Relationships.License.Data.Type = "licenses"
	payload.Data.Relationships.License.Data.ID = c.licKey

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("keygen ActivateMachine: marshal: %w", err)
	}

	url := fmt.Sprintf("%s/v1/accounts/%s/machines", c.baseURL, neturl.PathEscape(c.accountID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("keygen ActivateMachine: %w", err)
	}
	req.Header.Set("Authorization", "License "+c.licKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keygen ActivateMachine: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated, http.StatusConflict:
		return nil
	case http.StatusUnprocessableEntity:
		return ErrMachineLimitExceeded
	default:
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("keygen ActivateMachine: status %d: %s", resp.StatusCode, errBody)
	}
}

type keygenMachineListResp struct {
	Data []json.RawMessage `json:"data"`
}

// ValidateMachine returns true if this fingerprint is still activated on the license.
func (c *keygenClient) ValidateMachine(ctx context.Context, fingerprint string) (bool, error) {
	url := fmt.Sprintf(
		"%s/v1/accounts/%s/machines?fingerprint=%s",
		c.baseURL,
		neturl.PathEscape(c.accountID),
		neturl.QueryEscape(fingerprint),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("keygen ValidateMachine: %w", err)
	}
	req.Header.Set("Authorization", "License "+c.licKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("keygen ValidateMachine: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, fmt.Errorf("keygen ValidateMachine: status %d: %s", resp.StatusCode, errBody)
	}

	var result keygenMachineListResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("keygen ValidateMachine: decode: %w", err)
	}

	return len(result.Data) > 0, nil
}
