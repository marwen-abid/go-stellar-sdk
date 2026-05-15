package networktest

import (
	"context"
	"testing"
)

func TestNew_Defaults(t *testing.T) {
	sb := New()
	if sb == nil {
		t.Fatal("New returned nil")
	}
	if sb.RPCURL != defaultRPCURL {
		t.Errorf("RPCURL = %q, want %q", sb.RPCURL, defaultRPCURL)
	}
	if sb.Network != defaultNetworkPassphrase {
		t.Errorf("Network = %q, want %q", sb.Network, defaultNetworkPassphrase)
	}
	if sb.RPC != nil {
		t.Errorf("RPC should be nil before Start, got %v", sb.RPC)
	}
	if sb.Funded != nil {
		t.Errorf("Funded should be nil before T7.2 funding, got %v", sb.Funded)
	}
}

func TestNew_OptionsOverride(t *testing.T) {
	const (
		url        = "https://soroban-testnet.stellar.org"
		passphrase = "Test SDF Network ; September 2015"
	)
	sb := New(WithRPCURL(url), WithNetworkPassphrase(passphrase))
	if sb.RPCURL != url {
		t.Errorf("RPCURL = %q, want %q", sb.RPCURL, url)
	}
	if sb.Network != passphrase {
		t.Errorf("Network = %q, want %q", sb.Network, passphrase)
	}
}

func TestStart_EmptyRPCURL(t *testing.T) {
	sb := New(WithRPCURL(""))
	if err := sb.Start(context.Background()); err == nil {
		t.Fatal("Start with empty RPCURL: want error, got nil")
	}
}

func TestStart_EmptyNetwork(t *testing.T) {
	sb := New(WithNetworkPassphrase(""))
	if err := sb.Start(context.Background()); err == nil {
		t.Fatal("Start with empty Network: want error, got nil")
	}
}

func TestClose_NoOp(t *testing.T) {
	sb := New()
	if err := sb.Close(); err != nil {
		t.Fatalf("Close on fresh Sandbox: %v", err)
	}
	if sb.RPC != nil {
		t.Errorf("Close should clear RPC, got %v", sb.RPC)
	}
}

// TestRequire_SkipsWhenEnvUnset verifies the gating: with the env var unset,
// Require must call t.Skip so default `go test ./...` stays hermetic. We
// observe the skip via a child subtest's Skipped() report.
func TestRequire_SkipsWhenEnvUnset(t *testing.T) {
	t.Setenv(EnvVar, "")

	var returned *Sandbox
	ok := t.Run("inner", func(t *testing.T) {
		returned = Require(t)
		// If Require returned, the gating did not skip — fail the inner test
		// so the parent can flag it. (Require calls runtime.Goexit via
		// t.Skip on the unset path, so reaching this line is the bug.)
		t.Errorf("Require returned without skipping; got %+v", returned)
	})
	// t.Run reports true when the subtest passed OR was skipped. The
	// distinguishing signal is `returned`: it must remain nil if Require
	// skipped before assigning.
	if !ok {
		t.Fatal("inner subtest failed — Require did not skip")
	}
	if returned != nil {
		t.Errorf("Require should have skipped before returning; got %+v", returned)
	}
}

// TestRequire_ReturnsSandboxWhenEnvSet verifies the happy path: with the env
// var set, Require returns a configured Sandbox without starting it.
func TestRequire_ReturnsSandboxWhenEnvSet(t *testing.T) {
	t.Setenv(EnvVar, "local")
	sb := Require(t, WithRPCURL("http://example.invalid"))
	if sb == nil {
		t.Fatal("Require returned nil with env set")
	}
	if sb.RPCURL != "http://example.invalid" {
		t.Errorf("RPCURL = %q, want option override", sb.RPCURL)
	}
	if sb.RPC != nil {
		t.Error("Require should not Start the Sandbox")
	}
}
