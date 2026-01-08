package core

import (
	"cmp"
	"fmt"
	"runtime"
	"slices"
	"time"

	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/types/bal"
	"github.com/ethereum/go-ethereum/core/vm"
	"golang.org/x/sync/errgroup"
)

type ProcessChunkResult struct {
	Index    uint16
	Receipts types.Receipts
	Logs     []*types.Log
	Requests [][]byte
}

type ProcessChunkResultWithMetrics struct {
	Result           *ProcessChunkResult
	ProcessChunkTime time.Duration
}

func (p *ParallelStateProcessor) ProcessChunk(
	header *types.Header,
	chunk *types.ChunkBody,
	cal bal.BlockAccessList,
	statedb *state.StateDB,
) (*ProcessChunkResultWithMetrics, error) {
	var (
		preTxExecResult  *chunkPreTxExecResult
		txExecRes        *chunkTxExecResult
		postTxExecResult *chunkPostTxExecResult
		err              error

		pStart = time.Now()
		signer = types.MakeSigner(p.chainConfig(), header.Number, header.Time)

		allStateReads = make(bal.StateAccesses)
	)

	// ======================================
	// 1. Pre-TX execution (only first chunk)
	// ======================================
	if chunk.ChunkHeader.Index == 0 {
		preTxExecResult, err = p.chunkPreTxExec(header, statedb.Copy())
		if err != nil {
			return nil, err
		}
		allStateReads.Merge(preTxExecResult.stateReads)
	}

	// ======================================
	// 2. Tx execution
	// ======================================
	txExecRes, err = p.chunkTxExec(header, chunk, statedb.Copy(), signer)
	if err != nil {
		return nil, err
	}
	allStateReads.Merge(txExecRes.stateReads)

	// ======================================
	// 3. Post-Tx execution (only last chunk)
	// ======================================
	isLast := header.GasUsed == (chunk.ChunkHeader.PreChunkGasUsed + chunk.ChunkHeader.GasUsed)
	if isLast {
		postTxExecResult, err = p.chunkPostTxExec(header, chunk, statedb.Copy())
		if err != nil {
			return nil, err
		}
		allStateReads.Merge(postTxExecResult.stateReads)
	}

	// ======================================
	// 4. Verify CAL state reads
	// ======================================

	// TODO(EIP-8101)
	// if err := statedb.BlockAccessList().ValidateStateReads(*allStateReads); err != nil {
	// 	return &ProcessResultWithMetrics{
	// 		ProcessResult: &ProcessResult{Error: err},
	// 	}
	// }

	// ======================================
	// 5. Merge requests
	// ======================================
	var requests [][]byte
	if postTxExecResult == nil {
		requests = txExecRes.requests
	} else {
		requests = types.MergeRequests([][][]byte{txExecRes.requests, postTxExecResult.requests})
	}

	res := ProcessChunkResult{
		Index:    chunk.ChunkHeader.Index,
		Receipts: txExecRes.receipts,
		Logs:     txExecRes.logs,
		Requests: requests,
	}
	resWithMetrics := ProcessChunkResultWithMetrics{
		Result:           &res,
		ProcessChunkTime: time.Since(pStart),
	}
	return &resWithMetrics, nil
}

type chunkPreTxExecResult struct {
	stateReads bal.StateAccesses
}

// Should be caleld only for the first chunk (when pre-tx execution happens)
func (p *ParallelStateProcessor) chunkPreTxExec(header *types.Header, statedb *state.StateDB) (*chunkPreTxExecResult, error) {
	res := chunkPreTxExecResult{
		stateReads: make(bal.StateAccesses),
	}

	balTracer, hooks := NewBlockAccessListTracer()
	tracingStateDB := state.NewHookedState(statedb, hooks)

	cfg := vm.Config{
		Tracer:                  hooks,
		NoBaseFee:               p.vmCfg.NoBaseFee,
		EnablePreimageRecording: p.vmCfg.EnablePreimageRecording,
		ExtraEips:               slices.Clone(p.vmCfg.ExtraEips),
		StatelessSelfValidation: p.vmCfg.StatelessSelfValidation,
		EnableWitnessStats:      p.vmCfg.EnableWitnessStats,
	}

	context := NewEVMBlockContext(header, p.chain, nil)
	evm := vm.NewEVM(context, tracingStateDB, p.chainConfig(), cfg)

	if beaconRoot := header.ParentBeaconRoot; beaconRoot != nil {
		ProcessBeaconBlockRoot(*beaconRoot, evm)
	}
	if p.chainConfig().IsPrague(header.Number, header.Time) || p.chainConfig().IsVerkle(header.Number, header.Time) {
		ProcessParentBlockHash(header.ParentHash, evm)
	}

	diff, stateReads := balTracer.builder.FinalizedIdxChanges()
	if !statedb.BlockAccessList().ValidateStateDiff(0, diff) {
		return nil, fmt.Errorf("pre-Tx CAL validation failure")
	}

	res.stateReads.Merge(stateReads)
	return &res, nil
}

type chunkTxExecResult struct {
	receipts   types.Receipts
	logs       []*types.Log
	requests   [][]byte
	stateReads bal.StateAccesses
}

func (p *ParallelStateProcessor) chunkTxExec(header *types.Header, chunk *types.ChunkBody, statedb *state.StateDB, signer types.Signer) (*chunkTxExecResult, error) {
	txResCh := make(chan txExecResult)
	aggTxResCh := make(chan txExecResults)

	txCount := chunk.Transactions.Len()

	go aggregateTxExecResults(txCount, txResCh, aggTxResCh)

	var workers errgroup.Group
	workers.SetLimit(runtime.NumCPU())
	for i, tx := range chunk.Transactions {
		tx := tx
		txIdx := int(chunk.ChunkHeader.PreChunkTxCount) + i
		workers.Go(func() error {
			txResCh <- *p.execTx(header, tx, txIdx, statedb.Copy(), signer)
			return nil
		})
	}

	aggTxRes := <-aggTxResCh
	if aggTxRes.err != nil {
		return nil, aggTxRes.err
	}

	res := chunkTxExecResult{
		receipts:   aggTxRes.receipts,
		stateReads: aggTxRes.stateReads,
	}

	// Update CumulativeGasUsed and logs
	chunkGasUsed := uint64(0)
	chunkBlobGasUsed := uint64(0)
	for _, receipt := range res.receipts {
		chunkGasUsed += receipt.GasUsed
		chunkBlobGasUsed += receipt.BlobGasUsed

		receipt.CumulativeGasUsed = chunk.ChunkHeader.PreChunkGasUsed + chunkGasUsed
		res.logs = append(res.logs, receipt.Logs...)
	}

	// Create deposit requests if Prague is enabled.
	if p.chainConfig().IsPrague(header.Number, header.Time) {
		// EIP-6110
		if err := ParseDepositLogs(&res.requests, res.logs, p.chainConfig()); err != nil {
			return nil, err
		}
	}

	if chunkGasUsed != chunk.ChunkHeader.GasUsed {
		return nil, fmt.Errorf("Invalid chunk.GasUsed=%v, actual=%v", chunk.ChunkHeader.GasUsed, chunkGasUsed)
	}
	if chunkBlobGasUsed != chunk.ChunkHeader.BlobGasUsed {
		return nil, fmt.Errorf("Invalid chunk.BlobGasUsed=%v, actual=%v", chunk.ChunkHeader.BlobGasUsed, chunkBlobGasUsed)
	}

	return &res, nil
}

type txExecResults struct {
	receipts   types.Receipts
	stateReads bal.StateAccesses
	err        error
}

func aggregateTxExecResults(txCount int, txResCh chan txExecResult, resCh chan txExecResults) {
	var err error

	receipts := make(types.Receipts, 0, txCount)
	stateReads := make(bal.StateAccesses)

	for i := 0; i < txCount; i++ {
		txRes := <-txResCh
		if txRes.err != nil && err == nil {
			err = txRes.err
		}
		receipts = append(receipts, txRes.receipt)
		stateReads.Merge(txRes.stateReads)
	}
	if err != nil {
		resCh <- txExecResults{err: err}
		return
	}

	// Sort receipts by tx index
	slices.SortFunc(receipts, func(a, b *types.Receipt) int {
		return cmp.Compare(a.TransactionIndex, b.TransactionIndex)
	})

	resCh <- txExecResults{
		receipts:   receipts,
		stateReads: stateReads,
	}
}

type chunkPostTxExecResult struct {
	requests   [][]byte
	stateReads bal.StateAccesses
}

func (p *ParallelStateProcessor) chunkPostTxExec(
	header *types.Header,
	chunk *types.ChunkBody,
	statedb *state.StateDB,
) (*chunkPostTxExecResult, error) {
	requests := [][]byte{}

	balTracer, hooks := NewBlockAccessListTracer()
	balTracer.SetPostTx()
	tracingStateDB := state.NewHookedState(statedb, hooks)

	cfg := vm.Config{
		Tracer:                  hooks,
		NoBaseFee:               p.vmCfg.NoBaseFee,
		EnablePreimageRecording: p.vmCfg.EnablePreimageRecording,
		ExtraEips:               slices.Clone(p.vmCfg.ExtraEips),
		StatelessSelfValidation: p.vmCfg.StatelessSelfValidation,
		EnableWitnessStats:      p.vmCfg.EnableWitnessStats,
	}

	context := NewEVMBlockContext(header, p.chain, nil)
	evm := vm.NewEVM(context, tracingStateDB, p.chainConfig(), cfg)

	blockTxCount := int(chunk.ChunkHeader.PreChunkTxCount) + chunk.Transactions.Len()
	balIdx := blockTxCount + 1
	statedb.SetAccessListIndex(balIdx)

	// Read requests if Prague is enabled.
	if p.chainConfig().IsPrague(header.Number, header.Time) {
		// EIP-7002
		if err := ProcessWithdrawalQueue(&requests, evm); err != nil {
			return nil, err
		}

		// EIP-7251
		if err := ProcessConsolidationQueue(&requests, evm); err != nil {
			return nil, err
		}
	}

	// Finalize the block, applying any consensus engine specific extras (e.g. block rewards)
	// TODO(EIP-8101): I think we only need Withdrawals in the body
	p.chain.Engine().Finalize(p.chain, header, tracingStateDB, &types.Body{Withdrawals: chunk.Withdrawals})
	// invoke FinaliseIdxChanges so that withdrawals are accounted for in the state diff
	statedb.Finalise(true)

	balTracer.OnBlockFinalization()
	diff, stateReads := balTracer.builder.FinalizedIdxChanges()

	if !statedb.BlockAccessList().ValidateStateDiff(balIdx, diff) {
		return nil, fmt.Errorf("post-Tx CAL validation failure")
	}

	res := chunkPostTxExecResult{
		requests:   requests,
		stateReads: stateReads,
	}
	return &res, nil
}
