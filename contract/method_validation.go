package contract

import (
	"fmt"
	"sort"
)

// maxMethodSuggestions caps the number of "did you mean" candidates surfaced
// in an unknown-method error message. Three matches the JS-SDK's contract
// client and keeps log lines short.
const maxMethodSuggestions = 3

// suggestMethods returns up to maxMethodSuggestions function names from spec
// whose Levenshtein distance to name is within an adaptive threshold of
// max(2, len(name)/3) — generous enough for short tokens (one-char typo on
// "transfer" -> "transferr") while rejecting clearly unrelated inputs
// ("xyz" -> no suggestions). Results are ordered by ascending distance, then
// by ascending name for stable output.
//
// Returns nil when spec is nil or has no functions, when name is empty, or
// when no function falls inside the threshold.
func suggestMethods(spec *Spec, name string) []string {
	if spec == nil || name == "" {
		return nil
	}
	funcs := spec.Funcs()
	if len(funcs) == 0 {
		return nil
	}

	threshold := len(name) / 3
	if threshold < 2 {
		threshold = 2
	}

	type cand struct {
		name string
		dist int
	}
	cands := make([]cand, 0, len(funcs))
	for _, fn := range funcs {
		candName := string(fn.Name)
		d := levenshtein(name, candName)
		if d <= threshold {
			cands = append(cands, cand{name: candName, dist: d})
		}
	}
	if len(cands) == 0 {
		return nil
	}

	sort.Slice(cands, func(i, j int) bool {
		if cands[i].dist != cands[j].dist {
			return cands[i].dist < cands[j].dist
		}
		return cands[i].name < cands[j].name
	})

	n := len(cands)
	if n > maxMethodSuggestions {
		n = maxMethodSuggestions
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = cands[i].name
	}
	return out
}

// unknownMethodError builds the *Error returned when a method name is not
// defined in the bound spec. It always uses KindInvalidArgs (preserving the
// errors.Is contract from T4.1) and appends a "did you mean: …" hint when
// suggestMethods finds at least one near match.
func unknownMethodError(spec *Spec, method string) error {
	suggestions := suggestMethods(spec, method)
	if len(suggestions) == 0 {
		return invalidArgsf("Invoke: function %q not found in spec", method)
	}
	return invalidArgsf(
		"Invoke: function %q not found in spec; did you mean: %s",
		method, formatSuggestions(suggestions),
	)
}

// formatSuggestions renders the suggestion list as a quoted, comma-separated
// string suitable for embedding in an error message.
func formatSuggestions(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("%q", names[0])
	}
	out := fmt.Sprintf("%q", names[0])
	for _, n := range names[1:] {
		out += fmt.Sprintf(", %q", n)
	}
	return out
}

// levenshtein returns the edit distance between a and b using the standard
// two-row dynamic-programming implementation. Stdlib-only by design — adding
// a dependency for ~25 lines of textbook code is not warranted.
func levenshtein(a, b string) int {
	ar := []rune(a)
	br := []rune(b)
	la, lb := len(ar), len(br)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			// deletion, insertion, substitution
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}
