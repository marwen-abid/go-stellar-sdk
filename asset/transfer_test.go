package asset

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/network"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ----- shouldUseClassicPath: pure dispatch-boundary table test ------------
//
// This is the actual "dispatch logic" T5.3 codifies — independent of any
// RPC plumbing. The classic Payment fast path is only valid when both ends
// are G/M accounts AND the Token wraps a classic asset; every other
// configuration MUST route through the SAC `transfer` invocation so the
// contract handles auth and asymmetry uniformly.

const (
	gAddrA = "GA5XIGA5C7QTPTWXQHY6MCJRMTRZDOSHR6EFIBNDQTCQHG262N4GGKTM"
	gAddrB = "GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H"
	cAddr  = "CA7QYNF7SOWQ3GLR2BGMZEHXAVIRZA4KVWLTJJFC7MGXUA74P7UJUWDA"
	mAddr  = "MA7QFXANJYHIA5QSRJSE2OEYV6INTPGYRO4XJTNDM3SK4FCNPGGJYAAAAAAAAAAAAALK4" // synthetic
)

func TestShouldUseClassicPath(t *testing.T) {
	classic := &Token{classic: true}
	sacOnly := &Token{classic: false}

	cases := []struct {
		name   string
		tok    *Token
		from   string
		to     string
		expect bool
	}{
		// Classic asset, both G→G → fast path applies.
		{"classic G→G", classic, gAddrA, gAddrB, true},
		// Classic asset, M source still account-shaped → fast path applies.
		{"classic M→G", classic, mAddr, gAddrB, true},
		// Classic asset, G→C → SAC.
		{"classic G→C", classic, gAddrA, cAddr, false},
		// Classic asset, C→G → SAC.
		{"classic C→G", classic, cAddr, gAddrA, false},
		// Classic asset, C→C → SAC.
		{"classic C→C", classic, cAddr, cAddr, false},
		// SAC-only token, both G→G → must still go SAC (no classic counterpart).
		{"sac-only G→G", sacOnly, gAddrA, gAddrB, false},
		// SAC-only token, any C involvement → SAC.
		{"sac-only G→C", sacOnly, gAddrA, cAddr, false},
		// Garbage strkey → never the classic path.
		{"classic bad-from", classic, "not-a-strkey", gAddrB, false},
		{"classic bad-to", classic, gAddrA, "", false},
		// Nil receiver → false (no panic).
		{"nil token", nil, gAddrA, gAddrB, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.tok.shouldUseClassicPath(tc.from, tc.to)
			if got != tc.expect {
				t.Fatalf("shouldUseClassicPath(%q→%q) = %v, want %v", tc.from, tc.to, got, tc.expect)
			}
		})
	}
}

// ----- Transfer: argument validation -------------------------------------

func TestTransfer_RejectsBadArgs(t *testing.T) {
	tok := newTransferTestToken(t, nil)

	cases := []struct {
		name   string
		from   string
		to     string
		amount *big.Int
		want   string
	}{
		{"bad from", "not-a-strkey", gAddrB, big.NewInt(1), `from "not-a-strkey"`},
		{"bad to", gAddrA, "", big.NewInt(1), `to ""`},
		{"nil amount", gAddrA, gAddrB, nil, "amount is nil"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tok.Transfer(context.Background(), tc.from, tc.to, tc.amount)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrInvalidTransferArg) {
				t.Fatalf("err not ErrInvalidTransferArg: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestTransfer_NilReceiver(t *testing.T) {
	var tok *Token
	_, err := tok.Transfer(context.Background(), gAddrA, gAddrB, big.NewInt(1))
	if !errors.Is(err, ErrInvalidTransferArg) {
		t.Fatalf("err not ErrInvalidTransferArg: %v", err)
	}
}

// ----- Transfer: SAC happy path (G→C invokes the SAC transfer fn) --------
//
// Uses an httptest-backed rpcclient.Client so we observe what the contract
// path sends over the wire. The test asserts:
//   - the simulateTransaction RPC was reached (so dispatch chose SAC), and
//   - the simulated envelope carries an InvokeHostFunction op whose function
//     name is "transfer" with three args (from, to, amount) matching the call.

func TestTransfer_SACPath_GtoC_InvokesTransferOnContract(t *testing.T) {
	got := &capturedSimReq{}
	server := newSimulateServer(t, got)
	defer server.Close()
	rpc := rpcclient.NewClient(server.URL, nil)
	defer rpc.Close()

	tok := newTransferTestToken(t, rpc)

	// Build the simulator response to be a benign read-call shape so
	// AssembledTransaction.Simulate succeeds without driving Send/Wait.
	got.respondWith = canonicalReadCallResp(t)

	const amount = int64(10_000_000)
	at, err := tok.Transfer(context.Background(), gAddrA, cAddr, big.NewInt(amount))
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	if at == nil {
		t.Fatal("Transfer returned nil AssembledTransaction")
	}
	if got.calls != 1 {
		t.Fatalf("simulateTransaction calls = %d, want 1", got.calls)
	}

	method, args := decodeInvokeFn(t, got.lastEnvelope)
	if method != "transfer" {
		t.Fatalf("invoked method = %q, want %q", method, "transfer")
	}
	if len(args) != 3 {
		t.Fatalf("len(args) = %d, want 3", len(args))
	}
	// arg[0] = from (address)
	if got, want := scAddrStrkey(t, args[0]), gAddrA; got != want {
		t.Fatalf("from arg = %q, want %q", got, want)
	}
	// arg[1] = to (address)
	if got, want := scAddrStrkey(t, args[1]), cAddr; got != want {
		t.Fatalf("to arg = %q, want %q", got, want)
	}
	// arg[2] = amount (i128)
	if args[2].Type != xdr.ScValTypeScvI128 {
		t.Fatalf("amount type = %v, want ScvI128", args[2].Type)
	}
	if args[2].I128 == nil || args[2].I128.Hi != 0 || uint64(args[2].I128.Lo) != uint64(amount) {
		t.Fatalf("amount = %+v, want i128(%d)", args[2].I128, amount)
	}
}

// ----- helpers -----------------------------------------------------------

func newTransferTestToken(t *testing.T, rpc *rpcclient.Client) *Token {
	t.Helper()
	if rpc == nil {
		// Arg-validation tests need a Token whose rpc is non-nil but is
		// never reached; point at a closed loopback so accidental network
		// calls fail loudly.
		rpc = rpcclient.NewClient("http://127.0.0.1:1", nil)
	}
	tok, err := NewFromContractID(cAddr,
		WithRPC(rpc),
		WithNetwork(network.TestNetworkPassphrase),
		WithSource(gAddrA),
	)
	if err != nil {
		t.Fatalf("NewFromContractID: %v", err)
	}
	return tok
}

// capturedSimReq records the most-recent simulateTransaction request body
// observed by newSimulateServer. respondWith holds the SimulateTransactionResponse
// to return.
type capturedSimReq struct {
	calls        int
	lastEnvelope string
	respondWith  protocol.SimulateTransactionResponse
}

type jsonRPCRequest struct {
	JSONRPC string                              `json:"jsonrpc"`
	Method  string                              `json:"method"`
	Params  protocol.SimulateTransactionRequest `json:"params"`
	ID      any                                 `json:"id"`
}

type jsonRPCResponse struct {
	JSONRPC string `json:"jsonrpc"`
	Result  any    `json:"result"`
	ID      any    `json:"id"`
}

func newSimulateServer(t *testing.T, captured *capturedSimReq) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode rpc req: %v", err)
		}
		if req.Method != protocol.SimulateTransactionMethodName {
			t.Fatalf("unexpected rpc method %q", req.Method)
		}
		captured.calls++
		captured.lastEnvelope = req.Params.Transaction

		resp := jsonRPCResponse{
			JSONRPC: "2.0",
			Result:  captured.respondWith,
			ID:      req.ID,
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("encode rpc resp: %v", err)
		}
	}))
}

// canonicalReadCallResp returns a simulate response shaped as a read call
// (empty footprint, no auth) so AssembledTransaction.Simulate succeeds and
// Transfer returns cleanly without triggering Send / Wait.
func canonicalReadCallResp(t *testing.T) protocol.SimulateTransactionResponse {
	t.Helper()
	data := xdr.SorobanTransactionData{
		Resources:   xdr.SorobanResources{Footprint: xdr.LedgerFootprint{}},
		ResourceFee: 1_000,
	}
	dataB64, err := xdr.MarshalBase64(data)
	if err != nil {
		t.Fatalf("marshal soroban data: %v", err)
	}
	retScv := xdr.ScVal{Type: xdr.ScValTypeScvVoid}
	retB64, err := xdr.MarshalBase64(retScv)
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

// decodeInvokeFn pulls the (functionName, args) pair out of the base64
// transaction envelope captured from the simulateTransaction request body.
func decodeInvokeFn(t *testing.T, envelopeB64 string) (string, []xdr.ScVal) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(envelopeB64)
	if err != nil {
		t.Fatalf("base64 decode envelope: %v", err)
	}
	var env xdr.TransactionEnvelope
	if err := env.UnmarshalBinary(raw); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	ops := env.Operations()
	if len(ops) == 0 {
		t.Fatal("envelope has no operations")
	}
	body := ops[0].Body
	if body.Type != xdr.OperationTypeInvokeHostFunction {
		t.Fatalf("op type = %v, want InvokeHostFunction", body.Type)
	}
	fn := body.InvokeHostFunctionOp.HostFunction
	if fn.Type != xdr.HostFunctionTypeHostFunctionTypeInvokeContract {
		t.Fatalf("hostfn type = %v, want InvokeContract", fn.Type)
	}
	ic := fn.InvokeContract
	return string(ic.FunctionName), ic.Args
}

// scAddrStrkey extracts the strkey representation of an ScAddress ScVal.
func scAddrStrkey(t *testing.T, v xdr.ScVal) string {
	t.Helper()
	if v.Type != xdr.ScValTypeScvAddress || v.Address == nil {
		t.Fatalf("scv = %+v, want ScvAddress", v)
	}
	s, err := v.Address.String()
	if err != nil {
		t.Fatalf("ScAddress.String: %v", err)
	}
	return s
}
