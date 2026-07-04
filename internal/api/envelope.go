package api

import (
	"encoding/json"
	"fmt"
	"io"
)

type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// decodeEnvelope reads the {code,msg,data} wrapper; a non-zero code is an error.
// When out is non-nil, data is unmarshaled into it.
func decodeEnvelope(r io.Reader, out any) error {
	var e envelope
	if err := json.NewDecoder(r).Decode(&e); err != nil {
		return fmt.Errorf("decode envelope: %w", err)
	}
	if e.Code != 0 {
		return fmt.Errorf("api error %d: %s", e.Code, e.Msg)
	}
	if out != nil {
		return json.Unmarshal(e.Data, out)
	}
	return nil
}
