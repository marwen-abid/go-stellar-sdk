package contract

import (
	"context"
	"fmt"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// Default polling parameters used by SentTransaction.Wait / Watch when no
// PollOption overrides them. They mirror rpcclient.PollTransaction defaults
// (initial 500ms, max 3.5s) and pair with a 30s overall timeout that matches
// the JS SDK's AssembledTransaction.poll default. Callers can override via
// PollInterval, PollTimeout, and PollBackoff.
const (
	DefaultPollInterval = 500 * time.Millisecond
	DefaultPollTimeout  = 30 * time.Second
	DefaultPollBackoff  = 1.5
	DefaultPollMaxWait  = 3500 * time.Millisecond
)

// SentTransaction is the handle returned by AssembledTransaction.Send. It
// captures the post-submission state (hash + raw RPC response) and exposes
// Wait / Status / Watch to poll the network for the on-chain result.
// The zero value is not useful; callers obtain a *SentTransaction from
// AssembledTransaction.Send.
type SentTransaction struct {
	// Hash is the transaction hash returned by the RPC.
	Hash xdr.Hash
	// SendResponse is the raw RPC response (status, latest ledger, optional
	// diagnostic events).
	SendResponse *protocol.SendTransactionResponse

	// unexported state. rpc and method are set by Send so the Wait / Watch /
	// Status methods can issue follow-up getTransaction calls. spec, when
	// non-nil, lets Result() decode the final ScVal via the contract's
	// declared output type. getResp caches the terminal getTransaction
	// response produced by Wait so AssembledTransaction.Result() can decode
	// it without re-polling the network.
	rpc     rpcSimulator
	method  string
	spec    *Spec
	getResp *protocol.GetTransactionResponse
	// classic marks a SentTransaction returned by the classic Payment fast
	// path (see contract.ClassicSubmitFunc). Classic submitters confirm
	// inclusion synchronously, so Wait short-circuits to an immediate
	// success rather than polling Stellar RPC.
	classic bool
}

// pollConfig captures the knobs exposed by the PollOption functional options.
// All durations default to the package-level defaults.
type pollConfig struct {
	interval time.Duration
	timeout  time.Duration
	backoff  float64
	maxWait  time.Duration
}

func defaultPollConfig() pollConfig {
	return pollConfig{
		interval: DefaultPollInterval,
		timeout:  DefaultPollTimeout,
		backoff:  DefaultPollBackoff,
		maxWait:  DefaultPollMaxWait,
	}
}

// PollOption configures the polling behavior of Wait and Watch. Options are
// applied in order; later options override earlier ones.
type PollOption func(*pollConfig)

// PollInterval sets the initial delay between polling attempts. Values <= 0
// are ignored.
func PollInterval(d time.Duration) PollOption {
	return func(c *pollConfig) {
		if d > 0 {
			c.interval = d
		}
	}
}

// PollTimeout sets the overall deadline for the poll loop. Values <= 0 are
// ignored. The timeout is layered on top of any deadline already attached to
// the context passed to Wait / Watch; whichever fires first wins.
func PollTimeout(d time.Duration) PollOption {
	return func(c *pollConfig) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// PollBackoff sets the multiplier applied to the interval after each
// unsuccessful poll. Values <= 1 disable backoff (fixed interval).
func PollBackoff(f float64) PollOption {
	return func(c *pollConfig) {
		c.backoff = f
	}
}

// StatusUpdate is emitted by Watch on every poll. Response is nil for the
// initial NOT_FOUND emissions and populated once the transaction is found.
// Err is non-nil only on the final emission when polling ends in error
// (timeout, RPC failure, or terminal FAILED status).
type StatusUpdate struct {
	Status   string
	Response *protocol.GetTransactionResponse
	Err      error
}

// Status performs a single getTransaction call and returns the current
// status string (one of protocol.TransactionStatusSuccess / TransactionStatusFailed
// / TransactionStatusNotFound). It does not retry.
func (s *SentTransaction) Status(ctx context.Context) (string, error) {
	if s == nil {
		return "", invalidArgsf("SentTransaction not initialized")
	}
	if s.classic {
		// Classic submitters confirm inclusion synchronously; the
		// SentTransaction handed back already represents a finalized
		// transaction. Surface that as the canonical SUCCESS status so
		// callers can poll uniformly.
		return protocol.TransactionStatusSuccess, nil
	}
	if s.rpc == nil {
		return "", invalidArgsf("SentTransaction not initialized")
	}
	resp, err := s.rpc.GetTransaction(ctx, protocol.GetTransactionRequest{
		Hash: s.Hash.HexString(),
	})
	if err != nil {
		return "", &Error{Kind: KindSubmissionFailed, Details: "Status: GetTransaction", cause: err}
	}
	return resp.Status, nil
}

// Wait blocks until the transaction reaches a terminal state (SUCCESS or
// FAILED) or the poll deadline elapses. It polls getTransaction at the
// configured interval, with optional exponential backoff capped at maxWait.
//
// Returns:
//   - the final *protocol.GetTransactionResponse on SUCCESS.
//   - an *Error matching ErrTransactionFailed (wrapping the response) on FAILED.
//   - an *Error matching ErrTimeout when the poll deadline elapses while the
//     transaction is still NOT_FOUND.
//   - an *Error matching ErrSubmissionFailed if a getTransaction call returns
//     a transport error.
//
// Wait honors ctx.Done(): if the caller cancels the context, Wait returns
// the underlying ctx.Err() wrapped by an *Error with KindTimeout.
//
// Native decoding of the result ScVal is the job of T3.6's Result() helper;
// callers can read the raw scval via Response.ReturnValueXDR.
func (s *SentTransaction) Wait(ctx context.Context, opts ...PollOption) (*protocol.GetTransactionResponse, error) {
	if s == nil {
		return nil, invalidArgsf("SentTransaction not initialized")
	}
	if s.classic {
		// Classic transactions are already finalized when their hash is
		// returned from the submitter; synthesize a minimal success
		// response so callers can drive Wait → (anything-they-need) in
		// the same shape as the Soroban path.
		resp := &protocol.GetTransactionResponse{
			TransactionDetails: protocol.TransactionDetails{
				Status: protocol.TransactionStatusSuccess,
			},
		}
		s.getResp = resp
		return resp, nil
	}
	if s.rpc == nil {
		return nil, invalidArgsf("SentTransaction not initialized")
	}

	cfg := defaultPollConfig()
	for _, o := range opts {
		o(&cfg)
	}

	pollCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	interval := cfg.interval
	hashHex := s.Hash.HexString()
	req := protocol.GetTransactionRequest{Hash: hashHex}

	for {
		resp, err := s.rpc.GetTransaction(pollCtx, req)
		if err != nil {
			// Distinguish a context-driven cancellation from a true RPC error
			// so callers can errors.Is(err, ErrTimeout) on the former.
			if ctxErr := pollCtx.Err(); ctxErr != nil {
				return nil, &Error{Kind: KindTimeout, Details: "Wait: poll deadline", cause: ctxErr}
			}
			return nil, &Error{Kind: KindSubmissionFailed, Details: "Wait: GetTransaction", cause: err}
		}

		switch resp.Status {
		case protocol.TransactionStatusSuccess:
			respCopy := resp
			s.getResp = &respCopy
			return &respCopy, nil
		case protocol.TransactionStatusFailed:
			return &resp, &Error{
				Kind:    KindTransactionFailed,
				Details: fmt.Sprintf("Wait: transaction %s failed on-chain", hashHex),
			}
		case protocol.TransactionStatusNotFound:
			// Not yet finalized — fall through to sleep+retry below.
		default:
			return nil, &Error{
				Kind:    KindSubmissionFailed,
				Details: fmt.Sprintf("Wait: unrecognized status %q", resp.Status),
			}
		}

		// Sleep before the next poll, honoring ctx cancellation. Use a timer
		// so we don't leak goroutines if pollCtx fires.
		timer := time.NewTimer(interval)
		select {
		case <-pollCtx.Done():
			timer.Stop()
			return nil, &Error{Kind: KindTimeout, Details: "Wait: poll deadline", cause: pollCtx.Err()}
		case <-timer.C:
		}

		// Exponential backoff up to maxWait when configured.
		if cfg.backoff > 1 && interval < cfg.maxWait {
			next := time.Duration(float64(interval) * cfg.backoff)
			if next > cfg.maxWait {
				next = cfg.maxWait
			}
			interval = next
		}
	}
}

// Watch returns a buffered channel that receives a StatusUpdate on every poll
// until the transaction reaches a terminal state or ctx is canceled. The
// channel is closed once the final update is emitted. The terminal update
// carries the same Err semantics as Wait: non-nil for FAILED / timeout /
// transport error, nil for SUCCESS.
//
// The channel is buffered (size 1) so a slow consumer cannot block the poll
// loop; intermediate NOT_FOUND updates may be coalesced — only the most recent
// one is retained when the consumer has not yet read the previous one.
//
// This is the Go-idiomatic answer to the JS SDK's onProgress callback.
func (s *SentTransaction) Watch(ctx context.Context, opts ...PollOption) <-chan StatusUpdate {
	ch := make(chan StatusUpdate, 1)

	if s == nil || s.rpc == nil {
		ch <- StatusUpdate{Err: invalidArgsf("SentTransaction not initialized")}
		close(ch)
		return ch
	}

	cfg := defaultPollConfig()
	for _, o := range opts {
		o(&cfg)
	}

	go func() {
		defer close(ch)

		pollCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
		defer cancel()

		interval := cfg.interval
		req := protocol.GetTransactionRequest{Hash: s.Hash.HexString()}

		emit := func(u StatusUpdate) {
			// Non-blocking send with drop-oldest semantics: if the consumer
			// hasn't read the prior update, replace it with the newer one.
			select {
			case ch <- u:
			default:
				select {
				case <-ch:
				default:
				}
				select {
				case ch <- u:
				default:
				}
			}
		}

		for {
			resp, err := s.rpc.GetTransaction(pollCtx, req)
			if err != nil {
				if ctxErr := pollCtx.Err(); ctxErr != nil {
					emit(StatusUpdate{Err: &Error{Kind: KindTimeout, Details: "Watch: poll deadline", cause: ctxErr}})
					return
				}
				emit(StatusUpdate{Err: &Error{Kind: KindSubmissionFailed, Details: "Watch: GetTransaction", cause: err}})
				return
			}

			respCopy := resp
			switch resp.Status {
			case protocol.TransactionStatusSuccess:
				emit(StatusUpdate{Status: resp.Status, Response: &respCopy})
				return
			case protocol.TransactionStatusFailed:
				emit(StatusUpdate{
					Status:   resp.Status,
					Response: &respCopy,
					Err: &Error{
						Kind:    KindTransactionFailed,
						Details: fmt.Sprintf("Watch: transaction %s failed on-chain", req.Hash),
					},
				})
				return
			case protocol.TransactionStatusNotFound:
				emit(StatusUpdate{Status: resp.Status, Response: &respCopy})
			default:
				emit(StatusUpdate{
					Status:   resp.Status,
					Response: &respCopy,
					Err: &Error{
						Kind:    KindSubmissionFailed,
						Details: fmt.Sprintf("Watch: unrecognized status %q", resp.Status),
					},
				})
				return
			}

			timer := time.NewTimer(interval)
			select {
			case <-pollCtx.Done():
				timer.Stop()
				emit(StatusUpdate{Err: &Error{Kind: KindTimeout, Details: "Watch: poll deadline", cause: pollCtx.Err()}})
				return
			case <-timer.C:
			}

			if cfg.backoff > 1 && interval < cfg.maxWait {
				next := time.Duration(float64(interval) * cfg.backoff)
				if next > cfg.maxWait {
					next = cfg.maxWait
				}
				interval = next
			}
		}
	}()

	return ch
}
