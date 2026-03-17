# EIP-8141 Frame Transaction Implementation in go-ethereum

## Objective

Implement the EIP-8141 frame transaction type in go-ethereum. The frame transaction (type `0x06`) introduces a multi-frame execution model where transaction validation, gas payment, and execution are defined abstractly through EVM code rather than relying on ECDSA signatures.

## Specification

Read the full EIP specification at `../../eips/EIPS/eip-8141.md`. The spec is comprehensive and is the authoritative source for all behavior. Key constants:

- `FRAME_TX_TYPE = 0x06`
- `FRAME_TX_INTRINSIC_COST = 15000`
- `ENTRY_POINT = address(0xaa)`
- `MAX_FRAMES = 10^3`
- New opcodes: `APPROVE (0xaa)`, `TXPARAMLOAD (0xb0)`, `TXPARAMSIZE (0xb1)`, `TXPARAMCOPY (0xb2)`

## Reference Implementation & Tests

The execution specs contain a reference implementation and comprehensive test suite:

- Test directory: `../../execution-specs/tests/unscheduled/eip8141_frame_tx/`
- Spec constants: `spec.py`
- Test helpers: `helpers.py`
- Test files: `test_frames.py`, `test_opcodes.py`, `test_invalid_tx.py`, `test_gas.py`, `test_blobs.py`, `test_receipts.py` (~3300 lines total)

All tests are marked `valid_from("Bogota")`. Study these tests carefully to understand expected behavior for edge cases.

## Fork Activation

The frame transaction MUST activate ONLY when the **Bogota** fork activates. The Bogota fork does not yet exist in go-ethereum. You must add it following the exact same pattern as Osaka and Amsterdam:

1. Add `BogotaTime *uint64` to `ChainConfig` in `params/config.go` (after `AmsterdamTime`)
2. Add `IsBogota` method to `ChainConfig`
3. Add `IsBogota` to the `Rules` struct
4. Add Bogota to the fork ordering validation
5. Add Bogota to `AllDevChainProtocolChanges` and `TestChainConfig` with `newUint64(0)`
6. Set real network configs to `nil` for now
7. Add fork name to the t8n tool's fork mapping in `tests/init.go`

## Implementation Plan

Work in the `main/` directory (the go-ethereum source tree). Follow existing architectural patterns precisely.

### 1. Transaction Type (`core/types/tx_frame.go`)

Create a new `FrameTx` struct implementing the `TxData` interface, following the pattern of `tx_blob.go` and `tx_setcode.go`:

```go
type FrameTx struct {
    ChainID              *uint256.Int
    Nonce                uint64
    Sender               common.Address
    Frames               []Frame
    MaxPriorityFeePerGas *uint256.Int
    MaxFeePerGas         *uint256.Int
    MaxFeePerBlobGas     *uint256.Int
    BlobVersionedHashes  []common.Hash
}

type Frame struct {
    Mode     uint8
    Target   *common.Address  // nil means sender
    GasLimit uint64
    Data     []byte
}
```

Key differences from other transaction types:
- **No signature fields** (V, R, S) — authentication is done via APPROVE opcode in VERIFY frames
- **No `To` field** — replaced by per-frame targets
- **No `Value` field** — transfers happen via account code
- **No `Gas` field** — gas is computed from `FRAME_TX_INTRINSIC_COST + calldata_cost(rlp(frames)) + sum(frame.gas_limit)`
- **No `AccessList`** — rationale explained in the EIP
- **Explicit `Sender` field** — sender is declared in the transaction, not recovered from a signature
- The `gas()` method must compute the total gas from intrinsic cost + calldata cost + sum of frame gas limits
- The RLP payload is: `[chain_id, nonce, sender, frames, max_priority_fee_per_gas, max_fee_per_gas, max_fee_per_blob_gas, blob_versioned_hashes]`
- Each frame encodes as: `[mode, target, gas_limit, data]`

For the `TxData` interface methods:
- `rawSignatureValues()` returns `(nil, nil, nil)` — no ECDSA signature
- `setSignatureValues()` is a no-op
- `sigHash()` computes the canonical signature hash with VERIFY frame data elided (see spec)
- `to()` returns `nil` (no single recipient)
- `value()` returns `big.NewInt(0)` (no value field)
- `data()` returns `nil` (no single data field)
- `accessList()` returns `nil`

Register type `FrameTxType = 0x06` in the transaction type constants.

Add `FrameTxType` to `decodeTyped()` in `transaction.go` to handle decoding.

### 2. Transaction Validation

Static validation constraints (reject invalid transactions before execution):
- `chain_id < 2^256`
- `nonce < 2^64`
- `len(frames) > 0 && len(frames) <= MAX_FRAMES`
- `len(sender) == 20` (enforced by type)
- `frame.mode < 3` for all frames
- `len(frame.target) == 20 OR frame.target is nil` for all frames
- If no blobs: `blob_versioned_hashes` must be empty and `max_fee_per_blob_gas` must be 0

Stateful validation:
- `tx.nonce == state[tx.sender].nonce`

Add validation in the `decode` method of `FrameTx` and in `stateTransition.preCheck()`.

**Important**: Frame transactions must NOT check that the sender is an EOA. The sender is expected to be a contract account. Skip the `ErrSenderNoEOA` check for frame transactions.

### 3. Message Extension (`core/state_transition.go`)

Extend the `Message` struct to carry frame transaction data. The invariant that **1 transaction == 1 Message** must hold. Add fields to `Message`:

```go
type Message struct {
    // ... existing fields ...

    // Frame transaction fields (EIP-8141)
    FrameSender common.Address
    Frames    []types.FrameTxFrame
}
```

Update `TransactionToMessage` to populate these fields for frame transactions. For frame transactions:
- `From` is set to `tx.Sender` (not recovered from signature)
- `GasLimit` is the computed total gas
- `To` is `nil`
- `Value` is `big.NewInt(0)`
- `Data` is `nil`
- The frame list is carried in `Frames`

### 4. State Transition (`core/state_transition.go`)

The `execute()` method in `stateTransition` needs a new code path for frame transactions. When `len(msg.Frames) != 0`:

1. **Pre-check**: Skip EOA check. Validate nonce. Validate gas fees. Do NOT call `buyGas()` — gas payment is deferred to APPROVE.

2. **Intrinsic gas**: Compute using `FRAME_TX_INTRINSIC_COST + calldata_cost(rlp(frames)) + sum(frame.gas_limit)`. Deduct intrinsic cost (the fixed + calldata portion) but NOT the per-frame gas limits — those are allocated individually.

3. **Frame execution loop**: Initialize `sender_approved = false`, `payer_approved = false`, `payer = common.Address{}`. For each frame:
   - Resolve target: if nil, use `tx.sender`
   - Set caller based on mode:
     - `DEFAULT` or `VERIFY`: caller is `ENTRY_POINT (0xaa)`
     - `SENDER`: caller is `tx.sender`, but ONLY if `sender_approved == true` (otherwise tx is invalid)
   - `ORIGIN` returns the frame's caller (not a fixed tx origin)
   - For `VERIFY` mode: execute as `STATICCALL` semantics (no state modifications)
   - Each frame gets its own `gas_limit` allocation — unused gas does NOT carry over
   - If mode is `VERIFY` and the frame did not successfully call `APPROVE`, the entire transaction is invalid
   - Transient storage (`TSTORE`/`TLOAD`) is discarded between frames
   - Warm/cold access journal is shared across frames

4. **Post-frame check**: After all frames, verify `payer_approved == true`. If not, the transaction is invalid.

5. **Gas refund**: Refund unused gas to the payer (the account that called `APPROVE(0x1)` or `APPROVE(0x2)`), NOT to `msg.From`.

### 5. New Opcodes (`core/vm/`)

#### APPROVE (`0xaa`)

Implement in `core/vm/instructions.go` or a new file like `core/vm/instructions_frame.go`:

- Stack inputs: `offset`, `length`, `scope`
- Behaves like `RETURN` (exits current context successfully) BUT also updates tx-scoped approval state
- Can only be called when `CALLER == frame.target`, otherwise exceptional halt
- Scope values:
  - `0x0`: Approve execution (`sender_approved = true`). Must have `CALLER == tx.sender`. Cannot re-approve.
  - `0x1`: Approve payment. Increments sender nonce, collects total gas cost. Must have `sender_approved == true`. Cannot re-approve. Must have sufficient balance.
  - `0x2`: Both execution and payment in one call. Must have `CALLER == tx.sender`. Cannot re-approve either. Must have sufficient balance.
- Any other scope value → exceptional halt

The APPROVE opcode needs access to transaction-scoped state. Consider adding frame context to the EVM or TxContext:
```go
type TxContext struct {
    // ... existing fields ...
    FrameContext *FrameContext  // nil for non-frame transactions
}
```

Where `FrameContext` holds `sender_approved`, `payer_approved`, `payer`, the frame list, current frame index, frame statuses, and the original transaction data needed by TXPARAM opcodes.

#### TXPARAMLOAD (`0xb0`)

Stack inputs: `in1`, `in2`, `offset`
Returns 32-byte value at offset into the parameter identified by `(in1, in2)`.

Parameter table:
| in1  | in2         | Value                                                    |
|------|-------------|----------------------------------------------------------|
| 0x00 | must be 0   | transaction type (0x06)                                  |
| 0x01 | must be 0   | nonce                                                    |
| 0x02 | must be 0   | sender (left-padded to 32 bytes)                         |
| 0x03 | must be 0   | max_priority_fee_per_gas                                 |
| 0x04 | must be 0   | max_fee_per_gas                                          |
| 0x05 | must be 0   | max_fee_per_blob_gas                                     |
| 0x06 | must be 0   | max cost (all gas used at max fee, includes blob + intrinsic) |
| 0x07 | must be 0   | len(blob_versioned_hashes)                               |
| 0x08 | must be 0   | signature hash (canonical, with VERIFY data elided)      |
| 0x09 | must be 0   | len(frames)                                              |
| 0x10 | must be 0   | current frame index                                      |
| 0x11 | frame index | target (resolved: nil → sender)                          |
| 0x12 | frame index | data (dynamic size — returns 0-size for VERIFY frames)   |
| 0x13 | frame index | gas_limit                                                |
| 0x14 | frame index | mode                                                     |
| 0x15 | frame index | status (exceptional halt if current or future frame)     |

Invalid `in1` values or out-of-bounds frame indices → exceptional halt.

#### TXPARAMSIZE (`0xb1`)

Stack inputs: `in1`, `in2`
Returns the byte size of the parameter identified by `(in1, in2)`.
All fixed parameters are 32 bytes. `data` (0x12) is dynamic.

#### TXPARAMCOPY (`0xb2`)

Stack inputs: `in1`, `in2`, `destOffset`, `offset`, `length`
Copies `length` bytes from parameter starting at `offset` to memory at `destOffset`.
Follows `CALLDATACOPY` semantics (zero-padding for out-of-range reads).

Register all new opcodes in the jump table, gated on the Bogota fork.

### 6. Receipt Changes (`core/types/receipt.go`)

Frame transactions have a different receipt format:
```
ReceiptPayload = [cumulative_gas_used, payer, [frame_receipt, ...]]
frame_receipt = [status, gas_used, logs]
```

Add fields to the `Receipt` struct:
```go
type Receipt struct {
    // ... existing fields ...
    Payer         *common.Address `json:"payer,omitempty"`
    FrameReceipts []FrameReceipt  `json:"frameReceipts,omitempty"`
}

type FrameReceipt struct {
    Status  uint64   `json:"status"`
    GasUsed uint64   `json:"gasUsed"`
    Logs    []*Log   `json:"logs"`
}
```

Update `MakeReceipt` in `core/state_processor.go` to populate frame receipt data. The `ExecutionResult` must carry frame-level results back from the state transition.

### 7. Gas Accounting

Total gas limit:
```
tx_gas_limit = FRAME_TX_INTRINSIC_COST + calldata_cost(rlp(tx.frames)) + sum(frame.gas_limit)
```

Where `calldata_cost` uses standard EVM calldata pricing (4 gas per zero byte, 16 per non-zero byte) applied to the RLP encoding of the frames list.

Each frame has its own `gas_limit`. Unused gas from a frame is NOT available to subsequent frames.

After all frames execute:
```
refund = sum(frame.gas_limit) - total_gas_used_across_frames
```

Refund goes to the payer (not sender), added back to block gas pool.

EIP-7623 floor data gas does NOT apply to frame transactions (no `Data` field in the traditional sense).

### 8. Signer Changes (`core/types/transaction_signing.go`)

Frame transactions do not use ECDSA signatures. Add handling in the `Signer` interface:
- `Sender()` for frame transactions returns `tx.Sender` directly (no signature recovery)
- `SignatureValues()` returns an error for frame transactions (cannot sign with ECDSA)
- `Hash()` computes the canonical signature hash (with VERIFY data elided)

### 9. EVM Changes (`core/vm/`)

- Add new opcodes to `opcodes.go` with proper naming
- Add opcode implementations
- Update the jump table (`jump_table.go`) — create a new Bogota instruction set
- The `ORIGIN` opcode must return the frame's caller for frame transactions, not a fixed origin. This requires the EVM's `TxContext.Origin` to be updated per-frame, or the ORIGIN instruction to check frame context.
- `STATICCALL` semantics for VERIFY frames: set the EVM's read-only flag

### 10. t8n Tool Updates

Update `cmd/evm/internal/t8ntool/`:
- `transaction.go`: Handle frame transaction JSON deserialization (the `txWithKey` struct needs to support frame tx fields; frame transactions have no `secretKey`)
- `execution.go`: Add Bogota fork handling
- `tx_iterator.go`: Handle frame transaction iteration and validation

The t8n tool transaction validity tests must pass. These test that invalid transactions are properly rejected with the correct error codes.

### 11. Test Infrastructure

Update `tests/init.go` to register the Bogota fork name so the test runner can parse fixture files.

## Critical Implementation Details

1. **1 tx == 1 Message invariant**: Do NOT create multiple Messages for one frame transaction. The Message carries the full frame list and the state transition loop iterates frames internally.

2. **APPROVE is like RETURN**: When APPROVE executes successfully, it terminates the current call frame just like RETURN. The return data from APPROVE is the data at `[offset, offset+length)`. But it ALSO sets tx-scoped flags.

3. **Ordering constraints**: `sender_approved` must be set before `payer_approved`. The sender must approve before a SENDER-mode frame can execute. These are hard validity requirements — violation means the entire tx is invalid (not just a frame revert).

4. **VERIFY frame semantics**: VERIFY frames execute with STATICCALL semantics (no state writes). Their `data` is elided from the signature hash and from TXPARAM introspection by other frames (returns size 0).

5. **Nonce handling**: The nonce is NOT incremented in preCheck for frame transactions. Instead, it is incremented when APPROVE(0x1) or APPROVE(0x2) is called. This is part of the "collect gas cost" step.

6. **Gas collection in APPROVE**: When APPROVE(0x1) or APPROVE(0x2) succeeds, the total gas cost of the transaction is collected from the approving account's balance. This is the deferred equivalent of `buyGas()`.

7. **Transient storage**: TSTORE/TLOAD transient storage is cleared between frames. This means each frame starts with a clean transient storage slate.

8. **Warm/cold access list**: The journal of warm/cold storage accesses is shared across all frames. An address warmed in frame 0 stays warm in frame 1.

9. **Frame status for TXPARAMLOAD(0x15)**: Returns 0 (failure) or 1 (success) for past frames. Accessing the current or future frame index causes an exceptional halt.

10. **No value transfer**: Frame transactions have no `value` field. The account's code handles any ETH transfers via CALL with value.

## Verification

After implementation, the following must pass:

1. **All existing tests**: No regressions on any previous fork tests
2. **EIP-8141 spec test fixtures**: Generated from the execution-specs test suite
3. **t8n tool transaction validity tests**: Both acceptance and rejection of frame transactions
4. **Build and vet**: `go build ./...` and `go vet ./...` must pass cleanly

## Files to Modify/Create

### New Files
- `core/types/tx_frame.go` — FrameTx type implementing TxData
- `core/vm/instructions_frame.go` — APPROVE, TXPARAM* opcode implementations (or add to existing files)

### Modified Files
- `params/config.go` — Add BogotaTime, IsBogota, Rules.IsBogota
- `core/types/transaction.go` — Add FrameTxType constant, decodeTyped case
- `core/types/transaction_signing.go` — Handle frame tx sender recovery
- `core/types/receipt.go` — Add FrameReceipt type, payer field
- `core/state_transition.go` — Message extension, frame execution loop, APPROVE gas collection
- `core/state_processor.go` — MakeReceipt changes for frame receipts
- `core/evm.go` — NewEVMTxContext changes for frame context
- `core/vm/opcodes.go` — New opcode constants
- `core/vm/jump_table.go` — Bogota instruction set with new opcodes
- `core/vm/evm.go` — TxContext extension for frame state, Origin per-frame
- `core/vm/interpreter.go` — If needed for STATICCALL enforcement in VERIFY frames
- `cmd/evm/internal/t8ntool/transaction.go` — Frame tx JSON handling
- `cmd/evm/internal/t8ntool/execution.go` — Bogota fork support
- `tests/init.go` — Register Bogota fork name
