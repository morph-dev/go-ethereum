package types

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types/bal"
	"github.com/ethereum/go-ethereum/params"
)

type Chunks []*Chunk

type Chunk struct {
	Header          ChunkHeader         `json:"header"`
	BlockMetadata   *BlockMetadata      `json:"blockMetadata"`
	Transactions    Transactions        `json:"transactions"`
	Withdrawals     Withdrawals         `json:"withdrawals"`
	ChunkAccessList bal.ChunkAccessList `json:"chunkAccessList"`
}

type BlockMetadata struct {
	ParentHash       common.Hash `json:"parentHash"`
	Number           uint64      `json:"number"`
	Timestamp        uint64      `json:"timestamp"`
	MixHash          common.Hash `json:"mixHash"`
	BaseFeePerGas    big.Int     `json:"baseFeePerGas"`
	ParentBeaconRoot common.Hash `json:"parentBeaconBlockRoot"`
}

type ChunkHeader struct {
	ChunkIndex          uint16      `json:"chunkIndex"`
	PreChunkTxCount     uint64      `json:"preChunkTxCount"`
	TxsRoot             common.Hash `json:"transactionsRoot"`
	ReceiptsRoot        common.Hash `json:"receiptsRoot"`
	Bloom               Bloom       `json:"logsBloom"`
	PreChunkGasUsed     uint64      `json:"preChunkGasUsed"`
	GasUsed             uint64      `json:"gasUsed"`
	PreChunkBlobGasUsed uint64      `json:"preChunkBlobGasUsed"`
	BlobGasUsed         uint64      `json:"blobGasUsed"`
	WithdrawalsRoot     common.Hash `json:"withdrawalsRoot"`
	ChunkAccessListHash common.Hash `json:"calHash"`
	IsLast              bool        `json:"isLast"`

	// TODO: consider adding
	// PostStateRoot    common.Hash `json:"postStateRoot"`
	// ParentBeaconRoot common.Hash `json:"parentBeaconBlockRoot"`
}

func CreateChunks(
	blockMetadata *BlockMetadata,
	transactions Transactions,
	receipts Receipts,
	withdrawals Withdrawals,
	blockAccessList bal.BlockAccessList,
	hasher TrieHasher,
) []*Chunk {
	// Split into chunks
	chunksMetadata := split(transactions, receipts, withdrawals, blockAccessList)

	chunks := make([]*Chunk, 0, len(chunksMetadata))
	var (
		preChunkGasUsed     uint64
		preChunkBlobGasUsed uint64
	)

	for i, chunk := range chunksMetadata {
		gasUsed := uint64(0)
		blobGasUsed := uint64(0)
		for _, receipt := range chunk.receipts {
			gasUsed += receipt.GasUsed
			blobGasUsed += receipt.BlobGasUsed
		}

		chunks = append(chunks, &Chunk{
			Header: ChunkHeader{
				ChunkIndex:          uint16(i),
				PreChunkTxCount:     chunk.firstTxIndex,
				TxsRoot:             DeriveSha(chunk.transactions, hasher),
				ReceiptsRoot:        DeriveSha(chunk.receipts, hasher),
				Bloom:               MergeBloom(chunk.receipts),
				PreChunkGasUsed:     preChunkGasUsed,
				GasUsed:             gasUsed,
				PreChunkBlobGasUsed: preChunkBlobGasUsed,
				BlobGasUsed:         blobGasUsed,
				WithdrawalsRoot:     DeriveSha(chunk.withdrawals, hasher),
				ChunkAccessListHash: chunk.chunkAccessList.Hash(),
				IsLast:              chunk.isLast,
			},
			BlockMetadata:   blockMetadata,
			Transactions:    chunk.transactions,
			Withdrawals:     chunk.withdrawals,
			ChunkAccessList: bal.ChunkAccessList(chunk.chunkAccessList),
		})
		preChunkGasUsed += gasUsed
		preChunkBlobGasUsed += blobGasUsed
	}

	return chunks
}

type chunkMetadata struct {
	firstTxIndex    uint64
	transactions    Transactions
	receipts        Receipts
	withdrawals     Withdrawals
	chunkAccessList bal.BlockAccessList
	isLast          bool
}

func split(
	transactions Transactions,
	receipts Receipts,
	withdrawals Withdrawals,
	blockAccessList bal.BlockAccessList,
) []chunkMetadata {
	chunks := make([]chunkMetadata, 0, params.MaxChunksPerBlock)

	var chunkFirstTxIndex uint64 = 0
	var chunkGasUsed uint64 = 0

	for i := range transactions {
		// check whether we can include tx "i" into current chunk
		if chunkGasUsed+receipts[i].GasUsed <= params.ChunkMaxGas {
			chunkGasUsed += receipts[i].GasUsed
		} else {
			// create chunk without tx "i" and without withdrawals
			chunks = append(chunks, chunkMetadata{
				chunkFirstTxIndex,
				transactions[chunkFirstTxIndex:i],
				receipts[chunkFirstTxIndex:i],
				withdrawals[:0],
				blockAccessList.Copy(), // TODO(milos): split chunks
				/* isLast= */ false,
			})
			chunkFirstTxIndex = uint64(i)
			chunkGasUsed = receipts[i].GasUsed
		}
	}
	// create last chunk (with withdrawals)
	chunks = append(chunks, chunkMetadata{
		chunkFirstTxIndex,
		transactions[chunkFirstTxIndex:],
		receipts[chunkFirstTxIndex:],
		withdrawals,
		blockAccessList.Copy(), // TODO(milos): split chunks
		/* isLast= */ true,
	})

	return chunks
}
