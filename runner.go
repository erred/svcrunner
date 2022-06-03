// svcrunner provides a runner framework for tasks,
// providing hooks for registering flags,
// running initialization, starting, and stopping.
// Registers signal handlers and provides configured observability tools.
package svcrunner

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"go.seankhliao.com/svcrunner/envflag"
)

type Options struct {
	Output  io.Writer
	Args    []string
	Environ []string
	NoExit  bool
}

func (o *Options) init() {
	if o.Output == nil {
		o.Output = os.Stderr
	}
	if o.Args == nil {
		o.Args = os.Args
	}
	if o.Environ == nil {
		o.Environ = os.Environ()
	}
}

func (o Options) Run(procs ...Process) error {
	if len(procs) == 0 {
		return errors.New("svcrunner: no processes configured")
	}
	for i := range procs {
		if procs[i].Name == "" {
			procs[i].Name = strconv.Itoa(i)
		}
	}
	o.init()
	err := run(o, procs)
	if err != nil && o.NoExit {
		return err
	} else if err != nil {
		fmt.Fprintf(o.Output, "Exit with error: %v", err)
		os.Exit(1)
	} else if o.NoExit {
		return nil
	}
	os.Exit(0)
	return nil
}

func run(o Options, procs []Process) error {
	ctx := context.Background() // root
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sigc := make(chan os.Signal, 2)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)

	// handle signals during startup
	startc := make(chan struct{})
	go cancelOnSignal(ctx, sigc, startc, cancel)

	var t Tools

	// register config
	conf := envflag.New("", o.Output)
	t.register(conf)
	for _, proc := range procs {
		if proc.Register == nil {
			continue
		}
		proc.Register(conf)
	}
	err := conf.Parse(o.Args[1:], o.Environ)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return fmt.Errorf("svcrunner parse config: %w", err)
	}
	err = t.init(o.Output)
	if err != nil {
		return fmt.Errorf("svcrunner init tools: %w", err)
	}

	log := t.Log.WithName("svcrunner")

	log.V(1).Info("initializing processes")
	for _, proc := range procs {
		if proc.Init == nil {
			log.V(2).Info("skipping Init", "process", proc.Name)
			continue
		}
		log.V(2).Info("running Init", "process", proc.Name)
		err := proc.Init(ctx, t)
		if err != nil {
			return fmt.Errorf("svcrunner init process %v: %w", proc.Name, err)
		}
	}

	var ctr int
	var errs []error
	errc := make(chan phaseError)

	log.V(1).Info("starting processes")
	for _, proc := range procs {
		if proc.Start == nil {
			log.V(2).Info("skipping Start", "process", proc.Name)
			continue
		}
		log.V(2).Info("running Start", "process", proc.Name)
		ctr++
		go runFunc(ctx, t, proc.Start, proc.Name, "run", errc)
	}

	close(startc) // startup completed

	log.V(2).Info("waiting for interrupt")
	select {
	case sig := <-sigc: // signal during run
		log.V(1).Info("received shutdown signal", "signal", sig)
		cancel()
	case pe := <-errc:
		log.Error(pe.err, "process exited", "process", pe.name, "phase", pe.phase)
		if pe.err != nil {
			errs = append(errs, err)
		}
		ctr--
	}

	ctx = context.Background() // shutdown root
	ctx, cancel = context.WithCancel(ctx)
	defer cancel()

	// handle signal during shutdown
	stopc := make(chan struct{})

	go cancelOnSignal(ctx, sigc, stopc, cancel)

	log.V(2).Info("shutting down processes")
	for _, proc := range procs {
		if proc.Stop == nil {
			log.V(2).Info("skipping Stop", "process", proc.Name)
			continue
		}
		log.V(2).Info("running Stop", "process", proc.Name)
		ctr++
		go runFunc(ctx, t, proc.Stop, proc.Name, "stop", errc)
	}

	log.V(2).Info("waiting for procsses to exit")
countExit:
	for {
		select {
		case sig := <-sigc:
			log.Info("forcing shutdown", "signal", sig)
		case pe := <-errc:
			ctr--
			if pe.err != nil {
				log.Error(err, "process unclean exit", "process", pe.name, "phase", pe.phase)
				errs = append(errs, err)
			}
			if ctr == 0 {
				break countExit
			}
		}
	}
	close(stopc)
	log.V(1).Info("exiting")

	if len(errs) > 0 {
		return fmt.Errorf("errors during run: %v", err)
	}
	return nil
}

type phaseError struct {
	name  string
	phase string
	err   error
}

func runFunc(ctx context.Context, t Tools, fn RunFunc, name, phase string, errc chan phaseError) {
	errc <- phaseError{
		name,
		phase,
		fn(ctx, t),
	}
}

func cancelOnSignal(ctx context.Context, sigc chan os.Signal, stop chan struct{}, cancel func()) {
	select {
	case <-sigc: // signal
		cancel()
	case <-stop: // shutdown completed
	case <-ctx.Done(): // other error
	}
}
