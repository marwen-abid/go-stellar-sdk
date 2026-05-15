package networktest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
)

// ErrFundingFailed is returned by Sandbox.Fund when the friendbot request
// fails (network error, non-2xx response, or malformed response body).
// Callers can match it with errors.Is to distinguish funding failures from
// other Sandbox errors.
var ErrFundingFailed = errors.New("networktest: friendbot funding failed")

// fundHTTPClient is the http.Client used for friendbot calls. It is a package
// variable so tests can swap in an httptest.Server-backed transport; the
// default has a conservative timeout so unattended live calls don't hang.
var fundHTTPClient = &http.Client{Timeout: 30 * time.Second}

// friendbotResponse models the JSON envelope returned by the canonical
// friendbot. On success the server returns the Horizon transaction envelope
// (hash, ledger, ...). On failure it returns a problem-style document with
// status/detail/title fields. We only decode the fields we need to surface a
// useful error.
type friendbotResponse struct {
	Hash   string `json:"hash"`
	Ledger int32  `json:"ledger"`

	// Problem fields (RFC 7807-ish, also used by Horizon errors).
	Status int    `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// Fund creates and funds the given account on the sandbox's network by
// calling its friendbot endpoint (Sandbox.FriendbotURL). It is only useful
// on test networks (local quickstart, testnet, futurenet). On the public
// network this will always fail.
//
// Returns ErrFundingFailed wrapped with the underlying cause on failure.
// The "account already exists" response from friendbot is treated as a
// success, since the post-condition (a funded account at address) holds.
func (s *Sandbox) Fund(ctx context.Context, address string) error {
	if s.FriendbotURL == "" {
		return fmt.Errorf("%w: Sandbox.FriendbotURL is empty (call Start first or pass WithFriendbotURL)", ErrFundingFailed)
	}
	if address == "" {
		return fmt.Errorf("%w: address is empty", ErrFundingFailed)
	}

	reqURL, err := buildFriendbotURL(s.FriendbotURL, address)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrFundingFailed, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrFundingFailed, err)
	}

	resp, err := fundHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrFundingFailed, err)
	}
	defer resp.Body.Close()

	// Cap the body read; friendbot envelopes are small (<8KB in practice).
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: reading response: %v", ErrFundingFailed, err)
	}

	// Best-effort JSON decode; an unparseable body on a 2xx is still treated
	// as a failure because we can't confirm the post-condition.
	var fb friendbotResponse
	jsonErr := json.Unmarshal(body, &fb)

	if resp.StatusCode/100 != 2 {
		// Treat "op_already_exists"-style 400s as success: the network
		// already has a funded account at this address, which is the
		// post-condition we promise. The canonical signal is the substring
		// "op_already_exists" in the response body; some quickstart builds
		// also return a 400 with detail mentioning "already exists".
		if alreadyFunded(body, &fb) {
			return nil
		}
		detail := strings.TrimSpace(fb.Detail)
		if detail == "" {
			detail = strings.TrimSpace(string(body))
		}
		return fmt.Errorf("%w: HTTP %d: %s", ErrFundingFailed, resp.StatusCode, truncate(detail, 256))
	}

	if jsonErr != nil {
		return fmt.Errorf("%w: decoding 2xx response: %v", ErrFundingFailed, jsonErr)
	}
	if fb.Hash == "" {
		return fmt.Errorf("%w: friendbot returned 2xx without a transaction hash", ErrFundingFailed)
	}
	return nil
}

// NewFundedKeypair generates a fresh random keypair, funds its address via
// Fund, and returns the keypair. On first success it also caches the
// keypair on s.Funded so callers using the Sandbox as a "root account
// provider" can grab the same one back.
func (s *Sandbox) NewFundedKeypair(ctx context.Context) (*keypair.Full, error) {
	kp, err := keypair.Random()
	if err != nil {
		return nil, fmt.Errorf("%w: generating keypair: %v", ErrFundingFailed, err)
	}
	if err := s.Fund(ctx, kp.Address()); err != nil {
		return nil, err
	}
	if s.Funded == nil {
		s.Funded = kp
	}
	return kp, nil
}

// buildFriendbotURL appends ?addr=<address> to the configured friendbot URL,
// handling base URLs that already include a path or query string.
func buildFriendbotURL(base, address string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid friendbot URL %q: %w", base, err)
	}
	q := u.Query()
	q.Set("addr", address)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// alreadyFunded reports whether a non-2xx friendbot response indicates the
// account is already created/funded. Friendbot's exact wording varies by
// version, so we check both the decoded problem detail and the raw body.
func alreadyFunded(body []byte, fb *friendbotResponse) bool {
	needles := []string{"op_already_exists", "already exists", "already funded"}
	hay := strings.ToLower(string(body))
	if fb != nil {
		hay += " " + strings.ToLower(fb.Detail) + " " + strings.ToLower(fb.Title)
	}
	for _, n := range needles {
		if strings.Contains(hay, n) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
