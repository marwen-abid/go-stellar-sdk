// Package contract provides high-level helpers for interacting with Soroban
// smart contracts. It mirrors the surface of the JavaScript SDK's contract
// client and is intentionally small; concrete client/lifecycle types land in
// later phases of the refactor.
package contract

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/hash"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// Signer abstracts the act of signing transactions and Soroban authorization
// entries. The interface matches the JS SDK's signer shape: a stable
// account-address accessor plus two signing primitives the higher-level
// AssembledTransaction lifecycle calls into. Browser, HSM, and hardware-wallet
// integrations satisfy Signer directly.
type Signer interface {
	// Address returns the strkey-encoded account that this signer signs as
	// (either a "G..." ed25519 account or a "M..." muxed account).
	Address() string

	// SignTransaction returns a copy of tx with this signer's signature
	// appended. The network passphrase is required so signatures are bound
	// to a specific network.
	SignTransaction(network string, tx *txnbuild.Transaction) (*txnbuild.Transaction, error)

	// SignAuthEntryPreimage signs the SHA-256 hash of the canonical
	// XDR-encoded HashIdPreimage produced for a Soroban authorization
	// entry. Callers wrap the returned signature in the appropriate
	// SorobanAuthorizationEntry payload.
	SignAuthEntryPreimage(network string, preimage xdr.HashIdPreimage) (xdr.Signature, error)
}

// KeypairSigner adapts a *keypair.Full into a Signer. It is the default
// software-key implementation; production wallets typically supply their own.
func KeypairSigner(kp *keypair.Full) Signer {
	return keypairSigner{kp: kp}
}

type keypairSigner struct {
	kp *keypair.Full
}

func (s keypairSigner) Address() string {
	return s.kp.Address()
}

func (s keypairSigner) SignTransaction(network string, tx *txnbuild.Transaction) (*txnbuild.Transaction, error) {
	if tx == nil {
		return nil, fmt.Errorf("contract: SignTransaction: tx is nil")
	}
	return tx.Sign(network, s.kp)
}

func (s keypairSigner) SignAuthEntryPreimage(network string, preimage xdr.HashIdPreimage) (xdr.Signature, error) {
	raw, err := preimage.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("contract: marshal HashIdPreimage: %w", err)
	}
	digest := hash.Hash(raw)
	sig, err := s.kp.Sign(digest[:])
	if err != nil {
		return nil, fmt.Errorf("contract: sign auth preimage: %w", err)
	}
	return xdr.Signature(sig), nil
}
