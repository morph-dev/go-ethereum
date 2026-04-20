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
	"math"
	"slices"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/log"
	"github.com/holiman/uint256"
)

var (
	canonicalPaymasterCodehash = common.HexToHash("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	// pendingWithdrawalAmount field of canonical paymaster, slot 2
	canonicalPaymasterPendingWithdrawalAmount = common.HexToHash("0x405787fa12a823e0f2b7631cc41b3ba8828b3321ca811111fa75cd3aa3bb5ace")
)

type paymaster struct {
	addr      common.Address
	canonical bool

	balance           *uint256.Int
	reserved          *uint256.Int
	pendingWithdrawal *uint256.Int
	selfSponsored     *common.Hash
	sponsored         []common.Hash

	lock sync.RWMutex
}

func newPaymaster(addr common.Address, state *state.StateDB) *paymaster {
	balance := state.GetBalance(addr)
	canonical := state.GetCodeHash(addr) == canonicalPaymasterCodehash

	pendingWithdrawalAmount := new(uint256.Int)
	if canonical {
		pendingWithdrawalAmount.SetBytes(state.GetState(addr, canonicalPaymasterPendingWithdrawalAmount).Bytes())
	}

	return &paymaster{
		addr:      addr,
		canonical: canonical,

		balance:           balance,
		reserved:          new(uint256.Int),
		pendingWithdrawal: pendingWithdrawalAmount,
		selfSponsored:     nil,
		sponsored:         make([]common.Hash, 0),
	}
}

func (p *paymaster) maxPendingTxs() uint {
	p.lock.RLock()
	defer p.lock.RUnlock()

	if p.canonical {
		return math.MaxUint
	}
	return maxPendingTxsPerNonCanonicalPaymaster
}

func (p *paymaster) isEmpty() bool {
	p.lock.RLock()
	defer p.lock.RUnlock()

	return p.selfSponsored == nil && len(p.sponsored) == 0
}

func (p *paymaster) addTx(txMeta *frameTxMeta) bool {
	p.lock.Lock()
	defer p.lock.Unlock()

	// sanity check
	if p.addr != txMeta.payer() {
		log.Error("adding tx to paymaster thay they don't pay for")
		return false
	}

	if p.addr == txMeta.sender && p.selfSponsored != nil {
		log.Error("adding self-sponsored tx to paymaster, when it already has one")
		return false
	}

	txHash := txMeta.tx.Hash()
	txCost := uint256.MustFromBig(txMeta.tx.Cost())

	available := p.balance.Clone()
	available.Sub(available, p.reserved)
	available.Sub(available, p.pendingWithdrawal)
	if available.Lt(txCost) {
		return false
	}

	p.reserved.Add(p.reserved, txCost)
	if p.addr == txMeta.sender {
		p.selfSponsored = &txHash
	} else {
		p.sponsored = append(p.sponsored, txHash)
	}
	return true
}

func (p *paymaster) removeTx(txMeta *frameTxMeta) bool {
	p.lock.Lock()
	defer p.lock.Unlock()

	txHash := txMeta.tx.Hash()
	txCost := uint256.MustFromBig(txMeta.tx.Cost())

	if p.selfSponsored != nil && *p.selfSponsored == txHash {
		p.selfSponsored = nil
		p.reserved.Sub(p.reserved, txCost)
		return true
	}

	i := slices.Index(p.sponsored, txHash)
	if i == -1 {
		log.Error("paymaster doesn't have tx")
		return false
	}

	p.sponsored = slices.Delete(p.sponsored, i, i+1)
	p.reserved.Sub(p.reserved, txCost)
	return true
}
