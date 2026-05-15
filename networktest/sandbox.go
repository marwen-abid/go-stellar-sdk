// Package networktest provides a Sandbox helper that wraps an externally
// managed Stellar RPC endpoint (typically a local `stellar network container
// start local` quickstart, or testnet) and exposes the connection details
// integration tests need: an *rpcclient.Client, the network passphrase, and
// a place to hang a pre-funded root keypair.
//
// T7.1 — design §4.12 foundation. Funding via friendbot (Sandbox.Fund) and
// retrofitting existing integration tests onto Sandbox are deferred to T7.2
// and T7.3 respectively.
//
// Gating: tests that need a live endpoint should call Require(t), which skips
// unless the STELLAR_NETWORK_SANDBOX env var is set. Default `go test ./...`
// runs hermetic; no network is hit.
package networktest

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/keypair"
)

// EnvVar is the environment variable that opts a test in to running against a
// live sandbox. Any non-empty value enables the sandbox; tests that call
// Require(t) skip when the variable is unset.
const EnvVar = "STELLAR_NETWORK_SANDBOX"

// Quickstart defaults — the local `stellar network container start local`
// image exposes Soroban RPC on :8000/soroban/rpc and uses the standalone
// network passphrase.
const (
	defaultRPCURL            = "http://localhost:8000/soroban/rpc"
	defaultNetworkPassphrase = "Standalone Network ; February 2017"
)

// Sandbox is a thin facade over a Stellar RPC endpoint that integration tests
// can drive. It is intentionally narrow: T7.1 just records the connection
// details and lazily builds the RPC client; T7.2 will add Fund(ctx, addr) on
// top.
type Sandbox struct {
	// RPC is populated by Start. Callers must not use it before Start returns
	// nil.
	RPC *rpcclient.Client

	// Network is the network passphrase used to sign transactions submitted
	// through this sandbox.
	Network string

	// RPCURL is the URL of the Soroban RPC endpoint.
	RPCURL string

	// Funded is the pre-funded root account. Populated by T7.2 (Sandbox.Fund);
	// reserved here so the struct shape is stable across phases.
	Funded *keypair.Full
}

// Option configures a Sandbox at construction time.
type Option func(*Sandbox)

// WithRPCURL overrides the Soroban RPC URL. Useful for pointing the sandbox at
// testnet (`https://soroban-testnet.stellar.org`) or a custom local port.
func WithRPCURL(url string) Option {
	return func(s *Sandbox) { s.RPCURL = url }
}

// WithNetworkPassphrase overrides the network passphrase. Pair with
// WithRPCURL when targeting testnet/futurenet.
func WithNetworkPassphrase(passphrase string) Option {
	return func(s *Sandbox) { s.Network = passphrase }
}

// New returns a Sandbox configured for a local quickstart container by
// default. Apply WithRPCURL / WithNetworkPassphrase to target a different
// endpoint. The returned Sandbox is not yet connected — call Start to build
// the RPC client and verify reachability.
func New(opts ...Option) *Sandbox {
	s := &Sandbox{
		RPCURL:  defaultRPCURL,
		Network: defaultNetworkPassphrase,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start builds the RPC client and verifies the endpoint is reachable by
// issuing a GetHealth call. It is safe to call multiple times; subsequent
// calls re-issue the health check against the existing client.
func (s *Sandbox) Start(ctx context.Context) error {
	if s.RPCURL == "" {
		return errors.New("networktest: Sandbox.RPCURL is empty")
	}
	if s.Network == "" {
		return errors.New("networktest: Sandbox.Network is empty")
	}
	if s.RPC == nil {
		s.RPC = rpcclient.NewClient(s.RPCURL, nil)
	}
	if _, err := s.RPC.GetHealth(ctx); err != nil {
		return err
	}
	return nil
}

// Close releases any resources held by the Sandbox. T7.1 holds no owned
// resources beyond the http.Client embedded in rpcclient (which has no Close
// method of its own), so this is a no-op today. It exists so callers can
// `defer sb.Close()` and not need to change call sites when T7.2 introduces
// owned resources (e.g. a container handle).
func (s *Sandbox) Close() error {
	s.RPC = nil
	return nil
}

// Require returns a configured but not-yet-Started Sandbox, or calls t.Skip
// if the gating env var is unset. Use it at the top of integration tests:
//
//	func TestFoo(t *testing.T) {
//	    sb := networktest.Require(t)
//	    if err := sb.Start(context.Background()); err != nil {
//	        t.Fatal(err)
//	    }
//	    defer sb.Close()
//	    // ...
//	}
func Require(t *testing.T, opts ...Option) *Sandbox {
	t.Helper()
	if os.Getenv(EnvVar) == "" {
		t.Skipf("networktest: %s not set; skipping live-sandbox test", EnvVar)
	}
	return New(opts...)
}
