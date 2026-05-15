package events

import (
	"context"
	"errors"
	"fmt"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// Stream is the idiomatic Go answer to JS's onLogs / event-subscription
// helpers: given a filter, poll the RPC's getEvents endpoint on a background
// goroutine and emit decoded events on a channel until the caller cancels the
// context, the configured end ledger is reached, or the RPC produces a
// non-recoverable error.
//
// The streaming surface intentionally mirrors design §4.10 (T6.4): the public
// type is StreamFilter and the entry point is Stream(ctx, rpc, filter, opts...).
// The rpc argument is a narrow EventsRPC interface so callers can swap in fakes
// for testing; the concrete *rpcclient.Client satisfies it.

// EventsRPC is the slice of the RPC client that Stream needs. *rpcclient.Client
// satisfies this interface; tests can plug in fakes.
type EventsRPC interface {
	GetEvents(ctx context.Context, req protocol.GetEventsRequest) (protocol.GetEventsResponse, error)
}

// StreamFilter selects which contract events the stream emits.
//
// StartLedger is the ledger to begin polling from (required; an RPC error is
// surfaced if it falls outside the node's retention window). EndLedger, if
// non-zero, makes the stream stop after that ledger has been observed.
// ContractIDs and Topics map directly onto the underlying RPC filter; an empty
// slice means "no filter on this dimension". Topics are leading-topic symbols
// (e.g. "transfer"); for richer topic matching, callers can still use the
// underlying protocol.TopicFilter via the raw RPC client.
type StreamFilter struct {
	ContractIDs []string
	Topics      []string
	StartLedger uint32
	EndLedger   uint32
}

// StreamItem is one event delivered on the Stream channel.
//
// Event is always populated with the raw xdr.ContractEvent. Decoded holds the
// result of events.Decode(Event) — a *Transfer, *Mint, etc., or a typed value
// emitted by a contract-specific registered decoder. If decoding failed
// (including the "no decoder registered" case), DecodeErr is set and Decoded
// is nil; the caller can still inspect the raw event. Cursor is the RPC cursor
// string for this event, useful for checkpointing.
type StreamItem struct {
	Event     xdr.ContractEvent
	Decoded   any
	DecodeErr error
	Cursor    string

	// Ledger is the ledger sequence the event was emitted in.
	Ledger int32
	// LedgerClosedAt is the RFC3339 timestamp the RPC reported for the ledger.
	LedgerClosedAt string
	// TxHash is the hash of the transaction that emitted the event.
	TxHash string
}

// streamOptions holds tunables for the polling loop. Callers set them via
// StreamOption functional options.
type streamOptions struct {
	pollInterval time.Duration
	pageLimit    uint
	bufferSize   int
	retryBudget  int
}

// StreamOption customizes Stream behavior.
type StreamOption func(*streamOptions)

// WithPollInterval sets the delay between successive getEvents polls when the
// previous poll returned no events or has consumed all current pages. The
// default is 2 seconds.
func WithPollInterval(d time.Duration) StreamOption {
	return func(o *streamOptions) { o.pollInterval = d }
}

// WithPageLimit sets the per-request page size hint passed to getEvents. The
// RPC server caps this at MaxFiltersLimit's underlying page maximum; the
// default leaves the limit unset so the server picks.
func WithPageLimit(n uint) StreamOption {
	return func(o *streamOptions) { o.pageLimit = n }
}

// WithBufferSize sets the channel buffer size. Default is 64.
func WithBufferSize(n int) StreamOption {
	return func(o *streamOptions) { o.bufferSize = n }
}

// WithRetryBudget sets the number of consecutive transient RPC errors the
// stream will tolerate before giving up and closing the channel. Default is 5.
// A value of 0 disables retries (any RPC error closes the stream).
func WithRetryBudget(n int) StreamOption {
	return func(o *streamOptions) { o.retryBudget = n }
}

// Stream starts a background poller and returns two channels: one carrying
// decoded events in arrival order, and one carrying a terminal error if the
// stream stopped because of an unrecoverable RPC failure or a bad filter.
//
// Both channels are closed by Stream when the loop exits. The error channel
// is buffered (capacity 1); it will receive at most one value — nil if the
// stream ended cleanly (ctx canceled or end ledger reached), otherwise the
// terminal error. Callers can range over the event channel and then check the
// error channel for cleanup.
//
// Decoding errors for individual events are NOT terminal; they surface as
// StreamItem.DecodeErr on the affected item and the loop continues.
func Stream(
	ctx context.Context,
	rpc EventsRPC,
	filter StreamFilter,
	opts ...StreamOption,
) (<-chan StreamItem, <-chan error) {
	o := streamOptions{
		pollInterval: 2 * time.Second,
		bufferSize:   64,
		retryBudget:  5,
	}
	for _, opt := range opts {
		opt(&o)
	}

	itemsCh := make(chan StreamItem, o.bufferSize)
	errCh := make(chan error, 1)

	if rpc == nil {
		errCh <- errors.New("events: nil rpc")
		close(itemsCh)
		close(errCh)
		return itemsCh, errCh
	}
	if filter.StartLedger == 0 {
		errCh <- errors.New("events: StartLedger must be > 0")
		close(itemsCh)
		close(errCh)
		return itemsCh, errCh
	}

	go runStream(ctx, rpc, filter, o, itemsCh, errCh)

	return itemsCh, errCh
}

// runStream is the polling goroutine. It builds the next GetEventsRequest,
// invokes the RPC, decodes each event, and emits StreamItems on itemsCh until
// the context is canceled, the end ledger has been observed, or the retry
// budget is exhausted.
func runStream(
	ctx context.Context,
	rpc EventsRPC,
	filter StreamFilter,
	o streamOptions,
	itemsCh chan<- StreamItem,
	errCh chan<- error,
) {
	defer close(itemsCh)
	defer close(errCh)

	startLedger := filter.StartLedger
	var cursor *protocol.Cursor
	consecutiveErrs := 0

	for {
		if err := ctx.Err(); err != nil {
			errCh <- err
			return
		}

		req := buildRequest(filter, startLedger, cursor, o.pageLimit)
		resp, err := rpc.GetEvents(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				errCh <- ctx.Err()
				return
			}
			consecutiveErrs++
			if consecutiveErrs > o.retryBudget {
				errCh <- fmt.Errorf("events: RPC error after %d retries: %w", o.retryBudget, err)
				return
			}
			if !sleep(ctx, o.pollInterval) {
				errCh <- ctx.Err()
				return
			}
			continue
		}
		consecutiveErrs = 0

		for _, info := range resp.Events {
			item, buildErr := buildItem(info)
			if buildErr != nil {
				// We can't form a usable xdr.ContractEvent from this row;
				// skip it but keep streaming. Surfacing every malformed row
				// as a terminal error would let one bad event kill the
				// stream.
				continue
			}
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			case itemsCh <- item:
			}
		}

		// Advance the cursor / start ledger for the next poll. The RPC's
		// response cursor points at the last event returned; the next request
		// uses it as the pagination cursor and omits startLedger.
		if resp.Cursor != "" {
			c, parseErr := protocol.ParseCursor(resp.Cursor)
			if parseErr == nil {
				cursor = &c
				startLedger = 0
			}
		}

		// Stop when we've caught up past the configured end ledger.
		if filter.EndLedger > 0 && resp.LatestLedger >= filter.EndLedger {
			errCh <- nil
			return
		}

		// Empty page → wait before polling again. A full page suggests there
		// may be more events available immediately, so loop without sleeping.
		if len(resp.Events) == 0 {
			if !sleep(ctx, o.pollInterval) {
				errCh <- ctx.Err()
				return
			}
		}
	}
}

// buildRequest assembles a GetEventsRequest from the user-supplied filter.
// When cursor is non-nil it takes precedence over startLedger.
func buildRequest(filter StreamFilter, startLedger uint32, cursor *protocol.Cursor, limit uint) protocol.GetEventsRequest {
	req := protocol.GetEventsRequest{
		Format: protocol.FormatBase64,
	}

	innerFilter := protocol.EventFilter{
		ContractIDs: filter.ContractIDs,
	}
	for _, topic := range filter.Topics {
		innerFilter.Topics = append(innerFilter.Topics, protocol.TopicFilter{
			protocol.SegmentFilter{ScVal: symbolScVal(topic)},
		})
	}
	if len(innerFilter.ContractIDs) > 0 || len(innerFilter.Topics) > 0 {
		req.Filters = []protocol.EventFilter{innerFilter}
	}

	if cursor != nil {
		req.Pagination = &protocol.PaginationOptions{Cursor: cursor, Limit: limit}
	} else {
		req.StartLedger = startLedger
		if filter.EndLedger > 0 {
			req.EndLedger = filter.EndLedger
		}
		if limit > 0 {
			req.Pagination = &protocol.PaginationOptions{Limit: limit}
		}
	}
	return req
}

// symbolScVal wraps a string in an ScSymbol ScVal — the shape the RPC expects
// for a single-segment topic filter.
func symbolScVal(s string) *xdr.ScVal {
	sym := xdr.ScSymbol(s)
	return &xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}
}

// buildItem materializes a StreamItem from the RPC's wire-level EventInfo.
// It decodes the topic and value XDR back into an xdr.ContractEvent, then
// runs events.Decode on it. Decode errors are non-fatal: they are recorded
// on DecodeErr and the raw event is still delivered.
func buildItem(info protocol.EventInfo) (StreamItem, error) {
	event, err := eventInfoToContractEvent(info)
	if err != nil {
		return StreamItem{}, err
	}

	item := StreamItem{
		Event:          event,
		Cursor:         info.ID,
		Ledger:         info.Ledger,
		LedgerClosedAt: info.LedgerClosedAt,
		TxHash:         info.TransactionHash,
	}
	decoded, decodeErr := Decode(event)
	item.Decoded = decoded
	item.DecodeErr = decodeErr
	return item, nil
}

// eventInfoToContractEvent reverses the wire encoding the RPC applies to
// emitted events. The RPC returns each event as a list of base64 ScVal topics
// plus a base64 ScVal value, alongside the contract ID strkey and the
// event-type string. We rebuild the equivalent xdr.ContractEvent so existing
// decoders can consume it unchanged.
func eventInfoToContractEvent(info protocol.EventInfo) (xdr.ContractEvent, error) {
	topics := make(xdr.ScVec, 0, len(info.TopicXDR))
	for i, t := range info.TopicXDR {
		var sv xdr.ScVal
		if err := xdr.SafeUnmarshalBase64(t, &sv); err != nil {
			return xdr.ContractEvent{}, fmt.Errorf("events: topic %d: %w", i, err)
		}
		topics = append(topics, sv)
	}

	var data xdr.ScVal
	if info.ValueXDR != "" {
		if err := xdr.SafeUnmarshalBase64(info.ValueXDR, &data); err != nil {
			return xdr.ContractEvent{}, fmt.Errorf("events: value: %w", err)
		}
	}

	evt := xdr.ContractEvent{
		Type: protocol.GetEventTypeXDRFromEventType()[info.EventType],
		Body: xdr.ContractEventBody{
			V: 0,
			V0: &xdr.ContractEventV0{
				Topics: topics,
				Data:   data,
			},
		},
	}

	if info.ContractID != "" {
		raw, err := strkey.Decode(strkey.VersionByteContract, info.ContractID)
		if err != nil {
			return xdr.ContractEvent{}, fmt.Errorf("events: contract id: %w", err)
		}
		if len(raw) != len(xdr.ContractId{}) {
			return xdr.ContractEvent{}, fmt.Errorf("events: contract id: unexpected length %d", len(raw))
		}
		var cid xdr.ContractId
		copy(cid[:], raw)
		evt.ContractId = &cid
	}

	return evt, nil
}

// sleep waits for d or until ctx is canceled. Returns true if the full
// duration elapsed, false if the context was canceled first.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
