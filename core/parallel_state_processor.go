package core

import (
	"cmp"
	"fmt"
	"math/big"
	"slices"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/misc"
	"github.com/ethereum/go-ethereum/consensus/misc/eip4844"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/types/bal"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"golang.org/x/sync/errgroup"
)

type ParallelProcessorContext struct {
	blockContext *vm.BlockContext
	blockHash    common.Hash
	blockTxCount int    // total number of transactions in a block
	txCount      uint64 // number of transaction that will be processed in parallel
	preGasUsed   uint64
}

// ProcessResultWithMetrics wraps ProcessResult with some metrics that are
// emitted when executing blocks containing access lists.
type ProcessResultWithMetrics struct {
	ProcessResult *ProcessResult
	// the time it took to load modified prestate accounts from disk and instantiate statedbs for execution
	PreProcessTime time.Duration
	// the time it took to validate the block post transaction execution and state root calculation
	PostProcessTime time.Duration
	// the time it took to hash the state root, including intermediate node reads
	RootCalcTime time.Duration
	// the time that it took to load the prestate for accounts that were updated as part of
	// the state root update
	PrestateLoadTime time.Duration
	// the time it took to execute all txs in the block
	ExecTime time.Duration
}

// ParallelStateProcessor is used to execute and verify blocks containing
// access lists.
type ParallelStateProcessor struct {
	*StateProcessor
	vmCfg *vm.Config
}

// NewParallelStateProcessor returns a new ParallelStateProcessor instance.
func NewParallelStateProcessor(chain *HeaderChain, vmConfig *vm.Config) ParallelStateProcessor {
	res := NewStateProcessor(chain)
	return ParallelStateProcessor{
		res,
		vmConfig,
	}
}

// called when all transactions have successfully executed.
// performs post-tx state transition (system contracts and withdrawals)
// and updates the ProcessResultWithMetrics
func (p *ParallelStateProcessor) processPostTx(context *ParallelProcessorContext, allStateReads *bal.StateAccesses, postTxState *state.StateDB, requests *[][]byte, withdrawals types.Withdrawals) error {
	balTracer, hooks := NewBlockAccessListTracer()
	tracingStateDB := state.NewHookedState(postTxState, hooks)
	postTxState.SetAccessListIndex(context.blockTxCount + 1)

	blockContext := context.blockContext
	cfg := vm.Config{
		Tracer:                  hooks,
		NoBaseFee:               p.vmCfg.NoBaseFee,
		EnablePreimageRecording: p.vmCfg.EnablePreimageRecording,
		ExtraEips:               slices.Clone(p.vmCfg.ExtraEips),
		StatelessSelfValidation: p.vmCfg.StatelessSelfValidation,
		EnableWitnessStats:      p.vmCfg.EnableWitnessStats,
	}
	evm := vm.NewEVM(*blockContext, tracingStateDB, p.chainConfig(), cfg)

	// Read requests if Prague is enabled.
	if p.chainConfig().IsPrague(blockContext.BlockNumber, blockContext.Time) {
		// EIP-7002
		if err := ProcessWithdrawalQueue(requests, evm); err != nil {
			return err
		}

		// EIP-7251
		if err := ProcessConsolidationQueue(requests, evm); err != nil {
			return err
		}
	}

	// TODO(milos): empty header and body with only withdrawals is minimal required
	header := types.Header{Difficulty: common.Big0}
	body := types.Body{Withdrawals: withdrawals}
	// Finalize the block, applying any consensus engine specific extras (e.g. block rewards)
	p.chain.Engine().Finalize(p.chain, &header, tracingStateDB, &body)
	// invoke FinaliseIdxChanges so that withdrawals are accounted for in the state diff
	postTxState.Finalise(true)

	balTracer.OnBlockFinalization()
	diff, stateReads := balTracer.builder.FinalizedIdxChanges()
	allStateReads.Merge(stateReads)

	balIdx := context.blockTxCount + 1
	if err := postTxState.BlockAccessList().ValidateStateDiff(balIdx, diff); err != nil {
		return err
	}

	// TODO(milos): maybe disable this until BAL is fixed for chunks
	if err := postTxState.BlockAccessList().ValidateStateReads(*allStateReads); err != nil {
		log.Error("INVALID BAL STATE READS")
		return err
	}

	return nil
}

type txExecResult struct {
	idx     int // transaction index
	receipt *types.Receipt
	err     error // non-EVM error which would render the block invalid

	stateReads bal.StateAccesses
}

// resultHandler polls until all transactions have finished executing and the
// state root calculation is complete. The result is emitted on resCh.
func (p *ParallelStateProcessor) resultHandler(context *ParallelProcessorContext, preTxStateReads bal.StateAccesses, txResCh <-chan txExecResult, resCh chan *ProcessResult) {
	blockContext := context.blockContext

	// 1. if the block has transactions, receive the execution results from all of them and return an error on resCh if any txs err'd
	// 2. once all txs are executed, compute the post-tx state transition and produce the ProcessResult sending it on resCh (or an error if the post-tx state didn't match what is reported in the BAL)
	var receipts []*types.Receipt
	gp := new(GasPool)
	gp.SetGas(blockContext.GasLimit)
	var execErr error
	var numTxComplete uint64

	allReads := make(bal.StateAccesses)
	allReads.Merge(preTxStateReads)
	if context.txCount > 0 {
	loop:
		for {
			select {
			case res := <-txResCh:
				if execErr == nil {
					if res.err != nil {
						execErr = res.err
					} else {
						if err := gp.SubGas(res.receipt.GasUsed); err != nil {
							execErr = err
						} else {
							receipts = append(receipts, res.receipt)
							allReads.Merge(res.stateReads)
						}
					}
				}
				numTxComplete++
				if numTxComplete == context.txCount {
					break loop
				}
			}
		}

		if execErr != nil {
			resCh <- &ProcessResult{Error: execErr}
			return
		}
	}

	// 1. order the receipts by tx index
	// 2. correctly calculate the cumulative gas used per receipt, returning bad block error if it goes over the allowed
	slices.SortFunc(receipts, func(a, b *types.Receipt) int {
		return cmp.Compare(a.TransactionIndex, b.TransactionIndex)
	})

	var cumulativeGasUsed = context.preGasUsed
	var allLogs []*types.Log
	for _, receipt := range receipts {
		cumulativeGasUsed += receipt.GasUsed
		receipt.CumulativeGasUsed = cumulativeGasUsed
		if receipt.CumulativeGasUsed > blockContext.GasLimit {
			resCh <- &ProcessResult{Error: fmt.Errorf("gas limit exceeded")}
			return
		}
		allLogs = append(allLogs, receipt.Logs...)
	}

	var requests [][]byte = nil
	if p.chainConfig().IsPrague(blockContext.BlockNumber, blockContext.Time) {
		requests = [][]byte{}
		// EIP-6110
		if err := ParseDepositLogs(&requests, allLogs, p.chainConfig()); err != nil {
			resCh <- &ProcessResult{Error: err}
			return
		}
	}

	resCh <- &ProcessResult{
		Receipts: receipts,
		Requests: requests,
		Logs:     allLogs,
		GasUsed:  cumulativeGasUsed,
	}
}

type stateRootCalculationResult struct {
	err              error
	prestateLoadTime time.Duration
	rootCalcTime     time.Duration
	root             common.Hash
}

// calcAndVerifyRoot performs the post-state root hash calculation, verifying
// it against what is reported by the block and returning a result on resCh.
func (p *ParallelStateProcessor) calcAndVerifyRoot(preState *state.StateDB, expectedStateRoot common.Hash, resCh chan stateRootCalculationResult) {
	// calculate and apply the block state modifications
	root, prestateLoadTime, rootCalcTime := preState.BlockAccessList().StateRoot(preState)

	res := stateRootCalculationResult{
		root:             root,
		prestateLoadTime: prestateLoadTime,
		rootCalcTime:     rootCalcTime,
	}

	if root != expectedStateRoot {
		res.err = fmt.Errorf("state root mismatch. local: %x. remote: %x", root, expectedStateRoot)
	}
	resCh <- res
}

// execTx executes single transaction returning a result which includes state accessed/modified
func (p *ParallelStateProcessor) execTx(context *ParallelProcessorContext, tx *types.Transaction, txIdx int, db *state.StateDB, signer types.Signer) *txExecResult {
	blockContext := context.blockContext

	balTracer, hooks := NewBlockAccessListTracer()
	tracingStateDB := state.NewHookedState(db, hooks)

	cfg := vm.Config{
		Tracer:                  hooks,
		NoBaseFee:               p.vmCfg.NoBaseFee,
		EnablePreimageRecording: p.vmCfg.EnablePreimageRecording,
		ExtraEips:               slices.Clone(p.vmCfg.ExtraEips),
		StatelessSelfValidation: p.vmCfg.StatelessSelfValidation,
		EnableWitnessStats:      p.vmCfg.EnableWitnessStats,
	}
	evm := vm.NewEVM(*blockContext, tracingStateDB, p.chainConfig(), cfg)

	msg, err := TransactionToMessage(tx, signer, blockContext.BaseFee)
	if err != nil {
		err = fmt.Errorf("could not apply tx %d [%v]: %w", txIdx, tx.Hash().Hex(), err)
		return &txExecResult{err: err}
	}
	gp := new(GasPool)
	gp.SetGas(blockContext.GasLimit)
	db.SetTxContext(tx.Hash(), txIdx)
	var gasUsed uint64
	receipt, err := ApplyTransactionWithEVM(msg, gp, db, blockContext.BlockNumber, context.blockHash, blockContext.Time, tx, &gasUsed, evm)
	if err != nil {
		err := fmt.Errorf("could not apply tx %d [%v]: %w", txIdx, tx.Hash().Hex(), err)
		return &txExecResult{err: err}
	}

	diff, accesses := balTracer.builder.FinalizedIdxChanges()
	if err := db.BlockAccessList().ValidateStateDiff(txIdx+1, diff); err != nil {
		return &txExecResult{err: err}
	}

	return &txExecResult{
		idx:        txIdx,
		receipt:    receipt,
		stateReads: accesses,
	}
}

// Process performs EVM execution and state root computation for a block which is known
// to contain an access list.
func (p *ParallelStateProcessor) Process(block *types.Block, statedb *state.StateDB, cfg vm.Config) (*ProcessResultWithMetrics, error) {
	var (
		header                = block.Header()
		signer                = types.MakeSigner(p.chainConfig(), header.Number, header.Time)
		resCh                 = make(chan *ProcessResult)
		txResCh               = make(chan txExecResult)
		stateRootCalcResultCh = make(chan stateRootCalculationResult)
		res                   = ProcessResultWithMetrics{}
	)

	tPreProcessStart := time.Now()

	// Mutate the block and state according to any hard-fork specs
	if p.chainConfig().DAOForkSupport && p.chainConfig().DAOForkBlock != nil && p.chainConfig().DAOForkBlock.Cmp(block.Number()) == 0 {
		misc.ApplyDAOHardFork(statedb)
	}
	alReader := state.NewBALReader(*block.Body().AccessList, block.Transactions().Len(), statedb)
	statedb.SetBlockAccessList(alReader)

	balTracer, hooks := NewBlockAccessListTracer()
	tracingStateDB := state.NewHookedState(statedb, hooks)
	// TODO: figure out exactly why we need to set the hooks on the TracingStateDB and the vm.Config
	cfg.Tracer = hooks

	blockContext := NewEVMBlockContext(header, p.chain, nil)
	context := ParallelProcessorContext{
		blockContext: &blockContext,
		blockTxCount: block.Transactions().Len(),
		txCount:      uint64(block.Transactions().Len()),
		blockHash:    block.Hash(),
		preGasUsed:   0,
	}
	evm := vm.NewEVM(blockContext, tracingStateDB, p.chainConfig(), cfg)

	if beaconRoot := block.BeaconRoot(); beaconRoot != nil {
		ProcessBeaconBlockRoot(*beaconRoot, evm)
	}
	if p.chainConfig().IsPrague(block.Number(), block.Time()) || p.chainConfig().IsVerkle(block.Number(), block.Time()) {
		ProcessParentBlockHash(block.ParentHash(), evm)
	}

	// TODO: weird that I have to manually call finalize here
	balTracer.OnPreTxExecutionDone()

	diff, stateReads := balTracer.builder.FinalizedIdxChanges()
	if err := statedb.BlockAccessList().ValidateStateDiff(0, diff); err != nil {
		return nil, err
	}

	// compute the post-tx state prestate (before applying final block system calls and eip-4895 withdrawals)
	// the post-tx state transition is verified by resultHandler
	postTxState := statedb.Copy()

	res.PreProcessTime = time.Since(tPreProcessStart)
	tExecStart := time.Now()

	// execute transactions and state root calculation in parallel

	// TODO: figure out how to funnel the state reads from the bal tracer through to the post-block-exec state/slot read
	// validation
	go p.resultHandler(&context, stateReads, txResCh, resCh)
	var workers errgroup.Group
	startingState := statedb.Copy()
	for i, tx := range block.Transactions() {
		txIndex, tx := i, tx
		workers.Go(func() error {
			res := p.execTx(&context, tx, txIndex, startingState.Copy(), signer)
			txResCh <- *res
			return nil
		})
	}

	go p.calcAndVerifyRoot(statedb, block.Root(), stateRootCalcResultCh)

	res.ProcessResult = <-resCh
	if res.ProcessResult.Error != nil {
		return nil, res.ProcessResult.Error
	}

	res.ExecTime = time.Since(tExecStart)
	tPostProcessStart := time.Now()

	if err := p.processPostTx(&context, &stateReads, postTxState, &res.ProcessResult.Requests, block.Withdrawals()); err != nil {
		return nil, err
	}
	res.PostProcessTime = time.Since(tPostProcessStart)

	// Wait for stateRootCalc
	rootCalcRes := <-stateRootCalcResultCh
	if rootCalcRes.err != nil {
		return nil, rootCalcRes.err
	}
	res.RootCalcTime = rootCalcRes.rootCalcTime
	res.PrestateLoadTime = rootCalcRes.prestateLoadTime

	return &res, nil
}

func (p *ParallelStateProcessor) ProcessChunk(
	blockMetadata *types.ChunkBlockMetadata,
	chunkHeader *types.ChunkHeader,
	transactions types.Transactions,
	withdrawals types.Withdrawals,
	chunkAccessLists bal.BlockAccessList,
	statedb *state.StateDB,
	cfg vm.Config,
) (*ProcessResultWithMetrics, error) {
	var (
		resCh   = make(chan *ProcessResult)
		txResCh = make(chan txExecResult)
		res     = ProcessResultWithMetrics{}
	)

	context := ParallelProcessorContext{
		blockContext: createBlockContext(blockMetadata, p.chain),
		blockTxCount: int(chunkHeader.PreTxCount + chunkHeader.TxCount),
		txCount:      chunkHeader.TxCount,
		blockHash:    common.Hash{}, // TODO(milos): This is ok (probably)
		preGasUsed:   chunkHeader.PreGasUsed,
	}

	tPreProcessStart := time.Now()

	alReader := state.NewBALReader(chunkAccessLists, int(context.blockTxCount), statedb)
	statedb.SetBlockAccessList(alReader)

	blockContext := context.blockContext

	signer := types.MakeSigner(p.chainConfig(), blockContext.BlockNumber, blockContext.Time)
	allStateReads := make(bal.StateAccesses)

	if chunkHeader.ChunkIndex == 0 {
		balTracer, hooks := NewBlockAccessListTracer()
		tracingStateDB := state.NewHookedState(statedb, hooks)
		cfg.Tracer = hooks

		evm := vm.NewEVM(*blockContext, tracingStateDB, p.chainConfig(), cfg)
		ProcessBeaconBlockRoot(blockMetadata.ParentBeaconRoot, evm)

		if p.chainConfig().IsPrague(blockContext.BlockNumber, blockContext.Time) || p.chainConfig().IsVerkle(blockContext.BlockNumber, blockContext.Time) {
			ProcessParentBlockHash(blockMetadata.ParentHash, evm)
		}
		balTracer.OnPreTxExecutionDone()

		// TODO(milos): Correctly validate BAL
		diff, stateReads := balTracer.builder.FinalizedIdxChanges()
		if err := statedb.BlockAccessList().ValidateStateDiff(0, diff); err != nil {
			return nil, err
		}
		allStateReads.Merge(stateReads)
	}

	// compute the post-tx state prestate (before applying final block system calls and eip-4895 withdrawals)
	// the post-tx state transition is verified by resultHandler
	postTxState := statedb.Copy()

	res.PreProcessTime = time.Since(tPreProcessStart)
	tExecStart := time.Now()

	go p.resultHandler(&context, allStateReads, txResCh, resCh)
	var workers errgroup.Group
	startingState := statedb.Copy()
	for i, tx := range transactions {
		txIndex, tx := int(chunkHeader.PreTxCount)+i, tx
		workers.Go(func() error {
			res := p.execTx(&context, tx, txIndex, startingState.Copy(), signer)
			txResCh <- *res
			return nil
		})
	}

	res.ProcessResult = <-resCh
	if res.ProcessResult.Error != nil {
		return nil, res.ProcessResult.Error
	}

	res.ExecTime = time.Since(tExecStart)

	if chunkHeader.IsLast {
		tPostProcessStart := time.Now()
		if err := p.processPostTx(&context, &allStateReads, postTxState, &res.ProcessResult.Requests, withdrawals); err != nil {
			return nil, err
		}
		res.PostProcessTime = time.Since(tPostProcessStart)
	}

	return &res, nil
}

func createBlockContext(blockMetadata *types.ChunkBlockMetadata, chain ChainContext) *vm.BlockContext {
	blobBaseFee := eip4844.CalcBlobFeeDirectly(chain.Config(), blockMetadata.Timestamp, blockMetadata.ExcessBlobGas)
	return &vm.BlockContext{
		CanTransfer: CanTransfer,
		Transfer:    Transfer,
		GetHash:     getHashFn(blockMetadata.Number, blockMetadata.ParentHash, chain),
		Coinbase:    blockMetadata.Coinbase,
		GasLimit:    blockMetadata.GasLimit,
		BlockNumber: big.NewInt(int64(blockMetadata.Number)),
		Time:        blockMetadata.Timestamp,
		Difficulty:  common.Big0,
		BaseFee:     &blockMetadata.BaseFeePerGas,
		BlobBaseFee: blobBaseFee,
		Random:      &blockMetadata.MixHash,
	}
}
