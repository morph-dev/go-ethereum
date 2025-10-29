package types

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types/bal"
	"github.com/ethereum/go-ethereum/params"
)

type Chunk struct {
	Header          ChunkHeader         `json:"header"`
	BlockMetadata   *BlockMetadata      `json:"blockMetadata"`
	Transactions    Transactions        `json:"transactions"`
	Withdrawals     Withdrawals         `json:"withdrawals"`
	ChunkAccessList bal.BlockAccessList `json:"chunkAccessList"`
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
	TxCount             uint64      `json:"txCount"`
	TxsRoot             common.Hash `json:"transactionsRoot"`
	PreChunkGasUsed     uint64      `json:"preChunkGasUsed"`
	GasUsed             uint64      `json:"gasUsed"`
	PreChunkBlobGasUsed uint64      `json:"preChunkBlobGasUsed"`
	BlobGasUsed         uint64      `json:"blobGasUsed"`
	WithdrawalsRoot     common.Hash `json:"withdrawalsRoot"`
	ChunkAccessListHash common.Hash `json:"calHash"`
	IsLast              bool        `json:"isLast"`

	// TODO: consider adding
	// ReceiptsRoot        common.Hash `json:"receiptsRoot"`
	// Bloom               Bloom       `json:"logsBloom"`
	// PostStateRoot       common.Hash `json:"postStateRoot"`
	// ParentBeaconRoot    common.Hash `json:"parentBeaconBlockRoot"`
}

type ChunkMetadata struct {
	FirstTxIndex uint64
	TxCount      uint64
	GasUsed      uint64
	BlobGasUsed  uint64
	IsLast       bool
}

func (c *ChunkMetadata) String() string {
	return fmt.Sprintf(
		"Chunk { firstTx=%d txCount=%d gas=%d blobGas=%d last=%t }",
		c.FirstTxIndex, c.TxCount, c.GasUsed, c.BlobGasUsed, c.IsLast,
	)
}

func SplitIntoChunks(
	receipts Receipts,
) []*ChunkMetadata {
	chunks := make([]*ChunkMetadata, 0, params.MaxChunksPerBlock)

	var chunkFirstTxIndex uint64 = 0
	var chunkGasUsed uint64 = 0
	var chunkBlobGasUsed uint64 = 0

	for i := range receipts {
		// check whether we can include tx "i" into current chunk
		if chunkGasUsed+receipts[i].GasUsed <= params.ChunkMaxGas {
			chunkGasUsed += receipts[i].GasUsed
			chunkBlobGasUsed += receipts[i].BlobGasUsed
		} else {
			// create chunk without tx "i" and without withdrawals
			chunks = append(chunks, &ChunkMetadata{
				chunkFirstTxIndex,
				uint64(i) - chunkFirstTxIndex,
				chunkGasUsed,
				chunkBlobGasUsed,
				/* IsLast= */ false,
			})
			chunkFirstTxIndex = uint64(i)
			chunkGasUsed = receipts[i].GasUsed
			chunkBlobGasUsed = receipts[i].BlobGasUsed
		}
	}
	// create last chunk (with withdrawals)
	chunks = append(chunks, &ChunkMetadata{
		chunkFirstTxIndex,
		uint64(len(receipts)) - chunkFirstTxIndex,
		chunkGasUsed,
		chunkBlobGasUsed,
		/* IsLast= */ true,
	})

	return chunks
}

func (block *Block) CreateChunkHeaders(hasher ListHasher) []*ChunkHeader {
	chunkHeaders := make([]*ChunkHeader, 0, len(block.chunks))

	cumulativeGasUsed := uint64(0)
	cumulativeBlobGasUsed := uint64(0)

	for i, chunk := range block.chunks {
		lastTxIndex := (chunk.FirstTxIndex + chunk.TxCount)
		transactions := block.transactions[chunk.FirstTxIndex:lastTxIndex]

		withdrawalsRoot := EmptyWithdrawalsHash
		if chunk.IsLast {
			withdrawalsRoot = *block.header.WithdrawalsHash
		}
		chunkHeaders = append(chunkHeaders, &ChunkHeader{
			ChunkIndex:          uint16(i),
			PreChunkTxCount:     chunk.FirstTxIndex,
			TxCount:             chunk.TxCount,
			TxsRoot:             DeriveSha(transactions, hasher),
			PreChunkGasUsed:     cumulativeGasUsed,
			GasUsed:             chunk.GasUsed,
			PreChunkBlobGasUsed: cumulativeBlobGasUsed,
			BlobGasUsed:         chunk.BlobGasUsed,
			WithdrawalsRoot:     withdrawalsRoot,
			ChunkAccessListHash: *block.header.BlockAccessListHash,
			IsLast:              chunk.IsLast,
		})
		cumulativeGasUsed += chunk.GasUsed
		cumulativeBlobGasUsed += chunk.BlobGasUsed
	}
	return chunkHeaders
}
