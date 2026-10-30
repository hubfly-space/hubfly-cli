package cli

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// A small RTMP publisher for `hubfly stream push --protocol rtmps`: the
// handshake, connect, createStream and publish, then FLV tags (from ffmpeg's
// stdout) as chunked audio, video and data messages. The stream key only
// travels inside the (TLS) connection; it is never logged and never put in
// an error.

const (
	rtmpTypeSetChunkSize = 1
	rtmpTypeAck          = 3
	rtmpTypeUserControl  = 4
	rtmpTypeWindowAck    = 5
	rtmpTypePeerBW       = 6
	rtmpTypeAudio        = 8
	rtmpTypeVideo        = 9
	rtmpTypeDataAMF0     = 18
	rtmpTypeCommandAMF0  = 20

	rtmpCSControl = 2
	rtmpCSCommand = 3
	rtmpCSAudio   = 4
	rtmpCSVideo   = 6
	rtmpCSStream  = 8

	rtmpOutChunkSize  = 4096
	rtmpHandshakeSize = 1536
	rtmpSetupTimeout  = 15 * time.Second
	rtmpMaxMessage    = 16 << 20
)

type rtmpMessage struct {
	Type      byte
	StreamID  uint32
	Timestamp uint32
	Payload   []byte
}

type rtmpChunkState struct {
	timestamp uint32
	delta     uint32
	length    uint32
	typ       byte
	streamID  uint32
	extended  bool
	buf       []byte
	started   bool
}

// rtmpConn reads and writes RTMP chunk streams on one connection.
type rtmpConn struct {
	conn     net.Conn
	r        *bufio.Reader
	inChunk  uint32
	outChunk uint32
	in       map[uint32]*rtmpChunkState
	writeMu  sync.Mutex
}

func newRTMPConn(conn net.Conn) *rtmpConn {
	return &rtmpConn{conn: conn, r: bufio.NewReader(conn), inChunk: 128, outChunk: 128, in: map[uint32]*rtmpChunkState{}}
}

// clientHandshake sends C0/C1, reads S0/S1/S2 and answers C2 (the "simple"
// handshake every RTMP server accepts).
func (c *rtmpConn) clientHandshake() error {
	c1 := make([]byte, 1+rtmpHandshakeSize)
	c1[0] = 3
	binary.BigEndian.PutUint32(c1[1:5], uint32(time.Now().Unix()))
	if _, err := rand.Read(c1[9:]); err != nil {
		return err
	}
	if _, err := c.conn.Write(c1); err != nil {
		return err
	}
	s := make([]byte, 1+2*rtmpHandshakeSize)
	if _, err := io.ReadFull(c.r, s); err != nil {
		return fmt.Errorf("RTMP handshake: %w", err)
	}
	if s[0] != 3 {
		return fmt.Errorf("RTMP handshake: unsupported version %d", s[0])
	}
	_, err := c.conn.Write(s[1 : 1+rtmpHandshakeSize])
	return err
}

// serverHandshake is the other side (used by tests).
func (c *rtmpConn) serverHandshake() error {
	c0c1 := make([]byte, 1+rtmpHandshakeSize)
	if _, err := io.ReadFull(c.r, c0c1); err != nil {
		return err
	}
	s := make([]byte, 1+2*rtmpHandshakeSize)
	s[0] = 3
	if _, err := rand.Read(s[9 : 1+rtmpHandshakeSize]); err != nil {
		return err
	}
	copy(s[1+rtmpHandshakeSize:], c0c1[1:])
	if _, err := c.conn.Write(s); err != nil {
		return err
	}
	_, err := io.ReadFull(c.r, make([]byte, rtmpHandshakeSize))
	return err
}

func (c *rtmpConn) readMessage() (rtmpMessage, error) {
	for {
		b0, err := c.r.ReadByte()
		if err != nil {
			return rtmpMessage{}, err
		}
		format := b0 >> 6
		csid := uint32(b0 & 0x3f)
		switch csid {
		case 0:
			b, err := c.r.ReadByte()
			if err != nil {
				return rtmpMessage{}, err
			}
			csid = 64 + uint32(b)
		case 1:
			var b [2]byte
			if _, err := io.ReadFull(c.r, b[:]); err != nil {
				return rtmpMessage{}, err
			}
			csid = 64 + uint32(b[0]) + uint32(b[1])*256
		}
		state := c.in[csid]
		if state == nil {
			if format != 0 {
				return rtmpMessage{}, fmt.Errorf("RTMP: chunk stream %d starts without a full header", csid)
			}
			state = &rtmpChunkState{}
			c.in[csid] = state
		}
		var header [11]byte
		size := [4]int{11, 7, 3, 0}[format]
		if _, err := io.ReadFull(c.r, header[:size]); err != nil {
			return rtmpMessage{}, err
		}
		newMessage := len(state.buf) == 0
		var field uint32
		if format < 3 {
			field = uint32(header[0])<<16 | uint32(header[1])<<8 | uint32(header[2])
			state.extended = field == 0xFFFFFF
		}
		if format <= 1 {
			state.length = uint32(header[3])<<16 | uint32(header[4])<<8 | uint32(header[5])
			state.typ = header[6]
			if state.length > rtmpMaxMessage {
				return rtmpMessage{}, fmt.Errorf("RTMP: message of %d bytes is too large", state.length)
			}
		}
		if format == 0 {
			state.streamID = binary.LittleEndian.Uint32(header[7:11])
		}
		if state.extended {
			var ext [4]byte
			if _, err := io.ReadFull(c.r, ext[:]); err != nil {
				return rtmpMessage{}, err
			}
			if format < 3 {
				field = binary.BigEndian.Uint32(ext[:])
			}
		}
		switch format {
		case 0:
			state.timestamp, state.delta = field, 0
		case 1, 2:
			state.delta = field
			state.timestamp += field
		case 3:
			if newMessage && state.started {
				state.timestamp += state.delta
			}
		}
		state.started = true
		remaining := state.length - uint32(len(state.buf))
		if remaining > c.inChunk {
			remaining = c.inChunk
		}
		chunk := make([]byte, remaining)
		if _, err := io.ReadFull(c.r, chunk); err != nil {
			return rtmpMessage{}, err
		}
		state.buf = append(state.buf, chunk...)
		if uint32(len(state.buf)) < state.length {
			continue
		}
		message := rtmpMessage{Type: state.typ, StreamID: state.streamID, Timestamp: state.timestamp, Payload: state.buf}
		state.buf = nil
		if message.Type == rtmpTypeSetChunkSize && len(message.Payload) >= 4 {
			size := binary.BigEndian.Uint32(message.Payload) & 0x7fffffff
			if size < 1 || size > rtmpMaxMessage {
				return rtmpMessage{}, fmt.Errorf("RTMP: invalid chunk size %d", size)
			}
			c.inChunk = size
		}
		return message, nil
	}
}

// writeMessage writes one message as chunks: a type-0 header, then type-3
// continuation headers.
func (c *rtmpConn) writeMessage(csid uint32, message rtmpMessage) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	extended := message.Timestamp >= 0xFFFFFF
	stamp := message.Timestamp
	if extended {
		stamp = 0xFFFFFF
	}
	out := make([]byte, 0, len(message.Payload)+16+len(message.Payload)/int(c.outChunk)*5)
	out = append(out, byte(csid&0x3f), byte(stamp>>16), byte(stamp>>8), byte(stamp))
	length := len(message.Payload)
	out = append(out, byte(length>>16), byte(length>>8), byte(length), message.Type)
	out = binary.LittleEndian.AppendUint32(out, message.StreamID)
	if extended {
		out = binary.BigEndian.AppendUint32(out, message.Timestamp)
	}
	for offset := 0; ; {
		end := offset + int(c.outChunk)
		if end > length {
			end = length
		}
		out = append(out, message.Payload[offset:end]...)
		offset = end
		if offset >= length {
			break
		}
		out = append(out, 0xC0|byte(csid&0x3f))
		if extended {
			out = binary.BigEndian.AppendUint32(out, message.Timestamp)
		}
	}
	_, err := c.conn.Write(out)
	return err
}

func (c *rtmpConn) setChunkSize(size uint32) error {
	payload := binary.BigEndian.AppendUint32(nil, size)
	if err := c.writeMessage(rtmpCSControl, rtmpMessage{Type: rtmpTypeSetChunkSize, Payload: payload}); err != nil {
		return err
	}
	c.outChunk = size
	return nil
}

func (c *rtmpConn) command(csid, streamID uint32, values ...any) error {
	payload, err := amf0Encode(values...)
	if err != nil {
		return err
	}
	return c.writeMessage(csid, rtmpMessage{Type: rtmpTypeCommandAMF0, StreamID: streamID, Payload: payload})
}

// handleControl answers the protocol messages a client must answer: peer
// bandwidth (with a window ack size) and pings.
func (c *rtmpConn) handleControl(message rtmpMessage) {
	switch message.Type {
	case rtmpTypePeerBW:
		if len(message.Payload) >= 4 {
			_ = c.writeMessage(rtmpCSControl, rtmpMessage{Type: rtmpTypeWindowAck, Payload: message.Payload[:4]})
		}
	case rtmpTypeUserControl:
		if len(message.Payload) >= 6 && binary.BigEndian.Uint16(message.Payload) == 6 {
			reply := append([]byte{0, 7}, message.Payload[2:6]...)
			_ = c.writeMessage(rtmpCSControl, rtmpMessage{Type: rtmpTypeUserControl, Payload: reply})
		}
	}
}

// ---------------------------------------------------------------------------
// Publisher
// ---------------------------------------------------------------------------

type rtmpPublisher struct {
	c        *rtmpConn
	streamID uint32
	done     chan struct{}
	errMu    sync.Mutex
	err      error
}

// rtmpCommandResult waits for the _result/_error of a transaction, handling
// control messages on the way.
func (c *rtmpConn) awaitResult(txn float64) ([]any, error) {
	for {
		message, err := c.readMessage()
		if err != nil {
			return nil, fmt.Errorf("RTMP: %w", err)
		}
		c.handleControl(message)
		if message.Type != rtmpTypeCommandAMF0 {
			continue
		}
		values, err := amf0Decode(message.Payload)
		if err != nil || len(values) < 2 {
			continue
		}
		name, _ := values[0].(string)
		id, _ := values[1].(float64)
		if id != txn {
			continue
		}
		switch name {
		case "_result":
			return values, nil
		case "_error":
			return nil, fmt.Errorf("the server refused the request (%s)", rtmpStatusCode(values))
		}
	}
}

// rtmpStatusCode returns the info object's code (never its description, which
// a server might build from the stream name).
func rtmpStatusCode(values []any) string {
	for _, value := range values {
		if info, ok := value.(map[string]any); ok {
			if code, ok := info["code"].(string); ok && code != "" {
				return terminalSafe(code)
			}
		}
	}
	return "no status code"
}

// publishRTMP runs the publish setup on an open connection: handshake,
// connect (app, tcUrl), createStream and publish(key). It returns once the
// server answered NetStream.Publish.Start.
func publishRTMP(conn net.Conn, app, tcURL, key string) (*rtmpPublisher, error) {
	_ = conn.SetDeadline(time.Now().Add(rtmpSetupTimeout))
	c := newRTMPConn(conn)
	if err := c.clientHandshake(); err != nil {
		return nil, err
	}
	if err := c.setChunkSize(rtmpOutChunkSize); err != nil {
		return nil, err
	}
	connect := map[string]any{
		"app": app, "type": "nonprivate", "flashVer": "FMLE/3.0 (compatible; hubfly-cli)", "tcUrl": tcURL,
	}
	if err := c.command(rtmpCSCommand, 0, "connect", 1.0, connect); err != nil {
		return nil, err
	}
	if _, err := c.awaitResult(1); err != nil {
		return nil, fmt.Errorf("RTMP connect: %w", err)
	}
	// releaseStream and FCPublish as OBS and ffmpeg send them; their results
	// are not needed.
	_ = c.command(rtmpCSCommand, 0, "releaseStream", 2.0, nil, key)
	_ = c.command(rtmpCSCommand, 0, "FCPublish", 3.0, nil, key)
	if err := c.command(rtmpCSCommand, 0, "createStream", 4.0, nil); err != nil {
		return nil, err
	}
	result, err := c.awaitResult(4)
	if err != nil {
		return nil, fmt.Errorf("RTMP createStream: %w", err)
	}
	streamID := uint32(1)
	if len(result) >= 4 {
		if id, ok := result[3].(float64); ok {
			streamID = uint32(id)
		}
	}
	if err := c.command(rtmpCSStream, streamID, "publish", 5.0, nil, key, "live"); err != nil {
		return nil, err
	}
	for {
		message, err := c.readMessage()
		if err != nil {
			return nil, fmt.Errorf("RTMP publish: %w", err)
		}
		c.handleControl(message)
		if message.Type != rtmpTypeCommandAMF0 {
			continue
		}
		values, err := amf0Decode(message.Payload)
		if err != nil || len(values) == 0 {
			continue
		}
		name, _ := values[0].(string)
		if name == "_error" {
			return nil, fmt.Errorf("the server refused to publish (%s)", rtmpStatusCode(values))
		}
		if name != "onStatus" {
			continue
		}
		code := rtmpStatusCode(values)
		if code == "NetStream.Publish.Start" {
			break
		}
		if strings.Contains(code, "BadName") || strings.Contains(code, "Denied") || strings.Contains(code, "Failed") || strings.Contains(code, "Rejected") {
			return nil, fmt.Errorf("the server refused to publish (%s): the key, the input or its protocols do not allow it", code)
		}
	}
	_ = conn.SetDeadline(time.Time{})
	p := &rtmpPublisher{c: c, streamID: streamID, done: make(chan struct{})}
	go p.readLoop()
	return p, nil
}

// readLoop keeps reading what the server sends while media flows: pings,
// chunk sizes, and an onStatus that ends the publish.
func (p *rtmpPublisher) readLoop() {
	defer close(p.done)
	for {
		message, err := p.c.readMessage()
		if err != nil {
			p.fail(fmt.Errorf("RTMP connection closed: %w", err))
			return
		}
		p.c.handleControl(message)
		if message.Type != rtmpTypeCommandAMF0 {
			continue
		}
		values, err := amf0Decode(message.Payload)
		if err != nil || len(values) == 0 {
			continue
		}
		if name, _ := values[0].(string); name == "onStatus" {
			code := rtmpStatusCode(values)
			if strings.Contains(code, "Unpublish") || strings.Contains(code, "Failed") || strings.Contains(code, "Denied") || strings.Contains(code, "BadName") {
				p.fail(fmt.Errorf("the server ended the publish (%s)", code))
				return
			}
		}
	}
}

func (p *rtmpPublisher) fail(err error) {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	if p.err == nil {
		p.err = err
	}
}

func (p *rtmpPublisher) failure() error {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	return p.err
}

// WriteTag sends one FLV tag as an RTMP message.
func (p *rtmpPublisher) WriteTag(tagType byte, timestamp uint32, data []byte) error {
	if err := p.failure(); err != nil {
		return err
	}
	csid := uint32(rtmpCSAudio)
	switch tagType {
	case rtmpTypeAudio:
	case rtmpTypeVideo:
		csid = rtmpCSVideo
	case rtmpTypeDataAMF0:
		prefix, _ := amf0Encode("@setDataFrame")
		data = append(prefix, data...)
	default:
		return nil
	}
	return p.c.writeMessage(csid, rtmpMessage{Type: tagType, StreamID: p.streamID, Timestamp: timestamp, Payload: data})
}

// Close ends the publish politely and closes the connection.
func (p *rtmpPublisher) Close() error {
	_ = p.c.command(rtmpCSStream, p.streamID, "FCUnpublish", 6.0, nil)
	_ = p.c.command(rtmpCSCommand, 0, "deleteStream", 7.0, nil, float64(p.streamID))
	return p.c.conn.Close()
}

// ---------------------------------------------------------------------------
// FLV
// ---------------------------------------------------------------------------

type flvTag struct {
	Type      byte
	Timestamp uint32
	Data      []byte
}

type flvReader struct {
	r       *bufio.Reader
	started bool
}

func newFLVReader(r io.Reader) *flvReader { return &flvReader{r: bufio.NewReaderSize(r, 64<<10)} }

// next returns the next tag, io.EOF at the end of the stream.
func (f *flvReader) next() (flvTag, error) {
	if !f.started {
		header := make([]byte, 9)
		if _, err := io.ReadFull(f.r, header); err != nil {
			return flvTag{}, err
		}
		if string(header[:3]) != "FLV" {
			return flvTag{}, errors.New("ffmpeg did not write FLV")
		}
		offset := binary.BigEndian.Uint32(header[5:9])
		if offset < 9 || offset > 1024 {
			return flvTag{}, errors.New("invalid FLV header")
		}
		if _, err := io.CopyN(io.Discard, f.r, int64(offset-9)+4); err != nil {
			return flvTag{}, err
		}
		f.started = true
	}
	var header [11]byte
	if _, err := io.ReadFull(f.r, header[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return flvTag{}, io.EOF
		}
		return flvTag{}, err
	}
	size := uint32(header[1])<<16 | uint32(header[2])<<8 | uint32(header[3])
	timestamp := uint32(header[4])<<16 | uint32(header[5])<<8 | uint32(header[6]) | uint32(header[7])<<24
	data := make([]byte, size)
	if _, err := io.ReadFull(f.r, data); err != nil {
		return flvTag{}, fmt.Errorf("truncated FLV tag: %w", err)
	}
	if _, err := io.CopyN(io.Discard, f.r, 4); err != nil && !errors.Is(err, io.EOF) {
		return flvTag{}, err
	}
	return flvTag{Type: header[0] & 0x1f, Timestamp: timestamp, Data: data}, nil
}

// ---------------------------------------------------------------------------
// AMF0
// ---------------------------------------------------------------------------

func amf0Encode(values ...any) ([]byte, error) {
	var out []byte
	for _, value := range values {
		var err error
		if out, err = amf0Append(out, value); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func amf0Append(out []byte, value any) ([]byte, error) {
	switch typed := value.(type) {
	case nil:
		return append(out, 0x05), nil
	case float64:
		out = append(out, 0x00)
		return binary.BigEndian.AppendUint64(out, math.Float64bits(typed)), nil
	case int:
		return amf0Append(out, float64(typed))
	case bool:
		if typed {
			return append(out, 0x01, 1), nil
		}
		return append(out, 0x01, 0), nil
	case string:
		if len(typed) > 0xFFFF {
			out = append(out, 0x0C)
			out = binary.BigEndian.AppendUint32(out, uint32(len(typed)))
			return append(out, typed...), nil
		}
		out = append(out, 0x02)
		out = binary.BigEndian.AppendUint16(out, uint16(len(typed)))
		return append(out, typed...), nil
	case map[string]any:
		out = append(out, 0x03)
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			out = binary.BigEndian.AppendUint16(out, uint16(len(key)))
			out = append(out, key...)
			var err error
			if out, err = amf0Append(out, typed[key]); err != nil {
				return nil, err
			}
		}
		return append(out, 0, 0, 0x09), nil
	}
	return nil, fmt.Errorf("amf0: unsupported value %T", value)
}

func amf0Decode(data []byte) ([]any, error) {
	values := []any{}
	for len(data) > 0 {
		value, rest, err := amf0Value(data, 0)
		if err != nil {
			return values, err
		}
		values = append(values, value)
		data = rest
	}
	return values, nil
}

var errAMF0Short = errors.New("amf0: truncated value")

func amf0Value(data []byte, depth int) (any, []byte, error) {
	if len(data) == 0 || depth > 32 {
		return nil, nil, errAMF0Short
	}
	marker, data := data[0], data[1:]
	switch marker {
	case 0x00:
		if len(data) < 8 {
			return nil, nil, errAMF0Short
		}
		return math.Float64frombits(binary.BigEndian.Uint64(data)), data[8:], nil
	case 0x01:
		if len(data) < 1 {
			return nil, nil, errAMF0Short
		}
		return data[0] != 0, data[1:], nil
	case 0x02, 0x0C:
		width := 2
		if marker == 0x0C {
			width = 4
		}
		if len(data) < width {
			return nil, nil, errAMF0Short
		}
		size := int(binary.BigEndian.Uint16(data))
		if width == 4 {
			size = int(binary.BigEndian.Uint32(data))
		}
		data = data[width:]
		if size < 0 || len(data) < size {
			return nil, nil, errAMF0Short
		}
		return string(data[:size]), data[size:], nil
	case 0x03, 0x08:
		if marker == 0x08 {
			if len(data) < 4 {
				return nil, nil, errAMF0Short
			}
			data = data[4:]
		}
		object := map[string]any{}
		for {
			if len(data) < 3 {
				return nil, nil, errAMF0Short
			}
			size := int(binary.BigEndian.Uint16(data))
			if size == 0 && data[2] == 0x09 {
				return object, data[3:], nil
			}
			data = data[2:]
			if len(data) < size {
				return nil, nil, errAMF0Short
			}
			key := string(data[:size])
			value, rest, err := amf0Value(data[size:], depth+1)
			if err != nil {
				return nil, nil, err
			}
			object[key] = value
			data = rest
		}
	case 0x05, 0x06:
		return nil, data, nil
	case 0x0A:
		if len(data) < 4 {
			return nil, nil, errAMF0Short
		}
		count := int(binary.BigEndian.Uint32(data))
		data = data[4:]
		list := []any{}
		for index := 0; index < count; index++ {
			value, rest, err := amf0Value(data, depth+1)
			if err != nil {
				return nil, nil, err
			}
			list = append(list, value)
			data = rest
		}
		return list, data, nil
	case 0x0B:
		if len(data) < 10 {
			return nil, nil, errAMF0Short
		}
		return math.Float64frombits(binary.BigEndian.Uint64(data)), data[10:], nil
	}
	return nil, nil, fmt.Errorf("amf0: unsupported marker 0x%02x", marker)
}
