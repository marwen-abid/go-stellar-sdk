package contract

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/protocols/stellarcore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// primeSimulated drives Simulate on a fresh AssembledTransaction so Send
// tests start from a post-simulation state. When skipSign is false the
// envelope is also signed by a fresh random keypair (Sign appends a sig
// regardless of whether the signer matches the source account).
func primeSimulated(t *testing.T, rpc *fakeSimulator, skipSign bool) *AssembledTransaction {
	t.Helper()
	_, dataB64 := cannedSorobanData(t)
	_, authB64 := cannedAuthEntry(t)
	_, retB64 := cannedReturnValue(t)
	rpc.resp = protocol.SimulateTransactionResponse{
		TransactionDataXDR: dataB64,
		MinResourceFee:     1_000,
		Results: []protocol.SimulateHostFunctionResult{
			{AuthXDR: &[]string{authB64}, ReturnValueXDR: &retB64},
		},
	}

	at, err := NewAssembledTransaction(newAssembleParams(t, rpc))
	require.NoError(t, err)
	require.NoError(t, at.Simulate(context.Background()))

	if !skipSign {
		require.NoError(t, at.Sign(KeypairSigner(keypair.MustRandom())))
	}
	return at
}

// happy path: PENDING --------------------------------------------------

func TestAssembledTransaction_Send_PendingHappyPath(t *testing.T) {
	rpc := &fakeSimulator{}
	at := primeSimulated(t, rpc, false)

	wantHash := strings.Repeat("ab", 32) // 64 hex chars
	rpc.sendResp = protocol.SendTransactionResponse{
		Status:       stellarcore.TXStatusPending,
		Hash:         wantHash,
		LatestLedger: 42,
	}

	sent, err := at.Send(context.Background())
	require.NoError(t, err)
	require.NotNil(t, sent)
	assert.Equal(t, 1, rpc.sendCalls)
	assert.NotEmpty(t, rpc.gotSendReq.Transaction)
	assert.Equal(t, wantHash, sent.Hash.HexString())
	require.NotNil(t, sent.SendResponse)
	assert.Equal(t, stellarcore.TXStatusPending, sent.SendResponse.Status)

	// Hash was cached on the AT.
	assert.Same(t, sent, at.sent)
}

func TestAssembledTransaction_Send_DuplicateIsSuccess(t *testing.T) {
	rpc := &fakeSimulator{}
	at := primeSimulated(t, rpc, false)

	rpc.sendResp = protocol.SendTransactionResponse{
		Status: stellarcore.TXStatusDuplicate,
		Hash:   strings.Repeat("cd", 32),
	}

	sent, err := at.Send(context.Background())
	require.NoError(t, err)
	require.NotNil(t, sent)
	assert.Equal(t, stellarcore.TXStatusDuplicate, sent.SendResponse.Status)
}

// error mappings ------------------------------------------------------

func TestAssembledTransaction_Send_ErrorStatusWrapsSentinel(t *testing.T) {
	rpc := &fakeSimulator{}
	at := primeSimulated(t, rpc, false)

	rpc.sendResp = protocol.SendTransactionResponse{Status: stellarcore.TXStatusError}

	_, err := at.Send(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSubmissionFailed), "want ErrSubmissionFailed, got %v", err)
}

func TestAssembledTransaction_Send_TryAgainLaterWrapsSentinel(t *testing.T) {
	rpc := &fakeSimulator{}
	at := primeSimulated(t, rpc, false)

	rpc.sendResp = protocol.SendTransactionResponse{Status: stellarcore.TXStatusTryAgainLater}

	_, err := at.Send(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSubmissionFailed))
}

func TestAssembledTransaction_Send_TransportErrorWrapsSentinel(t *testing.T) {
	rpc := &fakeSimulator{}
	at := primeSimulated(t, rpc, false)

	rpcErr := errors.New("connection refused")
	rpc.sendErr = rpcErr

	_, err := at.Send(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSubmissionFailed))
	assert.ErrorIs(t, err, rpcErr)
}

func TestAssembledTransaction_Send_UnrecognizedStatus(t *testing.T) {
	rpc := &fakeSimulator{}
	at := primeSimulated(t, rpc, false)

	rpc.sendResp = protocol.SendTransactionResponse{Status: "MYSTERY"}

	_, err := at.Send(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSubmissionFailed))
}

func TestAssembledTransaction_Send_BadHashReturnsSubmissionFailed(t *testing.T) {
	rpc := &fakeSimulator{}
	at := primeSimulated(t, rpc, false)

	rpc.sendResp = protocol.SendTransactionResponse{
		Status: stellarcore.TXStatusPending,
		Hash:   "not-a-valid-hash",
	}

	_, err := at.Send(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSubmissionFailed))
}

// preconditions -------------------------------------------------------

func TestAssembledTransaction_Send_RequiresSimulate(t *testing.T) {
	rpc := &fakeSimulator{}
	at, err := NewAssembledTransaction(newAssembleParams(t, rpc))
	require.NoError(t, err)

	_, err = at.Send(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotYetSimulated))
	assert.Equal(t, 0, rpc.sendCalls)
}

func TestAssembledTransaction_Send_RequiresSignedEnvelope(t *testing.T) {
	rpc := &fakeSimulator{}
	at := primeSimulated(t, rpc, true) // skipSign: do NOT sign

	_, err := at.Send(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSubmissionFailed))
	assert.Contains(t, err.Error(), "unsigned")
	assert.Equal(t, 0, rpc.sendCalls)
}

// idempotency ---------------------------------------------------------

func TestAssembledTransaction_Send_IsIdempotent(t *testing.T) {
	rpc := &fakeSimulator{}
	at := primeSimulated(t, rpc, false)

	rpc.sendResp = protocol.SendTransactionResponse{
		Status: stellarcore.TXStatusPending,
		Hash:   strings.Repeat("ef", 32),
	}

	first, err := at.Send(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, rpc.sendCalls)

	second, err := at.Send(context.Background())
	require.NoError(t, err)
	assert.Same(t, first, second, "second Send should return the cached *SentTransaction")
	assert.Equal(t, 1, rpc.sendCalls, "RPC should be called only once across two Sends")
}
