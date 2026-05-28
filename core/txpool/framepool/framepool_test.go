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
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/beacon"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"
)

var (
	testPoolConfig             Config = DefaultConfig
	testDefaultGenesisGasLimit uint64 = 10_000_000
)

func newTestBlockChain(config *params.ChainConfig, gasLimit uint64, keys []*ecdsa.PrivateKey) *core.BlockChain {
	bytecode := common.Hex2Bytes("60016003600960a7565b1614601057005b6003601860a7565b60081c1680156096575f190160018116609a575b604236036096575f355f1a6096576022356fa2a8918ca85bafe22016d0b997e4df60600160ff1b0381116096575f6008b05f52601b6001355f1a0160205260023560405260605260205f60808160015afa156096573d156096575f51300360965760949060b1565b005b5f80fd5b5f6002b03014602c575f80fd5b5f6010b06013b090565b5f80aa56")
	alloc := make(types.GenesisAlloc, len(keys))
	for _, key := range keys {
		alloc[crypto.PubkeyToAddress(key.PublicKey)] = types.Account{
			Nonce:   0,
			Code:    bytecode,
			Balance: big.NewInt(1000000000000000000), // 1 ether
		}
	}

	db := rawdb.NewMemoryDatabase()
	gspec := &core.Genesis{
		Config:   config,
		GasLimit: gasLimit,
		Alloc:    alloc,
	}
	engine := beacon.New(ethash.NewFaker())
	blockchain, _ := core.NewBlockChain(db, gspec, engine, nil)
	return blockchain
}

func setupFramePoolTest(t testing.TB, keys int) (*FramePool, []*ecdsa.PrivateKey) {
	privateKeys := make([]*ecdsa.PrivateKey, keys)
	for i := range keys {
		privateKeys[i], _ = crypto.GenerateKey()
	}

	blockchain := newTestBlockChain(params.AllDevChainProtocolChanges, testDefaultGenesisGasLimit, privateKeys)

	pool := New(testPoolConfig, blockchain)

	reserverTracker := txpool.NewReservationTracker()
	reserver := reserverTracker.NewHandle(0)

	err := pool.Init(0, blockchain.CurrentBlock(), reserver)
	require.NoError(t, err)

	return pool, privateKeys
}

func newTestTx(t testing.TB, pool *FramePool, key *ecdsa.PrivateKey) *types.FrameTx {
	bc := pool.chain

	frame := types.FrameTxFrame{
		Mode:     types.FrameTxModeVerify | types.FrameTxAllowExecution | types.FrameTxAllowPayment,
		Target:   nil,
		GasLimit: 50_000,
		Data:     nil,
	}
	tx := &types.FrameTx{
		ChainID:              uint256.MustFromBig(bc.Config().ChainID),
		Nonce:                0,
		Sender:               crypto.PubkeyToAddress(key.PublicKey),
		Frames:               []types.FrameTxFrame{frame},
		MaxPriorityFeePerGas: uint256.NewInt(1),
		MaxFeePerGas:         uint256.MustFromBig(bc.CurrentBlock().BaseFee),
		MaxFeePerBlobGas:     nil,
		BlobVersionedHashes:  nil,
	}
	tx, err := tx.SignFrame(0, key, pool.signer)
	require.NoError(t, err)
	return tx
}

func TestBatchInsert(t *testing.T) {
	n := 100
	framepool, keys := setupFramePoolTest(t, n)

	txs := make(types.Transactions, n)
	for i, key := range keys {
		txs[i] = types.NewTx(newTestTx(t, framepool, key))
	}

	errs := framepool.Add(txs, true /* =sync */)
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, n, len(framepool.index))
}

func benchmarkBatchInsert(b *testing.B, size int) {
	framepool, keys := setupFramePoolTest(b, b.N*size)

	batches := make([]types.Transactions, b.N)
	for batchIdx := range b.N {
		batch := make(types.Transactions, size)
		for txIdx := range batch {
			key := keys[batchIdx*size+txIdx]
			tx := newTestTx(b, framepool, key)
			batch[txIdx] = types.NewTx(tx)
		}
		batches[batchIdx] = batch
	}

	b.ResetTimer()
	for _, batch := range batches {
		framepool.Add(batch, true /* =sync */)
		// for _, err := range errs {
		// 	require.NoError(b, err)
		// }
	}
}

func BenchmarkBatchInsert10(b *testing.B) { benchmarkBatchInsert(b, 10) }

func BenchmarkBatchInsert100(b *testing.B) { benchmarkBatchInsert(b, 100) }

func BenchmarkBatchInsert1000(b *testing.B) { benchmarkBatchInsert(b, 1000) }

func BenchmarkBatchInsert10000(b *testing.B) { benchmarkBatchInsert(b, 10000) }
