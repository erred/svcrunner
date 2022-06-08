package svcrunner

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"go.seankhliao.com/gchat"
	"go.seankhliao.com/svcrunner/envflag"
)

type Tools struct {
	// logging to stdout
	logfmt    string
	verbosity int
	Log       logr.Logger

	// also log errors to workspace
	gchatEndpoint string
	gchat         *gchat.WebhookClient
}

func (t *Tools) register(c *envflag.Config) {
	c.StringVar(&t.logfmt, "log.format", "json", "log output format: text|json|json+gcp")
	c.IntVar(&t.verbosity, "log.verbosity", 0, "log verbosity [error|notice|info|debug]: -1|0|1|2")
	c.StringVar(&t.gchatEndpoint, "log.errors-gchat", "", "log errors to google chat (only for json+gcp): $webhook_url")
}

func (t *Tools) init(out io.Writer) error {
	if t.gchatEndpoint != "" {
		t.gchat = &gchat.WebhookClient{
			Client:   http.DefaultClient,
			Endpoint: t.gchatEndpoint,
		}
	}

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
			Verbosity:       t.verbosity,
			LogTimestamp:    true,
			TimestampFormat: time.RFC3339,
		})
	case "json+gcp":
		t.Log = funcr.NewJSON(func(obj string) {
			fmt.Fprintln(out, obj)
		}, funcr.Options{
			Verbosity: t.verbosity,
			RenderBuiltinsHook: func(kvList []any) []any {
				out := kvListToGCPLog(kvList)
				if t.gchat != nil {
					gchatReport(t.gchat, out)
				}
				return out
			},
		})
	default:
		return fmt.Errorf("unknown log format: %v", t.logfmt)
	}
	return nil
}

func gchatReport(client *gchat.WebhookClient, kvList []any) {
	var found bool
	for i := 0; i < len(kvList); i += 2 {
		if kvList[i].(string) == "error" {
			found = true
			break
		}
	}
	if !found {
		return
	}
	var buf strings.Builder
	for i := 0; i < len(kvList); i += 2 {
		fmt.Fprintf(&buf, "%s=%v ", kvList[i], kvList[i+1])
	}
	client.Post(context.Background(), gchat.WebhookPayload{
		Text: buf.String(),
	})
}

func kvListToGCPLog(kvList []any) []any {
	out := make([]any, 0, len(kvList)+2)
	out = append(out, "severity", "ERROR")
	for i := 0; i < len(kvList); i += 2 {
		switch kvList[i].(string) {
		case "level":
			switch kvList[i+1].(int) {
			case 0:
				out[1] = "NOTICE"
			case 1:
				out[1] = "INFO"
			case 2:
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
}
