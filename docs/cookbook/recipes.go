// Package cookbook holds the compile-checked bodies of the recipes that
// appear in the surrounding markdown pages (classic-transfer.md, etc.).
//
// Each recipe in this file is a runnable function whose body is what the
// matching markdown page renders as a code block. The functions are never
// invoked at runtime — they exist so `go build`, `gofmt`, and `go vet` keep
// every cookbook snippet honest against the current SDK API. When you edit a
// recipe, update both the function here and the corresponding markdown page;
// CI's build step will catch drift in the Go side, and review will catch
// drift in the prose.
//
// No live network calls happen here. Recipes that would otherwise talk to
// Stellar accept their RPC client / signer / contract id as parameters so the
// function compiles without a running sandbox.
package cookbook

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/stellar/go-stellar-sdk/asset"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/contract"
	"github.com/stellar/go-stellar-sdk/contract/events"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// classicTransfer sends 1 XLM from kp to dest on the testnet using the
// classic Payment fast path exposed by asset.Token. This is the canonical
// G→G transfer: the Token wraps the native asset, both endpoints are
// account-shaped (G…), and Transfer dispatches to txnbuild.Payment under the
// hood while still returning a uniform *contract.AssembledTransaction.
//
// See docs/cookbook/classic-transfer.md.
func classicTransfer(ctx context.Context, rpc *rpcclient.Client, kp *keypair.Full, dest string) error {
	tok, err := asset.New(xdr.MustNewNativeAsset(),
		asset.WithRPC(rpc),
		asset.WithNetwork(network.TestNetworkPassphrase),
		asset.WithSource(kp.Address()),
		asset.WithSigner(contract.KeypairSigner(kp)),
	)
	if err != nil {
		return fmt.Errorf("build token: %w", err)
	}

	// 1 XLM in stroops.
	amount := big.NewInt(10_000_000)

	at, err := tok.Transfer(ctx, kp.Address(), dest, amount)
	if err != nil {
		return fmt.Errorf("transfer: %w", err)
	}

	sent, err := at.SignAndSend(ctx, contract.KeypairSigner(kp))
	if err != nil {
		return fmt.Errorf("sign and send: %w", err)
	}

	if _, err := sent.Wait(ctx); err != nil {
		return fmt.Errorf("wait: %w", err)
	}
	return nil
}

// sacTransfer moves a classic asset to a contract recipient (C…). Because
// the destination is a contract address, asset.Token automatically routes
// the call through the Stellar Asset Contract's `transfer` invocation
// instead of the classic Payment op. The caller sees the same
// *contract.AssembledTransaction surface as classicTransfer.
//
// See docs/cookbook/sac-transfer.md.
func sacTransfer(
	ctx context.Context,
	rpc *rpcclient.Client,
	kp *keypair.Full,
	code, issuer, contractRecipient string,
) error {
	a, err := xdr.NewCreditAsset(code, issuer)
	if err != nil {
		return fmt.Errorf("build asset: %w", err)
	}

	tok, err := asset.New(a,
		asset.WithRPC(rpc),
		asset.WithNetwork(network.TestNetworkPassphrase),
		asset.WithSource(kp.Address()),
		asset.WithSigner(contract.KeypairSigner(kp)),
	)
	if err != nil {
		return fmt.Errorf("build token: %w", err)
	}

	// 100 units in 7-decimal stroops.
	amount := big.NewInt(1_000_000_000)

	at, err := tok.Transfer(ctx, kp.Address(), contractRecipient, amount)
	if err != nil {
		return fmt.Errorf("transfer: %w", err)
	}

	sent, err := at.SignAndSend(ctx, contract.KeypairSigner(kp))
	if err != nil {
		return fmt.Errorf("sign and send: %w", err)
	}

	_, err = sent.Wait(ctx)
	return err
}

// multiPartyAuth shows the two-process hand-off for a Soroban call that
// requires a non-invoker signature (e.g. transfer_from, or any custom
// contract method whose require_auth is enforced against an account other
// than the transaction source).
//
// Stage 1 (coordinator): build, simulate, serialize to JSON.
// Stage 2 (co-signer): hydrate from the JSON, sign just the auth entries,
// then serialize again.
// Stage 3 (coordinator): hydrate the co-signed payload, sign the envelope,
// then submit.
//
// See docs/cookbook/multi-party-auth.md.
func multiPartyAuth(
	ctx context.Context,
	rpc *rpcclient.Client,
	contractID string,
	coordinator, cosigner *keypair.Full,
	args []xdr.ScVal,
) error {
	// Stage 1: coordinator builds and simulates.
	c, err := contract.From(ctx, contractID, rpc, network.TestNetworkPassphrase,
		contract.WithSource(coordinator.Address()),
		contract.WithSigner(contract.KeypairSigner(coordinator)),
	)
	if err != nil {
		return fmt.Errorf("from: %w", err)
	}

	at, err := c.Invoke(ctx, "transfer_from", args)
	if err != nil {
		return fmt.Errorf("invoke: %w", err)
	}

	pending, err := at.NeedsNonInvokerSigningBy(false)
	if err != nil {
		return fmt.Errorf("needs: %w", err)
	}
	log.Printf("auth entries still owed by: %v", pending)

	blob, err := at.ToJSON()
	if err != nil {
		return fmt.Errorf("to json: %w", err)
	}

	// Stage 2: co-signer hydrates, signs auth entries, returns a new blob.
	at2, err := contract.FromJSON(ctx, rpc, blob)
	if err != nil {
		return fmt.Errorf("from json: %w", err)
	}
	if err := at2.SignAuthEntries(ctx, contract.KeypairSigner(cosigner), 0); err != nil {
		return fmt.Errorf("sign auth entries: %w", err)
	}
	blob2, err := at2.ToJSON()
	if err != nil {
		return fmt.Errorf("to json (cosigned): %w", err)
	}

	// Stage 3: coordinator hydrates the co-signed payload and submits.
	at3, err := contract.FromJSON(ctx, rpc, blob2)
	if err != nil {
		return fmt.Errorf("from json (cosigned): %w", err)
	}
	sent, err := at3.SignAndSend(ctx, contract.KeypairSigner(coordinator))
	if err != nil {
		return fmt.Errorf("sign and send: %w", err)
	}
	_, err = sent.Wait(ctx)
	return err
}

// streamEvents subscribes to SEP-41 transfer events for a single contract,
// starting at startLedger. The Stream goroutine polls getEvents in the
// background; the caller ranges over the StreamItem channel until ctx is
// canceled or the optional end ledger is reached.
//
// Items are decoded by the process-global decoder registry. The registry's
// SEP-41 fallback handles `transfer`, `mint`, `burn`, and `clawback` topics
// out of the box; codegen'd packages can plug in typed decoders for
// contract-specific events.
//
// See docs/cookbook/event-streaming.md.
func streamEvents(ctx context.Context, rpc *rpcclient.Client, contractID string, startLedger uint32) error {
	filter := events.StreamFilter{
		ContractIDs: []string{contractID},
		Topics:      []string{"transfer"},
		StartLedger: startLedger,
	}

	items, errs := events.Stream(ctx, rpc, filter,
		events.WithPollInterval(2*time.Second),
		events.WithRetryBudget(5),
	)

	for item := range items {
		if item.DecodeErr != nil {
			log.Printf("ledger %d: decode error: %v", item.Ledger, item.DecodeErr)
			continue
		}
		switch v := item.Decoded.(type) {
		case *events.Transfer:
			log.Printf("transfer: %s -> %s amount=%s asset=%s",
				v.From, v.To, v.Amount.String(), v.Asset)
		case *events.Mint:
			log.Printf("mint: -> %s amount=%s", v.To, v.Amount.String())
		default:
			log.Printf("ledger %d: %T", item.Ledger, item.Decoded)
		}
	}

	// Stream closes errs after items; a non-nil value here is the terminal error.
	if err := <-errs; err != nil {
		return fmt.Errorf("stream terminated: %w", err)
	}
	return nil
}

// generatedClient sketches the shape of consuming a codegen'd binding emitted
// by `go run ./cmd/sorobangen -wasm <wasm> -out <dir> -package <name>
// -contract-id <C…>`. The generated package's init() calls
// contract.RegisterSpec (and events.RegisterDecoder for typed events) so
// contract.From and the events.Stream decoder pipeline pick up the spec
// without a network round-trip.
//
// The example here uses the SDK's bundled SAC spec via asset.New rather than
// importing a generated package (which would have to live outside this
// cookbook tree). The point is to show the shape: typed clients sit on top
// of contract.Client; they never replace it.
//
// See docs/cookbook/codegen.md.
func generatedClient(ctx context.Context, rpc *rpcclient.Client, kp *keypair.Full, contractID string) error {
	tok, err := asset.NewFromContractID(contractID,
		asset.WithRPC(rpc),
		asset.WithNetwork(network.TestNetworkPassphrase),
		asset.WithSource(kp.Address()),
		asset.WithSigner(contract.KeypairSigner(kp)),
	)
	if err != nil {
		return fmt.Errorf("new from contract id: %w", err)
	}

	// Read-only call: no transaction, no signature.
	balance, err := tok.Balance(ctx, kp.Address())
	if err != nil {
		// Restoration may be needed for archived state — surface as a
		// typed error so callers can drive BuildRestoreTransaction.
		if errors.Is(err, contract.ErrRestoreRequired) {
			return fmt.Errorf("contract state archived: %w", err)
		}
		return fmt.Errorf("balance: %w", err)
	}
	log.Printf("balance: %s", balance.String())
	return nil
}
