package contract

import (
	"context"
	"fmt"
	"math"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// DefaultResourceFeeMultiplier is the multiplier applied to the resource fee
// returned by simulation before it is written back into the transaction. The
// 15% pad mirrors the JS SDK's AssembledTransaction default and absorbs the
// small overhead introduced by ledger drift between simulate and submit.
const DefaultResourceFeeMultiplier = 1.15

// rpcSimulator is the minimal slice of the RPC client surface that
// AssembledTransaction needs at this stage. Subsequent phases (Send, Wait)
// will widen this interface. Accepting an interface keeps the type unit
// testable without standing up a live RPC.
type rpcSimulator interface {
	SimulateTransaction(ctx context.Context, req protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error)
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

	// unexported state held across the lifecycle.
	rpc                   rpcSimulator
	op                    *txnbuild.InvokeHostFunction
	source                txnbuild.Account
	network               string
	baseFee               int64
	memo                  txnbuild.Memo
	preconditions         txnbuild.Preconditions
	resourceFeeMultiplier float64
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
	}, nil
}

// Raw exposes the current underlying transaction for callers that need to
// drop down to txnbuild. The returned pointer should be treated as read-only;
// subsequent lifecycle steps may replace it.
func (a *AssembledTransaction) Raw() *txnbuild.Transaction { return a.Built }

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
// server-supplied message as Details. If the response carries a non-nil
// RestorePreamble (archived footprint entries), Simulate returns an *Error
// matching ErrRestoreRequired; the automatic restore path lives in T3.2.
//
// Simulate is idempotent: calling it again replays the request and
// overwrites the simulation-derived fields.
func (a *AssembledTransaction) Simulate(ctx context.Context) error {
	if a == nil || a.rpc == nil {
		return invalidArgsf("AssembledTransaction not initialized")
	}

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
