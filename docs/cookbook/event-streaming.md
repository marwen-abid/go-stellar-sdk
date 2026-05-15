# Event streaming

`contract/events.Stream` polls the RPC's `getEvents` endpoint on a background goroutine and emits decoded events on a channel. The decoder registry maps `(contractID, leading-topic)` pairs to typed decoders; the SEP-41 topics (`transfer`, `mint`, `burn`, `clawback`) have built-in fallbacks, so token events decode out of the box.

```go
import (
    "context"
    "fmt"
    "log"
    "time"

    "github.com/stellar/go-stellar-sdk/clients/rpcclient"
    "github.com/stellar/go-stellar-sdk/contract/events"
)

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
```

### Notes

- `StartLedger` is required and must be inside the RPC's retention window. Set `EndLedger` on the filter to stop the stream after a specific ledger; leave it zero for an open-ended subscription that runs until `ctx` is canceled.
- Decoding failures for individual events are **not** terminal — the loop continues and only `DecodeErr` is set on the affected `StreamItem`. The error channel only fires on unrecoverable RPC failures (bad filter, retry budget exhausted, etc.).
- For typed events on a non-SEP-41 contract, register a decoder with `events.RegisterDecoder(contractID, topic, decoder)`. Codegen'd packages do this in `init()` — importing the package is enough.
- `WithBufferSize` and `WithPageLimit` tune the channel/page sizing; the defaults (64 / server-chosen) are fine for most consumers.
