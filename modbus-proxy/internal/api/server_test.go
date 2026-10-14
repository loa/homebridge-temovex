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
	body := get(t, &src, "/temperatures")
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
	rec := request(t, &fakeSource{}, http.MethodGet, "/temperatures/ul")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not found") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestUpstreamError(t *testing.T) {
	src := fakeSource{err: errors.New("dial 192.168.3.2:26486: connection refused")}
	rec := request(t, &src, http.MethodGet, "/temperatures")
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
	srv := &Server{Source: &src, Timeout: 30 * time.Millisecond}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/temperatures", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestIncompleteReadingSet(t *testing.T) {
	src := fakeSource{readings: []temovex.Reading{{Name: "set", Celsius: 1}}}
	rec := request(t, &src, http.MethodGet, "/temperatures")
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
	rec := request(t, &src, http.MethodGet, "/temperatures")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestHealthReadyAndIndex(t *testing.T) {
	src := fakeSource{}
	for _, path := range []string{"/health", "/ready"} {
		body := get(t, &src, path)
		if body != `{"status":"ok"}`+"\n" {
			t.Fatalf("%s = %s", path, body)
		}
	}
	body := get(t, &src, "/")
	want := `{"service":"modbus-proxy","temperatures":["set","al","fl","ul","tl"]}` + "\n"
	if body != want {
		t.Fatalf("index = %s", body)
	}
	rec := request(t, &src, http.MethodGet, "/nope")
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

func TestSetSetpoint(t *testing.T) {
	src := fakeSource{}
	rec := requestBody(t, &src, http.MethodPut, "/temperatures/set", `{"celsius":21.26}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	want := `{"name":"set","description":"Supply-air setpoint","celsius":21.3}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s, want %s", rec.Body.String(), want)
	}
	if src.setpoint != 21.3 {
		t.Fatalf("written = %v", src.setpoint)
	}
	if rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("content type = %s", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache control = %s", rec.Header().Get("Cache-Control"))
	}
}

func TestSetSetpointRange(t *testing.T) {
	cases := []struct {
		body   string
		status int
		want   string
		sent   float64
	}{
		{`{"celsius":0}`, http.StatusOK, "0.0", 0},
		{`{"celsius":50}`, http.StatusOK, "50.0", 50},
		{`{"celsius":21.25}`, http.StatusOK, "21.3", 21.3},
		{`{"celsius":50.04}`, http.StatusOK, "50.0", 50},
		{`{"celsius":-0.04}`, http.StatusOK, "0.0", 0},
		{`{"celsius":-0.05}`, http.StatusBadRequest, "", 0},
		{`{"celsius":50.05}`, http.StatusBadRequest, "", 0},
		{`{"celsius":1e309}`, http.StatusBadRequest, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			src := fakeSource{}
			rec := requestBody(t, &src, http.MethodPut, "/temperatures/set", tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
			}
			if tc.status == http.StatusOK {
				if !strings.Contains(rec.Body.String(), `"celsius":`+tc.want) {
					t.Fatalf("body = %s", rec.Body.String())
				}
				if src.setpoint != tc.sent {
					t.Fatalf("written = %v, want %v", src.setpoint, tc.sent)
				}
				return
			}
			if !strings.Contains(rec.Body.String(), "celsius must be a finite number from 0 to 50") {
				t.Fatalf("body = %s", rec.Body.String())
			}
			if src.calls != 0 {
				t.Fatalf("controller calls = %d", src.calls)
			}
		})
	}
}

func TestSetSetpointRejectsBadJSON(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{"", "invalid json"},
		{"{}", "celsius is required"},
		{`{"celsius":null}`, "celsius is required"},
		{`{"celsius": null}`, "celsius is required"},
		{`{"celsius":"21.5"}`, "invalid json"},
		{`{"celsius":21.5,"name":"al"}`, "invalid json"},
		{`{"celsius":21.5}{"celsius":1}`, "invalid json"},
		{`{"celsius":` + strings.Repeat("1", 5000) + `}`, "body too large"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			src := fakeSource{}
			rec := requestBody(t, &src, http.MethodPut, "/temperatures/set", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body = %s, want %s", rec.Body.String(), tc.want)
			}
			if src.calls != 0 {
				t.Fatalf("controller calls = %d", src.calls)
			}
		})
	}
}

func TestSetSetpointUpstreamErrors(t *testing.T) {
	src := fakeSource{setErr: errors.New("dial 192.168.3.2:26486: connection refused")}
	rec := requestBody(t, &src, http.MethodPut, "/temperatures/set", `{"celsius":21.5}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}

	src = fakeSource{setErr: &temovex.ControllerError{Code: 0x19, Reason: "illegal parameter value"}}
	rec = requestBody(t, &src, http.MethodPut, "/temperatures/set", `{"celsius":21.5}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("rejected status = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "illegal parameter value") {
		t.Fatalf("body = %s", rec.Body.String())
	}

	src = fakeSource{setErr: &temovex.ControllerError{Code: 0x15, Reason: "variable not found or access denied"}}
	rec = requestBody(t, &src, http.MethodPut, "/temperatures/set", `{"celsius":21.5}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("nak status = %d, body %s", rec.Code, rec.Body.String())
	}

	wrong := temovex.Reading{Name: "al", Celsius: 1}
	src = fakeSource{setReading: &wrong}
	rec = requestBody(t, &src, http.MethodPut, "/temperatures/set", `{"celsius":21.5}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("wrong name status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestSetSetpointTimeout(t *testing.T) {
	src := fakeSource{block: true}
	srv := &Server{Source: &src, Timeout: 30 * time.Millisecond}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/temperatures/set", strings.NewReader(`{"celsius":21.5}`))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestSetSetpointMethodAndPath(t *testing.T) {
	src := fakeSource{}
	rec := request(t, &src, http.MethodGet, "/temperatures/set")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "method not allowed") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if rec.Header().Get("Allow") != http.MethodPut {
		t.Fatalf("Allow = %q", rec.Header().Get("Allow"))
	}
	if rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("content type = %s", rec.Header().Get("Content-Type"))
	}

	rec = requestBody(t, &src, http.MethodPut, "/temperatures/al", `{"celsius":21.5}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PUT sensor status = %d, body %s", rec.Code, rec.Body.String())
	}
	if src.calls != 0 {
		t.Fatalf("controller calls = %d", src.calls)
	}
}

func TestSetSetpointEndToEnd(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		reader := bufio.NewReader(conn)
		if _, err := reader.ReadBytes(0x3e); err != nil {
			return
		}
		_, _ = conn.Write([]byte{0x3d, 0x01, 0x00, 0x01, 0x3e})
	}()

	session := temovex.NewSession(ln.Addr().String(), time.Second, time.Second)
	t.Cleanup(func() { _ = session.Close() })
	httpSrv := httptest.NewServer((&Server{Source: session, Timeout: 2 * time.Second}).Handler())
	t.Cleanup(httpSrv.Close)

	req, err := http.NewRequest(http.MethodPut, httpSrv.URL+"/temperatures/set", strings.NewReader(`{"celsius":21.5}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpSrv.Client().Do(req)
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
	want := `{"name":"set","description":"Supply-air setpoint","celsius":21.5}` + "\n"
	if string(payload) != want {
		t.Fatalf("body = %s", payload)
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

func get(t *testing.T, src Source, path string) string {
	t.Helper()
	rec := request(t, src, http.MethodGet, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status = %d, body %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func request(t *testing.T, src Source, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	return requestBody(t, src, method, path, "")
}

func requestBody(t *testing.T, src Source, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	(&Server{Source: src}).Handler().ServeHTTP(rec, req)
	return rec
}

type fakeSource struct {
	readings   []temovex.Reading
	err        error
	setErr     error
	block      bool
	setpoint   float64
	calls      int
	setReading *temovex.Reading
}

func (f *fakeSource) Temperatures(ctx context.Context) ([]temovex.Reading, error) {
	f.calls++
	if err := f.wait(ctx); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.readings, nil
}

func (f *fakeSource) Setpoint(ctx context.Context, celsius float64) (temovex.Reading, error) {
	f.calls++
	if err := f.wait(ctx); err != nil {
		return temovex.Reading{}, err
	}
	if f.setErr != nil {
		return temovex.Reading{}, f.setErr
	}
	if f.setReading != nil {
		return *f.setReading, nil
	}
	f.setpoint = celsius
	return temovex.Reading{Name: "set", Description: "Supply-air setpoint", Celsius: celsius}, nil
}

func (f fakeSource) wait(ctx context.Context) error {
	if !f.block {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}
