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
	want := `{"unit":"celsius","set":18.0,"al":21.8,"fl":22.1,"ul":4.5,"tl":21.8}` + "\n"
	if body != want {
		t.Fatalf("body = %s, want %s", body, want)
	}
}

func TestTemperatureByName(t *testing.T) {
	src := fakeSource{readings: []temovex.Reading{
		{Name: "ul", Description: "Outdoor air", Celsius: -4.44},
	}}
	body := get(t, src, "/temperatures/ul")
	want := `{"name":"ul","description":"Outdoor air","unit":"celsius","celsius":-4.4}` + "\n"
	if body != want {
		t.Fatalf("body = %s, want %s", body, want)
	}
}

func TestUnknownTemperature(t *testing.T) {
	rec := request(t, fakeSource{}, http.MethodGet, "/temperatures/room")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unknown temperature") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestUpstreamError(t *testing.T) {
	src := fakeSource{err: errors.New("dial 192.168.3.2:26486: connection refused")}
	rec := request(t, src, http.MethodGet, "/temperatures")
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
	req := httptest.NewRequest(http.MethodGet, "/temperatures/set", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestIncompleteReadingSet(t *testing.T) {
	src := fakeSource{readings: []temovex.Reading{{Name: "set", Celsius: 1}}}
	rec := request(t, src, http.MethodGet, "/temperatures")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
}

func TestNonFiniteTemperature(t *testing.T) {
	src := fakeSource{readings: []temovex.Reading{{Name: "set", Celsius: math.NaN()}}}
	rec := request(t, src, http.MethodGet, "/temperatures/set")
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
	rec := request(t, src, http.MethodGet, "/nope")
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
	want := `{"unit":"celsius","set":19.0,"al":19.0,"fl":19.0,"ul":19.0,"tl":19.0}` + "\n"
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
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	(&Server{Source: src}).Handler().ServeHTTP(rec, req)
	return rec
}

type fakeSource struct {
	readings []temovex.Reading
	err      error
	block    bool
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

func (f fakeSource) Temperature(ctx context.Context, name string) (temovex.Reading, error) {
	if err := f.wait(ctx); err != nil {
		return temovex.Reading{}, err
	}
	if f.err != nil {
		return temovex.Reading{}, f.err
	}
	for _, reading := range f.readings {
		if reading.Name == name {
			return reading, nil
		}
	}
	return temovex.Reading{}, temovex.ErrUnknown
}

func (f fakeSource) wait(ctx context.Context) error {
	if !f.block {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}
