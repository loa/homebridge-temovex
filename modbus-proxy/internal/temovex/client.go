// Package temovex reads the five temperatures published by
// github.com/loa/temovex-exporter from a REC Temovex controller, and writes
// the supply-air setpoint.
//
// The session is EXOline-TCP, not Modbus-TCP. Each read is a MULCMD (0xC8)
// wrapped RRP (0xB6) request, which the controller answers with a
// little-endian float32. Frames, addresses, and checksums match that exporter.
// The setpoint write is a MULCMD-wrapped SRP (0x32) for the same line and cell.
package temovex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"time"
)

// ErrUnknown is returned when a temperature name is not one of the five
// exporter labels: set, al, fl, ul, tl.
var ErrUnknown = errors.New("unknown temperature")

// errNonFinite is a caller error. It must not be retried as a transport failure.
var errNonFinite = errors.New("non-finite temperature")

// Reading is one temperature in degrees Celsius.
type Reading struct {
	Name        string
	Description string
	Celsius     float64
}

// ControllerError is a completed EXOline error frame. The session stays
// usable after one of these because the frame was consumed in full.
type ControllerError struct {
	Code   byte
	Reason string
}

func (e *ControllerError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("controller error 0x%02x: %s", e.Code, e.Reason)
	}
	return fmt.Sprintf("controller error 0x%02x", e.Code)
}

// Client exchanges one EXOline-TCP frame at a time on Conn.
// Conn and Reader are set by Session, or by tests that inject a connection.
type Client struct {
	Conn        net.Conn
	Reader      *bufio.Reader
	readTimeout time.Duration
}

type signal struct {
	name        string
	description string
	payload     []byte
}

// Payloads are the bytes github.com/loa/temovex-exporter sends.
// The EXOline cell encoding is high = cell/60 and low = cell%60, so a
// spec-encoded low byte is 0–59. GetSet (cell 8) and GetFL (cell 480) follow
// that encoding. GetUL, GetTL, and GetAL keep the exporter's low bytes
// 0xEC, 0xFA, and 0x95, which fall outside that range; those are the bytes
// the unit answers.
var (
	sigSet = signal{name: "set", description: "Supply-air setpoint", payload: []byte{0xff, 0x1e, 0xc8, 0x04, 0xb6, 0x04, 0x00, 0x08}}
	sigAL  = signal{name: "al", description: "Exhaust air", payload: []byte{0xff, 0x1e, 0xc8, 0x04, 0xb6, 0x3b, 0x00, 0x95}}
	sigFL  = signal{name: "fl", description: "Extract air", payload: []byte{0xff, 0x1e, 0xc8, 0x04, 0xb6, 0x04, 0x08, 0x00}}
	sigUL  = signal{name: "ul", description: "Outdoor air", payload: []byte{0xff, 0x1e, 0xc8, 0x04, 0xb6, 0x04, 0x04, 0xec}}
	sigTL  = signal{name: "tl", description: "Supply air", payload: []byte{0xff, 0x1e, 0xc8, 0x04, 0xb6, 0x39, 0x02, 0xfa}}

	// Collector order: setpoint, exhaust, extract, outdoor, supply.
	signals = []signal{sigSet, sigAL, sigFL, sigUL, sigTL}
)

// Names returns the exporter temperature labels in scrape order.
func Names() []string {
	names := make([]string, len(signals))
	for i, sig := range signals {
		names[i] = sig.name
	}
	return names
}

// Lookup returns the description for an exporter temperature label.
func Lookup(name string) (Reading, bool) {
	sig, ok := lookup(name)
	if !ok {
		return Reading{}, false
	}
	return Reading{Name: sig.name, Description: sig.description}, true
}

func lookup(name string) (signal, bool) {
	for _, sig := range signals {
		if sig.name == name {
			return sig, true
		}
	}
	return signal{}, false
}

// GetSet reads the supply-air setpoint.
func (c *Client) GetSet(ctx context.Context) (float64, error) {
	return c.getFloat(ctx, sigSet.payload)
}

// GetAL reads the exhaust-air temperature.
func (c *Client) GetAL(ctx context.Context) (float64, error) {
	return c.getFloat(ctx, sigAL.payload)
}

// GetFL reads the extract-air temperature.
func (c *Client) GetFL(ctx context.Context) (float64, error) {
	return c.getFloat(ctx, sigFL.payload)
}

// GetUL reads the outdoor-air temperature.
func (c *Client) GetUL(ctx context.Context) (float64, error) {
	return c.getFloat(ctx, sigUL.payload)
}

// GetTL reads the supply-air temperature.
func (c *Client) GetTL(ctx context.Context) (float64, error) {
	return c.getFloat(ctx, sigTL.payload)
}

// SetSet writes the supply-air setpoint in degrees Celsius.
// The value is sent as an IEEE-754 binary32. A non-finite value is rejected
// before a frame is written.
func (c *Client) SetSet(ctx context.Context, celsius float64) error {
	payload, err := writeSetPayload(celsius)
	if err != nil {
		return err
	}
	if err := c.setFloat(ctx, payload); err != nil {
		return fmt.Errorf("set: %w", err)
	}
	return nil
}

// writeSetPayload is the MULCMD SRP for GetSet's line and cell.
// RRP is four bytes (opcode, line, cell high, cell low). SRP appends the
// float32, so the MULCMD length byte is 8.
func writeSetPayload(celsius float64) ([]byte, error) {
	if math.IsNaN(celsius) || math.IsInf(celsius, 0) {
		return nil, errNonFinite
	}
	payload := []byte{0xff, 0x1e, 0xc8, 0x08, 0x32}
	payload = append(payload, sigSet.payload[5:]...)
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], math.Float32bits(float32(celsius)))
	payload = append(payload, buf[:]...)
	return payload, nil
}

// Temperature reads one named temperature.
func (c *Client) Temperature(ctx context.Context, name string) (Reading, error) {
	sig, ok := lookup(name)
	if !ok {
		return Reading{}, fmt.Errorf("%w %q", ErrUnknown, name)
	}
	value, err := c.getFloat(ctx, sig.payload)
	if err != nil {
		return Reading{}, fmt.Errorf("%s: %w", sig.name, err)
	}
	return reading(sig, value), nil
}

// Temperatures reads all five temperatures in exporter scrape order.
// A failure discards the values already read so callers never see a mix of
// fresh readings and a failed one.
func (c *Client) Temperatures(ctx context.Context) ([]Reading, error) {
	out := make([]Reading, 0, len(signals))
	for _, sig := range signals {
		value, err := c.getFloat(ctx, sig.payload)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", sig.name, err)
		}
		out = append(out, reading(sig, value))
	}
	return out, nil
}

func reading(sig signal, value float64) Reading {
	return Reading{Name: sig.name, Description: sig.description, Celsius: value}
}

func (c *Client) getFloat(ctx context.Context, msg []byte) (float64, error) {
	msg, err := c.exchange(ctx, msg)
	if err != nil {
		return 0, err
	}

	// A MULCMD real reply is SOA, two header bytes, a float32, the checksum, and EOM.
	// Four bytes is the specification's error frame: SOA, code, checksum, EOM.
	if len(msg) == 4 {
		return 0, controllerError(msg[1])
	}
	if len(msg) < 9 {
		return 0, fmt.Errorf("expected float response, got %d bytes", len(msg))
	}
	return parseFloat(msg[3 : len(msg)-2])
}

func (c *Client) setFloat(ctx context.Context, msg []byte) error {
	msg, err := c.exchange(ctx, msg)
	if err != nil {
		return err
	}
	if len(msg) == 4 {
		return controllerError(msg[1])
	}
	// MULCMD SRP success, as answered by a Corrigo and checked by stampzilla's
	// SRP client: SOA, 0x01, 0x00, checksum 0x01, EOM.
	// The bare EXOline ACK is SOA, checksum 0x00, EOM.
	if bytes.Equal(msg, []byte{0x3d, 0x01, 0x00, 0x01, 0x3e}) || bytes.Equal(msg, []byte{0x3d, 0x00, 0x3e}) {
		return nil
	}
	return fmt.Errorf("unexpected write response, got %d bytes", len(msg))
}

func (c *Client) exchange(ctx context.Context, msg []byte) ([]byte, error) {
	if c.Conn == nil || c.Reader == nil {
		return nil, errors.New("not connected")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.setDeadline(ctx); err != nil {
		return nil, err
	}
	if err := c.send(msg); err != nil {
		return nil, err
	}
	return c.read()
}

func (c *Client) setDeadline(ctx context.Context) error {
	deadline := time.Time{}
	if c.readTimeout > 0 {
		deadline = time.Now().Add(c.readTimeout)
	}
	if d, ok := ctx.Deadline(); ok && (deadline.IsZero() || d.Before(deadline)) {
		deadline = d
	}
	return c.Conn.SetDeadline(deadline)
}

func (c *Client) send(data []byte) error {
	_, err := c.Conn.Write(encode(data))
	return err
}

func (c *Client) read() ([]byte, error) {
	msg, err := c.Reader.ReadBytes(0x3e)
	if err != nil {
		return nil, err
	}
	return decode(msg)
}

func controllerError(code byte) error {
	reasons := map[byte]string{
		0x15: "variable not found or access denied",
		0x19: "illegal parameter value",
		0x40: "framing error",
		0x41: "checksum mismatch",
		0x42: "empty response",
		0x47: "logic value error",
		0x48: "index out of range",
		0x49: "integer out of range",
		0x50: "real value error",
		0x51: "string value error",
	}
	return &ControllerError{Code: code, Reason: reasons[code]}
}

func encode(msg []byte) []byte {
	var checksum byte
	for _, v := range msg {
		checksum ^= v
	}

	out := []byte{0x3c}
	out = append(out, escape(msg)...)
	out = append(out, escape([]byte{checksum})...)
	out = append(out, 0x3e)
	return out
}

func decode(msg []byte) ([]byte, error) {
	msg, err := unescape(msg)
	if err != nil {
		return nil, err
	}
	// Answer frames open with SOA and close with EOM. The shortest valid
	// answer is SOA, checksum, EOM.
	if len(msg) < 3 || msg[0] != 0x3d || msg[len(msg)-1] != 0x3e {
		return nil, fmt.Errorf("invalid frame")
	}

	payload := msg[1 : len(msg)-2]
	providedChecksum := msg[len(msg)-2]

	// XOR every byte after SOA and before the checksum. SOA and EOM are excluded.
	var checksum byte
	for _, p := range payload {
		checksum ^= p
	}
	if providedChecksum != checksum {
		return nil, fmt.Errorf("invalid checksum")
	}
	return msg, nil
}

func escape(payload []byte) []byte {
	res := make([]byte, 0, len(payload))
	for _, p := range payload {
		if p == 0x3c || p == 0x3d || p == 0x1b || p == 0x3e {
			res = append(res, 0x1b, p^0xff)
		} else {
			res = append(res, p)
		}
	}
	return res
}

func unescape(payload []byte) ([]byte, error) {
	res := make([]byte, 0, len(payload))
	for i := 0; i < len(payload); i++ {
		if payload[i] == 0x1b {
			i++
			if i >= len(payload) {
				return nil, fmt.Errorf("truncated escape")
			}
			res = append(res, payload[i]^0xff)
		} else {
			res = append(res, payload[i])
		}
	}
	return res, nil
}

func parseFloat(b []byte) (float64, error) {
	if len(b) != 4 {
		return 0, fmt.Errorf("expected 4 byte float, got %d", len(b))
	}
	return float64(math.Float32frombits(binary.LittleEndian.Uint32(b))), nil
}
