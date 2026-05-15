package contract

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/protocols/stellarcore"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// DefaultResourceFeeMultiplier is the multiplier applied to the resource fee
// returned by simulation before it is written back into the transaction. The
// 15% pad mirrors the JS SDK's AssembledTransaction default and absorbs the
// small overhead introduced by ledger drift between simulate and submit.
const DefaultResourceFeeMultiplier = 1.15

// rpcSimulator is the minimal slice of the RPC client surface that
// AssembledTransaction needs. It is extended one method at a time as new
// lifecycle steps land (Simulate in T3.1, SendTransaction in T3.4, the
// get/poll calls in T3.5). Accepting an interface keeps the type unit
// testable without standing up a live RPC; the concrete
// *rpcclient.Client already implements every method.
type rpcSimulator interface {
	SimulateTransaction(ctx context.Context, req protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error)
	SendTransaction(ctx context.Context, req protocol.SendTransactionRequest) (protocol.SendTransactionResponse, error)
	GetTransaction(ctx context.Context, req protocol.GetTransactionRequest) (protocol.GetTransactionResponse, error)
}

// AssembledTransaction is the JS-parity wrapper around the Soroban
// transaction lifecycle: build -> simulate -> (restore) -> signAuthEntries ->
// sign -> send -> poll. T3.1 only seeds the type and the Simulate step;
// remaining steps land in later tasks (T3.2-T3.8).
//
// A zero value is not usable; construct one via NewAssembledTransaction.
// AssembledTransaction is not safe for concurrent use.
type AssembledTransaction struct {
	// Method is the contract function being invoked. Populated when the
	// invocation host function is of type InvokeContract; empty otherwise.
	Method string
	// Args are the positional ScVal arguments for Method (same shape as
	// xdr.InvokeContractArgs.Args). Empty when not an InvokeContract call.
	Args []xdr.ScVal
	// Built is the in-flight transaction. It is rebuilt by Simulate so that
	// the SorobanData footprint, resource fee, and auth entries reflect the
	// simulation outcome.
	Built *txnbuild.Transaction
	// Simulation is the most recent simulation response. Nil until Simulate
	// has run successfully at least once.
	Simulation *protocol.SimulateTransactionResponse
	// AuthEntries are the SorobanAuthorizationEntry values returned by
	// simulation, decoded from base64. Nil until Simulate has run.
	AuthEntries []xdr.SorobanAuthorizationEntry
	// ReturnValue is the simulated return value of the contract call (read
	// calls can use this directly without submitting). Nil until Simulate
	// has run and returned at least one result.
	ReturnValue *xdr.ScVal
	// RestorePreamble is the RestoreFootprint footprint surfaced by
	// simulation when one or more ledger entries in the invocation's
	// footprint are archived. Non-nil only when Simulate returned an *Error
	// matching ErrRestoreRequired; consumed by BuildRestoreTransaction to
	// construct the recovery operation.
	RestorePreamble *protocol.RestorePreamble

	// unexported state held across the lifecycle.
	rpc                   rpcSimulator
	op                    *txnbuild.InvokeHostFunction
	source                txnbuild.Account
	network               string
	baseFee               int64
	memo                  txnbuild.Memo
	preconditions         txnbuild.Preconditions
	resourceFeeMultiplier float64
	spec                  *Spec
	// signer is the per-AssembledTransaction default Signer plumbed from a
	// client-level WithSigner or per-call WithInvokeSigner. SignAndSend
	// continues to accept an explicit signer argument; when callers route
	// through InvokeAndConfirm this field is consulted only as a fallback.
	signer Signer
	// maxFee caps the total transaction fee in stroops. Zero means uncapped.
	maxFee int64
	// restoreEnabled records the caller's auto-restore preference. The
	// transparent restore-and-retry flow lands in a follow-up; today this
	// flag is read by callers who orchestrate restore manually.
	restoreEnabled bool

	// sent caches the result of a successful Send so subsequent Send calls
	// are no-ops (JS parity: AssembledTransaction.send is idempotent).
	sent *SentTransaction
}

// AssembleParams configures a new AssembledTransaction. SourceAccount must
// already carry the sequence number the transaction will use; Simulate does
// not increment it. RPC, NetworkPassphrase, BaseFee, SourceAccount, and Op
// are required.
type AssembleParams struct {
	// RPC is the Stellar RPC client used for simulation.
	RPC rpcSimulator
	// NetworkPassphrase identifies the network this transaction will be
	// submitted to and is required for signing in later phases.
	NetworkPassphrase string
	// BaseFee is the per-operation base fee in stroops. Must be >=
	// txnbuild.MinBaseFee.
	BaseFee int64
	// SourceAccount is the account that authorizes the transaction. It is
	// passed to txnbuild.NewTransaction as-is, without sequence increment.
	SourceAccount txnbuild.Account
	// Op is the InvokeHostFunction operation the transaction wraps. The
	// AssembledTransaction retains the pointer and mutates Op.Auth +
	// Op.Ext.SorobanData on Simulate.
	Op *txnbuild.InvokeHostFunction
	// Memo is an optional memo attached to the transaction.
	Memo txnbuild.Memo
	// Preconditions is an optional set of transaction preconditions
	// (timebounds, ledger bounds, etc.).
	Preconditions txnbuild.Preconditions
	// ResourceFeeMultiplier overrides DefaultResourceFeeMultiplier for this
	// transaction. Values <= 0 fall back to the default.
	ResourceFeeMultiplier float64
	// Spec is the optional contract Spec used by Result() to decode the
	// returned ScVal into a native Go value. When nil, Result() returns the
	// raw xdr.ScVal.
	Spec *Spec
}

// NewAssembledTransaction validates params, builds the initial
// txnbuild.Transaction wrapping the host function, and returns the lifecycle
// wrapper. The transaction has not yet been simulated; callers must invoke
// Simulate before Sign / Send.
func NewAssembledTransaction(params AssembleParams) (*AssembledTransaction, error) {
	if params.RPC == nil {
		return nil, invalidArgsf("AssembleParams.RPC is required")
	}
	if params.NetworkPassphrase == "" {
		return nil, invalidArgsf("AssembleParams.NetworkPassphrase is required")
	}
	if params.SourceAccount == nil {
		return nil, invalidArgsf("AssembleParams.SourceAccount is required")
	}
	if params.Op == nil {
		return nil, invalidArgsf("AssembleParams.Op is required")
	}
	if params.BaseFee < txnbuild.MinBaseFee {
		return nil, invalidArgsf("AssembleParams.BaseFee %d below MinBaseFee %d", params.BaseFee, txnbuild.MinBaseFee)
	}

	mult := params.ResourceFeeMultiplier
	if mult <= 0 {
		mult = DefaultResourceFeeMultiplier
	}

	method, args := extractInvocation(params.Op.HostFunction)

	tx, err := buildTx(params.SourceAccount, params.Op, params.BaseFee, params.Memo, params.Preconditions)
	if err != nil {
		return nil, fmt.Errorf("contract: build initial transaction: %w", err)
	}

	return &AssembledTransaction{
		Method:                method,
		Args:                  args,
		Built:                 tx,
		rpc:                   params.RPC,
		op:                    params.Op,
		source:                params.SourceAccount,
		network:               params.NetworkPassphrase,
		baseFee:               params.BaseFee,
		memo:                  params.Memo,
		preconditions:         params.Preconditions,
		resourceFeeMultiplier: mult,
		spec:                  params.Spec,
	}, nil
}

// Raw exposes the current underlying transaction for callers that need to
// drop down to txnbuild. The returned pointer should be treated as read-only;
// subsequent lifecycle steps may replace it.
func (a *AssembledTransaction) Raw() *txnbuild.Transaction { return a.Built }

// Send submits the signed envelope to the RPC's sendTransaction endpoint and
// returns a *SentTransaction wrapping the hash and submission response. Send
// requires Simulate to have run and the envelope to carry at least one
// signature (Sign must have been called); otherwise it returns an *Error
// matching ErrNotYetSimulated or wrapping ErrSubmissionFailed.
//
// Status handling mirrors the JS SDK's AssembledTransaction.send:
//
//   - PENDING / DUPLICATE → success (the transaction is on the RPC's
//     submission queue or was already accepted previously).
//   - ERROR / TRY_AGAIN_LATER → returns an *Error wrapping ErrSubmissionFailed
//     so callers can match with errors.Is and inspect the SentTransaction
//     attached as the cause's response field for diagnostics.
//
// Send is idempotent: a second call after a successful submission returns the
// cached *SentTransaction without re-submitting. Polling for the on-chain
// result is the job of *SentTransaction.Wait (lands in T3.5).
func (a *AssembledTransaction) Send(ctx context.Context) (*SentTransaction, error) {
	if a == nil || a.rpc == nil {
		return nil, invalidArgsf("AssembledTransaction not initialized")
	}
	if a.Simulation == nil || a.Built == nil {
		return nil, &Error{Kind: KindNotYetSimulated, Details: "Send"}
	}
	if a.sent != nil {
		return a.sent, nil
	}
	if len(a.Built.Signatures()) == 0 {
		return nil, &Error{Kind: KindSubmissionFailed, Details: "Send: envelope is unsigned; call Sign first"}
	}
	// Enforce the MaxFee cap. txnbuild.Transaction.MaxFee() returns the
	// envelope's total fee (BaseFee * #ops + Soroban resource fee), which
	// matches the inclusion+resource sum MaxFee documents itself to bound.
	// A zero cap means "uncapped" and preserves pre-cap behavior; we fail
	// the check only when the effective fee strictly exceeds the cap so
	// MaxFee == effective remains a successful send.
	if a.maxFee > 0 {
		if effective := a.Built.MaxFee(); effective > a.maxFee {
			return nil, &Error{
				Kind:    KindSubmissionFailed,
				Details: fmt.Sprintf("Send: effective fee %d exceeds MaxFee cap %d", effective, a.maxFee),
			}
		}
	}

	envelopeB64, err := a.Built.Base64()
	if err != nil {
		return nil, fmt.Errorf("contract: encode tx for send: %w", err)
	}

	resp, err := a.rpc.SendTransaction(ctx, protocol.SendTransactionRequest{
		Transaction: envelopeB64,
	})
	if err != nil {
		return nil, &Error{Kind: KindSubmissionFailed, cause: err}
	}

	switch resp.Status {
	case stellarcore.TXStatusPending, stellarcore.TXStatusDuplicate:
		// success — fall through.
	case stellarcore.TXStatusError, stellarcore.TXStatusTryAgainLater:
		return nil, &Error{Kind: KindSubmissionFailed, Details: fmt.Sprintf("Send: RPC returned %s", resp.Status)}
	default:
		return nil, &Error{Kind: KindSubmissionFailed, Details: fmt.Sprintf("Send: unrecognized status %q", resp.Status)}
	}

	hash, err := decodeHexHash(resp.Hash)
	if err != nil {
		return nil, &Error{Kind: KindSubmissionFailed, Details: "Send: decode response hash", cause: err}
	}

	sent := &SentTransaction{
		Hash:         hash,
		SendResponse: &resp,
		rpc:          a.rpc,
		method:       a.Method,
		spec:         a.spec,
	}
	a.sent = sent
	return sent, nil
}

// Simulate runs simulateTransaction against the RPC client supplied at
// construction time and folds the response into the transaction:
//
//   - The returned SorobanTransactionData (footprint + resources) replaces
//     the placeholder data on the InvokeHostFunction op.
//   - The simulated authorization entries replace Op.Auth.
//   - MinResourceFee is multiplied by ResourceFeeMultiplier and written
//     into the new SorobanData.ResourceFee.
//   - The transaction (a.Built) is rebuilt so the envelope picks up both.
//
// On RPC transport failure Simulate returns an *Error wrapping
// ErrSimulationFailed with the underlying error attached. If the response
// carries a non-empty Error field, the same sentinel is returned with the
// server-supplied message as Details.
//
// When the response carries a non-nil RestorePreamble (archived footprint
// entries) and the AssembledTransaction has auto-restore enabled (via the
// Restore(true) InvokeOption — the default for Client.Invoke) AND a Signer
// configured, Simulate transparently:
//
//  1. Builds the RestoreFootprint transaction via BuildRestoreTransaction.
//  2. Signs it with the AT's Signer.
//  3. Submits it via the RPC client.
//  4. Waits for the restore tx to reach a terminal status.
//  5. Re-runs simulation on the original invocation.
//
// Re-simulation is capped at one retry; if simulation reports archived
// footprint entries a second time, Simulate returns an *Error matching
// ErrRestoreRequired with a "loop detected" detail. If auto-restore is
// disabled (Restore(false)) or no Signer is configured, the original
// ErrRestoreRequired is surfaced unchanged so the caller can drive the
// restore flow manually.
//
// Simulate is idempotent: calling it again replays the request and
// overwrites the simulation-derived fields.
func (a *AssembledTransaction) Simulate(ctx context.Context) error {
	if a == nil || a.rpc == nil {
		return invalidArgsf("AssembledTransaction not initialized")
	}

	err := a.simulateOnce(ctx)
	if err == nil || !errors.Is(err, ErrRestoreRequired) {
		return err
	}

	// Auto-restore only fires when the caller opted in AND supplied a
	// Signer the AT can use to sign the restore tx. Otherwise we surface
	// the original ErrRestoreRequired so the caller drives restore manually.
	if !a.restoreEnabled || a.signer == nil {
		return err
	}

	if rerr := a.runAutoRestore(ctx); rerr != nil {
		return rerr
	}

	// Re-simulate the original invocation once. A second ErrRestoreRequired
	// means restore didn't actually resolve the archived footprint; we cap
	// the retry to avoid an infinite loop.
	err = a.simulateOnce(ctx)
	if err != nil && errors.Is(err, ErrRestoreRequired) {
		return &Error{
			Kind:    KindRestoreRequired,
			Details: "auto-restore loop detected: re-simulation still reports archived entries",
		}
	}
	return err
}

// simulateOnce performs a single simulate round-trip and folds the response
// into the AT. It is the body that Simulate wraps with the auto-restore
// retry; calling it directly skips auto-restore.
func (a *AssembledTransaction) simulateOnce(ctx context.Context) error {
	envelopeB64, err := a.Built.Base64()
	if err != nil {
		return fmt.Errorf("contract: encode tx for simulate: %w", err)
	}

	resp, err := a.rpc.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{
		Transaction: envelopeB64,
	})
	if err != nil {
		return &Error{Kind: KindSimulationFailed, cause: err}
	}
	if resp.Error != "" {
		return &Error{Kind: KindSimulationFailed, Details: resp.Error}
	}
	if resp.RestorePreamble != nil {
		// Stash the preamble so BuildRestoreTransaction can consume it; the
		// caller still receives ErrRestoreRequired and must drive the restore
		// flow before re-simulating.
		a.RestorePreamble = resp.RestorePreamble
		return &Error{Kind: KindRestoreRequired, Details: "simulation reported archived footprint entries"}
	}

	var sorobanData xdr.SorobanTransactionData
	if resp.TransactionDataXDR != "" {
		if err = xdr.SafeUnmarshalBase64(resp.TransactionDataXDR, &sorobanData); err != nil {
			return &Error{Kind: KindSimulationFailed, Details: "decode SorobanTransactionData", cause: err}
		}
	}

	authEntries, returnValue, err := decodeFirstResult(resp.Results)
	if err != nil {
		return &Error{Kind: KindSimulationFailed, Details: "decode simulation result", cause: err}
	}

	// Apply the resource-fee multiplier; clamp to int64.
	bumped := float64(resp.MinResourceFee) * a.resourceFeeMultiplier
	if bumped > math.MaxInt64 {
		bumped = math.MaxInt64
	}
	sorobanData.ResourceFee = xdr.Int64(int64(bumped))

	// Mutate the op and rebuild the envelope so txnbuild rewrites the
	// V1 envelope Ext + Auth and recomputes the total fee.
	a.op.Auth = authEntries
	a.op.Ext = xdr.TransactionExt{
		V:           1,
		SorobanData: &sorobanData,
	}

	rebuilt, err := buildTx(a.source, a.op, a.baseFee, a.memo, a.preconditions)
	if err != nil {
		return fmt.Errorf("contract: rebuild transaction after simulate: %w", err)
	}

	a.Built = rebuilt
	a.Simulation = &resp
	a.AuthEntries = authEntries
	a.ReturnValue = returnValue
	// Clear any stale preamble from a prior failed simulate so re-running
	// against a freshly-restored ledger does not leave a misleading pointer.
	a.RestorePreamble = nil
	return nil
}

// runAutoRestore builds, signs, submits, and waits for the
// RestoreFootprint transaction implied by the RestorePreamble from the most
// recent simulate. The preamble is consumed (cleared) on success so a
// follow-up simulate does not falsely advertise a still-archived footprint.
// Errors are wrapped as *Error with the appropriate Kind so callers can
// classify them with errors.Is.
func (a *AssembledTransaction) runAutoRestore(ctx context.Context) error {
	restoreTx, err := a.BuildRestoreTransaction()
	if err != nil {
		return err
	}

	signed, err := a.signer.SignTransaction(a.network, restoreTx)
	if err != nil {
		return &Error{Kind: KindSubmissionFailed, Details: "auto-restore: sign", cause: err}
	}
	if signed == nil {
		return &Error{Kind: KindSubmissionFailed, Details: "auto-restore: signer returned nil transaction"}
	}

	envelopeB64, err := signed.Base64()
	if err != nil {
		return &Error{Kind: KindSubmissionFailed, Details: "auto-restore: encode envelope", cause: err}
	}

	resp, err := a.rpc.SendTransaction(ctx, protocol.SendTransactionRequest{
		Transaction: envelopeB64,
	})
	if err != nil {
		return &Error{Kind: KindSubmissionFailed, Details: "auto-restore: send", cause: err}
	}
	switch resp.Status {
	case stellarcore.TXStatusPending, stellarcore.TXStatusDuplicate:
		// fall through.
	case stellarcore.TXStatusError, stellarcore.TXStatusTryAgainLater:
		return &Error{
			Kind:    KindSubmissionFailed,
			Details: fmt.Sprintf("auto-restore: RPC returned %s", resp.Status),
		}
	default:
		return &Error{
			Kind:    KindSubmissionFailed,
			Details: fmt.Sprintf("auto-restore: unrecognized status %q", resp.Status),
		}
	}

	hash, err := decodeHexHash(resp.Hash)
	if err != nil {
		return &Error{Kind: KindSubmissionFailed, Details: "auto-restore: decode response hash", cause: err}
	}

	// Reuse SentTransaction.Wait for poll semantics; default poll config is
	// sufficient — restore txs are tiny and confirm in 1-2 ledgers.
	sent := &SentTransaction{
		Hash:         hash,
		SendResponse: &resp,
		rpc:          a.rpc,
	}
	if _, err := sent.Wait(ctx); err != nil {
		return err
	}

	// Restore consumed the preamble; clear it so the re-simulate path
	// doesn't trip the "no preamble" branch in BuildRestoreTransaction if
	// the caller re-enters this code path.
	a.RestorePreamble = nil
	return nil
}

// buildTx constructs a transaction wrapping a single InvokeHostFunction
// without incrementing the source account sequence. It is shared between the
// initial build and the post-simulate rebuild.
func buildTx(
	source txnbuild.Account,
	op *txnbuild.InvokeHostFunction,
	baseFee int64,
	memo txnbuild.Memo,
	preconditions txnbuild.Preconditions,
) (*txnbuild.Transaction, error) {
	return txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        source,
		IncrementSequenceNum: false,
		Operations:           []txnbuild.Operation{op},
		BaseFee:              baseFee,
		Memo:                 memo,
		Preconditions:        preconditions,
	})
}

// extractInvocation pulls (method, args) out of a HostFunction when it is an
// InvokeContract call. For CreateContract / UploadWasm host functions the
// concept does not apply; we return zero values.
func extractInvocation(fn xdr.HostFunction) (string, []xdr.ScVal) {
	if fn.Type != xdr.HostFunctionTypeHostFunctionTypeInvokeContract || fn.InvokeContract == nil {
		return "", nil
	}
	ic := fn.InvokeContract
	return string(ic.FunctionName), append([]xdr.ScVal(nil), ic.Args...)
}

// decodeFirstResult decodes the first SimulateHostFunctionResult (Soroban
// only allows one InvokeHostFunction op per transaction so there is at most
// one result). Returns auth entries, the simulated return value, or an
// error if any base64 payload fails to decode.
func decodeFirstResult(results []protocol.SimulateHostFunctionResult) ([]xdr.SorobanAuthorizationEntry, *xdr.ScVal, error) {
	if len(results) == 0 {
		return nil, nil, nil
	}
	r := results[0]

	var authEntries []xdr.SorobanAuthorizationEntry
	if r.AuthXDR != nil {
		authEntries = make([]xdr.SorobanAuthorizationEntry, 0, len(*r.AuthXDR))
		for i, encoded := range *r.AuthXDR {
			var entry xdr.SorobanAuthorizationEntry
			if err := xdr.SafeUnmarshalBase64(encoded, &entry); err != nil {
				return nil, nil, fmt.Errorf("auth entry %d: %w", i, err)
			}
			authEntries = append(authEntries, entry)
		}
	}

	var returnValue *xdr.ScVal
	if r.ReturnValueXDR != nil && *r.ReturnValueXDR != "" {
		var v xdr.ScVal
		if err := xdr.SafeUnmarshalBase64(*r.ReturnValueXDR, &v); err != nil {
			return nil, nil, fmt.Errorf("return value: %w", err)
		}
		returnValue = &v
	}

	return authEntries, returnValue, nil
}

// decodeHexHash parses a 32-byte hex-encoded transaction hash (as returned
// by the RPC sendTransaction response) into an xdr.Hash. Lengths other than
// 64 hex characters are rejected.
func decodeHexHash(s string) (xdr.Hash, error) {
	var h xdr.Hash
	if len(s) != 2*len(h) {
		return h, fmt.Errorf("hash %q: want %d hex chars, got %d", s, 2*len(h), len(s))
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return h, err
	}
	copy(h[:], raw)
	return h, nil
}
