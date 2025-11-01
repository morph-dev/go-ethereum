package types

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

type ChunkBlockMetadata struct {
	ParentHash       common.Hash `json:"parentHash"`
	Number           uint64      `json:"number"`
	Timestamp        uint64      `json:"timestamp"`
	MixHash          common.Hash `json:"mixHash"`
	BaseFeePerGas    big.Int     `json:"baseFeePerGas"`
	ParentBeaconRoot common.Hash `json:"parentBeaconBlockRoot"`
}

type ChunkHeader struct {
	ChunkIndex          uint16      `json:"chunkIndex"`
	PreTxCount          uint64      `json:"preTxCount"`
	PreGasUsed          uint64      `json:"preGasUsed"`
	PreBlobGasUsed      uint64      `json:"preBlobGasUsed"`
	TxCount             uint64      `json:"txCount"`
	GasUsed             uint64      `json:"gasUsed"`
	BlobGasUsed         uint64      `json:"blobGasUsed"`
	TxsRoot             common.Hash `json:"transactionsRoot"`
	WithdrawalsRoot     common.Hash `json:"withdrawalsRoot"`
	ChunkAccessListHash common.Hash `json:"calHash"`
	IsLast              bool        `json:"isLast"`

	// TODO: consider adding
	// ReceiptsRoot        common.Hash `json:"receiptsRoot"`
	// Bloom               Bloom       `json:"logsBloom"`
	// PostStateRoot       common.Hash `json:"postStateRoot"`
	// ParentBeaconRoot    common.Hash `json:"parentBeaconBlockRoot"`
}

type ChunkTransactions struct {
	*ChunkMetadata
	Transactions Transactions
}

type ChunkMetadata struct {
	FirstTxIndex uint64
	TxCount      uint64
	GasUsed      uint64
	BlobGasUsed  uint64
	IsLast       bool
	IsPreBuilt   bool
}

func (c *ChunkMetadata) String() string {
	return fmt.Sprintf(
		"Chunk { firstTx=%d txCount=%d gas=%d blobGas=%d last=%t }",
		c.FirstTxIndex, c.TxCount, c.GasUsed, c.BlobGasUsed, c.IsLast,
	)
}

func SplitIntoChunks(
	receipts Receipts,
	preBuiltChunks []*ChunkTransactions,
) ([]*ChunkMetadata, error) {
	chunks := make([]*ChunkMetadata, 0, params.MaxChunksPerBlock)

	var firstTxIndex uint64 = 0
	var gasUsed uint64 = 0
	var blobGasUsed uint64 = 0

	// Iteratate over preBuiltChunks and verify that they are valid
	for chunkIndex, chunk := range preBuiltChunks {
		if int(chunk.TxCount) != len(chunk.Transactions) {
			err := fmt.Errorf(
				"chunk %d has unexpected number of transactions. expected %d, actual %d",
				chunkIndex, chunk.TxCount, len(chunk.Transactions),
			)
			return nil, err
		}
		if int(firstTxIndex+chunk.TxCount) > len(receipts) {
			err := fmt.Errorf(
				"expected at least %d receipts, but have only %d",
				firstTxIndex+chunk.TxCount, len(receipts),
			)
			return nil, err
		}
		if chunk.IsLast && int(firstTxIndex+chunk.TxCount) < len(receipts) {
			return nil, fmt.Errorf("remaining receitps after pre-built last chunk")
		}
		for txIndex, tx := range chunk.Transactions {
			receiptIndex := int(firstTxIndex) + txIndex
			receipt := receipts[receiptIndex]
			if tx.Hash() != receipt.TxHash {
				err := fmt.Errorf(
					"transaction %d from chunk %d doesn't match receipt %d. tx hash doesn't match: %v != %v",
					txIndex, chunkIndex, receiptIndex, tx.Hash(), receipt.TxHash,
				)
				return nil, err
			}
			gasUsed += receipt.GasUsed
			blobGasUsed += receipt.BlobGasUsed
		}

		if chunk.GasUsed != gasUsed {
			err := fmt.Errorf(
				"chunk %d hasis unexpected amount of GasUsed. expected %d actual %d",
				chunkIndex, chunk.GasUsed, gasUsed,
			)
			return nil, err
		}

		if chunk.BlobGasUsed != blobGasUsed {
			err := fmt.Errorf(
				"chunk %d has unexpected amount of BlobGasUsed. expected %d actual %d",
				chunkIndex, chunk.BlobGasUsed, blobGasUsed,
			)
			return nil, err
		}

		chunks = append(chunks, &ChunkMetadata{
			FirstTxIndex: firstTxIndex,
			TxCount:      chunk.TxCount,
			GasUsed:      gasUsed,
			BlobGasUsed:  blobGasUsed,
			IsLast:       chunk.IsLast,
			IsPreBuilt:   true,
		})

		firstTxIndex += chunk.TxCount
		gasUsed = 0
		blobGasUsed = 0
	}

	// Build the rest of chunks
	for i := firstTxIndex; i < uint64(len(receipts)); i++ {
		receipt := receipts[i]

		// check whether we can include tx "i" into current chunk
		if gasUsed+receipt.GasUsed <= params.ChunkMaxGas {
			gasUsed += receipt.GasUsed
			blobGasUsed += receipt.BlobGasUsed
		} else {
			// create chunk without tx "i" and without withdrawals
			chunks = append(chunks, &ChunkMetadata{
				FirstTxIndex: firstTxIndex,
				TxCount:      uint64(i) - firstTxIndex,
				GasUsed:      gasUsed,
				BlobGasUsed:  blobGasUsed,
				IsLast:       false,
				IsPreBuilt:   false,
			})
			firstTxIndex = uint64(i)
			gasUsed = receipt.GasUsed
			blobGasUsed = receipt.BlobGasUsed
		}
	}
	// create last chunk (with withdrawals)
	chunks = append(chunks, &ChunkMetadata{
		FirstTxIndex: firstTxIndex,
		TxCount:      uint64(len(receipts)) - firstTxIndex,
		GasUsed:      gasUsed,
		BlobGasUsed:  blobGasUsed,
		IsLast:       true,
		IsPreBuilt:   false,
	})

	return chunks, nil
}

func (block *Block) CreateChunkHeaders(finalize bool, includePreBuilt bool, hasher ListHasher) []*ChunkHeader {
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

		include := true
		if chunk.IsLast && !finalize {
			include = false
		}
		if chunk.IsPreBuilt && !includePreBuilt {
			include = false
		}
		if include {
			chunkHeaders = append(chunkHeaders, &ChunkHeader{
				ChunkIndex:          uint16(i),
				PreTxCount:          chunk.FirstTxIndex,
				PreGasUsed:          cumulativeGasUsed,
				PreBlobGasUsed:      cumulativeBlobGasUsed,
				TxCount:             chunk.TxCount,
				GasUsed:             chunk.GasUsed,
				BlobGasUsed:         chunk.BlobGasUsed,
				TxsRoot:             DeriveSha(transactions, hasher),
				WithdrawalsRoot:     withdrawalsRoot,
				ChunkAccessListHash: *block.header.BlockAccessListHash,
				IsLast:              chunk.IsLast,
			})
		}
		cumulativeGasUsed += chunk.GasUsed
		cumulativeBlobGasUsed += chunk.BlobGasUsed
	}
	return chunkHeaders
}
