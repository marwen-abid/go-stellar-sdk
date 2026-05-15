package asset

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/stellar/go-stellar-sdk/contract"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ErrInvalidTokenOpArg is returned by the Token operation methods (Balance,
// Decimals, Symbol, Name, Mint, Burn, Approve, Allowance, Authorized,
// SetAuthorized) when their arguments fail pre-flight validation — malformed
// strkey, nil amount, etc. Callers can branch on it via errors.Is.
var ErrInvalidTokenOpArg = errors.New("asset: invalid Token operation argument")

// Balance returns the SAC `balance(id)` view for the given account or
// contract. The result is the SEP-41 i128 balance decoded into *big.Int.
//
// Balance is read-only: it does not require a Signer. `who` must be a valid
// G/M/C strkey; otherwise Balance returns an error wrapping
// ErrInvalidTokenOpArg.
func (t *Token) Balance(ctx context.Context, who string) (*big.Int, error) {
	if err := t.checkClient("Balance"); err != nil {
		return nil, err
	}
	whoScv, err := xdr.ScvAddress(who)
	if err != nil {
		return nil, fmt.Errorf("%w: Balance: encode who: %v", ErrInvalidTokenOpArg, err)
	}
	v, err := t.invokeView(ctx, "balance", []xdr.ScVal{whoScv})
	if err != nil {
		return nil, err
	}
	return asBigInt(v, "Balance")
}

// Decimals returns the SAC `decimals()` view as a uint32. It is read-only and
// requires no Signer.
func (t *Token) Decimals(ctx context.Context) (uint32, error) {
	if err := t.checkClient("Decimals"); err != nil {
		return 0, err
	}
	v, err := t.invokeView(ctx, "decimals", []xdr.ScVal{})
	if err != nil {
		return 0, err
	}
	u, ok := v.(uint32)
	if !ok {
		return 0, fmt.Errorf("%w: Decimals: unexpected return %T", ErrInvalidTokenOpArg, v)
	}
	return u, nil
}

// Symbol returns the SAC `symbol()` view. Read-only, no Signer required.
func (t *Token) Symbol(ctx context.Context) (string, error) {
	if err := t.checkClient("Symbol"); err != nil {
		return "", err
	}
	v, err := t.invokeView(ctx, "symbol", []xdr.ScVal{})
	if err != nil {
		return "", err
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%w: Symbol: unexpected return %T", ErrInvalidTokenOpArg, v)
	}
	return s, nil
}

// Name returns the SAC `name()` view as a string. Read-only, no Signer
// required. For classic assets this is the asset's canonical descriptor
// (e.g. `"USDC:GA...EXAMPLE"`).
func (t *Token) Name(ctx context.Context) (string, error) {
	if err := t.checkClient("Name"); err != nil {
		return "", err
	}
	v, err := t.invokeView(ctx, "name", []xdr.ScVal{})
	if err != nil {
		return "", err
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%w: Name: unexpected return %T", ErrInvalidTokenOpArg, v)
	}
	return s, nil
}

// Authorized returns the SAC `authorized(id)` view: whether `id` is currently
// authorized to hold and transfer the asset. Read-only, no Signer required.
// `id` must be a valid G/M/C strkey; otherwise Authorized returns an error
// wrapping ErrInvalidTokenOpArg.
func (t *Token) Authorized(ctx context.Context, id string) (bool, error) {
	if err := t.checkClient("Authorized"); err != nil {
		return false, err
	}
	idScv, err := xdr.ScvAddress(id)
	if err != nil {
		return false, fmt.Errorf("%w: Authorized: encode id: %v", ErrInvalidTokenOpArg, err)
	}
	v, err := t.invokeView(ctx, "authorized", []xdr.ScVal{idScv})
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%w: Authorized: unexpected return %T", ErrInvalidTokenOpArg, v)
	}
	return b, nil
}

// Allowance returns the remaining SEP-41 allowance `spender` may transfer on
// behalf of `from`. Read-only, no Signer required.
func (t *Token) Allowance(ctx context.Context, from, spender string) (*big.Int, error) {
	if err := t.checkClient("Allowance"); err != nil {
		return nil, err
	}
	fromScv, err := xdr.ScvAddress(from)
	if err != nil {
		return nil, fmt.Errorf("%w: Allowance: encode from: %v", ErrInvalidTokenOpArg, err)
	}
	spenderScv, err := xdr.ScvAddress(spender)
	if err != nil {
		return nil, fmt.Errorf("%w: Allowance: encode spender: %v", ErrInvalidTokenOpArg, err)
	}
	v, err := t.invokeView(ctx, "allowance", []xdr.ScVal{fromScv, spenderScv})
	if err != nil {
		return nil, err
	}
	return asBigInt(v, "Allowance")
}

// Mint invokes the SAC `mint(to, amount)` admin function. The returned
// *contract.AssembledTransaction must be SignAndSend-ed by the token admin to
// take effect. amount is encoded as SCV_I128.
func (t *Token) Mint(
	ctx context.Context,
	to string,
	amount *big.Int,
	opts ...contract.InvokeOption,
) (*contract.AssembledTransaction, error) {
	if err := t.checkClient("Mint"); err != nil {
		return nil, err
	}
	if amount == nil {
		return nil, fmt.Errorf("%w: Mint: amount is nil", ErrInvalidTokenOpArg)
	}
	toScv, err := xdr.ScvAddress(to)
	if err != nil {
		return nil, fmt.Errorf("%w: Mint: encode to: %v", ErrInvalidTokenOpArg, err)
	}
	amountScv, err := xdr.ScvI128(amount)
	if err != nil {
		return nil, fmt.Errorf("%w: Mint: encode amount: %v", ErrInvalidTokenOpArg, err)
	}
	return t.client.Invoke(ctx, "mint", []xdr.ScVal{toScv, amountScv}, opts...)
}

// Burn invokes the SAC `burn(from, amount)` function, which destroys
// `amount` of the token held by `from`. Auth is enforced by the contract
// against `from`. The returned *contract.AssembledTransaction must be
// SignAndSend-ed.
func (t *Token) Burn(
	ctx context.Context,
	from string,
	amount *big.Int,
	opts ...contract.InvokeOption,
) (*contract.AssembledTransaction, error) {
	if err := t.checkClient("Burn"); err != nil {
		return nil, err
	}
	if amount == nil {
		return nil, fmt.Errorf("%w: Burn: amount is nil", ErrInvalidTokenOpArg)
	}
	fromScv, err := xdr.ScvAddress(from)
	if err != nil {
		return nil, fmt.Errorf("%w: Burn: encode from: %v", ErrInvalidTokenOpArg, err)
	}
	amountScv, err := xdr.ScvI128(amount)
	if err != nil {
		return nil, fmt.Errorf("%w: Burn: encode amount: %v", ErrInvalidTokenOpArg, err)
	}
	// When `from` is an account-shaped strkey, prepend it as Source so the
	// transaction envelope matches the on-chain authorizer — mirrors
	// Transfer's behavior. Caller-supplied opts can still override.
	callOpts := make([]contract.InvokeOption, 0, len(opts)+1)
	if classifyTransferAddress(from) == transferAddrAccount {
		callOpts = append(callOpts, contract.Source(from))
	}
	callOpts = append(callOpts, opts...)
	return t.client.Invoke(ctx, "burn", []xdr.ScVal{fromScv, amountScv}, callOpts...)
}

// Approve invokes the SAC `approve(from, spender, amount, expiration_ledger)`
// function. liveUntilLedger is the ledger number at which the allowance
// expires (SEP-41 expiration_ledger). amount is SCV_I128.
func (t *Token) Approve(
	ctx context.Context,
	from, spender string,
	amount *big.Int,
	liveUntilLedger uint32,
	opts ...contract.InvokeOption,
) (*contract.AssembledTransaction, error) {
	if err := t.checkClient("Approve"); err != nil {
		return nil, err
	}
	if amount == nil {
		return nil, fmt.Errorf("%w: Approve: amount is nil", ErrInvalidTokenOpArg)
	}
	fromScv, err := xdr.ScvAddress(from)
	if err != nil {
		return nil, fmt.Errorf("%w: Approve: encode from: %v", ErrInvalidTokenOpArg, err)
	}
	spenderScv, err := xdr.ScvAddress(spender)
	if err != nil {
		return nil, fmt.Errorf("%w: Approve: encode spender: %v", ErrInvalidTokenOpArg, err)
	}
	amountScv, err := xdr.ScvI128(amount)
	if err != nil {
		return nil, fmt.Errorf("%w: Approve: encode amount: %v", ErrInvalidTokenOpArg, err)
	}
	expScv := xdr.ScVal{
		Type: xdr.ScValTypeScvU32,
		U32:  (*xdr.Uint32)(&liveUntilLedger),
	}
	callOpts := make([]contract.InvokeOption, 0, len(opts)+1)
	if classifyTransferAddress(from) == transferAddrAccount {
		callOpts = append(callOpts, contract.Source(from))
	}
	callOpts = append(callOpts, opts...)
	return t.client.Invoke(ctx, "approve", []xdr.ScVal{fromScv, spenderScv, amountScv, expScv}, callOpts...)
}

// SetAuthorized invokes the SAC `set_authorized(admin, id, authorize)` admin
// function, flipping the authorization flag for `id`. The returned
// *contract.AssembledTransaction must be SignAndSend-ed by the token admin to
// take effect.
func (t *Token) SetAuthorized(
	ctx context.Context,
	admin, id string,
	authorize bool,
	opts ...contract.InvokeOption,
) (*contract.AssembledTransaction, error) {
	if err := t.checkClient("SetAuthorized"); err != nil {
		return nil, err
	}
	adminScv, err := xdr.ScvAddress(admin)
	if err != nil {
		return nil, fmt.Errorf("%w: SetAuthorized: encode admin: %v", ErrInvalidTokenOpArg, err)
	}
	idScv, err := xdr.ScvAddress(id)
	if err != nil {
		return nil, fmt.Errorf("%w: SetAuthorized: encode id: %v", ErrInvalidTokenOpArg, err)
	}
	authScv := xdr.ScvBool(authorize)
	callOpts := make([]contract.InvokeOption, 0, len(opts)+1)
	if classifyTransferAddress(admin) == transferAddrAccount {
		callOpts = append(callOpts, contract.Source(admin))
	}
	callOpts = append(callOpts, opts...)
	return t.client.Invoke(ctx, "set_authorized", []xdr.ScVal{adminScv, idScv, authScv}, callOpts...)
}

// checkClient guards every Token operation against a nil receiver or
// missing contract.Client (zero-value Token).
func (t *Token) checkClient(op string) error {
	if t == nil {
		return fmt.Errorf("%w: %s called on nil Token", ErrInvalidTokenOpArg, op)
	}
	if t.client == nil {
		return fmt.Errorf("%w: %s: Token has no underlying contract.Client", ErrInvalidTokenOpArg, op)
	}
	return nil
}

// invokeView runs a read-only SAC function via the wrapped contract.Client
// and returns the decoded native Go value from the simulation. It does NOT
// require a Signer — the AssembledTransaction is observed via Result() only,
// no envelope is signed or submitted. Non-read calls surface an error.
func (t *Token) invokeView(ctx context.Context, method string, args []xdr.ScVal) (any, error) {
	at, err := t.client.Invoke(ctx, method, args)
	if err != nil {
		return nil, err
	}
	if !at.IsReadCall() {
		return nil, fmt.Errorf("%w: %s: simulation reported a non-read call", ErrInvalidTokenOpArg, method)
	}
	return at.Result()
}

// asBigInt narrows an `any` returned by Spec.FuncResToNative for an i128
// output. Returns ErrInvalidTokenOpArg-wrapped if the type does not match.
func asBigInt(v any, op string) (*big.Int, error) {
	b, ok := v.(*big.Int)
	if !ok {
		return nil, fmt.Errorf("%w: %s: unexpected return %T", ErrInvalidTokenOpArg, op, v)
	}
	return b, nil
}
