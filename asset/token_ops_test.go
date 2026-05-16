package asset

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ----- View ops: happy paths --------------------------------------------
//
// Each view op invocation hits the simulateTransaction RPC exactly once,
// decodes the SAC function's typed return ScVal, and produces the
// corresponding native Go value (i128 → *big.Int, u32 → uint32, string →
// string). No signer is required for any of these.

func TestBalance_DecodesI128(t *testing.T) {
	want := big.NewInt(9_999)
	got := &capturedSimReq{respondWith: viewResp(t, i128Scv(t, want))}
	server := newSimulateServer(t, got)
	defer server.Close()
	rpc := rpcclient.NewClient(server.URL, nil)
	defer rpc.Close()

	tok := newTransferTestToken(t, rpc)
	bal, err := tok.Balance(context.Background(), gAddrA)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal.Cmp(want) != 0 {
		t.Fatalf("Balance = %s, want %s", bal, want)
	}
	if got.calls != 1 {
		t.Fatalf("simulate calls = %d, want 1", got.calls)
	}
	method, args := decodeInvokeFn(t, got.lastEnvelope)
	if method != "balance" {
		t.Fatalf("method = %q, want %q", method, "balance")
	}
	if len(args) != 1 || scAddrStrkey(t, args[0]) != gAddrA {
		t.Fatalf("args = %+v, want [%s]", args, gAddrA)
	}
}

func TestDecimals_DecodesU32(t *testing.T) {
	dec := xdr.Uint32(7)
	got := &capturedSimReq{
		respondWith: viewResp(t, xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &dec}),
	}
	server := newSimulateServer(t, got)
	defer server.Close()
	rpc := rpcclient.NewClient(server.URL, nil)
	defer rpc.Close()

	tok := newTransferTestToken(t, rpc)
	d, err := tok.Decimals(context.Background())
	if err != nil {
		t.Fatalf("Decimals: %v", err)
	}
	if d != 7 {
		t.Fatalf("Decimals = %d, want 7", d)
	}
	method, args := decodeInvokeFn(t, got.lastEnvelope)
	if method != "decimals" || len(args) != 0 {
		t.Fatalf("method=%q args=%+v, want decimals/[]", method, args)
	}
}

func TestSymbol_DecodesString(t *testing.T) {
	sym := xdr.ScString("USDC")
	got := &capturedSimReq{
		respondWith: viewResp(t, xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &sym}),
	}
	server := newSimulateServer(t, got)
	defer server.Close()
	rpc := rpcclient.NewClient(server.URL, nil)
	defer rpc.Close()

	tok := newTransferTestToken(t, rpc)
	s, err := tok.Symbol(context.Background())
	if err != nil {
		t.Fatalf("Symbol: %v", err)
	}
	if s != "USDC" {
		t.Fatalf("Symbol = %q, want %q", s, "USDC")
	}
}

func TestAllowance_DecodesI128(t *testing.T) {
	want := big.NewInt(42)
	got := &capturedSimReq{respondWith: viewResp(t, i128Scv(t, want))}
	server := newSimulateServer(t, got)
	defer server.Close()
	rpc := rpcclient.NewClient(server.URL, nil)
	defer rpc.Close()

	tok := newTransferTestToken(t, rpc)
	a, err := tok.Allowance(context.Background(), gAddrA, gAddrB)
	if err != nil {
		t.Fatalf("Allowance: %v", err)
	}
	if a.Cmp(want) != 0 {
		t.Fatalf("Allowance = %s, want %s", a, want)
	}
	method, args := decodeInvokeFn(t, got.lastEnvelope)
	if method != "allowance" || len(args) != 2 {
		t.Fatalf("method=%q args=%+v, want allowance/2 args", method, args)
	}
	if scAddrStrkey(t, args[0]) != gAddrA || scAddrStrkey(t, args[1]) != gAddrB {
		t.Fatalf("allowance args = [%s, %s], want [%s, %s]",
			scAddrStrkey(t, args[0]), scAddrStrkey(t, args[1]), gAddrA, gAddrB)
	}
}

func TestName_DecodesString(t *testing.T) {
	nm := xdr.ScString("USDC:GA...EXAMPLE")
	got := &capturedSimReq{
		respondWith: viewResp(t, xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &nm}),
	}
	server := newSimulateServer(t, got)
	defer server.Close()
	rpc := rpcclient.NewClient(server.URL, nil)
	defer rpc.Close()

	tok := newTransferTestToken(t, rpc)
	n, err := tok.Name(context.Background())
	if err != nil {
		t.Fatalf("Name: %v", err)
	}
	if n != "USDC:GA...EXAMPLE" {
		t.Fatalf("Name = %q, want %q", n, "USDC:GA...EXAMPLE")
	}
	method, args := decodeInvokeFn(t, got.lastEnvelope)
	if method != "name" || len(args) != 0 {
		t.Fatalf("method=%q args=%+v, want name/[]", method, args)
	}
}

func TestAuthorized_DecodesBool(t *testing.T) {
	for _, want := range []bool{true, false} {
		t.Run(map[bool]string{true: "true", false: "false"}[want], func(t *testing.T) {
			b := want
			got := &capturedSimReq{
				respondWith: viewResp(t, xdr.ScVal{Type: xdr.ScValTypeScvBool, B: &b}),
			}
			server := newSimulateServer(t, got)
			defer server.Close()
			rpc := rpcclient.NewClient(server.URL, nil)
			defer rpc.Close()

			tok := newTransferTestToken(t, rpc)
			ok, err := tok.Authorized(context.Background(), gAddrA)
			if err != nil {
				t.Fatalf("Authorized: %v", err)
			}
			if ok != want {
				t.Fatalf("Authorized = %v, want %v", ok, want)
			}
			method, args := decodeInvokeFn(t, got.lastEnvelope)
			if method != "authorized" || len(args) != 1 {
				t.Fatalf("method=%q args=%+v, want authorized/1 arg", method, args)
			}
			if scAddrStrkey(t, args[0]) != gAddrA {
				t.Fatalf("id arg = %q, want %q", scAddrStrkey(t, args[0]), gAddrA)
			}
		})
	}
}

// ----- Write ops: happy paths -------------------------------------------
//
// Mint/Burn/Approve return an *AssembledTransaction whose underlying
// InvokeContract op carries the correct method name and args. The fake
// simulator only needs to return a well-formed (read-call-shaped) response;
// the test never proceeds to SignAndSend so a signer is never required.

func TestMint_BuildsInvokeContractOp(t *testing.T) {
	got := &capturedSimReq{respondWith: canonicalReadCallResp(t)}
	server := newSimulateServer(t, got)
	defer server.Close()
	rpc := rpcclient.NewClient(server.URL, nil)
	defer rpc.Close()

	tok := newTransferTestToken(t, rpc)
	at, err := tok.Mint(context.Background(), gAddrB, big.NewInt(100))
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if at == nil {
		t.Fatal("Mint returned nil AssembledTransaction")
	}
	method, args := decodeInvokeFn(t, got.lastEnvelope)
	if method != "mint" || len(args) != 2 {
		t.Fatalf("method=%q args=%+v, want mint/2 args", method, args)
	}
	if scAddrStrkey(t, args[0]) != gAddrB {
		t.Fatalf("to arg = %q, want %q", scAddrStrkey(t, args[0]), gAddrB)
	}
	if args[1].Type != xdr.ScValTypeScvI128 || args[1].I128 == nil || uint64(args[1].I128.Lo) != 100 {
		t.Fatalf("amount arg = %+v, want i128(100)", args[1].I128)
	}
}

func TestBurn_BuildsInvokeContractOp(t *testing.T) {
	got := &capturedSimReq{respondWith: canonicalReadCallResp(t)}
	server := newSimulateServer(t, got)
	defer server.Close()
	rpc := rpcclient.NewClient(server.URL, nil)
	defer rpc.Close()

	tok := newTransferTestToken(t, rpc)
	if _, err := tok.Burn(context.Background(), gAddrA, big.NewInt(5)); err != nil {
		t.Fatalf("Burn: %v", err)
	}
	method, args := decodeInvokeFn(t, got.lastEnvelope)
	if method != "burn" || len(args) != 2 {
		t.Fatalf("method=%q args=%+v, want burn/2 args", method, args)
	}
}

func TestApprove_EncodesExpirationLedger(t *testing.T) {
	got := &capturedSimReq{respondWith: canonicalReadCallResp(t)}
	server := newSimulateServer(t, got)
	defer server.Close()
	rpc := rpcclient.NewClient(server.URL, nil)
	defer rpc.Close()

	tok := newTransferTestToken(t, rpc)
	const liveUntil uint32 = 123_456
	_, err := tok.Approve(context.Background(), gAddrA, gAddrB, big.NewInt(50), liveUntil)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	method, args := decodeInvokeFn(t, got.lastEnvelope)
	if method != "approve" || len(args) != 4 {
		t.Fatalf("method=%q args=%+v, want approve/4 args", method, args)
	}
	if args[3].Type != xdr.ScValTypeScvU32 || args[3].U32 == nil || uint32(*args[3].U32) != liveUntil {
		t.Fatalf("expiration_ledger = %+v, want u32(%d)", args[3], liveUntil)
	}
}

func TestSetAuthorized_BuildsInvokeContractOp(t *testing.T) {
	got := &capturedSimReq{respondWith: canonicalReadCallResp(t)}
	server := newSimulateServer(t, got)
	defer server.Close()
	rpc := rpcclient.NewClient(server.URL, nil)
	defer rpc.Close()

	tok := newTransferTestToken(t, rpc)
	if _, err := tok.SetAuthorized(context.Background(), gAddrA, gAddrB, true); err != nil {
		t.Fatalf("SetAuthorized: %v", err)
	}
	method, args := decodeInvokeFn(t, got.lastEnvelope)

	// Arity is asserted against the bundled SAC spec rather than a literal —
	// the on-chain SAC's set_authorized takes (id, authorize); a stale literal
	// here is exactly how the original three-arg drift (W§3.A.3) went
	// unnoticed.
	wantArity := -1
	for _, fn := range SACSpec().Funcs() {
		if string(fn.Name) == "set_authorized" {
			wantArity = len(fn.Inputs)
			break
		}
	}
	if wantArity < 0 {
		t.Fatalf("SACSpec missing set_authorized function entry")
	}
	if method != "set_authorized" || len(args) != wantArity {
		t.Fatalf("method=%q args=%+v, want set_authorized/%d args", method, args, wantArity)
	}
	if scAddrStrkey(t, args[0]) != gAddrB {
		t.Fatalf("id arg = %q, want %q", scAddrStrkey(t, args[0]), gAddrB)
	}
	if args[1].Type != xdr.ScValTypeScvBool || args[1].B == nil || *args[1].B != true {
		t.Fatalf("authorize arg = %+v, want bool(true)", args[1])
	}
}

// ----- Argument validation ----------------------------------------------

func TestTokenOps_RejectsBadArgs(t *testing.T) {
	tok := newTransferTestToken(t, nil) // unreachable rpc — never hit
	ctx := context.Background()

	cases := []struct {
		name string
		fn   func() error
		want string
	}{
		{"Balance bad strkey", func() error {
			_, err := tok.Balance(ctx, "not-a-strkey")
			return err
		}, "encode who"},
		{"Mint nil amount", func() error {
			_, err := tok.Mint(ctx, gAddrA, nil)
			return err
		}, "amount is nil"},
		{"Mint bad to", func() error {
			_, err := tok.Mint(ctx, "", big.NewInt(1))
			return err
		}, "encode to"},
		{"Burn nil amount", func() error {
			_, err := tok.Burn(ctx, gAddrA, nil)
			return err
		}, "amount is nil"},
		{"Approve bad spender", func() error {
			_, err := tok.Approve(ctx, gAddrA, "", big.NewInt(1), 0)
			return err
		}, "encode spender"},
		{"Allowance bad from", func() error {
			_, err := tok.Allowance(ctx, "x", gAddrB)
			return err
		}, "encode from"},
		{"Authorized bad id", func() error {
			_, err := tok.Authorized(ctx, "not-a-strkey")
			return err
		}, "encode id"},
		{"SetAuthorized bad admin", func() error {
			_, err := tok.SetAuthorized(ctx, "", gAddrB, true)
			return err
		}, "encode admin"},
		{"SetAuthorized bad id", func() error {
			_, err := tok.SetAuthorized(ctx, gAddrA, "nope", true)
			return err
		}, "encode id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrInvalidTokenOpArg) {
				t.Fatalf("err not ErrInvalidTokenOpArg: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestTokenOps_NilReceiver(t *testing.T) {
	var tok *Token
	if _, err := tok.Balance(context.Background(), gAddrA); !errors.Is(err, ErrInvalidTokenOpArg) {
		t.Fatalf("nil Balance err = %v, want ErrInvalidTokenOpArg", err)
	}
	if _, err := tok.Mint(context.Background(), gAddrA, big.NewInt(1)); !errors.Is(err, ErrInvalidTokenOpArg) {
		t.Fatalf("nil Mint err = %v, want ErrInvalidTokenOpArg", err)
	}
}

// ----- helpers -----------------------------------------------------------

// viewResp builds a SimulateTransactionResponse that mimics a successful
// read-only invocation returning the given ScVal. The transaction data
// footprint is empty so AssembledTransaction.IsReadCall() returns true and
// Result() can decode the value without driving Send/Wait.
func viewResp(t *testing.T, ret xdr.ScVal) protocol.SimulateTransactionResponse {
	t.Helper()
	data := xdr.SorobanTransactionData{
		Resources:   xdr.SorobanResources{Footprint: xdr.LedgerFootprint{}},
		ResourceFee: 1_000,
	}
	dataB64, err := xdr.MarshalBase64(data)
	if err != nil {
		t.Fatalf("marshal soroban data: %v", err)
	}
	retB64, err := xdr.MarshalBase64(ret)
	if err != nil {
		t.Fatalf("marshal return val: %v", err)
	}
	return protocol.SimulateTransactionResponse{
		TransactionDataXDR: dataB64,
		MinResourceFee:     500_000,
		Results: []protocol.SimulateHostFunctionResult{
			{ReturnValueXDR: &retB64},
		},
	}
}

// i128Scv encodes a non-negative *big.Int into an SCV_I128 ScVal. Test-only
// — xdr.ScvI128 is the production-side helper; this thin wrapper exists so
// per-op test bodies stay one-liners.
func i128Scv(t *testing.T, v *big.Int) xdr.ScVal {
	t.Helper()
	scv, err := xdr.ScvI128(v)
	if err != nil {
		t.Fatalf("ScvI128(%s): %v", v, err)
	}
	return scv
}
