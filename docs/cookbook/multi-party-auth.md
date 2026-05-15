# Multi-party auth

Soroban methods that require a signature from someone other than the transaction source (e.g. `transfer_from`, or any custom method whose `require_auth` is enforced against a third party) need each non-invoker to sign their auth entry separately. `AssembledTransaction` is built to hand off between processes: `ToJSON` / `FromJSON` (or `ToXDR` / `FromXDR`) round-trip the in-flight transaction without re-simulating it.

Three stages:

1. **Coordinator** builds and simulates the call, then serializes the resulting `AssembledTransaction`.
2. **Co-signer** hydrates from the blob, signs the auth entries it owes, and serializes again.
3. **Coordinator** hydrates the co-signed payload, signs the transaction envelope, and submits.

```go
import (
    "context"
    "fmt"
    "log"

    "github.com/stellar/go-stellar-sdk/clients/rpcclient"
    "github.com/stellar/go-stellar-sdk/contract"
    "github.com/stellar/go-stellar-sdk/keypair"
    "github.com/stellar/go-stellar-sdk/network"
    "github.com/stellar/go-stellar-sdk/xdr"
)

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
```

### Notes

- `NeedsNonInvokerSigningBy(false)` returns the strkeys whose auth entries still owe a signature; pass `true` to include addresses that have already signed (useful for status UI).
- `SignAuthEntries`'s third argument is `expirationLedger`. Pass `0` to keep the simulator's default; supply a specific ledger when you want a tighter expiry than the network's max.
- `FromJSON` / `FromXDR` accept a `WithSpecOverride` option if the co-signing process can't reach the contract spec via the registry or the network.
- For wire transports that prefer binary, swap `ToJSON` / `FromJSON` for `ToXDR` / `FromXDR` (base64-encoded XDR).
