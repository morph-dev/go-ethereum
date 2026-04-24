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

package framepool

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

type ReorgDiffs struct {
	// Accounts that were modified during head reorg (balance, nonce or code hash)
	TouchedAccounts map[common.Address]struct{}
	// Storage slots that were modified during head reorg
	TouchedStorage map[common.Address]map[common.Hash]struct{}

	// Transactions that were present in the "old" chain, but not in the "new" one
	RevertedTransactions map[common.Hash]*types.Transaction
	// Transactions that were present in the "new" chain, but not in the "old" one
	ExecutedTransactions map[common.Hash]*types.Transaction
}

func CalcReorgDiffs(chain BlockChain, oldHeader, newHeader *types.Header) *ReorgDiffs {
	old := chain.GetBlock(oldHeader.Hash(), oldHeader.Number.Uint64())
	if old == nil {
		log.Error("block not found", "hash", oldHeader.Hash(), "number", oldHeader.Number.Uint64())
		return nil
	}
	new := chain.GetBlock(newHeader.Hash(), newHeader.Number.Uint64())
	if new == nil {
		log.Error("block not found", "hash", newHeader.Hash(), "number", newHeader.Number.Uint64())
		return nil
	}

	reorgDiffs := &ReorgDiffs{
		TouchedAccounts:      make(map[common.Address]struct{}),
		TouchedStorage:       make(map[common.Address]map[common.Hash]struct{}),
		RevertedTransactions: make(map[common.Hash]*types.Transaction),
		ExecutedTransactions: make(map[common.Hash]*types.Transaction),
	}

	for old.NumberU64() > new.NumberU64() {
		if err := reorgDiffs.addBal(old); err != nil {
			log.Error("can't add bal to state touches", "err", err)
			return nil
		}
		reorgDiffs.addRevertedTransactions(old.Transactions())
		if old = chain.GetBlock(old.ParentHash(), old.NumberU64()-1); old == nil {
			log.Error("block not found", "hash", old.ParentHash(), "number", old.NumberU64()-1)
			return nil
		}
	}

	for new.NumberU64() > old.NumberU64() {
		if err := reorgDiffs.addBal(old); err != nil {
			log.Error("can't add bal to state touches", "err", err)
			return nil
		}
		reorgDiffs.addExecutedTransactions(old.Transactions())
		if new = chain.GetBlock(new.ParentHash(), new.NumberU64()-1); new == nil {
			log.Error("block not found", "hash", new.ParentHash(), "number", new.NumberU64()-1)
			return nil
		}
	}

	for old.Hash() != new.Hash() {
		reorgDiffs.addRevertedTransactions(old.Transactions())
		if old = chain.GetBlock(old.ParentHash(), old.NumberU64()-1); old == nil {
			log.Error("block not found", "hash", old.ParentHash(), "number", old.NumberU64()-1)
			return nil
		}

		reorgDiffs.addExecutedTransactions(old.Transactions())
		if new = chain.GetBlock(new.ParentHash(), new.NumberU64()-1); new == nil {
			log.Error("block not found", "hash", new.ParentHash(), "number", new.NumberU64()-1)
			return nil
		}
	}

	return reorgDiffs
}

func (d *ReorgDiffs) addBal(block *types.Block) error {
	bal := block.AccessList()
	if bal == nil {
		return fmt.Errorf("bal not found, block hash: %v number: %v", block.Hash(), block.NumberU64())
	}
	for _, changes := range *bal {
		address := changes.Address
		if len(changes.BalanceChanges) > 0 || len(changes.NonceChanges) > 0 || len(changes.CodeChanges) > 0 {
			d.addAccount(address)
		}
		for _, storageChanges := range changes.StorageChanges {
			slot := storageChanges.Slot.ToHash()
			d.addStorage(address, slot)
		}
	}
	return nil
}

func (d *ReorgDiffs) addAccount(address common.Address) {
	if _, ok := d.TouchedAccounts[address]; !ok {
		d.TouchedAccounts[address] = struct{}{}
	}
}

func (d *ReorgDiffs) addStorage(address common.Address, slot common.Hash) {
	touchedSlots, ok := d.TouchedStorage[address]
	if !ok {
		touchedSlots = make(map[common.Hash]struct{})
		d.TouchedStorage[address] = touchedSlots
	}
	if _, ok := touchedSlots[slot]; !ok {
		touchedSlots[slot] = struct{}{}
	}
}

func (d *ReorgDiffs) addRevertedTransactions(txs types.Transactions) {
	for _, tx := range txs {
		txHash := tx.Hash()
		if _, ok := d.ExecutedTransactions[txHash]; ok {
			delete(d.ExecutedTransactions, txHash)
		} else {
			d.RevertedTransactions[txHash] = tx
		}
	}
}

func (d *ReorgDiffs) addExecutedTransactions(txs types.Transactions) {
	for _, tx := range txs {
		txHash := tx.Hash()
		if _, ok := d.RevertedTransactions[txHash]; ok {
			delete(d.RevertedTransactions, txHash)
		} else {
			d.ExecutedTransactions[txHash] = tx
		}
	}
}
