package contract

import (
	"context"
	"fmt"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// SignAndSend is the one-shot convenience that chains SignAuthEntries → Sign
// → Send on an already-simulated AssembledTransaction. It does NOT poll: the
// caller invokes Wait (or Watch) on the returned *SentTransaction to await
// on-chain inclusion. This mirrors the JS SDK's signAndSend() shape (see
// design §4.3 / §4.8 example).
//
// When the simulated invocation is a read call (IsReadCall reports true),
// SignAndSend short-circuits: no envelope is signed, no transaction is
// submitted, and (nil, nil) is returned. Callers can read the simulated
// return value via Result(); §4.8's example funnels read and write paths
// through the same SignAndSend / Wait surface, so callers may always call
// SignAndSend and then defer to Result for the value.
//
// For write calls, SignAndSend assumes signer is the invoker (source
// account) and that any non-invoker authorization entries have already been
// signed via a preceding SignAuthEntries call. SignAndSend still runs
// SignAuthEntries(ctx, signer, 0) so that auth entries credentialed by the
// invoker itself (rare but legal — e.g. when the source has both invoker
// and contract-authorization roles) get signed; entries credentialed by
// other addresses are skipped, and a subsequent ErrNeedsMoreSignatures is
// surfaced before submission. expirationLedger 0 matches the JS default for
// invoker-only flows; callers needing a real expiration should drive
// SignAuthEntries themselves before calling SignAndSend.
//
// Returns:
//   - (nil, nil) for read calls (no submission occurred).
//   - (*SentTransaction, nil) for write calls accepted by the RPC.
//   - (nil, *Error) wrapping ErrNotYetSimulated / ErrNeedsMoreSignatures /
//     ErrSubmissionFailed / ErrInvalidArgs / SignAuthEntries' or Sign's own
//     error on any failure.
//
// SignAndSend is idempotent on write calls: a second invocation returns the
// cached *SentTransaction from the first Send. It is NOT idempotent across
// read/write boundaries — if the first call short-circuited as a read, the
// AT has no cached send.
func (a *AssembledTransaction) SignAndSend(ctx context.Context, signer Signer) (*SentTransaction, error) {
	if a == nil || a.rpc == nil {
		return nil, invalidArgsf("AssembledTransaction not initialized")
	}
	if a.Simulation == nil {
		return nil, &Error{Kind: KindNotYetSimulated, Details: "SignAndSend"}
	}
	if signer == nil {
		return nil, invalidArgsf("SignAndSend: signer is nil")
	}
	if ctx == nil {
		return nil, invalidArgsf("SignAndSend: ctx is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Read calls have nothing to submit; Result() reads from a.ReturnValue.
	if a.IsReadCall() {
		return nil, nil
	}

	// Idempotent on the write path.
	if a.sent != nil {
		return a.sent, nil
	}

	// Sign invoker-credentialed auth entries (if any) before checking for
	// remaining required signatures. expirationLedger=0 is the JS default
	// for invoker-only flows; multi-party flows must drive SignAuthEntries
	// explicitly with a meaningful expiration before calling SignAndSend.
	if err := a.SignAuthEntries(ctx, signer, 0); err != nil {
		return nil, err
	}

	// After SignAuthEntries, any remaining unsigned non-invoker entries are
	// a hard error: SignAndSend cannot speak for those addresses.
	pending, err := a.NeedsNonInvokerSigningBy(false)
	if err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		return nil, &Error{
			Kind:    KindNeedsMoreSignatures,
			Details: fmt.Sprintf("SignAndSend: %d auth entry/entries still need signatures", len(pending)),
		}
	}

	if err := a.Sign(signer); err != nil {
		return nil, err
	}
	return a.Send(ctx)
}

// Result returns the native Go value of the contract invocation's return
// ScVal. The source of the ScVal depends on lifecycle stage:
//
//   - For a read call (IsReadCall == true), Result decodes the simulated
//     ReturnValue captured by Simulate.
//   - For a write call after Wait, Result decodes the ScVal extracted from
//     the cached *GetTransactionResponse's ResultMetaXDR.
//   - When neither is available (Simulate has not run, or the AT was sent
//     but Wait has not yet completed), Result returns an *Error matching
//     ErrNotYetSimulated.
//
// When the AssembledTransaction was constructed with a non-nil Spec,
// Result invokes spec.FuncResToNative(Method, scval) to convert the value
// into its declared output Go type (uint64, *big.Int, struct, etc.). When
// no Spec is bound, Result returns the raw xdr.ScVal so callers can decode
// it themselves.
//
// Result is read-only with respect to the AT's lifecycle state and may be
// called any number of times.
func (a *AssembledTransaction) Result() (any, error) {
	if a == nil {
		return nil, invalidArgsf("AssembledTransaction not initialized")
	}
	if a.Simulation == nil {
		return nil, &Error{Kind: KindNotYetSimulated, Details: "Result"}
	}

	// Prefer the post-Wait ScVal: a successful submission is the source of
	// truth and may differ from the simulation when ledger state has shifted.
	if a.sent != nil && a.sent.getResp != nil {
		scval, err := extractReturnValueFromGetResp(a.sent.getResp)
		if err != nil {
			return nil, &Error{Kind: KindSubmissionFailed, Details: "Result: decode final ScVal", cause: err}
		}
		return a.decodeReturn(scval)
	}

	// Read calls (and pre-send write calls) fall back to the simulated value.
	if a.IsReadCall() {
		if a.ReturnValue == nil {
			// Soroban convention: void-returning fns omit the ScVal entirely.
			return a.decodeReturn(xdr.ScVal{Type: xdr.ScValTypeScvVoid})
		}
		return a.decodeReturn(*a.ReturnValue)
	}

	// Write call that has been Sent but not yet Wait-ed (or Send failed).
	return nil, &Error{
		Kind:    KindNotYetSimulated,
		Details: "Result: write call requires Wait to complete before the result is decodable",
	}
}

// decodeReturn applies the bound Spec when present; otherwise returns the
// raw ScVal so callers without a spec can still extract a value.
func (a *AssembledTransaction) decodeReturn(v xdr.ScVal) (any, error) {
	if a.spec == nil {
		return v, nil
	}
	return a.spec.FuncResToNative(a.Method, v)
}

// extractReturnValueFromGetResp pulls the contract invocation's return
// ScVal out of a getTransaction response. The ScVal lives inside
// ResultMetaXDR -> TransactionMeta -> SorobanMeta -> ReturnValue. We accept
// both V3 and V4 meta versions; older versions (pre-Soroban) cannot
// contain a return value and yield ScvVoid.
func extractReturnValueFromGetResp(resp *protocol.GetTransactionResponse) (xdr.ScVal, error) {
	if resp == nil || resp.ResultMetaXDR == "" {
		return xdr.ScVal{Type: xdr.ScValTypeScvVoid}, nil
	}
	var meta xdr.TransactionMeta
	if err := xdr.SafeUnmarshalBase64(resp.ResultMetaXDR, &meta); err != nil {
		return xdr.ScVal{}, fmt.Errorf("decode ResultMetaXDR: %w", err)
	}
	switch meta.V {
	case 3:
		v3 := meta.MustV3()
		if v3.SorobanMeta == nil {
			return xdr.ScVal{Type: xdr.ScValTypeScvVoid}, nil
		}
		return v3.SorobanMeta.ReturnValue, nil
	case 4:
		v4 := meta.MustV4()
		if v4.SorobanMeta == nil || v4.SorobanMeta.ReturnValue == nil {
			return xdr.ScVal{Type: xdr.ScValTypeScvVoid}, nil
		}
		return *v4.SorobanMeta.ReturnValue, nil
	default:
		return xdr.ScVal{}, fmt.Errorf("unsupported TransactionMeta version %d", meta.V)
	}
}
