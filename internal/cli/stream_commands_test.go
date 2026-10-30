package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Fake Dashboard fixtures for `hubfly stream`
// ---------------------------------------------------------------------------

const (
	streamTestBase = "/api/v1/projects/p-man/managed/instances/msi_live"
	streamTestAPI  = streamTestBase + "/stream"
	// The documentation example key (INGEST.md); a secret for these tests.
	streamTestKey    = "hfls_cam1abcd_9pQ2xV7mK4tR8wZ1nB6cY3hJ5fL0dS2a"
	streamTestSecret = "9pQ2xV7mK4tR8wZ1nB6cY3hJ5fL0dS2a"
)

func streamTestServer(host string) map[string]any {
	return map[string]any{
		"host": host, "speedMbps": 25, "processingMbps": 0, "webrtcSessions": 200, "timezone": "Africa/Kigali",
		"sync":    map[string]any{"generation": 3, "appliedGeneration": 3, "lastSyncedAt": "2026-10-29T08:00:00Z", "lastError": nil},
		"billing": map[string]any{"allowed": true, "reason": nil},
		"ingest": map[string]any{"rtmp": "rtmp://" + host + "/live", "rtmps": "rtmps://" + host + ":443/live", "srt": "srt://" + host + ":9000",
			"whipBase": "https://" + host + "/whip/", "icecastBase": "icecast://" + host + ":8000/radio/"},
		"price":          map[string]any{"speedMicroUsdPerHour": 1025, "speedUsdPerMonth": 0.75},
		"offlineSlate":   nil,
		"allowedOrigins": []string{},
		"studioOrigins":  []string{"https://app.hubfly.space"},
	}
}

func streamTestInput() map[string]any {
	return map[string]any{
		"id": "sin_cam1abcd", "name": "Camera", "kind": "push", "enabled": true, "protocols": []string{"rtmps", "srt"},
		"maxBitrateKbps": 8000, "allowedCidrs": []string{}, "revision": 2, "hasSrtPassphrase": false,
		"keyHints":        map[string]any{"primary": "hfls_cam1abcd_…dS2a"},
		"keyFingerprints": map[string]any{"primary": "sha256:0a1b2c3d4e5f"},
		"pull":            map[string]any{"urlHint": "", "transport": "auto", "reconnect": true},
	}
}

func streamTestChannel() map[string]any {
	return map[string]any{
		"id": "sch_main0001", "name": "Main", "enabled": true, "latency": "low", "dvrMinutes": 30, "maxViewers": 0, "revision": 3,
		"source": map[string]any{"type": "input", "ref": "sin_cam1abcd"},
		"access": map[string]any{"mode": "public", "allowedOrigins": []string{}, "allowedCidrs": []string{"10.0.0.0/8"}},
		"playback": map[string]any{"hls": true, "llHls": true, "webrtc": true, "icecast": []any{}, "rtmpPull": false, "srtPull": false,
			"urls": map[string]any{"hls": "https://live.test/hls/sch_main0001/index.m3u8"}},
	}
}

func streamTestMixer() map[string]any {
	return map[string]any{
		"id": "smx_show0001", "name": "Show", "enabled": true, "defaultScene": "sc_live", "revision": 1,
		"canvas": map[string]any{"width": 1920, "height": 1080, "fps": 30, "background": "#000000"},
		"scenes": []map[string]any{
			{"id": "sc_intro", "name": "Intro", "layers": []any{}, "audio": []any{}},
			{"id": "sc_live", "name": "Live", "layers": []any{map[string]any{"source": "cam"}}, "audio": []any{}},
		},
	}
}

type streamTestEnv struct {
	rtTestEnv
}

func setupStreamTest(t *testing.T) streamTestEnv {
	t.Helper()
	env := streamTestEnv{setupRealtimeTest(t)}
	f := env.dash
	f.ok("GET", "/api/v1/projects/p-man/managed/instances", map[string]any{"items": []map[string]any{
		rtTestInstance("mi_chat", "chat", "realtime"),
		rtTestInstance("msi_live", "live-show", "stream"),
	}})
	f.ok("GET", streamTestAPI, streamTestServer("live-1a2b.live.hubfly.test"))
	f.ok("GET", streamTestAPI+"/inputs", []map[string]any{streamTestInput()})
	f.ok("GET", streamTestAPI+"/channels", []map[string]any{streamTestChannel()})
	f.ok("GET", streamTestAPI+"/channels/sch_main0001", streamTestChannel())
	f.ok("GET", streamTestAPI+"/mixers", []map[string]any{streamTestMixer()})
	f.ok("GET", streamTestAPI+"/mixers/smx_show0001", streamTestMixer())
	f.ok("GET", streamTestAPI+"/destinations", []map[string]any{{"id": "sds_yt000001", "name": "YouTube", "platform": "youtube", "enabled": true, "streamKeyHint": "…9x7z"}})
	f.ok("POST", streamTestAPI+"/inputs/sin_cam1abcd/keys/reveal", map[string]any{"primary": streamTestKey})
	origStdin, origLookPath, origGOOS := rtStdin, streamLookPath, streamGOOS
	origPlayer, origPushContext := streamRunPlayer, streamPushContext
	t.Cleanup(func() {
		rtStdin, streamLookPath, streamGOOS = origStdin, origLookPath, origGOOS
		streamRunPlayer, streamPushContext = origPlayer, origPushContext
	})
	return env
}

// assertNoStreamSecret fails when the input key (or its secret part) leaked
// into any output or argument list.
func assertNoStreamSecret(t *testing.T, where, text string) {
	t.Helper()
	if strings.Contains(text, streamTestSecret) {
		t.Fatalf("%s leaked the stream key:\n%s", where, text)
	}
}

// ---------------------------------------------------------------------------
// Parsing and validation
// ---------------------------------------------------------------------------

func TestStreamCommandValidationHappensBeforeAnyRequest(t *testing.T) {
	env := setupStreamTest(t)
	video := filepath.Join(t.TempDir(), "show.mp4")
	if err := os.WriteFile(video, []byte("not really a video"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		args    []string
		wantErr string
	}{
		{[]string{"stream", "bogus"}, "unknown stream command"},
		{[]string{"live", "info"}, "usage: hubfly stream info"},
		{[]string{"stream", "speed", "live-show"}, "usage: hubfly stream speed"},
		{[]string{"stream", "usage", "live-show", "--month", "2026-13"}, "invalid --month"},
		{[]string{"stream", "inputs", "create", "live-show", "Cam", "--protocols", "ftp"}, "invalid --protocols"},
		{[]string{"stream", "inputs", "create", "live-show", "Cam", "--max-bitrate", "90000"}, "invalid --max-bitrate"},
		{[]string{"stream", "inputs", "update", "live-show", "Camera"}, "nothing to update"},
		{[]string{"stream", "inputs", "keys", "rotate", "live-show", "Camera", "--remove"}, "--remove works only with --role backup"},
		{[]string{"stream", "channels", "create", "live-show", "Main", "--source", "bogus"}, "invalid source"},
		{[]string{"stream", "channels", "create", "live-show", "Main", "--source", "input:Camera", "--latency", "instant"}, "invalid --latency"},
		{[]string{"stream", "channels", "token", "live-show", "Main", "--ttl", "5s"}, "invalid --ttl"},
		{[]string{"stream", "destinations", "create", "live-show", "YT", "--platform", "myspace"}, "invalid --platform"},
		{[]string{"stream", "destinations", "create", "live-show", "YT", "--platform", "youtube", "--source", "channel:Main"}, "--stream-key-stdin"},
		{[]string{"stream", "destinations", "create", "live-show", "LI", "--platform", "linkedin", "--source", "channel:Main", "--stream-key-stdin"}, "needs --url"},
		{[]string{"stream", "mixers", "update", "live-show", "Show", "--transition", "wipe"}, "invalid transition"},
		{[]string{"stream", "playlists", "create", "live-show", "Loop", "--item", "ftp://x"}, "invalid --item"},
		{[]string{"stream", "schedules", "create", "live-show", "Show", "--on-start", "{oops"}, "invalid --on-start"},
		{[]string{"stream", "webhooks", "create", "live-show", "--url", "http://example.test/hook"}, "https URL"},
		{[]string{"stream", "scene", "switch", "live-show", "Show", "Intro", "--fade", "1s", "--cut"}, "not both"},
		{[]string{"stream", "control", "live-show"}, "at least one operation"},
		{[]string{"stream", "control", "live-show", "--slate", "Main=maybe"}, "invalid --slate"},
		{[]string{"stream", "control", "live-show", "--op", `{"channel":"sch_main0001"}`}, `needs an "op" field`},
		{[]string{"stream", "tail", "live-show", "--watch", "foo:bar"}, "invalid --watch"},
		{[]string{"stream", "push", "live-show", "/nonexistent/show.mp4"}, "invalid source"},
		{[]string{"stream", "push", "live-show", video, "--protocol", "rtmp"}, "invalid --protocol"},
		{[]string{"stream", "push", "live-show", video, "--bitrate", "50"}, "invalid --bitrate"},
		{[]string{"stream", "play", "live-show", "Main", "--player", "vlc"}, "invalid --player"},
	}
	for _, tt := range tests {
		err := run(tt.args)
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Fatalf("run(%q) error = %v, want substring %q", tt.args, err, tt.wantErr)
		}
		if code := exitCodeForError(err); code != 2 {
			t.Fatalf("run(%q) exit code = %d, want 2 (%v)", tt.args, code, err)
		}
	}
	if raw := env.dash.allRaw(); raw != "" {
		t.Fatalf("validation errors made requests:\n%s", raw)
	}
}

func TestStreamHelpAndLiveAlias(t *testing.T) {
	env := setupStreamTest(t)
	mustRun(t, "live")
	for _, want := range []string{"hubfly stream create", "hubfly stream push", "hubfly stream tail", `"live" is an alias`} {
		if !strings.Contains(env.out.String(), want) {
			t.Fatalf("usage missing %q", want)
		}
	}
	mustRun(t, "stream", "inputs", "help")
	if !strings.Contains(env.out.String(), "hubfly stream inputs keys reveal") {
		t.Fatalf("inputs help:\n%s", env.out.String())
	}
}

// ---------------------------------------------------------------------------
// Resources, masking and secrets shown once
// ---------------------------------------------------------------------------

func TestStreamInputsGetMasksKeysInTextAndJSON(t *testing.T) {
	env := setupStreamTest(t)
	input := streamTestInput()
	// A Dashboard that echoed a key must still not reach the terminal.
	input["connection"] = map[string]any{
		"rtmps": map[string]any{"server": "rtmps://live.test:443/live", "streamKey": streamTestKey, "url": "rtmps://live.test:443/live/" + streamTestKey},
		"srt":   map[string]any{"url": "srt://live.test:9000?streamid=publish:" + streamTestKey + "&latency=2000000", "streamId": "publish:" + streamTestKey, "passphrase": ""},
	}
	env.dash.ok("GET", streamTestAPI+"/inputs/sin_cam1abcd", input)
	mustRun(t, "stream", "inputs", "get", "live-show", "Camera")
	text := env.out.String()
	assertNoStreamSecret(t, "inputs get", text)
	if !strings.Contains(text, "hfls_cam1abcd_[masked]") || !strings.Contains(text, "sha256:0a1b2c3d4e5f") {
		t.Fatalf("inputs get output:\n%s", text)
	}
	env.out.buf.Reset()
	mustRun(t, "stream", "inputs", "get", "live-show", "sin_cam1abcd", "--json")
	assertNoStreamSecret(t, "inputs get --json", env.out.String())
	var decoded map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &decoded); err != nil || decoded["id"] != "sin_cam1abcd" {
		t.Fatalf("--json output %q (%v)", env.out.String(), err)
	}
	// The id with its prefix needs no list request.
	if got := len(env.dash.find("GET", streamTestAPI+"/inputs")); got != 1 {
		t.Fatalf("inputs listed %d times, want 1 (by name only)", got)
	}
}

func TestStreamInputsCreatePrintsTheKeyOnce(t *testing.T) {
	env := setupStreamTest(t)
	created := streamTestInput()
	created["connection"] = map[string]any{"rtmps": map[string]any{"server": "rtmps://live-1a2b.live.hubfly.test:443/live", "streamKey": streamTestKey}}
	env.dash.on("POST", streamTestAPI+"/inputs/create", func(rtRecorded) (int, any) {
		return 200, map[string]any{"input": created, "keys": map[string]any{"primary": streamTestKey}, "sync": map[string]any{"status": "applied", "generation": 4}}
	})
	mustRun(t, "stream", "inputs", "create", "live-show", "Camera", "--protocols", "rtmps,SRT", "--max-bitrate", "6000")
	requests := env.dash.find("POST", streamTestAPI+"/inputs/create")
	want := map[string]any{"name": "Camera", "kind": "push", "protocols": []any{"rtmps", "srt"}, "maxBitrateKbps": float64(6000)}
	if len(requests) != 1 || !reflect.DeepEqual(requests[0].Body, want) {
		t.Fatalf("create body = %v, want %v", requests, want)
	}
	text := env.out.String()
	for _, want := range []string{"Created input 'Camera' (sin_cam1abcd).", "shown only now", streamTestKey, "rtmps://live-1a2b.live.hubfly.test:443/live", "B-frames 0"} {
		if !strings.Contains(text, want) {
			t.Fatalf("create output missing %q:\n%s", want, text)
		}
	}
	env.out.buf.Reset()
	mustRun(t, "stream", "inputs", "create", "live-show", "Camera", "--json")
	var decoded struct {
		Keys  map[string]string `json:"keys"`
		Input struct {
			Connection struct {
				RTMPS map[string]string `json:"rtmps"`
			} `json:"connection"`
		} `json:"input"`
	}
	if err := json.Unmarshal([]byte(env.out.String()), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Keys["primary"] != streamTestKey || strings.Contains(decoded.Input.Connection.RTMPS["streamKey"], streamTestSecret) {
		t.Fatalf("--json keys = %v, connection = %v", decoded.Keys, decoded.Input.Connection.RTMPS)
	}
}

func TestStreamInputKeysRevealAndRotateConfirm(t *testing.T) {
	env := setupStreamTest(t)
	mustRun(t, "stream", "inputs", "keys", "reveal", "live-show", "Camera")
	if !strings.Contains(env.out.String(), streamTestKey) {
		t.Fatalf("reveal output:\n%s", env.out.String())
	}
	rotatePath := streamTestAPI + "/inputs/sin_cam1abcd/keys/rotate"
	env.dash.ok("POST", rotatePath, map[string]any{"key": "hfls_cam1abcd_NEWsecretNEWsecretNEWsecretNEW1", "input": streamTestInput(), "sync": map[string]any{"status": "pending"}})
	err := run([]string{"stream", "inputs", "keys", "rotate", "live-show", "Camera", "--role", "backup"})
	if err == nil || !strings.Contains(err.Error(), "--yes") || len(env.dash.find("POST", rotatePath)) != 0 {
		t.Fatalf("rotate without --yes = %v", err)
	}
	mustRun(t, "stream", "inputs", "keys", "rotate", "live-show", "Camera", "--role", "backup", "--yes")
	requests := env.dash.find("POST", rotatePath)
	if len(requests) != 1 || !reflect.DeepEqual(requests[0].Body, map[string]any{"role": "backup"}) {
		t.Fatalf("rotate requests = %+v", requests)
	}
	if !strings.Contains(env.out.String(), "NEWsecretNEWsecret") || !strings.Contains(env.errOut.String(), "background") {
		t.Fatalf("rotate output:\n%s\n%s", env.out.String(), env.errOut.String())
	}
}

func TestStreamSpeedQuotesAndConfirms(t *testing.T) {
	env := setupStreamTest(t)
	env.dash.ok("GET", "/api/v1/managed/streams/price", map[string]any{
		"hourly":  map[string]any{"speedMicroUsd": 4100, "speedMicroUsdAfterFree": 3075},
		"monthly": map[string]any{"speedUsd": 2.99, "speedUsdAfterFree": 2.24}, "freeSpeedMbps": 25,
	})
	env.dash.ok("POST", streamTestBase+"/resize", map[string]any{"operationId": "op_resize1"})
	err := run([]string{"stream", "speed", "live-show", "100"})
	if err == nil || !strings.Contains(err.Error(), "--yes") || len(env.dash.find("POST", streamTestBase+"/resize")) != 0 {
		t.Fatalf("speed without --yes = %v", err)
	}
	for _, want := range []string{"100 Mbps, $2.99/month", "$2.24/month if this is your only running server (25 Mbps are free)"} {
		if !strings.Contains(env.out.String(), want) {
			t.Fatalf("quote missing %q:\n%s", want, env.out.String())
		}
	}
	quotes := env.dash.find("GET", "/api/v1/managed/streams/price")
	if len(quotes) != 1 || quotes[0].Query.Get("speedMbps") != "100" {
		t.Fatalf("price requests = %+v", quotes)
	}

	rtInteractive = func() bool { return true }
	setTestStdin("y\n")
	mustRun(t, "stream", "speed", "live-show", "100mbps", "--no-wait")
	requests := env.dash.find("POST", streamTestBase+"/resize")
	if len(requests) != 1 || !reflect.DeepEqual(requests[0].Body, map[string]any{"speedMbps": float64(100), "quotedHourlyTotal": 0.0041}) {
		t.Fatalf("resize requests = %+v", requests)
	}
	if !strings.Contains(env.out.String(), "Changing 'live-show' to 100 Mbps (operation op_resize1)") {
		t.Fatalf("speed output:\n%s", env.out.String())
	}
}

func TestStreamChannelsUpdateMergesNestedObjectsAndSendsRevision(t *testing.T) {
	env := setupStreamTest(t)
	updatePath := streamTestAPI + "/channels/sch_main0001/update"
	env.dash.ok("PUT", updatePath, map[string]any{"channel": streamTestChannel(), "sync": map[string]any{"status": "applied"}})
	mustRun(t, "stream", "channels", "update", "live-show", "Main", "--access", "token", "--no-webrtc", "--source", "mixer:Show", "--dvr", "60")
	requests := env.dash.find("PUT", updatePath)
	if len(requests) != 1 {
		t.Fatalf("update requests = %+v", requests)
	}
	want := map[string]any{
		"revision":   float64(3),
		"dvrMinutes": float64(60),
		"source":     map[string]any{"type": "mixer", "ref": "smx_show0001"},
		"access":     map[string]any{"mode": "token", "allowedOrigins": []any{}, "allowedCidrs": []any{"10.0.0.0/8"}},
		"playback":   map[string]any{"hls": true, "llHls": true, "webrtc": false, "icecast": []any{}, "rtmpPull": false, "srtPull": false},
	}
	if !reflect.DeepEqual(requests[0].Body, want) {
		t.Fatalf("update body = %v\nwant %v", requests[0].Body, want)
	}
	if !strings.Contains(env.out.String(), "Updated channel 'Main' (sch_main0001).") {
		t.Fatalf("update output:\n%s", env.out.String())
	}
}

func TestStreamRevisionConflictMapsToAHint(t *testing.T) {
	env := setupStreamTest(t)
	env.dash.on("PUT", streamTestAPI+"/channels/sch_main0001/update", func(rtRecorded) (int, any) {
		return http.StatusConflict, map[string]any{"ok": false, "error": map[string]any{"code": "STREAM_REVISION_CONFLICT", "message": "The channel changed"}}
	})
	env.dash.on("POST", streamTestAPI+"/channels/create", func(rtRecorded) (int, any) {
		return 422, map[string]any{"ok": false, "error": map[string]any{"code": "STREAM_INVALID", "message": "Invalid channel",
			"details": []any{map[string]any{"path": "channels[0].dvr_minutes", "code": "out_of_range", "message": "dvr_minutes is 0-360"}}}}
	})
	err := run([]string{"stream", "channels", "update", "live-show", "Main", "--latency", "ultra"})
	if err == nil || !strings.Contains(err.Error(), "Run the command again") || exitCodeForError(err) != 4 {
		t.Fatalf("conflict error = %v (exit %d)", err, exitCodeForError(err))
	}
	err = run([]string{"stream", "channels", "create", "live-show", "Side", "--source", "input:Camera"})
	if err == nil || !strings.Contains(err.Error(), "channels[0].dvr_minutes: dvr_minutes is 0-360 (out_of_range)") {
		t.Fatalf("invalid error = %v", err)
	}
}

func TestStreamDestinationCreateReadsTheKeyFromStdin(t *testing.T) {
	env := setupStreamTest(t)
	const platformKey = "abcd-efgh-ijkl-mnop-qrst"
	rtStdin = strings.NewReader(platformKey + "\n")
	rtStdinIsTerminal = func() bool { return false }
	env.dash.on("POST", streamTestAPI+"/destinations/create", func(rtRecorded) (int, any) {
		return 200, map[string]any{"destination": map[string]any{"id": "sds_yt000002", "name": "YT", "platform": "youtube", "streamKeyHint": "…qrst"}, "sync": map[string]any{"status": "applied"}}
	})
	mustRun(t, "stream", "destinations", "create", "live-show", "YT", "--platform", "youtube", "--source", "channel:Main", "--stream-key-stdin", "--auto-start=false")
	requests := env.dash.find("POST", streamTestAPI+"/destinations/create")
	want := map[string]any{"name": "YT", "platform": "youtube", "source": map[string]any{"type": "channel", "ref": "sch_main0001"}, "streamKey": platformKey, "autoStart": false}
	if len(requests) != 1 || !reflect.DeepEqual(requests[0].Body, want) {
		t.Fatalf("create body = %+v, want %v", requests, want)
	}
	if strings.Contains(env.out.String()+env.errOut.String(), platformKey) {
		t.Fatalf("the destination key was printed:\n%s%s", env.out.String(), env.errOut.String())
	}
}

func TestStreamDestinationStartGoesThroughControl(t *testing.T) {
	env := setupStreamTest(t)
	env.dash.ok("POST", streamTestAPI+"/control", map[string]any{"id": "x", "results": []any{map[string]any{"ok": true}}})
	mustRun(t, "stream", "destinations", "start", "live-show", "YouTube")
	requests := env.dash.find("POST", streamTestAPI+"/control")
	ops, _ := requests[0].Body["ops"].([]any)
	if len(requests) != 1 || !reflect.DeepEqual(ops, []any{map[string]any{"op": "destination.start", "destination": "sds_yt000001"}}) {
		t.Fatalf("control requests = %+v", requests)
	}
	if !strings.Contains(env.out.String(), "ok     destination.start sds_yt000001") {
		t.Fatalf("output:\n%s", env.out.String())
	}
}

func TestStreamControlBuildsOpsAndReportsFailures(t *testing.T) {
	env := setupStreamTest(t)
	env.dash.ok("POST", streamTestAPI+"/control", map[string]any{"id": "x", "results": []any{
		map[string]any{"ok": true}, map[string]any{"ok": true},
		map[string]any{"ok": false, "error": map[string]any{"code": "not_live", "message": "the channel is offline"}},
		map[string]any{"ok": true},
	}})
	err := run([]string{"stream", "control", "live-show", "--go-live", "Main", "--destination-start", "sds_yt000001", "--slate", "Main=on",
		"--op", `{"op":"playlist.next","channel":"sch_main0001"}`, "--json"})
	if err == nil || !strings.Contains(err.Error(), "1 of 4 control operations failed") {
		t.Fatalf("control error = %v", err)
	}
	requests := env.dash.find("POST", streamTestAPI+"/control")
	if len(requests) != 1 {
		t.Fatalf("control requests = %+v", requests)
	}
	id, _ := requests[0].Body["id"].(string)
	wantOps := []any{
		map[string]any{"op": "channel.start", "channel": "sch_main0001"},
		map[string]any{"op": "destination.start", "destination": "sds_yt000001"},
		map[string]any{"op": "channel.slate", "channel": "sch_main0001", "on": true},
		map[string]any{"op": "playlist.next", "channel": "sch_main0001"},
	}
	if !strings.HasPrefix(id, "ctl_") || len(id) < 8 || !reflect.DeepEqual(requests[0].Body["ops"], wantOps) {
		t.Fatalf("control body = %v", requests[0].Body)
	}
	var decoded map[string]any
	if json.Unmarshal([]byte(env.out.String()), &decoded) != nil {
		t.Fatalf("--json output %q", env.out.String())
	}
}

func TestStreamSceneSwitchResolvesNamesAndFades(t *testing.T) {
	env := setupStreamTest(t)
	env.dash.ok("POST", streamTestAPI+"/control", map[string]any{"id": "x", "results": []any{map[string]any{"ok": true}}})
	mustRun(t, "stream", "scene", "switch", "live-show", "Show", "intro", "--fade", "1s")
	requests := env.dash.find("POST", streamTestAPI+"/control")
	want := []any{map[string]any{"op": "mixer.scene", "mixer": "smx_show0001", "scene": "sc_intro", "transition": map[string]any{"type": "fade", "durationMs": float64(1000)}}}
	if len(requests) != 1 || !reflect.DeepEqual(requests[0].Body["ops"], want) {
		t.Fatalf("control requests = %+v", requests)
	}
	err := run([]string{"stream", "scene", "switch", "live-show", "Show", "Outro"})
	if err == nil || !strings.Contains(err.Error(), "scene 'Outro' not found") || !strings.Contains(err.Error(), "Intro (sc_intro)") {
		t.Fatalf("missing scene error = %v", err)
	}
	mustRun(t, "stream", "scene", "list", "live-show", "Show")
	if !strings.Contains(env.out.String(), "Live   1       yes") {
		t.Fatalf("scene list:\n%s", env.out.String())
	}
}

func TestStreamPlaybackKeyAndWebhookSecretsPrintOnce(t *testing.T) {
	env := setupStreamTest(t)
	const secret = "pk_secret_value_0123456789abcdef0123456789"
	const whsec = "whsec_abcdefghijklmnop0123456789"
	env.dash.ok("POST", streamTestAPI+"/playback-keys/create", map[string]any{
		"key": map[string]any{"id": "spk_key00001", "name": "web", "status": "active", "fingerprint": "sha256:aa11bb22cc33"}, "secret": secret,
	})
	env.dash.ok("POST", streamTestAPI+"/webhooks/create", map[string]any{
		"webhook": map[string]any{"id": "swh_hook0001", "url": "https://example.test/hook", "events": []string{"channel.live"}, "fingerprint": "sha256:dd44"}, "secret": whsec,
	})
	env.dash.ok("GET", streamTestAPI+"/webhooks", []map[string]any{{"id": "swh_hook0001", "url": "https://example.test/hook?token=abc", "events": []string{}, "enabled": true, "secret": whsec}})
	mustRun(t, "stream", "playback-keys", "create", "live-show", "web", "--json")
	var decoded map[string]any
	if err := json.Unmarshal([]byte(env.out.String()), &decoded); err != nil || decoded["secret"] != secret {
		t.Fatalf("playback key --json = %q (%v)", env.out.String(), err)
	}
	env.out.buf.Reset()
	mustRun(t, "stream", "webhooks", "create", "live-show", "--url", "https://example.test/hook", "--events", "channel.live")
	if !strings.Contains(env.out.String(), whsec) {
		t.Fatalf("webhook create output:\n%s", env.out.String())
	}
	requests := env.dash.find("POST", streamTestAPI+"/webhooks/create")
	if !reflect.DeepEqual(requests[0].Body, map[string]any{"url": "https://example.test/hook", "events": []any{"channel.live"}}) {
		t.Fatalf("webhook body = %v", requests[0].Body)
	}
	env.out.buf.Reset()
	mustRun(t, "stream", "webhooks", "live-show")
	mustRun(t, "stream", "webhooks", "list", "live-show", "--json")
	if strings.Contains(env.out.String(), whsec) || strings.Contains(env.out.String(), "token=abc") || !strings.Contains(env.out.String(), "all") {
		t.Fatalf("webhook list leaked a secret:\n%s", env.out.String())
	}
}

func TestStreamDeleteNeedsConfirmation(t *testing.T) {
	env := setupStreamTest(t)
	deletePath := streamTestAPI + "/inputs/sin_cam1abcd/delete"
	env.dash.ok("DELETE", deletePath, map[string]any{"deleted": true, "sync": map[string]any{"status": "applied"}})
	err := run([]string{"stream", "inputs", "delete", "live-show", "Camera"})
	if err == nil || !strings.Contains(err.Error(), "--yes") || len(env.dash.find("DELETE", deletePath)) != 0 {
		t.Fatalf("delete without --yes = %v", err)
	}
	mustRun(t, "stream", "inputs", "rm", "live-show", "Camera", "--yes")
	if len(env.dash.find("DELETE", deletePath)) != 1 || !strings.Contains(env.out.String(), "Deleted input 'Camera'.") {
		t.Fatalf("delete output:\n%s", env.out.String())
	}
}

func TestStreamListAndLive(t *testing.T) {
	env := setupStreamTest(t)
	mustRun(t, "stream", "list")
	if !strings.Contains(env.out.String(), "live-show") || strings.Contains(env.out.String(), "chat") {
		t.Fatalf("list output:\n%s", env.out.String())
	}
	env.dash.ok("GET", streamTestAPI+"/live", map[string]any{
		"server": map[string]any{"runState": "running", "viewers": 42, "egressMbps": 12.5, "speedMbps": 25},
		"inputs": []any{map[string]any{"id": "sin_cam1abcd", "state": "live", "activeRole": "primary", "publishers": []any{
			map[string]any{"role": "primary", "protocol": "srt", "video": map[string]any{"width": 1920, "height": 1080, "fps": 30, "bitrateKbps": 4500, "bframes": true}},
		}}},
		"channels": []any{map[string]any{"id": "sch_main0001", "state": "live", "viewers": 42}},
	})
	env.out.buf.Reset()
	mustRun(t, "stream", "live", "live-show")
	for _, want := range []string{"Server: running, 42 viewers", "role primary; primary via srt 1920x1080 30 fps 4500 kbps (B-frames: no WebRTC passthrough)", "viewers 42"} {
		if !strings.Contains(env.out.String(), want) {
			t.Fatalf("live output missing %q:\n%s", want, env.out.String())
		}
	}
}

// ---------------------------------------------------------------------------
// play
// ---------------------------------------------------------------------------

func TestStreamPlayURLOnlyAndPlayer(t *testing.T) {
	env := setupStreamTest(t)
	const viewerToken = "eyJhbGciOiJIUzI1NiJ9.eyJjaCI6InNjaF9tYWluMDAwMSJ9.c2lnbmF0dXJl"
	env.dash.ok("GET", streamTestAPI+"/channels/sch_main0001/playback", map[string]any{
		"urls": map[string]any{"hls": "https://live-1a2b.live.hubfly.test/hls/sch_main0001/index.m3u8"}, "embedHtml": "", "tokenRequired": true,
	})
	env.dash.ok("POST", streamTestAPI+"/channels/sch_main0001/token/create", map[string]any{"token": viewerToken, "expiresAt": "2026-10-29T10:00:00Z"})
	mustRun(t, "stream", "play", "live-show", "Main", "--url-only", "--ttl", "30m")
	if got := strings.TrimSpace(env.out.String()); got != "https://live-1a2b.live.hubfly.test/hls/sch_main0001/index.m3u8?token="+viewerToken {
		t.Fatalf("--url-only = %q", got)
	}
	tokens := env.dash.find("POST", streamTestAPI+"/channels/sch_main0001/token/create")
	if len(tokens) != 1 || !reflect.DeepEqual(tokens[0].Body, map[string]any{"ttlSeconds": float64(1800), "sub": "hubfly-cli"}) {
		t.Fatalf("token requests = %+v", tokens)
	}
	var ran []string
	streamLookPath = func(name string) (string, error) {
		if name == "mpv" {
			return "", os.ErrNotExist
		}
		return "/usr/bin/" + name, nil
	}
	streamRunPlayer = func(program string, args []string) error {
		ran = append([]string{program}, args...)
		return nil
	}
	mustRun(t, "stream", "play", "live-show", "sch_main0001")
	if len(ran) == 0 || ran[0] != "/usr/bin/ffplay" || !strings.HasSuffix(ran[len(ran)-1], "?token="+viewerToken) {
		t.Fatalf("player argv = %q", ran)
	}
	streamLookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if err := run([]string{"stream", "play", "live-show", "Main"}); err == nil || !strings.Contains(err.Error(), "--url-only") {
		t.Fatalf("no player error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// create, lifecycle, recordings, schedules
// ---------------------------------------------------------------------------

func TestStreamCreateShowsTheFirstKeyOnce(t *testing.T) {
	env := setupStreamTest(t)
	env.dash.ok("GET", "/api/v1/managed/streams/price", map[string]any{
		"hourly":  map[string]any{"speedMicroUsd": 1025, "speedMicroUsdAfterFree": 0},
		"monthly": map[string]any{"speedUsd": 0.75, "speedUsdAfterFree": 0}, "freeSpeedMbps": 25,
	})
	createPath := "/api/v1/projects/p-man/managed/instances/create"
	env.dash.ok("POST", createPath, map[string]any{
		"instance": rtTestInstance("msi_new", "acme-tv", "stream"), "operationId": "op_create1",
		"stream": map[string]any{"host": "acme-tv.live.hubfly.test", "firstInput": map[string]any{"id": "sin_cam1abcd", "name": "Main", "keys": map[string]any{"primary": streamTestKey}}},
	})
	err := run([]string{"stream", "create", "acme-tv", "--project", "rt-project"})
	if err == nil || !strings.Contains(err.Error(), "--yes") || len(env.dash.find("POST", createPath)) != 0 {
		t.Fatalf("create without --yes = %v", err)
	}
	mustRun(t, "stream", "create", "acme-tv", "--project", "rt-project", "--timezone", "Africa/Kigali", "--yes", "--no-wait")
	requests := env.dash.find("POST", createPath)
	if len(requests) != 1 {
		t.Fatalf("create requests = %+v", requests)
	}
	body := requests[0].Body
	wantStream := map[string]any{"speedMbps": float64(25), "timezone": "Africa/Kigali", "firstInput": map[string]any{"name": "Main", "kind": "push"}}
	if body["service"] != "stream" || body["name"] != "acme-tv" || body["quotedHourlyTotal"] != 0.001025 || !reflect.DeepEqual(body["stream"], wantStream) {
		t.Fatalf("create body = %v", body)
	}
	for _, want := range []string{"rtmps://acme-tv.live.hubfly.test:443/live", streamTestKey, "hubfly stream inputs keys reveal acme-tv sin_cam1abcd"} {
		if !strings.Contains(env.out.String(), want) {
			t.Fatalf("create output missing %q:\n%s", want, env.out.String())
		}
	}
}

func TestStreamStopUsesTheSharedLifecycle(t *testing.T) {
	env := setupStreamTest(t)
	env.dash.ok("POST", streamTestBase+"/stop", map[string]any{"operationId": "op_stop1"})
	mustRun(t, "stream", "stop", "live-show", "--no-wait")
	if len(env.dash.find("POST", streamTestBase+"/stop")) != 1 || !strings.Contains(env.errOut.String(), "offline card") {
		t.Fatalf("stop: out=%q err=%q", env.out.String(), env.errOut.String())
	}
}

func TestStreamRecordingsListFiltersByChannel(t *testing.T) {
	env := setupStreamTest(t)
	env.dash.ok("GET", streamTestAPI+"/recordings", map[string]any{"items": []any{map[string]any{
		"id": "srec_0001abcd", "name": "Main 2026-10-29", "kind": "recording", "channelId": "sch_main0001", "status": "completed",
		"bytes": 1048576, "bucket": "recordings", "objectKey": "main/0001.mp4", "startedAt": "2026-10-29T08:00:00Z",
	}}, "nextCursor": "c2"})
	mustRun(t, "stream", "recordings", "live-show", "--channel", "Main", "--kind", "recording")
	requests := env.dash.find("GET", streamTestAPI+"/recordings")
	if len(requests) != 1 || requests[0].Query.Get("channelId") != "sch_main0001" || requests[0].Query.Get("kind") != "recording" {
		t.Fatalf("recordings requests = %+v", requests)
	}
	if !strings.Contains(env.out.String(), "recordings/main/0001.mp4") || !strings.Contains(env.errOut.String(), "--cursor c2") {
		t.Fatalf("recordings output:\n%s\n%s", env.out.String(), env.errOut.String())
	}
	env.dash.ok("POST", streamTestAPI+"/clips/create", map[string]any{"clipId": "sclp_0001abcd"})
	mustRun(t, "stream", "clips", "create", "live-show", "--channel", "Main", "--from", "-90s", "--to", "-0s", "--name", "Goal")
	clips := env.dash.find("POST", streamTestAPI+"/clips/create")
	if len(clips) != 1 || !reflect.DeepEqual(clips[0].Body, map[string]any{"channelId": "sch_main0001", "from": "-90s", "to": "-0s", "name": "Goal"}) {
		t.Fatalf("clip requests = %+v", clips)
	}
}

func TestStreamSchedulesCreateUsesTheServerTimeZone(t *testing.T) {
	env := setupStreamTest(t)
	env.dash.ok("POST", streamTestAPI+"/schedules/create", map[string]any{"schedule": map[string]any{"id": "ssc_news0001", "name": "News"}, "sync": map[string]any{"status": "applied"}})
	mustRun(t, "stream", "schedules", "create", "live-show", "News", "--start", "2026-11-02 18:00", "--duration", "60",
		"--rrule", "FREQ=DAILY", "--on-start", `{"op":"channel.start","channel":"sch_main0001"}`)
	requests := env.dash.find("POST", streamTestAPI+"/schedules/create")
	want := map[string]any{"name": "News", "start": "2026-11-02T18:00:00", "timezone": "Africa/Kigali", "durationMinutes": float64(60), "rrule": "FREQ=DAILY",
		"onStart": []any{map[string]any{"op": "channel.start", "channel": "sch_main0001"}}}
	if len(requests) != 1 || !reflect.DeepEqual(requests[0].Body, want) {
		t.Fatalf("schedule body = %+v\nwant %v", requests, want)
	}
}
