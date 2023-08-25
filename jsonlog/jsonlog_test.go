package jsonlog

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"testing/slogtest"
)

func TestHandler(t *testing.T) {
	buf := new(bytes.Buffer)
	handler := New(slog.LevelInfo, buf)
	err := slogtest.TestHandler(handler, func() []map[string]any {
		dec := json.NewDecoder(buf)
		var results []map[string]any
		for dec.More() {
			var result map[string]any
			err := dec.Decode(&result)
			if err != nil {
				t.Errorf("unmarshal log: %v", err)
			}
			results = append(results, result)
		}
		return results
	})
	if err != nil {
		t.Errorf("testhandler: %v", err)
	}
}
