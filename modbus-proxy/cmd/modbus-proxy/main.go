// Command modbus-proxy serves Temovex temperatures over HTTP.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/loa/homebridge-temovex/modbus-proxy/internal/api"
	"github.com/loa/homebridge-temovex/modbus-proxy/internal/temovex"
)

type config struct {
	temovexAddr string
	httpAddr    string
	dialTimeout time.Duration
	readTimeout time.Duration
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		slog.Error("configuration", "error", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config) error {
	session := temovex.NewSession(cfg.temovexAddr, cfg.dialTimeout, cfg.readTimeout)
	defer session.Close()

	srv := &http.Server{
		Addr:              cfg.httpAddr,
		Handler:           (&api.Server{Source: session}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.httpAddr, "temovex", cfg.temovexAddr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			return err
		}
		err := <-errCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func loadConfig(getenv func(string) string) (config, error) {
	addr := strings.TrimSpace(getenv("TEMOVEX_ADDR"))
	if addr == "" {
		return config{}, errors.New("TEMOVEX_ADDR is required, for example 192.168.3.2:26486")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return config{}, fmt.Errorf("TEMOVEX_ADDR must be host:port: %w", err)
	}

	httpAddr := strings.TrimSpace(getenv("HTTP_ADDR"))
	if httpAddr == "" {
		httpAddr = ":8080"
	}

	dialTimeout, err := positiveDuration(getenv, "TEMOVEX_DIAL_TIMEOUT", 10*time.Second)
	if err != nil {
		return config{}, err
	}
	readTimeout, err := positiveDuration(getenv, "TEMOVEX_READ_TIMEOUT", 3*time.Second)
	if err != nil {
		return config{}, err
	}

	return config{
		temovexAddr: addr,
		httpAddr:    httpAddr,
		dialTimeout: dialTimeout,
		readTimeout: readTimeout,
	}, nil
}

func positiveDuration(getenv func(string) string, key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", key)
	}
	return d, nil
}
