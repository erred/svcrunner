package svcrunner

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	cloudtrace "github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/trace"
	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"go.opentelemetry.io/contrib/detectors/gcp"
	"go.opentelemetry.io/contrib/instrumentation/host"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric/global"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.10.0"
	"go.opentelemetry.io/otel/trace"
	"go.seankhliao.com/gchat"
	"go.seankhliao.com/svcrunner/envflag"
	"google.golang.org/api/idtoken"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/oauth"
)

type Tools struct {
	Log logr.Logger

	otlpAudience string

	// logging
	logfmt        string
	verbosity     int
	gchatEndpoint string // also log errors to workspace
	// tracing
	traceExport  string
	metricExport string
}

func (t *Tools) register(c *envflag.Config) {
	c.StringVar(&t.logfmt, "log.format", "json", "log output format: text|json|json+gcp")
	c.IntVar(&t.verbosity, "log.verbosity", 0, "log verbosity [error|notice|info|debug]: -1|0|1|2")
	c.StringVar(&t.gchatEndpoint, "log.errors-gchat", "", "log errors to google chat (only for json+gcp): $webhook_url")
	c.StringVar(&t.traceExport, "trace.export", "otlp", "enable tracing")
	c.StringVar(&t.metricExport, "metric.export", "otlp", "enable metrics")
	c.StringVar(&t.otlpAudience, "otlp.audience", "", "use oidc with the given audience")
}

func (t *Tools) init(out io.Writer) error {
	var err error

	// logging
	t.Log, err = logExporter(t.logfmt, t.verbosity, out, t.gchatEndpoint)
	if err != nil {
		return fmt.Errorf("setup log exporter: %w", err)
	}
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		t.Log.WithName("otel").Error(err, "otel error")
	}))

	// tracing
	err = traceExporter(t.traceExport, t.otlpAudience)
	if err != nil {
		return fmt.Errorf("setup trace exporter: %w", err)
	}

	// metrics
	err = metricExporter(t.metricExport, t.otlpAudience)
	if err != nil {
		return fmt.Errorf("setup metric exporter: %w", err)
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

func kvListToGCPLog(kvList []any, addSeverity bool) []any {
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
				"logging.googleapis.com/trace", spanCtx.TraceID().String(),
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
		log = funcr.NewJSON(func(obj string) {
			fmt.Fprintln(out, obj)
			if chat != nil {
				gchatReport(chat, obj)
			}
		}, funcr.Options{
			Verbosity: verbosity,
			RenderBuiltinsHook: func(kvList []interface{}) []interface{} {
				return kvListToGCPLog(kvList, true)
			},
			RenderValuesHook: func(kvList []interface{}) []interface{} {
				return kvListToGCPLog(kvList, false)
			},
			RenderArgsHook: func(kvList []interface{}) []interface{} {
				return kvListToGCPLog(kvList, false)
			},
		})
	default:
		return logr.Logger{}, fmt.Errorf("unknown log format: %v", format)
	}
	return log, nil
}

func traceExporter(exportType, audience string) error {
	var tpOpts []sdktrace.TracerProviderOption
	switch exportType {
	case "cloudtrace":
		exporter, err := cloudtrace.New()
		if err != nil {
			return fmt.Errorf("create google cloud trace exporter: %w", err)
		}

		tpOpts = append(tpOpts, sdktrace.WithSyncer(exporter))
	case "otlp":
		ctx := context.Background()
		var dialOpts []grpc.DialOption
		if audience != "" {
			gcpTS, err := idtoken.NewTokenSource(ctx, audience)
			if err != nil {
				return fmt.Errorf("create grpc idtoken source: %w", err)
			}
			dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})))
			dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(&oauth.TokenSource{TokenSource: gcpTS}))
		}

		exporter, err := otlptracegrpc.New(ctx,
			otlptracegrpc.WithDialOption(dialOpts...),
			otlpmetricgrpc.WithEndpoint(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
		)
		if err != nil {
			return fmt.Errorf("create otlpgrpc trace exporter: %w", err)
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

	res, err := createResource()
	if err != nil {
		return err
	}

	tpOpts = append(tpOpts, sdktrace.WithResource(res))

	tp := sdktrace.NewTracerProvider(tpOpts...)
	otel.SetTracerProvider(tp)
	// TODO: tp.Shutdown

	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	return nil
}

func metricExporter(exportType, audience string) error {
	var mpOpts []sdkmetric.Option
	switch exportType {
	case "otlp":
		ctx := context.Background()
		var dialOpts []grpc.DialOption
		if audience != "" {
			gcpTS, err := idtoken.NewTokenSource(ctx, audience)
			if err != nil {
				return fmt.Errorf("create grpc idtoken source: %w", err)
			}
			dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})))
			dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(&oauth.TokenSource{TokenSource: gcpTS}))
		}

		exporter, err := otlpmetricgrpc.New(ctx,
			otlpmetricgrpc.WithDialOption(dialOpts...),
			otlpmetricgrpc.WithEndpoint(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
		)
		if err != nil {
			return fmt.Errorf("create otlpgrpc metric exporter: %w", err)
		}
		mpOpts = append(mpOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(5*time.Second))))
	default:
		return nil
	}

	res, err := createResource()
	if err != nil {
		return err
	}
	mpOpts = append(mpOpts, sdkmetric.WithResource(res))

	mp := sdkmetric.NewMeterProvider(mpOpts...)
	global.SetMeterProvider(mp)

	host.Start()
	runtime.Start()

	return nil
}

var (
	otelResourceErr  error
	otelResource     *resource.Resource
	otelResourceOnce sync.Once
)

func createResource() (*resource.Resource, error) {
	var err error
	otelResourceOnce.Do(func() {
		bi, ok := debug.ReadBuildInfo()
		if !ok {
			otelResourceErr = fmt.Errorf("failed to read buildinfo")
			return
		}
		version := bi.Main.Version
		if version == "(devel)" {
			var t time.Time
			r, d := "000000000000", ""
			for _, setting := range bi.Settings {
				switch setting.Key {
				case "vcs.time":
					t, _ = time.Parse(time.RFC3339, setting.Value)
				case "vcs.revision":
					r = setting.Value
				case "vcs.modified":
					if setting.Value == "true" {
						d = "-dirty"
					}
				}
			}
			version = "v0.0.0-" + t.Format("20060102150405") + "-" + r[:12] + d
		}

		ctx := context.TODO()
		otelResource, otelResourceErr = resource.New(ctx,
			resource.WithFromEnv(),
			resource.WithTelemetrySDK(),
			resource.WithDetectors(gcp.NewCloudRun()),
			resource.WithAttributes(
				semconv.ServiceNameKey.String(bi.Path),
				semconv.ServiceVersionKey.String(version),
			),
		)
		if otelResourceErr != nil {
			err = fmt.Errorf("setup otel resource detectors: %w", err)
			return
		}
	})
	return otelResource, otelResourceErr
}
