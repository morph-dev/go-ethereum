// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

// Package blobpool implements the EIP-4844 blob transaction pool.
package framepool

import (
	"math/big"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/holiman/uint256"
)

const (
	// maxVerifyGas is the maximum cumulative gas the validation prefix can use
	maxVerifyGas = 100_000

	// maxTxsPerSender is the maximum number of frame transactions admitted from a single sender account
	maxTxsPerSender = 1

	// maxPendingTxsPerNonCanonicalPaymaster is the maximum number of pending transaction for a non-canonical paymaster
	maxPendingTxsPerNonCanonicalPaymaster = 1
)

type FramePool struct {
	config Config // Pool configuration

	signer types.Signer // Transaction signer to use for sender recovery
	chain  BlockChain   // Chain object to access the state through

	reserver txpool.Reserver             // Address reserver to ensure exclusivity across subpools
	gasTip   atomic.Pointer[uint256.Int] // Currently accepted minimum gas tip

	head  atomic.Pointer[types.Header] // Current head of the chain
	state *state.StateDB               // Current state at the head of the chain

}

// New creates a new transaction pool to gather, sort and filter inbound
// transactions from the network.
func New(config Config, chain BlockChain) *FramePool {
	// Sanitize the input to ensure no vulnerable gas prices are set
	config = (&config).sanitize()

	// Create the transaction pool with its initial settings
	signer := types.LatestSigner(chain.Config())
	pool := &FramePool{
		config: config,
		signer: signer,
		chain:  chain,
	}

	return pool
}

func (pool *FramePool) Init(gasTip uint64, head *types.Header, reserver txpool.Reserver) error {
	// Set the address reserver to request exclusive access to pooled accounts
	pool.reserver = reserver

	// Set the basic pool parameters
	pool.gasTip.Store(uint256.NewInt(gasTip))

	// Initialize the state with head block, or fallback to empty one in
	// case the head state is not available (might occur when node is not
	// fully synced).
	statedb, err := pool.chain.StateAt(head.Root)
	if err != nil {
		statedb, err = pool.chain.StateAt(types.EmptyRootHash)
	}
	if err != nil {
		return err
	}
	pool.head.Store(head)
	pool.state = statedb

	return nil
}

// Filter is a selector used to decide whether a transaction would be added
// to this particular subpool.
func (pool *FramePool) Filter(tx *types.Transaction) bool {
	return pool.FilterType(tx.Type())
}

// FilterType returns whether the subpool supports the given transaction type.
func (pool *FramePool) FilterType(kind byte) bool {
	return kind == types.FrameTxType
}

// Close terminates any background processing threads and releases any held
// resources.
func (pool *FramePool) Close() error {
	// TODO
	return nil
}

// Reset retrieves the current state of the blockchain and ensures the content
// of the transaction pool is valid with regard to the chain state.
func (pool *FramePool) Reset(oldHead, newHead *types.Header) {
	// TODO
}

// SetGasTip updates the minimum price required by the subpool for a new
// transaction, and drops all transactions below this threshold.
func (pool *FramePool) SetGasTip(tip *big.Int) {
	// TODO
}

// Has returns an indicator whether subpool has a transaction cached with the
// given hash.
func (pool *FramePool) Has(hash common.Hash) bool {
	// TODO
	return false
}

// Get returns a transaction if it is contained in the pool, or nil otherwise.
func (pool *FramePool) Get(hash common.Hash) *types.Transaction {
	// TODO
	return nil
}

// GetRLP returns a RLP-encoded transaction if it is contained in the pool.
func (pool *FramePool) GetRLP(hash common.Hash) []byte {
	// TODO
	return nil
}

// GetMetadata returns the transaction type and transaction size with the
// given transaction hash.
func (pool *FramePool) GetMetadata(hash common.Hash) *txpool.TxMetadata {
	// TODO
	return nil
}

// ValidateTxBasics checks whether a transaction is valid according to the consensus
// rules, but does not check state-dependent validation such as sufficient balance.
// This check is meant as a static check which can be performed without holding the
// pool mutex.
func (pool *FramePool) ValidateTxBasics(tx *types.Transaction) error {
	// TODO
	return nil
}

// Add enqueues a batch of transactions into the pool if they are valid. Due
// to the large transaction churn, add may postpone fully integrating the tx
// to a later point to batch multiple ones together.
func (pool *FramePool) Add(txs []*types.Transaction, sync bool) []error {
	// TODO
	return nil
}

// Pending retrieves all currently processable transactions, grouped by origin
// account and sorted by nonce.
//
// The transactions can also be pre-filtered by the dynamic fee components to
// reduce allocations and load on downstream subsystems.
func (pool *FramePool) Pending(filter txpool.PendingFilter) map[common.Address][]*txpool.LazyTransaction {
	// TODO
	return nil
}

// SubscribeTransactions subscribes to new transaction events. The subscriber
// can decide whether to receive notifications only for newly seen transactions
// or also for reorged out ones.
func (pool *FramePool) SubscribeTransactions(ch chan<- core.NewTxsEvent, reorgs bool) event.Subscription {
	// TODO
	return nil
}

// Nonce returns the next nonce of an account, with all transactions executable
// by the pool already applied on top.
func (pool *FramePool) Nonce(addr common.Address) uint64 {
	// TODO
	return 0
}

// Stats retrieves the current pool stats, namely the number of pending and the
// number of queued (non-executable) transactions.
func (pool *FramePool) Stats() (int, int) {
	// TODO
	return 0, 0
}

// Content retrieves the data content of the transaction pool, returning all the
// pending as well as queued transactions, grouped by account and sorted by nonce.
func (pool *FramePool) Content() (map[common.Address][]*types.Transaction, map[common.Address][]*types.Transaction) {
	// TODO
	return nil, nil
}

// ContentFrom retrieves the data content of the transaction pool, returning the
// pending as well as queued transactions of this address, grouped by nonce.
func (pool *FramePool) ContentFrom(addr common.Address) ([]*types.Transaction, []*types.Transaction) {
	// TODO
	return nil, nil
}

// Status returns the known status (unknown/pending/queued) of a transaction
// identified by their hashes.
func (pool *FramePool) Status(hash common.Hash) txpool.TxStatus {
	// TODO
	return txpool.TxStatusUnknown
}

// Clear removes all tracked transactions from the pool
func (pool *FramePool) Clear() {
	// TODO
}
