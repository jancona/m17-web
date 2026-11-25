// m17-web is the M17 hotspot's web UI: the setup wizard now, its system
// and admin pages and its dashboard later. It listens on localhost behind nginx, which sends
// it /setup/ (and, while the setup access point is up, every captive-portal
// check).
//
//	m17-web -dev     run on a workstation against a simulated hotspot
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jancona/m17-web/internal/activity"
	"github.com/jancona/m17-web/internal/platform"
	"github.com/jancona/m17-web/internal/web"
	"github.com/jancona/m17-web/internal/wizard"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8090", "address to listen on")
	stateDir := flag.String("state-dir", "/var/lib/m17-web", "where the wizard keeps its progress")
	dev := flag.Bool("dev", false, "run against a simulated hotspot, with state in a temporary directory")
	debug := flag.Bool("debug", false, "log every request")
	mqttBroker := flag.String("mqtt", "127.0.0.1:1883", "MQTT broker (host:port) m17-gateway publishes its status to; empty for none")
	mqttName := flag.String("mqtt-name", "m17-gateway", "m17-gateway's MQTT Name (its topic prefix)")
	hashPassword := flag.Bool("hash-password", false, "read a password from stdin, print its hash for the admin password file, and exit")
	flag.Parse()

	if *hashPassword {
		// For m17os-config, setting the password from m17-config.txt
		pw, err := bufio.NewReader(os.Stdin).ReadString('\n')
		pw = strings.TrimRight(pw, "\r\n")
		if pw == "" {
			fmt.Fprintln(os.Stderr, "m17-web: no password on stdin", err)
			os.Exit(2)
		}
		h, err := platform.HashPassword(pw)
		if err != nil {
			fmt.Fprintln(os.Stderr, "m17-web:", err)
			os.Exit(1)
		}
		fmt.Println(h)
		return
	}

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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Recent activity, heard from m17-gateway over MQTT
	subscribed := make(chan struct{})
	if *mqttBroker != "" {
		act := activity.New(filepath.Join(*stateDir, "lastheard.json"), activity.DefaultMax)
		if err := act.Load(); err != nil {
			log.Warn("activity: starting with an empty log", "err", err)
		}
		srv.SetActivity(act)
		go func() {
			defer close(subscribed)
			act.Subscribe(ctx, *mqttBroker, *mqttName, log)
		}()
	} else {
		close(subscribed)
	}

	hs := &http.Server{
		Addr:              *listen,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

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
	<-subscribed // the activity log is saved
}

func fail(log *slog.Logger, err error) {
	log.Error(err.Error())
	fmt.Fprintln(os.Stderr, "m17-web:", err)
	os.Exit(1)
}
