package contract

// Sandbox retrofit of the Phase 3 (T3.8) AssembledTransaction lifecycle
// tests against a real Stellar RPC. T7.3 — design §6 — adds a live-network
// analogue of one full Simulate -> SignAndSend -> Wait -> Result chain so
// we keep parity confidence beyond the hermetic fakeSimulator coverage in
// assembled_transaction_integration_test.go.
//
// Gating: networktest.Require(t) skips the test unless
// STELLAR_NETWORK_SANDBOX is set; default `go test ./...` stays hermetic.
//
// Scope: one canonical SAC-invocation chain. The hermetic tests already
// cover the read-only, non-invoker-auth, restore-preamble, send-error,
// wait-timeout, and XDR round-trip branches; T7.3 deliberately does NOT
// re-run every branch over the live network — that would be slow and
// add little signal over the hermetic coverage.

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/networktest"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// TestSandbox_Lifecycle_SACTransfer drives the full AssembledTransaction
// lifecycle against a live sandbox: build an InvokeHostFunction op
// targeting the native SAC's `transfer`, Simulate, SignAndSend, Wait.
// Mirrors the hermetic TestLifecycle_WriteCall_SourceOnly path —
// SourceAccount-credentialed auth, single signer, SUCCESS terminal state.
func TestSandbox_Lifecycle_SACTransfer(t *testing.T) {
	sb := networktest.Require(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := sb.Start(ctx); err != nil {
		t.Fatalf("Sandbox.Start: %v", err)
	}
	defer sb.Close()

	fromKP, err := sb.NewFundedKeypair(ctx)
	if err != nil {
		t.Fatalf("NewFundedKeypair: %v", err)
	}

	// Native SAC contract id for this network.
	rawID, err := xdr.MustNewNativeAsset().ContractID(sb.Network)
	if err != nil {
		t.Fatalf("native ContractID: %v", err)
	}
	nativeSAC := xdr.ContractId(rawID)

	// Recipient: contract-shaped strkey forces the SAC dispatch path. The
	// 32-byte body is harvested from a throwaway keypair so the value is
	// random but well-formed; the native SAC accepts arbitrary C-addresses
	// for the `to` argument without requiring deployed bytecode.
	recipientRaw, err := strkey.Decode(strkey.VersionByteAccountID, keypair.MustRandom().Address())
	if err != nil {
		t.Fatalf("strkey.Decode(account): %v", err)
	}
	recipientC, err := strkey.Encode(strkey.VersionByteContract, recipientRaw)
	if err != nil {
		t.Fatalf("strkey.Encode(contract): %v", err)
	}
	fromScv, err := xdr.ScvAddress(fromKP.Address())
	if err != nil {
		t.Fatalf("ScvAddress(from): %v", err)
	}
	toScv, err := xdr.ScvAddress(recipientC)
	if err != nil {
		t.Fatalf("ScvAddress(to): %v", err)
	}
	amountScv, err := xdr.ScvI128(big.NewInt(1_000_000))
	if err != nil {
		t.Fatalf("ScvI128: %v", err)
	}

	// Fetch the live source account so Simulate has an accurate sequence.
	srcAcct, err := sb.RPC.LoadAccount(ctx, fromKP.Address())
	if err != nil {
		t.Fatalf("LoadAccount: %v", err)
	}

	op := &txnbuild.InvokeHostFunction{
		HostFunction: xdr.HostFunction{
			Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
			InvokeContract: &xdr.InvokeContractArgs{
				ContractAddress: xdr.ScAddress{
					Type:       xdr.ScAddressTypeScAddressTypeContract,
					ContractId: &nativeSAC,
				},
				FunctionName: "transfer",
				Args:         xdr.ScVec{fromScv, toScv, amountScv},
			},
		},
		SourceAccount: fromKP.Address(),
	}

	at, err := NewAssembledTransaction(AssembleParams{
		RPC:               sb.RPC,
		NetworkPassphrase: sb.Network,
		BaseFee:           txnbuild.MinBaseFee,
		SourceAccount:     srcAcct,
		Op:                op,
		Preconditions:     txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
	})
	if err != nil {
		t.Fatalf("NewAssembledTransaction: %v", err)
	}

	if err := at.Simulate(ctx); err != nil {
		t.Fatalf("Simulate: %v", err)
	}
	if at.IsReadCall() {
		t.Fatal("transfer simulated as read call; want write")
	}

	sent, err := at.SignAndSend(ctx, KeypairSigner(fromKP))
	if err != nil {
		t.Fatalf("SignAndSend: %v", err)
	}
	if sent == nil {
		t.Fatal("SignAndSend returned nil SentTransaction on write call")
	}

	resp, err := sent.Wait(ctx,
		PollInterval(500*time.Millisecond),
		PollTimeout(45*time.Second),
	)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if resp == nil || resp.Status != protocol.TransactionStatusSuccess {
		t.Fatalf("Wait status = %+v, want SUCCESS", resp)
	}

	// Result drives the decoded return path (SAC transfer returns void).
	if _, err := at.Result(); err != nil {
		t.Fatalf("Result: %v", err)
	}
}
