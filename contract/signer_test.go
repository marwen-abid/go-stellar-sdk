package contract

import (
	"crypto/ed25519"
	"testing"

	"github.com/stellar/go-stellar-sdk/hash"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compile-time check that the adapter satisfies the interface.
var _ Signer = (*keypairSigner)(nil)

func TestKeypairSigner_Address(t *testing.T) {
	kp := keypair.MustRandom()
	s := KeypairSigner(kp)
	assert.Equal(t, kp.Address(), s.Address())
}

func TestKeypairSigner_SignTransaction(t *testing.T) {
	kp := keypair.MustRandom()
	src := txnbuild.NewSimpleAccount(kp.Address(), 1)
	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount: &src,
		Operations: []txnbuild.Operation{&txnbuild.BumpSequence{
			BumpTo: 10,
		}},
		Preconditions: txnbuild.Preconditions{TimeBounds: txnbuild.NewInfiniteTimeout()},
		BaseFee:       txnbuild.MinBaseFee,
	})
	require.NoError(t, err)
	require.Empty(t, tx.Signatures())

	signed, err := KeypairSigner(kp).SignTransaction(network.TestNetworkPassphrase, tx)
	require.NoError(t, err)
	require.Len(t, signed.Signatures(), 1)
	// Original tx is not mutated.
	assert.Empty(t, tx.Signatures())
}

func TestKeypairSigner_SignTransaction_NilTx(t *testing.T) {
	_, err := KeypairSigner(keypair.MustRandom()).SignTransaction(network.TestNetworkPassphrase, nil)
	assert.Error(t, err)
}

func TestKeypairSigner_SignAuthEntryPreimage(t *testing.T) {
	kp := keypair.MustRandom()
	preimage := xdr.HashIdPreimage{
		Type: xdr.EnvelopeTypeEnvelopeTypeSorobanAuthorization,
		SorobanAuthorization: &xdr.HashIdPreimageSorobanAuthorization{
			NetworkId:                 xdr.Hash(hash.Hash([]byte(network.TestNetworkPassphrase))),
			Nonce:                     42,
			SignatureExpirationLedger: 100,
			Invocation: xdr.SorobanAuthorizedInvocation{
				Function: xdr.SorobanAuthorizedFunction{
					Type: xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn,
					ContractFn: &xdr.InvokeContractArgs{
						ContractAddress: xdr.ScAddress{
							Type:      xdr.ScAddressTypeScAddressTypeAccount,
							AccountId: xdr.MustAddressPtr(kp.Address()),
						},
						FunctionName: "noop",
					},
				},
			},
		},
	}

	sig, err := KeypairSigner(kp).SignAuthEntryPreimage(network.TestNetworkPassphrase, preimage)
	require.NoError(t, err)
	assert.Len(t, []byte(sig), ed25519.SignatureSize)

	// Sanity-check: signature verifies against ed25519 over the canonical digest.
	raw, err := preimage.MarshalBinary()
	require.NoError(t, err)
	digest := hash.Hash(raw)
	pub, err := keypair.ParseAddress(kp.Address())
	require.NoError(t, err)
	require.NoError(t, pub.Verify(digest[:], sig))
}
