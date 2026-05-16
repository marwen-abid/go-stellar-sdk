package contract

import (
	"fmt"
	"regexp"
	"strconv"
)

// ErrorKind classifies an *Error so callers can branch on the lifecycle stage
// or invariant that produced it without inspecting message strings.
type ErrorKind int

const (
	// KindUnknown is the zero value and indicates the kind was not set.
	KindUnknown ErrorKind = iota
	// KindSimulationFailed: the RPC simulateTransaction call returned an error.
	KindSimulationFailed
	// KindRestoreRequired: simulation reported archived footprint entries that
	// must be restored before the transaction can succeed.
	KindRestoreRequired
	// KindNeedsMoreSignatures: an authorization entry still lacks a required
	// signature.
	KindNeedsMoreSignatures
	// KindAuthMissing: the transaction has no Soroban authorization entries
	// where one was expected.
	KindAuthMissing
	// KindContractRevert: the contract panicked or returned a Result::Err.
	// The Error wraps a *ContractRevertError as its cause.
	KindContractRevert
	// KindSubmissionFailed: the RPC sendTransaction call or the on-chain
	// inclusion path returned a failure status.
	KindSubmissionFailed
	// KindTimeout: a poll loop (simulation, send, get-transaction) exceeded
	// its deadline.
	KindTimeout
	// KindNotYetSimulated: the caller invoked an operation that requires
	// simulation results before simulation has been run.
	KindNotYetSimulated
	// KindInvalidArgs: caller-supplied arguments failed validation before any
	// network call was made.
	KindInvalidArgs
	// KindTransactionFailed: the transaction was included in a ledger with
	// a FAILED status. Distinct from KindSubmissionFailed (which covers RPC
	// sendTransaction rejection) — here submission succeeded but on-chain
	// execution returned an error.
	KindTransactionFailed
)

// String returns a stable, lower-case identifier for the kind, suitable for
// log fields and error messages.
func (k ErrorKind) String() string {
	switch k {
	case KindSimulationFailed:
		return "simulation_failed"
	case KindRestoreRequired:
		return "restore_required"
	case KindNeedsMoreSignatures:
		return "needs_more_signatures"
	case KindAuthMissing:
		return "auth_missing"
	case KindContractRevert:
		return "contract_revert"
	case KindSubmissionFailed:
		return "submission_failed"
	case KindTimeout:
		return "timeout"
	case KindNotYetSimulated:
		return "not_yet_simulated"
	case KindInvalidArgs:
		return "invalid_args"
	case KindTransactionFailed:
		return "transaction_failed"
	case KindUnknown:
		fallthrough
	default:
		return "unknown"
	}
}

// Error is the canonical error type returned by the contract package. It
// carries a machine-readable Kind plus an optional human-readable Details
// string and underlying cause. Callers should match on Kind via errors.Is
// against one of the package sentinels, and unwrap with errors.As / errors.Unwrap
// to retrieve typed causes such as *ContractRevertError.
type Error struct {
	Kind    ErrorKind
	Details string
	cause   error
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	switch {
	case e.Details != "" && e.cause != nil:
		return fmt.Sprintf("contract: %s: %s: %v", e.Kind, e.Details, e.cause)
	case e.Details != "":
		return fmt.Sprintf("contract: %s: %s", e.Kind, e.Details)
	case e.cause != nil:
		return fmt.Sprintf("contract: %s: %v", e.Kind, e.cause)
	default:
		return fmt.Sprintf("contract: %s", e.Kind)
	}
}

// Unwrap exposes the underlying cause for errors.As / errors.Unwrap.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Is reports whether the target matches this error. Two *Error values match
// when they share the same Kind, which lets the package-level sentinels act
// as classifiers via errors.Is.
func (e *Error) Is(target error) bool {
	if e == nil || target == nil {
		return false
	}
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	return e.Kind == t.Kind
}

// Sentinels for errors.Is. Each is a zero-Details *Error pinned to a Kind;
// real errors built inside the package are matched against these constants.
var (
	ErrSimulationFailed    = &Error{Kind: KindSimulationFailed}
	ErrRestoreRequired     = &Error{Kind: KindRestoreRequired}
	ErrNeedsMoreSignatures = &Error{Kind: KindNeedsMoreSignatures}
	ErrAuthMissing         = &Error{Kind: KindAuthMissing}
	ErrSubmissionFailed    = &Error{Kind: KindSubmissionFailed}
	ErrTimeout             = &Error{Kind: KindTimeout}
	ErrNotYetSimulated     = &Error{Kind: KindNotYetSimulated}
	ErrTransactionFailed   = &Error{Kind: KindTransactionFailed}
)

// ContractRevertError is returned (wrapped in an *Error with KindContractRevert)
// when a Soroban contract panics or returns a Result::Err. Name is resolved
// against the contract spec when one is available; otherwise it is empty and
// callers should fall back to Code.
type ContractRevertError struct {
	ContractID string
	Code       int32
	Name       string
	RawXDR     string
}

// Error implements the error interface.
func (e *ContractRevertError) Error() string {
	if e == nil {
		return "<nil>"
	}
	name := e.Name
	if name == "" {
		name = "ContractError"
	}
	if e.ContractID != "" {
		return fmt.Sprintf("%s (code %d) at %s", name, e.Code, e.ContractID)
	}
	return fmt.Sprintf("%s (code %d)", name, e.Code)
}

// Compile-time interface checks.
var (
	_ error = (*Error)(nil)
	_ error = (*ContractRevertError)(nil)
)

// contractRevertPattern matches the "Error(Contract, #N)" fragment that
// soroban-rpc embeds in the SimulateTransactionResponse.Error string when a
// contract returns Result::Err or panics with a #[contracterror] code. It
// mirrors the regex used by the JS SDK so the two stay behaviourally aligned.
var contractRevertPattern = regexp.MustCompile(`Error\(Contract, #(\d+)\)`)

// parseContractRevert inspects a simulation-error string and, when it carries
// an "Error(Contract, #N)" fragment, returns (N, true). Otherwise it returns
// (0, false). The fragment is the canonical way the Soroban host surfaces a
// user-defined #[contracterror] code through the JSON-RPC error channel.
func parseContractRevert(msg string) (int32, bool) {
	m := contractRevertPattern.FindStringSubmatch(msg)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseUint(m[1], 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(n), true
}

// newContractRevertError builds a *ContractRevertError for code, resolving its
// Name against the spec's ErrorCases() when available. spec and contractID may
// be empty/nil — callers will still receive a usefully formatted error.
func newContractRevertError(spec *Spec, contractID string, code int32) *ContractRevertError {
	rev := &ContractRevertError{ContractID: contractID, Code: code}
	if spec == nil {
		return rev
	}
	for _, c := range spec.ErrorCases() {
		if int32(c.Value) == code {
			rev.Name = c.Name
			break
		}
	}
	return rev
}
