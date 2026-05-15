package contract

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time confirmation that the exported types satisfy the error
// interface; full behavioural coverage lives in T1.3.
var (
	_ error = (*Error)(nil)
	_ error = (*ContractRevertError)(nil)
)

func TestError_SentinelsMatchByKind(t *testing.T) {
	cases := []struct {
		name     string
		sentinel *Error
	}{
		{"ErrSimulationFailed", ErrSimulationFailed},
		{"ErrRestoreRequired", ErrRestoreRequired},
		{"ErrNeedsMoreSignatures", ErrNeedsMoreSignatures},
		{"ErrAuthMissing", ErrAuthMissing},
		{"ErrTimeout", ErrTimeout},
		{"ErrNotYetSimulated", ErrNotYetSimulated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A freshly-constructed error of the same kind round-trips
			// through errors.Is against the sentinel.
			err := &Error{Kind: tc.sentinel.Kind, Details: "detail", cause: fmt.Errorf("boom")}
			require.True(t, errors.Is(err, tc.sentinel))
		})
	}
}

func TestError_UnwrapExposesCause(t *testing.T) {
	cause := fmt.Errorf("underlying")
	err := &Error{Kind: KindSimulationFailed, cause: cause}
	require.Same(t, cause, errors.Unwrap(err))
}

func TestError_AsContractRevert(t *testing.T) {
	revert := &ContractRevertError{ContractID: "C123", Code: 7, Name: "ErrInsufficientBalance"}
	wrapped := &Error{Kind: KindContractRevert, cause: revert}

	var got *ContractRevertError
	require.True(t, errors.As(wrapped, &got))
	assert.Same(t, revert, got)
}
