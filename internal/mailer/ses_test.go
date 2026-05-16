package mailer

import (
	"strings"
	"testing"
	"time"
)

func TestSignSES_OutputFormat(t *testing.T) {
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	body := []byte(`{"test":"value"}`)

	xAmzDate, authorization := signSES("AKID", "SECRET", "us-east-1", body, now)

	if xAmzDate != "20260516T120000Z" {
		t.Errorf("xAmzDate = %q, want %q", xAmzDate, "20260516T120000Z")
	}

	wantPrefix := "AWS4-HMAC-SHA256 Credential=AKID/20260516/us-east-1/ses/aws4_request,"
	if !strings.HasPrefix(authorization, wantPrefix) {
		t.Errorf("authorization = %q\nwant prefix: %q", authorization, wantPrefix)
	}

	if !strings.Contains(authorization, "SignedHeaders=content-type;host;x-amz-date,") {
		t.Errorf("authorization missing SignedHeaders: %q", authorization)
	}

	const sigPrefix = "Signature="
	idx := strings.Index(authorization, sigPrefix)
	if idx == -1 {
		t.Fatalf("authorization missing Signature= field: %q", authorization)
	}
	sig := authorization[idx+len(sigPrefix):]
	// Pinned value — guards against accidental changes to the HMAC key derivation
	// order or string-to-sign construction. Computed from the fixed test inputs above.
	const wantSig = "74672992aaca538d2adeb6289ea656ba984439fb1ffaf967f9883d70c35f0ab1"
	if sig != wantSig {
		t.Errorf("signature = %q, want %q", sig, wantSig)
	}
}
