package asset

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/stellar/go-stellar-sdk/amount"
	"github.com/stellar/go-stellar-sdk/contract"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ErrInvalidTransferArg is returned by Token.Transfer when its arguments fail
// pre-flight validation (malformed from/to strkey, nil amount, etc.). Callers
// can branch on it via errors.Is.
var ErrInvalidTransferArg = errors.New("asset: invalid Transfer argument")

// transferAddressKind classifies a strkey by its high-level "is this an
// account address or a contract address?" bucket. The classic Payment fast
// path is only valid when both ends are account-shaped (G… or M…).
type transferAddressKind int

const (
	transferAddrInvalid  transferAddressKind = iota
	transferAddrAccount                      // G… or M…
	transferAddrContract                     // C…
)

// classifyTransferAddress decodes a strkey and returns its high-level kind
// for transfer-dispatch purposes. Unknown / malformed strkeys map to
// transferAddrInvalid.
func classifyTransferAddress(s string) transferAddressKind {
	v, err := strkey.Version(s)
	if err != nil {
		return transferAddrInvalid
	}
	switch v {
	case strkey.VersionByteAccountID, strkey.VersionByteMuxedAccount:
		return transferAddrAccount
	case strkey.VersionByteContract:
		return transferAddrContract
	default:
		return transferAddrInvalid
	}
}

// shouldUseClassicPath reports whether the JS-SDK's "prefer classic Payment"
// fast path applies to this Transfer: both endpoints are G/M accounts AND
// the Token wraps a classic asset (native or issued credit). In every other
// case the SAC `transfer` invocation handles the call uniformly.
//
// The predicate is exported via this internal helper so T5.3's tests can pin
// the dispatch boundary independently of the submission seam — even when no
// ClassicSubmitter is configured, this still returns true for the cases the
// fast path applies to in principle.
func (t *Token) shouldUseClassicPath(from, to string) bool {
	if t == nil || !t.classic {
		return false
	}
	return classifyTransferAddress(from) == transferAddrAccount &&
		classifyTransferAddress(to) == transferAddrAccount
}

// canDispatchClassic reports whether Transfer can actually dispatch the
// classic Payment fast path right now: the dispatch boundary applies AND
// the caller wired up both the submission hook (ClassicSubmitter) and a
// way to load the source account's sequence (AccountLoader). When either
// is missing Transfer silently falls back to the SAC path so callers who
// never opt in preserve the pre-T5.3.1 behavior.
func (t *Token) canDispatchClassic(from, to string) bool {
	if !t.shouldUseClassicPath(from, to) {
		return false
	}
	return t.classicSubmitter != nil && t.accountLoader != nil
}

// Transfer moves `amount` of the Token's underlying asset from `from` to
// `to`. The dispatch boundary follows design §4.8:
//
//   - Both `from` and `to` are G… or M… accounts AND the Token wraps a
//     classic asset → the JS SDK prefers a classic txnbuild.Payment for the
//     lower fee / no Soroban resource cost. Opt in by supplying
//     WithClassicSubmitter + WithAccountLoader; see the note below.
//   - Any C-address involvement, or a pure-SAC Token → the SAC `transfer`
//     contract function is invoked via the wrapped contract.Client. This
//     branch handles native XLM, issued credit assets, and pure-SAC tokens
//     uniformly. T5.4 builds on this same plumbing for balance / mint / burn
//     / approve.
//
// The amount is encoded as the SEP-41-standard SCV_I128 (signed 128-bit), so
// it accommodates the full range the SAC contract accepts. Negative amounts
// are not rejected here — the contract itself enforces non-negative transfer
// amounts.
//
// Both endpoints must decode as valid strkeys (G/M/C). On failure Transfer
// returns an error wrapping ErrInvalidTransferArg.
//
// NOTE on the classic Payment fast path: callers opt in by supplying
// WithClassicSubmitter + WithAccountLoader. When both are present and the
// Token wraps a classic asset AND the dispatch matrix matches (G/M→G/M),
// Transfer builds a txnbuild.Payment op and routes submission through the
// configured ClassicSubmitter, returning a *contract.AssembledTransaction
// whose SignAndSend → Wait surface is uniform with the SAC path. Without
// those options Transfer falls back to the SAC `transfer` invocation, so
// classic-asset callers who never opt in preserve the T5.3 behavior.
func (t *Token) Transfer(
	ctx context.Context,
	from, to string,
	amount *big.Int,
	opts ...contract.InvokeOption,
) (*contract.AssembledTransaction, error) {
	if t == nil {
		return nil, fmt.Errorf("%w: Transfer called on nil Token", ErrInvalidTransferArg)
	}
	if t.client == nil {
		return nil, fmt.Errorf("%w: Transfer: Token has no underlying contract.Client", ErrInvalidTransferArg)
	}
	if amount == nil {
		return nil, fmt.Errorf("%w: Transfer: amount is nil", ErrInvalidTransferArg)
	}
	if k := classifyTransferAddress(from); k == transferAddrInvalid {
		return nil, fmt.Errorf("%w: Transfer: from %q is not a valid G/M/C strkey", ErrInvalidTransferArg, from)
	}
	if k := classifyTransferAddress(to); k == transferAddrInvalid {
		return nil, fmt.Errorf("%w: Transfer: to %q is not a valid G/M/C strkey", ErrInvalidTransferArg, to)
	}

	// Classic Payment fast path: both ends are accounts AND the Token
	// wraps a classic asset AND the caller wired up a submitter+loader.
	// Cheaper / faster than the SAC `transfer` invocation; falls back to
	// SAC otherwise so opt-in is graceful.
	if t.canDispatchClassic(from, to) {
		return t.transferClassic(ctx, from, to, amount)
	}

	// SCV_I128 is the SEP-41 amount type used by the SAC `transfer` function.
	// FuncArgsToScVals would marshal a *big.Int into the same shape via the
	// bound spec; we pre-build the ScVal here to avoid depending on a Spec
	// override that downstream callers may have swapped in via WithSpec.
	amountScv, err := xdr.ScvI128(amount)
	if err != nil {
		return nil, fmt.Errorf("%w: Transfer: encode amount: %v", ErrInvalidTransferArg, err)
	}
	fromScv, err := xdr.ScvAddress(from)
	if err != nil {
		return nil, fmt.Errorf("%w: Transfer: encode from: %v", ErrInvalidTransferArg, err)
	}
	toScv, err := xdr.ScvAddress(to)
	if err != nil {
		return nil, fmt.Errorf("%w: Transfer: encode to: %v", ErrInvalidTransferArg, err)
	}

	// Per-call Source: when `from` is an account-shaped strkey (G/M) it is
	// the natural transaction source for the transfer — prepend it so
	// caller-supplied opts can still override. When `from` is a C-address
	// (contract-to-… transfer) Stellar requires a separate G/M source to
	// sign the envelope; rely on the client's WithSource default or a
	// caller-supplied Source option further down opts.
	callOpts := make([]contract.InvokeOption, 0, len(opts)+1)
	if classifyTransferAddress(from) == transferAddrAccount {
		callOpts = append(callOpts, contract.Source(from))
	}
	callOpts = append(callOpts, opts...)

	return t.client.Invoke(ctx, "transfer", []xdr.ScVal{fromScv, toScv, amountScv}, callOpts...)
}

// transferClassic builds a txnbuild.Payment op + transaction wrapping the
// Token's classic asset and routes submission through the configured
// ClassicSubmitter. It is only invoked when canDispatchClassic returned
// true (so the asset is classic, both endpoints are G/M, and both the
// submitter and account loader are wired up).
//
// The returned *contract.AssembledTransaction is shaped so callers can
// drive the same SignAndSend → Wait surface as the SAC path: the classic
// submitter runs inside SignAndSend, Wait short-circuits to success.
func (t *Token) transferClassic(ctx context.Context, from, to string, amt *big.Int) (*contract.AssembledTransaction, error) {
	// Classic amounts are stored as int64 stroops; SAC uses i128. Build the
	// payment string via amount.IntStringToAmount so a value of 100 stroops
	// renders as "0.0000100" — the format txnbuild.Payment expects.
	if amt.Sign() < 0 {
		return nil, fmt.Errorf("%w: Transfer: classic path requires non-negative amount", ErrInvalidTransferArg)
	}
	amtStr, err := amount.IntStringToAmount(amt.String())
	if err != nil {
		return nil, fmt.Errorf("%w: Transfer: encode classic amount: %v", ErrInvalidTransferArg, err)
	}

	asset, err := txnbuildAssetFromXDR(t.Asset)
	if err != nil {
		return nil, fmt.Errorf("%w: Transfer: translate classic asset: %v", ErrInvalidTransferArg, err)
	}

	src, err := t.accountLoader.LoadAccount(ctx, from)
	if err != nil {
		return nil, fmt.Errorf("asset: Transfer: load source account %q: %w", from, err)
	}

	baseFee := t.classicBaseFee
	if baseFee <= 0 {
		baseFee = txnbuild.MinBaseFee
	}

	op := &txnbuild.Payment{
		Destination: to,
		Amount:      amtStr,
		Asset:       asset,
	}

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        src,
		IncrementSequenceNum: true,
		Operations:           []txnbuild.Operation{op},
		BaseFee:              baseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
	})
	if err != nil {
		return nil, fmt.Errorf("asset: Transfer: build classic transaction: %w", err)
	}

	submitter := t.classicSubmitter
	submit := func(ctx context.Context, built *txnbuild.Transaction, signer contract.Signer) (xdr.Hash, error) {
		return submitter.SubmitClassic(ctx, built, signer)
	}

	return contract.NewClassicAssembledTransaction(contract.ClassicAssembleParams{
		Built:             tx,
		NetworkPassphrase: t.network,
		Signer:            t.signer,
		Submit:            submit,
	})
}

// txnbuildAssetFromXDR converts a classic xdr.Asset into the txnbuild.Asset
// type required by txnbuild.Payment. Mirrors the unexported assetFromXDR
// helper in txnbuild but is callable from outside that package.
func txnbuildAssetFromXDR(a xdr.Asset) (txnbuild.Asset, error) {
	switch a.Type {
	case xdr.AssetTypeAssetTypeNative:
		return txnbuild.NativeAsset{}, nil
	case xdr.AssetTypeAssetTypeCreditAlphanum4:
		code := bytesTrimNul(a.AlphaNum4.AssetCode[:])
		return txnbuild.CreditAsset{Code: code, Issuer: a.AlphaNum4.Issuer.Address()}, nil
	case xdr.AssetTypeAssetTypeCreditAlphanum12:
		code := bytesTrimNul(a.AlphaNum12.AssetCode[:])
		return txnbuild.CreditAsset{Code: code, Issuer: a.AlphaNum12.Issuer.Address()}, nil
	default:
		return nil, fmt.Errorf("unsupported asset type %v", a.Type)
	}
}

// bytesTrimNul trims trailing NUL padding from a fixed-width asset code.
func bytesTrimNul(b []byte) string {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] != 0 {
			return string(b[:i+1])
		}
	}
	return ""
}
