package contract

import (
	"context"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// Client is the user-facing facade for invoking Soroban contract functions.
// It bundles the contract ID, RPC transport, network passphrase, optional
// *Spec, and per-client defaults (source account, base fee). The zero value is
// not usable; construct one via New or From.
//
// Client is safe to share across goroutines provided its dependencies are
// (the *rpcclient.Client is; *Spec is read-only after construction). The
// transactions Client.Invoke builds are not — each Invoke returns a fresh
// *AssembledTransaction the caller owns.
//
// Mirrors JS-SDK's contract.Client. Method-name validation is intentionally
// minimal at this layer (T4.1); T4.3 will tighten it.
type Client struct {
	// ContractID is the strkey contract identifier (`C...`) this client
	// targets.
	ContractID string
	// RPC is the Stellar RPC transport. It must satisfy the same minimal
	// surface the AssembledTransaction lifecycle needs (Simulate / Send /
	// Get); the concrete *rpcclient.Client implements every method.
	RPC rpcSimulator
	// Network is the network passphrase the transactions Client builds will
	// be signed against.
	Network string

	// unexported defaults. T4.4 will widen this set.
	spec    *Spec
	source  txnbuild.Account
	baseFee int64
}

// clientConfig accumulates ClientOption mutations before construction.
type clientConfig struct {
	spec    *Spec
	source  txnbuild.Account
	baseFee int64
}

// ClientOption is the functional-option type accepted by New / From. T4.1
// ships the minimum set required to build a transaction; T4.4 will add
// WithSigner, WithTimeout, WithPollOptions, and the per-call InvokeOption
// counterparts.
type ClientOption func(*clientConfig)

// WithSpec attaches a pre-parsed contract Spec to the client. When omitted,
// New falls back to LookupSpec(contractID); From fetches the spec from the
// network. An explicit WithSpec always wins.
func WithSpec(s *Spec) ClientOption {
	return func(c *clientConfig) { c.spec = s }
}

// WithSource sets the default source account for transactions this client
// builds. The account must already carry the sequence number the next
// transaction will use; Invoke does not increment it. Per-call source
// overrides arrive in T4.4.
func WithSource(a txnbuild.Account) ClientOption {
	return func(c *clientConfig) { c.source = a }
}

// WithBaseFee sets the per-operation base fee in stroops. Values below
// txnbuild.MinBaseFee are rejected at Invoke time. Defaults to
// txnbuild.MinBaseFee when omitted.
func WithBaseFee(stroops int64) ClientOption {
	return func(c *clientConfig) { c.baseFee = stroops }
}

// New constructs a Client with the spec the caller supplies (via WithSpec) or
// the one previously registered for contractID via RegisterSpec. The Client's
// spec stays nil when neither is present — Invoke will still work for callers
// who pass pre-marshaled []xdr.ScVal arguments and a method name they know is
// valid.
//
// New does not touch the network. Use From when the spec must be discovered.
func New(contractID string, rpc rpcSimulator, network string, opts ...ClientOption) *Client {
	cfg := &clientConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	spec := cfg.spec
	if spec == nil {
		spec = LookupSpec(contractID)
	}

	baseFee := cfg.baseFee
	if baseFee == 0 {
		baseFee = txnbuild.MinBaseFee
	}

	return &Client{
		ContractID: contractID,
		RPC:        rpc,
		Network:    network,
		spec:       spec,
		source:     cfg.source,
		baseFee:    baseFee,
	}
}

// Spec returns the contract spec bound to this client, or nil if none was
// supplied / discovered. Advanced callers can use it to marshal arguments or
// decode return values directly.
func (c *Client) Spec() *Spec {
	if c == nil {
		return nil
	}
	return c.spec
}

// Invoke builds an AssembledTransaction wrapping a call to method on the
// client's contract, runs Simulate against the bound RPC, and returns the
// transaction ready for sign-and-send.
//
// args accepts:
//   - []xdr.ScVal — used positionally as-is.
//   - map[string]any — marshaled through the client's Spec via
//     FuncArgsToScVals; requires Spec() != nil.
//   - nil — treated as the empty argument list.
//
// When the client has a *Spec, Invoke validates that method exists before any
// network call. Tighter validation (signature arity, type compatibility)
// lands in T4.3.
//
// Invoke requires a source account (WithSource) and base fee (defaulted to
// MinBaseFee). InvokeOption is reserved for T4.4; passing options here is a
// no-op today.
func (c *Client) Invoke(
	ctx context.Context,
	method string,
	args any,
	_ ...InvokeOption,
) (*AssembledTransaction, error) {
	if c == nil || c.RPC == nil {
		return nil, invalidArgsf("Invoke: client not initialized")
	}
	if method == "" {
		return nil, invalidArgsf("Invoke: method is required")
	}
	if c.source == nil {
		return nil, invalidArgsf("Invoke: no source account; pass WithSource")
	}
	if c.spec != nil && !c.spec.HasFunc(method) {
		return nil, invalidArgsf("Invoke: function %q not found in spec", method)
	}

	scArgs, err := c.marshalArgs(method, args)
	if err != nil {
		return nil, err
	}

	addr, err := xdr.ScAddressFromStrkey(c.ContractID)
	if err != nil {
		return nil, invalidArgsf("Invoke: contract id %q: %v", c.ContractID, err)
	}

	op := &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &xdr.InvokeContractArgs{
				ContractAddress: addr,
				FunctionName:    xdr.ScSymbol(method),
				Args:            scArgs,
			},
		},
		SourceAccount: c.source.GetAccountID(),
	}

	at, err := NewAssembledTransaction(AssembleParams{
		RPC:               c.RPC,
		NetworkPassphrase: c.Network,
		BaseFee:           c.baseFee,
		SourceAccount:     c.source,
		Op:                op,
		Spec:              c.spec,
		// txnbuild rejects a zero-value Preconditions; default to an
		// infinite timeout. Per-call timebounds belong to T4.4.
		Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
	})
	if err != nil {
		return nil, err
	}
	if err := at.Simulate(ctx); err != nil {
		return nil, err
	}
	return at, nil
}

// marshalArgs converts the args parameter accepted by Invoke into the
// positional []xdr.ScVal the host function expects.
func (c *Client) marshalArgs(method string, args any) ([]xdr.ScVal, error) {
	switch v := args.(type) {
	case nil:
		return nil, nil
	case []xdr.ScVal:
		return v, nil
	case map[string]any:
		if c.spec == nil {
			return nil, invalidArgsf("Invoke: map[string]any args require a Spec; pass WithSpec or register one")
		}
		return c.spec.FuncArgsToScVals(method, v)
	default:
		return nil, invalidArgsf("Invoke: unsupported args type %T; want []xdr.ScVal, map[string]any, or nil", args)
	}
}

// InvokeOption is the per-call option type for Invoke / InvokeAndConfirm.
// T4.1 reserves the type; the full set (MaxFee, ResourceFeeMultiplier, Memo,
// TimeBounds, Source, Signer, AdditionalAuth, Restore, SkipSimulate) lands in
// T4.4.
type InvokeOption func(*invokeConfig)

// invokeConfig is the receiver for InvokeOption mutations. T4.1 keeps it
// empty; T4.4 widens it.
type invokeConfig struct{}

// From fetches the contract's spec from the network and returns a Client
// bound to it. Equivalent to JS-SDK's Client.from().
//
// Resolution order:
//  1. If WithSpec is set, use it directly (no network call).
//  2. If a spec is already registered for contractID, use it.
//  3. Read the contract instance ledger entry, follow its wasm hash to the
//     contract code entry, and parse the embedded spec section.
//
// The discovered spec is registered into the process-global spec registry as
// a side effect so subsequent New / From calls for the same contract are
// cheap.
//
// Pass any other options (WithSource, WithBaseFee) the constructed Client
// should pick up.
func From(
	ctx context.Context,
	contractID string,
	rpc fromRPC,
	network string,
	opts ...ClientOption,
) (*Client, error) {
	if rpc == nil {
		return nil, invalidArgsf("From: rpc is required")
	}
	if contractID == "" {
		return nil, invalidArgsf("From: contractID is required")
	}

	cfg := &clientConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	spec := cfg.spec
	if spec == nil {
		spec = LookupSpec(contractID)
	}
	if spec == nil {
		fetched, err := fetchSpec(ctx, rpc, contractID)
		if err != nil {
			return nil, err
		}
		spec = fetched
		RegisterSpec(contractID, spec)
	}

	// Re-apply opts on a fresh New so the spec-discovery side effect is
	// reflected and per-client defaults stay consistent.
	allOpts := append([]ClientOption{WithSpec(spec)}, opts...)
	return New(contractID, rpc, network, allOpts...), nil
}

// fromRPC is the subset of the RPC client surface From needs: the
// rpcSimulator dependencies plus GetLedgerEntries for spec discovery.
type fromRPC interface {
	rpcSimulator
	GetLedgerEntries(ctx context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error)
}

// fetchSpec resolves contractID -> *Spec by reading the contract instance
// ledger entry, extracting the wasm hash, fetching the contract code entry,
// and parsing the embedded spec custom section.
func fetchSpec(ctx context.Context, rpc fromRPC, contractID string) (*Spec, error) {
	addr, err := xdr.ScAddressFromStrkey(contractID)
	if err != nil {
		return nil, invalidArgsf("From: contract id %q: %v", contractID, err)
	}

	var instanceKey xdr.LedgerKey
	if err := instanceKey.SetContractData(
		addr,
		xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
		xdr.ContractDataDurabilityPersistent,
	); err != nil {
		return nil, invalidArgsf("From: build instance ledger key: %v", err)
	}
	instanceKeyB64, err := xdr.MarshalBase64(instanceKey)
	if err != nil {
		return nil, invalidArgsf("From: encode instance ledger key: %v", err)
	}

	resp, err := rpc.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{
		Keys: []string{instanceKeyB64},
	})
	if err != nil {
		return nil, &Error{Kind: KindSimulationFailed, Details: "From: fetch contract instance", cause: err}
	}
	if len(resp.Entries) == 0 {
		return nil, invalidArgsf("From: contract %q has no instance ledger entry", contractID)
	}

	var instanceEntry xdr.LedgerEntryData
	if err := xdr.SafeUnmarshalBase64(resp.Entries[0].DataXDR, &instanceEntry); err != nil {
		return nil, invalidArgsf("From: decode contract instance: %v", err)
	}
	if instanceEntry.Type != xdr.LedgerEntryTypeContractData || instanceEntry.ContractData == nil {
		return nil, invalidArgsf("From: contract instance entry is not ContractData")
	}
	val := instanceEntry.ContractData.Val
	if val.Type != xdr.ScValTypeScvContractInstance || val.Instance == nil {
		return nil, invalidArgsf("From: contract instance value is not ScvContractInstance")
	}
	exec := val.Instance.Executable
	if exec.Type != xdr.ContractExecutableTypeContractExecutableWasm || exec.WasmHash == nil {
		return nil, invalidArgsf("From: contract executable is not Wasm (type %s)", exec.Type)
	}
	wasmHash := *exec.WasmHash

	var codeKey xdr.LedgerKey
	if err := codeKey.SetContractCode(wasmHash); err != nil {
		return nil, invalidArgsf("From: build contract code ledger key: %v", err)
	}
	codeKeyB64, err := xdr.MarshalBase64(codeKey)
	if err != nil {
		return nil, invalidArgsf("From: encode contract code ledger key: %v", err)
	}

	codeResp, err := rpc.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{
		Keys: []string{codeKeyB64},
	})
	if err != nil {
		return nil, &Error{Kind: KindSimulationFailed, Details: "From: fetch contract code", cause: err}
	}
	if len(codeResp.Entries) == 0 {
		return nil, invalidArgsf("From: contract code entry for %x not found", wasmHash)
	}

	var codeEntry xdr.LedgerEntryData
	if err := xdr.SafeUnmarshalBase64(codeResp.Entries[0].DataXDR, &codeEntry); err != nil {
		return nil, invalidArgsf("From: decode contract code: %v", err)
	}
	if codeEntry.Type != xdr.LedgerEntryTypeContractCode || codeEntry.ContractCode == nil {
		return nil, invalidArgsf("From: contract code entry is not ContractCode")
	}

	return NewSpecFromWasm(codeEntry.ContractCode.Code)
}
