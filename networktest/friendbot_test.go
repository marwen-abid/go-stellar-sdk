package networktest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testAddress = "GBLPP2W3X3PJQXYMC7EFWM5G2QCZL7HTCTFNMONS4ITGAYJ3GNNZIQ4V"

// fakeFriendbot stands up a local HTTP server impersonating friendbot. The
// handler closure can inspect the request and choose status + body per case.
func fakeFriendbot(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)
	return srv
}

func TestFund_Success(t *testing.T) {
	var gotAddr string
	srv := fakeFriendbot(t, func(w http.ResponseWriter, r *http.Request) {
		gotAddr = r.URL.Query().Get("addr")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hash":"deadbeef","ledger":42}`))
	})

	sb := New(WithFriendbotURL(srv.URL))
	if err := sb.Fund(context.Background(), testAddress); err != nil {
		t.Fatalf("Fund: unexpected error: %v", err)
	}
	if gotAddr != testAddress {
		t.Errorf("friendbot received addr=%q, want %q", gotAddr, testAddress)
	}
}

func TestFund_HTTPError(t *testing.T) {
	srv := fakeFriendbot(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":500,"title":"Internal Error","detail":"boom"}`))
	})

	sb := New(WithFriendbotURL(srv.URL))
	err := sb.Fund(context.Background(), testAddress)
	if err == nil {
		t.Fatal("Fund: expected error on 500, got nil")
	}
	if !errors.Is(err, ErrFundingFailed) {
		t.Errorf("Fund: error is not ErrFundingFailed: %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("Fund: error should include server detail; got %v", err)
	}
}

func TestFund_AlreadyFundedTreatedAsSuccess(t *testing.T) {
	srv := fakeFriendbot(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		// Real friendbot uses an op_already_exists code in the result_codes
		// block; we exercise the lowercase substring match too.
		_, _ = w.Write([]byte(`{"status":400,"title":"Bad Request","detail":"createAccount op_already_exists"}`))
	})

	sb := New(WithFriendbotURL(srv.URL))
	if err := sb.Fund(context.Background(), testAddress); err != nil {
		t.Fatalf("Fund: already-funded should be success; got %v", err)
	}
}

func TestFund_MissingURL(t *testing.T) {
	sb := New(WithFriendbotURL(""))
	err := sb.Fund(context.Background(), testAddress)
	if err == nil {
		t.Fatal("Fund: expected error when FriendbotURL is empty")
	}
	if !errors.Is(err, ErrFundingFailed) {
		t.Errorf("Fund: error is not ErrFundingFailed: %v", err)
	}
}

func TestFund_EmptyAddress(t *testing.T) {
	sb := New(WithFriendbotURL("http://example.invalid"))
	err := sb.Fund(context.Background(), "")
	if err == nil {
		t.Fatal("Fund: expected error with empty address")
	}
	if !errors.Is(err, ErrFundingFailed) {
		t.Errorf("Fund: error is not ErrFundingFailed: %v", err)
	}
}

func TestFund_Success2xxWithoutHashFails(t *testing.T) {
	srv := fakeFriendbot(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	sb := New(WithFriendbotURL(srv.URL))
	err := sb.Fund(context.Background(), testAddress)
	if err == nil {
		t.Fatal("Fund: empty 2xx body should fail")
	}
	if !errors.Is(err, ErrFundingFailed) {
		t.Errorf("Fund: error is not ErrFundingFailed: %v", err)
	}
}

func TestFund_TransportError(t *testing.T) {
	// Point at an unreachable URL — connection should be refused immediately.
	sb := New(WithFriendbotURL("http://127.0.0.1:1/friendbot"))
	err := sb.Fund(context.Background(), testAddress)
	if err == nil {
		t.Fatal("Fund: expected transport error")
	}
	if !errors.Is(err, ErrFundingFailed) {
		t.Errorf("Fund: error is not ErrFundingFailed: %v", err)
	}
}

func TestFund_PreservesBaseQuery(t *testing.T) {
	var gotURL string
	srv := fakeFriendbot(t, func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hash":"abc","ledger":1}`))
	})

	// Some friendbot deployments expose the endpoint behind a path
	// (e.g. /friendbot). Use the test server URL plus a /friendbot suffix.
	base := srv.URL + "/friendbot"
	sb := New(WithFriendbotURL(base))
	if err := sb.Fund(context.Background(), testAddress); err != nil {
		t.Fatalf("Fund: %v", err)
	}
	if !strings.HasPrefix(gotURL, "/friendbot") {
		t.Errorf("expected request path to start with /friendbot, got %q", gotURL)
	}
	if !strings.Contains(gotURL, "addr="+testAddress) {
		t.Errorf("expected addr query param, got %q", gotURL)
	}
}

func TestNewFundedKeypair_Success(t *testing.T) {
	var gotAddr string
	srv := fakeFriendbot(t, func(w http.ResponseWriter, r *http.Request) {
		gotAddr = r.URL.Query().Get("addr")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hash":"abc","ledger":1}`))
	})

	sb := New(WithFriendbotURL(srv.URL))
	kp, err := sb.NewFundedKeypair(context.Background())
	if err != nil {
		t.Fatalf("NewFundedKeypair: %v", err)
	}
	if kp == nil {
		t.Fatal("NewFundedKeypair returned nil keypair without error")
	}
	if kp.Address() != gotAddr {
		t.Errorf("friendbot funded %q, but returned keypair has address %q", gotAddr, kp.Address())
	}
	if sb.Funded != kp {
		t.Error("Sandbox.Funded should be set to the first funded keypair")
	}
}

func TestNewFundedKeypair_FundingErrorPropagates(t *testing.T) {
	srv := fakeFriendbot(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":503,"detail":"down"}`))
	})

	sb := New(WithFriendbotURL(srv.URL))
	kp, err := sb.NewFundedKeypair(context.Background())
	if err == nil {
		t.Fatal("NewFundedKeypair: expected error on friendbot 503")
	}
	if kp != nil {
		t.Errorf("NewFundedKeypair: expected nil keypair on error, got %v", kp)
	}
	if !errors.Is(err, ErrFundingFailed) {
		t.Errorf("error is not ErrFundingFailed: %v", err)
	}
}

func TestBuildFriendbotURL(t *testing.T) {
	tests := []struct {
		base    string
		addr    string
		want    string
		wantErr bool
	}{
		{"https://friendbot.stellar.org", "GABC", "https://friendbot.stellar.org?addr=GABC", false},
		{"http://localhost:8000/friendbot", "GXYZ", "http://localhost:8000/friendbot?addr=GXYZ", false},
		{"http://localhost:8000/friendbot?network=local", "GXYZ", "http://localhost:8000/friendbot?addr=GXYZ&network=local", false},
		{"://bad", "GXYZ", "", true},
	}
	for _, tc := range tests {
		got, err := buildFriendbotURL(tc.base, tc.addr)
		if tc.wantErr {
			if err == nil {
				t.Errorf("buildFriendbotURL(%q): expected error", tc.base)
			}
			continue
		}
		if err != nil {
			t.Errorf("buildFriendbotURL(%q): unexpected error %v", tc.base, err)
			continue
		}
		if got != tc.want {
			t.Errorf("buildFriendbotURL(%q, %q) = %q, want %q", tc.base, tc.addr, got, tc.want)
		}
	}
}
