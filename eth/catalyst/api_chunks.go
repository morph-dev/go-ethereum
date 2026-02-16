package catalyst

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/types/bal"
	"github.com/ethereum/go-ethereum/params/forks"
)

func (api *ConsensusAPI) NewBlockHeaderV1(
	data engine.ExecutableData,
	beaconRoot common.Hash,
	blobHashes []common.Hash,
	requests [][]byte,
) (engine.PayloadStatusV1, error) {
	if !api.checkFork(data.Timestamp, forks.Amsterdam) {
		return invalidStatus, unsupportedForkErr("newBlockHeaderV1 is not available before Amsterdam")
	}

	requestsHash := types.CalcRequestsHash(requests)

	header := types.Header{
		ParentHash:          data.ParentHash,
		UncleHash:           types.EmptyUncleHash,
		Coinbase:            data.FeeRecipient,
		Root:                data.StateRoot,
		TxHash:              *data.TxHash,
		ReceiptHash:         data.ReceiptsRoot,
		Bloom:               types.BytesToBloom(data.LogsBloom),
		Difficulty:          common.Big0,
		Number:              new(big.Int).SetUint64(data.Number),
		GasLimit:            data.GasLimit,
		GasUsed:             data.GasUsed,
		Time:                data.Timestamp,
		BaseFee:             data.BaseFeePerGas,
		Extra:               data.ExtraData,
		MixDigest:           data.Random,
		WithdrawalsHash:     data.WithdrawalsRoot,
		ExcessBlobGas:       data.ExcessBlobGas,
		BlobGasUsed:         data.BlobGasUsed,
		ParentBeaconRoot:    &beaconRoot,
		RequestsHash:        &requestsHash,
		BlockAccessListHash: data.BlockAccessListHash,
	}

	if data.BlockHash != header.Hash() {
		err := fmt.Errorf("blockhash mismatch, want %x, got %x", data.BlockHash, header.Hash())
		return api.invalid(err, nil), nil
	}

	if _, err := api.eth.BlockChain().NewChunkValidator(&header, blobHashes, data.ChunkCount); err != nil {
		if err == consensus.ErrUnknownAncestor || err == consensus.ErrPrunedAncestor {
			return engine.PayloadStatusV1{Status: engine.SYNCING}, nil
		}
		return invalidStatus, err
	}

	return engine.PayloadStatusV1{Status: engine.ACCEPTED}, nil
}

func (api *ConsensusAPI) NewChunkAccessListV1(blockHash common.Hash, chunkIndex uint16, cal bal.BlockAccessList) (engine.PayloadStatusV1, error) {
	chunkValidator := api.eth.BlockChain().GetChunkValidator(blockHash)
	if chunkValidator == nil {
		err := fmt.Errorf("unknown block hash: %x", blockHash)
		return api.invalid(err, nil), nil
	}

	if err := chunkValidator.AddCal(chunkIndex, cal); err != nil {
		return api.invalid(err, nil), nil
	}

	return engine.PayloadStatusV1{Status: engine.ACCEPTED}, nil
}

func (api *ConsensusAPI) ExecuteChunkV1(blockHash common.Hash, chunk engine.ExecutionChunk) (engine.PayloadStatusV1, error) {
	chunkValidator := api.eth.BlockChain().GetChunkValidator(blockHash)
	if chunkValidator == nil {
		err := fmt.Errorf("unknown block hash: %x", blockHash)
		return api.invalid(err, nil), nil
	}

	transactions, err := decodeTxs(chunk.Transactions)
	if err != nil {
		return api.invalid(err, nil), nil
	}
	chunkBody := types.ChunkBody{
		ChunkHeader:  chunk.ChunkHeader,
		Transactions: transactions,
		Withdrawals:  chunk.Withdrawals,
	}

	if err := chunkValidator.AddAndExecute(&chunkBody); err != nil {
		if err == core.ErrMissingChunk || err == core.ErrMissingCal {
			return engine.PayloadStatusV1{Status: engine.INSUFFICIENT_INFORMATION}, err
		}
		return api.invalid(err, nil), nil
	}

	return engine.PayloadStatusV1{Status: engine.VALID}, nil
}

func (api *ConsensusAPI) FinalizeBlockV1(blockHash common.Hash) (engine.PayloadStatusV1, error) {
	chunkValidator := api.eth.BlockChain().GetChunkValidator(blockHash)
	if chunkValidator == nil {
		err := fmt.Errorf("unknown block hash: %x", blockHash)
		return api.invalid(err, nil), nil
	}

	if err := chunkValidator.Finalize(); err != nil {
		if err == core.ErrMissingChunk || err == core.ErrMissingCal {
			return engine.PayloadStatusV1{Status: engine.INSUFFICIENT_INFORMATION}, err
		}
		return api.invalid(err, nil), nil
	}

	return engine.PayloadStatusV1{Status: engine.VALID}, nil
}

func decodeTxs(enc [][]byte) ([]*types.Transaction, error) {
	var txs = make([]*types.Transaction, len(enc))
	for i, encTx := range enc {
		var tx types.Transaction
		if err := tx.UnmarshalBinary(encTx); err != nil {
			return nil, fmt.Errorf("invalid transaction %d: %v", i, err)
		}
		txs[i] = &tx
	}
	return txs, nil
}
