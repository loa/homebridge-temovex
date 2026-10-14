package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/loa/homebridge-temovex/modbus-proxy/internal/temovex"
)

func TestTemperaturesJSONMatchesExporterLabels(t *testing.T) {
	src := fakeSource{readings: []temovex.Reading{
		{Name: "set", Celsius: 18.04},
		{Name: "al", Celsius: 21.75},
		{Name: "fl", Celsius: 22.14},
		{Name: "ul", Celsius: 4.46},
		{Name: "tl", Celsius: 21.845},
	}}
	body := get(t, src, "/temperatures")
	want := `{"unit":"celsius","temperatures":[` +
		`{"name":"set","description":"Supply-air setpoint","celsius":18.0},` +
		`{"name":"al","description":"Exhaust air","celsius":21.8},` +
		`{"name":"fl","description":"Extract air","celsius":22.1},` +
		`{"name":"ul","description":"Outdoor air","celsius":4.5},` +
		`{"name":"tl","description":"Supply air","celsius":21.8}` +
		`]}` + "\n"
	if body != want {
		t.Fatalf("body = %s, want %s", body, want)
	}
}

func TestPerSensorPathIsNotFound(t *testing.T) {
	rec := request(t, fakeSource{}, http.MethodGet, "/temperatures/ul", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not found") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestUpstreamError(t *testing.T) {
	src := fakeSource{err: errors.New("dial 192.168.3.2:26486: connection refused")}
	rec := request(t, src, http.MethodGet, "/temperatures", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", rec.Code)
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error != src.err.Error() {
		t.Fatalf("error = %q", payload.Error)
	}
}

func TestReadTimeout(t *testing.T) {
	src := fakeSource{block: true}
	srv := &Server{Source: src, Timeout: 30 * time.Millisecond}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/temperatures", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestIncompleteReadingSet(t *testing.T) {
	src := fakeSource{readings: []temovex.Reading{{Name: "set", Celsius: 1}}}
	rec := request(t, src, http.MethodGet, "/temperatures", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestNonFiniteTemperature(t *testing.T) {
	src := fakeSource{readings: []temovex.Reading{
		{Name: "set", Celsius: math.NaN()},
		{Name: "al", Celsius: 1},
		{Name: "fl", Celsius: 1},
		{Name: "ul", Celsius: 1},
		{Name: "tl", Celsius: 1},
	}}
	rec := request(t, src, http.MethodGet, "/temperatures", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestHealthReadyAndIndex(t *testing.T) {
	src := fakeSource{}
	for _, path := range []string{"/health", "/ready"} {
		body := get(t, src, path)
		if body != `{"status":"ok"}`+"\n" {
			t.Fatalf("%s = %s", path, body)
		}
	}
	body := get(t, src, "/")
	want := `{"service":"modbus-proxy","temperatures":["set","al","fl","ul","tl"]}` + "\n"
	if body != want {
		t.Fatalf("index = %s", body)
	}
	rec := request(t, src, http.MethodGet, "/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("content type = %s", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache control = %s", rec.Header().Get("Cache-Control"))
	}
}

func TestCelsiusJSON(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{19, "19.0"},
		{21.845, "21.8"},
		{1.25, "1.3"},
		{-1.25, "-1.3"},
		{-12.34, "-12.3"},
	}
	for _, tc := range cases {
		got, err := celsius(tc.in).MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Fatalf("MarshalJSON(%v) = %s, want %s", tc.in, got, tc.want)
		}
	}
	if _, err := celsius(math.NaN()).MarshalJSON(); err == nil {
		t.Fatal("expected NaN to fail")
	}
}

func TestEndToEndAgainstFakeUnit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				for {
					_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
					if _, err := reader.ReadBytes(0x3e); err != nil {
						return
					}
					if _, err := conn.Write([]byte{0x3d, 0x05, 0x00, 0x00, 0x00, 0x98, 0x41, 0xdc, 0x3e}); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	session := temovex.NewSession(ln.Addr().String(), time.Second, time.Second)
	t.Cleanup(func() { _ = session.Close() })
	httpSrv := httptest.NewServer((&Server{Source: session, Timeout: 2 * time.Second}).Handler())
	t.Cleanup(httpSrv.Close)

	resp, err := httpSrv.Client().Get(httpSrv.URL + "/temperatures")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, payload)
	}
	want := `{"unit":"celsius","temperatures":[` +
		`{"name":"set","description":"Supply-air setpoint","celsius":19.0},` +
		`{"name":"al","description":"Exhaust air","celsius":19.0},` +
		`{"name":"fl","description":"Extract air","celsius":19.0},` +
		`{"name":"ul","description":"Outdoor air","celsius":19.0},` +
		`{"name":"tl","description":"Supply air","celsius":19.0}` +
		`]}` + "\n"
	if string(payload) != want {
		t.Fatalf("body = %s", payload)
	}
}

func TestSetSetpoint(t *testing.T) {
	var got float64
	src := fakeSource{setpoint: &got}
	rec := request(t, src, http.MethodPut, "/temperatures/set", `{"celsius":21.5}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	want := `{"name":"set","description":"Supply-air setpoint","celsius":21.5}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if got != 21.5 {
		t.Fatalf("setpoint = %v", got)
	}
	for _, body := range []string{`{"celsius":10}`, `{"celsius":40}`} {
		rec = request(t, fakeSource{}, http.MethodPut, "/temperatures/set", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("body %s status = %d, response %s", body, rec.Code, rec.Body.String())
		}
	}

	rec = request(t, fakeSource{}, http.MethodGet, "/temperatures/set", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET status = %d", rec.Code)
	}
}

func TestSetSetpointRejectsBadBody(t *testing.T) {
	cases := []string{``, `{"celsius":9.9}`, `{"celsius":40.1}`, `{"celsius":null}`, `{"other":1}`, `[]`, `{"celsius":"21"}`}
	for _, body := range cases {
		rec := request(t, fakeSource{}, http.MethodPut, "/temperatures/set", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s status = %d, response %s", body, rec.Code, rec.Body.String())
		}
	}
}

func TestSetSetpointUpstreamError(t *testing.T) {
	src := fakeSource{setErr: errors.New("controller error 0x50: real value error")}
	rec := request(t, src, http.MethodPut, "/temperatures/set", `{"celsius":18}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func get(t *testing.T, src Source, path string) string {
	t.Helper()
	rec := request(t, src, http.MethodGet, path, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status = %d, body %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func request(t *testing.T, src Source, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	(&Server{Source: src}).Handler().ServeHTTP(rec, req)
	return rec
}

type fakeSource struct {
	readings []temovex.Reading
	err      error
	setErr   error
	block    bool
	setpoint *float64
}

func (f fakeSource) Temperatures(ctx context.Context) ([]temovex.Reading, error) {
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.readings, nil
}

func (f fakeSource) SetSetpoint(ctx context.Context, celsius float64) error {
	if err := f.wait(ctx); err != nil {
		return err
	}
	if f.setErr != nil {
		return f.setErr
	}
	if f.setpoint != nil {
		*f.setpoint = celsius
	}
	return nil
}

func (f fakeSource) wait(ctx context.Context) error {
	if !f.block {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}
