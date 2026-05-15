package contract

import (
	"context"
	"errors"
	"testing"

	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildTokenLikeSpec returns a *Spec declaring the four common SAC functions —
// enough surface area to exercise suggestion ranking and the threshold cutoff.
func buildTokenLikeSpec(t *testing.T) *Spec {
	t.Helper()
	return NewSpecFromEntries([]xdr.ScSpecEntry{
		fnEntryWithSig(t, "transfer", nil, nil),
		fnEntryWithSig(t, "balance", nil, nil),
		fnEntryWithSig(t, "approve", nil, nil),
		fnEntryWithSig(t, "mint", nil, nil),
	})
}

func TestSuggestMethods_ExactMatchIsAlsoSuggested(t *testing.T) {
	// Sanity: an exact match has distance 0 and lands first. Invoke itself
	// short-circuits before this is called, but the helper should still
	// behave for direct callers.
	got := suggestMethods(buildTokenLikeSpec(t), "transfer")
	require.NotEmpty(t, got)
	assert.Equal(t, "transfer", got[0])
}

func TestSuggestMethods_OneCharTypoReturnsTopMatch(t *testing.T) {
	got := suggestMethods(buildTokenLikeSpec(t), "transferr")
	require.NotEmpty(t, got)
	assert.Equal(t, "transfer", got[0])
}

func TestSuggestMethods_FarOffNameReturnsNoSuggestions(t *testing.T) {
	// "xyz" is distance >= 3 from every token-spec name and should fall
	// outside the threshold for any of them.
	got := suggestMethods(buildTokenLikeSpec(t), "xyz")
	assert.Empty(t, got)
}

func TestSuggestMethods_EmptySpecReturnsNoSuggestions(t *testing.T) {
	empty := NewSpecFromEntries(nil)
	assert.Empty(t, suggestMethods(empty, "transfer"))
	assert.Empty(t, suggestMethods(nil, "transfer"))
}

func TestSuggestMethods_CapsAtThree(t *testing.T) {
	// Build a spec where five names are all within distance 1 of "ab".
	s := NewSpecFromEntries([]xdr.ScSpecEntry{
		fnEntryWithSig(t, "a", nil, nil),
		fnEntryWithSig(t, "b", nil, nil),
		fnEntryWithSig(t, "ac", nil, nil),
		fnEntryWithSig(t, "ad", nil, nil),
		fnEntryWithSig(t, "ae", nil, nil),
	})
	got := suggestMethods(s, "ab")
	assert.Len(t, got, maxMethodSuggestions)
}

func TestUnknownMethodError_IncludesSuggestionInMessage(t *testing.T) {
	err := unknownMethodError(buildTokenLikeSpec(t), "transferr")
	require.Error(t, err)
	var ce *Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, KindInvalidArgs, ce.Kind)
	assert.Contains(t, err.Error(), `"transferr"`)
	assert.Contains(t, err.Error(), "did you mean")
	assert.Contains(t, err.Error(), `"transfer"`)
}

func TestUnknownMethodError_FarOffOmitsSuggestion(t *testing.T) {
	err := unknownMethodError(buildTokenLikeSpec(t), "xyz")
	require.Error(t, err)
	var ce *Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, KindInvalidArgs, ce.Kind)
	assert.NotContains(t, err.Error(), "did you mean")
}

func TestInvoke_UnknownMethodIncludesSuggestion(t *testing.T) {
	cid := testContractID(t)
	c := New(cid, &fakeSimulator{}, network.TestNetworkPassphrase,
		WithSpec(buildTokenLikeSpec(t)),
		WithSourceAccount(newClientSource(t)),
	)

	_, err := c.Invoke(context.Background(), "transferr", map[string]any{})
	require.Error(t, err)
	var ce *Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, KindInvalidArgs, ce.Kind)
	assert.Contains(t, err.Error(), `"transferr"`)
	assert.Contains(t, err.Error(), "did you mean")
	assert.Contains(t, err.Error(), `"transfer"`)
}

func TestLevenshtein_KnownCases(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{"abc", "abc", 0},
		{"transferr", "transfer", 1},
		{"trnsfer", "transfer", 1},
		{"kitten", "sitting", 3},
		{"saturday", "sunday", 3},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, levenshtein(c.a, c.b), "levenshtein(%q, %q)", c.a, c.b)
	}
}
