// m17-web is the M17 hotspot's web UI: the setup wizard now, its system
// and admin pages and its dashboard later. It listens on localhost behind nginx, which sends
// it /setup/ (and, while the setup access point is up, every captive-portal
// check).
//
//	m17-web -dev     run on a workstation against a simulated hotspot
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jancona/m17-web/internal/platform"
	"github.com/jancona/m17-web/internal/web"
	"github.com/jancona/m17-web/internal/wizard"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8090", "address to listen on")
	stateDir := flag.String("state-dir", "/var/lib/m17-web", "where the wizard keeps its progress")
	dev := flag.Bool("dev", false, "run against a simulated hotspot, with state in a temporary directory")
	debug := flag.Bool("debug", false, "log every request")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	var plat platform.Platform
	if *dev {
		fake := platform.NewFake()
		fake.FlashTime = 10 * time.Second // long enough to see the progress page
		plat = fake
		dir, err := os.MkdirTemp("", "m17-web-dev-")
		if err != nil {
			fail(log, err)
		}
		defer os.RemoveAll(dir)
		*stateDir = dir
		log.Info("development mode: simulated hotspot", "state", dir)
	} else {
		plat = platform.NewAlpine()
	}

	srv, err := web.New(plat, wizard.NewStore(*stateDir), log)
	if err != nil {
		fail(log, err)
	}
	hs := &http.Server{
		Addr:              *listen,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
	}()

	log.Info("listening", "addr", "http://"+*listen+"/setup/")
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fail(log, err)
	}
}

func fail(log *slog.Logger, err error) {
	log.Error(err.Error())
	fmt.Fprintln(os.Stderr, "m17-web:", err)
	os.Exit(1)
}
