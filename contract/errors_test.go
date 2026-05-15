package contract

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time confirmation that the exported types satisfy the error
// interface.
var (
	_ error = (*Error)(nil)
	_ error = (*ContractRevertError)(nil)
)

// allSentinels enumerates every package-level *Error sentinel together with
// the Kind it advertises. The tests below iterate this table so adding a new
// sentinel forces the author to declare its kind here.
var allSentinels = []struct {
	name     string
	sentinel *Error
	kind     ErrorKind
}{
	{"ErrSimulationFailed", ErrSimulationFailed, KindSimulationFailed},
	{"ErrRestoreRequired", ErrRestoreRequired, KindRestoreRequired},
	{"ErrNeedsMoreSignatures", ErrNeedsMoreSignatures, KindNeedsMoreSignatures},
	{"ErrAuthMissing", ErrAuthMissing, KindAuthMissing},
	{"ErrTimeout", ErrTimeout, KindTimeout},
	{"ErrNotYetSimulated", ErrNotYetSimulated, KindNotYetSimulated},
}

// --- ErrorKind.String -------------------------------------------------------

func TestErrorKind_String(t *testing.T) {
	cases := []struct {
		kind ErrorKind
		want string
	}{
		{KindUnknown, "unknown"},
		{KindSimulationFailed, "simulation_failed"},
		{KindRestoreRequired, "restore_required"},
		{KindNeedsMoreSignatures, "needs_more_signatures"},
		{KindAuthMissing, "auth_missing"},
		{KindContractRevert, "contract_revert"},
		{KindSubmissionFailed, "submission_failed"},
		{KindTimeout, "timeout"},
		{KindNotYetSimulated, "not_yet_simulated"},
		{KindInvalidArgs, "invalid_args"},
		// Out-of-range values fall through to "unknown" so callers logging
		// the kind never see a numeric int.
		{ErrorKind(999), "unknown"},
		{ErrorKind(-1), "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			require.Equal(t, tc.want, tc.kind.String())
		})
	}
}

// --- errors.Is sentinel matching -------------------------------------------

func TestError_Is_SentinelRoundTrip(t *testing.T) {
	for _, tc := range allSentinels {
		t.Run(tc.name, func(t *testing.T) {
			// A freshly-constructed *Error of the same Kind matches the
			// sentinel regardless of Details/cause.
			built := &Error{Kind: tc.kind, Details: "ctx", cause: fmt.Errorf("boom")}
			require.True(t, errors.Is(built, tc.sentinel),
				"errors.Is(built, %s) should be true", tc.name)

			// And it still matches after being wrapped through fmt.Errorf %w.
			wrapped := fmt.Errorf("outer: %w", built)
			require.True(t, errors.Is(wrapped, tc.sentinel),
				"errors.Is(wrapped, %s) should be true", tc.name)
		})
	}
}

func TestError_Is_DifferentKindDoesNotMatch(t *testing.T) {
	err := &Error{Kind: KindSimulationFailed}
	require.False(t, errors.Is(err, ErrTimeout))
	require.False(t, errors.Is(err, ErrAuthMissing))
}

func TestError_Is_NilSafety(t *testing.T) {
	var nilErr *Error
	// nil receiver should not panic and should not match anything.
	require.False(t, nilErr.Is(ErrTimeout))
	// Non-nil receiver vs nil target also must not match.
	require.False(t, ErrTimeout.Is(nil))
}

func TestError_Is_NonErrorTargetDoesNotMatch(t *testing.T) {
	// errors.Is against an unrelated error type must return false (not panic).
	other := fmt.Errorf("plain error")
	require.False(t, errors.Is(ErrTimeout, other))
}

func TestError_Is_UnknownKindDoesNotMatchSentinels(t *testing.T) {
	// A zero-Kind *Error (KindUnknown) should not collide with any sentinel.
	zero := &Error{}
	for _, tc := range allSentinels {
		require.False(t, errors.Is(zero, tc.sentinel),
			"zero *Error should not match %s", tc.name)
	}
}

// --- errors.As typed cause extraction --------------------------------------

func TestError_As_ContractRevert_Direct(t *testing.T) {
	revert := &ContractRevertError{ContractID: "CAAA", Code: 7, Name: "ErrInsufficientBalance"}
	wrapped := &Error{Kind: KindContractRevert, cause: revert}

	var got *ContractRevertError
	require.True(t, errors.As(wrapped, &got))
	assert.Same(t, revert, got)
}

func TestError_As_ContractRevert_ThroughFmtWrap(t *testing.T) {
	// errors.As must traverse a fmt.Errorf("%w") wrap layered on top of an
	// *Error that wraps a *ContractRevertError.
	revert := &ContractRevertError{ContractID: "CBBB", Code: 13, Name: "ErrOverflow"}
	outer := fmt.Errorf("submit: %w", &Error{Kind: KindContractRevert, cause: revert})

	var got *ContractRevertError
	require.True(t, errors.As(outer, &got))
	assert.Same(t, revert, got)
}

func TestError_As_ExtractsErrorItself(t *testing.T) {
	// errors.As targeting **Error pulls the contract *Error out of an outer
	// fmt.Errorf wrap — this is how callers read the Kind off a wrapped error.
	inner := &Error{Kind: KindSubmissionFailed, Details: "ledger 123 not found"}
	outer := fmt.Errorf("rpc: %w", inner)

	var got *Error
	require.True(t, errors.As(outer, &got))
	require.Same(t, inner, got)
	require.Equal(t, KindSubmissionFailed, got.Kind)
}

func TestError_As_NoContractRevertWhenAbsent(t *testing.T) {
	// An *Error whose cause is not a *ContractRevertError must not yield one
	// via errors.As.
	err := &Error{Kind: KindSimulationFailed, cause: fmt.Errorf("rpc 500")}
	var got *ContractRevertError
	require.False(t, errors.As(err, &got))
	require.Nil(t, got)
}

// --- Unwrap -----------------------------------------------------------------

func TestError_Unwrap_ReturnsCause(t *testing.T) {
	cause := fmt.Errorf("underlying")
	err := &Error{Kind: KindSimulationFailed, cause: cause}
	require.Same(t, cause, errors.Unwrap(err))
}

func TestError_Unwrap_NilWhenNoCause(t *testing.T) {
	err := &Error{Kind: KindAuthMissing, Details: "no entries"}
	require.NoError(t, errors.Unwrap(err))
}

func TestError_Unwrap_NilReceiver(t *testing.T) {
	var nilErr *Error
	require.NoError(t, nilErr.Unwrap())
}

// --- Kind classification helper --------------------------------------------
//
// §4.7 does not prescribe an IsKind helper; the canonical pattern is to pull
// the *Error out via errors.As and read its Kind. These tests pin that
// pattern so it stays usable as the public classification path.

func TestError_KindClassification_ViaAs(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorKind
	}{
		{"simulation", &Error{Kind: KindSimulationFailed}, KindSimulationFailed},
		{"restore", &Error{Kind: KindRestoreRequired}, KindRestoreRequired},
		{"needsMoreSigs", &Error{Kind: KindNeedsMoreSignatures}, KindNeedsMoreSignatures},
		{"authMissing", &Error{Kind: KindAuthMissing}, KindAuthMissing},
		{"contractRevert", &Error{Kind: KindContractRevert, cause: &ContractRevertError{Code: 1}}, KindContractRevert},
		{"submission", &Error{Kind: KindSubmissionFailed}, KindSubmissionFailed},
		{"timeout", &Error{Kind: KindTimeout}, KindTimeout},
		{"notYetSimulated", &Error{Kind: KindNotYetSimulated}, KindNotYetSimulated},
		{"invalidArgs", &Error{Kind: KindInvalidArgs}, KindInvalidArgs},
		{"wrappedTimeout", fmt.Errorf("ctx: %w", &Error{Kind: KindTimeout}), KindTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got *Error
			require.True(t, errors.As(tc.err, &got))
			require.Equal(t, tc.want, got.Kind)
		})
	}
}

func TestError_KindClassification_UnknownForNonPackageError(t *testing.T) {
	// A non-package error has no Kind to extract.
	plain := fmt.Errorf("network down")
	var got *Error
	require.False(t, errors.As(plain, &got))
	require.Nil(t, got)
}

// --- *Error.Error() formatting ---------------------------------------------
//
// We assert structural properties — the kind tag must appear, and the cause
// message must show through when present — without pinning the exact
// punctuation. This keeps the contract usable for log greps without making
// the tests fragile to whitespace tweaks.

func TestError_Error_NilReceiver(t *testing.T) {
	var nilErr *Error
	require.Equal(t, "<nil>", nilErr.Error())
}

func TestError_Error_KindOnly(t *testing.T) {
	got := (&Error{Kind: KindTimeout}).Error()
	require.Contains(t, got, "contract:")
	require.Contains(t, got, "timeout")
}

func TestError_Error_KindAndDetails(t *testing.T) {
	got := (&Error{Kind: KindAuthMissing, Details: "no entries"}).Error()
	require.Contains(t, got, "auth_missing")
	require.Contains(t, got, "no entries")
}

func TestError_Error_KindAndCause(t *testing.T) {
	got := (&Error{Kind: KindSimulationFailed, cause: fmt.Errorf("rpc 500")}).Error()
	require.Contains(t, got, "simulation_failed")
	require.Contains(t, got, "rpc 500")
}

func TestError_Error_KindDetailsAndCause(t *testing.T) {
	got := (&Error{
		Kind:    KindSubmissionFailed,
		Details: "ledger 123",
		cause:   fmt.Errorf("rpc 500"),
	}).Error()
	require.Contains(t, got, "submission_failed")
	require.Contains(t, got, "ledger 123")
	require.Contains(t, got, "rpc 500")
	// Details must precede the cause so log readers see context first.
	require.Less(t, strings.Index(got, "ledger 123"), strings.Index(got, "rpc 500"))
}

// --- *ContractRevertError.Error() ------------------------------------------

func TestContractRevertError_Error_NilReceiver(t *testing.T) {
	var nilRevert *ContractRevertError
	require.Equal(t, "<nil>", nilRevert.Error())
}

func TestContractRevertError_Error_WithNameAndContract(t *testing.T) {
	got := (&ContractRevertError{
		ContractID: "CAAA",
		Code:       7,
		Name:       "ErrInsufficientBalance",
	}).Error()
	require.Contains(t, got, "ErrInsufficientBalance")
	require.Contains(t, got, "7")
	require.Contains(t, got, "CAAA")
}

func TestContractRevertError_Error_DefaultsNameWhenEmpty(t *testing.T) {
	// When the spec didn't resolve a name, the formatted message must still
	// be intelligible — falling back to "ContractError" per §4.7.
	got := (&ContractRevertError{Code: 42}).Error()
	require.Contains(t, got, "ContractError")
	require.Contains(t, got, "42")
}

func TestContractRevertError_Error_NoContractID(t *testing.T) {
	got := (&ContractRevertError{Code: 3, Name: "ErrFoo"}).Error()
	require.Contains(t, got, "ErrFoo")
	require.Contains(t, got, "3")
	require.NotContains(t, got, " at ")
}

// --- Integration: forged revert through the full wrap chain ----------------

func TestError_RevertIntegration_FullChain(t *testing.T) {
	// Simulate what production code will produce: a transport layer wraps a
	// contract revert with fmt.Errorf, which itself wraps an *Error whose
	// cause is a *ContractRevertError. Callers must be able to (a) classify
	// it as KindContractRevert, (b) pull the revert struct out, and (c) see
	// the revert name in the formatted message.
	revert := &ContractRevertError{
		ContractID: "CCCC",
		Code:       11,
		Name:       "ErrOverflow",
		RawXDR:     "AAAAAA==",
	}
	wrapped := &Error{Kind: KindContractRevert, Details: "invoke transfer", cause: revert}
	outer := fmt.Errorf("rpc submit: %w", wrapped)

	// (a) classify
	require.True(t, errors.Is(outer, &Error{Kind: KindContractRevert}))
	var asErr *Error
	require.True(t, errors.As(outer, &asErr))
	require.Equal(t, KindContractRevert, asErr.Kind)

	// (b) extract typed cause
	var asRevert *ContractRevertError
	require.True(t, errors.As(outer, &asRevert))
	require.Same(t, revert, asRevert)
	require.Equal(t, int32(11), asRevert.Code)
	require.Equal(t, "ErrOverflow", asRevert.Name)

	// (c) message contains both layers' context.
	msg := outer.Error()
	require.Contains(t, msg, "rpc submit")
	require.Contains(t, msg, "contract_revert")
	require.Contains(t, msg, "invoke transfer")
	require.Contains(t, msg, "ErrOverflow")
}
