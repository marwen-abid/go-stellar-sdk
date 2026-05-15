package contract

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// IsReadCall reports whether the simulated transaction is a read-only view
// call: simulation returned no Address-credentialed authorization entries
// (only SourceAccount, or none at all), and the SorobanTransactionData
// footprint touches no read-write ledger keys. Such calls can be served
// directly from ReturnValue without submission.
//
// IsReadCall is conservative: when no simulation has been run yet (or the
// transaction carries no SorobanData), it returns false.
func (a *AssembledTransaction) IsReadCall() bool {
	if a == nil || a.Simulation == nil {
		return false
	}
	// Any Address-credentialed auth entry means signatures are required.
	for _, entry := range a.AuthEntries {
		if entry.Credentials.Type == xdr.SorobanCredentialsTypeSorobanCredentialsAddress {
			return false
		}
	}
	// A non-empty ReadWrite footprint means the call writes state.
	if a.op == nil || a.op.Ext.SorobanData == nil {
		return false
	}
	return len(a.op.Ext.SorobanData.Resources.Footprint.ReadWrite) == 0
}

// NeedsNonInvokerSigningBy returns the deduplicated set of strkey-encoded
// addresses whose Address-type authorization entries still require a
// signature (or, when includeAlreadySigned is true, every Address-type
// authorization entry's address, signed or not). The source account is
// implicitly authorized by the transaction envelope signature and never
// appears in the returned list.
//
// Order is the order of first appearance in the simulation's auth entry
// slice; duplicates are dropped.
//
// Returns an *Error wrapping ErrNotYetSimulated when Simulate has not run.
func (a *AssembledTransaction) NeedsNonInvokerSigningBy(includeAlreadySigned bool) ([]string, error) {
	if a == nil || a.Simulation == nil {
		return nil, &Error{Kind: KindNotYetSimulated, Details: "NeedsNonInvokerSigningBy"}
	}

	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, entry := range a.AuthEntries {
		if entry.Credentials.Type != xdr.SorobanCredentialsTypeSorobanCredentialsAddress {
			continue
		}
		ac := entry.Credentials.MustAddress()
		if !includeAlreadySigned && ac.Signature.Type != xdr.ScValTypeScvVoid {
			continue
		}
		addr, err := ac.Address.String()
		if err != nil {
			return nil, fmt.Errorf("contract: NeedsNonInvokerSigningBy: encode address: %w", err)
		}
		if _, dup := seen[addr]; dup {
			continue
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
	}
	return out, nil
}

// SignAuthEntries iterates the simulation's authorization entries and, for
// each Address-type entry whose credentialed address matches signer.Address(),
// builds the canonical HashIdPreimageSorobanAuthorization, asks the signer to
// sign it, and writes the resulting Soroban-account-signature ScVal plus
// signatureExpirationLedger back into the entry's credentials. Both the
// AssembledTransaction's cached slice (a.AuthEntries) and the underlying
// op.Auth are updated so the next Sign / Send rebuild picks them up.
//
// The signature ScVal is the canonical SCV_VEC<SCV_MAP{public_key, signature}>
// payload that Stellar's default account contract validates against. Hardware
// wallets, smart contracts, and other custom Signer implementations are free
// to substitute a different ScVal via their SignAuthEntryPreimage; this method
// always wraps the returned raw signature in the canonical envelope.
//
// expirationLedger sets SignatureExpirationLedger on each signed entry. The
// caller is responsible for choosing a value far enough in the future that
// the transaction can plausibly land before expiry (the JS SDK defaults to
// current ledger + 100).
//
// Entries credentialed by SourceAccount are skipped: they are authorized
// implicitly by the envelope signature. Entries credentialed by a different
// address are also skipped.
//
// Returns an *Error wrapping ErrNotYetSimulated when called before Simulate.
func (a *AssembledTransaction) SignAuthEntries(ctx context.Context, signer Signer, expirationLedger uint32) error {
	if a == nil || a.Simulation == nil {
		return &Error{Kind: KindNotYetSimulated, Details: "SignAuthEntries"}
	}
	if signer == nil {
		return invalidArgsf("SignAuthEntries: signer is nil")
	}
	if ctx == nil {
		return invalidArgsf("SignAuthEntries: ctx is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	signerAddr := signer.Address()
	pubKeyBytes, err := strkey.Decode(strkey.VersionByteAccountID, signerAddr)
	if err != nil {
		return invalidArgsf("SignAuthEntries: signer.Address() %q is not a G... account strkey: %v", signerAddr, err)
	}

	networkID := xdr.Hash(network.ID(a.network))

	for i := range a.AuthEntries {
		entry := &a.AuthEntries[i]
		if entry.Credentials.Type != xdr.SorobanCredentialsTypeSorobanCredentialsAddress {
			continue
		}
		ac := entry.Credentials.Address
		if ac == nil {
			return invalidArgsf("SignAuthEntries: auth entry %d has nil Address credentials", i)
		}
		addr, addrErr := ac.Address.String()
		if addrErr != nil {
			return fmt.Errorf("contract: SignAuthEntries: encode entry %d address: %w", i, addrErr)
		}
		if addr != signerAddr {
			continue
		}

		preimage := xdr.HashIdPreimage{
			Type: xdr.EnvelopeTypeEnvelopeTypeSorobanAuthorization,
			SorobanAuthorization: &xdr.HashIdPreimageSorobanAuthorization{
				NetworkId:                 networkID,
				Nonce:                     ac.Nonce,
				SignatureExpirationLedger: xdr.Uint32(expirationLedger),
				Invocation:                entry.RootInvocation,
			},
		}
		sig, sigErr := signer.SignAuthEntryPreimage(a.network, preimage)
		if sigErr != nil {
			return fmt.Errorf("contract: SignAuthEntries: sign entry %d: %w", i, sigErr)
		}

		sigVal, sigValErr := buildAccountSignatureScVal(pubKeyBytes, []byte(sig))
		if sigValErr != nil {
			return fmt.Errorf("contract: SignAuthEntries: encode signature ScVal for entry %d: %w", i, sigValErr)
		}

		ac.SignatureExpirationLedger = xdr.Uint32(expirationLedger)
		ac.Signature = sigVal
	}

	// Mirror the cached slice onto the underlying op so that later
	// envelope rebuilds (Simulate, Sign, Send) carry the signatures.
	if a.op != nil {
		a.op.Auth = append([]xdr.SorobanAuthorizationEntry(nil), a.AuthEntries...)
		rebuilt, rebuildErr := buildTx(a.source, a.op, a.baseFee, a.memo, a.preconditions)
		if rebuildErr != nil {
			return fmt.Errorf("contract: SignAuthEntries: rebuild transaction: %w", rebuildErr)
		}
		// Preserve any envelope signatures already on a.Built (Sign may
		// have run first for partial-handoff workflows).
		for _, existing := range a.Built.Signatures() {
			rebuilt, rebuildErr = rebuilt.AddSignatureDecorated(existing)
			if rebuildErr != nil {
				return fmt.Errorf("contract: SignAuthEntries: preserve existing signature: %w", rebuildErr)
			}
		}
		a.Built = rebuilt
	}

	return nil
}

// Sign appends a signature from signer to the envelope. The method is
// idempotent: if a decorated signature with the signer's hint is already
// present on a.Built, Sign returns without re-signing.
//
// Returns an *Error wrapping ErrNotYetSimulated when called before Simulate
// (Sign must run after the simulation has folded the final footprint, auth,
// and resource fee into the envelope).
func (a *AssembledTransaction) Sign(signer Signer) error {
	if a == nil || a.Simulation == nil || a.Built == nil {
		return &Error{Kind: KindNotYetSimulated, Details: "Sign"}
	}
	if signer == nil {
		return invalidArgsf("Sign: signer is nil")
	}

	pubKP, err := keypair.ParseAddress(signer.Address())
	if err != nil {
		return invalidArgsf("Sign: signer.Address() %q is not an account strkey: %v", signer.Address(), err)
	}
	hint := pubKP.Hint()
	for _, sig := range a.Built.Signatures() {
		if sig.Hint == xdr.SignatureHint(hint) {
			return nil
		}
	}

	signed, err := signer.SignTransaction(a.network, a.Built)
	if err != nil {
		return fmt.Errorf("contract: Sign: %w", err)
	}
	if signed == nil {
		return invalidArgsf("Sign: signer returned nil transaction")
	}
	a.Built = signed
	return nil
}

// buildAccountSignatureScVal constructs the canonical Soroban
// account-contract signature ScVal: a vector of one map with
// { public_key: BytesN<32>, signature: BytesN<64> }. Map keys are emitted
// in lexicographic order ("public_key" < "signature") per ScMap canonical
// encoding rules.
func buildAccountSignatureScVal(pubKey, signature []byte) (xdr.ScVal, error) {
	if len(pubKey) != 32 {
		return xdr.ScVal{}, fmt.Errorf("public key length %d, want 32", len(pubKey))
	}
	if len(signature) != 64 {
		return xdr.ScVal{}, fmt.Errorf("signature length %d, want 64", len(signature))
	}
	entry, err := xdr.ScvMap(map[string]xdr.ScVal{
		"public_key": xdr.ScvBytes(append([]byte(nil), pubKey...)),
		"signature":  xdr.ScvBytes(append([]byte(nil), signature...)),
	})
	if err != nil {
		return xdr.ScVal{}, err
	}
	return xdr.ScvVec(entry), nil
}
