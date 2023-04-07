package tshttp

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"path/filepath"

	"go.seankhliao.com/svcrunner/v2/observability"
	"golang.org/x/exp/slog"
	"tailscale.com/tsnet"
)

type Config struct {
	Address string
	Dir     string
	O       observability.Config
}

func (c *Config) SetFlags(f *flag.FlagSet) {
	c.O.SetFlags(f)
	f.StringVar(&c.Address, "listen.address", ":8080", "server address, 'funnel', 'tsnet', 'tsnet+tls', ':8080'")
	f.StringVar(&c.Dir, "state.dir", "", "state directory")
}

type Server struct {
	O   *observability.O
	Mux *http.ServeMux

	http *http.Server
	ts   *tsnet.Server
}

func New(ctx context.Context, c Config) *Server {
	o := observability.New(c.O)
	tslog := o.L.WithGroup("tsnet")
	mux := http.NewServeMux()
	s := &Server{
		O:   o,
		Mux: mux,
		http: &http.Server{
			Addr:     c.Address,
			Handler:  mux,
			ErrorLog: slog.NewLogLogger(o.H.WithGroup("nethttp"), slog.LevelWarn),
		},
		ts: &tsnet.Server{
			Hostname:  o.N,
			Ephemeral: true,
			Dir:       filepath.Join(c.Dir, "ts"),
			Logf: func(f string, args ...any) {
				tslog.LogAttrs(ctx, slog.LevelDebug, "tsnet",
					slog.String("msg", fmt.Sprintf(f, args...)))
			},
		},
	}

	return s
}

func (s *Server) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.O.L.LogAttrs(ctx, slog.LevelInfo, "shutting down",
			slog.String("reason", context.Cause(ctx).Error()),
		)
		err := s.http.Shutdown(context.Background())
		if err != nil {
			s.O.Err(ctx, "unclean shutdown", err)
		}
	}()

	var lis net.Listener
	var err error
	s.O.L.LogAttrs(ctx, slog.LevelInfo, "starting listen", slog.String("address", s.http.Addr))
	switch s.http.Addr {
	case "funnel":
		lis, err = s.ts.ListenFunnel("tcp", ":443")
		if err != nil {
			return s.O.Err(ctx, "listen tailscale funnel", err)
		}
	case "tsnet+tls":
		lis, err = s.ts.ListenTLS("tcp", ":443")
		if err != nil {
			return s.O.Err(ctx, "listen tailscale funnel", err)
		}
	case "tsnet":
		lis, err = s.ts.Listen("tcp", ":80")
		if err != nil {
			return s.O.Err(ctx, "listen tailscale funnel", err)
		}
	default:
		lis, err = net.Listen("tcp", s.http.Addr)
		if err != nil {
			return s.O.Err(ctx, "listen locally", err)
		}
	}

	s.O.L.LogAttrs(ctx, slog.LevelInfo, "starting server")
	err = s.http.Serve(lis)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return s.O.Err(ctx, "unexpected server shutdown", err)
	}
	return nil
}
