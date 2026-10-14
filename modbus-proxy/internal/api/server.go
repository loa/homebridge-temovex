// Package api serves the Temovex temperatures over HTTP.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/loa/homebridge-temovex/modbus-proxy/internal/temovex"
)

const defaultTimeout = 60 * time.Second

// Source reads temperatures. *temovex.Session implements it.
type Source interface {
	Temperatures(ctx context.Context) ([]temovex.Reading, error)
	Temperature(ctx context.Context, name string) (temovex.Reading, error)
}

// Server is the REST API. Timeout bounds one HTTP read of the controller,
// including a single reconnect. Zero uses a 60 second default.
type Server struct {
	Source  Source
	Timeout time.Duration
}

// Handler returns the REST routes.
//
//	GET /health
//	GET /ready
//	GET /temperatures
//	GET /temperatures/{name}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /health", s.status)
	mux.HandleFunc("GET /ready", s.status)
	mux.HandleFunc("GET /temperatures", s.temperatures)
	mux.HandleFunc("GET /temperatures/{name}", s.temperature)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, errorBody{Error: "not found"})
	})
	return mux
}

func (s *Server) index(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, indexBody{
		Service:      "modbus-proxy",
		Temperatures: temovex.Names(),
	})
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, statusBody{Status: "ok"})
}

func (s *Server) temperatures(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.context(r)
	defer cancel()

	readings, err := s.Source.Temperatures(ctx)
	if err != nil {
		s.writeReadError(w, r, err)
		return
	}
	body, err := newTemperaturesBody(readings)
	if err != nil {
		s.writeReadError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func newTemperaturesBody(readings []temovex.Reading) (temperaturesBody, error) {
	want := temovex.Names()
	if len(readings) != len(want) {
		return temperaturesBody{}, fmt.Errorf("expected %d temperatures, got %d", len(want), len(readings))
	}
	body := temperaturesBody{Unit: "celsius"}
	seen := make(map[string]bool, len(want))
	for _, reading := range readings {
		if seen[reading.Name] {
			return temperaturesBody{}, fmt.Errorf("duplicate temperature %q", reading.Name)
		}
		if math.IsNaN(reading.Celsius) || math.IsInf(reading.Celsius, 0) {
			return temperaturesBody{}, fmt.Errorf("%s: non-finite temperature", reading.Name)
		}
		seen[reading.Name] = true
		value := celsius(reading.Celsius)
		switch reading.Name {
		case "set":
			body.Set = value
		case "al":
			body.AL = value
		case "fl":
			body.FL = value
		case "ul":
			body.UL = value
		case "tl":
			body.TL = value
		default:
			return temperaturesBody{}, fmt.Errorf("unexpected temperature %q", reading.Name)
		}
	}
	return body, nil
}

func (s *Server) temperature(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	meta, ok := temovex.Lookup(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorBody{Error: fmt.Sprintf("unknown temperature %q", name)})
		return
	}

	ctx, cancel := s.context(r)
	defer cancel()

	reading, err := s.Source.Temperature(ctx, name)
	if err != nil {
		s.writeReadError(w, r, err)
		return
	}
	if math.IsNaN(reading.Celsius) || math.IsInf(reading.Celsius, 0) {
		s.writeReadError(w, r, fmt.Errorf("%s: non-finite temperature", name))
		return
	}
	writeJSON(w, http.StatusOK, temperatureBody{
		Name:        meta.Name,
		Description: meta.Description,
		Unit:        "celsius",
		Celsius:     celsius(reading.Celsius),
	})
}

func (s *Server) context(r *http.Request) (context.Context, context.CancelFunc) {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return context.WithTimeout(r.Context(), timeout)
}

func (s *Server) writeReadError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusBadGateway
	if errors.Is(err, temovex.ErrUnknown) {
		status = http.StatusNotFound
	} else if timedOut(err) {
		status = http.StatusGatewayTimeout
	}
	if status >= 500 {
		slog.Error("temperature read failed", "path", r.URL.Path, "error", err)
	}
	writeJSON(w, status, errorBody{Error: err.Error()})
}

func timedOut(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

type indexBody struct {
	Service      string   `json:"service"`
	Temperatures []string `json:"temperatures"`
}

type statusBody struct {
	Status string `json:"status"`
}

// temperaturesBody uses the exporter label names as JSON keys, in scrape order.
type temperaturesBody struct {
	Unit string  `json:"unit"`
	Set  celsius `json:"set"`
	AL   celsius `json:"al"`
	FL   celsius `json:"fl"`
	UL   celsius `json:"ul"`
	TL   celsius `json:"tl"`
}

type temperatureBody struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Unit        string  `json:"unit"`
	Celsius     celsius `json:"celsius"`
}

type errorBody struct {
	Error string `json:"error"`
}

// celsius renders one decimal place, matching temovex_temperature_c.
type celsius float64

func (c celsius) MarshalJSON() ([]byte, error) {
	value := float64(c)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, fmt.Errorf("non-finite temperature")
	}
	rounded := math.Round(value*10) / 10
	return []byte(strconv.FormatFloat(rounded, 'f', 1, 64)), nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("write json response", "error", err)
	}
}
