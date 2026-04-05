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
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

var (
	canonicalPaymasterCodehash = common.HexToHash("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
)

type paymaster struct {
	address   common.Address
	canonical bool

	balance           *big.Int
	reserved          *big.Int
	pendingWithdrawal *big.Int

	sponsored map[common.Address]*frameTxMeta
}

func (p *paymaster) maxPendingTxs() uint {
	if p.canonical {
		return math.MaxUint
	}
	return maxPendingTxsPerNonCanonicalPaymaster
}
