package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	gws "github.com/gorilla/websocket"
)

// `hubfly stream tail`: the studio socket (hubfly-stream docs/STUDIO.md).
// The CLI mints a studio token with origin "hubfly-cli", dials
// wss://<host>/v1/studio without an Origin header and sends the token in the
// first frame (never in the URL). It prints events, live state changes and,
// with --meters, audio levels; it reconnects with a fresh token.

const (
	streamStudioPath         = "/v1/studio"
	streamStudioOrigin       = "hubfly-cli"
	streamTailPingInterval   = 25 * time.Second
	streamTailIdleTimeout    = 75 * time.Second
	streamTailMaxAuthFails   = 3
	streamTailMaxWatch       = 16
	streamTailMeterInterval  = time.Second
	streamCloseBadToken      = 4401
	streamCloseForbidden     = 4403
	streamCloseSilence       = 4408
	streamCloseTooManyStudio = 4429
)

type streamTail struct {
	mint    func(ctx context.Context) (streamStudioToken, error)
	watch   []string
	meters  bool
	json    bool
	out     io.Writer
	errOut  io.Writer
	backoff func(attempt int) time.Duration

	writeMu   sync.Mutex
	live      map[string]string
	lastMeter map[string]time.Time
}

type streamTailFrame struct {
	Type    string          `json:"type"`
	Tenant  string          `json:"tenant"`
	Scopes  []string        `json:"scopes"`
	Session string          `json:"session"`
	Data    json.RawMessage `json:"data"`
	Code    string          `json:"code"`
	Message string          `json:"message"`
}

// validateStudioURL accepts wss://<host>/v1/studio (ws:// only for loopback
// hosts) and drops any query: the token never goes in the URL.
func validateStudioURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "", errors.New("the Dashboard returned an invalid studio URL")
	}
	switch parsed.Scheme {
	case "wss":
	case "ws":
		if !isLoopbackHost(parsed.Hostname()) {
			return "", fmt.Errorf("refusing an unencrypted studio URL (ws://%s)", parsed.Host)
		}
	default:
		return "", fmt.Errorf("the Dashboard returned a studio URL with scheme %q", parsed.Scheme)
	}
	if parsed.Path != streamStudioPath {
		return "", fmt.Errorf("unexpected studio path %q", parsed.Path)
	}
	parsed.RawQuery, parsed.Fragment, parsed.User = "", "", nil
	return parsed.String(), nil
}

func (t *streamTail) notice(format string, args ...any) {
	for index, arg := range args {
		switch typed := arg.(type) {
		case string:
			args[index] = terminalSafe(typed)
		case error:
			args[index] = terminalSafe(redactStreamSecretText(typed.Error()))
		}
	}
	_, _ = fmt.Fprintf(t.errOut, "# "+format+"\n", args...)
}

// run keeps a session open until ctx ends or a fatal close code arrives.
func (t *streamTail) run(ctx context.Context) error {
	attempt, authFailures := 0, 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		token, err := t.mint(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var apiErr *apiError
			if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 && apiErr.Status != 429 {
				return streamAPIError(err)
			}
			attempt++
			delay := t.backoff(attempt)
			t.notice("could not get a studio token (%v); retrying in %s", err, delay)
			if !sleepContext(ctx, delay) {
				return nil
			}
			continue
		}
		ready, err := t.session(ctx, token)
		if ctx.Err() != nil {
			return nil
		}
		var fatal realtimeTailFatal
		if errors.As(err, &fatal) {
			return fatal.err
		}
		if ready {
			attempt = 0
		}
		switch realtimeCloseCode(err) {
		case streamCloseForbidden:
			return errors.New("the stream server refused the studio session (close 4403); check that your role can read this stream server")
		case streamCloseTooManyStudio:
			return errors.New("the stream server already has 10 studio sessions open (close 4429); close one (Dashboard studio or CLI) and try again")
		case streamCloseBadToken:
			authFailures++
			if authFailures >= streamTailMaxAuthFails {
				return errors.New("the stream server rejected the studio token (close 4401: missing, invalid, expired or replayed)")
			}
			t.notice("studio token rejected; reconnecting with a new token")
			continue
		case streamCloseSilence:
			t.notice("studio session timed out; reconnecting")
		default:
			if ready {
				authFailures = 0
			}
			if err != nil {
				debugf("studio session ended: %v", err)
			}
		}
		attempt++
		delay := t.backoff(attempt)
		t.notice("studio connection lost; reconnecting in %s", delay)
		if !sleepContext(ctx, delay) {
			return nil
		}
	}
}

func (t *streamTail) write(conn *gws.Conn, value any) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return conn.WriteJSON(value)
}

func (t *streamTail) session(ctx context.Context, token streamStudioToken) (bool, error) {
	target, err := validateStudioURL(token.URL)
	if err != nil {
		return false, realtimeTailFatal{err}
	}
	if strings.TrimSpace(token.Token) == "" {
		return false, realtimeTailFatal{errors.New("the Dashboard returned an empty studio token")}
	}
	// No Origin header: the token's orn is "hubfly-cli" (STUDIO.md).
	dialer := gws.Dialer{HandshakeTimeout: realtimeTailDialTimeout, Proxy: http.ProxyFromEnvironment}
	dialCtx, cancelDial := context.WithTimeout(ctx, realtimeTailDialTimeout)
	conn, response, err := dialer.DialContext(dialCtx, target, nil)
	cancelDial()
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		if response != nil && response.StatusCode == http.StatusForbidden {
			return false, &gws.CloseError{Code: streamCloseForbidden, Text: "forbidden"}
		}
		return false, err
	}
	defer func() { _ = conn.Close() }()
	stop := make(chan struct{})
	var stopOnce sync.Once
	defer stopOnce.Do(func() { close(stop) })
	go func() {
		select {
		case <-ctx.Done():
			t.writeMu.Lock()
			_ = conn.WriteControl(gws.CloseMessage, gws.FormatCloseMessage(gws.CloseNormalClosure, ""), time.Now().Add(time.Second))
			t.writeMu.Unlock()
			_ = conn.Close()
		case <-stop:
		}
	}()

	if err := t.write(conn, map[string]string{"type": "auth", "token": token.Token}); err != nil {
		return false, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(realtimeTailReadyTimeout))
	var ready streamTailFrame
	for {
		_, raw, readErr := conn.ReadMessage()
		if readErr != nil {
			return false, readErr
		}
		if json.Unmarshal(raw, &ready) == nil && ready.Type == "ready" {
			break
		}
		t.handleFrame(raw)
	}
	t.live = nil
	t.notice("studio connected (session %s, scopes %s); press Ctrl-C to stop", ready.Session, strings.Join(ready.Scopes, ","))
	if len(t.watch) > 0 {
		if err := t.write(conn, map[string]any{"type": "watch", "sources": t.watch}); err != nil {
			return true, err
		}
	}
	go func() {
		ticker := time.NewTicker(streamTailPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if t.write(conn, map[string]string{"type": "ping"}) != nil {
					return
				}
			}
		}
	}()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(streamTailIdleTimeout))
		_, raw, readErr := conn.ReadMessage()
		if readErr != nil {
			return true, readErr
		}
		t.handleFrame(raw)
	}
}

func (t *streamTail) handleFrame(raw []byte) {
	var frame streamTailFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		debugf("studio: undecodable frame: %v", err)
		return
	}
	switch frame.Type {
	case "event", "live":
		if t.json {
			t.printJSONLine(raw)
			return
		}
		var data any
		decoder := json.NewDecoder(bytes.NewReader(frame.Data))
		if decoder.Decode(&data) != nil {
			return
		}
		data = redactStreamSecrets(data)
		if frame.Type == "event" {
			event, _ := data.(map[string]any)
			_, _ = fmt.Fprintln(t.out, formatStreamEvent(event))
			return
		}
		t.printLive(data)
	case "meters":
		if !t.meters {
			return
		}
		if t.json {
			t.printJSONLine(raw)
			return
		}
		t.printMeters(frame.Data)
	case "error":
		t.notice("studio error: %s %s", frame.Code, redactStreamSecretText(frame.Message))
	case "ready", "pong", "preview.grant", "control.result":
	default:
		debugf("studio: unhandled frame type %q", frame.Type)
	}
}

// printJSONLine prints a frame as one compact JSON line, secrets masked.
func (t *streamTail) printJSONLine(raw []byte) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return
	}
	encoded, err := json.Marshal(redactStreamSecrets(value))
	if err != nil {
		return
	}
	_, _ = t.out.Write(append(escapeJSONNonPrintable(encoded), '\n'))
}

// printLive prints the state of every input, channel, destination and mixer
// on the first live frame, then only what changed.
func (t *streamTail) printLive(data any) {
	payload, _ := data.(map[string]any)
	next := map[string]string{}
	if server, ok := payload["server"].(map[string]any); ok {
		state := streamText(server["runState"])
		if server["throttled"] == true {
			state += " (throttled at the speed cap)"
		}
		if billing, ok := server["billing"].(map[string]any); ok && billing["allowed"] == false {
			state += " (billing blocked: " + streamText(billing["reason"]) + ")"
		}
		next["server"] = state
	}
	for _, section := range []struct{ key, noun string }{{"inputs", "input"}, {"channels", "channel"}, {"destinations", "destination"}, {"mixers", "mixer"}} {
		items, _ := payload[section.key].([]any)
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			label := streamText(item["id"])
			if name, ok := item["name"].(string); ok && name != "" {
				label = terminalSafe(name) + " (" + label + ")"
			}
			state := streamText(item["state"])
			switch section.key {
			case "inputs":
				if role := streamText(item["activeRole"]); role != "-" {
					state += " role " + role
				}
			case "mixers":
				if scene := streamText(item["scene"]); scene != "-" {
					state += " scene " + scene
				}
			}
			next[section.noun+" "+label] = state
		}
	}
	keys := make([]string, 0, len(next))
	for key := range next {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	stamp := streamNow().Format("15:04:05")
	first := t.live == nil
	for _, key := range keys {
		before, seen := t.live[key]
		switch {
		case first || !seen:
			_, _ = fmt.Fprintf(t.out, "%s %s %s\n", stamp, key, next[key])
		case before != next[key]:
			_, _ = fmt.Fprintf(t.out, "%s %s %s -> %s\n", stamp, key, before, next[key])
		}
	}
	for key := range t.live {
		if _, still := next[key]; !still {
			_, _ = fmt.Fprintf(t.out, "%s %s removed\n", stamp, key)
		}
	}
	t.live = next
}

// printMeters prints each watched source at most once a second:
// "15:04:05 meters in:sin_x rms -18.2/-17.9 peak -6.1/-5.8 dBFS".
func (t *streamTail) printMeters(raw json.RawMessage) {
	var meters map[string]struct {
		RMS  []float64 `json:"rms"`
		Peak []float64 `json:"peak"`
	}
	if json.Unmarshal(raw, &meters) != nil {
		return
	}
	if t.lastMeter == nil {
		t.lastMeter = map[string]time.Time{}
	}
	now := streamNow()
	sources := make([]string, 0, len(meters))
	for source := range meters {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	join := func(values []float64) string {
		parts := make([]string, 0, len(values))
		for _, value := range values {
			parts = append(parts, fmt.Sprintf("%.1f", value))
		}
		if len(parts) == 0 {
			return "-"
		}
		return strings.Join(parts, "/")
	}
	for _, source := range sources {
		if last, ok := t.lastMeter[source]; ok && now.Sub(last) < streamTailMeterInterval {
			continue
		}
		t.lastMeter[source] = now
		level := meters[source]
		_, _ = fmt.Fprintf(t.out, "%s meters %s rms %s peak %s dBFS\n", now.Format("15:04:05"), terminalSafe(source), join(level.RMS), join(level.Peak))
	}
}

// resolveStreamWatch turns in:<input>, ch:<channel> and mx:<mixer> refs into
// studio source names (ids). With meters and no refs it watches every input
// and channel (at most 16).
func resolveStreamWatch(target streamTarget, refs []string, meters bool) ([]string, error) {
	res := newStreamResolver(target)
	kinds := map[string]*streamResource{"in": streamResourceByName("inputs"), "ch": streamResourceByName("channels"), "mx": streamResourceByName("mixers")}
	sources := []string{}
	if len(refs) == 0 && meters {
		for _, prefix := range []string{"in", "ch"} {
			items, err := res.list(kinds[prefix])
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				if len(sources) < streamTailMaxWatch {
					sources = append(sources, prefix+":"+streamString(item, "id"))
				}
			}
		}
		return sources, nil
	}
	for _, ref := range refs {
		prefix, rest, _ := strings.Cut(ref, ":")
		name, sub, _ := strings.Cut(rest, "/")
		id, err := res.id(kinds[prefix], name)
		if err != nil {
			return nil, err
		}
		source := prefix + ":" + id
		if sub != "" {
			source += "/" + sub
		}
		sources = append(sources, source)
	}
	return sources, nil
}

func streamTailFlow(args []string) error {
	const usage = "hubfly stream tail <server> [--meters] [--watch in:<input>,ch:<channel>,mx:<mixer>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream tail")
	meters := fs.Bool("meters", false, "")
	watch := &rtList{split: true}
	fs.Var(watch, "watch", "")
	asJSON := fs.Bool("json", false, "")
	target, _, err := streamParseTarget(fs, args, usage, 1, func([]string) error {
		if len(watch.values) > streamTailMaxWatch {
			return fmt.Errorf("invalid --watch: at most %d sources", streamTailMaxWatch)
		}
		for _, ref := range watch.values {
			prefix, rest, ok := strings.Cut(ref, ":")
			if !ok || rest == "" || (prefix != "in" && prefix != "ch" && prefix != "mx") {
				return fmt.Errorf("invalid --watch %q (use in:<input>, ch:<channel> or mx:<mixer>)", ref)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	sources, err := resolveStreamWatch(target, watch.values, *meters)
	if err != nil {
		return err
	}
	tail := &streamTail{
		watch:   sources,
		meters:  *meters || len(sources) > 0,
		json:    *asJSON,
		out:     rtOut,
		errOut:  rtErr,
		backoff: defaultRealtimeTailBackoff,
		mint: func(context.Context) (streamStudioToken, error) {
			return createStreamStudioToken(target.token, target.instance, streamStudioOrigin)
		},
	}
	ctx, stop := rtTailContext()
	defer stop()
	return tail.run(ctx)
}
