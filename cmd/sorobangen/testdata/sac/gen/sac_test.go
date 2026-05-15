// T8.5 smoke test for the SAC binding produced by `sorobangen` against the
// bundled SAC spec (asset/sac_spec.bin). Lives inside the generated package so
// it exercises the file the integration test diffs against — if this builds
// and the read-call decoders return the right Go types, the codegen pipeline
// is end-to-end functional on a realistic, structurally diverse spec.
//
// The test runs entirely offline: a fakeRPC value satisfies the unexported
// rpcSimulator interface that contract.Client.Invoke requires by implementing
// its four methods structurally. Each subtest hand-rolls a SimulateTransaction
// response whose ReturnValueXDR encodes the SAC function's declared return
// type, then asserts the generated Client method returns the expected Go type
// when callers drop down to (*AssembledTransaction).Result().
package sac

import (
	"context"
	"math/big"
	"testing"

	"github.com/stellar/go-stellar-sdk/contract"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// nativeSAC is the deployed contract id for the Stellar Asset Contract bound
// to native XLM on Pubnet. The smoke test never hits a real network, but
// using the real id makes the assertion that the generated init() registers
// the spec via contract.LookupSpec a meaningful one.
const nativeSAC = "CAS3J7GYLGXMF6TDJBBYYSE3HQ6BBSMLNUQ34T6TZMYMW2EVH34XOWMA"

// fakeRPC implements the four methods Client.Invoke needs from its RPC
// transport. It records the last SimulateTransaction request so a test can
// assert the request was actually built, then returns the canned response.
// The Send/Get/LoadAccount halves stay minimal because read-call smoke tests
// never reach the submission half of the lifecycle.
type fakeRPC struct {
	simResp protocol.SimulateTransactionResponse
	simErr  error
	simReq  protocol.SimulateTransactionRequest
	simN    int
}

func (f *fakeRPC) SimulateTransaction(_ context.Context, req protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error) {
	f.simReq = req
	f.simN++
	return f.simResp, f.simErr
}

func (f *fakeRPC) SendTransaction(context.Context, protocol.SendTransactionRequest) (protocol.SendTransactionResponse, error) {
	return protocol.SendTransactionResponse{}, nil
}

func (f *fakeRPC) GetTransaction(context.Context, protocol.GetTransactionRequest) (protocol.GetTransactionResponse, error) {
	return protocol.GetTransactionResponse{}, nil
}

func (f *fakeRPC) LoadAccount(_ context.Context, addr string) (txnbuild.Account, error) {
	acct := txnbuild.NewSimpleAccount(addr, 0)
	return &acct, nil
}

// readCallSimResp wraps a ScVal in the response shape Simulate emits for a
// pure read: empty footprint (so IsReadCall returns true) and a single result
// carrying the encoded ReturnValueXDR. Tests pass the encoded ScVal that the
// generated Spec should decode for the function under test.
func readCallSimResp(t *testing.T, returnValue xdr.ScVal) protocol.SimulateTransactionResponse {
	t.Helper()
	data := xdr.SorobanTransactionData{
		Resources:   xdr.SorobanResources{Footprint: xdr.LedgerFootprint{}},
		ResourceFee: 1_000,
	}
	dataB64, err := xdr.MarshalBase64(data)
	if err != nil {
		t.Fatalf("marshal soroban data: %v", err)
	}
	retB64, err := xdr.MarshalBase64(returnValue)
	if err != nil {
		t.Fatalf("marshal return value: %v", err)
	}
	return protocol.SimulateTransactionResponse{
		TransactionDataXDR: dataB64,
		MinResourceFee:     500,
		Results: []protocol.SimulateHostFunctionResult{
			{ReturnValueXDR: &retB64},
		},
	}
}

// newClient builds a *Client wired to a fakeRPC whose SimulateTransaction
// returns whatever the caller pre-loaded. The source account is a fresh
// random keypair; the contract id is the native-XLM SAC.
func newClient(t *testing.T, rpc *fakeRPC) *Client {
	t.Helper()
	src := keypair.MustRandom()
	acct := txnbuild.NewSimpleAccount(src.Address(), 0)
	inner := contract.New(
		nativeSAC,
		rpc,
		network.PublicNetworkPassphrase,
		contract.WithSpec(Spec()),
		contract.WithSourceAccount(&acct),
	)
	return NewClient(inner)
}

// TestSpec_RegisteredAtInit confirms the generated init() registered the
// bundled spec under the contract id baked into the binding so callers can
// resolve it via contract.LookupSpec without holding a reference to this
// package — the property that makes the codegen pipeline composable.
func TestSpec_RegisteredAtInit(t *testing.T) {
	got := contract.LookupSpec(nativeSAC)
	if got == nil {
		t.Fatalf("contract.LookupSpec(%q) returned nil; init() should have registered the spec", nativeSAC)
	}
	if got != Spec() {
		t.Errorf("LookupSpec did not return the same *Spec as Spec()")
	}
	if !got.HasFunc("balance") {
		t.Errorf("registered spec is missing the SAC 'balance' function")
	}
}

// TestDecimals_DecodesU32 exercises a no-arg read call returning u32. The
// generated Decimals method should produce an AssembledTransaction whose
// Result() decodes via the bound Spec to a Go uint32.
func TestDecimals_DecodesU32(t *testing.T) {
	rv := xdr.ScVal{Type: xdr.ScValTypeScvU32}
	u := xdr.Uint32(7)
	rv.U32 = &u
	rpc := &fakeRPC{simResp: readCallSimResp(t, rv)}
	c := newClient(t, rpc)

	at, err := c.Decimals(context.Background())
	if err != nil {
		t.Fatalf("Decimals: %v", err)
	}
	if rpc.simN != 1 {
		t.Errorf("expected exactly one Simulate call, got %d", rpc.simN)
	}
	got, err := at.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	gotU32, ok := got.(uint32)
	if !ok {
		t.Fatalf("Result type = %T, want uint32", got)
	}
	if gotU32 != 7 {
		t.Errorf("Result value = %d, want 7", gotU32)
	}
}

// TestBalance_DecodesI128 exercises a single-Address-argument read call
// returning i128. This is the canonical SAC happy-path — `Balance(addr)`
// returning *big.Int proves both argument marshaling (string→ScAddress) and
// return decoding (ScvI128→*big.Int) flow through the bound Spec end-to-end.
func TestBalance_DecodesI128(t *testing.T) {
	rv := xdr.ScVal{Type: xdr.ScValTypeScvI128}
	parts := xdr.Int128Parts{Hi: 0, Lo: 12345}
	rv.I128 = &parts
	rpc := &fakeRPC{simResp: readCallSimResp(t, rv)}
	c := newClient(t, rpc)

	// The address is the holder being queried; any valid strkey works because
	// the fake rpc echoes back a fixed simulation response.
	holder := keypair.MustRandom().Address()
	at, err := c.Balance(context.Background(), holder)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	got, err := at.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	gotBig, ok := got.(*big.Int)
	if !ok {
		t.Fatalf("Result type = %T, want *big.Int", got)
	}
	if gotBig.Cmp(big.NewInt(12345)) != 0 {
		t.Errorf("Result value = %s, want 12345", gotBig.String())
	}
}

// TestSymbol_DecodesString exercises a no-arg read call returning a string.
// Covers ScvString → Go string decoding via the bound Spec.
func TestSymbol_DecodesString(t *testing.T) {
	s := xdr.ScString("native")
	rv := xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &s}
	rpc := &fakeRPC{simResp: readCallSimResp(t, rv)}
	c := newClient(t, rpc)

	at, err := c.Symbol(context.Background())
	if err != nil {
		t.Fatalf("Symbol: %v", err)
	}
	got, err := at.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	gotStr, ok := got.(string)
	if !ok {
		t.Fatalf("Result type = %T, want string", got)
	}
	if gotStr != "native" {
		t.Errorf("Result value = %q, want %q", gotStr, "native")
	}
}
