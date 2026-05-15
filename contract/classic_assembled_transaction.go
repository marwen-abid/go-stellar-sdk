package contract

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ClassicSubmitFunc submits an already-built classic (non-Soroban)
// transaction to the network and returns its on-chain hash. Implementations
// are caller-supplied (Horizon, custom RPC fan-out, an in-memory mock for
// tests, etc.) so the contract package does not need to import Horizon.
//
// The signer passed to ClassicSubmitFunc is the default Signer threaded
// through AssembledTransaction; implementations are expected to invoke
// signer.SignTransaction(network, tx) before submission. They may inspect
// or replace the transaction (e.g. to attach a memo); the returned hash
// must be the hash of the envelope that landed on the network.
type ClassicSubmitFunc func(ctx context.Context, tx *txnbuild.Transaction, signer Signer) (xdr.Hash, error)

// ClassicAssembleParams configures a classic AssembledTransaction returned
// by NewClassicAssembledTransaction. It is the equivalent of AssembleParams
// for the classic Payment fast path that asset.Token.Transfer dispatches
// when both endpoints are G/M accounts and the asset is a classic asset.
//
// Built and Submit are required. NetworkPassphrase is required for signing.
// Signer is optional at construction time but must be supplied (either
// here or at the SignAndSend call site) before the transaction is sent.
type ClassicAssembleParams struct {
	// Built is the pre-built classic transaction (a txnbuild.Payment or
	// any other non-Soroban op chain). NewClassicAssembledTransaction does
	// not modify it; signing happens inside the Submit closure or via the
	// SignAndSend signer fallback.
	Built *txnbuild.Transaction
	// NetworkPassphrase identifies the network this transaction will be
	// submitted to; passed to Signer.SignTransaction by the SignAndSend
	// short-circuit.
	NetworkPassphrase string
	// Signer is the default Signer used by SignAndSend when the caller
	// does not pass an explicit signer. Optional at construction time.
	Signer Signer
	// Submit is the caller-supplied classic submission hook. Required.
	Submit ClassicSubmitFunc
}

// NewClassicAssembledTransaction wraps a pre-built classic transaction in an
// AssembledTransaction whose SignAndSend short-circuits to a caller-supplied
// ClassicSubmitFunc instead of the Soroban Sign/Simulate/Send pipeline. The
// result is a uniform handle: asset.Token.Transfer can return one regardless
// of whether it dispatched the classic Payment fast path or the SAC
// transfer invocation.
//
// The returned AssembledTransaction has Simulate / Send / Sign / Result /
// IsReadCall / NeedsNonInvokerSigningBy disabled — they are Soroban-only
// concepts that do not apply to classic transactions. Calling them returns
// an *Error matching ErrInvalidArgs explaining the misuse.
func NewClassicAssembledTransaction(params ClassicAssembleParams) (*AssembledTransaction, error) {
	if params.Built == nil {
		return nil, invalidArgsf("ClassicAssembleParams.Built is required")
	}
	if params.NetworkPassphrase == "" {
		return nil, invalidArgsf("ClassicAssembleParams.NetworkPassphrase is required")
	}
	if params.Submit == nil {
		return nil, invalidArgsf("ClassicAssembleParams.Submit is required")
	}

	return &AssembledTransaction{
		Built:         params.Built,
		network:       params.NetworkPassphrase,
		signer:        params.Signer,
		classicSubmit: params.Submit,
	}, nil
}

// IsClassic reports whether this AssembledTransaction wraps a classic
// transaction (Payment, etc.) submitted via the ClassicSubmitFunc seam,
// versus the Soroban host-function pipeline. Callers can branch on this
// when inspecting an AT returned by asset.Token.Transfer.
func (a *AssembledTransaction) IsClassic() bool {
	if a == nil {
		return false
	}
	return a.classicSubmit != nil
}

// signAndSendClassic is the SignAndSend short-circuit for classic
// AssembledTransactions. It delegates submission to the configured
// ClassicSubmitFunc and wraps the returned hash in a *SentTransaction.
//
// Signer resolution mirrors the Soroban path: an explicit signer argument
// wins over the AT's default signer. A nil signer is acceptable when the
// classicSubmit closure carries its own signing material (e.g. Horizon
// already-signed envelope wrappers).
func (a *AssembledTransaction) signAndSendClassic(ctx context.Context, signer Signer) (*SentTransaction, error) {
	if a.sent != nil {
		return a.sent, nil
	}
	effectiveSigner := signer
	if effectiveSigner == nil {
		effectiveSigner = a.signer
	}

	hash, err := a.classicSubmit(ctx, a.Built, effectiveSigner)
	if err != nil {
		return nil, &Error{Kind: KindSubmissionFailed, Details: fmt.Sprintf("SignAndSend: classic submitter: %v", err), cause: err}
	}

	sent := &SentTransaction{
		Hash:    hash,
		classic: true,
	}
	a.sent = sent
	return sent, nil
}
