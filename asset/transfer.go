package asset

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/stellar/go-stellar-sdk/contract"
	"github.com/stellar/go-stellar-sdk/strkey"
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
// the dispatch boundary independently of the (currently SAC-only)
// implementation of Transfer itself.
func (t *Token) shouldUseClassicPath(from, to string) bool {
	if t == nil || !t.classic {
		return false
	}
	return classifyTransferAddress(from) == transferAddrAccount &&
		classifyTransferAddress(to) == transferAddrAccount
}

// Transfer moves `amount` of the Token's underlying asset from `from` to
// `to`. The dispatch boundary follows design §4.8:
//
//   - Both `from` and `to` are G… or M… accounts AND the Token wraps a
//     classic asset → the JS SDK prefers a classic txnbuild.Payment for the
//     lower fee / no Soroban resource cost. The Go SDK reaches feature
//     parity here in a follow-up; see the note below.
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
// NOTE on the classic Payment fast path: the JS SDK exposes a unified return
// type (AssembledTransaction) for both classic and SAC dispatch. Go's
// AssembledTransaction is currently Soroban-only (it owns an
// *InvokeHostFunction op and a Simulate step). Extending it to wrap classic
// Payment txns is a larger refactor than T5.3's "dispatch logic" budget; for
// now Transfer routes the G→G + classic case through the SAC transfer
// invocation as well, preserving call semantics at the cost of a slightly
// higher fee. A P1 follow-up (`5.3.1`) tracks the fast-path enablement.
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
