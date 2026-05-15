package asset

// Sandbox retrofits of the Phase 5 (T5.5) end-to-end Token tests against a
// real Stellar RPC. T7.3 — design §6 — adds a live-network analogue of the
// headline 5-liner so we keep parity confidence beyond the hermetic
// fakeRPCRouter coverage in token_e2e_test.go.
//
// Gating: every test starts with networktest.Require(t), which skips unless
// the STELLAR_NETWORK_SANDBOX env var is set. Default `go test ./...`
// therefore stays hermetic and does not touch a network.
//
// Funding: Sandbox.NewFundedKeypair fronts a fresh G-address per test via
// friendbot. The native XLM SAC is derived for the target network and used
// for every transfer/balance assertion.

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/contract"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/networktest"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// TestSandbox_Token_TransferLifecycle runs the §4.8 5-liner against a live
// sandbox: build a native-asset Token, Transfer XLM from a fresh G-account
// to a freshly-derived C-address, and assert the AT runs through
// SignAndSend -> Wait to SUCCESS.
//
// Using a C-address recipient forces dispatch through the SAC `transfer`
// invocation (rather than the classic Payment fast path), exercising the
// same code path the hermetic TestToken_TransferLifecycle_E2E covers.
func TestSandbox_Token_TransferLifecycle(t *testing.T) {
	sb := networktest.Require(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := sb.Start(ctx); err != nil {
		t.Fatalf("Sandbox.Start: %v", err)
	}
	defer sb.Close()

	fromKP, err := sb.NewFundedKeypair(ctx)
	if err != nil {
		t.Fatalf("NewFundedKeypair(from): %v", err)
	}

	// Recipient: a contract-shaped strkey forces SAC dispatch. We don't need
	// the address to host real bytecode for `transfer` simulation to succeed
	// — the native SAC handles arbitrary C-recipients on its own ledger.
	recipientC := newContractStrkey(t)

	tok, err := New(xdr.MustNewNativeAsset(),
		WithRPC(sb.RPC),
		WithNetwork(sb.Network),
		WithSource(fromKP.Address()),
		WithSigner(contract.KeypairSigner(fromKP)),
	)
	if err != nil {
		t.Fatalf("asset.New(native): %v", err)
	}

	at, err := tok.Transfer(ctx, fromKP.Address(), recipientC, big.NewInt(1_000_000))
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	if at.IsReadCall() {
		t.Fatal("Transfer simulated as read call; want write")
	}

	sent, err := at.SignAndSend(ctx, contract.KeypairSigner(fromKP))
	if err != nil {
		t.Fatalf("SignAndSend: %v", err)
	}
	if sent == nil {
		t.Fatal("SignAndSend returned nil SentTransaction on the write path")
	}

	resp, err := sent.Wait(ctx,
		contract.PollInterval(500*time.Millisecond),
		contract.PollTimeout(45*time.Second),
	)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if resp == nil || resp.Status != protocol.TransactionStatusSuccess {
		t.Fatalf("Wait status = %+v, want SUCCESS", resp)
	}
}

// TestSandbox_Token_BalanceReadOnly verifies the read-only path against a
// live sandbox. The fresh G-address is funded by friendbot for its starting
// XLM balance; the native SAC `balance` view reports it as a non-negative
// i128. No Signer is supplied — the read path must short-circuit before
// signing or submitting anything.
func TestSandbox_Token_BalanceReadOnly(t *testing.T) {
	sb := networktest.Require(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := sb.Start(ctx); err != nil {
		t.Fatalf("Sandbox.Start: %v", err)
	}
	defer sb.Close()

	kp, err := sb.NewFundedKeypair(ctx)
	if err != nil {
		t.Fatalf("NewFundedKeypair: %v", err)
	}

	tok, err := New(xdr.MustNewNativeAsset(),
		WithRPC(sb.RPC),
		WithNetwork(sb.Network),
		WithSource(kp.Address()),
	)
	if err != nil {
		t.Fatalf("asset.New(native): %v", err)
	}

	bal, err := tok.Balance(ctx, kp.Address())
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal == nil {
		t.Fatal("Balance returned nil *big.Int")
	}
	if bal.Sign() < 0 {
		t.Fatalf("Balance = %s, want non-negative for a friendbot-funded account", bal)
	}
}

// TestSandbox_Token_TransferThenBalance composes a write + read on the same
// Token instance, mirroring the hermetic TestToken_MintThenBalance_E2E
// shape. Native XLM mint is admin-only and rejects arbitrary signers, so we
// substitute Transfer (which IS callable by the source account) and then
// assert the recipient G-account's post-transfer Balance increased — same
// chained-lifecycle property without needing the native admin.
func TestSandbox_Token_TransferThenBalance(t *testing.T) {
	sb := networktest.Require(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := sb.Start(ctx); err != nil {
		t.Fatalf("Sandbox.Start: %v", err)
	}
	defer sb.Close()

	fromKP, err := sb.NewFundedKeypair(ctx)
	if err != nil {
		t.Fatalf("NewFundedKeypair(from): %v", err)
	}
	toKP, err := sb.NewFundedKeypair(ctx)
	if err != nil {
		t.Fatalf("NewFundedKeypair(to): %v", err)
	}

	tok, err := New(xdr.MustNewNativeAsset(),
		WithRPC(sb.RPC),
		WithNetwork(sb.Network),
		WithSource(fromKP.Address()),
		WithSigner(contract.KeypairSigner(fromKP)),
	)
	if err != nil {
		t.Fatalf("asset.New(native): %v", err)
	}

	balBefore, err := tok.Balance(ctx, toKP.Address())
	if err != nil {
		t.Fatalf("Balance(before): %v", err)
	}

	amount := big.NewInt(1_000_000) // 0.1 XLM in stroops
	at, err := tok.Transfer(ctx, fromKP.Address(), toKP.Address(), amount)
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	sent, err := at.SignAndSend(ctx, contract.KeypairSigner(fromKP))
	if err != nil {
		t.Fatalf("SignAndSend: %v", err)
	}
	if sent == nil {
		t.Fatal("SignAndSend returned nil on write path")
	}
	if _, err := sent.Wait(ctx,
		contract.PollInterval(500*time.Millisecond),
		contract.PollTimeout(45*time.Second),
	); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	balAfter, err := tok.Balance(ctx, toKP.Address())
	if err != nil {
		t.Fatalf("Balance(after): %v", err)
	}
	delta := new(big.Int).Sub(balAfter, balBefore)
	if delta.Cmp(amount) != 0 {
		t.Fatalf("Balance delta = %s, want %s (before=%s after=%s)",
			delta, amount, balBefore, balAfter)
	}
}

// newContractStrkey builds a syntactically valid C-strkey from random bytes
// so SAC `transfer` dispatch is exercised against a contract-shaped
// recipient. The bytes do not need to correspond to a deployed contract —
// the native SAC accepts arbitrary C-addresses as `to`.
func newContractStrkey(t *testing.T) string {
	t.Helper()
	// Reuse a fresh keypair's public-key bytes as 32 bytes of entropy for
	// the contract id strkey body.
	kp := keypair.MustRandom()
	raw, err := strkey.Decode(strkey.VersionByteAccountID, kp.Address())
	if err != nil {
		t.Fatalf("strkey.Decode(account): %v", err)
	}
	c, err := strkey.Encode(strkey.VersionByteContract, raw)
	if err != nil {
		t.Fatalf("strkey.Encode(contract): %v", err)
	}
	return c
}
