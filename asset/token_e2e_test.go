package asset

// Token end-to-end (Phase 5 closer, T5.5).
//
// This file is the parity-claim artifact for §4.8: after Phases 0–5 land, the
// 30–50-line Soroban token send in Go collapses to ~5 lines, matching the JS
// SDK's `contract.Client` + `AssembledTransaction` shape.
//
// The headline 5-liner the tests below exercise:
//
//   tok, _ := asset.New(xdr.MustNewNativeAsset(),
//       asset.WithRPC(rpc),
//       asset.WithNetwork(network.TestNetworkPassphrase),
//       asset.WithSource(from),
//       asset.WithSigner(contract.KeypairSigner(kp)),
//   )
//   at, _   := tok.Transfer(ctx, from, to, big.NewInt(amount))
//   sent, _ := at.SignAndSend(ctx, contract.KeypairSigner(kp))
//   resp, _ := sent.Wait(ctx)
//   _ = resp
//
// Every test wires a single in-process JSON-RPC fake (fakeRPCRouter) that
// records the method sequence so each scenario can assert the exact RPC trace
// — read-only ops hit simulateTransaction once; write ops hit
// simulate → send → get. Phase 7 (networktest) owns the live-net retrofit;
// the follow-up "T7 retrofit: re-run T5.5 against a live sandbox" tracks it.

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/contract"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/protocols/stellarcore"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ----- chained-lifecycle write op: Transfer ------------------------------
//
// Exercises the §4.8 5-liner end-to-end: a Token is constructed for a
// SAC-only token, Transfer is called G→C (so SAC dispatch wins), the
// returned AssembledTransaction is driven through SignAndSend → Wait. The
// fake RPC records the method sequence so the test asserts that the correct
// JSON-RPC methods fire in the correct order — and only those.

func TestToken_TransferLifecycle_E2E(t *testing.T) {
	router := newFakeRPCRouter(t)
	router.simResp = writeCallSimResponse(t)
	router.sendResp = pendingSendResponse(t, "11")
	router.getResp = successGetResponse(t, voidScv())

	server := httptest.NewServer(router)
	defer server.Close()
	rpc := rpcclient.NewClient(server.URL, nil)
	defer rpc.Close()

	tok := newE2ETestToken(t, rpc, keypair.MustRandom())

	at, err := tok.Transfer(context.Background(), gAddrA, cAddr, big.NewInt(10_000_000))
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	if at == nil {
		t.Fatal("Transfer returned nil AssembledTransaction")
	}
	if at.IsReadCall() {
		t.Fatal("write-call simulate response should produce IsReadCall=false")
	}

	sent, err := at.SignAndSend(context.Background(), tok.Signer())
	if err != nil {
		t.Fatalf("SignAndSend: %v", err)
	}
	if sent == nil {
		t.Fatal("SignAndSend returned nil SentTransaction (write call must submit)")
	}

	resp, err := sent.Wait(
		context.Background(),
		contract.PollInterval(time.Millisecond),
		contract.PollTimeout(2*time.Second),
	)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if resp == nil || resp.Status != protocol.TransactionStatusSuccess {
		t.Fatalf("Wait status = %+v, want %s", resp, protocol.TransactionStatusSuccess)
	}

	// RPC sequence: exactly one simulate, one send, one get — in that order.
	router.assertSequence(t,
		protocol.SimulateTransactionMethodName,
		protocol.SendTransactionMethodName,
		protocol.GetTransactionMethodName,
	)

	// The simulated envelope carried the right SAC method.
	method, args := decodeInvokeFn(t, router.lastSimulateEnv())
	if method != "transfer" {
		t.Fatalf("invoked method = %q, want %q", method, "transfer")
	}
	if len(args) != 3 {
		t.Fatalf("len(args) = %d, want 3", len(args))
	}
}

// ----- read-only roundtrip: Balance --------------------------------------
//
// Balance must hit simulateTransaction once and never reach for the signer
// (read calls short-circuit before any envelope is signed). The fake RPC
// records the call sequence so a regression that adds an accidental
// send/get on the read path would fail this assertion.

func TestToken_BalanceReadOnly_E2E(t *testing.T) {
	want := big.NewInt(123_456)
	router := newFakeRPCRouter(t)
	router.simResp = readCallSimResponse(t, i128Scv(t, want))

	server := httptest.NewServer(router)
	defer server.Close()
	rpc := rpcclient.NewClient(server.URL, nil)
	defer rpc.Close()

	// Build a Token WITHOUT a signer to prove the read path needs none.
	tok, err := NewFromContractID(cAddr,
		WithRPC(rpc),
		WithNetwork(network.TestNetworkPassphrase),
		WithSource(gAddrA),
	)
	if err != nil {
		t.Fatalf("NewFromContractID: %v", err)
	}

	got, err := tok.Balance(context.Background(), gAddrA)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if got.Cmp(want) != 0 {
		t.Fatalf("Balance = %s, want %s", got, want)
	}

	// Only simulate fires — no send / get.
	router.assertSequence(t, protocol.SimulateTransactionMethodName)
}

// ----- Mint (write) then Balance (read) chained on one Token --------------
//
// The same Token instance services a write and a read in turn. The recorded
// trace must be: simulate → send → get  (mint), then  simulate  (balance).

func TestToken_MintThenBalance_E2E(t *testing.T) {
	router := newFakeRPCRouter(t)
	router.simResp = writeCallSimResponse(t)
	router.sendResp = pendingSendResponse(t, "22")
	router.getResp = successGetResponse(t, voidScv())

	server := httptest.NewServer(router)
	defer server.Close()
	rpc := rpcclient.NewClient(server.URL, nil)
	defer rpc.Close()

	tok := newE2ETestToken(t, rpc, keypair.MustRandom())

	// --- Mint: full write lifecycle through Wait.
	at, err := tok.Mint(context.Background(), gAddrB, big.NewInt(500))
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	sent, err := at.SignAndSend(context.Background(), tok.Signer())
	if err != nil {
		t.Fatalf("Mint SignAndSend: %v", err)
	}
	if _, err := sent.Wait(
		context.Background(),
		contract.PollInterval(time.Millisecond),
		contract.PollTimeout(2*time.Second),
	); err != nil {
		t.Fatalf("Mint Wait: %v", err)
	}

	// --- Balance: read-only, reflects the post-mint balance.
	router.simResp = readCallSimResponse(t, i128Scv(t, big.NewInt(500)))
	bal, err := tok.Balance(context.Background(), gAddrB)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal.Cmp(big.NewInt(500)) != 0 {
		t.Fatalf("post-mint Balance = %s, want 500", bal)
	}

	router.assertSequence(t,
		protocol.SimulateTransactionMethodName, // Mint simulate
		protocol.SendTransactionMethodName,     // Mint send
		protocol.GetTransactionMethodName,      // Mint wait
		protocol.SimulateTransactionMethodName, // Balance simulate
	)
}

// ----- helpers -----------------------------------------------------------

// newE2ETestToken builds a SAC Token wired to the given RPC and a fresh
// KeypairSigner. The Signer's keypair never needs to match `source` (G/M
// strkey) — Sign appends a signature regardless; the contract would enforce
// auth on-chain. For these hermetic tests that's exactly the contract we
// want.
func newE2ETestToken(t *testing.T, rpc *rpcclient.Client, signerKP *keypair.Full) *Token {
	t.Helper()
	tok, err := NewFromContractID(cAddr,
		WithRPC(rpc),
		WithNetwork(network.TestNetworkPassphrase),
		WithSource(gAddrA),
		WithSigner(contract.KeypairSigner(signerKP)),
	)
	if err != nil {
		t.Fatalf("NewFromContractID: %v", err)
	}
	return tok
}

// fakeRPCRouter is a JSON-RPC dispatcher that handles the three methods
// AssembledTransaction's lifecycle touches and records the call sequence
// for sequence-assertion tests. It is the asset-package E2E analog of
// contract/fakeSimulator (whose tests can hold rpcSimulator directly); the
// asset package uses a real *rpcclient.Client so the router speaks HTTP.
type fakeRPCRouter struct {
	t *testing.T

	mu          sync.Mutex
	methodCalls []string
	simEnvelope string

	simResp  protocol.SimulateTransactionResponse
	sendResp protocol.SendTransactionResponse
	getResp  protocol.GetTransactionResponse
}

func newFakeRPCRouter(t *testing.T) *fakeRPCRouter {
	return &fakeRPCRouter{t: t}
}

func (r *fakeRPCRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.t.Helper()
	var raw struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
		ID      any             `json:"id"`
	}
	if err := json.NewDecoder(req.Body).Decode(&raw); err != nil {
		r.t.Fatalf("decode rpc req: %v", err)
	}

	r.mu.Lock()
	r.methodCalls = append(r.methodCalls, raw.Method)
	r.mu.Unlock()

	var result any
	switch raw.Method {
	case protocol.SimulateTransactionMethodName:
		var p protocol.SimulateTransactionRequest
		if err := json.Unmarshal(raw.Params, &p); err != nil {
			r.t.Fatalf("decode simulate params: %v", err)
		}
		r.mu.Lock()
		r.simEnvelope = p.Transaction
		r.mu.Unlock()
		result = r.simResp
	case protocol.SendTransactionMethodName:
		result = r.sendResp
	case protocol.GetTransactionMethodName:
		result = r.getResp
	case protocol.GetLedgerEntriesMethodName:
		// rpcclient.LoadAccount call triggered by contract.Client.Invoke
		// when WithSource(strkey) routes through the live source-fetch.
		// The lookup isn't part of the canonical sequence callers assert
		// against, so we don't record it here; assertSequence only sees
		// the simulate/send/get triplet.
		r.mu.Lock()
		r.methodCalls = r.methodCalls[:len(r.methodCalls)-1]
		r.mu.Unlock()
		var p protocol.GetLedgerEntriesRequest
		if err := json.Unmarshal(raw.Params, &p); err != nil {
			r.t.Fatalf("decode getLedgerEntries params: %v", err)
		}
		result = accountLookupResponse(r.t, p)
	default:
		r.t.Fatalf("unexpected rpc method %q", raw.Method)
	}

	resp := struct {
		JSONRPC string `json:"jsonrpc"`
		Result  any    `json:"result"`
		ID      any    `json:"id"`
	}{JSONRPC: "2.0", Result: result, ID: raw.ID}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		r.t.Fatalf("encode rpc resp: %v", err)
	}
}

func (r *fakeRPCRouter) lastSimulateEnv() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.simEnvelope
}

func (r *fakeRPCRouter) assertSequence(t *testing.T, want ...string) {
	t.Helper()
	r.mu.Lock()
	got := append([]string(nil), r.methodCalls...)
	r.mu.Unlock()
	if len(got) != len(want) {
		t.Fatalf("rpc method sequence = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("rpc method[%d] = %q, want %q (full: %v vs %v)", i, got[i], want[i], got, want)
		}
	}
}

// writeCallSimResponse mirrors contract.simResponseWriteCall — a non-empty
// ReadWrite footprint flags IsReadCall=false, no auth entries means
// SignAndSend can sign and submit without non-invoker signatures.
func writeCallSimResponse(t *testing.T) protocol.SimulateTransactionResponse {
	t.Helper()
	rwKey := xdr.LedgerKey{
		Type:    xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{AccountId: xdr.MustAddress(keypair.MustRandom().Address())},
	}
	data := xdr.SorobanTransactionData{
		Resources: xdr.SorobanResources{
			Footprint: xdr.LedgerFootprint{ReadWrite: []xdr.LedgerKey{rwKey}},
		},
		ResourceFee: 1_000,
	}
	dataB64, err := xdr.MarshalBase64(data)
	if err != nil {
		t.Fatalf("marshal soroban data: %v", err)
	}
	retB64, err := xdr.MarshalBase64(voidScv())
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

// readCallSimResponse is the canonical "view" simulate shape — empty
// footprint, no auth — so IsReadCall=true and SignAndSend short-circuits.
func readCallSimResponse(t *testing.T, ret xdr.ScVal) protocol.SimulateTransactionResponse {
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

// pendingSendResponse fabricates a PENDING sendTransaction response with a
// well-formed 64-hex transaction hash so SentTransaction can decode it.
func pendingSendResponse(t *testing.T, hashByte string) protocol.SendTransactionResponse {
	t.Helper()
	return protocol.SendTransactionResponse{
		Status: stellarcore.TXStatusPending,
		Hash:   strings.Repeat(hashByte, 32),
	}
}

// successGetResponse fabricates a SUCCESS getTransaction response carrying
// a TransactionMetaV3 with the given return ScVal. Mirrors
// contract.buildReadWriteResultMeta.
func successGetResponse(t *testing.T, ret xdr.ScVal) protocol.GetTransactionResponse {
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
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	return protocol.GetTransactionResponse{
		TransactionDetails: protocol.TransactionDetails{
			Status:        protocol.TransactionStatusSuccess,
			ResultMetaXDR: b64,
		},
	}
}

func voidScv() xdr.ScVal { return xdr.ScVal{Type: xdr.ScValTypeScvVoid} }
