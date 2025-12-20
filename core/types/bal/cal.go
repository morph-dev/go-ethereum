package bal

import (
	"bytes"
	"cmp"
	"maps"
	"slices"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

func (a *ConstructionAccountAccesses) toEncodingCalObj(addr common.Address, firstIdx, lastIdx uint16) AccountAccess {
	res := AccountAccess{
		Address:        addr,
		StorageChanges: make([]encodingSlotWrites, 0),
		StorageReads:   make([]*EncodedStorage, 0),
		BalanceChanges: make([]encodingBalanceChange, 0),
		NonceChanges:   make([]encodingAccountNonce, 0),
		CodeChanges:    make([]CodeChange, 0),
	}

	accessedSlots := make(map[common.Hash]struct{})

	// Convert write slots
	writeSlots := slices.Collect(maps.Keys(a.StorageWrites))
	slices.SortFunc(writeSlots, common.Hash.Cmp)
	for _, slot := range writeSlots {
		slotWrites := a.StorageWrites[slot]
		obj := encodingSlotWrites{
			Slot:     newEncodedStorageFromHash(slot),
			Accesses: make([]encodingStorageWrite, 0, len(slotWrites)),
		}

		indices := slices.Collect(maps.Keys(slotWrites))
		slices.SortFunc(indices, cmp.Compare[uint16])
		for _, index := range indices {
			if index > lastIdx {
				break
			}
			accessedSlots[slot] = struct{}{}
			if index < firstIdx {
				continue
			}
			obj.Accesses = append(obj.Accesses, encodingStorageWrite{
				TxIdx:      index,
				ValueAfter: newEncodedStorageFromHash(slotWrites[index]),
			})
		}
		if len(obj.Accesses) > 0 {
			res.StorageChanges = append(res.StorageChanges, obj)
		}
	}

	// Convert read slots
	readSlots := slices.Collect(maps.Keys(a.StorageReads))
	slices.SortFunc(readSlots, common.Hash.Cmp)
	for _, slot := range readSlots {
		idx := a.StorageReads[slot]
		if idx < firstIdx {
			continue
		}
		if idx > lastIdx {
			break
		}
		if _, accessed := accessedSlots[slot]; !accessed {
			res.StorageReads = append(res.StorageReads, newEncodedStorageFromHash(slot))
		}
	}

	// Convert balance changes
	balanceIndices := slices.Collect(maps.Keys(a.BalanceChanges))
	slices.SortFunc(balanceIndices, cmp.Compare[uint16])
	for _, idx := range balanceIndices {
		if idx < firstIdx {
			continue
		}
		if idx > lastIdx {
			break
		}
		res.BalanceChanges = append(res.BalanceChanges, encodingBalanceChange{
			TxIdx:   idx,
			Balance: new(uint256.Int).Set(a.BalanceChanges[idx]),
		})
	}

	// Convert nonce changes
	nonceIndices := slices.Collect(maps.Keys(a.NonceChanges))
	slices.SortFunc(nonceIndices, cmp.Compare[uint16])
	for _, idx := range nonceIndices {
		if idx < firstIdx {
			continue
		}
		if idx > lastIdx {
			break
		}
		res.NonceChanges = append(res.NonceChanges, encodingAccountNonce{
			TxIdx: idx,
			Nonce: a.NonceChanges[idx],
		})
	}

	// Convert code change
	codeChangeIdxs := slices.Collect(maps.Keys(a.CodeChanges))
	slices.SortFunc(codeChangeIdxs, cmp.Compare[uint16])
	for _, idx := range codeChangeIdxs {
		if idx < firstIdx {
			continue
		}
		if idx > lastIdx {
			break
		}
		res.CodeChanges = append(res.CodeChanges, CodeChange{
			idx,
			bytes.Clone(a.CodeChanges[idx].Code),
		})
	}
	return res
}

func (c *AccessListBuilder) ToChunkAccessList(firstIdx, lastIdx uint16) *BlockAccessList {
	var addresses []common.Address
	for addr := range c.FinalizedAccesses {
		addresses = append(addresses, addr)
	}
	slices.SortFunc(addresses, common.Address.Cmp)

	var res BlockAccessList = make(BlockAccessList, 0, len(addresses))
	for _, addr := range addresses {
		constructionAccountAccess := c.FinalizedAccesses[addr]
		accountAddress := constructionAccountAccess.toEncodingCalObj(addr, firstIdx, lastIdx)
		// Check that there is at least some change in the range
		if len(accountAddress.StorageChanges) == 0 &&
			len(accountAddress.StorageReads) == 0 &&
			len(accountAddress.BalanceChanges) == 0 &&
			len(accountAddress.NonceChanges) == 0 &&
			len(accountAddress.CodeChanges) == 0 {
			// If Account has no changes, check that it wasn't accessed for the first time in this period.
			if constructionAccountAccess.firstIdx < firstIdx || constructionAccountAccess.firstIdx > lastIdx {
				continue
			}
		}
		res = append(res, accountAddress)
	}
	return &res
}

func MergeCals(cals ...BlockAccessList) BlockAccessList {
	res := make(BlockAccessList, 0)
	for _, cal := range cals {
		for aaIndex := range cal {
			aa := cal[aaIndex].Copy()

			index := slices.IndexFunc(res,
				func(res_aa AccountAccess) bool { return res_aa.Address == aa.Address },
			)
			if index == -1 {
				res = append(res, aa)
				continue
			}
			res_aa := &res[index]

			// Merge StorageChanges
			for _, sw := range aa.StorageChanges {
				index := slices.IndexFunc(res_aa.StorageChanges,
					func(res_sw encodingSlotWrites) bool { return *res_sw.Slot.inner == *sw.Slot.inner },
				)
				if index == -1 {
					res_aa.StorageChanges = append(res_aa.StorageChanges, sw)
					continue
				}
				res_sw := &res_aa.StorageChanges[index]
				res_sw.Accesses = append(res_sw.Accesses, sw.Accesses...)
			}

			// Merge StorageReads
			res_aa.StorageReads = append(res_aa.StorageReads, aa.StorageReads...)
			res_aa.StorageReads = slices.DeleteFunc(res_aa.StorageReads,
				func(slot *EncodedStorage) bool {
					return slices.ContainsFunc(res_aa.StorageChanges,
						func(sw encodingSlotWrites) bool { return *sw.Slot.inner == *slot.inner },
					)
				},
			)

			// Merge BalanceChanges
			res_aa.BalanceChanges = append(res_aa.BalanceChanges, aa.BalanceChanges...)

			// Merge NonceChanges
			res_aa.NonceChanges = append(res_aa.NonceChanges, aa.NonceChanges...)

			// Merge Code Changes
			res_aa.CodeChanges = append(res_aa.CodeChanges, aa.CodeChanges...)

		}
	}

	// Sort resulting object, and all internal lists
	slices.SortStableFunc(res, func(a, b AccountAccess) int { return a.Address.Cmp(b.Address) })
	for index := range res {
		aa := &res[index]
		// Storage Changes
		slices.SortStableFunc(aa.StorageChanges,
			func(a, b encodingSlotWrites) int { return a.Slot.inner.Cmp(b.Slot.inner) },
		)
		for index := range aa.StorageChanges {
			sw := &aa.StorageChanges[index]
			slices.SortStableFunc(sw.Accesses,
				func(a, b encodingStorageWrite) int { return cmp.Compare(a.TxIdx, b.TxIdx) },
			)
		}

		// Storage Reads
		slices.SortStableFunc(aa.StorageReads,
			func(a, b *EncodedStorage) int { return a.inner.Cmp(b.inner) })

		// BalanceChanges
		slices.SortStableFunc(aa.BalanceChanges,
			func(a, b encodingBalanceChange) int { return cmp.Compare(a.TxIdx, b.TxIdx) },
		)

		// Nonce
		slices.SortStableFunc(aa.NonceChanges,
			func(a, b encodingAccountNonce) int { return cmp.Compare(a.TxIdx, b.TxIdx) },
		)

		// Code
		slices.SortStableFunc(aa.CodeChanges,
			func(a, b CodeChange) int { return cmp.Compare(a.TxIdx, b.TxIdx) },
		)
	}

	return res
}
