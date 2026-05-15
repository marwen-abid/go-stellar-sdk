# Codegen

Hand-written contract calls work for one-off scripts, but typed bindings — generated from a contract's WASM spec — give you compile-time argument checks, typed return values, and per-contract error types. `cmd/sorobangen` emits one Go package per contract; each generated package's `init()` calls `contract.RegisterSpec` and (for events) `events.RegisterDecoder`, so `contract.From` and the events pipeline find the spec without a network round-trip.

### Generate a package

From a deployed contract id (the spec is fetched via RPC):

```bash
go run ./cmd/sorobangen \
    -wasm ./build/my_contract.wasm \
    -out  ./internal/mycontract \
    -package mycontract \
    -contract-id C...                          # optional but recommended
```

Or from a raw XDR `ScSpecEntry` stream you already have on disk:

```bash
go run ./cmd/sorobangen \
    -spec ./spec.bin \
    -out  ./internal/mycontract \
    -package mycontract
```

`-contract-id` makes the generated `init()` register the spec under that strkey. Skip it for ABI-only packages that aren't tied to a specific deployment.

### Use the generated client

A generated client sits on top of `contract.Client` — it does not replace it. The example below uses the SDK's bundled SAC binding via `asset.NewFromContractID`, which is the same shape a `sorobangen`-emitted package exposes:

```go
import (
    "context"
    "errors"
    "fmt"
    "log"

    "github.com/stellar/go-stellar-sdk/asset"
    "github.com/stellar/go-stellar-sdk/clients/rpcclient"
    "github.com/stellar/go-stellar-sdk/contract"
    "github.com/stellar/go-stellar-sdk/keypair"
    "github.com/stellar/go-stellar-sdk/network"
)

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
```

### Notes

- Drive codegen from a `//go:generate` directive in a sibling file so `go generate ./...` regenerates the binding when you bump the contract's WASM. See `cmd/sorobangen/integration_test.go` for the in-repo pattern.
- Generated packages register their spec **and** decoders for any events the contract declares. Stream consumers (see [event-streaming.md](event-streaming.md)) automatically get typed `Decoded` values once the generated package is imported anywhere in the binary.
- Want the bundled SAC binding without running codegen? Use `asset.NewFromContractID` (sketched above) — the SAC spec ships embedded in the `asset` package.
