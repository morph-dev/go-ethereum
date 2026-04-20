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
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type validationPrefix uint8

const (
	validationPrefixSelf validationPrefix = iota
	validationPrefixVerifyPay
	validationPrefixDeploySelf
	validationPrefixDeployVerifyPay
)

func (vp validationPrefix) VerifyFrameIndex() uint {
	switch vp {
	case validationPrefixSelf, validationPrefixVerifyPay:
		return 0
	case validationPrefixDeploySelf, validationPrefixDeployVerifyPay:
		return 1
	}
	panic("unknown validation prefix")
}

func (vp validationPrefix) PayerFrameIndex() uint {
	switch vp {
	case validationPrefixSelf:
		return 0
	case validationPrefixVerifyPay, validationPrefixDeploySelf:
		return 1
	case validationPrefixDeployVerifyPay:
		return 2
	}
	panic("unknown validation prefix")
}

func (vp validationPrefix) Length() uint {
	switch vp {
	case validationPrefixSelf:
		return 1
	case validationPrefixVerifyPay, validationPrefixDeploySelf:
		return 2
	case validationPrefixDeployVerifyPay:
		return 3
	}
	panic("unknown validation prefix")
}

func getValidationPrefix(sender *common.Address, frames []types.FrameTxFrame) (validationPrefix, bool) {
	if len(frames) == 0 {
		return validationPrefixSelf, false
	}

	hasDeployFrame := false
	verifyFrameIndex := 0

	// Check if first framet is "deploy" frame
	if frames[0].Mode() == types.FrameTxModeDefault {
		hasDeployFrame = true
		verifyFrameIndex = 1
	}

	if len(frames) < verifyFrameIndex+1 {
		return validationPrefixSelf, false
	}

	// Check that next frame is VERIFY and target is sender
	verifyFrame := &frames[verifyFrameIndex]
	if verifyFrame.Mode() != types.FrameTxModeVerify {
		return validationPrefixSelf, false
	}
	if verifyFrame.Target != nil && *verifyFrame.Target != *sender {
		return validationPrefixSelf, false
	}

	// Check if it is "self-verify"
	if verifyFrame.Scope() == types.FrameTxScopeBoth {
		if hasDeployFrame {
			return validationPrefixDeploySelf, true
		} else {
			return validationPrefixSelf, true
		}
	}

	// Check that it approves sender
	if verifyFrame.Scope() != types.FrameTxScopeSender {
		return validationPrefixSelf, false
	}

	// Check that it has one more frame
	if len(frames) < verifyFrameIndex+2 {
		return validationPrefixSelf, false
	}

	// Check that next frame is payer
	payerFrame := &frames[verifyFrameIndex+1]
	if payerFrame.Mode() == types.FrameTxModeVerify && payerFrame.Scope() == types.FrameTxScopePayer {
		if hasDeployFrame {
			return validationPrefixDeployVerifyPay, true
		} else {
			return validationPrefixVerifyPay, true
		}
	}

	return validationPrefixSelf, false
}
