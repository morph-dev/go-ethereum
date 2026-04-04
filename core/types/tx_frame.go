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

package types

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

var (
	ErrFrameTxInvalidFormat     = errors.New("invalid frame tx format")
	ErrFrameTxInvalidBlobFields = errors.New("invalid frame tx blob fields")
)

type FrameTxMode = uint8
type FrameTxScope = uint8

const (
	FrameTxModeDefault uint8 = 0
	FrameTxModeVerify  uint8 = 1
	FrameTxModeSender  uint8 = 2

	FrameTxScopeNone   uint8 = 0
	FrameTxScopeSender uint8 = 1
	FrameTxScopePayer  uint8 = 2
	FrameTxScopeBoth   uint8 = 3
)

// FrameTxFrame is a single frame in a frame transaction.
type FrameTxFrame struct {
	ScopeMode uint16
	Target    *common.Address `rlp:"nil"` // nil means sender
	GasLimit  uint64
	Data      []byte
}

type frameTxFrameJSON struct {
	Mode     hexutil.Uint64  `json:"mode"`
	Target   *common.Address `json:"target"`
	GasLimit hexutil.Uint64  `json:"gasLimit"`
	Data     hexutil.Bytes   `json:"data"`
}

func (f FrameTxFrame) MarshalJSON() ([]byte, error) {
	enc := frameTxFrameJSON{
		Mode:     hexutil.Uint64(f.ScopeMode),
		Target:   f.Target,
		GasLimit: hexutil.Uint64(f.GasLimit),
		Data:     f.Data,
	}
	return json.Marshal(&enc)
}

func (f *FrameTxFrame) UnmarshalJSON(input []byte) error {
	var dec frameTxFrameJSON
	if err := json.Unmarshal(input, &dec); err != nil {
		return err
	}
	f.ScopeMode = uint16(dec.Mode)
	f.Target = dec.Target
	f.GasLimit = uint64(dec.GasLimit)
	f.Data = dec.Data
	return nil
}

func (f *FrameTxFrame) Mode() FrameTxMode {
	return uint8(f.ScopeMode & 0xFF)
}

func (f *FrameTxFrame) Scope() FrameTxScope {
	return uint8(f.ScopeMode>>8) & FrameTxScopeBoth
}

// FrameTx represents an EIP-8141 frame transaction.
type FrameTx struct {
	ChainID *uint256.Int
	Nonce   uint64
	Sender  common.Address
	Frames  []FrameTxFrame

	MaxPriorityFeePerGas *uint256.Int
	MaxFeePerGas         *uint256.Int
	MaxFeePerBlobGas     *uint256.Int
	BlobVersionedHashes  []common.Hash
}

func (tx *FrameTx) copy() TxData {
	cpy := &FrameTx{
		Nonce:               tx.Nonce,
		Sender:              tx.Sender,
		Frames:              make([]FrameTxFrame, len(tx.Frames)),
		BlobVersionedHashes: make([]common.Hash, len(tx.BlobVersionedHashes)),

		ChainID:              new(uint256.Int),
		MaxPriorityFeePerGas: new(uint256.Int),
		MaxFeePerGas:         new(uint256.Int),
		MaxFeePerBlobGas:     new(uint256.Int),
	}
	for i, frame := range tx.Frames {
		var target *common.Address
		if frame.Target != nil {
			t := *frame.Target
			target = &t
		}
		cpy.Frames[i] = FrameTxFrame{
			ScopeMode: frame.ScopeMode,
			Target:    target,
			GasLimit:  frame.GasLimit,
			Data:      common.CopyBytes(frame.Data),
		}
	}
	copy(cpy.BlobVersionedHashes, tx.BlobVersionedHashes)
	if tx.ChainID != nil {
		cpy.ChainID.Set(tx.ChainID)
	}
	if tx.MaxPriorityFeePerGas != nil {
		cpy.MaxPriorityFeePerGas.Set(tx.MaxPriorityFeePerGas)
	}
	if tx.MaxFeePerGas != nil {
		cpy.MaxFeePerGas.Set(tx.MaxFeePerGas)
	}
	if tx.MaxFeePerBlobGas != nil {
		cpy.MaxFeePerBlobGas.Set(tx.MaxFeePerBlobGas)
	}
	return cpy
}

func (tx *FrameTx) txType() byte { return FrameTxType }
func (tx *FrameTx) chainID() *big.Int {
	if tx.ChainID == nil {
		return new(big.Int)
	}
	return tx.ChainID.ToBig()
}
func (tx *FrameTx) nonce() uint64 { return tx.Nonce }
func (tx *FrameTx) to() *common.Address {
	return nil
}
func (tx *FrameTx) value() *big.Int { return common.Big0 }
func (tx *FrameTx) data() []byte    { return nil }
func (tx *FrameTx) accessList() AccessList {
	return nil
}
func (tx *FrameTx) gasFeeCap() *big.Int {
	if tx.MaxFeePerGas == nil {
		return new(big.Int)
	}
	return tx.MaxFeePerGas.ToBig()
}
func (tx *FrameTx) gasTipCap() *big.Int {
	if tx.MaxPriorityFeePerGas == nil {
		return new(big.Int)
	}
	return tx.MaxPriorityFeePerGas.ToBig()
}
func (tx *FrameTx) gasPrice() *big.Int {
	if tx.MaxFeePerGas == nil {
		return new(big.Int)
	}
	return tx.MaxFeePerGas.ToBig()
}

func (tx *FrameTx) gas() uint64 {
	_, _, total, err := CalcFrameTxGas(tx.Frames)
	if err != nil {
		return 0
	}
	return total
}

func (tx *FrameTx) effectiveGasPrice(dst *big.Int, baseFee *big.Int) *big.Int {
	if baseFee == nil {
		return dst.Set(tx.gasFeeCap())
	}
	tip := dst.Sub(tx.gasFeeCap(), baseFee)
	if tip.Cmp(tx.gasTipCap()) > 0 {
		tip.Set(tx.gasTipCap())
	}
	return tip.Add(tip, baseFee)
}

func (tx *FrameTx) rawSignatureValues() (v, r, s *big.Int) {
	return nil, nil, nil
}

func (tx *FrameTx) setSignatureValues(chainID, v, r, s *big.Int) {}

func (tx *FrameTx) encode(b *bytes.Buffer) error {
	return rlp.Encode(b, tx)
}

func (tx *FrameTx) decode(input []byte) error {
	if err := rlp.DecodeBytes(input, tx); err != nil {
		return fmt.Errorf("%w: %v", ErrFrameTxInvalidFormat, err)
	}
	return tx.validate()
}

func (tx *FrameTx) validate() error {
	if tx.ChainID == nil || tx.MaxPriorityFeePerGas == nil || tx.MaxFeePerGas == nil || tx.MaxFeePerBlobGas == nil {
		return ErrFrameTxInvalidFormat
	}
	if len(tx.Frames) == 0 || len(tx.Frames) > params.FrameTxMaxFrames {
		return ErrFrameTxInvalidFormat
	}
	for _, frame := range tx.Frames {
		if frame.Mode() > FrameTxModeSender {
			return ErrFrameTxInvalidFormat
		}
		if frame.Scope() > FrameTxScopeBoth {
			return ErrFrameTxInvalidFormat
		}
	}
	if len(tx.BlobVersionedHashes) == 0 {
		if !tx.MaxFeePerBlobGas.IsZero() {
			return ErrFrameTxInvalidBlobFields
		}
	} else if tx.MaxFeePerBlobGas.IsZero() {
		return ErrFrameTxInvalidBlobFields
	}
	if _, _, _, err := CalcFrameTxGas(tx.Frames); err != nil {
		return fmt.Errorf("%w: %v", ErrFrameTxInvalidFormat, err)
	}
	return nil
}

func (tx *FrameTx) sigHash(chainID *big.Int) common.Hash {
	if chainID == nil {
		chainID = tx.chainID()
	}
	frames := make([]FrameTxFrame, len(tx.Frames))
	for i, frame := range tx.Frames {
		frames[i] = frame
		if frame.Mode() == FrameTxModeVerify {
			frames[i].Data = nil
		} else {
			frames[i].Data = common.CopyBytes(frame.Data)
		}
	}
	return prefixedRlpHash(
		FrameTxType,
		[]any{
			chainID,
			tx.Nonce,
			tx.Sender,
			frames,
			tx.MaxPriorityFeePerGas,
			tx.MaxFeePerGas,
			tx.MaxFeePerBlobGas,
			tx.BlobVersionedHashes,
		},
	)
}

// CalcFrameTxGas calculates frame tx gas components as specified by EIP-8141.
//
// The returned values are:
//   - intrinsic gas: FRAME_TX_INTRINSIC_COST + calldata_cost(rlp(frames))
//   - frame gas: sum(frame.gas_limit)
//   - total gas: intrinsic gas + frame gas
func CalcFrameTxGas(frames []FrameTxFrame) (uint64, uint64, uint64, error) {
	encodedFrames, err := rlp.EncodeToBytes(frames)
	if err != nil {
		return 0, 0, 0, err
	}
	var (
		intrinsic = params.FrameTxIntrinsicGas
		frameGas  uint64
	)
	for _, frame := range frames {
		if math.MaxUint64-frameGas < frame.GasLimit {
			return 0, 0, 0, coreIntrinsicGasOverflowError
		}
		frameGas += frame.GasLimit
	}
	z := uint64(bytes.Count(encodedFrames, []byte{0}))
	nz := uint64(len(encodedFrames)) - z
	if (math.MaxUint64-intrinsic)/params.TxDataZeroGas < z {
		return 0, 0, 0, coreIntrinsicGasOverflowError
	}
	intrinsic += z * params.TxDataZeroGas
	if (math.MaxUint64-intrinsic)/params.TxDataNonZeroGasEIP2028 < nz {
		return 0, 0, 0, coreIntrinsicGasOverflowError
	}
	intrinsic += nz * params.TxDataNonZeroGasEIP2028
	if math.MaxUint64-intrinsic < frameGas {
		return 0, 0, 0, coreIntrinsicGasOverflowError
	}
	return intrinsic, frameGas, intrinsic + frameGas, nil
}

var coreIntrinsicGasOverflowError = errors.New("gas uint64 overflow")
