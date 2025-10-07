package bal

import (
	"encoding/json"
	"fmt"
)

func (c *ContractCode) MarshalJSON() ([]byte, error) {
	hexStr := fmt.Sprintf("%x", *c)
	return json.Marshal(hexStr)
}
func (e encodingBalanceChange) MarshalJSON() ([]byte, error) {
	type Alias encodingBalanceChange
	return json.Marshal(&struct {
		TxIdx string `json:"txIndex"`
		*Alias
	}{
		TxIdx: fmt.Sprintf("0x%x", e.TxIdx),
		Alias: (*Alias)(&e),
	})
}

func (e *encodingBalanceChange) UnmarshalJSON(data []byte) error {
	type Alias encodingBalanceChange
	aux := &struct {
		TxIdx string `json:"txIndex"`
		*Alias
	}{
		Alias: (*Alias)(e),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if len(aux.TxIdx) >= 2 && aux.TxIdx[:2] == "0x" {
		if _, err := fmt.Sscanf(aux.TxIdx, "0x%x", &e.TxIdx); err != nil {
			return err
		}
	}
	return nil
}
func (e encodingAccountNonce) MarshalJSON() ([]byte, error) {
	type Alias encodingAccountNonce
	return json.Marshal(&struct {
		TxIdx string `json:"txIndex"`
		Nonce string `json:"nonce"`
		*Alias
	}{
		TxIdx: fmt.Sprintf("0x%x", e.TxIdx),
		Nonce: fmt.Sprintf("0x%x", e.Nonce),
		Alias: (*Alias)(&e),
	})
}

func (e *encodingAccountNonce) UnmarshalJSON(data []byte) error {
	type Alias encodingAccountNonce
	aux := &struct {
		TxIdx string `json:"txIndex"`
		Nonce string `json:"nonce"`
		*Alias
	}{
		Alias: (*Alias)(e),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if len(aux.TxIdx) >= 2 && aux.TxIdx[:2] == "0x" {
		if _, err := fmt.Sscanf(aux.TxIdx, "0x%x", &e.TxIdx); err != nil {
			return err
		}
	}
	if len(aux.Nonce) >= 2 && aux.Nonce[:2] == "0x" {
		if _, err := fmt.Sscanf(aux.Nonce, "0x%x", &e.Nonce); err != nil {
			return err
		}
	}
	return nil
}
