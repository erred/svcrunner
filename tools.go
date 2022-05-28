package svcrunner

import (
	"fmt"
	"io"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/iand/logfmtr"
	"go.seankhliao.com/svcrunner/envflag"
)

type Tools struct {
	logfmt    string
	verbosity int
	Log       logr.Logger
}

func (t *Tools) register(c *envflag.Config) {
	c.StringVar(&t.logfmt, "log.format", "json", "log output format: text|json")
	c.IntVar(&t.verbosity, "log.verbosity", 0, "log verbosity: 0-2")
}

func (t *Tools) init(out io.Writer) error {
	switch t.logfmt {
	case "text":
		t.Log = logfmtr.NewWithOptions(logfmtr.Options{
			Writer: out,
		})
		logfmtr.SetVerbosity(t.verbosity)
	case "json":
		t.Log = funcr.NewJSON(func(obj string) {
			fmt.Fprintln(out, obj)
		}, funcr.Options{
			Verbosity: t.verbosity,
		})
	default:
		return fmt.Errorf("unknown log format: %v", t.logfmt)
	}
	return nil
}
