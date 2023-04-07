package observability

import (
	"context"
	"flag"
	"io"
	"os"
	"runtime/debug"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/exp/slog"
)

type Config struct {
	LogOutput io.Writer
	LogLevel  slog.Level
}

func (c *Config) SetFlags(f *flag.FlagSet) {
	f.TextVar(&c.LogLevel, "log.level", slog.LevelInfo, "log level: debug|info|warn|error")
}

type O struct {
	N string
	L *slog.Logger
	H slog.Handler
	T trace.Tracer
	M metric.Meter
}

func New(c Config) *O {
	o := &O{}

	bi, _ := debug.ReadBuildInfo()
	fullname := bi.Main.Path
	o.N = "earbug"

	defer func() {
		// always set instrumentation, even if they may be noops
		o.T = otel.Tracer(fullname)
		o.M = otel.Meter(fullname)
	}()

	logOptions := &slog.HandlerOptions{
		Level: c.LogLevel,
	}
	out := c.LogOutput
	if out == nil {
		out = os.Stdout
	}
	o.H = logOptions.NewJSONHandler(out)
	o.L = slog.New(o.H)

	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		ctx := context.Background()

		otelLog := o.L.WithGroup("otel")
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			otelLog.LogAttrs(ctx, slog.LevelWarn, "otel error")
		}))

		te, err := otlptracegrpc.New(ctx)
		if err != nil {
			otelLog.LogAttrs(ctx, slog.LevelError, "create trace exporter",
				slog.String("error", err.Error()),
			)
			return o
		}
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(te),
		)
		otel.SetTracerProvider(tp)
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.Baggage{},
			propagation.TraceContext{},
		))

		me, err := otlpmetricgrpc.New(ctx)
		if err != nil {
			otelLog.LogAttrs(ctx, slog.LevelError, "create metric exporter",
				slog.String("error", err.Error()),
			)
			return o
		}
		mp := sdkmetric.NewMeterProvider(
			sdkmetric.WithReader(
				sdkmetric.NewPeriodicReader(me),
			),
			// https://github.com/open-telemetry/opentelemetry-go-contrib/issues/3071
			sdkmetric.WithView(sdkmetric.NewView(sdkmetric.Instrument{
				Scope: instrumentation.Scope{
					Name: "go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp",
				},
			}, sdkmetric.Stream{
				AttributeFilter: attribute.Filter(func(kv attribute.KeyValue) bool {
					switch kv.Key {
					case "net.sock.peer.addr", "net.sock.peer.port":
						return false
					default:
						return true
					}
				}),
			})),
		)
		otel.SetMeterProvider(mp)
	}

	return o
}
