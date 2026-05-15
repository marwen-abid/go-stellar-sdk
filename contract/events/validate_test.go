package events

import (
	"errors"
	"math/big"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/stretchr/testify/require"
)

func TestValidateSACEvent(t *testing.T) {
	const pass = "Test SDF Network ; September 2015"
	native := xdr.MustNewNativeAsset()
	usdcIssuer := keypair.MustRandom().Address()
	usdc := xdr.MustNewCreditAsset("USDC", usdcIssuer)

	t.Run("native happy path", func(t *testing.T) {
		ev := GenerateEvent(EventTypeTransfer, randomAccount, zeroContract, "", native, big.NewInt(1), pass)
		require.NoError(t, ValidateSACEvent(ev, native, pass))
	})

	t.Run("issued asset happy path", func(t *testing.T) {
		ev := GenerateEvent(EventTypeTransfer, randomAccount, zeroContract, "", usdc, big.NewInt(1), pass)
		require.NoError(t, ValidateSACEvent(ev, usdc, pass))
	})

	t.Run("mint happy path", func(t *testing.T) {
		ev := GenerateEvent(EventTypeMint, "", zeroContract, randomAccount, usdc, big.NewInt(1), pass)
		require.NoError(t, ValidateSACEvent(ev, usdc, pass))
	})

	t.Run("impostor contract id rejected", func(t *testing.T) {
		// Build a transfer event "for" the native asset, but stamp its
		// ContractId with the SAC id of a different asset (USDC). A naive
		// SEP-41 decoder would happily parse this; ValidateSACEvent must reject.
		ev := GenerateEvent(EventTypeTransfer, randomAccount, zeroContract, "", native, big.NewInt(1), pass)
		impostorRaw, err := usdc.ContractID(pass)
		require.NoError(t, err)
		impostor := xdr.ContractId(impostorRaw)
		ev.ContractId = &impostor

		err = ValidateSACEvent(ev, native, pass)
		require.Error(t, err)
		require.ErrorIs(t, err, ErrInvalidSACEvent)
	})

	t.Run("wrong asset rejected", func(t *testing.T) {
		// Genuine SAC event for USDC, but caller declares it as native.
		ev := GenerateEvent(EventTypeTransfer, randomAccount, zeroContract, "", usdc, big.NewInt(1), pass)
		err := ValidateSACEvent(ev, native, pass)
		require.ErrorIs(t, err, ErrInvalidSACEvent)
	})

	t.Run("wrong network rejected", func(t *testing.T) {
		// Event derived on pass, validated against a different passphrase —
		// the derived contract id won't match.
		ev := GenerateEvent(EventTypeTransfer, randomAccount, zeroContract, "", native, big.NewInt(1), pass)
		err := ValidateSACEvent(ev, native, "Public Global Stellar Network ; September 2015")
		require.ErrorIs(t, err, ErrInvalidSACEvent)
	})

	t.Run("non-contract event rejected", func(t *testing.T) {
		ev := GenerateEvent(EventTypeTransfer, randomAccount, zeroContract, "", native, big.NewInt(1), pass)
		ev.Type = xdr.ContractEventTypeSystem
		err := ValidateSACEvent(ev, native, pass)
		require.ErrorIs(t, err, ErrInvalidSACEvent)
	})

	t.Run("nil contract id rejected", func(t *testing.T) {
		ev := GenerateEvent(EventTypeTransfer, randomAccount, zeroContract, "", native, big.NewInt(1), pass)
		ev.ContractId = nil
		err := ValidateSACEvent(ev, native, pass)
		require.ErrorIs(t, err, ErrInvalidSACEvent)
	})

	t.Run("error wraps ErrInvalidSACEvent for unwrap", func(t *testing.T) {
		ev := GenerateEvent(EventTypeTransfer, randomAccount, zeroContract, "", native, big.NewInt(1), pass)
		ev.ContractId = nil
		err := ValidateSACEvent(ev, native, pass)
		require.True(t, errors.Is(err, ErrInvalidSACEvent))
	})
}
