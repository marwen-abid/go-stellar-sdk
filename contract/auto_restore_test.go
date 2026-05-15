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

// autoRestoreSim is a scripted rpcSimulator that records the order of
// Simulate / Send / GetTransaction calls so tests can assert the
// auto-restore loop drives Sim -> Send -> Get* -> Sim. Each side has its
// own response queue; once exhausted, the trailing entry is reused. Errors
// are queued in parallel slices.
type autoRestoreSim struct {
	simResps []protocol.SimulateTransactionResponse
	simErrs  []error
	simCalls int

	sendResp protocol.SendTransactionResponse
	sendErr  error
	sendCall int

	getResp protocol.GetTransactionResponse
	getErr  error
	getCall int

	order []string
}

func (s *autoRestoreSim) SimulateTransaction(_ context.Context, _ protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error) {
	idx := s.simCalls
	s.simCalls++
	s.order = append(s.order, "sim")
	if idx >= len(s.simResps) {
		idx = len(s.simResps) - 1
	}
	var err error
	if idx < len(s.simErrs) {
		err = s.simErrs[idx]
	}
	return s.simResps[idx], err
}

func (s *autoRestoreSim) SendTransaction(_ context.Context, _ protocol.SendTransactionRequest) (protocol.SendTransactionResponse, error) {
	s.sendCall++
	s.order = append(s.order, "send")
	return s.sendResp, s.sendErr
}

func (s *autoRestoreSim) GetTransaction(_ context.Context, _ protocol.GetTransactionRequest) (protocol.GetTransactionResponse, error) {
	s.getCall++
	s.order = append(s.order, "get")
	return s.getResp, s.getErr
}

// happyPathSim returns a simulator preloaded with: first Simulate yields a
// RestorePreamble, the restore tx sends PENDING then GetTransaction returns
// SUCCESS, second Simulate returns the canned good response.
func happyPathSim(t *testing.T) (*autoRestoreSim, *protocol.RestorePreamble) {
	t.Helper()
	_, preamble := cannedRestorePreamble(t)
	_, dataB64 := cannedSorobanData(t)
	_, authB64 := cannedAuthEntry(t)
	_, retB64 := cannedReturnValue(t)

	return &autoRestoreSim{
		simResps: []protocol.SimulateTransactionResponse{
			{RestorePreamble: preamble},
			{
				TransactionDataXDR: dataB64,
				MinResourceFee:     1_000,
				Results: []protocol.SimulateHostFunctionResult{
					{AuthXDR: &[]string{authB64}, ReturnValueXDR: &retB64},
				},
			},
		},
		sendResp: protocol.SendTransactionResponse{
			Status: stellarcore.TXStatusPending,
			Hash:   strings.Repeat("ab", 32),
		},
		getResp: protocol.GetTransactionResponse{
			TransactionDetails: protocol.TransactionDetails{
				Status: protocol.TransactionStatusSuccess,
			},
		},
	}, preamble
}

func newATForAutoRestore(t *testing.T, rpc rpcSimulator, signer Signer, enable bool) *AssembledTransaction {
	t.Helper()
	at, err := NewAssembledTransaction(newAssembleParams(t, rpc))
	require.NoError(t, err)
	at.restoreEnabled = enable
	at.signer = signer
	return at
}

func TestSimulate_AutoRestore_HappyPath(t *testing.T) {
	sim, _ := happyPathSim(t)
	at := newATForAutoRestore(t, sim, KeypairSigner(keypair.MustRandom()), true)

	err := at.Simulate(context.Background())
	require.NoError(t, err)

	// Sim -> Send -> Get -> Sim.
	require.Equal(t, []string{"sim", "send", "get", "sim"}, sim.order)
	assert.Equal(t, 2, sim.simCalls)
	assert.Equal(t, 1, sim.sendCall)
	assert.Equal(t, 1, sim.getCall)

	// AT state reflects the *second* simulation (the original invocation).
	require.NotNil(t, at.Simulation)
	require.NotNil(t, at.ReturnValue)
	assert.Nil(t, at.RestorePreamble, "preamble should be cleared after successful re-simulate")
}

func TestSimulate_AutoRestore_DisabledSurfacesError(t *testing.T) {
	sim, _ := happyPathSim(t)
	at := newATForAutoRestore(t, sim, KeypairSigner(keypair.MustRandom()), false)

	err := at.Simulate(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRestoreRequired))
	assert.Equal(t, 1, sim.simCalls, "no re-simulation when Restore=false")
	assert.Equal(t, 0, sim.sendCall)
	assert.Equal(t, 0, sim.getCall)
}

func TestSimulate_AutoRestore_NoSignerSurfacesError(t *testing.T) {
	sim, _ := happyPathSim(t)
	at := newATForAutoRestore(t, sim, nil, true)

	err := at.Simulate(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRestoreRequired))
	assert.Equal(t, 1, sim.simCalls)
	assert.Equal(t, 0, sim.sendCall, "no send when no signer is configured")
}

func TestSimulate_AutoRestore_SendFailureWrapped(t *testing.T) {
	sim, _ := happyPathSim(t)
	sim.sendResp = protocol.SendTransactionResponse{Status: stellarcore.TXStatusError}
	at := newATForAutoRestore(t, sim, KeypairSigner(keypair.MustRandom()), true)

	err := at.Simulate(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSubmissionFailed))
	assert.Equal(t, 1, sim.simCalls)
	assert.Equal(t, 1, sim.sendCall)
	assert.Equal(t, 0, sim.getCall)
}

func TestSimulate_AutoRestore_LoopSentinel(t *testing.T) {
	_, preamble := cannedRestorePreamble(t)
	sim := &autoRestoreSim{
		simResps: []protocol.SimulateTransactionResponse{
			{RestorePreamble: preamble},
			{RestorePreamble: preamble}, // re-simulate STILL reports archived.
		},
		sendResp: protocol.SendTransactionResponse{
			Status: stellarcore.TXStatusPending,
			Hash:   strings.Repeat("cd", 32),
		},
		getResp: protocol.GetTransactionResponse{
			TransactionDetails: protocol.TransactionDetails{
				Status: protocol.TransactionStatusSuccess,
			},
		},
	}
	at := newATForAutoRestore(t, sim, KeypairSigner(keypair.MustRandom()), true)

	err := at.Simulate(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRestoreRequired))
	var e *Error
	require.True(t, errors.As(err, &e))
	assert.Contains(t, e.Details, "loop detected")
	assert.Equal(t, 2, sim.simCalls, "auto-restore retries at most once")
}
