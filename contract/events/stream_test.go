package events

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRPC implements EventsRPC by replaying a scripted sequence of responses
// and/or errors. Calls beyond the script return the final scripted value
// indefinitely so the stream's polling loop has something to consume while
// the test sets up its cancellation.
type fakeRPC struct {
	mu       sync.Mutex
	scripted []rpcResult
	calls    []protocol.GetEventsRequest
	idx      int32
	final    rpcResult
}

type rpcResult struct {
	resp protocol.GetEventsResponse
	err  error
}

func (f *fakeRPC) GetEvents(_ context.Context, req protocol.GetEventsRequest) (protocol.GetEventsResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	i := int(atomic.AddInt32(&f.idx, 1)) - 1
	f.mu.Unlock()
	if i < len(f.scripted) {
		r := f.scripted[i]
		return r.resp, r.err
	}
	return f.final.resp, f.final.err
}

func (f *fakeRPC) capturedCalls() []protocol.GetEventsRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]protocol.GetEventsRequest, len(f.calls))
	copy(out, f.calls)
	return out
}

// makeRawTransferEventInfo encodes a SEP-41 transfer event into the wire-form
// EventInfo the RPC actually returns. The leading topic is "transfer", the
// next two are address topics, and the value is an i128 amount.
func makeRawTransferEventInfo(t *testing.T, contractRaw [32]byte, amount int64, id string, ledger int32) protocol.EventInfo {
	t.Helper()

	cidStr, err := strkey.Encode(strkey.VersionByteContract, contractRaw[:])
	require.NoError(t, err)

	from := strkey.MustEncode(strkey.VersionByteContract, fillBytes(0x42))
	to := strkey.MustEncode(strkey.VersionByteContract, fillBytes(0x43))

	topics := []xdr.ScVal{
		symVal("transfer"),
		mustAddrScVal(t, from),
		mustAddrScVal(t, to),
	}
	topicXDR := make([]string, 0, len(topics))
	for _, sv := range topics {
		b, err := xdr.MarshalBase64(sv)
		require.NoError(t, err)
		topicXDR = append(topicXDR, b)
	}

	valueXDR, err := xdr.MarshalBase64(makeBigAmount(big.NewInt(amount)))
	require.NoError(t, err)

	return protocol.EventInfo{
		EventType:  protocol.EventTypeContract,
		Ledger:     ledger,
		ContractID: cidStr,
		ID:         id,
		TopicXDR:   topicXDR,
		ValueXDR:   valueXDR,
	}
}

// fillBytes returns a 32-byte slice with every byte set to b — enough to
// build deterministic strkey ids for fixtures.
func fillBytes(b byte) []byte {
	var raw [32]byte
	for i := range raw {
		raw[i] = b
	}
	return raw[:]
}

func symVal(s string) xdr.ScVal {
	sym := xdr.ScSymbol(s)
	return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}
}

// mustAddrScVal builds an Address-typed ScVal from a contract strkey.
func mustAddrScVal(t *testing.T, c string) xdr.ScVal {
	t.Helper()
	raw, err := strkey.Decode(strkey.VersionByteContract, c)
	require.NoError(t, err)
	var cid xdr.ContractId
	copy(cid[:], raw)
	addr := xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &cid}
	return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &addr}
}

func cursorAt(ledger uint32, event uint32) string {
	return protocol.Cursor{Ledger: ledger, Tx: 0, Op: 0, Event: event}.String()
}

func TestStream_SinglePageHappyPath(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	var contract [32]byte
	contract[0] = 0xAA

	ev1 := makeRawTransferEventInfo(t, contract, 100, cursorAt(10, 0), 10)
	ev2 := makeRawTransferEventInfo(t, contract, 200, cursorAt(10, 1), 10)
	ev3 := makeRawTransferEventInfo(t, contract, 300, cursorAt(10, 2), 10)

	rpc := &fakeRPC{
		scripted: []rpcResult{{resp: protocol.GetEventsResponse{
			Events:       []protocol.EventInfo{ev1, ev2, ev3},
			Cursor:       cursorAt(10, 2),
			LatestLedger: 10,
		}}},
		final: rpcResult{resp: protocol.GetEventsResponse{LatestLedger: 11}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	items, errCh := Stream(ctx, rpc, StreamFilter{StartLedger: 10}, WithPollInterval(time.Millisecond))

	got := make([]StreamItem, 0, 3)
	timeout := time.After(2 * time.Second)
	for len(got) < 3 {
		select {
		case it := <-items:
			got = append(got, it)
		case <-timeout:
			t.Fatalf("timed out after %d items", len(got))
		}
	}

	cancel()
	// Drain remaining items (channel will close).
	for range items {
	}
	finalErr := <-errCh
	assert.ErrorIs(t, finalErr, context.Canceled)

	require.Len(t, got, 3)
	for i, it := range got {
		require.NoError(t, it.DecodeErr, "item %d", i)
		tr, ok := it.Decoded.(*Transfer)
		require.True(t, ok, "item %d decoded type = %T", i, it.Decoded)
		assert.NotNil(t, tr.Amount)
	}
}

func TestStream_MultiPageCursorAdvances(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	var contract [32]byte
	contract[0] = 0xBB

	page1 := protocol.GetEventsResponse{
		Events: []protocol.EventInfo{
			makeRawTransferEventInfo(t, contract, 1, cursorAt(5, 0), 5),
			makeRawTransferEventInfo(t, contract, 2, cursorAt(5, 1), 5),
		},
		Cursor:       cursorAt(5, 1),
		LatestLedger: 5,
	}
	page2 := protocol.GetEventsResponse{
		Events: []protocol.EventInfo{
			makeRawTransferEventInfo(t, contract, 3, cursorAt(6, 0), 6),
		},
		Cursor:       cursorAt(6, 0),
		LatestLedger: 6,
	}

	rpc := &fakeRPC{
		scripted: []rpcResult{{resp: page1}, {resp: page2}},
		final:    rpcResult{resp: protocol.GetEventsResponse{LatestLedger: 7}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	items, _ := Stream(ctx, rpc, StreamFilter{StartLedger: 5}, WithPollInterval(time.Millisecond))

	got := 0
	timeout := time.After(2 * time.Second)
	for got < 3 {
		select {
		case <-items:
			got++
		case <-timeout:
			t.Fatalf("timed out after %d items", got)
		}
	}
	cancel()
	for range items {
	}

	calls := rpc.capturedCalls()
	require.GreaterOrEqual(t, len(calls), 2, "expected at least two RPC calls")
	// First call: StartLedger=5, no cursor.
	assert.EqualValues(t, 5, calls[0].StartLedger)
	assert.Nil(t, calls[0].Pagination)
	// Second call: cursor set to page1's response cursor; StartLedger cleared.
	require.NotNil(t, calls[1].Pagination)
	require.NotNil(t, calls[1].Pagination.Cursor)
	assert.Equal(t, uint32(5), calls[1].Pagination.Cursor.Ledger)
	assert.EqualValues(t, 0, calls[1].StartLedger)
}

func TestStream_DecodeErrorSurfaces(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	// An event with no contract id and a non-symbol topic — Decode will
	// return ErrUnaddressableEvent — but it is still a valid xdr.ContractEvent
	// shape so the stream should deliver it with DecodeErr set.
	val, err := xdr.MarshalBase64(xdr.ScVal{Type: xdr.ScValTypeScvI32, I32: func() *xdr.Int32 { v := xdr.Int32(7); return &v }()})
	require.NoError(t, err)

	weird := protocol.EventInfo{
		EventType: protocol.EventTypeContract,
		Ledger:    1,
		ID:        cursorAt(1, 0),
		TopicXDR:  []string{val},
		ValueXDR:  val,
	}

	rpc := &fakeRPC{
		scripted: []rpcResult{{resp: protocol.GetEventsResponse{
			Events:       []protocol.EventInfo{weird},
			Cursor:       cursorAt(1, 0),
			LatestLedger: 1,
		}}},
		final: rpcResult{resp: protocol.GetEventsResponse{LatestLedger: 2}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	items, _ := Stream(ctx, rpc, StreamFilter{StartLedger: 1}, WithPollInterval(time.Millisecond))

	select {
	case it := <-items:
		assert.Error(t, it.DecodeErr)
		assert.Nil(t, it.Decoded)
	case <-time.After(2 * time.Second):
		t.Fatal("no item delivered")
	}
	cancel()
	for range items {
	}
}

func TestStream_CtxCancelClosesStream(t *testing.T) {
	rpc := &fakeRPC{
		final: rpcResult{resp: protocol.GetEventsResponse{LatestLedger: 1}},
	}
	ctx, cancel := context.WithCancel(context.Background())

	items, errCh := Stream(ctx, rpc, StreamFilter{StartLedger: 1}, WithPollInterval(10*time.Millisecond))

	cancel()

	// Drain in case any were buffered (no events scripted, so likely none).
	deadline := time.After(2 * time.Second)
loop:
	for {
		select {
		case _, ok := <-items:
			if !ok {
				break loop
			}
		case <-deadline:
			t.Fatal("items channel did not close")
		}
	}

	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("error channel did not produce")
	}
}

func TestStream_TransientErrorRetried(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	var contract [32]byte
	contract[0] = 0xCC
	good := protocol.GetEventsResponse{
		Events:       []protocol.EventInfo{makeRawTransferEventInfo(t, contract, 9, cursorAt(2, 0), 2)},
		Cursor:       cursorAt(2, 0),
		LatestLedger: 2,
	}

	transient := errors.New("transient blip")
	rpc := &fakeRPC{
		scripted: []rpcResult{
			{err: transient},
			{err: transient},
			{resp: good},
		},
		final: rpcResult{resp: protocol.GetEventsResponse{LatestLedger: 3}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	items, _ := Stream(ctx, rpc, StreamFilter{StartLedger: 2},
		WithPollInterval(time.Millisecond),
		WithRetryBudget(5),
	)

	select {
	case it := <-items:
		require.NoError(t, it.DecodeErr)
	case <-time.After(2 * time.Second):
		t.Fatal("no item delivered after transient retries")
	}
	cancel()
	for range items {
	}
}

func TestStream_PermanentErrorClosesStream(t *testing.T) {
	permanent := errors.New("permanent failure")
	rpc := &fakeRPC{
		final: rpcResult{err: permanent},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	items, errCh := Stream(ctx, rpc, StreamFilter{StartLedger: 1},
		WithPollInterval(time.Millisecond),
		WithRetryBudget(2),
	)

	// Items channel should close.
	deadline := time.After(2 * time.Second)
loop:
	for {
		select {
		case _, ok := <-items:
			if !ok {
				break loop
			}
		case <-deadline:
			t.Fatal("items channel did not close on permanent error")
		}
	}

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.ErrorIs(t, err, permanent)
	case <-time.After(2 * time.Second):
		t.Fatal("error channel did not produce")
	}
}

func TestStream_EndLedgerStops(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	var contract [32]byte
	contract[0] = 0xDD
	resp := protocol.GetEventsResponse{
		Events: []protocol.EventInfo{
			makeRawTransferEventInfo(t, contract, 1, cursorAt(20, 0), 20),
		},
		Cursor:       cursorAt(20, 0),
		LatestLedger: 25, // past EndLedger=22
	}

	rpc := &fakeRPC{
		scripted: []rpcResult{{resp: resp}},
		final:    rpcResult{resp: resp},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	items, errCh := Stream(ctx, rpc, StreamFilter{StartLedger: 20, EndLedger: 22},
		WithPollInterval(time.Millisecond),
	)

	// Should deliver the one event, then close cleanly.
	gotOne := false
	deadline := time.After(2 * time.Second)
loop:
	for {
		select {
		case _, ok := <-items:
			if !ok {
				break loop
			}
			gotOne = true
		case <-deadline:
			t.Fatal("items channel did not close at end ledger")
		}
	}
	assert.True(t, gotOne, "expected one event before close")

	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("error channel did not produce")
	}
}

func TestStream_NilRPCReturnsError(t *testing.T) {
	items, errCh := Stream(context.Background(), nil, StreamFilter{StartLedger: 1})
	_, open := <-items
	assert.False(t, open, "items channel should be pre-closed")
	err := <-errCh
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil rpc")
}

func TestStream_FilterBuilding(t *testing.T) {
	// Verify the constructed request carries contract IDs + topic symbol.
	rpc := &fakeRPC{
		final: rpcResult{resp: protocol.GetEventsResponse{LatestLedger: 1}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cidStr := strkey.MustEncode(strkey.VersionByteContract, fillBytes(0x55))
	items, _ := Stream(ctx, rpc, StreamFilter{
		StartLedger: 1,
		ContractIDs: []string{cidStr},
		Topics:      []string{"transfer"},
	}, WithPollInterval(10*time.Millisecond), WithPageLimit(50))

	// Wait for at least one RPC call to be observed.
	require.Eventually(t, func() bool {
		return len(rpc.capturedCalls()) > 0
	}, 2*time.Second, 5*time.Millisecond)

	cancel()
	for range items {
	}

	calls := rpc.capturedCalls()
	require.NotEmpty(t, calls)
	first := calls[0]
	require.Len(t, first.Filters, 1)
	assert.Equal(t, []string{cidStr}, first.Filters[0].ContractIDs)
	require.Len(t, first.Filters[0].Topics, 1)
	require.Len(t, first.Filters[0].Topics[0], 1)
	seg := first.Filters[0].Topics[0][0]
	require.NotNil(t, seg.ScVal)
	sym, ok := seg.ScVal.GetSym()
	require.True(t, ok)
	assert.Equal(t, xdr.ScSymbol("transfer"), sym)
	require.NotNil(t, first.Pagination)
	assert.EqualValues(t, 50, first.Pagination.Limit)
}

// Sanity test for buildItem error path: a TopicXDR that isn't valid base64.
func TestStream_BadEventInfoSkipped(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	bad := protocol.EventInfo{
		EventType: protocol.EventTypeContract,
		TopicXDR:  []string{"not-base64-xdr!!!"},
		ID:        cursorAt(1, 0),
	}

	rpc := &fakeRPC{
		scripted: []rpcResult{{resp: protocol.GetEventsResponse{
			Events:       []protocol.EventInfo{bad},
			Cursor:       cursorAt(1, 0),
			LatestLedger: 1,
		}}},
		final: rpcResult{resp: protocol.GetEventsResponse{LatestLedger: 2}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	items, _ := Stream(ctx, rpc, StreamFilter{StartLedger: 1}, WithPollInterval(time.Millisecond))

	// We don't expect any item to arrive — bad events are skipped. Just wait
	// a moment and then cancel, asserting the channel closes cleanly.
	select {
	case it := <-items:
		t.Fatalf("unexpected item delivered: %+v", it)
	case <-time.After(50 * time.Millisecond):
		// good — no items
	}
	cancel()
	for range items {
	}

	// Bonus assertion: we did keep calling the RPC despite the bad row.
	assert.NotEmpty(t, rpc.capturedCalls())
}

// Compile-time sanity check that the concrete fakeRPC satisfies EventsRPC.
var _ EventsRPC = (*fakeRPC)(nil)
