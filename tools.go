package svcrunner

import (
	"fmt"
	"io"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"go.seankhliao.com/svcrunner/envflag"
)

type Tools struct {
	logfmt    string
	verbosity int
	Log       logr.Logger
}

func (t *Tools) register(c *envflag.Config) {
	c.StringVar(&t.logfmt, "log.format", "json", "log output format: text|json")
	c.IntVar(&t.verbosity, "log.verbosity", 0, "log verbosity (error/info/debug/trace): -1 - 2")
}

func (t *Tools) init(out io.Writer) error {
	switch t.logfmt {
	case "text":
		t.Log = funcr.New(func(prefix, args string) {
			fmt.Fprintln(out, prefix, args)
		}, funcr.Options{
			LogTimestamp:    true,
			TimestampFormat: time.RFC3339,
			Verbosity:       t.verbosity,
		})
	case "json":
		t.Log = funcr.NewJSON(func(obj string) {
			fmt.Fprintln(out, obj)
		}, funcr.Options{
			Verbosity: t.verbosity,
		})
	case "json+gcp":
		t.Log = funcr.NewJSON(func(obj string) {
			fmt.Fprintln(out, obj)
		}, funcr.Options{
			Verbosity: t.verbosity,
			RenderBuiltinsHook: func(kvList []any) []any {
				out := make([]any, 0, len(kvList)+2)
				out = append(out, "severity", "ERROR")
				for i := 0; i < len(kvList); i += 2 {
					switch kvList[i].(string) {
					case "level":
						switch kvList[i+1].(int) {
						case 0:
							out[1] = "INFO"
						case 1, 2:
							out[1] = "DEBUG"
						default:
							out[1] = "DEFAULT"
						}
					case "msg":
						out = append(out, "message", kvList[i+1])
					default:
						out = append(out, kvList[i], kvList[i+1])
					}
				}

				return out
			},
		})
	default:
		return fmt.Errorf("unknown log format: %v", t.logfmt)
	}
	return nil
}
