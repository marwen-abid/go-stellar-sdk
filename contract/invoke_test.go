package contract

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/protocols/stellarcore"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeCallSimResp returns a SimulateTransactionResponse that drives the
// post-Invoke AssembledTransaction into the write-call shape: one Address
// auth entry credentialed by srcAddr (which the signer will sign), plus a
// ResourceFee large enough to satisfy resource-fee plumbing, plus a non-empty
// ReadWrite footprint.
func writeCallSimResp(t *testing.T, srcAddr string) protocol.SimulateTransactionResponse {
	t.Helper()
	// Soroban transaction data with a non-empty ReadWrite footprint so
	// IsReadCall returns false.
	rwKey := xdr.LedgerKey{
		Type:    xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{AccountId: xdr.MustAddress(keypair.MustRandom().Address())},
	}
	data := xdr.SorobanTransactionData{
		Resources: xdr.SorobanResources{
			Instructions:  9_000_000,
			DiskReadBytes: 4_096,
			WriteBytes:    2_048,
			Footprint:     xdr.LedgerFootprint{ReadWrite: []xdr.LedgerKey{rwKey}},
		},
		ResourceFee: 7_777_777,
	}
	dataB64, err := xdr.MarshalBase64(data)
	require.NoError(t, err)

	// One Address-credentialed auth entry whose address is srcAddr — the
	// invoker signer will both sign the envelope AND fill in the auth-entry
	// signature; NeedsNonInvokerSigningBy afterwards returns empty.
	authB64, err := xdr.MarshalBase64(addressEntry(t, srcAddr, 1))
	require.NoError(t, err)

	// Simulated return value (u64=42 to match buildBumpSpec output type).
	retB64, err := xdr.MarshalBase64(scValU64(42))
	require.NoError(t, err)

	auth := []string{authB64}
	return protocol.SimulateTransactionResponse{
		TransactionDataXDR: dataB64,
		MinResourceFee:     500_000,
		Results: []protocol.SimulateHostFunctionResult{
			{ReturnValueXDR: &retB64, AuthXDR: &auth},
		},
	}
}

// writeCallGetResp returns a GetTransactionResponse encoding a SUCCESS with a
// u64=`ret` return value baked into ResultMetaXDR.
func writeCallGetResp(t *testing.T, ret uint64) protocol.GetTransactionResponse {
	t.Helper()
	return protocol.GetTransactionResponse{
		TransactionDetails: protocol.TransactionDetails{
			Status:        protocol.TransactionStatusSuccess,
			ResultMetaXDR: buildReadWriteResultMeta(t, scValU64(ret)),
		},
	}
}

// newInvokeAndConfirmClient assembles a Client whose source account, spec,
// and contract id are aligned with the test signer the caller will pass to
// InvokeAndConfirm. Returns the client and the source keypair so the test
// can build a recordingSigner from the same address.
func newInvokeAndConfirmClient(t *testing.T, rpc rpcSimulator) (*Client, *keypair.Full) {
	t.Helper()
	srcKP := keypair.MustRandom()
	acct := txnbuild.NewSimpleAccount(srcKP.Address(), 42)

	// Deterministic contract id so test failures are stable.
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	cid, err := strkey.Encode(strkey.VersionByteContract, raw)
	require.NoError(t, err)

	c := New(cid, rpc, network.TestNetworkPassphrase,
		WithSpec(buildBumpSpec(t)),
		WithSourceAccount(&acct),
	)
	return c, srcKP
}

// ----------- Read-only happy path: no Send / Wait ------------------------

func TestInvokeAndConfirm_ReadCall_DecodesSimulationResult(t *testing.T) {
	// Build a read-call simulation: no Auth entries + empty ReadWrite
	// footprint => IsReadCall returns true. The return ScVal is a u64 to
	// match buildBumpSpec's declared output type so FuncResToNative succeeds.
	data := xdr.SorobanTransactionData{
		Resources:   xdr.SorobanResources{Footprint: xdr.LedgerFootprint{}},
		ResourceFee: 1_000,
	}
	dataB64, err := xdr.MarshalBase64(data)
	require.NoError(t, err)
	retB64, err := xdr.MarshalBase64(scValU64(42))
	require.NoError(t, err)
	rpc := &fakeSimulator{
		resp: protocol.SimulateTransactionResponse{
			TransactionDataXDR: dataB64,
			MinResourceFee:     500_000,
			Results: []protocol.SimulateHostFunctionResult{
				{ReturnValueXDR: &retB64},
			},
		},
	}
	c, srcKP := newInvokeAndConfirmClient(t, rpc)
	signer := newRecordingSigner(t, srcKP.Address())

	got, hash, err := InvokeAndConfirm(context.Background(), c, "bump", map[string]any{"amount": uint32(7)}, signer)
	require.NoError(t, err)

	assert.Equal(t, uint64(42), got, "read-call result decoded from simulation")
	assert.Equal(t, xdr.Hash{}, hash, "read calls return the zero hash")

	assert.Equal(t, 1, rpc.calls, "Simulate must run exactly once")
	assert.Equal(t, 0, rpc.sendCalls, "read call must not invoke Send")
	assert.Equal(t, 0, rpc.getCalls, "read call must not invoke GetTransaction")
	assert.Equal(t, 0, signer.txCalls, "read call must not sign envelope")
	assert.Equal(t, 0, len(signer.preimage), "read call must not sign auth entries")
}

// ----------- Read-write happy path: simulate → SignAndSend → Wait → Result

func TestInvokeAndConfirm_WriteCall_ChainsSimulateSignSendWait(t *testing.T) {
	rpc := &fakeSimulator{}
	c, srcKP := newInvokeAndConfirmClient(t, rpc)
	rpc.resp = writeCallSimResp(t, srcKP.Address())

	wantHash := strings.Repeat("ab", 32)
	rpc.sendResp = protocol.SendTransactionResponse{
		Status: stellarcore.TXStatusPending,
		Hash:   wantHash,
	}
	rpc.getResp = writeCallGetResp(t, 777)

	signer := newRecordingSigner(t, srcKP.Address())

	got, hash, err := InvokeAndConfirm(context.Background(), c, "bump", map[string]any{"amount": uint32(7)}, signer)
	require.NoError(t, err)
	assert.Equal(t, uint64(777), got, "Result must come from the Wait response, not the simulation")
	assert.Equal(t, wantHash, hash.HexString())

	assert.Equal(t, 1, rpc.calls, "Simulate ran once")
	assert.Equal(t, 1, rpc.sendCalls, "SendTransaction ran once")
	assert.GreaterOrEqual(t, rpc.getCalls, 1, "Wait polled GetTransaction at least once")
	assert.Equal(t, 1, signer.txCalls, "envelope signed once")
	assert.Equal(t, 1, len(signer.preimage), "the single Address auth entry was signed")
}

// ----------- Simulate error surfaces ErrSimulationFailed ----------------

func TestInvokeAndConfirm_SimulateError(t *testing.T) {
	rpc := &fakeSimulator{err: errors.New("simulate exploded")}
	c, srcKP := newInvokeAndConfirmClient(t, rpc)

	_, _, err := InvokeAndConfirm(context.Background(), c, "bump", map[string]any{"amount": uint32(7)}, newRecordingSigner(t, srcKP.Address()))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSimulationFailed), "want ErrSimulationFailed, got %v", err)
}

// ----------- Send error surfaces ErrSubmissionFailed --------------------

func TestInvokeAndConfirm_SendError(t *testing.T) {
	rpc := &fakeSimulator{}
	c, srcKP := newInvokeAndConfirmClient(t, rpc)
	rpc.resp = writeCallSimResp(t, srcKP.Address())
	rpc.sendErr = errors.New("send exploded")

	_, _, err := InvokeAndConfirm(context.Background(), c, "bump", map[string]any{"amount": uint32(7)}, newRecordingSigner(t, srcKP.Address()))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSubmissionFailed), "want ErrSubmissionFailed, got %v", err)
}

// ----------- Wait FAILED surfaces ErrTransactionFailed ------------------

func TestInvokeAndConfirm_WaitError(t *testing.T) {
	rpc := &fakeSimulator{}
	c, srcKP := newInvokeAndConfirmClient(t, rpc)
	rpc.resp = writeCallSimResp(t, srcKP.Address())

	wantHash := strings.Repeat("cd", 32)
	rpc.sendResp = protocol.SendTransactionResponse{
		Status: stellarcore.TXStatusPending,
		Hash:   wantHash,
	}
	rpc.getResp = protocol.GetTransactionResponse{
		TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusFailed},
	}

	_, hash, err := InvokeAndConfirm(context.Background(), c, "bump", map[string]any{"amount": uint32(7)}, newRecordingSigner(t, srcKP.Address()))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTransactionFailed), "want ErrTransactionFailed, got %v", err)
	// Hash is still reported so callers can correlate the failed submission.
	assert.Equal(t, wantHash, hash.HexString())
}

// ----------- Missing signer rejected before any RPC ---------------------

func TestInvokeAndConfirm_NilSignerRejected(t *testing.T) {
	rpc := &fakeSimulator{}
	c, _ := newInvokeAndConfirmClient(t, rpc)

	_, _, err := InvokeAndConfirm(context.Background(), c, "bump", map[string]any{"amount": uint32(7)}, nil)
	require.Error(t, err)
	var ce *Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, KindInvalidArgs, ce.Kind)
	assert.Equal(t, 0, rpc.calls, "nil-signer guard must fire before Simulate")
}

// ----------- Nil client rejected ----------------------------------------

func TestInvokeAndConfirm_NilClientRejected(t *testing.T) {
	_, _, err := InvokeAndConfirm(context.Background(), nil, "bump", nil, newRecordingSigner(t, keypair.MustRandom().Address()))
	require.Error(t, err)
	var ce *Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, KindInvalidArgs, ce.Kind)
}
