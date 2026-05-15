package events

import (
	"errors"
	"sync"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// The decoder registry is a process-global map from (contractID, topic-symbol)
// to an EventDecoder. Codegen-emitted packages (see design doc T8.3) call
// RegisterDecoder from init(), so importing a generated package — e.g.
// github.com/.../usdc — automatically wires up typed decoders for that
// contract's events. Non-codegen consumers can also register decoders
// manually.
//
// Mirrors the contract.spec_registry pattern.

// EventDecoder turns one raw ContractEvent into a typed Go value.
//
// Implementations are typically generated per (contract, event) pair and
// registered at init() time via RegisterDecoder.
type EventDecoder interface {
	Decode(e xdr.ContractEvent) (any, error)
}

// ErrNoDecoder is returned by Decode when no decoder has been registered for
// the (contractID, topic) pair carried by the event. T6.3 will extend Decode
// to fall back to a SEP-41 generic parser for the canonical
// transfer/mint/burn/clawback topics; until then a miss is reported as
// ErrNoDecoder.
var ErrNoDecoder = errors.New("events: no decoder registered for event")

// ErrUnaddressableEvent is returned by Decode when the event does not carry
// the metadata required to look up a decoder (nil ContractId, wrong body
// version, missing or non-symbol leading topic).
var ErrUnaddressableEvent = errors.New("events: event has no (contractID, topic) key")

type decoderKey struct {
	contractID string
	topic      string
}

var (
	decoderRegistryMu sync.RWMutex
	decoderRegistry   = map[decoderKey]EventDecoder{}
)

// RegisterDecoder attaches d to the (contractID, topic) pair for the lifetime
// of the process. contractID is the strkey "C..." form. topic is the leading
// event-topic symbol (e.g. "transfer"). Subsequent registrations for the same
// key overwrite the previous value. RegisterDecoder is safe to call
// concurrently.
func RegisterDecoder(contractID, topic string, d EventDecoder) {
	decoderRegistryMu.Lock()
	decoderRegistry[decoderKey{contractID, topic}] = d
	decoderRegistryMu.Unlock()
}

// LookupDecoder returns the EventDecoder previously registered for
// (contractID, topic), or nil if none has been registered. LookupDecoder is
// safe to call concurrently.
func LookupDecoder(contractID, topic string) EventDecoder {
	decoderRegistryMu.RLock()
	d := decoderRegistry[decoderKey{contractID, topic}]
	decoderRegistryMu.RUnlock()
	return d
}

// Decode looks up a registered decoder for e's (contractID, leading-topic)
// pair and invokes it. Returns ErrUnaddressableEvent if e lacks the metadata
// to form a key, and ErrNoDecoder if no decoder is registered. T6.3 will add
// a SEP-41 generic fallback for the canonical transfer/mint/burn/clawback
// topics.
func Decode(e xdr.ContractEvent) (any, error) {
	cid, topic, err := eventKey(e)
	if err != nil {
		return nil, err
	}
	if d := LookupDecoder(cid, topic); d != nil {
		return d.Decode(e)
	}
	return nil, ErrNoDecoder
}

// eventKey extracts the (contractID strkey, leading-topic symbol) pair used
// to dispatch decoders.
func eventKey(e xdr.ContractEvent) (string, string, error) {
	if e.Type != xdr.ContractEventTypeContract || e.ContractId == nil || e.Body.V != 0 {
		return "", "", ErrUnaddressableEvent
	}
	topics := e.Body.V0.Topics
	if len(topics) == 0 {
		return "", "", ErrUnaddressableEvent
	}
	sym, ok := topics[0].GetSym()
	if !ok {
		return "", "", ErrUnaddressableEvent
	}
	cid, err := strkey.Encode(strkey.VersionByteContract, e.ContractId[:])
	if err != nil {
		return "", "", ErrUnaddressableEvent
	}
	return cid, string(sym), nil
}
