package temovex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"net"
	"testing"
	"time"
)

func TestEncodeMatchesExporter(t *testing.T) {
	// Wire frames published by github.com/loa/temovex-exporter.
	response := []byte{0x3d, 0x05, 0x00, 0x00, 0x00, 0x98, 0x41, 0xdc, 0x3e}
	tests := []struct {
		name    string
		payload []byte
		wire    []byte
		call    func(*Client, context.Context) (float64, error)
	}{
		{
			name:    "set",
			payload: []byte{0xff, 0x1e, 0xc8, 0x04, 0xb6, 0x04, 0x00, 0x08},
			wire:    []byte{0x3c, 0xff, 0x1e, 0xc8, 0x04, 0xb6, 0x04, 0x00, 0x08, 0x97, 0x3e},
			call:    (*Client).GetSet,
		},
		{
			name:    "al",
			payload: []byte{0xff, 0x1e, 0xc8, 0x04, 0xb6, 0x3b, 0x00, 0x95},
			wire:    []byte{0x3c, 0xff, 0x1e, 0xc8, 0x04, 0xb6, 0x3b, 0x00, 0x95, 0x35, 0x3e},
			call:    (*Client).GetAL,
		},
		{
			name:    "fl",
			payload: []byte{0xff, 0x1e, 0xc8, 0x04, 0xb6, 0x04, 0x08, 0x00},
			wire:    []byte{0x3c, 0xff, 0x1e, 0xc8, 0x04, 0xb6, 0x04, 0x08, 0x00, 0x97, 0x3e},
			call:    (*Client).GetFL,
		},
		{
			name:    "ul",
			payload: []byte{0xff, 0x1e, 0xc8, 0x04, 0xb6, 0x04, 0x04, 0xec},
			wire:    []byte{0x3c, 0xff, 0x1e, 0xc8, 0x04, 0xb6, 0x04, 0x04, 0xec, 0x77, 0x3e},
			call:    (*Client).GetUL,
		},
		{
			name:    "tl",
			payload: []byte{0xff, 0x1e, 0xc8, 0x04, 0xb6, 0x39, 0x02, 0xfa},
			wire:    []byte{0x3c, 0xff, 0x1e, 0xc8, 0x04, 0xb6, 0x39, 0x02, 0xfa, 0x5a, 0x3e},
			call:    (*Client).GetTL,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sig, ok := lookup(tc.name)
			if !ok {
				t.Fatalf("missing signal %s", tc.name)
			}
			if !bytes.Equal(sig.payload, tc.payload) {
				t.Fatalf("payload = %x, exporter sends %x", sig.payload, tc.payload)
			}
			if got := encode(tc.payload); !bytes.Equal(got, tc.wire) {
				t.Fatalf("encode = %x, want %x", got, tc.wire)
			}
			if !bytes.HasPrefix(tc.payload, []byte{0xff, 0x1e, 0xc8, 0x04, 0xb6}) {
				t.Fatalf("payload does not start with the fixed MULCMD header")
			}

			got, val, err := exchange(t, tc.call, response)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.wire) {
				t.Fatalf("sent %x, want %x", got, tc.wire)
			}
			if val != 19 {
				t.Fatalf("value = %v, want 19", val)
			}
		})
	}
}

func TestExporterLowBytesStayOutsideCellEncoding(t *testing.T) {
	// Spec cell encoding is high = cell/60, low = cell%60, so low is 0–59.
	if got, want := []byte{sigSet.payload[6], sigSet.payload[7]}, []byte{8 / 60, 8 % 60}; !bytes.Equal(got, want) {
		t.Fatalf("set cell = %x, want %x", got, want)
	}
	if got, want := []byte{sigFL.payload[6], sigFL.payload[7]}, []byte{480 / 60, 480 % 60}; !bytes.Equal(got, want) {
		t.Fatalf("fl cell = %x, want %x", got, want)
	}
	for _, sig := range []signal{sigUL, sigTL, sigAL} {
		if sig.payload[7] <= 59 {
			t.Fatalf("%s low byte 0x%02x is inside cell%%60; the exporter sends a byte above 59", sig.name, sig.payload[7])
		}
	}
}

func TestDecode(t *testing.T) {
	tests := []struct {
		input    []byte
		expected []byte
	}{
		{
			[]byte{0x3d, 0x5, 0x0, 0x0, 0x0, 0x98, 0x41, 0xdc, 0x3e},
			[]byte{0x3d, 0x5, 0x0, 0x0, 0x0, 0x98, 0x41, 0xdc, 0x3e},
		},
		{
			[]byte{0x3d, 0x5, 0x0, 0x90, 0xcc, 0x7a, 0x41, 0x62, 0x3e},
			[]byte{0x3d, 0x5, 0x0, 0x90, 0xcc, 0x7a, 0x41, 0x62, 0x3e},
		},
		{
			[]byte{0x3d, 0x5, 0x0, 0x2b, 0xe9, 0xba, 0x41, 0x1b, 0xc3, 0x3e},
			[]byte{0x3d, 0x5, 0x0, 0x2b, 0xe9, 0xba, 0x41, 0x3c, 0x3e},
		},
		{
			[]byte{0x3d, 0x5, 0x0, 0xd, 0x1b, 0xe4, 0xc4, 0x41, 0x96, 0x3e},
			[]byte{0x3d, 0x5, 0x0, 0xd, 0x1b, 0xc4, 0x41, 0x96, 0x3e},
		},
	}

	for _, test := range tests {
		actual, err := decode(test.input)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(actual, test.expected) {
			t.Fatalf("decode = %x, want %x", actual, test.expected)
		}
	}
}

func TestSpecTemperatureExample(t *testing.T) {
	// Published MULCMD temperature reply. Checksum 0x3D is stuffed because it equals SOA.
	wire := []byte{0x3d, 0x05, 0x00, 0x14, 0xc3, 0xae, 0x41, 0x1b, 0xc2, 0x3e}
	msg, err := decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	wantMsg := []byte{0x3d, 0x05, 0x00, 0x14, 0xc3, 0xae, 0x41, 0x3d, 0x3e}
	if !bytes.Equal(msg, wantMsg) {
		t.Fatalf("decode = %x, want %x", msg, wantMsg)
	}

	got, err := parseFloat(msg[3 : len(msg)-2])
	if err != nil {
		t.Fatal(err)
	}
	want := float64(math.Float32frombits(binary.LittleEndian.Uint32([]byte{0x14, 0xc3, 0xae, 0x41})))
	if got != want {
		t.Fatalf("parseFloat = %v, want %v", got, want)
	}
	if math.Abs(got-21.845) > 0.001 {
		t.Fatalf("temperature = %v, want about 21.845", got)
	}

	sent, val, err := exchange(t, (*Client).GetSet, wire)
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) == 0 {
		t.Fatal("client sent no request")
	}
	if math.Abs(val-21.845) > 0.001 {
		t.Fatalf("GetSet = %v, want about 21.845", val)
	}
}

func TestDecodeRejectsSpecViolations(t *testing.T) {
	_, err := decode([]byte{0x3d, 0x05, 0x00, 0x00, 0x00, 0x98, 0x41, 0x00, 0x3e})
	if err == nil || err.Error() != "invalid checksum" {
		t.Fatalf("checksum error = %v", err)
	}

	_, err = decode([]byte{0x3c, 0x05, 0x00, 0x05, 0x3e})
	if err == nil || err.Error() != "invalid frame" {
		t.Fatalf("frame error = %v", err)
	}

	_, err = decode([]byte{0x3d, 0x00, 0x1b})
	if err == nil || err.Error() != "truncated escape" {
		t.Fatalf("escape error = %v", err)
	}

	_, err = parseFloat([]byte{0x00, 0x00})
	if err == nil || err.Error() != "expected 4 byte float, got 2" {
		t.Fatalf("float error = %v", err)
	}
}

func TestErrorResponseDoesNotPanic(t *testing.T) {
	// SOA, NAK 0x15, checksum 0x15, EOM.
	nak := []byte{0x3d, 0x15, 0x15, 0x3e}
	msg, err := decode(nak)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(msg, nak) {
		t.Fatalf("decode = %x, want %x", msg, nak)
	}

	_, _, err = exchange(t, (*Client).GetSet, nak)
	if err == nil || err.Error() != "controller error 0x15: variable not found or access denied" {
		t.Fatalf("GetSet error = %v", err)
	}
}

func TestShortResponse(t *testing.T) {
	// Five-byte answer: SOA, two header bytes, checksum, EOM.
	// Checksum of 0x05 ^ 0x00 is 0x05. Too short to be a float.
	short := []byte{0x3d, 0x05, 0x00, 0x05, 0x3e}
	_, _, err := exchange(t, (*Client).GetSet, short)
	if err == nil || err.Error() != "expected float response, got 5 bytes" {
		t.Fatalf("GetSet error = %v", err)
	}
}

func TestSetSetWritesSupplySetpoint(t *testing.T) {
	// 23°C is the float the exporter read back for this same cell: 00 00 B8 41.
	payload := []byte{0xff, 0x1e, 0xc8, 0x08, 0x32, 0x04, 0x00, 0x08, 0x00, 0x00, 0xb8, 0x41}
	wire := []byte{0x3c, 0xff, 0x1e, 0xc8, 0x08, 0x32, 0x04, 0x00, 0x08, 0x00, 0x00, 0xb8, 0x41, 0xe6, 0x3e}
	if got := encode(payload); !bytes.Equal(got, wire) {
		t.Fatalf("encode = %x, want %x", got, wire)
	}
	if !bytes.Equal(payload[5:8], sigSet.payload[5:8]) {
		t.Fatalf("setpoint address = %x, read address = %x", payload[5:8], sigSet.payload[5:8])
	}

	ack := []byte{0x3d, 0x01, 0x00, 0x01, 0x3e}
	got, err := exchangeSet(t, 23, ack)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wire) {
		t.Fatalf("sent %x, want %x", got, wire)
	}

	got, err = exchangeSet(t, 23, []byte{0x3d, 0x00, 0x3e})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wire) {
		t.Fatalf("sent %x, want %x", got, wire)
	}

	_, err = exchangeSet(t, 23, []byte{0x3d, 0x15, 0x15, 0x3e})
	if err == nil || err.Error() != "controller error 0x15: variable not found or access denied" {
		t.Fatalf("error = %v", err)
	}
	if _, err = setpointPayload(math.NaN()); err == nil {
		t.Fatal("expected non-finite setpoint to fail")
	}
}

func exchangeSet(t *testing.T, celsius float64, response []byte) ([]byte, error) {
	t.Helper()
	srv, cli := net.Pipe()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = cli.Close()
	})
	deadline := time.Now().Add(2 * time.Second)
	_ = srv.SetDeadline(deadline)
	_ = cli.SetDeadline(deadline)

	type captured struct {
		got []byte
		err error
	}
	ch := make(chan captured, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := srv.Read(buf)
		if err != nil {
			ch <- captured{err: err}
			return
		}
		_, err = srv.Write(response)
		ch <- captured{got: append([]byte{}, buf[:n]...), err: err}
	}()

	client := &Client{Conn: cli, Reader: bufio.NewReader(cli), readTimeout: 2 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := client.SetSet(ctx, celsius)
	capd := <-ch
	if capd.err != nil {
		return capd.got, capd.err
	}
	return capd.got, err
}

func TestNamesFollowCollectorOrder(t *testing.T) {
	want := []string{"set", "al", "fl", "ul", "tl"}
	got := Names()
	if len(got) != len(want) {
		t.Fatalf("Names = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names = %v, want %v", got, want)
		}
	}
	if _, ok := Lookup("nope"); ok {
		t.Fatal("Lookup accepted an unknown name")
	}
	reading, ok := Lookup("ul")
	if !ok || reading.Description != "Outdoor air" {
		t.Fatalf("Lookup(ul) = %+v, %v", reading, ok)
	}
}

func exchange(t *testing.T, call func(*Client, context.Context) (float64, error), response []byte) ([]byte, float64, error) {
	t.Helper()
	srv, cli := net.Pipe()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = cli.Close()
	})
	deadline := time.Now().Add(2 * time.Second)
	_ = srv.SetDeadline(deadline)
	_ = cli.SetDeadline(deadline)

	type captured struct {
		got []byte
		err error
	}
	ch := make(chan captured, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := srv.Read(buf)
		if err != nil {
			ch <- captured{err: err}
			return
		}
		_, err = srv.Write(response)
		ch <- captured{got: append([]byte{}, buf[:n]...), err: err}
	}()

	client := &Client{Conn: cli, Reader: bufio.NewReader(cli), readTimeout: 2 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	val, err := call(client, ctx)
	capd := <-ch
	if capd.err != nil {
		return capd.got, val, capd.err
	}
	return capd.got, val, err
}
