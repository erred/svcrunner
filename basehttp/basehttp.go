package basehttp

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"

	"go.seankhliao.com/svcrunner/v2/observability"
)

type Config struct {
	Address string
	O       observability.Config
}

func (c *Config) SetFlags(f *flag.FlagSet) {
	c.O.SetFlags(f)
	f.StringVar(&c.Address, "listen.address", ":8080", "server address")
}

type Server struct {
	O   *observability.O
	Mux *http.ServeMux

	http *http.Server
}

func New(ctx context.Context, c *Config) *Server {
	o := observability.New(c.O)
	mux := http.NewServeMux()
	return &Server{
		O:   o,
		Mux: mux,
		http: &http.Server{
			Addr:     c.Address,
			Handler:  mux,
			ErrorLog: slog.NewLogLogger(o.H.WithGroup("nethttp"), slog.LevelWarn),
		},
	}
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

	s.O.L.LogAttrs(ctx, slog.LevelInfo, "starting listen", slog.String("address", s.http.Addr))
	lis, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return s.O.Err(ctx, "listen locally", err)
	}

	s.O.L.LogAttrs(ctx, slog.LevelInfo, "starting server")
	err = s.http.Serve(lis)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return s.O.Err(ctx, "unexpected server shutdown", err)
	}
	return nil
}
