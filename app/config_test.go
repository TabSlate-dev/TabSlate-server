package app

import (
	"testing"
)

func TestEnvStringSlice_NotSet(t *testing.T) {
	// Use a key guaranteed to be absent from the environment.
	result := envStringSlice("TEST_PROXIES_UNSET_XYZ", []string{"default"})
	if len(result) != 1 || result[0] != "default" {
		t.Fatalf("expected [default], got %v", result)
	}
}

func TestEnvStringSlice_EmptyString(t *testing.T) {
	t.Setenv("TEST_PROXIES_EMPTY", "")
	result := envStringSlice("TEST_PROXIES_EMPTY", []string{"default"})
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestEnvStringSlice_Values(t *testing.T) {
	t.Setenv("TEST_PROXIES_VALS", "172.16.0.0/12, 10.0.0.0/8 , 192.168.0.0/16")
	result := envStringSlice("TEST_PROXIES_VALS", nil)
	want := []string{"172.16.0.0/12", "10.0.0.0/8", "192.168.0.0/16"}
	if len(result) != len(want) {
		t.Fatalf("expected %v, got %v", want, result)
	}
	for i, v := range want {
		if result[i] != v {
			t.Fatalf("index %d: expected %q, got %q", i, v, result[i])
		}
	}
}
