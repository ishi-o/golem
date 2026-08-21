package app

import (
	"encoding/json"
	"io"
)

func jsonEncoder(w io.Writer, value any) error {
	return json.NewEncoder(w).Encode(value)
}
