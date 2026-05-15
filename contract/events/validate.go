package events

import (
	"errors"
	"fmt"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// ErrInvalidSACEvent is returned by ValidateSACEvent when the event's source
// contract id does not match the canonical Stellar Asset Contract id for the
// declared asset and network — i.e. the event was not emitted by the SAC of
// record. Wraps the underlying cause when one is available so callers can
// inspect it via errors.Unwrap / errors.Is.
var ErrInvalidSACEvent = errors.New("events: event is not from the canonical SAC for the given asset")

// ValidateSACEvent reports whether ev was emitted by the canonical Stellar
// Asset Contract (SAC) for the given classic asset on the given network.
//
// The SEP-41 decoders in this package (ParseSEP41Transfer, etc.) will happily
// decode any contract emitting SEP-41-shaped events, including impostor
// contracts that forge transfer/mint/burn/clawback topics for assets they do
// not actually issue. ValidateSACEvent closes that gap: it derives the
// expected SAC contract id from (asset, networkPassphrase) via
// xdr.Asset.ContractID — the same derivation asset.Token uses — and compares
// it against ev.ContractId. It does NOT decode the event payload; combine it
// with a ParseSEP41* call (or events.Decode) when you need both.
//
// Returns nil on a match. Returns ErrInvalidSACEvent (possibly wrapping a
// more specific cause) on mismatch or on a structurally unusable event
// (wrong event type, nil ContractId, asset-id derivation failure).
func ValidateSACEvent(ev xdr.ContractEvent, asset xdr.Asset, networkPassphrase string) error {
	if ev.Type != xdr.ContractEventTypeContract {
		return fmt.Errorf("%w: event type is %s, want Contract", ErrInvalidSACEvent, ev.Type)
	}
	if ev.ContractId == nil {
		return fmt.Errorf("%w: event has no ContractId", ErrInvalidSACEvent)
	}
	expected, err := asset.ContractID(networkPassphrase)
	if err != nil {
		return fmt.Errorf("%w: deriving expected contract id: %v", ErrInvalidSACEvent, err)
	}
	if xdr.ContractId(expected) != *ev.ContractId {
		return ErrInvalidSACEvent
	}
	return nil
}
