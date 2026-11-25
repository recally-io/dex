package outbound

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
)

// decode reads the params of one JSON-RPC call.
func decode(data *jsonv1.RawMessage, value any) error {
	if data == nil {
		return nil
	}
	return json.Unmarshal(*data, value)
}
