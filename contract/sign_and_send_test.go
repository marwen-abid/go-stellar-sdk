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

// ----------- Helpers -----------------------------------------------------

// scValU64 builds an ScVal carrying a uint64 (ScvU64).
func scValU64(v uint64) xdr.ScVal {
	x := xdr.Uint64(v)
	return xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &x}
}

// scSpecTypeU64 helper for spec output types.
func scSpecTypeU64() xdr.ScSpecTypeDef {
	return xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeU64}
}

// buildBumpSpec returns a *Spec declaring `bump(amount: u32) -> u64` to
// match newTestInvokeOp.
func buildBumpSpec(t *testing.T) *Spec {
	t.Helper()
	out := scSpecTypeU64()
	return NewSpecFromEntries([]xdr.ScSpecEntry{
		fnEntryWithSig(t, "bump", []xdr.ScSpecFunctionInputV0{
			input("amount", xdr.ScSpecTypeScSpecTypeU32),
		}, &out),
	})
}

// buildReadWriteResultMeta packages a return-value ScVal into a base64
// TransactionMetaV3 (SorobanMeta.ReturnValue) that mimics a real Soroban
// success response.
func buildReadWriteResultMeta(t *testing.T, ret xdr.ScVal) string {
	t.Helper()
	meta := xdr.TransactionMeta{
		V: 3,
		V3: &xdr.TransactionMetaV3{
			SorobanMeta: &xdr.SorobanTransactionMeta{
				ReturnValue: ret,
			},
		},
	}
	b64, err := xdr.MarshalBase64(meta)
	require.NoError(t, err)
	return b64
}

// readCallSetup primes an AT in IsReadCall=true state with a simulated u64
// return value of `simReturn` and a bound Spec.
func readCallSetup(t *testing.T, simReturn xdr.ScVal) (*AssembledTransaction, *fakeSimulator) {
	t.Helper()
	rpc := &fakeSimulator{}
	srcKP := keypair.MustRandom()
	acct := txnbuild.NewSimpleAccount(srcKP.Address(), 42)
	op := newTestInvokeOp(t, srcKP.Address())

	at, err := NewAssembledTransaction(AssembleParams{
		RPC:               rpc,
		NetworkPassphrase: network.TestNetworkPassphrase,
		BaseFee:           txnbuild.MinBaseFee,
		SourceAccount:     &acct,
		Op:                op,
		Spec:              buildBumpSpec(t),
		Preconditions:     txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
	})
	require.NoError(t, err)

	// Empty Auth + empty ReadWrite footprint => IsReadCall() is true.
	op.Auth = nil
	op.Ext = xdr.TransactionExt{
		V: 1,
		SorobanData: &xdr.SorobanTransactionData{
			Resources: xdr.SorobanResources{
				Footprint: xdr.LedgerFootprint{}, // empty RW
			},
		},
	}
	rebuilt, err := buildTx(&acct, op, txnbuild.MinBaseFee, nil, txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()})
	require.NoError(t, err)
	at.Built = rebuilt
	at.AuthEntries = nil
	at.Simulation = &protocol.SimulateTransactionResponse{}
	ret := simReturn
	at.ReturnValue = &ret
	return at, rpc
}

// writeCallSetup primes an AT with an Address-credentialed auth entry that
// belongs to the source account (so SignAuthEntries signs it and
// NeedsNonInvokerSigningBy returns empty afterwards). Spec is bound so
// Result can decode native u64.
func writeCallSetup(t *testing.T) (*AssembledTransaction, *fakeSimulator, *keypair.Full) {
	t.Helper()
	rpc := &fakeSimulator{}
	srcKP := keypair.MustRandom()
	acct := txnbuild.NewSimpleAccount(srcKP.Address(), 42)
	op := newTestInvokeOp(t, srcKP.Address())

	at, err := NewAssembledTransaction(AssembleParams{
		RPC:               rpc,
		NetworkPassphrase: network.TestNetworkPassphrase,
		BaseFee:           txnbuild.MinBaseFee,
		SourceAccount:     &acct,
		Op:                op,
		Spec:              buildBumpSpec(t),
		Preconditions:     txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
	})
	require.NoError(t, err)

	// Single Address auth entry credentialed by the source account (so the
	// invoker signer will both sign the envelope AND fill in the auth-entry
	// signature; NeedsNonInvokerSigningBy afterwards returns empty).
	entries := []xdr.SorobanAuthorizationEntry{addressEntry(t, srcKP.Address(), 1)}
	rwKey := xdr.LedgerKey{
		Type:    xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{AccountId: xdr.MustAddress(keypair.MustRandom().Address())},
	}
	op.Auth = append([]xdr.SorobanAuthorizationEntry(nil), entries...)
	op.Ext = xdr.TransactionExt{
		V: 1,
		SorobanData: &xdr.SorobanTransactionData{
			Resources: xdr.SorobanResources{
				Footprint: xdr.LedgerFootprint{ReadWrite: []xdr.LedgerKey{rwKey}},
			},
		},
	}
	rebuilt, err := buildTx(&acct, op, txnbuild.MinBaseFee, nil, txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()})
	require.NoError(t, err)
	at.Built = rebuilt
	at.AuthEntries = append([]xdr.SorobanAuthorizationEntry(nil), entries...)
	at.Simulation = &protocol.SimulateTransactionResponse{}
	// Stash a simulated value so we can prove Result() prefers the
	// post-Wait final ScVal over the simulated one.
	simReturn := scValU64(111)
	at.ReturnValue = &simReturn
	return at, rpc, srcKP
}

// ----------- SignAndSend: read-call short-circuit -----------------------

func TestSignAndSend_ReadCall_SkipsSignAndSend(t *testing.T) {
	at, rpc := readCallSetup(t, scValU64(42))
	signer := newRecordingSigner(t, keypair.MustRandom().Address())

	sent, err := at.SignAndSend(context.Background(), signer)
	require.NoError(t, err)
	require.Nil(t, sent, "read-call SignAndSend should not return a SentTransaction")

	assert.Equal(t, 0, signer.txCalls, "signer.SignTransaction must not be called for read calls")
	assert.Equal(t, 0, len(signer.preimage), "signer.SignAuthEntryPreimage must not be called for read calls")
	assert.Equal(t, 0, rpc.sendCalls, "RPC SendTransaction must not be called for read calls")
}

func TestSignAndSend_ReadCall_ResultDecodesFromSimulation(t *testing.T) {
	at, _ := readCallSetup(t, scValU64(42))
	_, err := at.SignAndSend(context.Background(), newRecordingSigner(t, keypair.MustRandom().Address()))
	require.NoError(t, err)

	got, err := at.Result()
	require.NoError(t, err)
	assert.Equal(t, uint64(42), got)
}

// ----------- SignAndSend: write-call happy path -------------------------

func TestSignAndSend_WriteCall_ChainsThroughSend(t *testing.T) {
	at, rpc, srcKP := writeCallSetup(t)
	signer := newRecordingSigner(t, srcKP.Address())

	wantHash := strings.Repeat("ab", 32)
	rpc.sendResp = protocol.SendTransactionResponse{
		Status: stellarcore.TXStatusPending,
		Hash:   wantHash,
	}

	sent, err := at.SignAndSend(context.Background(), signer)
	require.NoError(t, err)
	require.NotNil(t, sent)
	assert.Equal(t, wantHash, sent.Hash.HexString())

	// Signer was used for both the auth entry and the envelope.
	assert.Equal(t, 1, signer.txCalls, "envelope must be signed once")
	require.Len(t, signer.preimage, 1, "the single Address auth entry must be signed")
	assert.Equal(t, 1, rpc.sendCalls)
	assert.Same(t, sent, at.sent, "SentTransaction must be cached on the AT")
}

func TestSignAndSend_WriteCall_IsIdempotent(t *testing.T) {
	at, rpc, srcKP := writeCallSetup(t)
	signer := newRecordingSigner(t, srcKP.Address())

	rpc.sendResp = protocol.SendTransactionResponse{
		Status: stellarcore.TXStatusPending,
		Hash:   strings.Repeat("ab", 32),
	}

	first, err := at.SignAndSend(context.Background(), signer)
	require.NoError(t, err)
	second, err := at.SignAndSend(context.Background(), signer)
	require.NoError(t, err)
	assert.Same(t, first, second)
	assert.Equal(t, 1, rpc.sendCalls, "second SignAndSend must not re-submit")
}

// ----------- SignAndSend: missing signatures rejected -------------------

func TestSignAndSend_WriteCall_MissingNonInvokerSignatureErrors(t *testing.T) {
	at, _, srcKP := writeCallSetup(t)

	// Add a SECOND auth entry credentialed by some other address that the
	// invoker-signer cannot speak for. SignAuthEntries will skip it, then
	// NeedsNonInvokerSigningBy should flag it.
	other := keypair.MustRandom().Address()
	otherEntry := addressEntry(t, other, 99)
	at.AuthEntries = append(at.AuthEntries, otherEntry)
	at.op.Auth = append([]xdr.SorobanAuthorizationEntry(nil), at.AuthEntries...)

	signer := newRecordingSigner(t, srcKP.Address())
	sent, err := at.SignAndSend(context.Background(), signer)
	require.Error(t, err)
	assert.Nil(t, sent)
	assert.True(t, errors.Is(err, ErrNeedsMoreSignatures), "want ErrNeedsMoreSignatures, got %v", err)
}

// ----------- SignAndSend: not-simulated guard ---------------------------

func TestSignAndSend_NotSimulatedErrors(t *testing.T) {
	rpc := &fakeSimulator{}
	at, err := NewAssembledTransaction(newAssembleParams(t, rpc))
	require.NoError(t, err)
	sent, err := at.SignAndSend(context.Background(), newRecordingSigner(t, keypair.MustRandom().Address()))
	require.Error(t, err)
	assert.Nil(t, sent)
	assert.True(t, errors.Is(err, ErrNotYetSimulated))
}

// ----------- Result: before any simulation ------------------------------

func TestResult_BeforeSimulationErrors(t *testing.T) {
	rpc := &fakeSimulator{}
	at, err := NewAssembledTransaction(newAssembleParams(t, rpc))
	require.NoError(t, err)
	_, err = at.Result()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotYetSimulated))
}

// ----------- Result: write call after Wait ------------------------------

func TestResult_WriteCall_DecodesFromWaitResponse(t *testing.T) {
	at, rpc, srcKP := writeCallSetup(t)

	// Send.
	rpc.sendResp = protocol.SendTransactionResponse{
		Status: stellarcore.TXStatusPending,
		Hash:   strings.Repeat("ab", 32),
	}
	sent, err := at.SignAndSend(context.Background(), newRecordingSigner(t, srcKP.Address()))
	require.NoError(t, err)
	require.NotNil(t, sent)

	// Wait response with a u64=777 return value baked into ResultMetaXDR.
	rpc.getResp = protocol.GetTransactionResponse{
		TransactionDetails: protocol.TransactionDetails{
			Status:        protocol.TransactionStatusSuccess,
			ResultMetaXDR: buildReadWriteResultMeta(t, scValU64(777)),
		},
	}
	_, err = sent.Wait(context.Background(), PollInterval(1*time.Millisecond), PollTimeout(2*time.Second))
	require.NoError(t, err)

	// Result must decode 777, NOT the simulated 111 stashed in writeCallSetup.
	got, err := at.Result()
	require.NoError(t, err)
	assert.Equal(t, uint64(777), got)
}

// ----------- Result: write call sent but not waited ---------------------

func TestResult_WriteCall_BeforeWaitErrors(t *testing.T) {
	at, rpc, srcKP := writeCallSetup(t)
	rpc.sendResp = protocol.SendTransactionResponse{
		Status: stellarcore.TXStatusPending,
		Hash:   strings.Repeat("ab", 32),
	}
	_, err := at.SignAndSend(context.Background(), newRecordingSigner(t, srcKP.Address()))
	require.NoError(t, err)

	_, err = at.Result()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotYetSimulated))
}

// ----------- Result: marshaling — primitive, composite, UDT -------------

// TestResult_DecodesPrimitive covers a primitive (u64) read-call response.
func TestResult_DecodesPrimitive(t *testing.T) {
	at, _ := readCallSetup(t, scValU64(2026))
	got, err := at.Result()
	require.NoError(t, err)
	assert.Equal(t, uint64(2026), got)
}

// TestResult_DecodesComposite covers a Vec<u32> read-call response.
func TestResult_DecodesComposite(t *testing.T) {
	// Build a Vec<u32> return value and spec.
	a, b := xdr.Uint32(1), xdr.Uint32(2)
	vec := xdr.ScVec{
		{Type: xdr.ScValTypeScvU32, U32: &a},
		{Type: xdr.ScValTypeScvU32, U32: &b},
	}
	vecPtr := &vec
	ret := xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vecPtr}

	out := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeVec,
		Vec: &xdr.ScSpecTypeVec{
			ElementType: xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeU32},
		},
	}
	spec := NewSpecFromEntries([]xdr.ScSpecEntry{
		fnEntryWithSig(t, "bump", []xdr.ScSpecFunctionInputV0{
			input("amount", xdr.ScSpecTypeScSpecTypeU32),
		}, &out),
	})

	at, _ := readCallSetup(t, ret)
	at.spec = spec

	got, err := at.Result()
	require.NoError(t, err)
	assert.Equal(t, []any{uint32(1), uint32(2)}, got)
}

// TestResult_DecodesUDT covers a user-defined struct (Pair{x:u32, y:u32}).
func TestResult_DecodesUDT(t *testing.T) {
	// UDT spec entry: struct Pair { x: u32, y: u32 }.
	udtFields := []xdr.ScSpecUdtStructFieldV0{
		{Name: "x", Type: xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeU32}},
		{Name: "y", Type: xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeU32}},
	}
	udt, err := xdr.NewScSpecEntry(xdr.ScSpecEntryKindScSpecEntryUdtStructV0, xdr.ScSpecUdtStructV0{
		Name:   "Pair",
		Fields: udtFields,
	})
	require.NoError(t, err)

	out := xdr.ScSpecTypeDef{
		Type: xdr.ScSpecTypeScSpecTypeUdt,
		Udt:  &xdr.ScSpecTypeUdt{Name: "Pair"},
	}
	fn := fnEntryWithSig(t, "bump", []xdr.ScSpecFunctionInputV0{
		input("amount", xdr.ScSpecTypeScSpecTypeU32),
	}, &out)
	spec := NewSpecFromEntries([]xdr.ScSpecEntry{fn, udt})

	// Build the ScVal: Map{x:u32(7), y:u32(9)}.
	x, y := xdr.Uint32(7), xdr.Uint32(9)
	scMap := xdr.ScMap{
		{Key: xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: symPtr("x")}, Val: xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &x}},
		{Key: xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: symPtr("y")}, Val: xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &y}},
	}
	mapPtr := &scMap
	ret := xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &mapPtr}

	at, _ := readCallSetup(t, ret)
	at.spec = spec

	got, err := at.Result()
	require.NoError(t, err)
	asMap, ok := got.(map[string]any)
	require.True(t, ok, "expected map[string]any, got %T (%v)", got, got)
	assert.Equal(t, uint32(7), asMap["x"])
	assert.Equal(t, uint32(9), asMap["y"])
}

func symPtr(s string) *xdr.ScSymbol { v := xdr.ScSymbol(s); return &v }

// ----------- Result: raw ScVal fallback when no spec --------------------

func TestResult_NoSpecReturnsRawScVal(t *testing.T) {
	at, _ := readCallSetup(t, scValU64(99))
	at.spec = nil
	got, err := at.Result()
	require.NoError(t, err)
	asVal, ok := got.(xdr.ScVal)
	require.True(t, ok, "expected raw xdr.ScVal, got %T", got)
	assert.Equal(t, xdr.ScValTypeScvU64, asVal.Type)
	assert.Equal(t, uint64(99), uint64(*asVal.U64))
}
