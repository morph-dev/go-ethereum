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
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
)

type validationStateTracer struct {
	sender common.Address
	reads  map[common.Hash]struct{}
}

func newValidationStateTracer(sender common.Address) *validationStateTracer {
	return &validationStateTracer{
		sender: sender,
		reads:  make(map[common.Hash]struct{}),
	}
}

func (t *validationStateTracer) OnStorageRead(addr common.Address, slot common.Hash) {
	if t.sender != addr {
		log.Error("frame tx validation accessing non-sender storage", "sender", t.sender, "addr", addr)
		return
	}
	t.reads[slot] = struct{}{}
}
