package types

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types/bal"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

type ChunkHeader struct {
	Index               uint16      `json:"index"`
	ChunkAccessListHash common.Hash `json:"calHash"`
	PreChunkTxCount     uint64      `json:"preChunkTxCount"`
	PreChunkGasUsed     uint64      `json:"preChunkGasUsed"`
	PreChunkBlobGasUsed uint64      `json:"preChunkBlobGasUsed"`
	TxsRoot             common.Hash `json:"txsRoot"`
	GasUsed             uint64      `json:"gasUsed"`
	BlobGasUsed         uint64      `json:"blobGasUsed"`
	WithdrawalsRoot     common.Hash `json:"withdrawalsRoot"`
}

type ChunkBody struct {
	ChunkHeader  ChunkHeader
	Transactions Transactions
	Withdrawals  Withdrawals
}

type Chunk struct {
	ChunkHeader  ChunkHeader
	Transactions Transactions
	Withdrawals  Withdrawals
	Cal          *bal.BlockAccessList
}

type Chunks = []*Chunk

// Creates chunks and chunk access lists.
func CreateChunks(
	transactions Transactions,
	receipts Receipts,
	withdrawals Withdrawals,
	accessListBuilder bal.AccessListBuilder,
	hasher ListHasher,
) Chunks {
	chunksTxCount := splitIntoChunks(receipts)
	chunks := make(Chunks, 0, len(chunksTxCount))

	preChunkTxCount := 0
	preChunkGasUsed := uint64(0)
	preChunkBlobGasUsed := uint64(0)

	for chunkIndex, chunkTxCount := range chunksTxCount {
		var (
			firstTxIdx        = preChunkTxCount
			lastTxIdx         = preChunkTxCount + chunkTxCount - 1
			chunkTransactions = transactions[firstTxIdx : lastTxIdx+1]
			chunkReceipts     = receipts[firstTxIdx : lastTxIdx+1]

			isLast                       = lastTxIdx+1 == len(transactions)
			chunkWithdrawals Withdrawals = nil
		)
		if isLast {
			chunkWithdrawals = withdrawals
		}

		gasUsed, blobGasUsed := uint64(0), uint64(0)
		for _, receipt := range chunkReceipts {
			gasUsed += receipt.GasUsed
			blobGasUsed += receipt.BlobGasUsed
		}

		// The CAL indices are offset by one. Also
		// - index 0 is used for pre-execution (should be included in the first chunk)
		// - index "txCount+2" is used for post-execution (should be included in the last chunk)
		firstCalIdx := firstTxIdx + 1
		lastCalIdx := lastTxIdx + 1
		if chunkIndex == 0 {
			firstCalIdx = 0
		}
		if isLast {
			lastCalIdx += 1
		}
		cal := accessListBuilder.ToChunkAccessList(uint16(firstCalIdx), uint16(lastCalIdx))

		chunk := &Chunk{
			ChunkHeader: ChunkHeader{
				Index:               uint16(chunkIndex),
				ChunkAccessListHash: cal.Hash(),
				PreChunkTxCount:     uint64(preChunkTxCount),
				PreChunkGasUsed:     preChunkGasUsed,
				PreChunkBlobGasUsed: preChunkBlobGasUsed,
				TxsRoot:             DeriveSha(chunkTransactions, hasher),
				GasUsed:             gasUsed,
				BlobGasUsed:         blobGasUsed,
				WithdrawalsRoot:     DeriveSha(chunkWithdrawals, hasher),
			},
			Transactions: chunkTransactions,
			Withdrawals:  chunkWithdrawals,
			Cal:          cal,
		}
		chunks = append(chunks, chunk)

		preChunkTxCount += chunkTxCount
		preChunkGasUsed += gasUsed
		preChunkBlobGasUsed += blobGasUsed
	}

	// TODO(EIP-8101): Remove this - it's not needed
	// Checked that splitting BAL and merging CALs works
	cals := make([]bal.BlockAccessList, 0, len(chunks))
	for _, chunk := range chunks {
		cals = append(cals, *chunk.Cal)
	}
	mergedBal := bal.MergeCals(cals...)
	bal := *accessListBuilder.ToEncodingObj()
	if mergedBal.Hash() != bal.Hash() {
		log.Warn("EIP-8101: Merged BAL doesn't match header.BAL", "mergedBalHash", mergedBal.Hash(), "balHash", bal.Hash())
	}

	return chunks
}

// Splits block (represented by receipts) into chunks.
//
// Returns the number of transactions in each chunk.
func splitIntoChunks(receipts Receipts) []int {
	chunksTxCount := make([]int, 0, 10)

	firstTxIndex := 0
	gasUsed := uint64(0)

	for i, receipt := range receipts {
		if gasUsed+receipt.GasUsed <= params.ChunkMaxGas {
			gasUsed += receipt.GasUsed
		} else {
			chunksTxCount = append(chunksTxCount, i-firstTxIndex)
			firstTxIndex = i
			gasUsed = receipt.GasUsed
		}
	}
	chunksTxCount = append(chunksTxCount, len(receipts)-firstTxIndex)

	return chunksTxCount
}
