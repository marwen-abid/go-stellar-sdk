package contract

import (
	"context"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
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
// Mirrors JS-SDK's contract.Client. When a Spec is bound, Invoke rejects
// unknown method names up front with "did you mean" suggestions (T4.3).
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

	// unexported defaults populated by ClientOption.
	spec *Spec
	// sourceAddr is the strkey-encoded G/M address bound via WithSource.
	// When non-empty, Invoke resolves the live txnbuild.Account at call time
	// via rpc.LoadAccount(ctx, sourceAddr) so the transaction picks up the
	// current sequence number. Mirrors JS-SDK Server.getAccount(publicKey).
	sourceAddr string
	// sourceAcct is the pre-populated txnbuild.Account bound via
	// WithSourceAccount. Used as-is (no fresh fetch) when set; takes
	// precedence over sourceAddr so callers managing their own sequence
	// number aren't surprised by a live re-fetch.
	sourceAcct txnbuild.Account
	sourceErr  error
	signer     Signer
	baseFee    int64
	timeout    time.Duration
	pollOpts   rpcclient.PollTransactionOptions
	hasPoll    bool
}

// clientConfig accumulates ClientOption mutations before construction.
type clientConfig struct {
	spec       *Spec
	sourceAddr string
	sourceAcct txnbuild.Account
	sourceErr  error
	signer     Signer
	baseFee    int64
	timeout    time.Duration
	pollOpts   rpcclient.PollTransactionOptions
	hasPoll    bool
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

// WithSource sets the default source account from a strkey-encoded account
// id ("G..." ed25519 or "M..." muxed). The client fetches the live
// txnbuild.Account (carrying the current sequence number) at Invoke time via
// rpc.LoadAccount, so callers don't need to manage sequences themselves.
//
// Mirrors JS-SDK's ContractClientOptions.publicKey + Server.getAccount.
// Invalid strkeys defer rejection to New, which surfaces them through the
// first Invoke call. Use WithSourceAccount when you already have a managed
// txnbuild.Account and want to skip the fresh fetch.
func WithSource(addr string) ClientOption {
	return func(c *clientConfig) {
		if _, err := strkey.Decode(strkey.VersionByteAccountID, addr); err != nil {
			if _, mErr := strkey.Decode(strkey.VersionByteMuxedAccount, addr); mErr != nil {
				c.sourceErr = invalidArgsf("WithSource: %q is not a valid G/M strkey: %v", addr, err)
				return
			}
		}
		c.sourceAddr = addr
		c.sourceErr = nil
	}
}

// WithSourceAccount sets the default source account directly from a
// txnbuild.Account. Use this when you need to manage the sequence number
// yourself (e.g. by fetching it from Horizon / RPC before each submission)
// and want to skip the live-fetch behavior of WithSource. The strkey-only
// WithSource is the preferred JS-parity shape; this escape hatch is for
// callers who already have an Account in hand.
func WithSourceAccount(a txnbuild.Account) ClientOption {
	return func(c *clientConfig) {
		c.sourceAcct = a
		c.sourceErr = nil
	}
}

// WithSigner sets the default Signer the client uses for sign-and-send
// flows (InvokeAndConfirm) when the caller does not provide a per-call
// override. The Signer is not consulted for pure-read calls.
func WithSigner(s Signer) ClientOption {
	return func(c *clientConfig) { c.signer = s }
}

// WithTimeout sets a default context timeout applied to lifecycle methods
// that build a context. Currently informational: callers continue to pass
// the context themselves to Invoke / InvokeAndConfirm. Stored so higher-
// level wrappers can read it via Client.Timeout().
func WithTimeout(d time.Duration) ClientOption {
	return func(c *clientConfig) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithPollOptions sets the default rpcclient.PollTransactionOptions the
// client carries. The contract package's SentTransaction.Wait uses its own
// PollOption set internally; the rpcclient options stored here are exposed
// via Client.PollOptions() so callers wiring a custom poll loop (or a
// future rpcclient.PollTransaction integration) can read them.
func WithPollOptions(o rpcclient.PollTransactionOptions) ClientOption {
	return func(c *clientConfig) {
		c.pollOpts = o
		c.hasPoll = true
	}
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

	c := &Client{
		ContractID: contractID,
		RPC:        rpc,
		Network:    network,
		spec:       spec,
		sourceAddr: cfg.sourceAddr,
		sourceAcct: cfg.sourceAcct,
		signer:     cfg.signer,
		baseFee:    baseFee,
		timeout:    cfg.timeout,
		pollOpts:   cfg.pollOpts,
		hasPoll:    cfg.hasPoll,
	}
	// Capture deferred validation (e.g. WithSource strkey decode failure) so
	// Invoke can surface it without panicking. We store it as a closure-bound
	// field via a sentinel source account; cleaner alternative would be a
	// dedicated err field, but New's no-error signature is part of T4.1's
	// committed contract.
	c.sourceErr = cfg.sourceErr
	return c
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

// Signer returns the default Signer bound via WithSigner, or nil if none
// was set. InvokeAndConfirm falls back to it when its signer argument is
// nil.
func (c *Client) Signer() Signer {
	if c == nil {
		return nil
	}
	return c.signer
}

// Timeout returns the default lifecycle timeout set via WithTimeout, or
// zero if none was set.
func (c *Client) Timeout() time.Duration {
	if c == nil {
		return 0
	}
	return c.timeout
}

// PollOptions returns the rpcclient.PollTransactionOptions bound via
// WithPollOptions and a flag indicating whether the option was supplied.
// When the flag is false the value should be treated as unset (not as
// "default zero").
func (c *Client) PollOptions() (rpcclient.PollTransactionOptions, bool) {
	if c == nil {
		return rpcclient.PollTransactionOptions{}, false
	}
	return c.pollOpts, c.hasPoll
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
// network call. Unknown names return an *Error with KindInvalidArgs whose
// message includes "did you mean: …" suggestions from the spec when a near
// match exists (T4.3). Signature-level validation (arity, type compatibility)
// stays the responsibility of Spec.FuncArgsToScVals.
//
// Invoke requires a source account (WithSource) and base fee (defaulted to
// MinBaseFee). Per-call InvokeOption values override client defaults for
// fee, memo, timebounds, source, signer, resource-fee multiplier, and the
// auto-restore / skip-simulate behaviors.
func (c *Client) Invoke(
	ctx context.Context,
	method string,
	args any,
	opts ...InvokeOption,
) (*AssembledTransaction, error) {
	if c == nil || c.RPC == nil {
		return nil, invalidArgsf("Invoke: client not initialized")
	}
	if method == "" {
		return nil, invalidArgsf("Invoke: method is required")
	}
	if c.sourceErr != nil {
		return nil, c.sourceErr
	}
	if c.spec != nil && !c.spec.HasFunc(method) {
		return nil, unknownMethodError(c.spec, method)
	}

	icfg := invokeConfig{
		baseFee:               c.baseFee,
		resourceFeeMultiplier: 0, // 0 = let AssembleParams pick DefaultResourceFeeMultiplier
		sourceAddr:            c.sourceAddr,
		sourceAcct:            c.sourceAcct,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&icfg)
		}
	}
	if icfg.sourceErr != nil {
		return nil, icfg.sourceErr
	}
	source, err := c.resolveSource(ctx, &icfg)
	if err != nil {
		return nil, err
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
		SourceAccount: source.GetAccountID(),
		Auth:          icfg.additionalAuth,
	}

	preconditions := txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()}
	if icfg.hasTimeBounds {
		preconditions.TimeBounds = txnbuild.NewTimebounds(icfg.tbMin, icfg.tbMax)
	}

	at, err := NewAssembledTransaction(AssembleParams{
		RPC:                   c.RPC,
		NetworkPassphrase:     c.Network,
		BaseFee:               icfg.baseFee,
		SourceAccount:         source,
		Op:                    op,
		Spec:                  c.spec,
		Memo:                  icfg.memo,
		Preconditions:         preconditions,
		ResourceFeeMultiplier: icfg.resourceFeeMultiplier,
	})
	if err != nil {
		return nil, err
	}
	if icfg.signer != nil {
		at.signer = icfg.signer
	}
	at.maxFee = icfg.maxFee
	at.restoreEnabled = icfg.restoreSet && icfg.restoreEnabled || !icfg.restoreSet // default true

	if icfg.skipSimulate {
		return at, nil
	}
	if err := at.Simulate(ctx); err != nil {
		return nil, err
	}
	return at, nil
}

// resolveSource picks the txnbuild.Account Invoke will hand to
// AssembleParams: a pre-populated sourceAcct (WithSourceAccount) wins; else
// the strkey sourceAddr (WithSource / Source) drives a fresh
// rpc.LoadAccount fetch so the transaction sees the current sequence number.
// Returns an InvalidArgs error when neither is set.
//
// rpc.LoadAccount returns the on-chain sequence N; the next transaction
// signed by this account must carry N+1 to satisfy the protocol. The
// AssembleParams contract requires the source to already carry the sequence
// the tx will use (buildTx passes IncrementSequenceNum: false on both the
// initial build and the post-simulate rebuild), so we bump here exactly
// once. Callers that supply their own account via WithSourceAccount manage
// their own sequence and are not affected.
func (c *Client) resolveSource(ctx context.Context, icfg *invokeConfig) (txnbuild.Account, error) {
	if icfg.sourceAcct != nil {
		return icfg.sourceAcct, nil
	}
	if icfg.sourceAddr != "" {
		acct, err := c.RPC.LoadAccount(ctx, icfg.sourceAddr)
		if err != nil {
			return nil, &Error{Kind: KindSimulationFailed, Details: "Invoke: load source account", cause: err}
		}
		if _, err := acct.IncrementSequenceNumber(); err != nil {
			return nil, &Error{Kind: KindSimulationFailed, Details: "Invoke: increment source sequence", cause: err}
		}
		return acct, nil
	}
	return nil, invalidArgsf("Invoke: no source account; pass WithSource or Source")
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
// Mirrors JS-SDK's MethodOptions: each option overrides the client's
// default for a single invocation. Per-call options always win over
// ClientOption defaults.
type InvokeOption func(*invokeConfig)

// invokeConfig is the receiver for InvokeOption mutations.
type invokeConfig struct {
	baseFee               int64
	maxFee                int64
	resourceFeeMultiplier float64
	memo                  txnbuild.Memo
	tbMin                 int64
	tbMax                 int64
	hasTimeBounds         bool
	// sourceAddr / sourceAcct mirror the Client fields. A per-call Source
	// option always sets sourceAddr (and clears the inherited sourceAcct) so
	// it routes through the live LoadAccount path; callers needing a managed
	// sequence should construct a txnbuild.Account and use AssembleParams.
	sourceAddr     string
	sourceAcct     txnbuild.Account
	sourceErr      error
	signer         Signer
	additionalAuth []xdr.SorobanAuthorizationEntry
	restoreEnabled bool
	restoreSet     bool
	skipSimulate   bool
}

// MaxFee sets the maximum total fee (inclusion + resource) the caller is
// willing to pay in stroops. Stored on the resulting AssembledTransaction;
// Send rejects envelopes whose final fee exceeds this cap. Zero (the
// default) disables the cap.
func MaxFee(stroops int64) InvokeOption {
	return func(c *invokeConfig) { c.maxFee = stroops }
}

// ResourceFeeMultiplier overrides the per-call multiplier applied to the
// simulated resource fee. Defaults to DefaultResourceFeeMultiplier (1.15)
// when omitted. Values <= 0 are ignored.
func ResourceFeeMultiplier(f float64) InvokeOption {
	return func(c *invokeConfig) {
		if f > 0 {
			c.resourceFeeMultiplier = f
		}
	}
}

// Memo attaches a txnbuild.Memo to the transaction Invoke builds.
func Memo(m txnbuild.Memo) InvokeOption {
	return func(c *invokeConfig) { c.memo = m }
}

// TimeBounds sets the transaction's time bounds. Pass the zero value of
// time.Time for min or max to leave that bound open. The default is the
// txnbuild infinite timeout.
func TimeBounds(min, max time.Time) InvokeOption {
	return func(c *invokeConfig) {
		c.hasTimeBounds = true
		if min.IsZero() {
			c.tbMin = 0
		} else {
			c.tbMin = min.Unix()
		}
		if max.IsZero() {
			c.tbMax = 0
		} else {
			c.tbMax = max.Unix()
		}
	}
}

// Source overrides the client's default source account from a strkey for a
// single invocation. The live txnbuild.Account is fetched at Invoke time via
// rpc.LoadAccount(ctx, addr); callers needing a managed sequence should
// construct a txnbuild.Account themselves and use AssembleParams directly.
func Source(addr string) InvokeOption {
	return func(c *invokeConfig) {
		if _, err := strkey.Decode(strkey.VersionByteAccountID, addr); err != nil {
			if _, mErr := strkey.Decode(strkey.VersionByteMuxedAccount, addr); mErr != nil {
				c.sourceErr = invalidArgsf("Source: %q is not a valid G/M strkey: %v", addr, err)
				return
			}
		}
		c.sourceAddr = addr
		c.sourceAcct = nil // per-call override always routes through live fetch
		c.sourceErr = nil
	}
}

// WithInvokeSigner overrides the client's default signer for a single
// invocation. Spec-name parity: the design's "Signer(s Signer)" InvokeOption
// collides with the existing Signer interface name in this package; this
// constructor preserves the per-call semantic without shadowing the type.
func WithInvokeSigner(s Signer) InvokeOption {
	return func(c *invokeConfig) { c.signer = s }
}

// AdditionalAuth attaches extra SorobanAuthorizationEntry values to the
// host function before simulation. Useful when the caller has pre-signed
// authorizations from out-of-band signers.
func AdditionalAuth(es ...xdr.SorobanAuthorizationEntry) InvokeOption {
	return func(c *invokeConfig) {
		c.additionalAuth = append(c.additionalAuth, es...)
	}
}

// Restore toggles the automatic-restore behavior. The default is true:
// when simulation surfaces an archived footprint and the AT has a Signer
// configured, Simulate transparently builds, signs, submits, and waits for
// a RestoreFootprint transaction, then re-simulates the original
// invocation (capped at one retry). Setting enable=false (or omitting a
// Signer) surfaces ErrRestoreRequired so the caller can drive restore.
func Restore(enable bool) InvokeOption {
	return func(c *invokeConfig) {
		c.restoreSet = true
		c.restoreEnabled = enable
	}
}

// SkipSimulate causes Invoke to return the AssembledTransaction without
// running Simulate. Intended for re-hydration paths (FromXDR / FromJSON)
// where simulation output is already attached.
func SkipSimulate() InvokeOption {
	return func(c *invokeConfig) { c.skipSimulate = true }
}

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
