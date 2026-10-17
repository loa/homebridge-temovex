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

const (
	defaultTimeout = 60 * time.Second
	minSetpointC   = 10
	maxSetpointC   = 40
)

// Source reads every Temovex temperature in one call and writes the supply-air
// setpoint. *temovex.Session implements it.
type Source interface {
	Temperatures(ctx context.Context) ([]temovex.Reading, error)
	SetSetpoint(ctx context.Context, celsius float64) error
}

// Server is the REST API. Timeout bounds one HTTP call to the controller,
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
//	PUT /temperatures/set
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /health", s.status)
	mux.HandleFunc("GET /ready", s.status)
	mux.HandleFunc("GET /temperatures", s.temperatures)
	mux.HandleFunc("PUT /temperatures/set", s.setSetpoint)
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

func (s *Server) setSetpoint(w http.ResponseWriter, r *http.Request) {
	degrees, err := decodeSetpoint(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: err.Error()})
		return
	}
	ctx, cancel := s.context(r)
	defer cancel()
	if err := s.Source.SetSetpoint(ctx, degrees); err != nil {
		s.writeReadError(w, r, err)
		return
	}
	slog.Info("setpoint updated", "celsius", degrees)
	writeJSON(w, http.StatusOK, temperatureJSON{
		Name:        "set",
		Description: "Supply-air setpoint",
		Celsius:     celsius(degrees),
	})
}

func decodeSetpoint(r *http.Request) (float64, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var body struct {
		Celsius *float64 `json:"celsius"`
	}
	if err := decoder.Decode(&body); err != nil || decoder.More() {
		return 0, errors.New("expected JSON object with celsius")
	}
	if body.Celsius == nil {
		return 0, errors.New("celsius is required")
	}
	degrees := *body.Celsius
	if math.IsNaN(degrees) || math.IsInf(degrees, 0) {
		return 0, errors.New("celsius must be a finite number")
	}
	if degrees < minSetpointC || degrees > maxSetpointC {
		return 0, fmt.Errorf("celsius must be between %d and %d", minSetpointC, maxSetpointC)
	}
	return degrees, nil
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
	byName := make(map[string]temovex.Reading, len(readings))
	for _, reading := range readings {
		if _, ok := byName[reading.Name]; ok {
			return temperaturesBody{}, fmt.Errorf("duplicate temperature %q", reading.Name)
		}
		byName[reading.Name] = reading
	}
	if len(byName) != len(want) {
		return temperaturesBody{}, fmt.Errorf("expected %d temperatures, got %d", len(want), len(byName))
	}

	body := temperaturesBody{
		Unit:         "celsius",
		Temperatures: make([]temperatureJSON, 0, len(want)),
	}
	for _, name := range want {
		reading, ok := byName[name]
		if !ok {
			return temperaturesBody{}, fmt.Errorf("missing temperature %q", name)
		}
		if math.IsNaN(reading.Celsius) || math.IsInf(reading.Celsius, 0) {
			return temperaturesBody{}, fmt.Errorf("%s: non-finite temperature", reading.Name)
		}
		description := reading.Description
		if description == "" {
			if meta, ok := temovex.Lookup(name); ok {
				description = meta.Description
			}
		}
		body.Temperatures = append(body.Temperatures, temperatureJSON{
			Name:        name,
			Description: description,
			Celsius:     celsius(reading.Celsius),
		})
	}
	return body, nil
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
	if timedOut(err) {
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

// temperaturesBody is the only temperature resource. Names follow the exporter scrape order.
type temperaturesBody struct {
	Unit         string            `json:"unit"`
	Temperatures []temperatureJSON `json:"temperatures"`
}

type temperatureJSON struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
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
