# Classic transfer (G→G)

Send a classic asset (native XLM or any `AlphaNum4`/`AlphaNum12`) from one account to another. `asset.Token` wraps the underlying `txnbuild.Payment` op behind the same `*contract.AssembledTransaction` lifecycle the SAC path uses, so the call shape is the same whether you eventually point at a `G…` or a `C…`.

The classic fast path is opt-in: supply both `WithClassicSubmitter` and `WithAccountLoader` and `Transfer` will dispatch through `txnbuild.Payment` when both endpoints are account-shaped. Without those options the call falls back to the SAC `transfer` invocation — also correct, just heavier.

```go
import (
    "context"
    "fmt"
    "math/big"

    "github.com/stellar/go-stellar-sdk/asset"
    "github.com/stellar/go-stellar-sdk/clients/rpcclient"
    "github.com/stellar/go-stellar-sdk/contract"
    "github.com/stellar/go-stellar-sdk/keypair"
    "github.com/stellar/go-stellar-sdk/network"
    "github.com/stellar/go-stellar-sdk/xdr"
)

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
```

### Notes

- Use `xdr.NewCreditAsset(code, issuer)` for issued credits instead of `MustNewNativeAsset()`.
- `Transfer` returns once simulation succeeds; nothing has been signed or submitted yet. `SignAndSend` does both; `Wait` polls until inclusion.
- To opt into the classic Payment fast path, also pass `asset.WithClassicSubmitter` and `asset.WithAccountLoader` — without them the call still works, but it routes through SAC `transfer`.
