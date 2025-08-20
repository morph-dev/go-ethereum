// Copyright 2021 The go-ethereum Authors
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

package misc

import (
	"fmt"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// VerifyGaslimit verifies the header gas limit according increase/decrease
// in relation to the parent gas limit.
func VerifyGaslimit(config *params.ChainConfig, parent, header *types.Header) error {
	low, high := gasLimitRange(config, parent, header)

	if header.GasLimit < low || header.GasLimit > high {
		return fmt.Errorf(
			"invalid gas limit: parent %d -> want [%d, %d], have %d",
			parent.GasLimit,
			low,
			high,
			header.GasLimit,
		)
	}

	return nil
}

// CalcGasLimit computes the gas limit of the next block after parent. It aims
// to keep the baseline gas close to the provided target, but within valid bounds.
func CalcGasLimit(config *params.ChainConfig, parent, header *types.Header, desiredGasLimit uint64) uint64 {
	low, high := gasLimitRange(config, parent, header)

	if desiredGasLimit < low {
		return low
	}
	if desiredGasLimit > high {
		return high
	}
	return desiredGasLimit
}

// gasLimitRange returns the inclusive range of valid values for GasLimit. It takes into
// consideration all forks and MinGasLimit.
func gasLimitRange(config *params.ChainConfig, parent, header *types.Header) (uint64, uint64) {
	parentGasLimit := parent.GasLimit

	// Handle the London fork transition
	if !config.IsLondon(parent.Number) && config.IsLondon(header.Number) {
		parentGasLimit *= config.ElasticityMultiplier()
	}

	// Handle the EIP-7782 fork transition
	if !config.IsEIP7782(parent.Number, parent.Time) && config.IsEIP7782(header.Number, header.Time) {
		parentGasLimit /= 2
	}

	// Get correct GasLimitBoundDivisor and MinGasLimit
	gasLimitBoundDivisor := params.GasLimitBoundDivisor
	minGasLimit := params.MinGasLimit
	if config.IsEIP7782(header.Number, header.Time) {
		gasLimitBoundDivisor = params.GasLimitBoundDivisorEIP7782
		minGasLimit = params.MinGasLimitEIP7782
	}

	maxDiff := parentGasLimit/gasLimitBoundDivisor - 1

	low := max(parentGasLimit-maxDiff, minGasLimit)
	high := parentGasLimit + maxDiff

	return low, high
}
