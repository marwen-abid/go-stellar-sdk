package events

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetDecoderRegistry wipes the package-global registry so tests don't leak
// state. Tests that mutate the registry must call this in a t.Cleanup.
func resetDecoderRegistry() {
	decoderRegistryMu.Lock()
	decoderRegistry = map[decoderKey]EventDecoder{}
	decoderRegistryMu.Unlock()
}

// decoderFunc is a tiny EventDecoder implementation that lets tests assert on
// the event passed in and return a canned value/error.
type decoderFunc func(e xdr.ContractEvent) (any, error)

func (f decoderFunc) Decode(e xdr.ContractEvent) (any, error) { return f(e) }

// makeContractEvent builds a ContractEvent with the given contract id (raw
// 32 bytes) and a single leading symbol topic. Sufficient for registry
// dispatch tests; the body is otherwise empty.
func makeContractEvent(t *testing.T, contractIDRaw [32]byte, topic string) xdr.ContractEvent {
	t.Helper()
	cid := xdr.ContractId(contractIDRaw)
	topics := xdr.ScVec{
		{Type: xdr.ScValTypeScvSymbol, Sym: func() *xdr.ScSymbol { s := xdr.ScSymbol(topic); return &s }()},
	}
	return xdr.ContractEvent{
		Type:       xdr.ContractEventTypeContract,
		ContractId: &cid,
		Body: xdr.ContractEventBody{
			V:  0,
			V0: &xdr.ContractEventV0{Topics: topics},
		},
	}
}

// strkeyContract returns the C... encoding of raw.
func strkeyContract(t *testing.T, raw [32]byte) string {
	t.Helper()
	s, err := strkey.Encode(strkey.VersionByteContract, raw[:])
	require.NoError(t, err)
	return s
}

func TestDecoderRegistry_RegisterAndLookup(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	d := decoderFunc(func(xdr.ContractEvent) (any, error) { return "ok", nil })
	RegisterDecoder("CAAA", "transfer", d)

	got := LookupDecoder("CAAA", "transfer")
	require.NotNil(t, got)
	out, err := got.Decode(xdr.ContractEvent{})
	require.NoError(t, err)
	assert.Equal(t, "ok", out)
}

func TestDecoderRegistry_LookupMissReturnsNil(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	assert.Nil(t, LookupDecoder("CAAA", "transfer"))
}

func TestDecoderRegistry_ReRegisterOverwrites(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	first := decoderFunc(func(xdr.ContractEvent) (any, error) { return 1, nil })
	second := decoderFunc(func(xdr.ContractEvent) (any, error) { return 2, nil })
	RegisterDecoder("CAAA", "transfer", first)
	RegisterDecoder("CAAA", "transfer", second)

	got := LookupDecoder("CAAA", "transfer")
	require.NotNil(t, got)
	out, err := got.Decode(xdr.ContractEvent{})
	require.NoError(t, err)
	assert.Equal(t, 2, out, "second RegisterDecoder should overwrite the first")
}

func TestDecoderRegistry_Concurrent(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			cid := fmt.Sprintf("CID_%d", i)
			topic := fmt.Sprintf("evt_%d", i)
			want := i
			RegisterDecoder(cid, topic, decoderFunc(func(xdr.ContractEvent) (any, error) {
				return want, nil
			}))
			d := LookupDecoder(cid, topic)
			require.NotNil(t, d)
			got, err := d.Decode(xdr.ContractEvent{})
			require.NoError(t, err)
			assert.Equal(t, want, got)
		}(i)
	}
	wg.Wait()

	for i := range n {
		assert.NotNil(t, LookupDecoder(fmt.Sprintf("CID_%d", i), fmt.Sprintf("evt_%d", i)))
	}
}

func TestDecode_DispatchesToRegisteredDecoder(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	var raw [32]byte
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	cid := strkeyContract(t, raw)
	evt := makeContractEvent(t, raw, "transfer")

	var sawCID string
	var sawTopic string
	RegisterDecoder(cid, "transfer", decoderFunc(func(e xdr.ContractEvent) (any, error) {
		// confirm we got the same event back.
		require.NotNil(t, e.ContractId)
		assert.Equal(t, raw, [32]byte(*e.ContractId))
		require.Len(t, e.Body.V0.Topics, 1)
		sym, ok := e.Body.V0.Topics[0].GetSym()
		require.True(t, ok)
		sawTopic = string(sym)
		sawCID = cid
		return "decoded", nil
	}))

	out, err := Decode(evt)
	require.NoError(t, err)
	assert.Equal(t, "decoded", out)
	assert.Equal(t, cid, sawCID)
	assert.Equal(t, "transfer", sawTopic)
}

func TestDecode_NoDecoderRegisteredReturnsErrNoDecoder(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	var raw [32]byte
	raw[0] = 0xAB
	evt := makeContractEvent(t, raw, "transfer")

	_, err := Decode(evt)
	assert.ErrorIs(t, err, ErrNoDecoder)
}

func TestDecode_UnaddressableEvents(t *testing.T) {
	t.Cleanup(resetDecoderRegistry)
	resetDecoderRegistry()

	cases := map[string]xdr.ContractEvent{
		"nil contract id": {
			Type: xdr.ContractEventTypeContract,
			Body: xdr.ContractEventBody{V: 0, V0: &xdr.ContractEventV0{}},
		},
		"non-contract event type": func() xdr.ContractEvent {
			cid := xdr.ContractId{}
			return xdr.ContractEvent{
				Type:       xdr.ContractEventTypeSystem,
				ContractId: &cid,
				Body:       xdr.ContractEventBody{V: 0, V0: &xdr.ContractEventV0{}},
			}
		}(),
		"empty topics": func() xdr.ContractEvent {
			cid := xdr.ContractId{}
			return xdr.ContractEvent{
				Type:       xdr.ContractEventTypeContract,
				ContractId: &cid,
				Body:       xdr.ContractEventBody{V: 0, V0: &xdr.ContractEventV0{Topics: xdr.ScVec{}}},
			}
		}(),
		"leading topic not a symbol": func() xdr.ContractEvent {
			cid := xdr.ContractId{}
			i := xdr.Int32(7)
			return xdr.ContractEvent{
				Type:       xdr.ContractEventTypeContract,
				ContractId: &cid,
				Body: xdr.ContractEventBody{V: 0, V0: &xdr.ContractEventV0{
					Topics: xdr.ScVec{{Type: xdr.ScValTypeScvI32, I32: &i}},
				}},
			}
		}(),
	}

	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Decode(e)
			assert.True(t, errors.Is(err, ErrUnaddressableEvent), "want ErrUnaddressableEvent, got %v", err)
		})
	}
}
