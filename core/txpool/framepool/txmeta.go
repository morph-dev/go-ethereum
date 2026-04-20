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

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type frameTxMeta struct {
	tx               *types.Transaction
	sender           common.Address
	frames           []types.FrameTxFrame
	validationPrefix validationPrefix

	storageReads map[common.Hash]struct{}
}

func newTxMeta(tx *types.Transaction) (*frameTxMeta, error) {
	sender := tx.FrameSender()
	if sender == nil {
		return nil, errors.New("missing frame sender")
	}

	frames := tx.Frames()
	if frames == nil {
		return nil, errors.New("missing frames")
	}

	validationPrefix, ok := getValidationPrefix(sender, frames)
	if !ok {
		return nil, errors.New("unknown validation prefix")
	}

	meta := frameTxMeta{
		tx:               tx,
		sender:           *sender,
		frames:           frames,
		validationPrefix: validationPrefix,
	}
	return &meta, nil
}

func (f *frameTxMeta) payer() common.Address {
	payer := f.frames[f.validationPrefix.PayerFrameIndex()].Target
	if payer == nil {
		return f.sender
	}
	return *payer
}

func (f frameTxMeta) validationFrames() []types.FrameTxFrame {
	return f.frames[:f.validationPrefix.Length()]
}

func (f frameTxMeta) validationGasLimit() uint64 {
	gasLimit := uint64(0)
	for _, f := range f.validationFrames() {
		gasLimit += f.GasLimit
	}
	return gasLimit
}
