package local

import (
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/tabslate/server/billing"
)

// License represents a parsed and verified TabSlate License JWT.
type License struct {
	Plan      billing.Plan
	Limits    billing.Limits
	Licensee  string
	ExpiresAt int64 // unix timestamp
}

// Valid reports whether the license has not yet expired.
func (l *License) Valid() bool {
	return time.Now().Unix() < l.ExpiresAt
}

// licenseClaims is the JWT payload for a TabSlate License.
type licenseClaims struct {
	Plan     billing.Plan   `json:"plan"`
	Limits   billing.Limits `json:"limits"`
	Licensee string         `json:"licensee"`
	jwt.RegisteredClaims
}

// ParseAndVerify parses a License JWT and verifies its RSA-PS256 signature.
// publicKey may be nil to skip signature verification (tests only).
func ParseAndVerify(tokenStr string, publicKeyDER []byte) (*License, error) {
	keyFunc := func(t *jwt.Token) (any, error) {
		if publicKeyDER == nil {
			// tests: accept any key
			return t.Method, nil
		}
		pub, err := parseRSAPublicKey(publicKeyDER)
		if err != nil {
			return nil, err
		}
		if _, ok := t.Method.(*jwt.SigningMethodRSAPSS); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return pub, nil
	}

	claims := &licenseClaims{}
	tok, err := jwt.ParseWithClaims(tokenStr, claims, keyFunc)
	if err != nil {
		return nil, err
	}
	if !tok.Valid {
		return nil, errors.New("invalid license token")
	}
	if claims.Subject != "license" {
		return nil, errors.New("token is not a license")
	}
	if claims.Issuer != "tabslate" {
		return nil, errors.New("unknown license issuer")
	}

	exp := claims.ExpiresAt.Unix()
	return &License{
		Plan:      claims.Plan,
		Limits:    claims.Limits,
		Licensee:  claims.Licensee,
		ExpiresAt: exp,
	}, nil
}

// parseRSAPublicKey parses PEM-encoded RSA public key bytes.
func parseRSAPublicKey(der []byte) (*rsa.PublicKey, error) {
	var raw any
	if err := json.Unmarshal(der, &raw); err == nil {
		return nil, errors.New("JSON public key format not supported; provide PEM-encoded PKIX bytes")
	}
	key, err := jwt.ParseRSAPublicKeyFromPEM(der)
	if err != nil {
		return nil, fmt.Errorf("parse RSA public key: %w", err)
	}
	return key, nil
}
