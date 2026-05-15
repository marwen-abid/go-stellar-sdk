package contract

import (
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// SentTransaction is the handle returned by AssembledTransaction.Send. It
// captures the post-submission state (hash + raw RPC response) and carries
// the rpc handle the Wait / Watch / Status methods (T3.5) will use to poll
// for the on-chain result. The zero value is not useful; callers obtain a
// *SentTransaction from AssembledTransaction.Send.
type SentTransaction struct {
	// Hash is the transaction hash returned by the RPC. After T3.5 lands,
	// callers can poll the network for this hash to retrieve the result.
	Hash xdr.Hash
	// SendResponse is the raw RPC response (status, latest ledger, optional
	// diagnostic events).
	SendResponse *protocol.SendTransactionResponse

	// unexported state. rpc and method are set by Send so the upcoming
	// Wait/Watch/Status methods (T3.5) can issue follow-up calls.
	rpc    rpcSimulator
	method string
}
