package asset

import (
	"context"
	"errors"
	"fmt"

	"github.com/stellar/go-stellar-sdk/clients/horizonclient"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/contract"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ClassicSubmitter is the seam through which Token.Transfer submits classic
// (non-Soroban) transactions when the JS-SDK-equivalent "prefer Payment"
// fast path applies (both endpoints are G/M accounts AND the Token wraps a
// classic asset). Keeping it as an interface lets callers BYO submission
// transport — Horizon, an in-house relay, an in-memory test fake — without
// the asset package taking a hard dependency on clients/horizonclient.
//
// Implementations are expected to sign the transaction using the provided
// Signer (if non-nil), submit it, and return the on-chain hash. The hash
// is wrapped into a *contract.SentTransaction whose Wait short-circuits to
// success, because classic submitters confirm inclusion synchronously.
type ClassicSubmitter interface {
	SubmitClassic(ctx context.Context, tx *txnbuild.Transaction, signer contract.Signer) (xdr.Hash, error)
}

// AccountLoader resolves the live sequence number for a Stellar account.
// Token.Transfer uses it to populate the Source account when dispatching
// the classic Payment fast path. Mirrors the JS SDK's
// `AccountResponse`-shaped expectation but kept narrow so callers can wire
// in Horizon, RPC, or a custom cache without pulling in those packages.
type AccountLoader interface {
	LoadAccount(ctx context.Context, addr string) (txnbuild.Account, error)
}

// ErrInvalidToken is returned by Token constructors when their arguments fail
// pre-flight validation (missing required option, malformed contract id,
// undecodable asset). Callers can branch on it via errors.Is.
var ErrInvalidToken = errors.New("asset: invalid token configuration")

// Token is the unified, transport-agnostic handle for a Stellar token.
//
// It wraps either a classic asset (native XLM, or an issued credit asset) or
// a pure Soroban Asset Contract token (no classic counterpart). A single
// Token value exposes the same call shape regardless of which side of the
// classic-vs-SAC split it lives on; subsequent tasks (T5.3, T5.4) add the
// Transfer / Balance / Mint / Burn / Approve / Allowance methods that pick
// the right path at call time.
//
// Construct one of two ways:
//
//   - asset.New(xdr.MustNewNativeAsset(), …) — classic asset; ContractID is
//     auto-derived via the protocol-defined SAC recipe.
//   - asset.NewFromContractID("C…", …) — pure-SAC token with no classic
//     counterpart.
//
// Token is safe to share across goroutines provided its dependencies are.
type Token struct {
	// Asset is the classic descriptor when the Token wraps a classic asset
	// (native or alpha-num). For tokens constructed via NewFromContractID
	// Asset.Type is AssetTypeAssetTypeNative and IsClassic reports false.
	Asset xdr.Asset
	// ContractID is the strkey-encoded Stellar Asset Contract id this Token
	// targets. For classic assets it is derived from Asset + Network via the
	// protocol-defined hash recipe; for SAC-only tokens it is caller-supplied.
	ContractID string

	// classic records whether this Token was constructed from a classic
	// asset (and thus may dispatch the classic Payment path) or from a raw
	// SAC contract id. The split matters to T5.3 (Transfer dispatch); for
	// T5.2 the field is set but otherwise unread.
	classic bool

	// network is the network passphrase the underlying contract.Client uses
	// for signing. Required for SAC path; required for classic path because
	// the contract id is network-scoped.
	network string

	// rpc is the Stellar RPC transport. Required.
	rpc *rpcclient.Client
	// horizon is the optional Horizon client used by the classic-asset path
	// when T5.3 lands. Optional today.
	horizon *horizonclient.Client

	// client is the bound contract.Client for the SAC path. Populated lazily
	// from the rpc + network + spec at construction time.
	client *contract.Client

	// source is the default source-account strkey (G… or M…). Optional at
	// construction time — callers can supply per-call sources later. Stored
	// for forward use by T5.3 / T5.4.
	source string
	// signer is the default Signer for sign-and-send flows. Optional.
	signer contract.Signer
	// spec is the contract Spec bound to client. For classic assets it
	// defaults to the bundled SACSpec(); for SAC-only tokens it may be
	// caller-supplied (or fall back to SACSpec() since most SEP-41 tokens
	// share that surface).
	spec *contract.Spec

	// classicSubmitter is the caller-supplied classic-transaction submission
	// hook. When set on a Token wrapping a classic asset, Transfer auto-
	// routes G/M→G/M dispatches through a txnbuild.Payment op (faster /
	// cheaper than the SAC `transfer` invocation). When nil, Transfer
	// falls back to the SAC path even when both endpoints are accounts —
	// preserving the pre-T5.3.1 behavior for callers that never opt in.
	classicSubmitter ClassicSubmitter
	// accountLoader resolves sequence numbers for the classic Payment
	// fast path. Required when classicSubmitter is set.
	accountLoader AccountLoader
	// baseFee overrides the per-op base fee for classic transactions. Zero
	// falls back to txnbuild.MinBaseFee.
	classicBaseFee int64
}

// tokenConfig accumulates Option mutations before Token construction.
type tokenConfig struct {
	rpc              *rpcclient.Client
	horizon          *horizonclient.Client
	network          string
	source           string
	signer           contract.Signer
	spec             *contract.Spec
	classicSubmitter ClassicSubmitter
	accountLoader    AccountLoader
	classicBaseFee   int64
}

// Option is the functional-option type accepted by New / NewFromContractID.
// Mirrors JS-SDK's ContractClientOptions: each option mutates a single field
// on the resulting Token.
type Option func(*tokenConfig)

// WithRPC binds a Stellar RPC client to the Token. Required.
func WithRPC(rpc *rpcclient.Client) Option {
	return func(c *tokenConfig) { c.rpc = rpc }
}

// WithHorizon binds an optional Horizon client. The classic-asset transfer
// path (T5.3) uses it to resolve sequence numbers and submit Payment
// transactions; pure-SAC tokens do not require it.
func WithHorizon(h *horizonclient.Client) Option {
	return func(c *tokenConfig) { c.horizon = h }
}

// WithNetwork sets the network passphrase. Required: classic-asset contract
// ids are network-scoped (`Asset.ContractID(passphrase)`), and the underlying
// contract.Client signs against this network.
func WithNetwork(passphrase string) Option {
	return func(c *tokenConfig) { c.network = passphrase }
}

// WithSource sets the default source-account strkey (G… or M…). Optional —
// callers can also pass a per-call source to subsequent Transfer / Mint
// invocations.
func WithSource(addr string) Option {
	return func(c *tokenConfig) { c.source = addr }
}

// WithSigner sets the default Signer used by sign-and-send flows. Optional.
func WithSigner(s contract.Signer) Option {
	return func(c *tokenConfig) { c.signer = s }
}

// WithSpec overrides the contract Spec bound to the Token's underlying
// contract.Client. By default classic assets and SAC-only tokens both use
// the bundled SACSpec(); callers wrapping a non-standard SEP-41 contract
// can supply a custom spec here.
func WithSpec(s *contract.Spec) Option {
	return func(c *tokenConfig) { c.spec = s }
}

// WithClassicSubmitter installs the classic-Payment submission hook. When
// present on a Token wrapping a classic asset, Transfer dispatches G/M→G/M
// payments through a txnbuild.Payment op + this submitter instead of the
// SAC `transfer` invocation. Without this option (or without an
// AccountLoader) Transfer continues to route the SAC path for every
// dispatch, matching the T5.3 baseline.
func WithClassicSubmitter(s ClassicSubmitter) Option {
	return func(c *tokenConfig) { c.classicSubmitter = s }
}

// WithAccountLoader installs the AccountLoader the classic Payment fast
// path uses to fetch sequence numbers for the source account. Required
// alongside WithClassicSubmitter; when either is missing the classic path
// is disabled and Transfer falls back to the SAC `transfer` invocation.
func WithAccountLoader(a AccountLoader) Option {
	return func(c *tokenConfig) { c.accountLoader = a }
}

// WithClassicBaseFee overrides the per-op base fee used when building
// classic-Payment transactions. Zero or negative values fall back to
// txnbuild.MinBaseFee. Has no effect on the SAC path.
func WithClassicBaseFee(fee int64) Option {
	return func(c *tokenConfig) { c.classicBaseFee = fee }
}

// New constructs a Token from a classic asset, auto-deriving its Stellar
// Asset Contract id from the asset + network. The returned Token can
// dispatch both classic Payment and SAC transfer paths once T5.3 lands.
//
// Required options: WithRPC, WithNetwork. WithHorizon is recommended for
// the classic Payment path but not enforced here.
func New(a xdr.Asset, opts ...Option) (*Token, error) {
	cfg := newConfig(opts)
	if err := cfg.validateCommon("New"); err != nil {
		return nil, err
	}

	contractID, err := deriveSACContractID(a, cfg.network)
	if err != nil {
		return nil, fmt.Errorf("%w: derive SAC contract id: %v", ErrInvalidToken, err)
	}

	tok := &Token{
		Asset:            a,
		ContractID:       contractID,
		classic:          true,
		network:          cfg.network,
		rpc:              cfg.rpc,
		horizon:          cfg.horizon,
		source:           cfg.source,
		signer:           cfg.signer,
		spec:             cfg.specOrDefault(),
		classicSubmitter: cfg.classicSubmitter,
		accountLoader:    cfg.accountLoader,
		classicBaseFee:   cfg.classicBaseFee,
	}
	tok.client = tok.buildClient()
	return tok, nil
}

// NewFromContractID constructs a Token wrapping a pure Soroban Asset
// Contract — one that has no classic counterpart. The contract id must be
// a valid C-strkey.
//
// Required options: WithRPC, WithNetwork. The bundled SACSpec() is used by
// default; pass WithSpec to override for non-standard SEP-41 contracts.
func NewFromContractID(contractID string, opts ...Option) (*Token, error) {
	cfg := newConfig(opts)
	if err := cfg.validateCommon("NewFromContractID"); err != nil {
		return nil, err
	}
	if _, err := strkey.Decode(strkey.VersionByteContract, contractID); err != nil {
		return nil, fmt.Errorf("%w: contract id %q: %v", ErrInvalidToken, contractID, err)
	}

	tok := &Token{
		ContractID:       contractID,
		classic:          false,
		network:          cfg.network,
		rpc:              cfg.rpc,
		horizon:          cfg.horizon,
		source:           cfg.source,
		signer:           cfg.signer,
		spec:             cfg.specOrDefault(),
		classicSubmitter: cfg.classicSubmitter,
		accountLoader:    cfg.accountLoader,
		classicBaseFee:   cfg.classicBaseFee,
	}
	tok.client = tok.buildClient()
	return tok, nil
}

// IsClassic reports whether the Token was constructed from a classic asset
// (in which case Transfer may dispatch the classic Payment path) or from a
// raw SAC contract id (SAC path only).
func (t *Token) IsClassic() bool {
	if t == nil {
		return false
	}
	return t.classic
}

// Client returns the underlying contract.Client. T5.3 / T5.4 dispatch
// SAC-side invocations through it; advanced callers may also reach for it
// directly to issue arbitrary SAC calls not exposed on Token.
func (t *Token) Client() *contract.Client {
	if t == nil {
		return nil
	}
	return t.client
}

// Network returns the network passphrase the Token was constructed with.
func (t *Token) Network() string {
	if t == nil {
		return ""
	}
	return t.network
}

// Source returns the default source-account strkey (may be empty).
func (t *Token) Source() string {
	if t == nil {
		return ""
	}
	return t.source
}

// Signer returns the default Signer (may be nil).
func (t *Token) Signer() contract.Signer {
	if t == nil {
		return nil
	}
	return t.signer
}

// Horizon returns the bound *horizonclient.Client (may be nil).
func (t *Token) Horizon() *horizonclient.Client {
	if t == nil {
		return nil
	}
	return t.horizon
}

// newConfig folds opts into a fresh tokenConfig.
func newConfig(opts []Option) *tokenConfig {
	cfg := &tokenConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	return cfg
}

// validateCommon enforces the cross-constructor required-option set.
func (c *tokenConfig) validateCommon(ctor string) error {
	if c.rpc == nil {
		return fmt.Errorf("%w: %s: WithRPC is required", ErrInvalidToken, ctor)
	}
	if c.network == "" {
		return fmt.Errorf("%w: %s: WithNetwork is required", ErrInvalidToken, ctor)
	}
	return nil
}

// specOrDefault returns the caller-supplied spec or the bundled SAC spec.
func (c *tokenConfig) specOrDefault() *contract.Spec {
	if c.spec != nil {
		return c.spec
	}
	return SACSpec()
}

// buildClient constructs the contract.Client backing this Token's SAC path.
func (t *Token) buildClient() *contract.Client {
	opts := []contract.ClientOption{contract.WithSpec(t.spec)}
	if t.source != "" {
		opts = append(opts, contract.WithSource(t.source))
	}
	if t.signer != nil {
		opts = append(opts, contract.WithSigner(t.signer))
	}
	return contract.New(t.ContractID, t.rpc, t.network, opts...)
}

// deriveSACContractID computes the Stellar Asset Contract id strkey for the
// given classic asset and network. Wraps xdr.Asset.ContractID + strkey
// encoding — the canonical recipe used elsewhere in the SDK.
func deriveSACContractID(a xdr.Asset, passphrase string) (string, error) {
	raw, err := a.ContractID(passphrase)
	if err != nil {
		return "", err
	}
	return strkey.Encode(strkey.VersionByteContract, raw[:])
}
