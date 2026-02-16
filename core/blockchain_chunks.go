package core

import (
	"errors"
	"math"
	"slices"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/types/bal"
	"github.com/ethereum/go-ethereum/trie"
)

var (
	ErrChunkIndexOutOfRange = errors.New("chunk index out of range")
	ErrMissingChunk         = errors.New("missing chunk")
	ErrMissingCal           = errors.New("missing cal")
	ErrMissingResult        = errors.New("missing result")
	ErrChunkAlreadyExists   = errors.New("chunk already exists")
	ErrCalAlreadyExists     = errors.New("cal already exists")

	ErrInvalidStateRoot           = errors.New("invalid state root")
	ErrInvalidTransactionsRoot    = errors.New("invalid transactions root")
	ErrInvalidReceiptsRoot        = errors.New("invalid receipts root")
	ErrInvalidWithdrawalsRoot     = errors.New("invalid withdrawals root")
	ErrInvalidRequestsHash        = errors.New("invalid requests hash")
	ErrInvalidCalHash             = errors.New("invalid CAL hash")
	ErrInvalidGasUsed             = errors.New("invalid gas used")
	ErrInvalidBlobGasUsed         = errors.New("invalid blob gas used")
	ErrInvalidBlobHashes          = errors.New("invalid blob hashes")
	ErrInvalidPreChunkTxCount     = errors.New("invalid pre chunk tx count")
	ErrInvalidPreChunkGasUsed     = errors.New("invalid pre chunk gas used")
	ErrInvalidPreChunkBlobGasUsed = errors.New("invalid pre chunk blob gas used")
)

type BlockChunkValidator struct {
	bc         *BlockChain
	parent     types.Header
	header     types.Header
	blobHashes []common.Hash

	chunks  []*types.ChunkBody
	cals    []bal.BlockAccessList
	results []*ProcessChunkResult

	stateTransition *state.BALStateTransition
	stateRootReady  chan struct{}
}

func (v *BlockChunkValidator) ChunkCount() uint16 {
	return uint16(len(v.chunks))
}

func (v *BlockChunkValidator) AddCal(chunkIndex uint16, cal bal.BlockAccessList) error {
	if chunkIndex >= v.ChunkCount() {
		return ErrChunkIndexOutOfRange
	}
	if v.cals[chunkIndex] != nil {
		return ErrCalAlreadyExists
	}
	v.cals[chunkIndex] = cal

	if err := v.maybeCalculateStateRoot(); err != nil {
		if err != ErrMissingCal {
			return err
		}
	}

	return nil
}

func (v *BlockChunkValidator) AddAndExecute(chunk *types.ChunkBody) error {
	chunkIndex := chunk.ChunkHeader.Index

	if err := v.AddChunk(chunk); err != nil {
		return err
	}

	if err := v.ExecuteChunk(chunkIndex); err != nil {
		v.chunks[chunkIndex] = nil
		return err
	}

	return nil
}

func (v *BlockChunkValidator) AddChunk(chunk *types.ChunkBody) error {
	chunkIndex := chunk.ChunkHeader.Index
	if chunkIndex >= v.ChunkCount() {
		return ErrChunkIndexOutOfRange
	}

	hasher := trie.NewStackTrie(nil)
	if chunk.ChunkHeader.TxsRoot != types.DeriveSha(chunk.Transactions, hasher) {
		return ErrInvalidTransactionsRoot
	}
	if chunk.ChunkHeader.WithdrawalsRoot != types.DeriveSha(chunk.Withdrawals, hasher) {
		return ErrInvalidWithdrawalsRoot
	}

	if v.chunks[chunkIndex] != nil {
		return ErrChunkAlreadyExists
	}
	v.chunks[chunkIndex] = chunk
	return nil
}

func (v *BlockChunkValidator) ExecuteChunk(chunkIndex uint16) error {
	if chunkIndex >= v.ChunkCount() {
		return ErrChunkIndexOutOfRange
	}

	chunk := v.chunks[chunkIndex]
	if chunk == nil {
		return ErrMissingChunk
	}

	cals := v.cals[:chunkIndex+1]
	for _, cal := range cals {
		if cal == nil {
			return ErrMissingCal
		}
	}
	mergedCals := bal.MergeCals(cals...)

	cal := cals[chunkIndex]
	if chunk.ChunkHeader.ChunkAccessListHash != cal.Hash() {
		return ErrInvalidCalHash
	}

	// TODO(EIP-8101): figure out if we need correct block txCount
	txCount := int(chunk.ChunkHeader.PreChunkTxCount) + chunk.Transactions.Len()

	reader, err := v.bc.statedb.Reader(v.parent.Root)
	if err != nil {
		return err
	}
	calStateReader := state.NewBALReader(txCount, mergedCals, reader)

	statedb, err := state.New(v.parent.Root, v.bc.statedb)
	if err != nil {
		return err
	}
	statedb.SetBlockAccessList(calStateReader)

	resultWithMetrics, err := v.bc.parallelProcessor.ProcessChunk(&v.header, chunk, cal, statedb)
	if err != nil {
		return err
	}

	v.results[chunkIndex] = resultWithMetrics.Result

	return nil
}

// Calculates StateRoot based on
func (v *BlockChunkValidator) maybeCalculateStateRoot() error {
	for _, cal := range v.cals {
		if cal == nil {
			return ErrMissingCal
		}
	}

	reader, err := v.bc.statedb.Reader(v.parent.Root)
	if err != nil {
		return err
	}

	txCount := math.MaxInt - 2 // TODO(EIP-8101): make sure we use correct txCount
	stateReader := state.NewBALReader(txCount, bal.MergeCals(v.cals...), reader)

	stateTransition, err := state.NewBALStateTransition(stateReader, v.bc.statedb, v.parent.Root)
	if err != nil {
		return err
	}

	go func() {
		// Calculate the root
		stateTransition.IntermediateRoot(false)
		v.stateTransition = stateTransition
		close(v.stateRootReady)
	}()

	return nil
}

func (v *BlockChunkValidator) Finalize() error {
	if err := v.validateBlock(); err != nil {
		return err
	}

	block := types.NewBlockWithHeader(&v.header).WithBody(v.createBody())

	receipts := make(types.Receipts, 0)
	for _, result := range v.results {
		receipts = append(receipts, result.Receipts...)
	}

	// Don't set the head, only insert the block
	if err := v.bc.writeBlockWithState(block, receipts, v.stateTransition); err != nil {
		return err
	}

	return nil
}

func (v *BlockChunkValidator) validateBlock() error {
	hasher := trie.NewStackTrie(nil)

	body := v.createBody()
	if v.header.TxHash != types.DeriveSha(types.Transactions(body.Transactions), hasher) {
		return ErrInvalidTransactionsRoot
	}
	if *v.header.WithdrawalsHash != types.DeriveSha(types.Withdrawals(body.Withdrawals), hasher) {
		return ErrInvalidWithdrawalsRoot
	}

	for _, cal := range v.cals {
		if cal == nil {
			return ErrMissingCal
		}
	}

	txCount := 0
	gasUsed := uint64(0)
	blobGasUsed := uint64(0)
	for _, chunk := range v.chunks {
		if chunk == nil {
			return ErrMissingChunk
		}

		chunkHeader := chunk.ChunkHeader

		if chunkHeader.PreChunkTxCount != uint64(txCount) {
			return ErrInvalidPreChunkTxCount
		}
		txCount += chunk.Transactions.Len()

		if chunkHeader.PreChunkGasUsed != gasUsed {
			return ErrInvalidPreChunkGasUsed
		}
		gasUsed += chunkHeader.GasUsed

		if chunkHeader.PreChunkBlobGasUsed != blobGasUsed {
			return ErrInvalidPreChunkBlobGasUsed
		}
		blobGasUsed += chunkHeader.BlobGasUsed
	}
	if v.header.GasUsed != gasUsed {
		return ErrInvalidGasUsed
	}
	if *v.header.BlobGasUsed != blobGasUsed {
		return ErrInvalidBlobGasUsed
	}

	blobHashes := make([]common.Hash, 0)
	for _, tx := range body.Transactions {
		blobHashes = append(blobHashes, tx.BlobHashes()...)
	}
	if !slices.Equal(v.blobHashes, blobHashes) {
		return ErrInvalidBlobHashes
	}

	receipts := make(types.Receipts, 0)
	logs := make([]*types.Log, 0)
	allRequests := make([][][]byte, 0)
	for _, result := range v.results {
		if result == nil {
			return ErrMissingResult
		}

		receipts = append(receipts, result.Receipts...)
		logs = append(logs, result.Logs...)
		allRequests = append(allRequests, result.Requests)
	}
	if v.header.ReceiptHash != types.DeriveSha(receipts, hasher) {
		return ErrInvalidReceiptsRoot
	}
	if *v.header.RequestsHash != types.CalcRequestsHash(types.MergeRequests(allRequests)) {
		return ErrInvalidRequestsHash
	}

	// wait for state root and verify it
	<-v.stateRootReady
	if v.header.Root != v.stateTransition.IntermediateRoot(false) {
		return ErrInvalidStateRoot
	}

	// TODO(EIP-8101): Validate other fields: Number, Time, UncleHash, Nonce, Bloom, GasLimit, BaseFee, ExcessBlobGas...

	return nil
}

func (v *BlockChunkValidator) createBody() types.Body {
	transactions := make(types.Transactions, 0)
	withdrawals := make(types.Withdrawals, 0)
	for _, chunk := range v.chunks {
		transactions = append(transactions, chunk.Transactions...)
		withdrawals = append(withdrawals, chunk.Withdrawals...)
	}

	blockAccessList := bal.MergeCals(v.cals...)

	return types.Body{
		Transactions: transactions,
		Withdrawals:  withdrawals,
		AccessList:   &blockAccessList,
	}
}

// =============================================
// BlockChain
// =============================================

func (bc *BlockChain) NewChunkValidator(header *types.Header, blobHashes []common.Hash, chunkCount uint16) (*BlockChunkValidator, error) {
	// Ancestor block must be known.
	if !bc.HasBlockAndState(header.ParentHash, header.Number.Uint64()-1) {
		if !bc.HasBlock(header.ParentHash, header.Number.Uint64()-1) {
			return nil, consensus.ErrUnknownAncestor
		}
		return nil, consensus.ErrPrunedAncestor
	}

	parent := bc.GetBlock(header.ParentHash, header.Number.Uint64()-1)
	if parent == nil {
		return nil, consensus.ErrUnknownAncestor
	}

	validator := &BlockChunkValidator{
		bc:         bc,
		parent:     *parent.Header(),
		header:     *header,
		blobHashes: blobHashes,

		chunks:  make([]*types.ChunkBody, chunkCount),
		cals:    make([]bal.BlockAccessList, chunkCount),
		results: make([]*ProcessChunkResult, chunkCount),

		stateRootReady: make(chan struct{}),
	}
	bc.blockChunkValidator.Add(header.Hash(), validator)
	return validator, nil
}

func (bc *BlockChain) GetChunkValidator(blockHash common.Hash) *BlockChunkValidator {
	v, _ := bc.blockChunkValidator.Get(blockHash)
	return v
}
