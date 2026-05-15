package contract

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stellar/go-stellar-sdk/network"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// simulatedAT returns an AssembledTransaction whose Simulate has been driven
// to a successful state, so it has Built, Simulation, AuthEntries, and
// ReturnValue all populated.
func simulatedAT(t *testing.T) (*AssembledTransaction, *fakeSimulator) {
	t.Helper()
	_, dataB64 := cannedSorobanData(t)
	_, authB64 := cannedAuthEntry(t)
	_, retB64 := cannedReturnValue(t)
	rpc := &fakeSimulator{
		resp: protocol.SimulateTransactionResponse{
			TransactionDataXDR: dataB64,
			MinResourceFee:     1_000_000,
			Results: []protocol.SimulateHostFunctionResult{
				{
					AuthXDR:        &[]string{authB64},
					ReturnValueXDR: &retB64,
				},
			},
		},
	}
	at, err := NewAssembledTransaction(newAssembleParams(t, rpc))
	require.NoError(t, err)
	require.NoError(t, at.Simulate(context.Background()))
	return at, rpc
}

func TestAssembledTransaction_ToXDR_RequiresSimulate(t *testing.T) {
	rpc := &fakeSimulator{}
	at, err := NewAssembledTransaction(newAssembleParams(t, rpc))
	require.NoError(t, err)

	_, err = at.ToXDR()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotYetSimulated))

	_, err = at.ToJSON()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotYetSimulated))
}

func TestAssembledTransaction_ToJSON_ShapeMatchesJSSDK(t *testing.T) {
	at, _ := simulatedAT(t)

	raw, err := at.ToJSON()
	require.NoError(t, err)

	// Decode to a generic map so we assert on the literal field names.
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))

	// Top-level keys must match the JS SDK shape verbatim.
	for _, k := range []string{"method", "tx", "simulationResult", "simulationTransactionData"} {
		_, ok := got[k]
		assert.True(t, ok, "missing top-level key %q", k)
	}

	assert.Equal(t, "bump", got["method"])
	assert.NotEmpty(t, got["tx"])
	assert.NotEmpty(t, got["simulationTransactionData"])

	sim, ok := got["simulationResult"].(map[string]any)
	require.True(t, ok)
	for _, k := range []string{"auth", "retval"} {
		_, ok := sim[k]
		assert.True(t, ok, "missing simulationResult.%s", k)
	}
}

func TestAssembledTransaction_ToFromJSON_RoundTrip(t *testing.T) {
	at, _ := simulatedAT(t)

	payload, err := at.ToJSON()
	require.NoError(t, err)

	rpc := &fakeSimulator{}
	rehydrated, err := FromJSON(context.Background(), rpc, payload,
		WithNetworkPassphrase(network.TestNetworkPassphrase),
	)
	require.NoError(t, err)
	assertATEquivalent(t, at, rehydrated, rpc)
}

func TestAssembledTransaction_ToFromXDR_RoundTrip(t *testing.T) {
	at, _ := simulatedAT(t)

	encoded, err := at.ToXDR()
	require.NoError(t, err)
	require.NotEmpty(t, encoded)

	rpc := &fakeSimulator{}
	rehydrated, err := FromXDR(context.Background(), rpc, encoded,
		WithNetworkPassphrase(network.TestNetworkPassphrase),
	)
	require.NoError(t, err)
	assertATEquivalent(t, at, rehydrated, rpc)
}

// assertATEquivalent checks the rehydrated AT carries the same observable
// state as the original (envelope hash, method, args, auth entries, return
// value, soroban data) and is wired up with the supplied rpc + network.
func assertATEquivalent(t *testing.T, want, got *AssembledTransaction, wantRPC rpcSimulator) {
	t.Helper()

	assert.Equal(t, want.Method, got.Method)
	require.Len(t, got.Args, len(want.Args))
	assert.Equal(t, want.Args[0].Type, got.Args[0].Type)
	assert.Equal(t, *want.Args[0].U32, *got.Args[0].U32)

	// Built envelope: compare base64. Sequence + fee + soroban data must
	// match the original byte-for-byte.
	wantB64, err := want.Built.Base64()
	require.NoError(t, err)
	gotB64, err := got.Built.Base64()
	require.NoError(t, err)
	assert.Equal(t, wantB64, gotB64)

	// AuthEntries — exact same XDR.
	require.Len(t, got.AuthEntries, len(want.AuthEntries))
	for i := range want.AuthEntries {
		a, err := xdr.MarshalBase64(want.AuthEntries[i])
		require.NoError(t, err)
		b, err := xdr.MarshalBase64(got.AuthEntries[i])
		require.NoError(t, err)
		assert.Equal(t, a, b, "auth entry %d differs", i)
	}

	// ReturnValue.
	require.NotNil(t, got.ReturnValue)
	require.NotNil(t, want.ReturnValue)
	assert.Equal(t, want.ReturnValue.Type, got.ReturnValue.Type)
	assert.Equal(t, *want.ReturnValue.U32, *got.ReturnValue.U32)

	// SorobanData on the op survived the round-trip.
	require.NotNil(t, got.op)
	require.NotNil(t, got.op.Ext.SorobanData)
	require.NotNil(t, want.op.Ext.SorobanData)
	assert.Equal(t, want.op.Ext.SorobanData.ResourceFee, got.op.Ext.SorobanData.ResourceFee)
	assert.Equal(t, want.op.Ext.SorobanData.Resources.Instructions, got.op.Ext.SorobanData.Resources.Instructions)

	// Wiring: rpc + network + multiplier.
	assert.Same(t, wantRPC, got.rpc.(*fakeSimulator))
	assert.Equal(t, network.TestNetworkPassphrase, got.network)
	assert.Equal(t, DefaultResourceFeeMultiplier, got.resourceFeeMultiplier)

	// Simulation snapshot retained so toBlob still works on the rehydrated AT.
	require.NotNil(t, got.Simulation)
	assert.NotEmpty(t, got.Simulation.TransactionDataXDR)
}

func TestFromXDR_RejectsMissingDeps(t *testing.T) {
	at, _ := simulatedAT(t)
	encoded, err := at.ToXDR()
	require.NoError(t, err)

	// nil rpc.
	_, err = FromXDR(context.Background(), nil, encoded,
		WithNetworkPassphrase(network.TestNetworkPassphrase),
	)
	require.Error(t, err)
	var e *Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, KindInvalidArgs, e.Kind)

	// missing network passphrase.
	_, err = FromXDR(context.Background(), &fakeSimulator{}, encoded)
	require.Error(t, err)
	require.True(t, errors.As(err, &e))
	assert.Equal(t, KindInvalidArgs, e.Kind)
	assert.Contains(t, e.Error(), "NetworkPassphrase")
}

func TestFromJSON_RejectsMissingDeps(t *testing.T) {
	at, _ := simulatedAT(t)
	payload, err := at.ToJSON()
	require.NoError(t, err)

	_, err = FromJSON(context.Background(), nil, payload,
		WithNetworkPassphrase(network.TestNetworkPassphrase),
	)
	require.Error(t, err)
	var e *Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, KindInvalidArgs, e.Kind)

	_, err = FromJSON(context.Background(), &fakeSimulator{}, payload)
	require.Error(t, err)
	require.True(t, errors.As(err, &e))
	assert.Equal(t, KindInvalidArgs, e.Kind)
}

func TestFromXDR_RejectsTamperedPayload(t *testing.T) {
	// Not base64 at all.
	_, err := FromXDR(context.Background(), &fakeSimulator{}, "!!not base64!!",
		WithNetworkPassphrase(network.TestNetworkPassphrase),
	)
	require.Error(t, err)
	var e *Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, KindInvalidArgs, e.Kind)

	// Valid base64, not valid JSON.
	_, err = FromXDR(context.Background(), &fakeSimulator{},
		base64.StdEncoding.EncodeToString([]byte("not json")),
		WithNetworkPassphrase(network.TestNetworkPassphrase),
	)
	require.Error(t, err)
	require.True(t, errors.As(err, &e))
	assert.Equal(t, KindInvalidArgs, e.Kind)

	// Valid JSON, garbage tx.
	bad, err := json.Marshal(assembledTxBlob{
		Method: "bump",
		Tx:     "!!not-xdr!!",
		SimulationResult: simulationResultB64{
			Auth: []string{}, Retval: "",
		},
		SimulationTransactionData: "",
	})
	require.NoError(t, err)
	_, err = FromXDR(context.Background(), &fakeSimulator{},
		base64.StdEncoding.EncodeToString(bad),
		WithNetworkPassphrase(network.TestNetworkPassphrase),
	)
	require.Error(t, err)
	require.True(t, errors.As(err, &e))
	assert.Equal(t, KindInvalidArgs, e.Kind)
}

func TestFromJSON_RejectsMethodMismatch(t *testing.T) {
	at, _ := simulatedAT(t)
	payload, err := at.ToJSON()
	require.NoError(t, err)

	var blob assembledTxBlob
	require.NoError(t, json.Unmarshal(payload, &blob))
	blob.Method = "not_bump"
	tampered, err := json.Marshal(blob)
	require.NoError(t, err)

	_, err = FromJSON(context.Background(), &fakeSimulator{}, tampered,
		WithNetworkPassphrase(network.TestNetworkPassphrase),
	)
	require.Error(t, err)
	var e *Error
	require.True(t, errors.As(err, &e))
	assert.Equal(t, KindInvalidArgs, e.Kind)
	assert.Contains(t, e.Error(), "method mismatch")
}

func TestFromJSON_UsesSpecRegistry(t *testing.T) {
	at, _ := simulatedAT(t)
	payload, err := at.ToJSON()
	require.NoError(t, err)

	// The host function the test fixture builds has contract id = [1..32].
	// Register a sentinel spec under that id; rehydration should pick it up.
	cid := contractIDFromOp(at.op)
	require.NotEmpty(t, cid)
	sentinel := &Spec{}
	RegisterSpec(cid, sentinel)
	t.Cleanup(func() {
		// Best-effort cleanup: overwrite with nil so subsequent tests don't
		// observe the registration. (No deregister API exists today.)
		RegisterSpec(cid, nil)
	})

	rehydrated, err := FromJSON(context.Background(), &fakeSimulator{}, payload,
		WithNetworkPassphrase(network.TestNetworkPassphrase),
	)
	require.NoError(t, err)
	assert.Same(t, sentinel, rehydrated.spec)
}

func TestWithSpecOverridesRegistry(t *testing.T) {
	at, _ := simulatedAT(t)
	payload, err := at.ToJSON()
	require.NoError(t, err)

	override := &Spec{}
	rehydrated, err := FromJSON(context.Background(), &fakeSimulator{}, payload,
		WithNetworkPassphrase(network.TestNetworkPassphrase),
		WithSpec(override),
	)
	require.NoError(t, err)
	assert.Same(t, override, rehydrated.spec)
}
