package jsonlog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"testing/slogtest"
)

func TestHandler(t *testing.T) {
	buf := new(bytes.Buffer)
	handler := New(slog.LevelInfo, buf)
	err := slogtest.TestHandler(handler, func() []map[string]any {
		all := buf.String()
		dec := json.NewDecoder(buf)
		var results []map[string]any
		for dec.More() {
			var result map[string]any
			err := dec.Decode(&result)
			if err != nil {
				t.Errorf("unmarshal log: %v\n%v", err, all)
				break
			}
			results = append(results, result)
		}
		return results
	})
	if err != nil {
		t.Errorf("testhandler: %v", err)
	}
}

func BenchmarkHandler(b *testing.B) {
	lg := slog.New(New(slog.LevelDebug, io.Discard))
	for i := 0; i < b.N; i++ {
		lg.LogAttrs(context.Background(), slog.LevelInfo, "this is a test message", slog.Int("aaa", 1), slog.Bool("bbb", true), slog.String("ddd", "zzzzzz"))
	}
}

func FuzzHandler(f *testing.F) {
	f.Fuzz(func(t *testing.T, lines uint8, level, level2 int, nargs uint64, i1, i2, i3, i4, i5, i6, i7, i8, i9, i0, msg string) {
		strs := []string{i0, i1, i2, i3, i4, i5, i6, i7, i8, i9}
		buf := new(bytes.Buffer)
		lg := slog.New(New(slog.Level(level), buf))
		fmt.Fprintln(os.Stderr, lines, level, level2, nargs, msg)
		for i := uint8(0); i < lines; i++ {
			nlg := lg
			nargs := nargs * uint64(lines)
			var args []any
			for nargs > 0 {
				switch nargs % 6 {
				case 0:
					nlg = nlg.With(args...)
					args = nil
				case 1:
					nlg = nlg.WithGroup(strs[nargs%10])
				case 2:
					args = append(args, strs[nargs%10], strs[(nargs*7)%10])
				case 4:
					args = append(args, strs[nargs%10], nargs)
				case 5:
					args = append(args, strs[nargs%10], nargs%2 == 0)
				case 6:
					lop := int(nargs) % (len(args) / 2)
					args = append(args[:lop*2], strs[nargs%10], slog.Group(strs[(nargs*13)%10], args[lop*2:]...))
				}
				nargs /= 6
			}
			nlg.Log(context.Background(), slog.Level(level2), msg, args...)
		}

		all := buf.String()
		fmt.Fprintln(os.Stderr, all)
		dec := json.NewDecoder(buf)
		for dec.More() {
			var out any
			err := dec.Decode(&out)
			if err != nil {
				t.Error(err, all)
			}
		}
	})
}
