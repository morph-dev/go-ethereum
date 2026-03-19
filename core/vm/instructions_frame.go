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

package vm

import (
	"errors"
	"math"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

var (
	errInvalidFrameOpcode = errors.New("invalid frame opcode")
	errInvalidTxParam     = errors.New("invalid tx parameter")
)

func frameExecutionMode(mode uint64) uint64 {
	return mode & types.FrameTxModeMask
}

func approveScopeAllowed(mode uint64, scope uint64) bool {
	switch (mode >> 8) & 0x3 {
	case 0:
		return false
	case 1:
		return scope == 0
	case 2:
		return scope == 1
	default:
		return scope <= 2
	}
}

func opApprove(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	fc := evm.TxContext.FrameContext
	if fc == nil {
		return nil, errInvalidFrameOpcode
	}
	offset := scope.Stack.pop()
	length := scope.Stack.pop()
	scopeArg := scope.Stack.pop()

	scopeValue, overflow := scopeArg.Uint64WithOverflow()
	if overflow || scopeValue > 2 {
		return nil, errInvalidFrameOpcode
	}
	if fc.CurrentFrame < 0 || fc.CurrentFrame >= len(fc.Frames) {
		return nil, errInvalidFrameOpcode
	}
	frame := fc.Frames[fc.CurrentFrame]
	if !approveScopeAllowed(frame.Mode, scopeValue) {
		return nil, errInvalidFrameOpcode
	}
	if scope.Contract.Address() != fc.CurrentTarget {
		return nil, ErrExecutionReverted
	}

	ret := scope.Memory.GetCopy(offset.Uint64(), length.Uint64())
	senderNonce := evm.StateDB.GetNonce(fc.Sender)
	collectPayment := func() error {
		if senderNonce+1 < senderNonce {
			return ErrExecutionReverted
		}
		if fc.UpfrontCost == nil {
			return ErrExecutionReverted
		}
		if evm.StateDB.GetBalance(fc.CurrentTarget).Cmp(fc.UpfrontCost) < 0 {
			return ErrExecutionReverted
		}
		evm.StateDB.SetNonce(fc.Sender, senderNonce+1, tracing.NonceChangeEoACall)
		evm.StateDB.SubBalance(fc.CurrentTarget, fc.UpfrontCost, tracing.BalanceDecreaseGasBuy)
		fc.PayerApproved = true
		fc.Payer = fc.CurrentTarget
		return nil
	}

	switch scopeValue {
	case 0:
		if fc.SenderApproved || fc.CurrentTarget != fc.Sender {
			return nil, ErrExecutionReverted
		}
		fc.SenderApproved = true
	case 1:
		if fc.PayerApproved || !fc.SenderApproved {
			return nil, ErrExecutionReverted
		}
		if err := collectPayment(); err != nil {
			return nil, err
		}
	case 2:
		if fc.SenderApproved || fc.PayerApproved || fc.CurrentTarget != fc.Sender {
			return nil, ErrExecutionReverted
		}
		fc.SenderApproved = true
		if err := collectPayment(); err != nil {
			return nil, err
		}
	default:
		return nil, errInvalidFrameOpcode
	}
	fc.CurrentFrameApproved = true
	evm.frameCallStatus = 1
	return ret, errStopToken
}

func opTxParam(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	fc := evm.TxContext.FrameContext
	if fc == nil {
		return nil, errInvalidTxParam
	}
	param := scope.Stack.pop()
	in2 := scope.Stack.pop()

	selector, overflow := param.Uint64WithOverflow()
	if overflow {
		return nil, errInvalidTxParam
	}
	index, overflow := in2.Uint64WithOverflow()
	if overflow {
		return nil, errInvalidTxParam
	}
	word, err := frameTxParamWord(evm, fc, selector, index)
	if err != nil {
		return nil, err
	}
	scope.Stack.push(new(uint256.Int).SetBytes(word))
	return nil, nil
}

func opFrameDataLoad(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	fc := evm.TxContext.FrameContext
	if fc == nil {
		return nil, errInvalidTxParam
	}
	offset := scope.Stack.pop()
	frameIndex := scope.Stack.pop()

	idx, overflow := frameIndex.Uint64WithOverflow()
	if overflow {
		return nil, errInvalidTxParam
	}
	frame, err := frameByIndex(fc, idx)
	if err != nil {
		return nil, err
	}
	if frameExecutionMode(frame.Mode) == types.FrameTxModeVerify {
		scope.Stack.push(new(uint256.Int))
		return nil, nil
	}
	off, overflow := offset.Uint64WithOverflow()
	if overflow {
		off = math.MaxUint64
	}
	scope.Stack.push(new(uint256.Int).SetBytes(getData(frame.Data, off, 32)))
	return nil, nil
}

func opFrameDataCopy(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	fc := evm.TxContext.FrameContext
	if fc == nil {
		return nil, errInvalidTxParam
	}
	memOffset := scope.Stack.pop()
	dataOffset := scope.Stack.pop()
	length := scope.Stack.pop()
	frameIndex := scope.Stack.pop()

	idx, overflow := frameIndex.Uint64WithOverflow()
	if overflow {
		return nil, errInvalidTxParam
	}
	frame, err := frameByIndex(fc, idx)
	if err != nil {
		return nil, err
	}
	off, overflow := dataOffset.Uint64WithOverflow()
	if overflow {
		off = math.MaxUint64
	}
	if frameExecutionMode(frame.Mode) == types.FrameTxModeVerify {
		scope.Memory.Set(memOffset.Uint64(), length.Uint64(), make([]byte, length.Uint64()))
		return nil, nil
	}
	scope.Memory.Set(memOffset.Uint64(), length.Uint64(), getData(frame.Data, off, length.Uint64()))
	return nil, nil
}

func frameByIndex(fc *FrameContext, i uint64) (*types.FrameTxFrame, error) {
	if i >= uint64(len(fc.Frames)) {
		return nil, errInvalidTxParam
	}
	return &fc.Frames[i], nil
}

func frameTxParamWord(evm *EVM, fc *FrameContext, selector uint64, index uint64) ([]byte, error) {
	asUint := func(v uint64) []byte {
		out := make([]byte, 32)
		new(uint256.Int).SetUint64(v).WriteToSlice(out)
		return out
	}
	asAddress := func(addr common.Address) []byte {
		out := make([]byte, 32)
		copy(out[12:], addr[:])
		return out
	}
	asBig := func(v *big.Int) ([]byte, error) {
		if v == nil {
			return make([]byte, 32), nil
		}
		u, overflow := uint256.FromBig(v)
		if overflow {
			return nil, errInvalidTxParam
		}
		out := make([]byte, 32)
		u.WriteToSlice(out)
		return out, nil
	}

	switch selector {
	case 0x00:
		if index != 0 {
			return nil, errInvalidTxParam
		}
		return asUint(types.FrameTxType), nil
	case 0x01:
		if index != 0 {
			return nil, errInvalidTxParam
		}
		return asUint(fc.Nonce), nil
	case 0x02:
		if index != 0 {
			return nil, errInvalidTxParam
		}
		return asAddress(fc.Sender), nil
	case 0x03:
		if index != 0 {
			return nil, errInvalidTxParam
		}
		return asBig(fc.MaxPriorityFeePerGas)
	case 0x04:
		if index != 0 {
			return nil, errInvalidTxParam
		}
		return asBig(fc.MaxFeePerGas)
	case 0x05:
		if index != 0 {
			return nil, errInvalidTxParam
		}
		return asBig(fc.MaxFeePerBlobGas)
	case 0x06:
		if index != 0 {
			return nil, errInvalidTxParam
		}
		return asBig(fc.MaxCost)
	case 0x07:
		if index != 0 {
			return nil, errInvalidTxParam
		}
		return asUint(uint64(len(evm.TxContext.BlobHashes))), nil
	case 0x08:
		if index != 0 {
			return nil, errInvalidTxParam
		}
		out := make([]byte, 32)
		copy(out, fc.SigHash[:])
		return out, nil
	case 0x09:
		if index != 0 {
			return nil, errInvalidTxParam
		}
		return asUint(uint64(len(fc.Frames))), nil
	case 0x10:
		if index != 0 {
			return nil, errInvalidTxParam
		}
		return asUint(uint64(fc.CurrentFrame)), nil
	case 0x11:
		frame, err := frameByIndex(fc, index)
		if err != nil {
			return nil, err
		}
		if frame.Target == nil {
			return make([]byte, 32), nil
		}
		return asAddress(*frame.Target), nil
	case 0x12:
		frame, err := frameByIndex(fc, index)
		if err != nil {
			return nil, err
		}
		return asUint(frame.GasLimit), nil
	case 0x13:
		frame, err := frameByIndex(fc, index)
		if err != nil {
			return nil, err
		}
		return asUint(frame.Mode), nil
	case 0x14:
		frame, err := frameByIndex(fc, index)
		if err != nil {
			return nil, err
		}
		if frameExecutionMode(frame.Mode) == types.FrameTxModeVerify {
			return make([]byte, 32), nil
		}
		return asUint(uint64(len(frame.Data))), nil
	case 0x15:
		if index >= uint64(fc.CurrentFrame) || index >= uint64(len(fc.FrameStatuses)) {
			return nil, errInvalidTxParam
		}
		return asUint(fc.FrameStatuses[index]), nil
	default:
		return nil, errInvalidTxParam
	}
}
