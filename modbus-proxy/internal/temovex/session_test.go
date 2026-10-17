package temovex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFloatFrameKnownSample(t *testing.T) {
	want := []byte{0x3d, 0x05, 0x00, 0x00, 0x00, 0x98, 0x41, 0xdc, 0x3e}
	if got := floatFrame(19); !bytes.Equal(got, want) {
		t.Fatalf("floatFrame(19) = %x, want %x", got, want)
	}
}

func TestSessionReadsExporterOrder(t *testing.T) {
	values := map[string]float32{
		"set": 18.25,
		"al":  21.75,
		"fl":  22.125,
		"ul":  -4.5,
		"tl":  19,
	}
	var mu sync.Mutex
	var got []string
	unit := startUnit(t, false, func(frame []byte) []byte {
		name := nameOf(frame)
		mu.Lock()
		got = append(got, name)
		mu.Unlock()
		if name == "" {
			return nil
		}
		return floatFrame(values[name])
	})

	session := NewSession(unit.addr, time.Second, time.Second)
	t.Cleanup(func() { _ = session.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	readings, err := session.Temperatures(ctx)
	if err != nil {
		t.Fatal(err)
	}

	wantOrder := []string{"set", "al", "fl", "ul", "tl"}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != len(wantOrder) {
		t.Fatalf("requests = %v", got)
	}
	if len(readings) != len(wantOrder) {
		t.Fatalf("readings = %+v", readings)
	}
	for i, name := range wantOrder {
		if got[i] != name {
			t.Fatalf("request order = %v", got)
		}
		if readings[i].Name != name {
			t.Fatalf("reading order = %+v", readings)
		}
		if readings[i].Celsius != float64(values[name]) {
			t.Fatalf("%s = %v, want %v", name, readings[i].Celsius, values[name])
		}
	}
	if unit.accepts.Load() != 1 {
		t.Fatalf("accepts = %d, want 1", unit.accepts.Load())
	}
}

func TestSessionKeepsConnectionAfterControllerError(t *testing.T) {
	var calls atomic.Int32
	unit := startUnit(t, false, func(frame []byte) []byte {
		if calls.Add(1) == 1 {
			return []byte{0x3d, 0x15, 0x15, 0x3e}
		}
		return floatFrame(19)
	})
	session := NewSession(unit.addr, time.Second, time.Second)
	t.Cleanup(func() { _ = session.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := session.Temperature(ctx, "set")
	var controller *ControllerError
	if !errors.As(err, &controller) || controller.Code != 0x15 {
		t.Fatalf("first error = %v", err)
	}
	reading, err := session.Temperature(ctx, "set")
	if err != nil {
		t.Fatal(err)
	}
	if reading.Celsius != 19 || reading.Description != "Supply-air setpoint" {
		t.Fatalf("second reading = %+v", reading)
	}
	if unit.accepts.Load() != 1 {
		t.Fatalf("accepts = %d, want the same connection", unit.accepts.Load())
	}
}

func TestSessionReconnectsOnce(t *testing.T) {
	var calls atomic.Int32
	// Close the socket after each answer so the next read must dial again.
	unit := startUnit(t, true, func(frame []byte) []byte {
		n := calls.Add(1)
		if n == 1 {
			return floatFrame(18)
		}
		return floatFrame(20)
	})

	session := NewSession(unit.addr, time.Second, time.Second)
	t.Cleanup(func() { _ = session.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	first, err := session.Temperature(ctx, "ul")
	if err != nil {
		t.Fatal(err)
	}
	second, err := session.Temperature(ctx, "ul")
	if err != nil {
		t.Fatal(err)
	}
	if first.Celsius != 18 || second.Celsius != 20 {
		t.Fatalf("readings = %+v %+v", first, second)
	}
	if unit.accepts.Load() != 2 {
		t.Fatalf("accepts = %d, want 2", unit.accepts.Load())
	}
}

func TestSessionSerializesFrames(t *testing.T) {
	unit := startUnit(t, false, func(frame []byte) []byte {
		if nameOf(frame) != "fl" {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
		return floatFrame(19)
	})
	session := NewSession(unit.addr, time.Second, time.Second)
	t.Cleanup(func() { _ = session.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := session.Temperature(ctx, "fl"); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if unit.accepts.Load() != 1 {
		t.Fatalf("accepts = %d, want one shared connection", unit.accepts.Load())
	}
}

func TestSessionUnknownNameDoesNotDial(t *testing.T) {
	unit := startUnit(t, false, func(frame []byte) []byte { return floatFrame(1) })
	session := NewSession(unit.addr, time.Second, time.Second)
	t.Cleanup(func() { _ = session.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := session.Temperature(ctx, "room")
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("error = %v", err)
	}
	if unit.accepts.Load() != 0 {
		t.Fatalf("accepts = %d, want 0", unit.accepts.Load())
	}
}

func TestSessionSetsSetpoint(t *testing.T) {
	ack := []byte{0x3d, 0x01, 0x00, 0x01, 0x3e}
	var got []byte
	unit := startUnit(t, false, func(frame []byte) []byte {
		got = append([]byte{}, frame...)
		return ack
	})
	session := NewSession(unit.addr, time.Second, time.Second)
	t.Cleanup(func() { _ = session.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := session.SetSetpoint(ctx, 23); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x3c, 0xff, 0x1e, 0xc8, 0x08, 0x32, 0x04, 0x00, 0x08, 0x00, 0x00, 0xb8, 0x41, 0xe6, 0x3e}
	if !bytes.Equal(got, want) {
		t.Fatalf("sent %x, want %x", got, want)
	}
	if unit.accepts.Load() != 1 {
		t.Fatalf("accepts = %d, want 1", unit.accepts.Load())
	}

	if err := session.SetSetpoint(ctx, math.NaN()); err == nil {
		t.Fatal("expected non-finite setpoint to fail")
	}
	if unit.accepts.Load() != 1 {
		t.Fatalf("non-finite setpoint dialed the controller, accepts = %d", unit.accepts.Load())
	}
}

func TestSessionDialFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	session := NewSession(addr, 200*time.Millisecond, time.Second)
	t.Cleanup(func() { _ = session.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = session.Temperature(ctx, "set")
	if err == nil {
		t.Fatal("expected dial error")
	}
}

type fakeUnit struct {
	addr               string
	accepts            atomic.Int32
	closeAfterResponse bool
}

func startUnit(t *testing.T, closeAfter bool, respond func([]byte) []byte) *fakeUnit {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unit := &fakeUnit{addr: ln.Addr().String(), closeAfterResponse: closeAfter}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			unit.accepts.Add(1)
			go serveConn(conn, unit.closeAfterResponse, respond)
		}
	}()
	return unit
}

func serveConn(conn net.Conn, closeAfter bool, respond func([]byte) []byte) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		frame, err := reader.ReadBytes(0x3e)
		if err != nil {
			return
		}
		response := respond(append([]byte{}, frame...))
		if response == nil {
			return
		}
		if _, err := conn.Write(response); err != nil {
			return
		}
		if closeAfter {
			return
		}
	}
}

func nameOf(frame []byte) string {
	for _, sig := range signals {
		if bytes.Equal(frame, encode(sig.payload)) {
			return sig.name
		}
	}
	return ""
}

func floatFrame(v float32) []byte {
	bits := math.Float32bits(v)
	body := []byte{0x05, 0x00, byte(bits), byte(bits >> 8), byte(bits >> 16), byte(bits >> 24)}
	var sum byte
	for _, b := range body {
		sum ^= b
	}
	var raw []byte
	raw = append(raw, body...)
	raw = append(raw, sum)
	frame := []byte{0x3d}
	frame = append(frame, escape(raw)...)
	frame = append(frame, 0x3e)
	return frame
}

func TestFloatFrameRoundTrip(t *testing.T) {
	for _, value := range []float32{19, -4.5, 21.75, 0} {
		msg, err := decode(floatFrame(value))
		if err != nil {
			t.Fatal(err)
		}
		got, err := parseFloat(msg[3 : len(msg)-2])
		if err != nil {
			t.Fatal(err)
		}
		want := float64(math.Float32frombits(binary.LittleEndian.Uint32(msg[3 : len(msg)-2])))
		if got != want || float32(got) != value {
			t.Fatalf("round trip %v -> %v (bits %v)", value, got, want)
		}
	}
}
