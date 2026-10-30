package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	srt "github.com/datarhei/gosrt"
)

// ---------------------------------------------------------------------------
// ffmpeg arguments
// ---------------------------------------------------------------------------

func TestBuildStreamPushArgsNeverHoldTheKey(t *testing.T) {
	cases := []struct {
		name string
		opts streamPushOptions
		goos string
		want []string
	}{
		{"file srt loop", streamPushOptions{Source: "show.mp4", Protocol: "srt", Loop: true, BitrateKbps: 4500, AudioKbps: 160}, "linux",
			[]string{"-stream_loop", "-1", "-re", "-i", "show.mp4", "-f", "mpegts", "pipe:1"}},
		{"file rtmps", streamPushOptions{Source: "show.mp4", Protocol: "rtmps", BitrateKbps: 6000, AudioKbps: 160}, "linux",
			[]string{"-re", "-i", "show.mp4", "-b:v", "6000k", "-bufsize", "12000k", "-f", "flv", "pipe:1"}},
		{"v4l2", streamPushOptions{Source: "device", Protocol: "srt", BitrateKbps: 4500, AudioKbps: 160}, "linux",
			[]string{"-f", "v4l2", "-i", "/dev/video0", "anullsrc=channel_layout=stereo:sample_rate=48000", "-map", "1:a:0"}},
		{"avfoundation", streamPushOptions{Source: "device", Protocol: "srt", BitrateKbps: 4500, AudioKbps: 160}, "darwin",
			[]string{"-f", "avfoundation", "-framerate", "30", "-i", "0:0"}},
		{"dshow", streamPushOptions{Source: "device:video=Integrated Camera", Protocol: "rtmps", BitrateKbps: 4500, AudioKbps: 160}, "windows",
			[]string{"-f", "dshow", "-i", "video=Integrated Camera", "anullsrc=channel_layout=stereo:sample_rate=48000"}},
	}
	for _, tt := range cases {
		args, err := buildStreamPushArgs(tt.opts, tt.goos)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		joined := "\x00" + strings.Join(args, "\x00") + "\x00"
		if !strings.Contains(joined, "\x00"+strings.Join(tt.want, "\x00")+"\x00") && !containsInOrder(args, tt.want) {
			t.Fatalf("%s: args %q lack %q", tt.name, args, tt.want)
		}
		for _, required := range []string{"-nostdin", "-bf", "expr:gte(t,n_forced*2)", "48000"} {
			if !strings.Contains(joined, "\x00"+required+"\x00") {
				t.Fatalf("%s: args %q lack %q", tt.name, args, required)
			}
		}
		if args[len(args)-1] != "pipe:1" || strings.Contains(joined, "hfls_") || strings.Contains(joined, "srt://") || strings.Contains(joined, "rtmp") {
			t.Fatalf("%s: args must end with pipe:1 and hold no key or server URL: %q", tt.name, args)
		}
	}
	if _, err := buildStreamPushArgs(streamPushOptions{Source: "device", Protocol: "srt", BitrateKbps: 4500}, "windows"); err == nil || !strings.Contains(err.Error(), "list_devices") {
		t.Fatalf("dshow without a name: %v", err)
	}
	if _, err := buildStreamPushArgs(streamPushOptions{Source: "device", Protocol: "srt", Loop: true}, "linux"); err == nil {
		t.Fatal("--loop with a device was accepted")
	}
}

// containsInOrder reports whether want appears in args in order (not
// necessarily adjacent).
func containsInOrder(args, want []string) bool {
	index := 0
	for _, arg := range args {
		if index < len(want) && arg == want[index] {
			index++
		}
	}
	return index == len(want)
}

func TestStreamPushInstallHintPerPlatform(t *testing.T) {
	for goos, want := range map[string]string{"darwin": "brew install ffmpeg", "windows": "winget install", "linux": "apt install ffmpeg"} {
		if hint := streamPushInstallHint(goos); !strings.Contains(hint, want) {
			t.Fatalf("hint(%s) = %q", goos, hint)
		}
	}
}

func TestStreamPushWithoutFFmpegExplainsTheInstall(t *testing.T) {
	env := setupStreamTest(t)
	video := filepath.Join(t.TempDir(), "show.mp4")
	_ = os.WriteFile(video, []byte("x"), 0o600)
	streamLookPath = func(string) (string, error) { return "", os.ErrNotExist }
	streamGOOS = "darwin"
	err := run([]string{"stream", "push", "live-show", video})
	if err == nil || !strings.Contains(err.Error(), "ffmpeg was not found") || !strings.Contains(err.Error(), "brew install ffmpeg") {
		t.Fatalf("missing ffmpeg error = %v", err)
	}
	if raw := env.dash.allRaw(); strings.Contains(raw, "reveal") {
		t.Fatalf("a key was revealed without ffmpeg:\n%s", raw)
	}
}

func TestStreamPushDryRunPrintsArgvWithoutAKey(t *testing.T) {
	env := setupStreamTest(t)
	video := filepath.Join(t.TempDir(), "my show.mp4")
	_ = os.WriteFile(video, []byte("x"), 0o600)
	streamLookPath = func(string) (string, error) { return "/usr/bin/ffmpeg", nil }
	mustRun(t, "stream", "push", "live-show", video, "--loop", "--dry-run")
	out := env.out.String()
	if !strings.HasPrefix(out, "/usr/bin/ffmpeg -hide_banner") || !strings.Contains(out, "'"+video+"'") || !strings.Contains(out, "pipe:1") ||
		!strings.Contains(out, "srt://live-1a2b.live.hubfly.test:9000 (streamid publish:<key>)") {
		t.Fatalf("dry-run output:\n%s", out)
	}
	assertNoStreamSecret(t, "dry-run", out)
	if len(env.dash.find("POST", streamTestAPI+"/inputs/sin_cam1abcd/keys/reveal")) != 0 {
		t.Fatal("dry-run revealed the key")
	}
}

func TestStreamPushRefusesInputsWithoutTheProtocol(t *testing.T) {
	env := setupStreamTest(t)
	input := streamTestInput()
	input["protocols"] = []string{"rtmps"}
	env.dash.ok("GET", streamTestAPI+"/inputs", []map[string]any{input})
	video := filepath.Join(t.TempDir(), "show.mp4")
	_ = os.WriteFile(video, []byte("x"), 0o600)
	streamLookPath = func(string) (string, error) { return "/usr/bin/ffmpeg", nil }
	err := run([]string{"stream", "push", "live-show", video})
	if err == nil || !strings.Contains(err.Error(), "--protocols rtmps,srt") {
		t.Fatalf("protocol error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Fake ffmpeg
// ---------------------------------------------------------------------------

type fakeFFmpeg struct {
	path, argv, env string
}

// newFakeFFmpeg writes a shell script that records its argv and environment
// and writes payload to stdout, like ffmpeg encoding to pipe:1.
func newFakeFFmpeg(t *testing.T, payload []byte) fakeFFmpeg {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake ffmpeg is a shell script")
	}
	dir := t.TempDir()
	fake := fakeFFmpeg{path: filepath.Join(dir, "ffmpeg"), argv: filepath.Join(dir, "argv"), env: filepath.Join(dir, "env")}
	if err := os.WriteFile(filepath.Join(dir, "payload"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$0\" \"$@\" > '%s'\nenv > '%s'\ncat '%s'\nsleep 0.5\n", fake.argv, fake.env, filepath.Join(dir, "payload"))
	if err := os.WriteFile(fake.path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return fake
}

func (f fakeFFmpeg) assertNoKey(t *testing.T) {
	t.Helper()
	for _, file := range []string{f.argv, f.env} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("the fake ffmpeg did not run: %v", err)
		}
		if strings.Contains(string(data), streamTestSecret) || strings.Contains(string(data), "hfls_") {
			t.Fatalf("the stream key reached ffmpeg (%s):\n%s", filepath.Base(file), data)
		}
	}
}

func setupStreamPushTest(t *testing.T, payload []byte) (streamTestEnv, fakeFFmpeg, string) {
	t.Helper()
	env := setupStreamTest(t)
	env.dash.ok("GET", streamTestAPI, streamTestServer("127.0.0.1"))
	fake := newFakeFFmpeg(t, payload)
	streamLookPath = func(string) (string, error) { return fake.path, nil }
	streamPushContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 20*time.Second)
	}
	origSRT, origRTMP, origTLS, origLatency := streamSRTPort, streamRTMPSPort, streamRTMPUseTLS, streamSRTLatency
	t.Cleanup(func() {
		streamSRTPort, streamRTMPSPort, streamRTMPUseTLS, streamSRTLatency = origSRT, origRTMP, origTLS, origLatency
	})
	video := filepath.Join(t.TempDir(), "show.mp4")
	_ = os.WriteFile(video, []byte("x"), 0o600)
	return env, fake, video
}

// ---------------------------------------------------------------------------
// SRT
// ---------------------------------------------------------------------------

func TestStreamPushSRTCarriesTheKeyOutsideFFmpeg(t *testing.T) {
	payload := bytes.Repeat(append([]byte{0x47}, bytes.Repeat([]byte{0xAB}, streamTSPacket-1)...), 7*20)
	env, fake, video := setupStreamPushTest(t, payload)
	streamSRTLatency = 120 * time.Millisecond
	config := srt.DefaultConfig()
	config.Latency = 120 * time.Millisecond
	config.PayloadSize = streamSRTPayload
	ln, err := srt.Listen("srt", "127.0.0.1:0", config)
	if err != nil {
		t.Skipf("no local UDP for SRT: %v", err)
	}
	defer ln.Close()
	streamSRTPort = ln.Addr().(*net.UDPAddr).Port

	var mu sync.Mutex
	streamIDs := []string{}
	received := 0
	go func() {
		for {
			req, err := ln.Accept2()
			if err != nil {
				return
			}
			mu.Lock()
			streamIDs = append(streamIDs, req.StreamId())
			mu.Unlock()
			conn, err := req.Accept()
			if err != nil {
				continue
			}
			go func() {
				buf := make([]byte, 2048)
				for {
					n, err := conn.Read(buf)
					mu.Lock()
					received += n
					mu.Unlock()
					if err != nil {
						return
					}
				}
			}()
		}
	}()

	mustRun(t, "stream", "push", "live-show", video, "--input", "Camera")
	fake.assertNoKey(t)
	waitForCondition(t, "SRT bytes at the server", func() bool { mu.Lock(); defer mu.Unlock(); return received > 0 })
	mu.Lock()
	ids := append([]string(nil), streamIDs...)
	mu.Unlock()
	if !reflect.DeepEqual(ids, []string{"publish:" + streamTestKey}) {
		t.Fatalf("SRT streamids = %q", ids)
	}
	assertNoStreamSecret(t, "push output", env.out.String()+env.errOut.String())
	if !strings.Contains(env.errOut.String(), "over SRT") {
		t.Fatalf("push stderr:\n%s", env.errOut.String())
	}
}

func TestStreamPushSRTRefusalNeverStartsFFmpeg(t *testing.T) {
	env, fake, video := setupStreamPushTest(t, []byte("x"))
	streamSRTLatency = 120 * time.Millisecond
	ln, err := srt.Listen("srt", "127.0.0.1:0", srt.DefaultConfig())
	if err != nil {
		t.Skipf("no local UDP for SRT: %v", err)
	}
	defer ln.Close()
	streamSRTPort = ln.Addr().(*net.UDPAddr).Port
	go func() {
		for {
			req, err := ln.Accept2()
			if err != nil {
				return
			}
			req.Reject(srt.REJX_UNAUTHORIZED)
		}
	}()
	err = run([]string{"stream", "push", "live-show", video})
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("refused push = %v", err)
	}
	assertNoStreamSecret(t, "refusal", err.Error()+env.out.String()+env.errOut.String())
	if _, statErr := os.Stat(fake.argv); statErr == nil {
		t.Fatal("ffmpeg started although the server refused the key")
	}
}

// ---------------------------------------------------------------------------
// RTMP(S)
// ---------------------------------------------------------------------------

// buildFLV writes an FLV stream (header, tags, previous-tag sizes).
func buildFLV(tags []flvTag) []byte {
	out := []byte{'F', 'L', 'V', 1, 5, 0, 0, 0, 9, 0, 0, 0, 0}
	for _, tag := range tags {
		size := len(tag.Data)
		out = append(out, tag.Type, byte(size>>16), byte(size>>8), byte(size),
			byte(tag.Timestamp>>16), byte(tag.Timestamp>>8), byte(tag.Timestamp), byte(tag.Timestamp>>24), 0, 0, 0)
		out = append(out, tag.Data...)
		out = binary.BigEndian.AppendUint32(out, uint32(11+size))
	}
	return out
}

func testFLVTags() []flvTag {
	meta, _ := amf0Encode("onMetaData", map[string]any{"width": 1280.0, "height": 720.0})
	video := bytes.Repeat([]byte{0x17, 0x01, 0x02, 0x03}, 2500) // 10000 bytes: several chunks
	return []flvTag{
		{Type: rtmpTypeDataAMF0, Timestamp: 0, Data: meta},
		{Type: rtmpTypeVideo, Timestamp: 0, Data: video},
		{Type: rtmpTypeAudio, Timestamp: 21, Data: bytes.Repeat([]byte{0xAF, 0x01}, 150)},
		{Type: rtmpTypeVideo, Timestamp: 0x01000005, Data: bytes.Repeat([]byte{0x27}, 5000)}, // extended timestamp
	}
}

// fakeRTMPServer is a minimal RTMP ingest: handshake, connect, createStream,
// publish (accepted or refused) and media capture.
type fakeRTMPServer struct {
	ln       net.Listener
	refuse   bool
	mu       sync.Mutex
	connect  map[string]any
	names    []string
	media    []rtmpMessage
	commands []string
	pongs    int
}

func newFakeRTMPServer(t *testing.T, refuse bool) *fakeRTMPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeRTMPServer{ln: ln, refuse: refuse}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

func (s *fakeRTMPServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *fakeRTMPServer) serve(conn net.Conn) {
	defer conn.Close()
	c := newRTMPConn(conn)
	if c.serverHandshake() != nil {
		return
	}
	// Exercise the client's control handling: a smaller chunk size, peer
	// bandwidth and a ping.
	_ = c.setChunkSize(300)
	_ = c.writeMessage(rtmpCSControl, rtmpMessage{Type: rtmpTypePeerBW, Payload: []byte{0, 0x26, 0x25, 0xA0, 2}})
	_ = c.writeMessage(rtmpCSControl, rtmpMessage{Type: rtmpTypeUserControl, Payload: []byte{0, 6, 0, 0, 0, 42}})
	for {
		message, err := c.readMessage()
		if err != nil {
			return
		}
		switch message.Type {
		case rtmpTypeUserControl:
			if binary.BigEndian.Uint16(message.Payload) == 7 {
				s.mu.Lock()
				s.pongs++
				s.mu.Unlock()
			}
		case rtmpTypeAudio, rtmpTypeVideo, rtmpTypeDataAMF0:
			s.mu.Lock()
			s.media = append(s.media, message)
			s.mu.Unlock()
		case rtmpTypeCommandAMF0:
			values, _ := amf0Decode(message.Payload)
			name, _ := values[0].(string)
			txn, _ := values[1].(float64)
			s.mu.Lock()
			s.commands = append(s.commands, name)
			s.mu.Unlock()
			switch name {
			case "connect":
				s.mu.Lock()
				s.connect, _ = values[2].(map[string]any)
				s.mu.Unlock()
				_ = c.command(rtmpCSCommand, 0, "_result", txn, map[string]any{"fmsVer": "FMS/3,0"}, map[string]any{"code": "NetConnection.Connect.Success"})
			case "createStream":
				_ = c.command(rtmpCSCommand, 0, "_result", txn, nil, 1.0)
			case "publish":
				publishName, _ := values[3].(string)
				s.mu.Lock()
				s.names = append(s.names, publishName)
				s.mu.Unlock()
				code := "NetStream.Publish.Start"
				if s.refuse {
					code = "NetStream.Publish.BadName"
				}
				// A server may echo the stream name in its description.
				_ = c.command(rtmpCSStream, 1, "onStatus", 0.0, nil, map[string]any{"level": "status", "code": code, "description": "publishing " + publishName})
			}
		}
	}
}

func (s *fakeRTMPServer) snapshot() (map[string]any, []string, []rtmpMessage, []string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connect, append([]string(nil), s.names...), append([]rtmpMessage(nil), s.media...), append([]string(nil), s.commands...), s.pongs
}

func TestStreamPushRTMPSCarriesTheKeyOutsideFFmpeg(t *testing.T) {
	tags := testFLVTags()
	env, fake, video := setupStreamPushTest(t, buildFLV(tags))
	server := newFakeRTMPServer(t, false)
	streamRTMPUseTLS, streamRTMPSPort = false, server.port()

	mustRun(t, "stream", "push", "live-show", video, "--protocol", "rtmps")
	fake.assertNoKey(t)
	waitForCondition(t, "media at the RTMP server", func() bool {
		_, _, media, _, _ := server.snapshot()
		return len(media) == len(tags)
	})
	connect, names, media, commands, pongs := server.snapshot()
	if connect["app"] != "live" || connect["tcUrl"] != fmt.Sprintf("rtmp://127.0.0.1:%d/live", server.port()) {
		t.Fatalf("connect = %v", connect)
	}
	if !reflect.DeepEqual(names, []string{streamTestKey}) || pongs != 1 {
		t.Fatalf("publish names = %q, pongs = %d", names, pongs)
	}
	for _, want := range []string{"connect", "releaseStream", "FCPublish", "createStream", "publish"} {
		if !containsInOrder(commands, []string{want}) {
			t.Fatalf("commands %q lack %q", commands, want)
		}
	}
	setDataFrame, _ := amf0Encode("@setDataFrame")
	for index, tag := range tags {
		got := media[index]
		want := tag.Data
		if tag.Type == rtmpTypeDataAMF0 {
			want = append(append([]byte(nil), setDataFrame...), tag.Data...)
		}
		if got.Type != tag.Type || got.Timestamp != tag.Timestamp || got.StreamID != 1 || !bytes.Equal(got.Payload, want) {
			t.Fatalf("message %d = type %d ts %d sid %d (%d bytes), want type %d ts %d (%d bytes)", index, got.Type, got.Timestamp, got.StreamID, len(got.Payload), tag.Type, tag.Timestamp, len(want))
		}
	}
	assertNoStreamSecret(t, "push output", env.out.String()+env.errOut.String())
}

func TestStreamPushRTMPSRefusalHidesTheKey(t *testing.T) {
	env, fake, video := setupStreamPushTest(t, buildFLV(testFLVTags()))
	server := newFakeRTMPServer(t, true)
	streamRTMPUseTLS, streamRTMPSPort = false, server.port()
	err := run([]string{"stream", "push", "live-show", video, "--protocol", "rtmps"})
	if err == nil || !strings.Contains(err.Error(), "NetStream.Publish.BadName") {
		t.Fatalf("refused push = %v", err)
	}
	assertNoStreamSecret(t, "refusal", err.Error()+env.out.String()+env.errOut.String())
	if _, statErr := os.Stat(fake.argv); statErr == nil {
		t.Fatal("ffmpeg started although the server refused the key")
	}
}

func TestStreamPushKeyFromStdin(t *testing.T) {
	env, fake, video := setupStreamPushTest(t, buildFLV(testFLVTags()))
	server := newFakeRTMPServer(t, false)
	streamRTMPUseTLS, streamRTMPSPort = false, server.port()
	rtStdin = strings.NewReader(streamTestKey + "\n")
	rtStdinIsTerminal = func() bool { return false }
	mustRun(t, "stream", "push", "live-show", video, "--protocol", "rtmps", "--key-stdin")
	fake.assertNoKey(t)
	if len(env.dash.find("POST", streamTestAPI+"/inputs/sin_cam1abcd/keys/reveal")) != 0 {
		t.Fatal("--key-stdin still revealed the key")
	}
	waitForCondition(t, "the publish", func() bool { _, names, _, _, _ := server.snapshot(); return len(names) == 1 })
	if _, names, _, _, _ := server.snapshot(); names[0] != streamTestKey {
		t.Fatalf("publish name = %q", names[0])
	}
}

// ---------------------------------------------------------------------------
// RTMP chunk stream and AMF0
// ---------------------------------------------------------------------------

func TestRTMPReadMessageHandlesHeaderFormats(t *testing.T) {
	var raw []byte
	raw = append(raw, 0x04, 0, 0, 100, 0, 0, 3, rtmpTypeAudio, 1, 0, 0, 0) // fmt 0, ts 100
	raw = append(raw, "abc"...)
	raw = append(raw, 0x84, 0, 0, 20) // fmt 2: delta 20
	raw = append(raw, "def"...)
	raw = append(raw, 0xC4) // fmt 3: same delta
	raw = append(raw, "ghi"...)
	raw = append(raw, 0x44, 0, 0, 5, 0, 0, 2, rtmpTypeVideo) // fmt 1: delta 5, new length and type
	raw = append(raw, "jk"...)
	c := &rtmpConn{r: bufio.NewReader(bytes.NewReader(raw)), inChunk: 128, in: map[uint32]*rtmpChunkState{}}
	want := []rtmpMessage{
		{Type: rtmpTypeAudio, StreamID: 1, Timestamp: 100, Payload: []byte("abc")},
		{Type: rtmpTypeAudio, StreamID: 1, Timestamp: 120, Payload: []byte("def")},
		{Type: rtmpTypeAudio, StreamID: 1, Timestamp: 140, Payload: []byte("ghi")},
		{Type: rtmpTypeVideo, StreamID: 1, Timestamp: 145, Payload: []byte("jk")},
	}
	for index, expected := range want {
		got, err := c.readMessage()
		if err != nil || !reflect.DeepEqual(got, expected) {
			t.Fatalf("message %d = %+v (%v), want %+v", index, got, err, expected)
		}
	}
}

func TestRTMPWriteReadRoundTripWithExtendedTimestamps(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	writer, reader := newRTMPConn(client), newRTMPConn(server)
	payload := bytes.Repeat([]byte{1, 2, 3, 4, 5}, 1000)
	messages := []rtmpMessage{
		{Type: rtmpTypeVideo, StreamID: 1, Timestamp: 0xFFFFFE, Payload: payload},
		{Type: rtmpTypeVideo, StreamID: 1, Timestamp: 0x7FFFFFFF, Payload: payload[:300]},
	}
	go func() {
		_ = writer.setChunkSize(256)
		for _, message := range messages {
			_ = writer.writeMessage(rtmpCSVideo, message)
		}
	}()
	if got, err := reader.readMessage(); err != nil || got.Type != rtmpTypeSetChunkSize || reader.inChunk != 256 {
		t.Fatalf("set chunk size = %+v (%v), inChunk %d", got, err, reader.inChunk)
	}
	for index, want := range messages {
		got, err := reader.readMessage()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("message %d = type %d ts %d len %d (%v)", index, got.Type, got.Timestamp, len(got.Payload), err)
		}
	}
}

func TestAMF0RoundTrip(t *testing.T) {
	encoded, err := amf0Encode("connect", 1.0, map[string]any{"app": "live", "ok": true, "nested": map[string]any{"n": 2.0}}, nil, strings.Repeat("x", 70000))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := amf0Decode(encoded)
	want := []any{"connect", 1.0, map[string]any{"app": "live", "ok": true, "nested": map[string]any{"n": 2.0}}, nil, strings.Repeat("x", 70000)}
	if err != nil || !reflect.DeepEqual(decoded, want) {
		t.Fatalf("decoded = %v (%v)", decoded, err)
	}
	if _, err := amf0Decode([]byte{0x02, 0, 9, 'a'}); err == nil {
		t.Fatal("a truncated string decoded")
	}
}

func TestFLVReaderReadsTags(t *testing.T) {
	tags := testFLVTags()
	reader := newFLVReader(bytes.NewReader(buildFLV(tags)))
	for index, want := range tags {
		got, err := reader.next()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("tag %d = type %d ts %d (%v)", index, got.Type, got.Timestamp, err)
		}
	}
	if _, err := reader.next(); err == nil || err.Error() != "EOF" {
		t.Fatalf("end of stream = %v", err)
	}
	if _, err := newFLVReader(strings.NewReader("not an flv stream")).next(); err == nil {
		t.Fatal("garbage parsed as FLV")
	}
}
