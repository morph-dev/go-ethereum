# Add Bogota Fork Plumbing

Add the **Bogota** hard fork definition to the codebase. Bogota is a timestamp-based fork that comes **after Amsterdam** in the fork ordering. It has no new features—this is purely structural plumbing. Follow exactly how Amsterdam was added, modifying every file listed below.

**Do NOT assign activation timestamps to any chain configs (mainnet, testnets, etc.). Leave `BogotaTime` as `nil` everywhere except `AllDevChainProtocolChanges` (set to `newUint64(0)`).**

---

## 1. `params/forks/forks.go`

- Add `Bogota` constant after `Amsterdam` in the `Fork` iota enum.
- Add `Bogota: "Bogota"` to the `forkToString` map.

## 2. `params/config.go`

### ChainConfig struct
- Add field `BogotaTime *uint64` with json tag `"bogotaTime,omitempty"` and comment, after `AmsterdamTime`.

### BlobScheduleConfig struct
- Add field `Bogota *BlobConfig` with json tag `"bogota,omitempty"` after `Amsterdam`.

### Network chain configs (Mainnet, Holesky, Sepolia, Hoodi)
- Add `BogotaTime: nil` (no activation time yet for any network).
- Do NOT add a `Bogota` entry to their `BlobScheduleConfig` (since the fork isn't activated).

### AllEthashProtocolChanges, AllCliqueProtocolChanges, TestChainConfig, MergedTestChainConfig, NonActivatedConfig
- Add `BogotaTime: nil` alongside the other nil time-forks.

### AllDevChainProtocolChanges
- Add `BogotaTime: newUint64(0)` (dev chains activate everything at genesis).

### `String()` method
- Add a block printing `BogotaTime` after the `AmsterdamTime` block.

### `Description()` method
- Add a banner line for Bogota after the Amsterdam line, printing blob config if `BogotaTime != nil`.

### `IsBogota()` method
- Add `func (c *ChainConfig) IsBogota(num *big.Int, time uint64) bool` returning `c.IsLondon(num) && isTimestampForked(c.BogotaTime, time)`, placed after `IsAmsterdam`.

### `CheckConfigForkOrder()`
- Add `{name: "bogota", timestamp: c.BogotaTime, optional: true}` to the fork ordering list, **after** `amsterdam`.
- Add `{name: "bogota", timestamp: c.BogotaTime, config: bsc.Bogota}` to the blob schedule validation list, **after** `amsterdam`.

### `checkCompatible()`
- Add a timestamp compatibility check for `BogotaTime` after the `AmsterdamTime` check:
  ```go
  if isForkTimestampIncompatible(c.BogotaTime, newcfg.BogotaTime, headTimestamp) {
      return newTimestampCompatError("Bogota fork timestamp", c.BogotaTime, newcfg.BogotaTime)
  }
  ```

### `LatestFork()`
- Add `case c.IsBogota(london, time): return forks.Bogota` as the **first** (highest-priority) case, before the `IsAmsterdam` case.

### `BlobConfig()` (the method on ChainConfig)
- Add `case forks.Bogota: return c.BlobScheduleConfig.Bogota` before the existing cases.

### `Timestamp()` (the method on ChainConfig)
- Add `case fork == forks.Bogota: return c.BogotaTime` as the first case.

### `Rules` struct
- Add `IsBogota` bool field. Place it on the line with `IsAmsterdam`, e.g.: `IsAmsterdam, IsBogota, IsVerkle bool`.

### `Rules()` method
- Add `IsBogota: isMerge && c.IsBogota(num, timestamp)` to the returned struct.

---

## 3. `eth/catalyst/api.go`

Every `checkFork` call that lists post-Osaka forks must include `forks.Bogota`. Find each of these patterns and append `forks.Bogota` to the fork list:

- `ForkchoiceUpdatedV3` — the `checkFork` call listing cancun/prague/osaka/BPO forks.
- `GetPayloadV5` — the forks list in the `getPayload` call.
- `NewPayloadV4` — the `checkFork` call listing prague/osaka/BPO forks.

## 4. `eth/catalyst/witness.go`

Same pattern — every `checkFork` call listing post-Osaka forks needs `forks.Bogota` appended:

- `ForkchoiceUpdatedWithWitnessV3`
- `NewPayloadWithWitnessV3` (the one requiring cancun/prague/osaka)
- `NewPayloadWithWitnessV4` (the one requiring prague/osaka)
- `ExecuteStatelessPayloadV4` (the one requiring prague/osaka)

## 5. `eth/catalyst/simulated_beacon.go`

- In `payloadVersion()`, add `forks.Bogota` to the case list that returns `engine.PayloadV3` (the line with `forks.BPO5, forks.BPO4, ...`).

---

## Files that need NO changes (auto-discovered via reflection or use fork-agnostic patterns)

- `core/forkid/forkid.go` — uses reflection on `ChainConfig` field names ending in `Time`/`Block`, so it auto-discovers `BogotaTime`.
- `core/vm/evm.go`, `core/vm/jump_table_export.go`, `core/vm/contracts.go` — Bogota will have new opcodes/precompiles, so create empty new instruction set. Add `IsBogota` case as highest.
- `core/vm/jump_table.go` — add empty new instruction set based on amsterdam.
- `consensus/misc/eip4844/eip4844.go` — the `latestBlobConfig` switch and `CalcExcessBlobGas` don't need Bogota entries until it has distinct blob params.
- `core/state_transition.go`, `core/block_validator.go`, `core/txpool/`, `miner/worker.go`, `internal/ethapi/` — no Bogota-specific logic yet.
- `core/types/transaction_signing.go` — no new tx types in Bogota.

---

## Verification

After making all changes, run:
```bash
go build ./...
```
The build must succeed with no errors. The fork should be structurally present but dormant (nil timestamps on all real networks).
