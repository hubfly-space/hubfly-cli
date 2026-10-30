package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"time"

	srt "github.com/datarhei/gosrt"
)

// `hubfly stream push`: publish a file or a capture device to an input with
// a local ffmpeg.
//
// The stream key never reaches ffmpeg. ffmpeg only encodes, to its stdout
// (MPEG-TS for SRT, FLV for RTMPS); the CLI carries those bytes to the
// server itself, SRT through gosrt (streamid publish:<key>, passphrase) and
// RTMPS through the built-in publisher (stream_rtmp.go). The key stays in
// this process's memory: it is not in any argv (ffmpeg's or ours, so not in
// process listings), not in the environment, not in a temp file and not in
// ffmpeg's logs. (FFmpeg's -/opt FILE form cannot carry protocol options
// such as srt streamid or rtmp_playpath, and ffmpeg 6.1 lacks it entirely.)

var (
	streamLookPath    = exec.LookPath
	streamPushContext = func() (context.Context, context.CancelFunc) {
		return signal.NotifyContext(context.Background(), os.Interrupt)
	}
	streamGOOS = runtime.GOOS
	// Ports and transport of the stream server; tests point them at local
	// servers.
	streamSRTPort    = 9000
	streamRTMPSPort  = 443
	streamRTMPUseTLS = true
	// streamSRTLatency is the SRT latency the CLI asks for (INGEST.md: 2,000
	// ms by default, the node accepts 120-8,000 ms).
	streamSRTLatency = 2 * time.Second
)

const (
	streamTSPacket      = 188
	streamSRTPayload    = 7 * streamTSPacket // 1316 bytes: 7 TS packets per SRT packet
	streamSRTWriteRetry = 2 * time.Second
)

type streamPushOptions struct {
	Source      string
	Protocol    string
	Loop        bool
	BitrateKbps int
	AudioKbps   int
}

// streamPushInstallHint explains how to install ffmpeg on this platform.
func streamPushInstallHint(goos string) string {
	switch goos {
	case "darwin":
		return "Install it with Homebrew: brew install ffmpeg"
	case "windows":
		return "Install it with: winget install Gyan.FFmpeg (or download it from https://ffmpeg.org/download.html), then open a new terminal"
	}
	return "Install it with your package manager, for example: sudo apt install ffmpeg (Debian/Ubuntu), sudo dnf install ffmpeg (Fedora), sudo pacman -S ffmpeg (Arch)"
}

// streamCaptureInput returns the ffmpeg input arguments of a capture device
// ("device" or "device:<spec>") and whether a silent audio track must be
// added.
func streamCaptureInput(spec, goos string) ([]string, bool, error) {
	switch goos {
	case "linux":
		if spec == "" {
			spec = "/dev/video0"
		}
		return []string{"-f", "v4l2", "-i", spec}, true, nil
	case "darwin":
		if spec == "" {
			spec = "0:0"
		}
		return []string{"-f", "avfoundation", "-framerate", "30", "-i", spec}, !strings.Contains(spec, ":"), nil
	case "windows":
		if spec == "" {
			return nil, false, errors.New("invalid source: name the DirectShow camera, for example device:video=\"Integrated Camera\":audio=\"Microphone\" (list them with: ffmpeg -list_devices true -f dshow -i dummy)")
		}
		return []string{"-f", "dshow", "-i", spec}, !strings.Contains(spec, "audio="), nil
	}
	return nil, false, fmt.Errorf("invalid source: capture devices are not supported on %s; push a file instead", goos)
}

// buildStreamPushArgs returns ffmpeg's arguments (without the program). They
// never hold the key: the output is stdout.
func buildStreamPushArgs(opts streamPushOptions, goos string) ([]string, error) {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "warning", "-stats"}
	source := opts.Source
	silentAudio := false
	if source == "device" || strings.HasPrefix(source, "device:") {
		if opts.Loop {
			return nil, errors.New("invalid flag --loop: it works with files only")
		}
		input, silent, err := streamCaptureInput(strings.TrimPrefix(strings.TrimPrefix(source, "device"), ":"), goos)
		if err != nil {
			return nil, err
		}
		args = append(args, input...)
		silentAudio = silent
	} else {
		if opts.Loop {
			args = append(args, "-stream_loop", "-1")
		}
		args = append(args, "-re", "-i", source)
	}
	if silentAudio {
		args = append(args, "-f", "lavfi", "-i", "anullsrc=channel_layout=stereo:sample_rate=48000", "-map", "0:v:0", "-map", "1:a:0")
	}
	bitrate := strconv.Itoa(opts.BitrateKbps) + "k"
	// INGEST.md "Recommended encoder settings", low-latency row: H.264
	// without B-frames (WebRTC passthrough), 2 s keyframes, AAC 48 kHz.
	args = append(args,
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-pix_fmt", "yuv420p",
		"-b:v", bitrate, "-maxrate", bitrate, "-bufsize", strconv.Itoa(opts.BitrateKbps*2)+"k",
		"-bf", "0", "-force_key_frames", "expr:gte(t,n_forced*2)", "-sc_threshold", "0",
		"-c:a", "aac", "-b:a", strconv.Itoa(opts.AudioKbps)+"k", "-ar", "48000", "-ac", "2",
	)
	switch opts.Protocol {
	case "srt":
		args = append(args, "-f", "mpegts", "-muxdelay", "0", "-muxpreload", "0", "pipe:1")
	case "rtmps":
		args = append(args, "-f", "flv", "-flvflags", "no_duration_filesize", "pipe:1")
	default:
		return nil, fmt.Errorf("invalid --protocol %q (use srt or rtmps)", opts.Protocol)
	}
	return args, nil
}

// streamShellQuote renders argv for --dry-run.
func streamShellQuote(args []string) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		if arg != "" && !strings.ContainsAny(arg, " \t\n'\"\\$`*?()[]{}<>|&;!#~=:") {
			parts = append(parts, arg)
			continue
		}
		parts = append(parts, "'"+strings.ReplaceAll(arg, "'", `'\''`)+"'")
	}
	return strings.Join(parts, " ")
}

// streamPushSink carries ffmpeg's output to the server.
type streamPushSink interface {
	carry(ctx context.Context, r io.Reader) error
	Close() error
}

// ---------------------------------------------------------------------------
// SRT
// ---------------------------------------------------------------------------

type streamSRTSink struct{ conn srt.Conn }

func dialStreamSRT(host, key, passphrase string) (*streamSRTSink, error) {
	config := srt.DefaultConfig()
	config.StreamId = "publish:" + key
	config.Passphrase = passphrase
	config.Latency = streamSRTLatency
	config.PayloadSize = streamSRTPayload
	config.ConnectionTimeout = 10 * time.Second
	address := net.JoinHostPort(host, strconv.Itoa(streamSRTPort))
	conn, err := srt.Dial("srt", address, config)
	if err != nil {
		message := redactStreamSecretText(err.Error())
		if strings.Contains(message, "rejected") {
			return nil, fmt.Errorf("the stream server refused the SRT connection (%s): check the key, that the input is enabled and allows SRT, the passphrase, and that the server is running", terminalSafe(message))
		}
		return nil, fmt.Errorf("SRT connection to %s failed: %s", address, terminalSafe(message))
	}
	return &streamSRTSink{conn: conn}, nil
}

// carry sends MPEG-TS in 1316-byte payloads. A full send queue is retried
// for a moment before it counts as a failure.
func (s *streamSRTSink) carry(ctx context.Context, r io.Reader) error {
	buf := make([]byte, 64<<10)
	pending := make([]byte, 0, 2*len(buf))
	send := func(packet []byte) error {
		deadline := time.Now().Add(streamSRTWriteRetry)
		for {
			_, err := s.conn.Write(packet)
			if err == nil {
				return nil
			}
			if ctx.Err() != nil || time.Now().After(deadline) {
				return fmt.Errorf("SRT connection lost: %v", err)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	for {
		n, readErr := r.Read(buf)
		pending = append(pending, buf[:n]...)
		for len(pending) >= streamSRTPayload {
			if err := send(pending[:streamSRTPayload]); err != nil {
				return err
			}
			pending = pending[streamSRTPayload:]
		}
		pending = append(pending[:0:0], pending...)
		if readErr != nil {
			if len(pending) > 0 {
				if err := send(pending); err != nil {
					return err
				}
			}
			if errors.Is(readErr, io.EOF) {
				// Let the last packets leave the send buffer (they are paced by
				// the latency) before the caller closes the connection.
				sleepContext(ctx, streamSRTLatency)
				return nil
			}
			return readErr
		}
	}
}

func (s *streamSRTSink) Close() error { return s.conn.Close() }

// ---------------------------------------------------------------------------
// RTMPS
// ---------------------------------------------------------------------------

type streamRTMPSink struct{ pub *rtmpPublisher }

func dialStreamRTMP(ctx context.Context, host, key string) (*streamRTMPSink, error) {
	address := net.JoinHostPort(host, strconv.Itoa(streamRTMPSPort))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	scheme := "rtmp"
	if streamRTMPUseTLS {
		scheme = "rtmps"
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return nil, fmt.Errorf("RTMPS connection to %s failed: %v", address, err)
	}
	tcURL := fmt.Sprintf("%s://%s/live", scheme, address)
	pub, err := publishRTMP(conn, "live", tcURL, key)
	if err != nil {
		_ = conn.Close()
		return nil, errors.New(redactStreamSecretText(strings.ReplaceAll(err.Error(), key, streamMasked)))
	}
	return &streamRTMPSink{pub: pub}, nil
}

func (s *streamRTMPSink) carry(ctx context.Context, r io.Reader) error {
	reader := newFLVReader(r)
	for {
		tag, err := reader.next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.pub.WriteTag(tag.Type, tag.Timestamp, tag.Data); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("RTMPS connection lost: %v", err)
		}
	}
}

func (s *streamRTMPSink) Close() error { return s.pub.Close() }

// ---------------------------------------------------------------------------
// Command
// ---------------------------------------------------------------------------

const streamPushUsage = "hubfly stream push <server> <file|device[:<spec>]> [--input <input>] [--protocol srt|rtmps] [--loop] [--key-stdin] [--role primary|backup] [--bitrate <kbps>] [--ffmpeg <path>] [--dry-run] [--project <id|name>]"

func streamPushFlow(args []string) error {
	fs := newRealtimeFlags("stream push")
	inputRef := fs.String("input", "", "")
	protocol := fs.String("protocol", "srt", "")
	loop := fs.Bool("loop", false, "")
	keyStdin := fs.Bool("key-stdin", false, "")
	role := fs.String("role", "primary", "")
	bitrate := fs.Int("bitrate", 4500, "")
	ffmpegPath := fs.String("ffmpeg", "", "")
	dryRun := fs.Bool("dry-run", false, "")
	var ffmpegArgs []string
	target, positionals, err := streamParseTarget(fs, args, streamPushUsage, 2, func(positionals []string) error {
		*protocol = strings.ToLower(*protocol)
		if err := streamOneOf("protocol", *protocol, "srt", "rtmps"); err != nil {
			return err
		}
		if err := streamOneOf("role", *role, "primary", "backup"); err != nil {
			return err
		}
		if *bitrate < 300 || *bitrate > 80000 {
			return fmt.Errorf("invalid --bitrate %d (kbps, 300-80000)", *bitrate)
		}
		source := positionals[1]
		if source != "device" && !strings.HasPrefix(source, "device:") && !strings.Contains(source, "://") {
			if info, statErr := os.Stat(source); statErr != nil || info.IsDir() {
				return fmt.Errorf("invalid source %q: not a readable file (or use device / device:<spec>)", source)
			}
		}
		var buildErr error
		ffmpegArgs, buildErr = buildStreamPushArgs(streamPushOptions{Source: source, Protocol: *protocol, Loop: *loop, BitrateKbps: *bitrate, AudioKbps: 160}, streamGOOS)
		return buildErr
	})
	if err != nil {
		return err
	}
	program := *ffmpegPath
	if program == "" {
		program = "ffmpeg"
	}
	resolved, lookErr := streamLookPath(program)
	if lookErr != nil && !*dryRun {
		return fmt.Errorf("ffmpeg was not found (%s). hubfly stream push encodes with a local ffmpeg.\n%s", terminalSafe(program), streamPushInstallHint(streamGOOS))
	}
	if resolved == "" {
		resolved = program
	}
	server, err := fetchStreamServer(target.token, target.instance)
	if err != nil {
		return streamAPIError(err)
	}
	if !server.Billing.Allowed {
		return fmt.Errorf("stream server '%s' is paused by billing; media resumes once it is resolved in the Dashboard (Billing)", target.instance.Name)
	}
	input, err := pickStreamPushInput(target, *inputRef)
	if err != nil {
		return err
	}
	inputID, inputName := streamString(input, "id"), terminalSafe(streamString(input, "name"))
	if input["enabled"] == false {
		return fmt.Errorf("input '%s' is disabled; enable it with: hubfly stream inputs update %s %s --enable", inputName, target.instance.Name, inputID)
	}
	if streamString(input, "kind") == "pull" {
		return fmt.Errorf("input '%s' pulls from a URL; push to a push input instead", inputName)
	}
	if protocols, ok := input["protocols"].([]any); ok && len(protocols) > 0 && !streamContains(protocols, *protocol) {
		return fmt.Errorf("input '%s' does not accept %s; allow it with: hubfly stream inputs update %s %s --protocols %s,%s",
			inputName, strings.ToUpper(*protocol), target.instance.Name, inputID, streamListOf(input, "protocols"), *protocol)
	}
	destination := fmt.Sprintf("srt://%s:%d (streamid publish:<key>)", server.Host, streamSRTPort)
	if *protocol == "rtmps" {
		destination = fmt.Sprintf("rtmps://%s:%d/live (stream name <key>)", server.Host, streamRTMPSPort)
	}
	if *dryRun {
		_, _ = fmt.Fprintf(rtOut, "%s %s\n", streamShellQuote([]string{resolved}), streamShellQuote(ffmpegArgs))
		_, _ = fmt.Fprintf(rtOut, "# ffmpeg writes to stdout; hubfly carries the stream to %s for input '%s'.\n", destination, inputName)
		_, _ = fmt.Fprintln(rtOut, "# The stream key never appears in ffmpeg's arguments, environment or logs.")
		if lookErr != nil {
			_, _ = fmt.Fprintf(rtErr, "Note: ffmpeg was not found. %s\n", streamPushInstallHint(streamGOOS))
		}
		return nil
	}

	key, passphrase := "", ""
	if *keyStdin {
		if key, err = readStreamSecretLine("Stream key"); err != nil {
			return err
		}
		if *protocol == "srt" && input["hasSrtPassphrase"] == true {
			if passphrase, err = readStreamSecretLine("SRT passphrase"); err != nil {
				return err
			}
		}
	} else {
		keys, revealErr := revealStreamInputKeys(target.token, target.instance, inputID)
		if revealErr != nil {
			err := streamAPIError(revealErr)
			var apiErr *apiError
			if errors.As(revealErr, &apiErr) && apiErr.Status == 403 {
				return fmt.Errorf("%w\nRevealing keys needs stream admin; pass the key on stdin instead: hubfly stream push … --key-stdin", err)
			}
			return err
		}
		key, passphrase = keys.Primary, keys.SRTPassphrase
		if *role == "backup" {
			key = keys.Backup
		}
	}
	if !strings.HasPrefix(key, "hfls_") {
		return fmt.Errorf("input '%s' has no %s stream key (or the key is not a HubFly stream key)", inputName, *role)
	}

	ctx, stop := streamPushContext()
	defer stop()
	var sink streamPushSink
	if *protocol == "srt" {
		sink, err = dialStreamSRT(server.Host, key, passphrase)
	} else {
		sink, err = dialStreamRTMP(ctx, server.Host, key)
	}
	key, passphrase = "", ""
	if err != nil {
		return err
	}
	defer func() { _ = sink.Close() }()

	cmd := exec.CommandContext(ctx, resolved, ffmpegArgs...)
	cmd.Stdin = nil
	cmd.Stderr = rtErr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	_, _ = fmt.Fprintf(rtErr, "Publishing %s to input '%s' over %s (%s); press Ctrl-C to stop.\n", terminalSafe(positionals[1]), inputName, strings.ToUpper(*protocol), server.Host)
	carryErr := sink.carry(ctx, stdout)
	if carryErr != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		_, _ = fmt.Fprintln(rtErr, "Stopped.")
		return nil
	}
	if carryErr != nil {
		return carryErr
	}
	if waitErr != nil {
		return fmt.Errorf("ffmpeg failed: %v (see its messages above)", waitErr)
	}
	_, _ = fmt.Fprintln(rtErr, "Finished: the source ended.")
	return nil
}

func streamContains(values []any, want string) bool {
	for _, value := range values {
		if text, ok := value.(string); ok && strings.EqualFold(text, want) {
			return true
		}
	}
	return false
}

// pickStreamPushInput resolves --input, or picks the server's only push input.
func pickStreamPushInput(target streamTarget, ref string) (map[string]any, error) {
	res := newStreamResolver(target)
	def := streamResourceByName("inputs")
	if ref != "" {
		return res.find(def, ref)
	}
	items, err := res.list(def)
	if err != nil {
		return nil, err
	}
	push := []map[string]any{}
	for _, item := range items {
		if streamString(item, "kind") != "pull" {
			push = append(push, item)
		}
	}
	switch len(push) {
	case 1:
		return push[0], nil
	case 0:
		return nil, fmt.Errorf("stream server '%s' has no push input; create one with: hubfly stream inputs create %s <name>", target.instance.Name, target.instance.Name)
	}
	names := []string{}
	for _, item := range push {
		names = append(names, terminalSafe(streamString(item, "name")))
	}
	return nil, fmt.Errorf("invalid arguments: stream server '%s' has %d push inputs (%s); choose one with --input", target.instance.Name, len(push), strings.Join(names, ", "))
}
