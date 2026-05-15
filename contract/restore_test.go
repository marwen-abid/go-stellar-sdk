package contract

import (
	"context"
	"errors"
	"testing"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cannedRestorePreamble builds a RestorePreamble whose embedded
// SorobanTransactionData carries a single read-write footprint entry, so
// tests can verify that BuildRestoreTransaction passes the data through
// verbatim (with the resource-fee bump applied).
func cannedRestorePreamble(t *testing.T) (xdr.SorobanTransactionData, *protocol.RestorePreamble) {
	t.Helper()
	var cid xdr.ContractId
	for i := range cid {
		cid[i] = byte(i + 7)
	}
	data := xdr.SorobanTransactionData{
		Resources: xdr.SorobanResources{
			Footprint: xdr.LedgerFootprint{
				ReadWrite: []xdr.LedgerKey{
					{
						Type: xdr.LedgerEntryTypeContractData,
						ContractData: &xdr.LedgerKeyContractData{
							Contract: xdr.ScAddress{
								Type:       xdr.ScAddressTypeScAddressTypeContract,
								ContractId: &cid,
							},
							Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
							Durability: xdr.ContractDataDurabilityPersistent,
						},
					},
				},
			},
			Instructions:  1_234,
			DiskReadBytes: 5_678,
			WriteBytes:    910,
		},
		ResourceFee: 100, // pre-bump fee on the preamble itself is irrelevant; helper uses MinResourceFee
	}
	b64, err := xdr.MarshalBase64(data)
	require.NoError(t, err)
	return data, &protocol.RestorePreamble{
		TransactionDataXDR: b64,
		MinResourceFee:     2_000_000,
	}
}

func TestSimulate_PopulatesRestorePreambleOnArchived(t *testing.T) {
	_, preamble := cannedRestorePreamble(t)
	rpc := &fakeSimulator{
		resp: protocol.SimulateTransactionResponse{
			RestorePreamble: preamble,
		},
	}
	at, err := NewAssembledTransaction(newAssembleParams(t, rpc))
	require.NoError(t, err)

	err = at.Simulate(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRestoreRequired))
	require.NotNil(t, at.RestorePreamble, "preamble should be stashed for BuildRestoreTransaction")
	assert.Equal(t, preamble.TransactionDataXDR, at.RestorePreamble.TransactionDataXDR)
	assert.Equal(t, preamble.MinResourceFee, at.RestorePreamble.MinResourceFee)
	// Simulation itself is intentionally not cached when restore is required.
	assert.Nil(t, at.Simulation)
}

func TestBuildRestoreTransaction_HappyPath(t *testing.T) {
	wantData, preamble := cannedRestorePreamble(t)
	rpc := &fakeSimulator{
		resp: protocol.SimulateTransactionResponse{
			RestorePreamble: preamble,
		},
	}
	at, err := NewAssembledTransaction(newAssembleParams(t, rpc))
	require.NoError(t, err)
	// Drive the preamble capture.
	require.Error(t, at.Simulate(context.Background()))

	restoreTx, err := at.BuildRestoreTransaction()
	require.NoError(t, err)
	require.NotNil(t, restoreTx)

	// Operation is RestoreFootprint with the same source as the underlying op.
	ops := restoreTx.Operations()
	require.Len(t, ops, 1)
	rf, ok := ops[0].(*txnbuild.RestoreFootprint)
	require.True(t, ok, "operation should be *txnbuild.RestoreFootprint")
	assert.Equal(t, at.op.SourceAccount, rf.SourceAccount)

	// Soroban data passed through verbatim, with the resource fee bumped.
	env := restoreTx.ToXDR()
	require.NotNil(t, env.V1)
	require.NotNil(t, env.V1.Tx.Ext.SorobanData)
	gotData := *env.V1.Tx.Ext.SorobanData
	assert.Equal(t, wantData.Resources.Instructions, gotData.Resources.Instructions)
	assert.Equal(t, wantData.Resources.DiskReadBytes, gotData.Resources.DiskReadBytes)
	assert.Equal(t, wantData.Resources.WriteBytes, gotData.Resources.WriteBytes)
	require.Len(t, gotData.Resources.Footprint.ReadWrite, 1)
	assert.Equal(t, wantData.Resources.Footprint.ReadWrite[0].Type, gotData.Resources.Footprint.ReadWrite[0].Type)

	wantFee := xdr.Int64(int64(float64(preamble.MinResourceFee) * DefaultResourceFeeMultiplier))
	assert.Equal(t, wantFee, gotData.ResourceFee)

	// Source account on the envelope matches the AssembledTransaction's.
	assert.Equal(t, at.source.GetAccountID(), restoreTx.SourceAccount().AccountID)
}

func TestBuildRestoreTransaction_NoPreambleReturnsRestoreRequired(t *testing.T) {
	rpc := &fakeSimulator{}
	at, err := NewAssembledTransaction(newAssembleParams(t, rpc))
	require.NoError(t, err)

	_, err = at.BuildRestoreTransaction()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRestoreRequired))
	var e *Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Details, "no RestorePreamble")
}

func TestBuildRestoreTransaction_BadPreambleB64(t *testing.T) {
	rpc := &fakeSimulator{
		resp: protocol.SimulateTransactionResponse{
			RestorePreamble: &protocol.RestorePreamble{
				TransactionDataXDR: "!!not-b64!!",
				MinResourceFee:     1,
			},
		},
	}
	at, err := NewAssembledTransaction(newAssembleParams(t, rpc))
	require.NoError(t, err)
	require.Error(t, at.Simulate(context.Background()))

	_, err = at.BuildRestoreTransaction()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRestoreRequired))
	var e *Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Details, "SorobanTransactionData")
}

func TestBuildRestoreTransaction_AppliesCustomFeeMultiplier(t *testing.T) {
	_, preamble := cannedRestorePreamble(t)
	rpc := &fakeSimulator{
		resp: protocol.SimulateTransactionResponse{
			RestorePreamble: preamble,
		},
	}
	params := newAssembleParams(t, rpc)
	params.ResourceFeeMultiplier = 2.0
	at, err := NewAssembledTransaction(params)
	require.NoError(t, err)
	require.Error(t, at.Simulate(context.Background()))

	restoreTx, err := at.BuildRestoreTransaction()
	require.NoError(t, err)

	env := restoreTx.ToXDR()
	require.NotNil(t, env.V1.Tx.Ext.SorobanData)
	assert.Equal(t, xdr.Int64(preamble.MinResourceFee*2), env.V1.Tx.Ext.SorobanData.ResourceFee)
}
