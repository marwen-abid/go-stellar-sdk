package contract

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSentTx builds a *SentTransaction wired to the given fakeSimulator
// without exercising the full Send path; the tests in this file isolate the
// polling logic, which doesn't depend on a real envelope or simulation.
func newSentTx(t *testing.T, rpc rpcSimulator) *SentTransaction {
	t.Helper()
	var hash xdr.Hash
	for i := range hash {
		hash[i] = byte(i + 1)
	}
	return &SentTransaction{
		Hash:         hash,
		SendResponse: &protocol.SendTransactionResponse{},
		rpc:          rpc,
		method:       "bump",
	}
}

// Wait happy path: NOT_FOUND, NOT_FOUND, SUCCESS -> returns final response.
func TestSentTransaction_Wait_SuccessAfterPolls(t *testing.T) {
	rpc := &fakeSimulator{
		getRespSeq: []protocol.GetTransactionResponse{
			{TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusNotFound}},
			{TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusNotFound}},
			{TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusSuccess, Ledger: 99}},
		},
	}
	sent := newSentTx(t, rpc)

	resp, err := sent.Wait(context.Background(), PollInterval(1*time.Millisecond), PollBackoff(1.0), PollTimeout(5*time.Second))
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, protocol.TransactionStatusSuccess, resp.Status)
	assert.Equal(t, uint32(99), resp.Ledger)
	assert.Equal(t, 3, rpc.getCalls)
	assert.Equal(t, sent.Hash.HexString(), rpc.gotGetReq.Hash)
}

// Wait FAILED path: returns the response and wraps ErrTransactionFailed.
func TestSentTransaction_Wait_FailedWrapsSentinel(t *testing.T) {
	rpc := &fakeSimulator{
		getResp: protocol.GetTransactionResponse{
			TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusFailed, Ledger: 7},
		},
	}
	sent := newSentTx(t, rpc)

	resp, err := sent.Wait(context.Background(), PollInterval(1*time.Millisecond))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTransactionFailed), "want ErrTransactionFailed, got %v", err)
	// Wait still surfaces the response so callers can inspect diagnostics.
	require.NotNil(t, resp)
	assert.Equal(t, uint32(7), resp.Ledger)
}

// Wait timeout via PollTimeout while still NOT_FOUND.
func TestSentTransaction_Wait_TimeoutWrapsSentinel(t *testing.T) {
	rpc := &fakeSimulator{
		getResp: protocol.GetTransactionResponse{
			TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusNotFound},
		},
	}
	sent := newSentTx(t, rpc)

	_, err := sent.Wait(context.Background(), PollInterval(1*time.Millisecond), PollTimeout(15*time.Millisecond))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTimeout), "want ErrTimeout, got %v", err)
	assert.GreaterOrEqual(t, rpc.getCalls, 1)
}

// Wait honors ctx cancellation: returns ErrTimeout wrapping context.Canceled.
func TestSentTransaction_Wait_ContextCancel(t *testing.T) {
	rpc := &fakeSimulator{
		getResp: protocol.GetTransactionResponse{
			TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusNotFound},
		},
	}
	sent := newSentTx(t, rpc)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel from the hook after the first call so the sleep is interrupted.
	rpc.getHookFunc = func(_ context.Context, _ protocol.GetTransactionRequest, idx int) (protocol.GetTransactionResponse, error) {
		if idx == 0 {
			go cancel()
		}
		return protocol.GetTransactionResponse{
			TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusNotFound},
		}, nil
	}

	_, err := sent.Wait(ctx, PollInterval(50*time.Millisecond), PollTimeout(5*time.Second))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTimeout))
}

// Wait propagates an RPC transport error as ErrSubmissionFailed.
func TestSentTransaction_Wait_TransportError(t *testing.T) {
	rpcErr := errors.New("connection refused")
	rpc := &fakeSimulator{getErr: rpcErr}
	sent := newSentTx(t, rpc)

	_, err := sent.Wait(context.Background(), PollInterval(1*time.Millisecond), PollTimeout(50*time.Millisecond))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSubmissionFailed), "want ErrSubmissionFailed, got %v", err)
	assert.ErrorIs(t, err, rpcErr)
}

// Wait rejects an unrecognized status.
func TestSentTransaction_Wait_UnrecognizedStatus(t *testing.T) {
	rpc := &fakeSimulator{
		getResp: protocol.GetTransactionResponse{
			TransactionDetails: protocol.TransactionDetails{Status: "MYSTERY"},
		},
	}
	sent := newSentTx(t, rpc)

	_, err := sent.Wait(context.Background(), PollInterval(1*time.Millisecond))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSubmissionFailed))
	assert.Contains(t, err.Error(), "MYSTERY")
}

// Wait rejects calls on a zero-valued SentTransaction.
func TestSentTransaction_Wait_RequiresRPC(t *testing.T) {
	var s SentTransaction
	_, err := s.Wait(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, &Error{Kind: KindInvalidArgs}))
}

// Status returns the raw status string for a single getTransaction call.
func TestSentTransaction_Status(t *testing.T) {
	rpc := &fakeSimulator{
		getResp: protocol.GetTransactionResponse{
			TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusNotFound},
		},
	}
	sent := newSentTx(t, rpc)

	status, err := sent.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, protocol.TransactionStatusNotFound, status)
	assert.Equal(t, 1, rpc.getCalls)
}

func TestSentTransaction_Status_TransportError(t *testing.T) {
	rpcErr := errors.New("boom")
	rpc := &fakeSimulator{getErr: rpcErr}
	sent := newSentTx(t, rpc)

	_, err := sent.Status(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSubmissionFailed))
	assert.ErrorIs(t, err, rpcErr)
}

// Watch happy path: emits NOT_FOUND then SUCCESS and closes the channel.
func TestSentTransaction_Watch_Success(t *testing.T) {
	rpc := &fakeSimulator{
		getRespSeq: []protocol.GetTransactionResponse{
			{TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusNotFound}},
			{TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusSuccess, Ledger: 11}},
		},
	}
	sent := newSentTx(t, rpc)

	ch := sent.Watch(context.Background(), PollInterval(1*time.Millisecond), PollBackoff(1.0), PollTimeout(2*time.Second))

	statuses := drainStatusUpdates(t, ch, 2*time.Second)
	// The drop-oldest emit semantics mean we might see 1 or 2 entries; the
	// last one must be SUCCESS with nil Err.
	require.NotEmpty(t, statuses)
	terminal := statuses[len(statuses)-1]
	assert.Equal(t, protocol.TransactionStatusSuccess, terminal.Status)
	assert.NoError(t, terminal.Err)
	require.NotNil(t, terminal.Response)
	assert.Equal(t, uint32(11), terminal.Response.Ledger)
}

// Watch FAILED: terminal update carries the wrapped sentinel.
func TestSentTransaction_Watch_Failed(t *testing.T) {
	rpc := &fakeSimulator{
		getResp: protocol.GetTransactionResponse{
			TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusFailed},
		},
	}
	sent := newSentTx(t, rpc)

	ch := sent.Watch(context.Background(), PollInterval(1*time.Millisecond))
	statuses := drainStatusUpdates(t, ch, 2*time.Second)
	require.NotEmpty(t, statuses)
	terminal := statuses[len(statuses)-1]
	assert.Equal(t, protocol.TransactionStatusFailed, terminal.Status)
	require.Error(t, terminal.Err)
	assert.True(t, errors.Is(terminal.Err, ErrTransactionFailed))
}

// Watch timeout: terminal update wraps ErrTimeout.
func TestSentTransaction_Watch_Timeout(t *testing.T) {
	rpc := &fakeSimulator{
		getResp: protocol.GetTransactionResponse{
			TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusNotFound},
		},
	}
	sent := newSentTx(t, rpc)

	ch := sent.Watch(context.Background(), PollInterval(1*time.Millisecond), PollTimeout(15*time.Millisecond))
	statuses := drainStatusUpdates(t, ch, 2*time.Second)
	require.NotEmpty(t, statuses)
	terminal := statuses[len(statuses)-1]
	require.Error(t, terminal.Err)
	assert.True(t, errors.Is(terminal.Err, ErrTimeout))
}

// drainStatusUpdates collects StatusUpdates from ch until it is closed or the
// timeout elapses; fails the test on timeout.
func drainStatusUpdates(t *testing.T, ch <-chan StatusUpdate, timeout time.Duration) []StatusUpdate {
	t.Helper()
	var out []StatusUpdate
	deadline := time.After(timeout)
	for {
		select {
		case u, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, u)
		case <-deadline:
			t.Fatalf("Watch did not close within %s; collected %d updates", timeout, len(out))
			return out
		}
	}
}

// Compile-time sanity: ensure hash hex form is the lowercase 64 chars the
// RPC expects (defends against accidental upper-case formatting if HexString
// is ever changed).
func TestSentTransaction_HashHex(t *testing.T) {
	s := newSentTx(t, &fakeSimulator{})
	h := s.Hash.HexString()
	require.Len(t, h, 64)
	assert.Equal(t, h, strings.ToLower(h))
}
