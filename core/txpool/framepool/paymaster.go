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
	"fmt"
	"math"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/log"
	"github.com/holiman/uint256"
)

var (
	// TODO(EIP-8141): use correct codehash
	canonicalPaymasterCodehash = common.HexToHash("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	// pendingWithdrawalAmount field of canonical paymaster, slot 2
	canonicalPaymasterPendingWithdrawalAmountSlotHash = common.HexToHash("0x405787fa12a823e0f2b7631cc41b3ba8828b3321ca811111fa75cd3aa3bb5ace")
)

type paymaster struct {
	addr      common.Address
	canonical bool

	balance           *uint256.Int // The available ETH
	pendingWithdrawal *uint256.Int // The amount of ETH that canonical paymaster is pending to withdraw

	sponsoredTxs map[common.Hash]struct{} // The transactions that paymaster is sponsoring (including self-sponsored)
	reserved     *uint256.Int             // The ETH that is reserved for sponsered transaction

	lock sync.RWMutex
}

func newPaymaster(addr common.Address, state *state.StateDB) *paymaster {
	balance := state.GetBalance(addr)
	canonical := state.GetCodeHash(addr) == canonicalPaymasterCodehash

	pendingWithdrawalAmount := new(uint256.Int)
	if canonical {
		pendingWithdrawalAmount.SetBytes(state.GetState(addr, canonicalPaymasterPendingWithdrawalAmountSlotHash).Bytes())
	}

	return &paymaster{
		addr:      addr,
		canonical: canonical,

		balance:           balance,
		pendingWithdrawal: pendingWithdrawalAmount,

		sponsoredTxs: make(map[common.Hash]struct{}),
		reserved:     new(uint256.Int),
	}
}

func (p *paymaster) maxPendingTxs() uint {
	if p.canonical {
		return math.MaxUint
	}
	return maxPendingTxsPerNonCanonicalPaymaster
}

func (p *paymaster) isEmpty() bool {
	p.lock.RLock()
	defer p.lock.RUnlock()

	return len(p.sponsoredTxs) == 0
}

func (p *paymaster) onStateUpdate(
	state *state.StateDB,
	accountTouched bool,
	touchedSlots map[common.Hash]struct{},
) error {
	if accountTouched {
		// check whether paymaster started/stopped being canonical
		canonical := state.GetCodeHash(p.addr) == canonicalPaymasterCodehash
		if p.canonical != canonical {
			return fmt.Errorf("paymaster canonical status changed, old: %v new %v", p.canonical, canonical)
		}

		// update balance
		p.balance = state.GetBalance(p.addr)
	}

	if p.canonical {
		// check if pendingWithdrawalAmount was updated
		if _, ok := touchedSlots[canonicalPaymasterPendingWithdrawalAmountSlotHash]; ok {
			pendingWithdrawal := state.GetState(p.addr, canonicalPaymasterPendingWithdrawalAmountSlotHash)
			p.pendingWithdrawal.SetBytes(pendingWithdrawal.Bytes())
		}
	}
	return nil
}

func (p *paymaster) addTx(txMeta *frameTxMeta) bool {
	p.lock.Lock()
	defer p.lock.Unlock()

	txHash := txMeta.tx.Hash()

	// sanity check
	if p.addr != txMeta.payer() {
		log.Error("adding tx to paymaster that they don't pay for")
		return false
	}
	if _, ok := p.sponsoredTxs[txHash]; ok {
		log.Error("adding tx to paymaster that already has it")
		return false
	}

	if len(p.sponsoredTxs) >= int(p.maxPendingTxs()) {
		log.Warn("paymaster can't sponsor any more transactions")
		return false
	}

	txCost := uint256.MustFromBig(txMeta.tx.Cost())

	totalNewReserved := p.reserved.Clone()
	totalNewReserved.Add(totalNewReserved, p.pendingWithdrawal)
	totalNewReserved.Add(totalNewReserved, txCost)

	if p.balance.Lt(totalNewReserved) {
		return false
	}

	p.reserved.Add(p.reserved, txCost)
	p.sponsoredTxs[txHash] = struct{}{}
	return true
}

func (p *paymaster) removeTx(txMeta *frameTxMeta) bool {
	p.lock.Lock()
	defer p.lock.Unlock()

	txHash := txMeta.tx.Hash()

	_, ok := p.sponsoredTxs[txHash]
	if !ok {
		log.Error("paymaster doesn't have tx")
		return false
	}

	delete(p.sponsoredTxs, txHash)
	p.reserved.Sub(p.reserved, uint256.MustFromBig(txMeta.tx.Cost()))

	return true
}
