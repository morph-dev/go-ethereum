package t8ntool

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"text/template"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

type verifyFrame struct {
	index        uint
	dataTemplate *template.Template
	secp256k1    *ecdsa.PrivateKey
	p256         *ecdsa.PrivateKey
}

func (vf *verifyFrame) UnmarshalJSON(input []byte) error {
	type verifyFrameData struct {
		Index        *uint          `json:"index"`
		DataTemplate *string        `json:"dataTemplate"`
		Curve        *string        `json:"curve"`
		Key          *hexutil.Bytes `json:"key"`
	}
	var data verifyFrameData
	if err := json.Unmarshal(input, &data); err != nil {
		return err
	}

	if data.Curve == nil {
		return fmt.Errorf("curve is not present, 'secp256k1' and 'p256' are supported")
	}
	if data.Key == nil || len(*data.Key) != 32 {
		return fmt.Errorf("key is not present or it doesn't have 32 bytes")
	}

	switch *data.Curve {
	case "secp256k1":
		if secp256k1, err := crypto.ToECDSA(*data.Key); err != nil {
			return err
		} else {
			vf.secp256k1 = secp256k1
		}
	case "p256":
		curve := elliptic.P256()
		d := new(big.Int).SetBytes(*data.Key)
		x, y := curve.ScalarBaseMult(d.Bytes())
		vf.p256 = &ecdsa.PrivateKey{
			D: d,
			PublicKey: ecdsa.PublicKey{
				Curve: curve,
				X:     x,
				Y:     y,
			},
		}
	default:
		return fmt.Errorf("unknown curve %v, only 'secp256k1' and 'p256' are supported", data.Curve)
	}

	if data.Index == nil {
		return fmt.Errorf("missing `index` for verify frame")
	} else {
		vf.index = *data.Index
	}
	if data.DataTemplate == nil {
		return fmt.Errorf("missing `DataTemplate` for verify frame")
	} else {
		tpl, err := template.New("").Parse(*data.DataTemplate)
		if err != nil {
			return fmt.Errorf("error parsing `DataTemplate` %w", err)
		}
		vf.dataTemplate = tpl
	}
	return nil
}

type sigData struct {
	R, S, V, Qx, Qy string
}

func (vf *verifyFrame) data(sigData sigData) ([]byte, error) {
	buffer := new(bytes.Buffer)
	if err := vf.dataTemplate.Execute(buffer, sigData); err != nil {
		return nil, err
	}
	return hexutil.Decode(buffer.String())
}

// Signs corresponding frame of transaction, and sets frame's data.
//
// If data only has signature, signs "sign_hash". Otherwise, signs "keccak(sign_hash + data_without_sig)".
func (vf *verifyFrame) sign(signer types.Signer, tx *types.Transaction) error {
	dataWithoutSig, err := vf.data(sigData{})
	if err != nil {
		return err
	}

	signHash := signer.Hash(tx)
	message := signHash.Bytes()
	if len(dataWithoutSig) > 0 {
		message = append(message, dataWithoutSig...)
		message = crypto.Keccak256(message)
	}

	sigData := sigData{}
	switch {
	case vf.secp256k1 != nil:
		sig, err := crypto.Sign(message, vf.secp256k1)
		if err != nil {
			return err
		}
		r, s, v, err := signer.SignatureValues(tx, sig)
		if err != nil {
			return err
		}
		sigData.R = fmt.Sprintf("%064x", r)
		sigData.S = fmt.Sprintf("%064x", s)
		sigData.V = fmt.Sprintf("%02x", v)
	case vf.p256 != nil:
		r, s, err := ecdsa.Sign(rand.Reader, vf.p256, message)
		if err != nil {
			return nil
		}
		sigData.R = fmt.Sprintf("%064x", r)
		sigData.S = fmt.Sprintf("%064x", s)
		sigData.Qx = fmt.Sprintf("%064x", vf.p256.PublicKey.X)
		sigData.Qy = fmt.Sprintf("%064x", vf.p256.PublicKey.Y)
	default:
		return fmt.Errorf("unknown signing key scheme")
	}

	data, err := vf.data(sigData)
	if err != nil {
		return err
	}

	return tx.SetFrameData(vf.index, data)
}
