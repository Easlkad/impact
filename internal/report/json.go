package report

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
)

// WriteJSON renders the complete report as indented JSON. The output is
// deterministic: the same report always produces the same bytes.
//
// The report is written field by field, and its lists item by item, each
// function's impact path being computed just before the function is
// written. The paths of a deep call graph hold O(n²) IDs in all; this way
// they are never all in memory at once, and neither is the output.
func WriteJSON(w io.Writer, r *Report) error {
	bw := bufio.NewWriter(w)
	jw := jsonWriter{w: bw}
	jw.str("{")
	v := reflect.ValueOf(r).Elem()
	first := true
	for i := 0; i < v.NumField(); i++ {
		field := v.Type().Field(i)
		if !field.IsExported() {
			continue
		}
		if !first {
			jw.str(",")
		}
		first = false
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		jw.str("\n  ")
		jw.value(name, "")
		jw.str(": ")

		fv := v.Field(i)
		if fv.Kind() != reflect.Slice || fv.Len() == 0 {
			jw.value(fv.Interface(), "  ")
			continue
		}
		jw.str("[")
		for j := 0; j < fv.Len(); j++ {
			if j > 0 {
				jw.str(",")
			}
			jw.str("\n    ")
			jw.value(r.withPaths(fv.Index(j).Interface()), "    ")
		}
		jw.str("\n  ]")
	}
	jw.str("\n}\n")
	if jw.err != nil {
		return jw.err
	}
	return bw.Flush()
}

// withPaths returns an item of a list of the report with the impact paths
// of its functions set.
func (r *Report) withPaths(item any) any {
	switch item := item.(type) {
	case Function:
		return r.withPath(item)
	case Endpoint:
		item.Handler = r.withPath(item.Handler)
		return item
	case Worker:
		item.Function = r.withPath(item.Function)
		return item
	case Test:
		item.Function = r.withPath(item.Function)
		return item
	}
	return item
}

// jsonWriter writes JSON values, remembering the first error.
type jsonWriter struct {
	w   *bufio.Writer
	buf bytes.Buffer
	err error
}

func (jw *jsonWriter) str(s string) {
	if jw.err == nil {
		_, jw.err = jw.w.WriteString(s)
	}
}

// value writes v as indented JSON nested at prefix: lines after the first
// start with prefix, as encoding the whole report at once would indent
// them. HTML characters are not escaped, to keep "->" and "<" readable in
// reasons.
func (jw *jsonWriter) value(v any, prefix string) {
	if jw.err != nil {
		return
	}
	jw.buf.Reset()
	enc := json.NewEncoder(&jw.buf)
	enc.SetIndent(prefix, "  ")
	enc.SetEscapeHTML(false)
	if jw.err = enc.Encode(v); jw.err != nil {
		return
	}
	_, jw.err = jw.w.Write(bytes.TrimSuffix(jw.buf.Bytes(), []byte("\n")))
}
