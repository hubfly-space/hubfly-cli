package cli

import (
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

// `hubfly stream play`: open a channel's HLS playback in mpv or ffplay, with
// a short-lived test token when the channel needs one (signed by the
// Dashboard with the server's newest active playback key).

// streamRunPlayer starts the player and waits for it (tests replace it).
var streamRunPlayer = func(program string, args []string) error {
	cmd := exec.Command(program, args...)
	cmd.Stdout, cmd.Stderr = rtErr, rtErr
	return cmd.Run()
}

// streamPlayerArgs returns the arguments of a known player for url.
func streamPlayerArgs(player, playURL, title string) []string {
	if player == "ffplay" {
		return []string{"-hide_banner", "-loglevel", "warning", "-window_title", title, playURL}
	}
	return []string{"--force-media-title=" + title, playURL}
}

func streamPlayFlow(args []string) error {
	const usage = "hubfly stream play <server> <channel> [--url-only] [--ttl <duration>] [--player mpv|ffplay] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream play")
	urlOnly := fs.Bool("url-only", false, "")
	ttl := fs.String("ttl", "1h", "")
	player := fs.String("player", "", "")
	asJSON := fs.Bool("json", false, "")
	seconds := 0
	target, positionals, err := streamParseTarget(fs, args, usage, 2, func([]string) error {
		var ttlErr error
		if seconds, ttlErr = parseStreamTTL(*ttl); ttlErr != nil {
			return ttlErr
		}
		return streamOneOf("player", *player, "mpv", "ffplay")
	})
	if err != nil {
		return err
	}
	channel, err := newStreamResolver(target).find(streamResourceByName("channels"), positionals[1])
	if err != nil {
		return err
	}
	channelID := streamString(channel, "id")
	playback, err := fetchStreamPlayback(target.token, target.instance, channelID)
	if err != nil {
		return streamAPIError(err)
	}
	if playback.URLs.HLS == "" {
		return fmt.Errorf("channel '%s' has no HLS output; turn it on with: hubfly stream channels update %s %s --hls", terminalSafe(streamString(channel, "name")), target.instance.Name, channelID)
	}
	playURL := playback.URLs.HLS
	expiresAt := ""
	if playback.TokenRequired {
		token, expires, tokenErr := createStreamTestToken(target.token, target.instance, channelID, map[string]any{"ttlSeconds": seconds, "sub": "hubfly-cli"})
		if tokenErr != nil {
			return streamAPIError(tokenErr)
		}
		parsed, parseErr := url.Parse(playURL)
		if parseErr != nil {
			return fmt.Errorf("the Dashboard returned an invalid playback URL")
		}
		query := parsed.Query()
		query.Set("token", token)
		parsed.RawQuery = query.Encode()
		playURL, expiresAt = parsed.String(), expires
	}
	if *asJSON {
		// The signed URL is what the user asked for.
		return rtPrintJSON(map[string]any{"channelId": channelID, "url": playURL, "tokenRequired": playback.TokenRequired, "expiresAt": expiresAt})
	}
	if *urlOnly {
		_, _ = fmt.Fprintln(rtOut, playURL)
		if expiresAt != "" {
			_, _ = fmt.Fprintf(rtErr, "The test token expires %s.\n", dbdTime(expiresAt))
		}
		return nil
	}
	candidates := []string{"mpv", "ffplay"}
	if *player != "" {
		candidates = []string{*player}
	}
	for _, candidate := range candidates {
		program, lookErr := streamLookPath(candidate)
		if lookErr != nil {
			continue
		}
		title := "HubFly: " + streamString(channel, "name")
		_, _ = fmt.Fprintf(rtErr, "Opening channel '%s' in %s (low-latency HLS can take a few seconds to start)…\n", terminalSafe(streamString(channel, "name")), candidate)
		if err := streamRunPlayer(program, streamPlayerArgs(candidate, playURL, title)); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return fmt.Errorf("%s exited with code %d", candidate, exitErr.ExitCode())
			}
			return fmt.Errorf("start %s: %v", candidate, err)
		}
		return nil
	}
	return fmt.Errorf("neither %s was found. Install mpv (https://mpv.io) or ffmpeg's ffplay, or print the URL with --url-only and open it in any HLS player.\n%s",
		strings.Join(candidates, " nor "), streamPushInstallHint(streamGOOS))
}
