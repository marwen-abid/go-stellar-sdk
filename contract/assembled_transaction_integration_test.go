package contract

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/protocols/stellarcore"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// T3.8 — hermetic end-to-end coverage of the AssembledTransaction lifecycle.
// Each test wires the full chain Simulate -> (SignAuthEntries) -> Sign -> Send
// -> Wait -> Result through fakeSimulator + recordingSigner, exercising the
// seams between the slice-level units already covered in *_test.go. Live
// sandbox coverage is intentionally deferred to T7's networktest helper.

// integrationParams returns AssembleParams + the source keypair, so individual
// tests can supply their own canned simulation response. Spec is bound so
// Result decodes natively (bump: u32 -> u64).
func integrationParams(t *testing.T, rpc rpcSimulator) (AssembleParams, *keypair.Full) {
	t.Helper()
	srcKP := keypair.MustRandom()
	acct := txnbuild.NewSimpleAccount(srcKP.Address(), 42)
	return AssembleParams{
		RPC:               rpc,
		NetworkPassphrase: network.TestNetworkPassphrase,
		BaseFee:           txnbuild.MinBaseFee,
		SourceAccount:     &acct,
		Op:                newTestInvokeOp(t, srcKP.Address()),
		Spec:              buildBumpSpec(t),
		Preconditions:     txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
	}, srcKP
}

// simResponseReadCall returns a simulation response that IsReadCall() will
// flag as a read: no auth entries, empty read-write footprint. retval is the
// simulated return ScVal (u64).
func simResponseReadCall(t *testing.T, retval xdr.ScVal) protocol.SimulateTransactionResponse {
	t.Helper()
	data := xdr.SorobanTransactionData{
		Resources: xdr.SorobanResources{
			Footprint: xdr.LedgerFootprint{}, // no RW => read call
		},
	}
	dataB64, err := xdr.MarshalBase64(data)
	require.NoError(t, err)
	retB64, err := xdr.MarshalBase64(retval)
	require.NoError(t, err)
	return protocol.SimulateTransactionResponse{
		TransactionDataXDR: dataB64,
		MinResourceFee:     1_000,
		Results: []protocol.SimulateHostFunctionResult{
			{ReturnValueXDR: &retB64},
		},
	}
}

// simResponseWriteCall returns a simulation response that IsReadCall() will
// flag as a write: one read-write footprint entry. authEntries is the
// (already base64-encoded) list of auth entries to surface. retval is the
// simulated return ScVal that will be replaced by the on-chain value after
// Wait.
func simResponseWriteCall(t *testing.T, retval xdr.ScVal, authB64 []string) protocol.SimulateTransactionResponse {
	t.Helper()
	rwKey := xdr.LedgerKey{
		Type:    xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{AccountId: xdr.MustAddress(keypair.MustRandom().Address())},
	}
	data := xdr.SorobanTransactionData{
		Resources: xdr.SorobanResources{
			Footprint: xdr.LedgerFootprint{ReadWrite: []xdr.LedgerKey{rwKey}},
		},
	}
	dataB64, err := xdr.MarshalBase64(data)
	require.NoError(t, err)
	retB64, err := xdr.MarshalBase64(retval)
	require.NoError(t, err)

	result := protocol.SimulateHostFunctionResult{ReturnValueXDR: &retB64}
	if len(authB64) > 0 {
		auth := append([]string(nil), authB64...)
		result.AuthXDR = &auth
	}
	return protocol.SimulateTransactionResponse{
		TransactionDataXDR: dataB64,
		MinResourceFee:     1_000_000,
		Results:            []protocol.SimulateHostFunctionResult{result},
	}
}

// ---------------------------------------------------------------------------
// Read-only call: Simulate -> SignAndSend short-circuits -> Result.
// ---------------------------------------------------------------------------

func TestLifecycle_ReadCall(t *testing.T) {
	rpc := &fakeSimulator{
		resp: simResponseReadCall(t, scValU64(42)),
	}
	params, srcKP := integrationParams(t, rpc)
	at, err := NewAssembledTransaction(params)
	require.NoError(t, err)

	require.NoError(t, at.Simulate(context.Background()))
	assert.True(t, at.IsReadCall(), "footprint+auth shape should classify as read call")

	signer := newRecordingSigner(t, srcKP.Address())
	sent, err := at.SignAndSend(context.Background(), signer)
	require.NoError(t, err)
	assert.Nil(t, sent, "read-call SignAndSend returns nil SentTransaction")

	// Signer untouched: the envelope was not signed and no preimage requested.
	assert.Equal(t, 0, signer.txCalls, "SignTransaction must NOT be called for read calls")
	assert.Len(t, signer.preimage, 0, "SignAuthEntryPreimage must NOT be called for read calls")

	// RPC: exactly one simulate call, zero send/get calls.
	assert.Equal(t, 1, rpc.calls)
	assert.Equal(t, 0, rpc.sendCalls)
	assert.Equal(t, 0, rpc.getCalls)

	got, err := at.Result()
	require.NoError(t, err)
	assert.Equal(t, uint64(42), got, "Result decodes the simulated retval via Spec")
}

// ---------------------------------------------------------------------------
// Read-write call with a source-only auth entry: full chain through Wait.
// ---------------------------------------------------------------------------

func TestLifecycle_WriteCall_SourceOnly(t *testing.T) {
	_, authB64 := cannedAuthEntry(t) // SourceAccount-credentialed
	rpc := &fakeSimulator{
		resp: simResponseWriteCall(t, scValU64(1), []string{authB64}),
		sendResp: protocol.SendTransactionResponse{
			Status: stellarcore.TXStatusPending,
			Hash:   strings.Repeat("ab", 32),
		},
	}
	params, srcKP := integrationParams(t, rpc)
	at, err := NewAssembledTransaction(params)
	require.NoError(t, err)

	require.NoError(t, at.Simulate(context.Background()))
	assert.False(t, at.IsReadCall(), "footprint with ReadWrite must NOT be read call")

	// No non-invoker signatures are required: SourceAccount-credentialed
	// entries authorize via the envelope signature.
	pending, err := at.NeedsNonInvokerSigningBy(false)
	require.NoError(t, err)
	assert.Empty(t, pending)

	signer := newRecordingSigner(t, srcKP.Address())
	sent, err := at.SignAndSend(context.Background(), signer)
	require.NoError(t, err)
	require.NotNil(t, sent)
	assert.Equal(t, strings.Repeat("ab", 32), sent.Hash.HexString())

	// Envelope was signed exactly once; no auth-entry signatures were
	// required for SourceAccount credentials.
	assert.Equal(t, 1, signer.txCalls)
	assert.Len(t, signer.preimage, 0, "SourceAccount-credentialed entries skip SignAuthEntryPreimage")

	// RPC sequence so far: 1 simulate, 1 send, 0 get.
	assert.Equal(t, 1, rpc.calls)
	assert.Equal(t, 1, rpc.sendCalls)
	assert.Equal(t, 0, rpc.getCalls)

	// Verify the Send request carries the same envelope that the AT holds.
	wantEnv, err := at.Built.Base64()
	require.NoError(t, err)
	assert.Equal(t, wantEnv, rpc.gotSendReq.Transaction)

	// Wait: RPC returns SUCCESS with an on-chain return value of u64(777).
	rpc.getResp = protocol.GetTransactionResponse{
		TransactionDetails: protocol.TransactionDetails{
			Status:        protocol.TransactionStatusSuccess,
			ResultMetaXDR: buildReadWriteResultMeta(t, scValU64(777)),
		},
	}
	resp, err := sent.Wait(context.Background(), PollInterval(time.Millisecond), PollTimeout(2*time.Second))
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, protocol.TransactionStatusSuccess, resp.Status)
	assert.Equal(t, 1, rpc.getCalls)
	assert.Equal(t, sent.Hash.HexString(), rpc.gotGetReq.Hash)

	// Result prefers the on-chain final value (777) over the simulated (1).
	got, err := at.Result()
	require.NoError(t, err)
	assert.Equal(t, uint64(777), got)
}

// ---------------------------------------------------------------------------
// Read-write call needing a non-invoker signer: SignAuthEntries first.
// ---------------------------------------------------------------------------

func TestLifecycle_WriteCall_NonInvokerAuth(t *testing.T) {
	srcKP := keypair.MustRandom()
	otherKP := keypair.MustRandom()

	// One Address auth entry credentialed by `other` (not source).
	otherEntry := addressEntry(t, otherKP.Address(), 99)
	otherEntryB64, err := xdr.MarshalBase64(otherEntry)
	require.NoError(t, err)

	rpc := &fakeSimulator{
		resp: simResponseWriteCall(t, scValU64(2), []string{otherEntryB64}),
		sendResp: protocol.SendTransactionResponse{
			Status: stellarcore.TXStatusPending,
			Hash:   strings.Repeat("cd", 32),
		},
	}

	// Hand-construct AssembleParams so the source matches srcKP exactly.
	acct := txnbuild.NewSimpleAccount(srcKP.Address(), 42)
	at, err := NewAssembledTransaction(AssembleParams{
		RPC:               rpc,
		NetworkPassphrase: network.TestNetworkPassphrase,
		BaseFee:           txnbuild.MinBaseFee,
		SourceAccount:     &acct,
		Op:                newTestInvokeOp(t, srcKP.Address()),
		Spec:              buildBumpSpec(t),
		Preconditions:     txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
	})
	require.NoError(t, err)

	require.NoError(t, at.Simulate(context.Background()))

	// NeedsNonInvokerSigningBy must surface the foreign address.
	pending, err := at.NeedsNonInvokerSigningBy(false)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, otherKP.Address(), pending[0])

	// SignAndSend with only the source signer must fail with
	// ErrNeedsMoreSignatures (the foreign entry is still unsigned).
	srcSigner := newRecordingSigner(t, srcKP.Address())
	failSent, err := at.SignAndSend(context.Background(), srcSigner)
	require.Error(t, err)
	assert.Nil(t, failSent)
	assert.True(t, errors.Is(err, ErrNeedsMoreSignatures))
	assert.Equal(t, 0, rpc.sendCalls, "must not submit until non-invoker entry is signed")

	// Sign the foreign entry with `other`, then retry SignAndSend with src.
	otherSigner := newRecordingSigner(t, otherKP.Address())
	require.NoError(t, at.SignAuthEntries(context.Background(), otherSigner, 200))
	assert.Len(t, otherSigner.preimage, 1, "other signer must be asked once")

	// After signing, no further non-invoker signatures should be pending.
	pending, err = at.NeedsNonInvokerSigningBy(false)
	require.NoError(t, err)
	assert.Empty(t, pending)

	sent, err := at.SignAndSend(context.Background(), srcSigner)
	require.NoError(t, err)
	require.NotNil(t, sent)
	assert.Equal(t, strings.Repeat("cd", 32), sent.Hash.HexString())
	assert.Equal(t, 1, srcSigner.txCalls, "source signer signs the envelope exactly once")
	assert.Equal(t, 1, rpc.sendCalls)

	// Wait + Result decode the on-chain value.
	rpc.getResp = protocol.GetTransactionResponse{
		TransactionDetails: protocol.TransactionDetails{
			Status:        protocol.TransactionStatusSuccess,
			ResultMetaXDR: buildReadWriteResultMeta(t, scValU64(123)),
		},
	}
	_, err = sent.Wait(context.Background(), PollInterval(time.Millisecond), PollTimeout(2*time.Second))
	require.NoError(t, err)

	got, err := at.Result()
	require.NoError(t, err)
	assert.Equal(t, uint64(123), got)
}

// ---------------------------------------------------------------------------
// Restore preamble: Simulate surfaces ErrRestoreRequired, caller builds the
// recovery tx via BuildRestoreTransaction.
// ---------------------------------------------------------------------------

func TestLifecycle_RestorePreamble(t *testing.T) {
	wantRestoreData, preamble := cannedRestorePreamble(t)
	rpc := &fakeSimulator{
		resp: protocol.SimulateTransactionResponse{
			RestorePreamble: preamble,
		},
	}
	params, _ := integrationParams(t, rpc)
	at, err := NewAssembledTransaction(params)
	require.NoError(t, err)

	err = at.Simulate(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRestoreRequired))
	require.NotNil(t, at.RestorePreamble, "RestorePreamble must be stashed for BuildRestoreTransaction")

	// Caller drives the restore: build the recovery tx without re-simulating.
	restoreTx, err := at.BuildRestoreTransaction()
	require.NoError(t, err)
	require.NotNil(t, restoreTx)

	// The restore op wraps RestoreFootprint with the preamble's footprint.
	ops := restoreTx.Operations()
	require.Len(t, ops, 1)
	rf, ok := ops[0].(*txnbuild.RestoreFootprint)
	require.True(t, ok, "expected *RestoreFootprint, got %T", ops[0])
	require.NotNil(t, rf.Ext.SorobanData)
	assert.Equal(t,
		wantRestoreData.Resources.Footprint.ReadWrite[0].Type,
		rf.Ext.SorobanData.Resources.Footprint.ReadWrite[0].Type,
		"restore tx must carry the preamble's RW footprint",
	)
}

// ---------------------------------------------------------------------------
// Send error: RPC returns ERROR; SignAndSend wraps ErrSubmissionFailed.
// ---------------------------------------------------------------------------

func TestLifecycle_SendError(t *testing.T) {
	_, authB64 := cannedAuthEntry(t)
	rpc := &fakeSimulator{
		resp:     simResponseWriteCall(t, scValU64(0), []string{authB64}),
		sendResp: protocol.SendTransactionResponse{Status: stellarcore.TXStatusError},
	}
	params, srcKP := integrationParams(t, rpc)
	at, err := NewAssembledTransaction(params)
	require.NoError(t, err)

	require.NoError(t, at.Simulate(context.Background()))

	sent, err := at.SignAndSend(context.Background(), newRecordingSigner(t, srcKP.Address()))
	require.Error(t, err)
	assert.Nil(t, sent)
	assert.True(t, errors.Is(err, ErrSubmissionFailed))

	// Result must remain unavailable for write calls when Send failed.
	_, err = at.Result()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotYetSimulated),
		"Result for an unsent write call should surface ErrNotYetSimulated")
}

// ---------------------------------------------------------------------------
// Wait timeout: GetTransaction stays NOT_FOUND; the poll deadline elapses.
// ---------------------------------------------------------------------------

func TestLifecycle_WaitTimeout(t *testing.T) {
	_, authB64 := cannedAuthEntry(t)
	rpc := &fakeSimulator{
		resp: simResponseWriteCall(t, scValU64(0), []string{authB64}),
		sendResp: protocol.SendTransactionResponse{
			Status: stellarcore.TXStatusPending,
			Hash:   strings.Repeat("ef", 32),
		},
		getResp: protocol.GetTransactionResponse{
			TransactionDetails: protocol.TransactionDetails{
				Status: protocol.TransactionStatusNotFound,
			},
		},
	}
	params, srcKP := integrationParams(t, rpc)
	at, err := NewAssembledTransaction(params)
	require.NoError(t, err)

	require.NoError(t, at.Simulate(context.Background()))
	sent, err := at.SignAndSend(context.Background(), newRecordingSigner(t, srcKP.Address()))
	require.NoError(t, err)
	require.NotNil(t, sent)

	// Tight poll deadline: the fake never advances past NOT_FOUND, so Wait
	// must surface ErrTimeout.
	_, err = sent.Wait(context.Background(),
		PollInterval(time.Millisecond),
		PollTimeout(20*time.Millisecond),
	)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTimeout), "want ErrTimeout, got %v", err)
}

// ---------------------------------------------------------------------------
// Serialized round-trip: ToXDR -> FromXDR -> SignAndSend on the rehydrated AT.
// Exercises the multi-party handoff seam (signer and submitter on different
// hosts).
// ---------------------------------------------------------------------------

func TestLifecycle_XDRRoundTrip_ThenSignAndSend(t *testing.T) {
	// Step 1: simulate on host A.
	_, authB64 := cannedAuthEntry(t)
	hostA := &fakeSimulator{
		resp: simResponseWriteCall(t, scValU64(7), []string{authB64}),
	}
	params, srcKP := integrationParams(t, hostA)
	atA, err := NewAssembledTransaction(params)
	require.NoError(t, err)
	require.NoError(t, atA.Simulate(context.Background()))

	encoded, err := atA.ToXDR()
	require.NoError(t, err)
	require.NotEmpty(t, encoded)

	// Step 2: rehydrate on host B with a fresh RPC and Spec.
	hostB := &fakeSimulator{
		sendResp: protocol.SendTransactionResponse{
			Status: stellarcore.TXStatusPending,
			Hash:   strings.Repeat("12", 32),
		},
		getResp: protocol.GetTransactionResponse{
			TransactionDetails: protocol.TransactionDetails{
				Status:        protocol.TransactionStatusSuccess,
				ResultMetaXDR: buildReadWriteResultMeta(t, scValU64(555)),
			},
		},
	}
	atB, err := FromXDR(context.Background(), hostB, encoded,
		WithNetworkPassphrase(network.TestNetworkPassphrase),
		WithSpecOverride(buildBumpSpec(t)),
	)
	require.NoError(t, err)
	require.NotNil(t, atB.Simulation, "rehydrated AT must look simulated")
	assert.Equal(t, atA.Method, atB.Method)

	// Step 3: SignAndSend on the rehydrated AT.
	sent, err := atB.SignAndSend(context.Background(), newRecordingSigner(t, srcKP.Address()))
	require.NoError(t, err)
	require.NotNil(t, sent)
	assert.Equal(t, 1, hostB.sendCalls)
	assert.Equal(t, 0, hostA.sendCalls, "host A must not have been touched after handoff")

	// Step 4: Wait + Result on the rehydrated AT.
	_, err = sent.Wait(context.Background(), PollInterval(time.Millisecond), PollTimeout(2*time.Second))
	require.NoError(t, err)

	got, err := atB.Result()
	require.NoError(t, err)
	assert.Equal(t, uint64(555), got)
}
