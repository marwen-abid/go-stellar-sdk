# Cookbook

Copy-paste recipes for the most common things you'll do with the Soroban side of `go-stellar-sdk`. Every code block in this directory is mirrored in [`recipes.go`](recipes.go) — if it lives in the docs, it compiles.

| Recipe | What it shows |
|---|---|
| [Classic transfer](classic-transfer.md) | G→G payment using `asset.Token` and the classic fast path. |
| [SAC transfer](sac-transfer.md) | G→C transfer of a classic asset via the Stellar Asset Contract. |
| [Multi-party auth](multi-party-auth.md) | Two-process hand-off for `transfer_from` (or any non-invoker auth). |
| [Event streaming](event-streaming.md) | Subscribing to SEP-41 events with `contract/events.Stream`. |
| [Codegen](codegen.md) | Emitting and consuming a typed binding with `cmd/sorobangen`. |

For the underlying types and rationale, see [`contract/README.md`](../../contract/README.md). For cross-SDK parity, see the workspace-level `stellar-sdk-token-transfer-comparison.md`.
