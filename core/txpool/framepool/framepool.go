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
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

const (
	// maxVerifyGas is the maximum cumulative gas the validation prefix can use
	maxVerifyGas = 100_000

	// maxPendingTxsPerNonCanonicalPaymaster is the maximum number of pending transaction for a non-canonical paymaster
	maxPendingTxsPerNonCanonicalPaymaster = 1

	// txMaxSize is the maximum size a single transaction can have
	txMaxSize = 1024 * 1024

	// TODO: This should most likely be params.BlobTxMaxBlobs
	// Some logic from BlobPool should be extracted so it can easily be reused here.
	maxBlobsPerTx = 0
)

type FramePool struct {
	config Config // Pool configuration

	signer types.Signer // Transaction signer to use for sender recovery
	chain  BlockChain   // Chain object to access the state through

	reserver txpool.Reserver             // Address reserver to ensure exclusivity across subpools
	gasTip   atomic.Pointer[uint256.Int] // Currently accepted minimum gas tip

	head  atomic.Pointer[types.Header] // Current head of the chain
	state *state.StateDB               // Current state at the head of the chain

	discoverFeed event.Feed // Event feed to send out new tx events on pool discovery (reorg excluded)
	insertFeed   event.Feed // Event feed to send out new tx events on pool inclusion (reorg included)

	lookup     map[common.Hash]*frameTxMeta    // Lookup table by tx hash
	index      map[common.Address]*frameTxMeta // Lookup table by address
	paymasters map[common.Address]*paymaster   // Paymaster tracking info

	lock sync.RWMutex // Mutex protecting the pool from concurrent access
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

		lookup:     make(map[common.Hash]*frameTxMeta),
		index:      make(map[common.Address]*frameTxMeta),
		paymasters: make(map[common.Address]*paymaster),
	}

	return pool
}

func (p *FramePool) Init(gasTip uint64, head *types.Header, reserver txpool.Reserver) error {
	p.lock.Lock()
	defer p.lock.Unlock()

	// Set the address reserver to request exclusive access to pooled accounts
	p.reserver = reserver

	// Set the basic pool parameters
	p.gasTip.Store(uint256.NewInt(gasTip))

	// Initialize the state with head block, or fallback to empty one in
	// case the head state is not available (might occur when node is not
	// fully synced).
	statedb, err := p.chain.StateAt(head.Root)
	if err != nil {
		statedb, err = p.chain.StateAt(types.EmptyRootHash)
	}
	if err != nil {
		return err
	}
	p.head.Store(head)
	p.state = statedb

	return nil
}

// Filter is a selector used to decide whether a transaction would be added
// to this particular subpool.
func (p *FramePool) Filter(tx *types.Transaction) bool {
	return p.FilterType(tx.Type())
}

// FilterType returns whether the subpool supports the given transaction type.
func (p *FramePool) FilterType(kind byte) bool {
	return kind == types.FrameTxType
}

// Close terminates any background processing threads and releases any held
// resources.
func (p *FramePool) Close() error {
	log.Info("Frame pool stopped")
	return nil
}

// Reset retrieves the current state of the blockchain and ensures the content
// of the transaction pool is valid with regard to the chain state.
func (p *FramePool) Reset(oldHead, newHead *types.Header) {
	newState, err := p.chain.StateAt(newHead.Hash())
	if err != nil {
		log.Error("can't get state", "block hash", newHead.Hash())
		return
	}

	reorgDiffs := CalcReorgDiffs(p.chain, oldHead, newHead)

	p.lock.Lock()
	defer p.lock.Unlock()

	p.head.Store(newHead)
	p.state = newState

	if reorgDiffs == nil {
		// We couldn't obtain all blocks between old and new head.
		// For now, just remove all txs from pool and add them again.
		// TODO(EIP-8141): Optimize this case.
		txs := p.removeAllLocked()
		for _, tx := range txs {
			p.addTxLocked(tx, true /* =fromReorg */)
		}
		return
	}

	// Remove all executed transactions
	for _, tx := range reorgDiffs.ExecutedTransactions {
		if txMeta, ok := p.lookup[tx.Hash()]; ok {
			p.removeTxLocked(txMeta)
		}
	}

	affectedTransactions := make([]*frameTxMeta, 0)

	// Remove all transactions affected by touched state
	for addr, txMeta := range p.index {
		affected := false

		if _, ok := reorgDiffs.TouchedAccounts[addr]; ok {
			affected = true
		} else {
			// check touched storage as well
			touchedSlots, ok := reorgDiffs.TouchedStorage[addr]
			if !ok {
				continue
			}
			for slot := range txMeta.storageReads {
				if _, ok := touchedSlots[slot]; ok {
					affected = true
					break
				}
			}
		}

		if affected {
			affectedTransactions = append(affectedTransactions, txMeta)
			p.removeTxLocked(txMeta)
		}
	}

	// Update all paymasters
	for addr, paymaster := range p.paymasters {
		_, accountTouched := reorgDiffs.TouchedAccounts[addr]
		touchedSlots, _ := reorgDiffs.TouchedStorage[addr]

		if accountTouched || len(touchedSlots) > 0 {
			err := paymaster.onStateUpdate(newState, accountTouched, touchedSlots)
			if err != nil {
				log.Info("error updating paymaster during reorg", "err", err)
				// remove all transactions that paymaster is sponsoring
				for txHash := range paymaster.sponsoredTxs {
					if txMeta, ok := p.lookup[txHash]; ok {
						affectedTransactions = append(affectedTransactions, txMeta)
						p.removeTxLocked(txMeta)
					} else {
						log.Error("tx from paymaster not found in lookup")
					}
				}
				// verify that paymaster is no longer present
				if _, ok := p.paymasters[addr]; ok {
					panic("paymaster still present")
				}
				// transactions and paymaster will be added when affectedTransactions are re-added
			}
		}
	}

	// TODO(EIP-8141): consider starting background task to add txs and return early here

	// Add all reverted transactions
	for _, tx := range reorgDiffs.RevertedTransactions {
		if p.ValidateTxBasics(tx) != nil {
			continue
		}
		txMeta, err := newTxMeta(tx)
		if err != nil {
			log.Warn("can't create txMeta for reverted transaction", "txHash", tx.Hash())
			continue
		}
		p.addTxLocked(txMeta, true /* =fromReorg */)
	}

	// Re-add all affected transactions
	for _, txMeta := range affectedTransactions {
		if p.ValidateTxBasics(txMeta.tx) != nil {
			continue
		}
		p.addTxLocked(txMeta, true /* =fromReorg */)
	}
}

// SetGasTip updates the minimum price required by the subpool for a new
// transaction, and drops all transactions below this threshold.
func (p *FramePool) SetGasTip(tip *big.Int) {
	p.lock.Lock()
	defer p.lock.Unlock()

	newTip := uint256.MustFromBig(tip)
	old := p.gasTip.Load()
	p.gasTip.Store(newTip)

	if newTip.Cmp(old) > 0 {
		for _, txMeta := range p.index {
			if txMeta.tx.GasTipCapIntCmp(tip) < 0 {
				p.removeTxLocked(txMeta)
			}
		}
	}
}

// Has returns an indicator whether subpool has a transaction cached with the
// given hash.
func (p *FramePool) Has(hash common.Hash) bool {
	return p.Get(hash) != nil
}

// Get returns a transaction if it is contained in the pool, or nil otherwise.
func (p *FramePool) Get(hash common.Hash) *types.Transaction {
	p.lock.RLock()
	defer p.lock.RUnlock()

	txMeta := p.lookup[hash]
	if txMeta == nil {
		return nil
	}
	return txMeta.tx
}

// GetRLP returns a RLP-encoded transaction if it is contained in the pool.
func (p *FramePool) GetRLP(hash common.Hash) []byte {
	tx := p.Get(hash)
	if tx == nil {
		return nil
	}

	encoded, err := rlp.EncodeToBytes(tx)
	if err != nil {
		log.Error("Failed to encoded transaction in framepool", "hash", hash, "err", err)
		return nil
	}

	return encoded
}

// GetMetadata returns the transaction type and transaction size with the
// given transaction hash.
func (p *FramePool) GetMetadata(hash common.Hash) *txpool.TxMetadata {
	tx := p.Get(hash)
	if tx == nil {
		return nil
	}
	return &txpool.TxMetadata{
		Type: tx.Type(),
		Size: tx.Size(),
	}
}

// ValidateTxBasics checks whether a transaction is valid according to the consensus
// rules, but does not check state-dependent validation such as sufficient balance.
// This check is meant as a static check which can be performed without holding the
// pool mutex.
func (p *FramePool) ValidateTxBasics(tx *types.Transaction) error {
	opts := &txpool.ValidationOptions{
		Config:       p.chain.Config(),
		Accept:       1 << types.FrameTxType,
		MaxSize:      txMaxSize,
		MinTip:       p.gasTip.Load().ToBig(),
		MaxBlobCount: maxBlobsPerTx,
	}
	if err := txpool.ValidateTransaction(tx, p.head.Load(), p.signer, opts); err != nil {
		return err
	}

	txMeta, err := newTxMeta(tx)
	if err != nil {
		return err
	}
	if txMeta.validationGasLimit() > maxVerifyGas {
		return txpool.ErrFrameTxValidationGasLimit
	}
	return nil
}

// Add enqueues a batch of transactions into the pool if they are valid. Due
// to the large transaction churn, add may postpone fully integrating the tx
// to a later point to batch multiple ones together.
func (p *FramePool) Add(txs []*types.Transaction, sync bool) []error {
	errs := make([]error, len(txs))

	for i, tx := range txs {
		hash := tx.Hash()
		if p.Has(hash) {
			errs[i] = txpool.ErrAlreadyKnown
			continue
		}

		if err := p.ValidateTxBasics(tx); err != nil {
			errs[i] = err
			continue
		}

		txMeta, err := newTxMeta(tx)
		if err != nil {
			errs[i] = err
			continue
		}

		p.lock.Lock()
		if err := p.addTxLocked(txMeta, false /* =fromReorg */); err != nil {
			errs[i] = err
		}
		p.lock.Unlock()
	}
	return nil
}

// Pending retrieves all currently processable transactions, grouped by origin
// account and sorted by nonce.
//
// The transactions can also be pre-filtered by the dynamic fee components to
// reduce allocations and load on downstream subsystems.
func (p *FramePool) Pending(filter txpool.PendingFilter) map[common.Address][]*txpool.LazyTransaction {
	p.lock.RLock()
	defer p.lock.RUnlock()

	pending := make(map[common.Address][]*txpool.LazyTransaction, len(p.index))
	for addr, txMeta := range p.index {
		tx := txMeta.tx

		if filter.BaseFee != nil && uint256.MustFromBig(tx.GasFeeCap()).Lt(filter.BaseFee) {
			continue
		}

		if tx.EffectiveGasTipIntCmp(filter.MinTip, filter.BaseFee) < 0 {
			continue
		}

		if filter.GasLimitCap != 0 && tx.Gas() > filter.GasLimitCap {
			continue
		}

		if filter.BlobTxs {
			if len(tx.BlobHashes()) == 0 {
				continue
			}
			// TODO: check filter.BlobVersion

			if filter.BlobFee != nil && uint256.MustFromBig(tx.BlobGasFeeCap()).Lt(filter.BlobFee) {
				continue
			}
		} else {
			if len(tx.BlobHashes()) != 0 {
				continue
			}
		}

		lazies := make([]*txpool.LazyTransaction, 1)
		lazies[0] = &txpool.LazyTransaction{
			Pool:      p,
			Hash:      tx.Hash(),
			Tx:        tx,
			Time:      tx.Time(),
			GasFeeCap: uint256.MustFromBig(tx.GasFeeCap()),
			GasTipCap: uint256.MustFromBig(tx.GasTipCap()),
			Gas:       tx.Gas(),
			BlobGas:   tx.BlobGas(),
		}
		pending[addr] = lazies
	}
	return pending
}

// SubscribeTransactions subscribes to new transaction events. The subscriber
// can decide whether to receive notifications only for newly seen transactions
// or also for reorged out ones.
func (p *FramePool) SubscribeTransactions(ch chan<- core.NewTxsEvent, reorgs bool) event.Subscription {
	if reorgs {
		return p.insertFeed.Subscribe(ch)
	} else {
		return p.discoverFeed.Subscribe(ch)
	}
}

// Nonce returns the next nonce of an account, with all transactions executable
// by the pool already applied on top.
func (p *FramePool) Nonce(addr common.Address) uint64 {
	// We need a write lock here, since state.GetNonce might write the cache.
	p.lock.Lock()
	defer p.lock.Unlock()

	if txMeta, ok := p.index[addr]; ok {
		return txMeta.tx.Nonce() + 1
	}
	return p.state.GetNonce(addr)
}

// Stats retrieves the current pool stats, namely the number of pending and the
// number of queued (non-executable) transactions.
func (p *FramePool) Stats() (int, int) {
	p.lock.RLock()
	defer p.lock.RUnlock()

	return len(p.index), 0 // No non-executable txs in the frame pool
}

// Content retrieves the data content of the transaction pool, returning all the
// pending as well as queued transactions, grouped by account and sorted by nonce.
func (p *FramePool) Content() (map[common.Address][]*types.Transaction, map[common.Address][]*types.Transaction) {
	p.lock.RLock()
	defer p.lock.RUnlock()

	pending := make(map[common.Address][]*types.Transaction, len(p.index))
	for addr, txMeta := range p.index {
		pending[addr] = []*types.Transaction{txMeta.tx}
	}
	return pending, map[common.Address][]*types.Transaction{}
}

// ContentFrom retrieves the data content of the transaction pool, returning the
// pending as well as queued transactions of this address, grouped by nonce.
func (p *FramePool) ContentFrom(addr common.Address) ([]*types.Transaction, []*types.Transaction) {
	p.lock.RLock()
	defer p.lock.RUnlock()

	if txMeta, ok := p.index[addr]; ok {
		return []*types.Transaction{txMeta.tx}, []*types.Transaction{}
	}

	return []*types.Transaction{}, []*types.Transaction{}
}

// Status returns the known status (unknown/pending/queued) of a transaction
// identified by their hashes.
func (p *FramePool) Status(hash common.Hash) txpool.TxStatus {
	if p.Has(hash) {
		return txpool.TxStatusPending
	}
	return txpool.TxStatusUnknown
}

// Clear removes all tracked transactions from the pool
func (p *FramePool) Clear() {
	p.lock.Lock()
	defer p.lock.Unlock()

	p.lookup = make(map[common.Hash]*frameTxMeta)
	p.index = make(map[common.Address]*frameTxMeta)
	p.paymasters = make(map[common.Address]*paymaster)
}

// Validates and adds tx to the pool. Updates frameTxMeta.storageReads if successful.
// Must hold the lock before calling this function.
func (p *FramePool) addTxLocked(txMeta *frameTxMeta, fromReorg bool) (err error) {
	if txMeta.validationGasLimit() > maxVerifyGas {
		return txpool.ErrFrameTxValidationGasLimit
	}

	header := p.head.Load()

	if _, ok := p.index[txMeta.sender]; ok {
		// TODO(EIP-8141): add support for updating transactions
		log.Warn("upgrading transaction is not yet supported")
		return fmt.Errorf("transaction for sender %v already exists", txMeta.sender)
	}

	if err := p.reserver.Hold(txMeta.sender); err != nil {
		return err
	}
	defer func() {
		// If the transaction is rejected by some later check, remove the lock
		// on the reservation set.
		//
		// Note, `err` here is the named error return, which will be initialized
		// by a return statement before running deferred methods. Take care with
		// removing or subscoping err as it will break this clause.
		if err != nil {
			p.reserver.Release(txMeta.sender)
		}
	}()

	// Initialize state tracer
	tracer := newValidationStateTracer(txMeta.sender)
	hooks := &tracing.Hooks{
		OnStorageRead: tracer.OnStorageRead,
	}
	hookedState := state.NewHookedState(p.state.Copy(), hooks)

	blockContext := core.NewEVMBlockContext(header, p.chain, nil)
	evmConfig := vm.Config{
		Tracer:                  hooks,
		NoBaseFee:               true,
		FrameTxValidation:       true,
		FrameTxValidationPrefix: txMeta.validationPrefix.Length(),
	}
	evm := vm.NewEVM(blockContext, hookedState, p.chain.Config(), evmConfig)

	msg, err := core.TransactionToMessage(txMeta.tx, p.signer, header.BaseFee)
	if err != nil {
		return err
	}

	gasPool := new(core.GasPool).AddGas(msg.GasLimit)

	res, err := core.ApplyMessage(evm, msg, gasPool)
	if err != nil {
		log.Warn("frame tx is invalid", "err", err)
		return err
	}

	for i, r := range res.FrameReceipts {
		if r.Status != 1 {
			log.Warn("frame tx is invalid", "frame", i, "status", r.Status)
			return errors.New("frame validation failed")
		}
	}
	txMeta.storageReads = tracer.reads

	payer := txMeta.payer()
	paymaster, ok := p.paymasters[payer]
	if !ok {
		paymaster = newPaymaster(payer, p.state)
		p.paymasters[payer] = paymaster
	}
	if !paymaster.addTx(txMeta) {
		return errors.New("paymaster insufficient balance")
	}

	p.index[txMeta.sender] = txMeta
	p.lookup[txMeta.tx.Hash()] = txMeta

	p.insertFeed.Send(core.NewTxsEvent{Txs: []*types.Transaction{txMeta.tx}})
	if !fromReorg {
		p.discoverFeed.Send(core.NewTxsEvent{Txs: []*types.Transaction{txMeta.tx}})
	}
	return nil
}

// Removes transaction from the pool.
// Must hold the lock before calling this function.
func (p *FramePool) removeTxLocked(txMeta *frameTxMeta) {
	txMeta.storageReads = nil

	delete(p.index, txMeta.sender)
	delete(p.lookup, txMeta.tx.Hash())

	payer := txMeta.payer()

	if paymaster, ok := p.paymasters[payer]; ok {
		paymaster.removeTx(txMeta)
		if paymaster.isEmpty() {
			delete(p.paymasters, payer)
		}
	}
	p.reserver.Release(txMeta.sender)
}

// Removes all transaction from the pool.
// Must hold the lock before calling this function.
func (p *FramePool) removeAllLocked() []*frameTxMeta {
	txs := slices.Collect(maps.Values(p.index))

	for _, txMeta := range p.index {
		p.reserver.Release(txMeta.sender)
	}

	clear(p.index)
	clear(p.lookup)
	clear(p.paymasters)

	return txs
}
