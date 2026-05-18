# contract

`contract` is the high-level Soroban surface of the Go Stellar SDK. It wraps the low-level `xdr` + `txnbuild` + `clients/rpcclient` stack with a state-machine-driven `AssembledTransaction` lifecycle (build → simulate → sign → send → poll), a `Spec`-driven argument marshaller, a pluggable `Signer`, a typed error tree, and a process-wide spec/event registry that codegen plugs into.

For classic asset transfers and SAC token operations, use the companion [`asset`](../asset) package — it auto-derives the SAC contract ID from a classic asset and dispatches to either `txnbuild.Payment` or `contract.Client.Invoke` depending on the addresses involved, returning an `*AssembledTransaction` in both cases.

## Quickstart — the 5-liner (`asset.Token`)

The shortest path: build a `Token`, call `Transfer`, sign and send the resulting `*AssembledTransaction`, wait for inclusion.

```go
import (
    "context"
    "math/big"

    "github.com/stellar/go-stellar-sdk/asset"
    "github.com/stellar/go-stellar-sdk/clients/rpcclient"
    "github.com/stellar/go-stellar-sdk/contract"
    "github.com/stellar/go-stellar-sdk/keypair"
    "github.com/stellar/go-stellar-sdk/network"
    "github.com/stellar/go-stellar-sdk/xdr"
)

func sendXLM(ctx context.Context, rpc *rpcclient.Client, kp *keypair.Full, to string) error {
    tok, err := asset.New(xdr.MustNewNativeAsset(),
        asset.WithRPC(rpc),
        asset.WithNetwork(network.TestNetworkPassphrase),
        asset.WithSource(kp.Address()),
        asset.WithSigner(contract.KeypairSigner(kp)),
    )
    if err != nil {
        return err
    }

    at, err := tok.Transfer(ctx, kp.Address(), to, big.NewInt(10_000_000))
    if err != nil {
        return err
    }

    sent, err := at.SignAndSend(ctx, contract.KeypairSigner(kp))
    if err != nil {
        return err
    }

    _, err = sent.Wait(ctx)
    return err
}
```

Same shape works for classic-asset trustline payments (`G…` → `G…` on an `AlphaNum4`/`AlphaNum12` asset), SAC transfers (`G…` → `C…` or any `C…` involvement), and pure-SAC tokens (`asset.NewFromContractID`). See `asset/token.go` for the full surface.

## Quickstart — manual lifecycle (`contract.Client`)

When you are calling an arbitrary contract method (not a SEP-41 token op), drive the lifecycle directly:

```go
import (
    "context"

    "github.com/stellar/go-stellar-sdk/clients/rpcclient"
    "github.com/stellar/go-stellar-sdk/contract"
    "github.com/stellar/go-stellar-sdk/keypair"
    "github.com/stellar/go-stellar-sdk/xdr"
)

func callMethod(ctx context.Context, rpc *rpcclient.Client, kp *keypair.Full, contractID, network string) (any, error) {
    // From fetches the contract's WASM spec from the network and validates it.
    // Use contract.New(contractID, rpc, network, WithSpec(s)) to skip the fetch
    // when you already have a spec (codegen registers them via RegisterSpec).
    c, err := contract.From(ctx, contractID, rpc, network,
        contract.WithSource(kp.Address()),
        contract.WithSigner(contract.KeypairSigner(kp)),
    )
    if err != nil {
        return nil, err
    }

    // Args may be:
    //   - map[string]any        (marshaled via the bound Spec)
    //   - []xdr.ScVal           (passed through)
    // Method names are validated against the spec.
    at, err := c.Invoke(ctx, "increment", map[string]any{})
    if err != nil {
        return nil, err
    }

    // Read calls (no auth, no state change) return the result after Simulate;
    // write calls return it after the tx lands.
    if at.IsReadCall() {
        return at.Result()
    }

    sent, err := at.SignAndSend(ctx, contract.KeypairSigner(kp))
    if err != nil {
        return nil, err
    }
    if _, err := sent.Wait(ctx); err != nil {
        return nil, err
    }
    return at.Result()
}
```

For the “just run it” shape, `contract.InvokeAndConfirm(ctx, c, method, args, signer, opts...)` folds `Invoke → SignAndSend → Wait → Result` into a single call.

## Type map

| Type | Role |
|---|---|
| `Client` | Entry point. Pins a contract ID, RPC, network, and optional defaults (source, signer, spec). |
| `AssembledTransaction` | The lifecycle. Holds the built `txnbuild.Transaction`, simulation result, decoded auth entries, return ScVal, and a bound `Spec`. |
| `SentTransaction` | Result of `Send`. Exposes `Wait`, `Watch`, `Status` over the RPC poll loop. |
| `Spec` | Wrapper over `xdr.ScSpecEntry` — marshals native Go values ↔ `xdr.ScVal`, validates method names, resolves contract error codes to names. |
| `Signer` | Interface a wallet/HSM/keypair implements: `Address`, `SignTransaction`, `SignAuthEntryPreimage`. `KeypairSigner` adapts `*keypair.Full`. |
| `Error` / `ContractRevertError` | Typed error tree (`Kind` + sentinels); revert errors carry the resolved code name when a spec is bound. |
| `StatusUpdate` | Streaming payload from `SentTransaction.Watch`. |

The companion sub-package `contract/events` provides `EventDecoder` registration, SEP-41 generic decoders (`Transfer`/`Mint`/`Burn`/`Clawback`), and `Stream` for live event tailing.

## Configuration

### Client-level (`ClientOption`)

| Option | Effect |
|---|---|
| `WithSpec(s)` | Bind a `*Spec` directly. Skips the network fetch in `From` (also seeded via `RegisterSpec`). |
| `WithSource(addr)` | Default source account strkey (`G…` or `M…`). |
| `WithSourceAccount(a)` | Default source as a `txnbuild.Account` (skips an RPC `getAccount`). |
| `WithSigner(s)` | Default `Signer`. Used by `SignAndSend` and `InvokeAndConfirm` when no per-call signer is passed. |
| `WithBaseFee(stroops)` | Override `txnbuild.MinBaseFee`. |
| `WithPollOptions(o)` | Default `rpcclient.PollTransactionOptions` for `Wait`. |
| `WithTimeout(d)` | Default context timeout for RPC calls. |

### Per-invoke (`InvokeOption`)

| Option | Effect |
|---|---|
| `MaxFee(stroops)` | Cap on inclusion-fee component (resource fee is added on top after simulation). |
| `ResourceFeeMultiplier(f)` | Multiplier applied to the simulated resource fee. Default 1.15. |
| `Memo(m)` | Attach a `txnbuild.Memo`. |
| `TimeBounds(min, max)` | Pre-condition time bounds. |
| `Source(addr)` / `WithInvokeSigner(s)` | Override the client-level defaults. |
| `AdditionalAuth(es...)` | Append extra `xdr.SorobanAuthorizationEntry` values before simulate. |
| `Restore(enable)` | Toggle the auto-restore preamble. Default on; disable to surface `ErrRestoreRequired` and drive the restore tx yourself via `BuildRestoreTransaction`. |
| `SkipSimulate()` | Re-load a previously simulated tx via `FromXDR` / `FromJSON` without re-simulating. |

## Error model

All errors returned by the package wrap `*contract.Error`, which carries a machine-readable `Kind`. Match on the package sentinels with `errors.Is`:

```go
_, err := at.Simulate(ctx)
switch {
case errors.Is(err, contract.ErrRestoreRequired):
    // archived footprint — restore preamble was disabled
case errors.Is(err, contract.ErrSimulationFailed):
    // simulation rejected by the RPC
case errors.Is(err, contract.ErrTimeout):
    // poll deadline exceeded
}

// Contract panics / Result::Err are wrapped in *ContractRevertError.
// errors.As unwraps the typed cause; Name is resolved from the spec when bound.
var rev *contract.ContractRevertError
if errors.As(err, &rev) {
    log.Printf("revert %s (code %d) at %s", rev.Name, rev.Code, rev.ContractID)
}
```

The full set: `ErrSimulationFailed`, `ErrRestoreRequired`, `ErrNeedsMoreSignatures`, `ErrAuthMissing`, `ErrSubmissionFailed`, `ErrTimeout`, `ErrNotYetSimulated`, `ErrTransactionFailed`. See [`errors.go`](errors.go) for the exhaustive `ErrorKind` list.

## Multi-party signing

`AssembledTransaction` is hand-off-safe between processes:

```go
// Coordinator: build, simulate, serialize.
at, _ := c.Invoke(ctx, "transfer_from", args)
blob, _ := at.ToJSON()
// ... ship blob to co-signer ...

// Co-signer: re-hydrate (no re-simulate), sign the auth entries, send back.
at2, _ := contract.FromJSON(ctx, rpc, blob)
_ = at2.SignAuthEntries(ctx, cosignerSigner, /* expirationLedger */ 0)
out, _ := at2.ToJSON()
```

`NeedsNonInvokerSigningBy(includeAlreadySigned bool)` reports which strkeys still owe a signature. `ToXDR` / `FromXDR` provide the same round-trip in base64-XDR form for transports that prefer it.

## Codegen

Hand-written contract calls work for one-off scripts, but typed bindings — generated from a contract's WASM spec — give you compile-time argument checks, typed return values, and per-contract `Err*` types. The `cmd/sorobangen` CLI emits a Go package per contract; each generated package's `init()` calls `contract.RegisterSpec` (and `events.RegisterDecoder` for typed events), so `contract.From` and `events.Decode` find the spec without a network round-trip.

```bash
go run ./cmd/sorobangen --contract-id C... --out ./internal/usdc
# or from a local WASM:
go run ./cmd/sorobangen --wasm ./build/my_contract.wasm --out ./internal/mycontract
```

Generated clients sit on top of `contract.Client` — they do not replace it.

## Testing against a live network

The [`networktest`](../networktest) package wraps `stellar network container start local` so integration tests can spin up a sandbox RPC, fund accounts via friendbot, and tear down at the end of the run. See `networktest/sandbox.go`.

## References

- [godoc — `contract`](https://pkg.go.dev/github.com/stellar/go-stellar-sdk/contract)
- [godoc — `contract/events`](https://pkg.go.dev/github.com/stellar/go-stellar-sdk/contract/events)
- [godoc — `asset`](https://pkg.go.dev/github.com/stellar/go-stellar-sdk/asset)
- Cookbook: `docs/` (classic transfer, SAC transfer, multi-party auth, event streaming, codegen)
- Runnable examples: [`go-stellar-sdk-demo`](https://github.com/marwen-abid/go-stellar-sdk-demo) — twelve numbered examples covering the 5-line send, SEP-41 surface, generic invoke, multi-party signing, events, errors, and codegen (testnet-only).
- Live demo docs: [marwen-abid.github.io/go-stellar-sdk-demo](https://marwen-abid.github.io/go-stellar-sdk-demo/) — rendered walkthrough of the demo examples above.
- Cross-SDK parity: `stellar-sdk-token-transfer-comparison.md` at the workspace root
