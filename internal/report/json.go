package report

import (
	"encoding/json"
	"io"
)

// WriteJSON renders the complete report as indented JSON. The output is
// deterministic: the same report always produces the same bytes.
func WriteJSON(w io.Writer, r *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false) // keep "->" and "<" readable in reasons
	return enc.Encode(r)
}
