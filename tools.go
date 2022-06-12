package svcrunner

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"cloud.google.com/go/compute/metadata"
	cloudtrace "github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/trace"
	gcppropagator "github.com/GoogleCloudPlatform/opentelemetry-operations-go/propagator"
	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"go.opentelemetry.io/contrib/detectors/gcp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.10.0"
	"go.opentelemetry.io/otel/trace"
	"go.seankhliao.com/gchat"
	"go.seankhliao.com/svcrunner/envflag"
)

type Tools struct {
	Log logr.Logger

	// logging
	logfmt        string
	verbosity     int
	gchatEndpoint string // also log errors to workspace
	// tracing
	traceExport string
}

func (t *Tools) register(c *envflag.Config) {
	c.StringVar(&t.logfmt, "log.format", "json", "log output format: text|json|json+gcp")
	c.IntVar(&t.verbosity, "log.verbosity", 0, "log verbosity [error|notice|info|debug]: -1|0|1|2")
	c.StringVar(&t.gchatEndpoint, "log.errors-gchat", "", "log errors to google chat (only for json+gcp): $webhook_url")
	c.StringVar(&t.traceExport, "trace.export", "cloudtrace", "enable tracing")
}

func (t *Tools) init(out io.Writer) error {
	// tracing
	err := traceExporter(t.traceExport)
	if err != nil {
		return fmt.Errorf("setup trace exporter: %w", err)
	}

	// logging
	t.Log, err = logExporter(t.logfmt, t.verbosity, out, t.gchatEndpoint)
	if err != nil {
		return fmt.Errorf("setup log exporter: %w", err)
	}

	return nil
}

func gchatReport(client *gchat.WebhookClient, obj string) {
	if !strings.Contains(obj, "ERROR") {
		return
	}
	client.Post(context.Background(), gchat.WebhookPayload{
		Text: obj,
	})
}

func kvListToGCPLog(kvList []any, addSeverity bool, projectID string) []any {
	out := make([]any, 0, len(kvList)+2)
	if addSeverity {
		out = append(out, "severity", "ERROR")
	}
	for i := 0; i < len(kvList); i += 2 {
		// note: RenderBuiltinsHook only works for logr/funcr builtin key/values
		switch kvList[i].(string) {
		case "ctx":
			ctx, ok := kvList[i+1].(context.Context)
			if !ok {
				out = append(out, kvList[i], kvList[i+1])
				continue
			}
			spanCtx := trace.SpanContextFromContext(ctx)
			out = append(out,
				"logging.googleapis.com/trace", "projects/"+projectID+"/traces/"+spanCtx.TraceID().String(),
				"logging.googleapis.com/spanId", spanCtx.SpanID().String(),
				"logging.googleapis.com/trace_sampled", spanCtx.IsSampled(),
			)
		case "http_request":
			req, ok := kvList[i+1].(*http.Request)
			if !ok {
				out = append(out, kvList[i], kvList[i+1])
				continue
			}
			req.URL.Scheme = "http"
			if req.TLS != nil {
				req.URL.Scheme = "https"
			}
			req.URL.Host = req.Host

			out = append(out, "httpRequest", map[string]any{
				"requestMethod": req.Method,
				"requestUrl":    req.URL.String(),
				"userAgent":     req.UserAgent(),
				"remoteIp":      req.RemoteAddr,
				"referer":       req.Referer(),
				"protocol":      req.Proto,
			})
		case "level":
			if addSeverity {
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
			}
		case "msg":
			out = append(out, "message", kvList[i+1])
		default:
			out = append(out, kvList[i], kvList[i+1])
		}
	}
	return out
}

func logExporter(format string, verbosity int, out io.Writer, gchatEndpoint string) (logr.Logger, error) {
	var chat *gchat.WebhookClient
	if gchatEndpoint != "" {
		chat = &gchat.WebhookClient{
			Client: &http.Client{
				Transport: otelhttp.NewTransport(nil),
			},
			Endpoint: gchatEndpoint,
		}
	}

	var log logr.Logger
	switch format {
	case "text":
		log = funcr.New(func(prefix, args string) {
			fmt.Fprintln(out, prefix, args)
		}, funcr.Options{
			LogTimestamp:    true,
			TimestampFormat: time.RFC3339,
			Verbosity:       verbosity,
		})

	case "json":
		log = funcr.NewJSON(func(obj string) {
			fmt.Fprintln(out, obj)
		}, funcr.Options{
			Verbosity:       verbosity,
			LogTimestamp:    true,
			TimestampFormat: time.RFC3339,
		})
	case "json+gcp":
		projectID, err := metadata.ProjectID()
		if err != nil {
			return log, fmt.Errorf("get google project id from metadata: %w", err)
		}
		log = funcr.NewJSON(func(obj string) {
			fmt.Fprintln(out, obj)
			if chat != nil {
				gchatReport(chat, obj)
			}
		}, funcr.Options{
			Verbosity: verbosity,
			RenderBuiltinsHook: func(kvList []interface{}) []interface{} {
				return kvListToGCPLog(kvList, true, projectID)
			},
			RenderValuesHook: func(kvList []interface{}) []interface{} {
				return kvListToGCPLog(kvList, false, projectID)
			},
			RenderArgsHook: func(kvList []interface{}) []interface{} {
				return kvListToGCPLog(kvList, false, projectID)
			},
		})
	default:
		return logr.Logger{}, fmt.Errorf("unknown log format: %v", format)
	}
	return log, nil
}

func traceExporter(exporter string) error {
	var tpOpts []sdktrace.TracerProviderOption
	switch exporter {
	case "cloudtrace":
		exporter, err := cloudtrace.New()
		if err != nil {
			return fmt.Errorf("create google cloud trace exporter: %w", err)
		}

		tpOpts = append(tpOpts, sdktrace.WithSyncer(exporter))
	case "stdout":
		exporter, err := stdouttrace.New()
		if err != nil {
			return fmt.Errorf("create stdout trace exporter: %w", err)
		}
		tpOpts = append(tpOpts, sdktrace.WithSyncer(exporter))
	default:
		return nil
	}

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Errorf("failed to read buildinfo")
	}
	ctx := context.TODO()
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithDetectors(gcp.NewCloudRun()),
		resource.WithAttributes(
			semconv.ServiceNameKey.String(bi.Path),
		),
	)
	if err != nil {
		return fmt.Errorf("setup otel resource detectors: %w", err)
	}

	tpOpts = append(tpOpts, sdktrace.WithResource(res))

	tp := sdktrace.NewTracerProvider(tpOpts...)
	otel.SetTracerProvider(tp)
	// TODO: tp.Shutdown

	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			// Putting the CloudTraceOneWayPropagator first means the TraceContext propagator
			// takes precedence if both the traceparent and the XCTC headers exist.
			gcppropagator.CloudTraceOneWayPropagator{},
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	return nil
}
