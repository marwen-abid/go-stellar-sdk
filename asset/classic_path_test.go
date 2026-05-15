package asset

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/contract"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// fakeClassicSubmitter records the most-recent classic submission so the
// test can assert what got built — what op, what asset, what amount, what
// destination. SubmitClassic does NOT actually sign or transmit anything;
// the hash it returns is a fixed sentinel so SignAndSend → Wait callers see
// a stable success.
type fakeClassicSubmitter struct {
	calls  int
	lastTx *txnbuild.Transaction
	hash   xdr.Hash
	err    error
}

func (f *fakeClassicSubmitter) SubmitClassic(ctx context.Context, tx *txnbuild.Transaction, signer contract.Signer) (xdr.Hash, error) {
	f.calls++
	f.lastTx = tx
	if f.err != nil {
		return xdr.Hash{}, f.err
	}
	return f.hash, nil
}

// fakeLoader returns a SimpleAccount with a caller-controlled sequence.
type fakeLoader struct {
	addr string
	seq  int64
	err  error
}

func (l *fakeLoader) LoadAccount(ctx context.Context, addr string) (txnbuild.Account, error) {
	if l.err != nil {
		return nil, l.err
	}
	l.addr = addr
	a := txnbuild.NewSimpleAccount(addr, l.seq)
	return &a, nil
}

// issuedTestAsset builds an XDR credit asset (USD issued by gAddrB) so the
// classic-path tests can pin both the native and the issued branches of
// txnbuildAssetFromXDR.
func issuedTestAsset(t *testing.T) xdr.Asset {
	t.Helper()
	var issuer xdr.AccountId
	if err := issuer.SetAddress(gAddrB); err != nil {
		t.Fatalf("set issuer: %v", err)
	}
	var asset xdr.Asset
	if err := asset.SetCredit("USD", issuer); err != nil {
		t.Fatalf("set credit: %v", err)
	}
	return asset
}

// newClassicToken builds a Token wrapping a classic asset with both the
// submitter and the account loader wired up.
func newClassicToken(t *testing.T, asset xdr.Asset, sub *fakeClassicSubmitter, ldr *fakeLoader) *Token {
	t.Helper()
	tok, err := New(asset,
		WithRPC(nilRPC(t)),
		WithNetwork(network.TestNetworkPassphrase),
		WithSource(gAddrA),
		WithClassicSubmitter(sub),
		WithAccountLoader(ldr),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tok
}

// nilRPC returns a *rpcclient.Client wired to a closed loopback so any
// accidental RPC call fails loudly. Tests assert RPC was never reached by
// observing that the submitter ran instead.
func nilRPC(t *testing.T) *rpcclient.Client {
	t.Helper()
	rpc := rpcclient.NewClient("http://127.0.0.1:1", nil)
	t.Cleanup(func() { rpc.Close() })
	return rpc
}

// newTestRPC builds a *rpcclient.Client pointed at the given URL and
// registers Close on test cleanup.
func newTestRPC(t *testing.T, url string) *rpcclient.Client {
	t.Helper()
	rpc := rpcclient.NewClient(url, nil)
	t.Cleanup(func() { rpc.Close() })
	return rpc
}

// ----- canDispatchClassic predicate --------------------------------------

func TestCanDispatchClassic(t *testing.T) {
	asset := xdr.MustNewNativeAsset()
	sub := &fakeClassicSubmitter{hash: xdr.Hash{0xaa}}
	ldr := &fakeLoader{seq: 1}

	t.Run("classic asset + submitter + loader + G→G", func(t *testing.T) {
		tok := newClassicToken(t, asset, sub, ldr)
		if !tok.canDispatchClassic(gAddrA, gAddrB) {
			t.Fatal("expected canDispatchClassic=true")
		}
	})

	t.Run("submitter missing → false", func(t *testing.T) {
		tok, err := New(asset,
			WithRPC(nilRPC(t)),
			WithNetwork(network.TestNetworkPassphrase),
			WithSource(gAddrA),
			WithAccountLoader(ldr),
		)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if tok.canDispatchClassic(gAddrA, gAddrB) {
			t.Fatal("expected canDispatchClassic=false when submitter missing")
		}
	})

	t.Run("loader missing → false", func(t *testing.T) {
		tok, err := New(asset,
			WithRPC(nilRPC(t)),
			WithNetwork(network.TestNetworkPassphrase),
			WithSource(gAddrA),
			WithClassicSubmitter(sub),
		)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if tok.canDispatchClassic(gAddrA, gAddrB) {
			t.Fatal("expected canDispatchClassic=false when loader missing")
		}
	})

	t.Run("G→C → false", func(t *testing.T) {
		tok := newClassicToken(t, asset, sub, ldr)
		if tok.canDispatchClassic(gAddrA, cAddr) {
			t.Fatal("expected canDispatchClassic=false for G→C")
		}
	})
}

// ----- Transfer: classic fast path ---------------------------------------
//
// The fake submitter records the transaction it was handed; the test asserts
// the op is a Payment with the correct destination, asset, and stroop-encoded
// amount. The SAC RPC is intentionally NOT mocked here — if dispatch picks
// SAC by mistake the test will fail with a connection error against the
// closed loopback rpcclient.

func TestTransfer_ClassicPath_NativeAsset_GtoG(t *testing.T) {
	hash := xdr.Hash{0xde, 0xad, 0xbe, 0xef}
	sub := &fakeClassicSubmitter{hash: hash}
	ldr := &fakeLoader{seq: 41}
	tok := newClassicToken(t, xdr.MustNewNativeAsset(), sub, ldr)

	at, err := tok.Transfer(context.Background(), gAddrA, gAddrB, big.NewInt(100))
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	if at == nil {
		t.Fatal("Transfer returned nil AT")
	}
	if !at.IsClassic() {
		t.Fatal("expected AT.IsClassic() = true on the classic path")
	}

	// Drive SignAndSend — that's where the submitter actually runs.
	signer := contract.KeypairSigner(keypair.MustRandom())
	sent, err := at.SignAndSend(context.Background(), signer)
	if err != nil {
		t.Fatalf("SignAndSend: %v", err)
	}
	if sent == nil {
		t.Fatal("SignAndSend returned nil SentTransaction on the classic path")
	}
	if sent.Hash != hash {
		t.Fatalf("Sent hash = %v, want %v", sent.Hash, hash)
	}
	if sub.calls != 1 {
		t.Fatalf("submitter calls = %d, want 1", sub.calls)
	}

	// Inspect the captured transaction: one Payment op, native asset,
	// destination = gAddrB, amount = "0.0000100" (100 stroops in 7-decimal form).
	tx := sub.lastTx
	if tx == nil {
		t.Fatal("submitter received nil tx")
	}
	ops := tx.Operations()
	if len(ops) != 1 {
		t.Fatalf("len(ops) = %d, want 1", len(ops))
	}
	pay, ok := ops[0].(*txnbuild.Payment)
	if !ok {
		t.Fatalf("op type = %T, want *txnbuild.Payment", ops[0])
	}
	if pay.Destination != gAddrB {
		t.Fatalf("destination = %q, want %q", pay.Destination, gAddrB)
	}
	if _, native := pay.Asset.(txnbuild.NativeAsset); !native {
		t.Fatalf("asset = %T, want NativeAsset", pay.Asset)
	}
	if pay.Amount != "0.0000100" {
		t.Fatalf("amount = %q, want %q", pay.Amount, "0.0000100")
	}

	// SourceAccount on the inner xdr.Transaction must match the loader's
	// account; sequence is incremented by NewTransaction.
	innerSrc := tx.SourceAccount().AccountID
	if innerSrc != gAddrA {
		t.Fatalf("source = %q, want %q", innerSrc, gAddrA)
	}

	// Wait should short-circuit to SUCCESS on the classic path.
	resp, err := sent.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if resp == nil || resp.Status != "SUCCESS" {
		t.Fatalf("Wait response = %+v, want SUCCESS", resp)
	}
}

func TestTransfer_ClassicPath_IssuedAsset_GtoG(t *testing.T) {
	sub := &fakeClassicSubmitter{hash: xdr.Hash{0x42}}
	ldr := &fakeLoader{seq: 99}
	tok := newClassicToken(t, issuedTestAsset(t), sub, ldr)

	_, err := tok.Transfer(context.Background(), gAddrA, gAddrB, big.NewInt(50_000_000))
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	signer := contract.KeypairSigner(keypair.MustRandom())
	at, _ := tok.Transfer(context.Background(), gAddrA, gAddrB, big.NewInt(50_000_000))
	if _, err := at.SignAndSend(context.Background(), signer); err != nil {
		t.Fatalf("SignAndSend: %v", err)
	}
	if sub.calls != 1 {
		t.Fatalf("submitter calls = %d, want 1", sub.calls)
	}

	pay := sub.lastTx.Operations()[0].(*txnbuild.Payment)
	credit, ok := pay.Asset.(txnbuild.CreditAsset)
	if !ok {
		t.Fatalf("asset = %T, want CreditAsset", pay.Asset)
	}
	if credit.Code != "USD" || credit.Issuer != gAddrB {
		t.Fatalf("credit = %+v, want USD/%s", credit, gAddrB)
	}
	// 50,000,000 stroops = 5.0000000 XLM-equivalent.
	if pay.Amount != "5.0000000" {
		t.Fatalf("amount = %q, want %q", pay.Amount, "5.0000000")
	}
}

// ----- Transfer: classic G→G without submitter → SAC fallback ------------
//
// When the Token is constructed without WithClassicSubmitter, Transfer must
// fall back to the SAC `transfer` invocation even for G→G + classic asset.
// We assert the fallback by pointing the RPC at the simulate-capture server
// and observing that simulateTransaction was reached.

func TestTransfer_ClassicAssetGtoG_FallsBackToSAC_WhenNoSubmitter(t *testing.T) {
	got := &capturedSimReq{}
	server := newSimulateServer(t, got)
	defer server.Close()
	rpcClient := newTestRPC(t, server.URL)

	tok, err := New(xdr.MustNewNativeAsset(),
		WithRPC(rpcClient),
		WithNetwork(network.TestNetworkPassphrase),
		WithSource(gAddrA),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got.respondWith = canonicalReadCallResp(t)

	at, err := tok.Transfer(context.Background(), gAddrA, gAddrB, big.NewInt(100))
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	if at.IsClassic() {
		t.Fatal("expected SAC fallback (IsClassic=false) when no ClassicSubmitter")
	}
	if got.calls != 1 {
		t.Fatalf("simulateTransaction calls = %d, want 1", got.calls)
	}
	method, _ := decodeInvokeFn(t, got.lastEnvelope)
	if method != "transfer" {
		t.Fatalf("invoked method = %q, want %q", method, "transfer")
	}
}

// ----- Transfer: classic submitter error surfaces as SubmissionFailed ----

func TestTransfer_ClassicPath_SubmitterError_Surfaces(t *testing.T) {
	sub := &fakeClassicSubmitter{err: errors.New("network down")}
	ldr := &fakeLoader{seq: 1}
	tok := newClassicToken(t, xdr.MustNewNativeAsset(), sub, ldr)

	at, err := tok.Transfer(context.Background(), gAddrA, gAddrB, big.NewInt(1))
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	signer := contract.KeypairSigner(keypair.MustRandom())
	_, err = at.SignAndSend(context.Background(), signer)
	if err == nil {
		t.Fatal("expected SignAndSend error from submitter")
	}
	if !strings.Contains(err.Error(), "network down") {
		t.Fatalf("err = %q, want substring %q", err.Error(), "network down")
	}
}

// ----- Transfer: classic path rejects negative amounts -------------------

func TestTransfer_ClassicPath_RejectsNegativeAmount(t *testing.T) {
	sub := &fakeClassicSubmitter{hash: xdr.Hash{0x01}}
	ldr := &fakeLoader{seq: 1}
	tok := newClassicToken(t, xdr.MustNewNativeAsset(), sub, ldr)

	_, err := tok.Transfer(context.Background(), gAddrA, gAddrB, big.NewInt(-5))
	if err == nil {
		t.Fatal("expected error for negative amount")
	}
	if !errors.Is(err, ErrInvalidTransferArg) {
		t.Fatalf("err not ErrInvalidTransferArg: %v", err)
	}
	if sub.calls != 0 {
		t.Fatalf("submitter calls = %d, want 0", sub.calls)
	}
}
