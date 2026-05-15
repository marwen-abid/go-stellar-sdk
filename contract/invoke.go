package contract

import (
	"context"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// InvokeAndConfirm is the "I just want it done" path that compresses a
// full Soroban call into one expression: build → simulate → (sign → send →
// poll). It is the Cosmos `BroadcastTx` / CosmJS `sendTokens("auto")` shape
// referenced by design §4.2 and is the entry point most user code paths
// should land on.
//
// Behavior:
//
//   - For read calls (IsReadCall true post-simulate), no envelope is signed
//     and no transaction is submitted. The returned hash is the zero value,
//     and result is decoded from the simulation's return value (via the
//     client's Spec when present, raw xdr.ScVal otherwise).
//   - For write calls, InvokeAndConfirm chains SignAndSend → Wait → Result,
//     returning the decoded final ScVal and the submitted transaction hash.
//
// Arguments mirror Client.Invoke: args may be `[]xdr.ScVal`, `map[string]any`
// (when the client has a Spec), or nil. signer must be non-nil for write
// calls; for read calls the signer is unused but is still required so callers
// don't accidentally drop it when refactoring between read/write paths.
//
// InvokeOption is accepted for forward-compat with T4.4; T4.2 ignores its
// contents. All errors flow through the package's existing *Error sentinels
// (ErrInvalidArgs / ErrSimulationFailed / ErrSubmissionFailed /
// ErrTransactionFailed / ErrNotYetSimulated / ErrTimeout) — no new kinds are
// introduced.
func InvokeAndConfirm(
	ctx context.Context,
	c *Client,
	method string,
	args any,
	signer Signer,
	opts ...InvokeOption,
) (result any, hash xdr.Hash, err error) {
	if c == nil {
		return nil, xdr.Hash{}, invalidArgsf("InvokeAndConfirm: client is nil")
	}
	if signer == nil {
		return nil, xdr.Hash{}, invalidArgsf("InvokeAndConfirm: signer is nil")
	}

	at, err := c.Invoke(ctx, method, args, opts...)
	if err != nil {
		return nil, xdr.Hash{}, err
	}

	// Read call: no submission needed; Result decodes from the simulated
	// return value. SignAndSend would short-circuit identically, but skipping
	// it avoids re-validating signer state for the read path.
	if at.IsReadCall() {
		v, err := at.Result()
		if err != nil {
			return nil, xdr.Hash{}, err
		}
		return v, xdr.Hash{}, nil
	}

	sent, err := at.SignAndSend(ctx, signer)
	if err != nil {
		return nil, xdr.Hash{}, err
	}
	// SignAndSend returning (nil, nil) on the write path would be a bug;
	// guard against it so a future refactor surfaces a clear error.
	if sent == nil {
		return nil, xdr.Hash{}, &Error{
			Kind:    KindSubmissionFailed,
			Details: "InvokeAndConfirm: SignAndSend returned no SentTransaction for a write call",
		}
	}

	if _, err := sent.Wait(ctx); err != nil {
		return nil, sent.Hash, err
	}

	v, err := at.Result()
	if err != nil {
		return nil, sent.Hash, err
	}
	return v, sent.Hash, nil
}
