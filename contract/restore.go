package contract

import (
	"fmt"
	"math"

	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// BuildRestoreTransaction constructs a txnbuild.Transaction wrapping a
// RestoreFootprint operation built from the most recent simulation's
// RestorePreamble. Callers reach this path after Simulate returns an *Error
// matching ErrRestoreRequired: the original invocation cannot proceed until
// the archived ledger entries identified by the preamble are restored.
//
// The returned transaction is unsigned and uses the same source account,
// base fee, memo, and preconditions as the original AssembledTransaction.
// The Soroban transaction data (footprint + resources) is taken verbatim
// from the preamble; the resource fee is multiplied by
// ResourceFeeMultiplier to absorb ledger drift, matching the behavior of
// Simulate on the original invocation.
//
// Submitting the restore tx, then re-running Simulate on the original
// AssembledTransaction, is the responsibility of later lifecycle methods
// (Send in T3.4, SignAndSend in T3.6); this helper only assembles the
// recovery operation.
//
// Returns an *Error wrapping ErrRestoreRequired with descriptive Details
// when no preamble is available (e.g. Simulate has not been run, or the
// last simulation did not report archived entries).
func (a *AssembledTransaction) BuildRestoreTransaction() (*txnbuild.Transaction, error) {
	if a == nil {
		return nil, &Error{
			Kind:    KindRestoreRequired,
			Details: "AssembledTransaction is nil",
		}
	}
	if a.RestorePreamble == nil {
		return nil, &Error{
			Kind:    KindRestoreRequired,
			Details: "no RestorePreamble: call Simulate first and check for ErrRestoreRequired",
		}
	}
	if a.RestorePreamble.TransactionDataXDR == "" {
		return nil, &Error{
			Kind:    KindRestoreRequired,
			Details: "RestorePreamble missing TransactionDataXDR",
		}
	}

	var sorobanData xdr.SorobanTransactionData
	if err := xdr.SafeUnmarshalBase64(a.RestorePreamble.TransactionDataXDR, &sorobanData); err != nil {
		return nil, &Error{
			Kind:    KindRestoreRequired,
			Details: "decode RestorePreamble SorobanTransactionData",
			cause:   err,
		}
	}

	// Apply the resource-fee multiplier; clamp to int64. Mirrors the
	// post-simulate fee bump applied to the original invocation so the
	// restore tx has the same safety margin against ledger drift.
	bumped := float64(a.RestorePreamble.MinResourceFee) * a.resourceFeeMultiplier
	if bumped > math.MaxInt64 {
		bumped = math.MaxInt64
	}
	sorobanData.ResourceFee = xdr.Int64(int64(bumped))

	op := &txnbuild.RestoreFootprint{
		SourceAccount: a.op.SourceAccount,
		Ext: xdr.TransactionExt{
			V:           1,
			SorobanData: &sorobanData,
		},
	}

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        a.source,
		IncrementSequenceNum: false,
		Operations:           []txnbuild.Operation{op},
		BaseFee:              a.baseFee,
		Memo:                 a.memo,
		Preconditions:        a.preconditions,
	})
	if err != nil {
		return nil, fmt.Errorf("contract: build restore transaction: %w", err)
	}
	return tx, nil
}
