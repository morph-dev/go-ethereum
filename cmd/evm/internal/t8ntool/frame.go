package t8ntool

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/urfave/cli/v2"
)

func FrameAction(ctx *cli.Context) error {
	var (
		chainId  = ctx.Int64(ChainIDFlag.Name)
		inputTxs = ctx.String(InputTxsFlag.Name)

		txsWithKeys []*txWithKey
		ioReader    io.Reader
		err         error
	)

	if inputTxs == stdinSelector {
		ioReader = os.Stdin
	} else {
		if ioReader, err = os.Open(inputTxs); err != nil {
			return err
		}
	}

	if err = json.NewDecoder(ioReader).Decode(&txsWithKeys); err != nil {
		return err
	}

	signer := types.LatestSignerForChainID(big.NewInt(chainId))
	txs, err := signUnsignedTransactions(txsWithKeys, signer)

	for _, tx := range txs {
		out, err := json.MarshalIndent(tx, "", "  ")
		if err != nil {
			return nil
		}
		rlp, err := tx.MarshalBinary()
		if err != nil {
			return err
		}
		fmt.Printf("signHash = %v\n%v\nrlp = %v\n\n", signer.Hash(tx), string(out), hexutil.Encode(rlp))
	}

	return nil
}
