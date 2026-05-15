# SAC transfer (G→C)

Move a classic asset to a contract recipient. Because the destination is a `C…` address, `asset.Token` automatically routes the call through the Stellar Asset Contract's `transfer` invocation — there is no classic Payment op that can target a contract.

The user-facing API is unchanged from the [classic transfer recipe](classic-transfer.md): build a `Token`, call `Transfer`, hand the resulting `*AssembledTransaction` off to `SignAndSend` and `Wait`.

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
```

### Notes

- For a SAC-native token (no classic asset behind it), use `asset.NewFromContractID(contractID, …)`.
- The SAC's `transfer(from, to, amount)` signature accepts both `G…` and `C…` for either end, so you can also use this recipe for C→C transfers — just swap the `from` argument.
- If the contract's footprint is archived, you'll get `contract.ErrRestoreRequired` from `Transfer`'s simulate phase unless `Restore(true)` (the default) is in effect; see [`contract/README.md`](../../contract/README.md) for the auto-restore knob.
