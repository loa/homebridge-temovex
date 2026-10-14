// Package api serves the Temovex temperatures over HTTP.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/loa/homebridge-temovex/modbus-proxy/internal/temovex"
)

const defaultTimeout = 60 * time.Second

// minSetpointC and maxSetpointC bound the supply-air setpoint. The Temovex
// technician manual documents temperature setpoints from 0 to 50 C. Values
// outside that span are rejected here. The controller can still reject a
// value inside the span when it falls outside the unit's own min and max.
const (
	minSetpointC = 0.0
	maxSetpointC = 50.0
	maxSetBody   = 4096
)

// Source reads every Temovex temperature in one call and writes the supply-air
// setpoint. *temovex.Session implements it.
type Source interface {
	Temperatures(ctx context.Context) ([]temovex.Reading, error)
	Setpoint(ctx context.Context, celsius float64) (temovex.Reading, error)
}

// Server is the REST API. Timeout bounds one controller exchange for a
// request, including a single reconnect. Zero uses a 60 second default.
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
	// Every method, not "PUT /temperatures/set". The catch-all below also
	// matches this path, so a method pattern would answer GET with 404
	// instead of 405.
	mux.HandleFunc("/temperatures/set", s.setSetpoint)
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

func (s *Server) setSetpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, errorBody{Error: "method not allowed"})
		return
	}
	value, ok := decodeSetpoint(w, r)
	if !ok {
		return
	}

	ctx, cancel := s.context(r)
	defer cancel()

	reading, err := s.Source.Setpoint(ctx, value)
	if err != nil {
		s.writeUpstreamError(w, r, "setpoint write failed", err)
		return
	}
	body, err := setpointBody(reading)
	if err != nil {
		s.writeUpstreamError(w, r, "setpoint write failed", err)
		return
	}
	slog.Info("setpoint written", "celsius", float64(body.Celsius))
	writeJSON(w, http.StatusOK, body)
}

func decodeSetpoint(w http.ResponseWriter, r *http.Request) (float64, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSetBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var body struct {
		Celsius json.RawMessage `json:"celsius"`
	}
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: jsonClientMessage(err)})
		return 0, false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid json"})
		return 0, false
	}
	literal := strings.TrimSpace(string(body.Celsius))
	if literal == "" || literal == "null" {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "celsius is required"})
		return 0, false
	}
	value, ok := setpointNumber(json.RawMessage(literal))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid json"})
		return 0, false
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: setpointRangeMessage()})
		return 0, false
	}
	rounded := math.Round(value*10) / 10
	if rounded == 0 {
		rounded = 0
	}
	if rounded < minSetpointC || rounded > maxSetpointC {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: setpointRangeMessage()})
		return 0, false
	}
	return rounded, true
}

// setpointNumber accepts a JSON number. A number that does not fit in a
// float64 is reported as +Inf so the range check can reject it. A JSON
// string or other type is rejected.
func setpointNumber(raw json.RawMessage) (float64, bool) {
	var value float64
	err := json.Unmarshal(raw, &value)
	if err == nil {
		return value, true
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && strings.HasPrefix(typeErr.Value, "number") {
		return math.Inf(1), true
	}
	return 0, false
}

func setpointRangeMessage() string {
	return fmt.Sprintf("celsius must be a finite number from %.0f to %.0f", minSetpointC, maxSetpointC)
}

func jsonClientMessage(err error) string {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		return "body too large"
	}
	return "invalid json"
}

func setpointBody(reading temovex.Reading) (temperatureJSON, error) {
	if reading.Name != "" && reading.Name != "set" {
		return temperatureJSON{}, fmt.Errorf("setpoint write returned %q", reading.Name)
	}
	if math.IsNaN(reading.Celsius) || math.IsInf(reading.Celsius, 0) {
		return temperatureJSON{}, errors.New("non-finite temperature")
	}
	description := reading.Description
	if description == "" {
		if meta, ok := temovex.Lookup("set"); ok {
			description = meta.Description
		}
	}
	return temperatureJSON{
		Name:        "set",
		Description: description,
		Celsius:     celsius(reading.Celsius),
	}, nil
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
	s.writeUpstreamError(w, r, "temperature read failed", err)
}

func (s *Server) writeUpstreamError(w http.ResponseWriter, r *http.Request, message string, err error) {
	status := http.StatusBadGateway
	if timedOut(err) {
		status = http.StatusGatewayTimeout
	} else if valueRejected(err) {
		status = http.StatusBadRequest
	}
	if status >= 500 {
		slog.Error(message, "path", r.URL.Path, "error", err)
	}
	writeJSON(w, status, errorBody{Error: err.Error()})
}

// valueRejected reports a controller answer that the written number was not
// accepted. Those are the caller's value, so they are 400 rather than 502.
func valueRejected(err error) bool {
	var controller *temovex.ControllerError
	if !errors.As(err, &controller) {
		return false
	}
	switch controller.Code {
	case 0x19, 0x47, 0x48, 0x49, 0x50, 0x51:
		return true
	default:
		return false
	}
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
	if rounded == 0 {
		rounded = 0
	}
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
