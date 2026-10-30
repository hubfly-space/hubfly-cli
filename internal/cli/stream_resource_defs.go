package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// The stream resources and their create/update flags. Bodies follow
// openapi.json (camelCase); anything the flags do not cover goes through
// --file <spec.json|->.

func init() {
	streamResources = []*streamResource{
		streamInputsResource(),
		streamChannelsResource(),
		streamDestinationsResource(),
		{
			Name: "mixers", Aliases: []string{"mixer"}, Noun: "mixer", Path: "/mixers", IDPrefix: "smx_", NameField: "name", ResultKey: "mixer", HasGet: true,
			Columns: []streamColumn{
				streamCol("NAME", "name"), streamColFunc("ENABLED", func(i map[string]any) string { return streamOnOff(i, "enabled") }),
				streamColFunc("CANVAS", func(i map[string]any) string {
					return fmt.Sprintf("%sx%s@%s", streamString(i, "canvas.width"), streamString(i, "canvas.height"), streamString(i, "canvas.fps"))
				}),
				streamColFunc("SCENES", func(i map[string]any) string { scenes, _ := i["scenes"].([]any); return strconv.Itoa(len(scenes)) }),
				streamCol("DEFAULT SCENE", "defaultScene"), streamCol("ID", "id"),
			},
			Define:      defineStreamMixer,
			CreateArg:   "<name>",
			CreateUsage: "hubfly stream mixers create <server> <name> [--preset vertical|1080p30|1080p60|720p30] [--disabled] [--file <spec.json|->] [--json]",
			UpdateUsage: "hubfly stream mixers update <server> <mixer> [--name <name>] [--enable|--disable] [--default-scene <scene>] [--transition cut|fade:<duration>] [--file <spec.json|->] [--json]",
			Create:      true, Update: true, Delete: true,
			DeleteNote: "Channels that play it go to their backup or slate.",
			Usage: `  hubfly stream mixers [list] <server> [--json]
  hubfly stream mixers get <server> <mixer> [--json]
  hubfly stream mixers create <server> <name> [--preset vertical|1080p30|1080p60|720p30] [--disabled] [--file <spec.json|->]
  hubfly stream mixers update <server> <mixer> [--name <name>] [--enable|--disable] [--default-scene <scene>]
                              [--transition cut|fade:<duration>] [--file <spec.json|->]
  hubfly stream mixers delete <server> <mixer> [--yes]
  hubfly stream scene list|switch … (live scene changes)`,
		},
		{
			Name: "playlists", Aliases: []string{"playlist"}, Noun: "playlist", Path: "/playlists", IDPrefix: "spl_", NameField: "name", ResultKey: "playlist", HasGet: true,
			Columns: []streamColumn{
				streamCol("NAME", "name"),
				streamColFunc("ITEMS", func(i map[string]any) string { items, _ := i["items"].([]any); return strconv.Itoa(len(items)) }),
				streamColFunc("LOOP", func(i map[string]any) string { return streamOnOff(i, "loop") }),
				streamColFunc("SHUFFLE", func(i map[string]any) string { return streamOnOff(i, "shuffle") }),
				streamCol("ENCODING", "encoding"), streamCol("ID", "id"),
			},
			Define:      defineStreamPlaylist,
			CreateArg:   "<name>",
			CreateUsage: "hubfly stream playlists create <server> <name> --item <url|asset:<link>:<asset id>|object:<link>:<key>>... [--loop] [--shuffle] [--encoding auto|reencode] [--file <spec.json|->] [--json]",
			UpdateUsage: "hubfly stream playlists update <server> <playlist> [--name <name>] [--item …]... [--loop[=false]] [--shuffle[=false]] [--encoding auto|reencode] [--file <spec.json|->] [--json]",
			Create:      true, Update: true, Delete: true,
			Usage: `  hubfly stream playlists [list] <server> [--json]
  hubfly stream playlists get <server> <playlist> [--json]
  hubfly stream playlists create <server> <name> --item <url|asset:<link>:<asset id>|object:<link>:<key>>...
                                 [--loop] [--shuffle] [--encoding auto|reencode] [--file <spec.json|->]
  hubfly stream playlists update <server> <playlist> [--name <name>] [--item …]... [--loop[=false]] [--shuffle[=false]]
  hubfly stream playlists delete <server> <playlist> [--yes]`,
		},
		streamSchedulesResource(),
		streamRecordingsResource(),
		streamClipsResource(),
		{
			Name: "playback-keys", Aliases: []string{"playback-key", "pkeys"}, Noun: "playback key", Path: "/playback-keys", IDPrefix: "spk_", NameField: "name", ResultKey: "key",
			Columns: []streamColumn{
				streamCol("NAME", "name"), streamCol("STATUS", "status"), streamCol("RETIRING UNTIL", "retiringUntil"),
				streamCol("FINGERPRINT", "fingerprint"), streamCol("ID", "id"),
			},
			CreateArg:   "<name>",
			CreateUsage: "hubfly stream playback-keys create <server> <name> [--json]",
			Create:      true, Delete: true,
			DeleteNote: "Tokens signed with it stop working at once.",
			Once:       []string{"secret"},
			Extras:     map[string]func(*streamResource, []string) error{"rotate": streamPlaybackKeyRotateFlow},
			Usage: `  hubfly stream playback-keys [list] <server> [--json]
  hubfly stream playback-keys create <server> <name> [--json]        (the secret prints once)
  hubfly stream playback-keys rotate <server> <key> [--yes] [--json]  (new secret once; the old key retires in 24 h)
  hubfly stream playback-keys delete <server> <key> [--yes]`,
		},
		streamWebhooksResource(),
		streamGraphicsResource(),
		{
			Name: "storage-links", Aliases: []string{"storage-link", "storage"}, Noun: "storage link", Path: "/storage-links", IDPrefix: "ssl_", NameField: "bucket", ResultKey: "storageLink",
			Columns: []streamColumn{
				streamCol("BUCKET", "bucket"), streamCol("MODE", "mode"), streamCol("STORAGE PROJECT", "storageProjectId"),
				streamCol("REGION", "region"), streamCol("ID", "id"),
			},
			Define:      defineStreamStorageLink,
			CreateUsage: "hubfly stream storage-links create <server> --storage-project <id|name> --bucket <bucket> [--mode read|write|read_write] [--json]",
			Create:      true, Delete: true,
			DeleteNote: "Recordings, playlists and graphics that use it stop working; its access key is revoked.",
			Extras:     map[string]func(*streamResource, []string) error{"assets": streamStorageAssetsFlow},
			Usage: `  hubfly stream storage-links [list] <server> [--json]
  hubfly stream storage-links create <server> --storage-project <id|name> --bucket <bucket> [--mode read|write|read_write]
  hubfly stream storage-links assets <server> <link> [--search <text>] [--hls-ready] [--json]
  hubfly stream storage-links delete <server> <link> [--yes]`,
		},
		{
			Name: "domains", Aliases: []string{"domain"}, Noun: "domain", Path: "/domains", IDPrefix: "sdm_", NameField: "hostname", ResultKey: "domain",
			Columns: []streamColumn{
				streamCol("HOSTNAME", "hostname"), streamCol("PURPOSE", "purpose"),
				streamColFunc("VERIFIED", func(i map[string]any) string {
					if i["verified"] == true {
						return "yes"
					}
					if text := streamString(i, "lastCheckError"); text != "" {
						return "no (" + terminalSafe(text) + ")"
					}
					return "no"
				}),
				streamCol("CNAME TARGET", "cnameTarget"), streamCol("ID", "id"),
			},
			Define:      defineStreamDomain,
			CreateArg:   "<hostname>",
			CreateUsage: "hubfly stream domains create <server> <hostname> [--purpose both|playback|ingest] [--json]",
			Create:      true, Delete: true,
			Once:   []string{},
			Extras: map[string]func(*streamResource, []string) error{"verify": streamDomainVerifyFlow},
			Usage: `  hubfly stream domains [list] <server> [--json]
  hubfly stream domains create <server> <hostname> [--purpose both|playback|ingest]   (prints the CNAME to add)
  hubfly stream domains verify <server> <domain> [--json]
  hubfly stream domains delete <server> <domain> [--yes]`,
		},
		{
			Name: "bindings", Aliases: []string{"binding", "bind"}, Noun: "binding", Path: "/bindings", NameField: "targetProjectName", ResultKey: "",
			Columns: []streamColumn{
				streamCol("PROJECT", "targetProjectName"), streamCol("PLAYBACK KEY", "playbackKeyId"), streamCol("CHANNEL", "channelId"),
				streamCol("PREFIX", "clientPrefix"), streamColFunc("VARIABLES", func(i map[string]any) string { return streamListOf(i, "envKeys") }),
				streamCol("ID", "id"),
			},
			Define:      defineStreamBinding,
			CreateUsage: "hubfly stream bindings create <server> --to <project> [--playback-key <key>] [--channel <channel>] [--client-prefix none|NEXT_PUBLIC_|VITE_|PUBLIC_] [--json]",
			Create:      true, Delete: true,
			DeleteNote: "",
			Usage: `  hubfly stream bindings [list] <server> [--json]
  hubfly stream bindings create <server> --to <project> [--playback-key <key>] [--channel <channel>]
                                [--client-prefix none|NEXT_PUBLIC_|VITE_|PUBLIC_]
                                (writes HUBFLY_STREAM_URL and the playback key into the project's variables)
  hubfly stream bindings delete <server> <binding> [--remove-variables] [--yes]`,
		},
	}
}

// ---------------------------------------------------------------------------
// Shared flag helpers
// ---------------------------------------------------------------------------

// streamEnableFlags registers --enable/--disable (update) or --disabled
// (create) and returns a setter for the body.
func streamEnableFlags(fs *flag.FlagSet, mode string) (validate func() error, apply func(body map[string]any)) {
	enable, disable := new(bool), new(bool)
	if mode == "create" {
		fs.BoolVar(disable, "disabled", false, "")
	} else {
		fs.BoolVar(enable, "enable", false, "")
		fs.BoolVar(disable, "disable", false, "")
	}
	return func() error {
			if *enable && *disable {
				return errors.New("invalid flags: pass --enable or --disable, not both")
			}
			return nil
		}, func(body map[string]any) {
			if *enable {
				body["enabled"] = true
			}
			if *disable {
				body["enabled"] = false
			}
		}
}

// streamNested returns body[key] as an object to change: the body's own, or
// a copy of the current resource's (update), or a copy of defaults.
func streamNested(b *streamBuild, key string, defaults map[string]any) map[string]any {
	if existing, ok := b.body[key].(map[string]any); ok {
		return existing
	}
	result := map[string]any{}
	source := defaults
	if current, ok := b.current[key].(map[string]any); ok && b.mode == "update" {
		source = current
	}
	for k, v := range source {
		result[k] = v
	}
	b.body[key] = result
	return result
}

// readStreamSecretLine reads one line of a secret from stdin. Secrets never
// come from argv (process listings) or the environment.
func readStreamSecretLine(what string) (string, error) {
	if rtStdinIsTerminal() {
		_, _ = fmt.Fprintf(rtErr, "%s: ", what)
	}
	line, err := readLineFrom(rtStdin)
	if rtStdinIsTerminal() {
		_, _ = fmt.Fprintln(rtErr)
	}
	if err != nil && line == "" {
		return "", fmt.Errorf("read the %s from stdin: %w", strings.ToLower(what), err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", fmt.Errorf("invalid %s: empty", strings.ToLower(what))
	}
	return line, nil
}

// readLineFrom reads up to a newline one byte at a time, so the rest of the
// stream stays unread for the next line.
func readLineFrom(reader interface{ Read([]byte) (int, error) }) (string, error) {
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := reader.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				return strings.TrimRight(string(line), "\r"), nil
			}
			line = append(line, buf[0])
			if len(line) > 4096 {
				return "", errors.New("line too long")
			}
		}
		if err != nil {
			return string(line), err
		}
	}
}

func streamOneOf(flagName, value string, allowed ...string) error {
	if value == "" {
		return nil
	}
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf("invalid --%s %q (use %s)", flagName, value, strings.Join(allowed, ", "))
}

// ---------------------------------------------------------------------------
// Inputs
// ---------------------------------------------------------------------------

var streamInputProtocols = []string{"rtmp", "rtmps", "srt", "whip", "icecast"}

func streamInputsResource() *streamResource {
	return &streamResource{
		Name: "inputs", Aliases: []string{"input"}, Noun: "input", Path: "/inputs", IDPrefix: "sin_", NameField: "name", ResultKey: "input", HasGet: true,
		Columns: []streamColumn{
			streamCol("NAME", "name"), streamCol("KIND", "kind"),
			streamColFunc("ENABLED", func(i map[string]any) string { return streamOnOff(i, "enabled") }),
			streamColFunc("PROTOCOLS", func(i map[string]any) string { return streamListOf(i, "protocols") }),
			streamColFunc("KEY", func(i map[string]any) string {
				hint := streamString(i, "keyHints.primary")
				if backup := streamString(i, "keyHints.backup"); backup != "" {
					hint += " (+backup)"
				}
				if hint == "" {
					return "-"
				}
				return terminalSafe(hint)
			}),
			streamCol("ID", "id"),
		},
		Define:      defineStreamInput,
		CreateArg:   "<name>",
		CreateUsage: "hubfly stream inputs create <server> <name> [--protocols rtmp,rtmps,srt,whip,icecast] [--backup-key] [--pull <url>] [--max-bitrate <kbps>] [--allow <cidr>...] [--srt-passphrase-stdin] [--disabled] [--file <spec.json|->] [--json]",
		UpdateUsage: "hubfly stream inputs update <server> <input> [--name <name>] [--protocols <list>] [--enable|--disable] [--pull <url>] [--max-bitrate <kbps>] [--allow <cidr>...|--allow none] [--srt-passphrase-stdin] [--file <spec.json|->] [--json]",
		Create:      true, Update: true, Delete: true,
		DeleteNote: "Its publishers are disconnected and its keys stop working.",
		Once:       []string{"keys"},
		Extras:     map[string]func(*streamResource, []string) error{"keys": streamInputKeysFlow},
		Usage: `  hubfly stream inputs [list] <server> [--json]
  hubfly stream inputs get <server> <input> [--json]          (connection details per protocol, keys masked)
  hubfly stream inputs create <server> <name> [--protocols rtmp,rtmps,srt,whip,icecast] [--backup-key]
                              [--pull <url>] [--max-bitrate <kbps>] [--allow <cidr>...] [--srt-passphrase-stdin]
                              [--disabled] [--file <spec.json|->]   (the stream key prints once)
  hubfly stream inputs update <server> <input> [--name <name>] [--protocols <list>] [--enable|--disable]
                              [--pull <url>] [--max-bitrate <kbps>] [--allow <cidr>...|--allow none] [--srt-passphrase-stdin]
  hubfly stream inputs delete <server> <input> [--yes]
  hubfly stream inputs keys reveal <server> <input> [--json]
  hubfly stream inputs keys rotate <server> <input> [--role primary|backup] [--remove] [--yes] [--json]`,
	}
}

func defineStreamInput(fs *flag.FlagSet, mode string) streamSpec {
	name := fs.String("name", "", "")
	protocols := &rtList{split: true}
	fs.Var(protocols, "protocols", "")
	backupKey := fs.Bool("backup-key", false, "")
	pull := fs.String("pull", "", "")
	transport := fs.String("pull-transport", "", "")
	maxBitrate := fs.Int("max-bitrate", -1, "")
	allow := &rtList{split: true}
	fs.Var(allow, "allow", "")
	passphrase := fs.Bool("srt-passphrase-stdin", false, "")
	validateEnable, applyEnable := streamEnableFlags(fs, mode)
	return streamSpec{
		validate: func(mode string) error {
			if err := validateEnable(); err != nil {
				return err
			}
			for _, protocol := range protocols.values {
				if err := streamOneOf("protocols", strings.ToLower(protocol), streamInputProtocols...); err != nil {
					return err
				}
			}
			if *maxBitrate == 0 || *maxBitrate > 80000 {
				return fmt.Errorf("invalid --max-bitrate %d (kbps, 1-80000)", *maxBitrate)
			}
			if *pull != "" && !strings.Contains(*pull, "://") {
				return fmt.Errorf("invalid --pull %q (an rtmp, rtmps, rtsp, rtsps, srt or https URL)", redactStreamSecretText(*pull))
			}
			if mode == "update" && *backupKey {
				return errors.New("invalid flag --backup-key on update (add a backup key with: hubfly stream inputs keys rotate <server> <input> --role backup)")
			}
			return streamOneOf("pull-transport", *transport, "auto", "tcp", "udp")
		},
		build: func(b *streamBuild) error {
			body := b.body
			if b.mode == "create" {
				if _, ok := body["kind"]; !ok {
					body["kind"] = "push"
				}
				if *pull != "" {
					body["kind"] = "pull"
				}
				if *backupKey {
					body["backupKey"] = true
				}
			}
			if *name != "" {
				body["name"] = strings.TrimSpace(*name)
			}
			if len(protocols.values) > 0 {
				list := []string{}
				for _, protocol := range protocols.values {
					list = append(list, strings.ToLower(protocol))
				}
				body["protocols"] = list
			}
			if *pull != "" || *transport != "" {
				spec := map[string]any{}
				if existing, ok := body["pull"].(map[string]any); ok {
					spec = existing
				}
				if *pull != "" {
					spec["url"] = *pull
				}
				if *transport != "" {
					spec["transport"] = *transport
				}
				body["pull"] = spec
			}
			if *maxBitrate > 0 {
				body["maxBitrateKbps"] = *maxBitrate
			}
			if len(allow.values) > 0 {
				if len(allow.values) == 1 && strings.EqualFold(allow.values[0], "none") {
					body["allowedCidrs"] = []string{}
				} else {
					body["allowedCidrs"] = allow.values
				}
			}
			if *passphrase {
				secret, err := readStreamSecretLine("SRT passphrase (10-79 characters)")
				if err != nil {
					return err
				}
				if len(secret) < 10 || len(secret) > 79 {
					return errors.New("invalid SRT passphrase: use 10-79 characters")
				}
				body["srtPassphrase"] = secret
			}
			applyEnable(body)
			return nil
		},
	}
}

func streamInputKeysFlow(def *streamResource, args []string) error {
	const usage = "hubfly stream inputs keys reveal|rotate <server> <input> [--role primary|backup] [--remove] [--yes] [--json]"
	if len(args) == 0 {
		return dbdUsageError(usage)
	}
	switch args[0] {
	case "reveal", "show":
		return streamInputKeysRevealFlow(args[1:])
	case "rotate":
		return streamInputKeysRotateFlow(args[1:])
	}
	return fmt.Errorf("unknown stream inputs keys command %q\nusage: %s", args[0], usage)
}

func streamInputKeysRevealFlow(args []string) error {
	const usage = "hubfly stream inputs keys reveal <server> <input> [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream inputs keys reveal")
	asJSON := fs.Bool("json", false, "")
	target, positionals, err := streamParseTarget(fs, args, usage, 2, nil)
	if err != nil {
		return err
	}
	inputID, err := newStreamResolver(target).id(streamResourceByName("inputs"), positionals[1])
	if err != nil {
		return err
	}
	keys, err := revealStreamInputKeys(target.token, target.instance, inputID)
	if err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		// An explicit reveal prints the secrets unmasked.
		return rtPrintJSON(keys)
	}
	tw := newStreamTabWriter()
	_, _ = fmt.Fprintf(tw, "Primary key:\t%s\n", dbdText(&keys.Primary, "-"))
	if keys.Backup != "" {
		_, _ = fmt.Fprintf(tw, "Backup key:\t%s\n", keys.Backup)
	}
	if keys.SRTPassphrase != "" {
		_, _ = fmt.Fprintf(tw, "SRT passphrase:\t%s\n", keys.SRTPassphrase)
	}
	if keys.PullURL != "" {
		_, _ = fmt.Fprintf(tw, "Pull URL:\t%s\n", keys.PullURL)
	}
	return tw.Flush()
}

func streamInputKeysRotateFlow(args []string) error {
	const usage = "hubfly stream inputs keys rotate <server> <input> [--role primary|backup] [--remove] [--project <id|name>] [--yes] [--json]"
	fs := newRealtimeFlags("stream inputs keys rotate")
	role := fs.String("role", "primary", "")
	remove := fs.Bool("remove", false, "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	asJSON := fs.Bool("json", false, "")
	target, positionals, err := streamParseTarget(fs, args, usage, 2, func([]string) error {
		if err := streamOneOf("role", *role, "primary", "backup"); err != nil {
			return err
		}
		if *remove && *role != "backup" {
			return errors.New("invalid flags: --remove works only with --role backup")
		}
		return nil
	})
	if err != nil {
		return err
	}
	input, err := newStreamResolver(target).find(streamResourceByName("inputs"), positionals[1])
	if err != nil {
		return err
	}
	inputID := streamString(input, "id")
	action := fmt.Sprintf("Rotate the %s key of input '%s'? The publisher using the old key is disconnected.", *role, terminalSafe(streamString(input, "name")))
	if *remove {
		action = fmt.Sprintf("Remove the backup key of input '%s'? A publisher using it is disconnected.", terminalSafe(streamString(input, "name")))
	}
	confirmed, err := dbdConfirm(*yes, action, "rotate the key")
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled.")
		return nil
	}
	body := map[string]any{"role": *role}
	if *remove {
		body["remove"] = true
	}
	var result map[string]any
	if err := streamCall(target.token, target.instance, "POST", "/inputs/"+inputID+"/keys/rotate", nil, body, &result); err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return streamPrintOnceJSON(result, "key")
	}
	if *remove {
		_, _ = fmt.Fprintf(rtOut, "Removed the backup key of input '%s'.\n", terminalSafe(streamString(input, "name")))
	} else {
		_, _ = fmt.Fprintf(rtOut, "New %s key of input '%s' (shown only now):\n  %s\n", *role, terminalSafe(streamString(input, "name")), streamString(result, "key"))
	}
	writeStreamSync(result)
	return nil
}

// ---------------------------------------------------------------------------
// Channels
// ---------------------------------------------------------------------------

func streamChannelsResource() *streamResource {
	return &streamResource{
		Name: "channels", Aliases: []string{"channel", "ch"}, Noun: "channel", Path: "/channels", IDPrefix: "sch_", NameField: "name", ResultKey: "channel", HasGet: true,
		Columns: []streamColumn{
			streamCol("NAME", "name"), streamColFunc("ENABLED", func(i map[string]any) string { return streamOnOff(i, "enabled") }),
			streamColFunc("SOURCE", func(i map[string]any) string { return streamText(i["source"]) }),
			streamCol("LATENCY", "latency"), streamCol("ACCESS", "access.mode"), streamCol("DVR MIN", "dvrMinutes"),
			streamCol("ID", "id"),
		},
		Define:      defineStreamChannel,
		CreateArg:   "<name>",
		CreateUsage: "hubfly stream channels create <server> <name> --source input:<id|name>|mixer:<id|name>|playlist:<id|name> [--backup <source>] [--latency standard|low|ultra] [--access public|token] [--allowed-origins <a,b>|none] [--dvr <minutes>] [--max-viewers <n>] [--no-webrtc] [--no-ll-hls] [--disabled] [--file <spec.json|->] [--json]",
		UpdateUsage: "hubfly stream channels update <server> <channel> [--name <name>] [--source <source>] [--backup <source>|none] [--latency …] [--access public|token] [--allowed-origins <a,b>|none] [--dvr <minutes>] [--max-viewers <n>] [--webrtc[=false]] [--ll-hls[=false]] [--enable|--disable] [--file <spec.json|->] [--json]",
		Create:      true, Update: true, Delete: true,
		DeleteNote: "Its viewers are disconnected and its DVR is deleted.",
		Extras: map[string]func(*streamResource, []string) error{
			"playback": streamChannelPlaybackFlow,
			"token":    streamChannelTokenFlow,
		},
		Usage: `  hubfly stream channels [list] <server> [--json]
  hubfly stream channels get <server> <channel> [--json]
  hubfly stream channels create <server> <name> --source input:<id|name>|mixer:<id|name>|playlist:<id|name>
                                [--backup <source>] [--latency standard|low|ultra] [--access public|token]
                                [--allowed-origins <a,b>|none] [--dvr <minutes>] [--max-viewers <n>]
                                [--no-webrtc] [--no-ll-hls] [--disabled] [--file <spec.json|->]
  hubfly stream channels update <server> <channel> [same flags] [--name <name>] [--backup none] [--enable|--disable]
  hubfly stream channels delete <server> <channel> [--yes]
  hubfly stream channels playback <server> <channel> [--json]   (URLs and embed code)
  hubfly stream channels token <server> <channel> [--ttl 1h] [--sub <viewer>] [--ip <cidr>] [--json]`,
	}
}

// streamNegBool is the --no-<name> form of an rtOptBool flag.
type streamNegBool struct{ target *rtOptBool }

func (n streamNegBool) String() string   { return "" }
func (n streamNegBool) IsBoolFlag() bool { return true }
func (n streamNegBool) Set(value string) error {
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return errors.New("expected true or false")
	}
	n.target.set, n.target.value = true, !parsed
	return nil
}

func streamToggle(fs *flag.FlagSet, name string) *rtOptBool {
	value := &rtOptBool{}
	fs.Var(value, name, "")
	fs.Var(streamNegBool{target: value}, "no-"+name, "")
	return value
}

func defineStreamChannel(fs *flag.FlagSet, mode string) streamSpec {
	name := fs.String("name", "", "")
	source := fs.String("source", "", "")
	backup := fs.String("backup", "", "")
	latency := fs.String("latency", "", "")
	access := fs.String("access", "", "")
	origins := &rtOptString{}
	fs.Var(origins, "allowed-origins", "")
	dvr := fs.Int("dvr", -1, "")
	maxViewers := fs.Int("max-viewers", -1, "")
	hls := streamToggle(fs, "hls")
	llHLS := streamToggle(fs, "ll-hls")
	webrtc := streamToggle(fs, "webrtc")
	validateEnable, applyEnable := streamEnableFlags(fs, mode)
	sourceKinds := []string{"input", "mixer", "playlist"}
	return streamSpec{
		validate: func(mode string) error {
			if err := validateEnable(); err != nil {
				return err
			}
			if err := validStreamSource(*source, sourceKinds...); err != nil {
				return err
			}
			if *backup != "none" {
				if err := validStreamSource(*backup, sourceKinds...); err != nil {
					return err
				}
			}
			if *dvr > 360 {
				return fmt.Errorf("invalid --dvr %d (minutes, 0-360)", *dvr)
			}
			if err := streamOneOf("latency", *latency, "standard", "low", "ultra"); err != nil {
				return err
			}
			return streamOneOf("access", *access, "public", "token")
		},
		build: func(b *streamBuild) error {
			body := b.body
			if *name != "" {
				body["name"] = strings.TrimSpace(*name)
			}
			if *source != "" {
				ref, err := b.res.sourceRef(*source, sourceKinds...)
				if err != nil {
					return err
				}
				body["source"] = ref
			}
			if *backup == "none" {
				body["backup"] = nil
			} else if *backup != "" {
				ref, err := b.res.sourceRef(*backup, sourceKinds...)
				if err != nil {
					return err
				}
				body["backup"] = ref
			}
			if *latency != "" {
				body["latency"] = *latency
			}
			if *dvr >= 0 {
				body["dvrMinutes"] = *dvr
			}
			if *maxViewers >= 0 {
				body["maxViewers"] = *maxViewers
			}
			if *access != "" || origins.set {
				spec := streamNested(b, "access", map[string]any{"mode": "public", "allowedOrigins": []any{}, "allowedCidrs": []any{}})
				if *access != "" {
					spec["mode"] = *access
				}
				if origins.set {
					list := []string{}
					if !strings.EqualFold(strings.TrimSpace(origins.value), "none") {
						list = dbdSplitList(origins.value)
					}
					spec["allowedOrigins"] = list
				}
			}
			if hls.set || llHLS.set || webrtc.set {
				spec := streamNested(b, "playback", map[string]any{"hls": true, "llHls": true, "webrtc": true, "icecast": []any{}, "rtmpPull": false, "srtPull": false})
				delete(spec, "urls")
				for key, toggle := range map[string]*rtOptBool{"hls": hls, "llHls": llHLS, "webrtc": webrtc} {
					if toggle.set {
						spec[key] = toggle.value
					}
				}
			}
			if b.mode == "create" {
				if _, ok := body["source"]; !ok {
					return errors.New("invalid arguments: pass --source input:<id|name>, mixer:<id|name> or playlist:<id|name> (or --file)")
				}
			}
			applyEnable(body)
			return nil
		},
	}
}

func streamChannelPlaybackFlow(def *streamResource, args []string) error {
	const usage = "hubfly stream channels playback <server> <channel> [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream channels playback")
	asJSON := fs.Bool("json", false, "")
	target, positionals, err := streamParseTarget(fs, args, usage, 2, nil)
	if err != nil {
		return err
	}
	channelID, err := newStreamResolver(target).id(def, positionals[1])
	if err != nil {
		return err
	}
	playback, err := fetchStreamPlayback(target.token, target.instance, channelID)
	if err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return streamPrintJSON(playback)
	}
	tw := newStreamTabWriter()
	urls := playback.URLs
	for _, row := range [][2]string{
		{"HLS", urls.HLS}, {"WebRTC (WHEP)", urls.WHEP}, {"Embed", urls.Embed}, {"Thumbnail", urls.Thumb}, {"Status", urls.State},
		{"RTMP pull", urls.RTMPPull}, {"SRT pull", urls.SRTPull},
	} {
		if row[1] != "" {
			_, _ = fmt.Fprintf(tw, "%s:\t%s\n", row[0], terminalSafe(redactStreamSecretText(row[1])))
		}
	}
	for _, audio := range urls.Audio {
		_, _ = fmt.Fprintf(tw, "Audio (%s):\t%s\n", audio.Format, terminalSafe(audio.URL))
	}
	_ = tw.Flush()
	if playback.TokenRequired {
		_, _ = fmt.Fprintf(rtOut, "\nViewers need a playback token (?token=). Test one with: hubfly stream channels token %s %s\n", target.instance.Name, channelID)
	}
	if playback.EmbedHTML != "" {
		_, _ = fmt.Fprintf(rtOut, "\nEmbed:\n%s\n", terminalSafe(playback.EmbedHTML))
	}
	return nil
}

func parseStreamTTL(value string) (int, error) {
	seconds, err := parseRealtimeDuration(value)
	if err != nil || seconds < 60 || seconds > 86400 {
		return 0, fmt.Errorf("invalid --ttl %q (60s to 24h)", value)
	}
	return seconds, nil
}

func streamChannelTokenFlow(def *streamResource, args []string) error {
	const usage = "hubfly stream channels token <server> <channel> [--ttl 1h] [--sub <viewer>] [--ip <cidr>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream channels token")
	ttl := fs.String("ttl", "1h", "")
	sub := fs.String("sub", "", "")
	ip := fs.String("ip", "", "")
	asJSON := fs.Bool("json", false, "")
	seconds := 0
	target, positionals, err := streamParseTarget(fs, args, usage, 2, func([]string) error {
		var ttlErr error
		seconds, ttlErr = parseStreamTTL(*ttl)
		return ttlErr
	})
	if err != nil {
		return err
	}
	channelID, err := newStreamResolver(target).id(def, positionals[1])
	if err != nil {
		return err
	}
	body := map[string]any{"ttlSeconds": seconds}
	if *sub != "" {
		body["sub"] = *sub
	}
	if *ip != "" {
		body["ip"] = *ip
	}
	token, expiresAt, err := createStreamTestToken(target.token, target.instance, channelID, body)
	if err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		// A test token is the output the user asked for.
		return rtPrintJSON(map[string]any{"token": token, "expiresAt": expiresAt})
	}
	_, _ = fmt.Fprintf(rtOut, "%s\n", token)
	_, _ = fmt.Fprintf(rtErr, "Expires %s. Append ?token=<token> to playback URLs.\n", dbdTime(expiresAt))
	return nil
}

// ---------------------------------------------------------------------------
// Destinations
// ---------------------------------------------------------------------------

var streamDestinationPlatforms = []string{"youtube", "twitch", "facebook", "kick", "x", "linkedin", "custom"}

func streamDestinationsResource() *streamResource {
	return &streamResource{
		Name: "destinations", Aliases: []string{"destination", "dest"}, Noun: "destination", Path: "/destinations", IDPrefix: "sds_", NameField: "name", ResultKey: "destination", HasGet: true,
		Columns: []streamColumn{
			streamCol("NAME", "name"), streamCol("PLATFORM", "platform"),
			streamColFunc("ENABLED", func(i map[string]any) string { return streamOnOff(i, "enabled") }),
			streamColFunc("SOURCE", func(i map[string]any) string { return streamText(i["source"]) }),
			streamColFunc("AUTO START", func(i map[string]any) string { return streamOnOff(i, "autoStart") }),
			streamCol("KEY", "streamKeyHint"), streamCol("ID", "id"),
		},
		Define:      defineStreamDestination,
		CreateArg:   "<name>",
		CreateUsage: "hubfly stream destinations create <server> <name> --platform youtube|twitch|facebook|kick|x|linkedin|custom --source channel:<id|name>|input:<id|name> --stream-key-stdin [--url <url>] [--protocol rtmp|rtmps|srt] [--rendition <name>] [--auto-start[=false]] [--disabled] [--file <spec.json|->] [--json]",
		UpdateUsage: "hubfly stream destinations update <server> <destination> [--name <name>] [--source <source>] [--url <url>] [--protocol rtmp|rtmps|srt] [--rendition <name>] [--auto-start[=false]] [--stream-key-stdin] [--enable|--disable] [--file <spec.json|->] [--json]",
		Create:      true, Update: true, Delete: true,
		DeleteNote: "A running push stops.",
		Extras: map[string]func(*streamResource, []string) error{
			"start": func(def *streamResource, args []string) error { return streamDestinationPowerFlow(def, "start", args) },
			"stop":  func(def *streamResource, args []string) error { return streamDestinationPowerFlow(def, "stop", args) },
		},
		Usage: `  hubfly stream destinations [list] <server> [--json]
  hubfly stream destinations get <server> <destination> [--json]
  hubfly stream destinations create <server> <name> --platform youtube|twitch|facebook|kick|x|linkedin|custom
                                    --source channel:<id|name>|input:<id|name> --stream-key-stdin [--url <url>]
                                    [--protocol rtmp|rtmps|srt] [--rendition <name>] [--auto-start[=false]] [--disabled]
                                    (presets fill the URL; custom and linkedin need --url)
  hubfly stream destinations update <server> <destination> [--name …] [--source …] [--url …] [--stream-key-stdin] [--enable|--disable]
  hubfly stream destinations start|stop <server> <destination> [--json]
  hubfly stream destinations delete <server> <destination> [--yes]`,
	}
}

func defineStreamDestination(fs *flag.FlagSet, mode string) streamSpec {
	name := fs.String("name", "", "")
	platform := fs.String("platform", "", "")
	source := fs.String("source", "", "")
	rawURL := fs.String("url", "", "")
	protocol := fs.String("protocol", "", "")
	rendition := fs.String("rendition", "", "")
	autoStart := &rtOptBool{}
	fs.Var(autoStart, "auto-start", "")
	keyStdin := fs.Bool("stream-key-stdin", false, "")
	passphrase := fs.Bool("srt-passphrase-stdin", false, "")
	validateEnable, applyEnable := streamEnableFlags(fs, mode)
	return streamSpec{
		validate: func(mode string) error {
			if err := validateEnable(); err != nil {
				return err
			}
			*platform = strings.ToLower(strings.TrimSpace(*platform))
			if err := streamOneOf("platform", *platform, streamDestinationPlatforms...); err != nil {
				return err
			}
			if mode == "update" && *platform != "" {
				return errors.New("invalid flag --platform on update (create a new destination for another platform)")
			}
			if mode == "create" && *platform != "" {
				if !*keyStdin {
					return errors.New("invalid arguments: pass the platform's stream key on stdin with --stream-key-stdin")
				}
				if (*platform == "custom" || *platform == "linkedin") && *rawURL == "" {
					return fmt.Errorf("invalid arguments: --platform %s needs --url", *platform)
				}
			}
			if err := validStreamSource(*source, "channel", "input"); err != nil {
				return err
			}
			return streamOneOf("protocol", *protocol, "rtmp", "rtmps", "srt")
		},
		build: func(b *streamBuild) error {
			body := b.body
			for key, value := range map[string]string{"name": *name, "platform": *platform, "url": *rawURL, "protocol": *protocol, "rendition": *rendition} {
				if strings.TrimSpace(value) != "" {
					body[key] = strings.TrimSpace(value)
				}
			}
			if *source != "" {
				ref, err := b.res.sourceRef(*source, "channel", "input")
				if err != nil {
					return err
				}
				body["source"] = ref
			}
			if autoStart.set {
				body["autoStart"] = autoStart.value
			}
			if *keyStdin {
				key, err := readStreamSecretLine("Stream key from the platform")
				if err != nil {
					return err
				}
				body["streamKey"] = key
			}
			if *passphrase {
				secret, err := readStreamSecretLine("SRT passphrase")
				if err != nil {
					return err
				}
				body["srtPassphrase"] = secret
			}
			if b.mode == "create" {
				if _, ok := body["platform"]; !ok {
					return errors.New("invalid arguments: pass --platform youtube|twitch|facebook|kick|x|linkedin|custom")
				}
				if _, ok := body["source"]; !ok {
					return errors.New("invalid arguments: pass --source channel:<id|name> or input:<id|name>")
				}
			}
			applyEnable(body)
			return nil
		},
	}
}

func streamDestinationPowerFlow(def *streamResource, action string, args []string) error {
	usage := fmt.Sprintf("hubfly stream destinations %s <server> <destination> [--project <id|name>] [--json]", action)
	fs := newRealtimeFlags("stream destinations " + action)
	asJSON := fs.Bool("json", false, "")
	target, positionals, err := streamParseTarget(fs, args, usage, 2, nil)
	if err != nil {
		return err
	}
	id, err := newStreamResolver(target).id(def, positionals[1])
	if err != nil {
		return err
	}
	return runStreamControlOps(target, []map[string]any{{"op": "destination." + action, "destination": id}}, *asJSON)
}

// ---------------------------------------------------------------------------
// Mixers and playlists
// ---------------------------------------------------------------------------

func parseStreamTransition(value string) (map[string]any, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "cut" {
		return map[string]any{"type": "cut", "durationMs": 0}, nil
	}
	if text, ok := strings.CutPrefix(value, "fade"); ok {
		duration := 500 * time.Millisecond
		if text = strings.TrimPrefix(text, ":"); text != "" {
			parsed, err := time.ParseDuration(text)
			if err != nil {
				ms, msErr := strconv.Atoi(text)
				if msErr != nil {
					return nil, fmt.Errorf("invalid transition %q (use cut or fade:<duration> such as fade:500ms)", value)
				}
				parsed = time.Duration(ms) * time.Millisecond
			}
			duration = parsed
		}
		if duration < 0 || duration > 10*time.Second {
			return nil, fmt.Errorf("invalid transition %q (fades last 0-10s)", value)
		}
		return map[string]any{"type": "fade", "durationMs": duration.Milliseconds()}, nil
	}
	return nil, fmt.Errorf("invalid transition %q (use cut or fade:<duration> such as fade:500ms)", value)
}

func defineStreamMixer(fs *flag.FlagSet, mode string) streamSpec {
	name := fs.String("name", "", "")
	preset := fs.String("preset", "", "")
	defaultScene := fs.String("default-scene", "", "")
	transition := fs.String("transition", "", "")
	validateEnable, applyEnable := streamEnableFlags(fs, mode)
	return streamSpec{
		validate: func(mode string) error {
			if err := validateEnable(); err != nil {
				return err
			}
			if mode == "update" && *preset != "" {
				return errors.New("invalid flag --preset on update (send a canvas with --file)")
			}
			if *transition != "" {
				if _, err := parseStreamTransition(*transition); err != nil {
					return err
				}
			}
			return streamOneOf("preset", *preset, "vertical", "1080p30", "1080p60", "720p30")
		},
		build: func(b *streamBuild) error {
			if *name != "" {
				b.body["name"] = strings.TrimSpace(*name)
			}
			if *preset != "" {
				b.body["preset"] = *preset
			}
			if *transition != "" {
				b.body["transition"], _ = parseStreamTransition(*transition)
			}
			if *defaultScene != "" {
				scene, err := pickStreamScene(b.current, *defaultScene)
				if err != nil {
					return err
				}
				b.body["defaultScene"] = streamString(scene, "id")
			}
			applyEnable(b.body)
			return nil
		},
	}
}

// pickStreamScene finds a mixer scene by id or name.
func pickStreamScene(mixer map[string]any, ref string) (map[string]any, error) {
	scenes, _ := mixer["scenes"].([]any)
	names := []string{}
	var byName map[string]any
	for _, raw := range scenes {
		scene, _ := raw.(map[string]any)
		if streamString(scene, "id") == ref {
			return scene, nil
		}
		if strings.EqualFold(streamString(scene, "name"), ref) && byName == nil {
			byName = scene
		}
		names = append(names, fmt.Sprintf("%s (%s)", terminalSafe(streamString(scene, "name")), streamString(scene, "id")))
	}
	if byName != nil {
		return byName, nil
	}
	available := "none"
	if len(names) > 0 {
		available = strings.Join(names, ", ")
	}
	return nil, fmt.Errorf("scene '%s' not found in mixer '%s' (scenes: %s)", terminalSafe(ref), terminalSafe(streamString(mixer, "name")), available)
}

// parseStreamPlaylistItem accepts a URL, asset:<link>:<asset id> or
// object:<link>:<object key>.
func parseStreamPlaylistItem(value string) (map[string]any, string, error) {
	value = strings.TrimSpace(value)
	for _, kind := range []string{"asset", "object"} {
		if rest, ok := strings.CutPrefix(value, kind+":"); ok {
			link, ref, found := strings.Cut(rest, ":")
			if !found || link == "" || ref == "" {
				return nil, "", fmt.Errorf("invalid --item %q (use %s:<storage link>:<%s>)", value, kind, map[string]string{"asset": "asset id", "object": "object key"}[kind])
			}
			key := "assetId"
			if kind == "object" {
				key = "objectKey"
			}
			return map[string]any{key: ref}, link, nil
		}
	}
	if !strings.HasPrefix(value, "https://") && !strings.HasPrefix(value, "http://") {
		return nil, "", fmt.Errorf("invalid --item %q (an http(s) URL, asset:<link>:<asset id> or object:<link>:<key>)", value)
	}
	item := map[string]any{"url": value}
	if strings.Contains(strings.ToLower(value), ".m3u8") {
		item["media"] = "hls"
	}
	return item, "", nil
}

func defineStreamPlaylist(fs *flag.FlagSet, mode string) streamSpec {
	name := fs.String("name", "", "")
	items := &rtList{}
	fs.Var(items, "item", "")
	loop := &rtOptBool{}
	fs.Var(loop, "loop", "")
	shuffle := &rtOptBool{}
	fs.Var(shuffle, "shuffle", "")
	encoding := fs.String("encoding", "", "")
	return streamSpec{
		validate: func(string) error {
			for _, item := range items.values {
				if _, _, err := parseStreamPlaylistItem(item); err != nil {
					return err
				}
			}
			return streamOneOf("encoding", *encoding, "auto", "reencode")
		},
		build: func(b *streamBuild) error {
			if *name != "" {
				b.body["name"] = strings.TrimSpace(*name)
			}
			if loop.set {
				b.body["loop"] = loop.value
			}
			if shuffle.set {
				b.body["shuffle"] = shuffle.value
			}
			if *encoding != "" {
				b.body["encoding"] = *encoding
			}
			if len(items.values) > 0 {
				list := []any{}
				for _, raw := range items.values {
					item, link, _ := parseStreamPlaylistItem(raw)
					if link != "" {
						id, err := b.res.id(streamResourceByName("storage-links"), link)
						if err != nil {
							return err
						}
						item["storageLinkId"] = id
					}
					list = append(list, item)
				}
				b.body["items"] = list
			}
			if b.mode == "create" {
				if _, ok := b.body["items"]; !ok {
					return errors.New("invalid arguments: pass at least one --item (or --file)")
				}
			}
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// Schedules
// ---------------------------------------------------------------------------

func streamSchedulesResource() *streamResource {
	return &streamResource{
		Name: "schedules", Aliases: []string{"schedule"}, Noun: "schedule", Path: "/schedules", IDPrefix: "ssc_", NameField: "name", ResultKey: "schedule", HasGet: true,
		Columns: []streamColumn{
			streamCol("NAME", "name"), streamColFunc("ENABLED", func(i map[string]any) string { return streamOnOff(i, "enabled") }),
			streamCol("START", "start"), streamCol("RRULE", "rrule"), streamCol("MINUTES", "durationMinutes"),
			streamCol("TIME ZONE", "timezone"), streamCol("ID", "id"),
		},
		Define:      defineStreamSchedule,
		CreateArg:   "<name>",
		CreateUsage: "hubfly stream schedules create <server> <name> --start <local time> --duration <minutes> [--rrule <rule>] [--timezone <tz>] [--on-start '<op json>']... [--on-end '<op json>']... [--disabled] [--file <spec.json|->] [--json]",
		UpdateUsage: "hubfly stream schedules update <server> <schedule> [--name <name>] [--start <time>] [--duration <minutes>] [--rrule <rule>|none] [--timezone <tz>] [--on-start '<op json>']... [--on-end '<op json>']... [--enable|--disable] [--file <spec.json|->] [--json]",
		Create:      true, Update: true, Delete: true,
		Extras: map[string]func(*streamResource, []string) error{
			"preview": streamSchedulePreviewFlow,
			"runs":    streamScheduleRunsFlow,
		},
		Usage: `  hubfly stream schedules [list] <server> [--json]
  hubfly stream schedules get <server> <schedule> [--json]          (with the next starts)
  hubfly stream schedules create <server> <name> --start <local time> --duration <minutes> [--rrule <rule>]
                                 [--timezone <tz>] [--on-start '<op json>']... [--on-end '<op json>']... [--disabled]
                                 (ops are control ops, e.g. '{"op":"channel.start","channel":"sch_…"}')
  hubfly stream schedules update <server> <schedule> [same flags] [--rrule none] [--enable|--disable]
  hubfly stream schedules preview <server> --start <time> --duration <minutes> [--rrule <rule>] [--timezone <tz>]
  hubfly stream schedules runs <server> <schedule> [--limit <n>] [--before <cursor>] [--json]
  hubfly stream schedules delete <server> <schedule> [--yes]`,
	}
}

// parseStreamOps parses --op/--on-start values: a JSON object or array.
func parseStreamOps(values []string, flagName string) ([]map[string]any, error) {
	ops := []map[string]any{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "[") {
			var list []map[string]any
			if err := json.Unmarshal([]byte(value), &list); err != nil {
				return nil, fmt.Errorf("invalid --%s %q: %v", flagName, value, err)
			}
			ops = append(ops, list...)
			continue
		}
		var op map[string]any
		if err := json.Unmarshal([]byte(value), &op); err != nil {
			return nil, fmt.Errorf("invalid --%s %q: %v", flagName, value, err)
		}
		ops = append(ops, op)
	}
	for _, op := range ops {
		if name, _ := op["op"].(string); name == "" {
			return nil, fmt.Errorf("invalid --%s: every op needs an \"op\" field (such as channel.start)", flagName)
		}
	}
	return ops, nil
}

var streamLocalMinutePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}$`)

// normalizeStreamLocalTime turns "2026-11-02T18:00" (or with a space) into
// the "2026-11-02T18:00:00" local date-time schedules use.
func normalizeStreamLocalTime(value string) string {
	value = strings.TrimSpace(value)
	if streamLocalMinutePattern.MatchString(value) {
		value += ":00"
	}
	if len(value) > 10 && value[10] == ' ' {
		value = value[:10] + "T" + value[11:]
	}
	return value
}

func defineStreamSchedule(fs *flag.FlagSet, mode string) streamSpec {
	name := fs.String("name", "", "")
	start := fs.String("start", "", "")
	duration := fs.Int("duration", 0, "")
	rrule := fs.String("rrule", "", "")
	timezone := fs.String("timezone", "", "")
	onStart := &rtList{}
	fs.Var(onStart, "on-start", "")
	onEnd := &rtList{}
	fs.Var(onEnd, "on-end", "")
	validateEnable, applyEnable := streamEnableFlags(fs, mode)
	return streamSpec{
		validate: func(mode string) error {
			if err := validateEnable(); err != nil {
				return err
			}
			if *duration < 0 || *duration > 10080 {
				return fmt.Errorf("invalid --duration %d (minutes, 1-10080)", *duration)
			}
			if _, err := parseStreamOps(onStart.values, "on-start"); err != nil {
				return err
			}
			_, err := parseStreamOps(onEnd.values, "on-end")
			return err
		},
		build: func(b *streamBuild) error {
			body := b.body
			for key, value := range map[string]string{"name": *name, "start": normalizeStreamLocalTime(*start), "timezone": *timezone} {
				if strings.TrimSpace(value) != "" {
					body[key] = strings.TrimSpace(value)
				}
			}
			if *rrule == "none" {
				body["rrule"] = ""
			} else if *rrule != "" {
				body["rrule"] = strings.TrimSpace(*rrule)
			}
			if *duration > 0 {
				body["durationMinutes"] = *duration
			}
			if len(onStart.values) > 0 {
				body["onStart"], _ = parseStreamOps(onStart.values, "on-start")
			}
			if len(onEnd.values) > 0 {
				body["onEnd"], _ = parseStreamOps(onEnd.values, "on-end")
			}
			if b.mode == "create" {
				if _, ok := body["timezone"]; !ok {
					server, err := fetchStreamServer(b.target.token, b.target.instance)
					if err != nil {
						return streamAPIError(err)
					}
					body["timezone"] = server.Timezone
				}
				if _, ok := body["start"]; !ok {
					return errors.New("invalid arguments: pass --start (local time such as 2026-11-02T18:00)")
				}
				if _, ok := body["durationMinutes"]; !ok {
					return errors.New("invalid arguments: pass --duration <minutes>")
				}
			}
			applyEnable(body)
			return nil
		},
	}
}

func streamSchedulePreviewFlow(def *streamResource, args []string) error {
	const usage = "hubfly stream schedules preview <server> --start <time> --duration <minutes> [--rrule <rule>] [--timezone <tz>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream schedules preview")
	start := fs.String("start", "", "")
	duration := fs.Int("duration", 0, "")
	rrule := fs.String("rrule", "", "")
	timezone := fs.String("timezone", "", "")
	asJSON := fs.Bool("json", false, "")
	target, _, err := streamParseTarget(fs, args, usage, 1, func([]string) error {
		if strings.TrimSpace(*start) == "" || *duration <= 0 {
			return errors.New("invalid arguments: pass --start and --duration")
		}
		return nil
	})
	if err != nil {
		return err
	}
	tz := strings.TrimSpace(*timezone)
	if tz == "" {
		server, serverErr := fetchStreamServer(target.token, target.instance)
		if serverErr != nil {
			return streamAPIError(serverErr)
		}
		tz = server.Timezone
	}
	body := map[string]any{"timezone": tz, "start": normalizeStreamLocalTime(*start), "durationMinutes": *duration}
	if *rrule != "" {
		body["rrule"] = *rrule
	}
	var result struct {
		Starts []string         `json:"starts"`
		Errors []map[string]any `json:"errors"`
	}
	if err := streamCall(target.token, target.instance, "POST", "/schedules/preview", nil, body, &result); err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(result)
	}
	for _, problem := range result.Errors {
		_, _ = fmt.Fprintf(rtErr, "Error: %s: %s\n", streamString(problem, "path"), terminalSafe(streamString(problem, "message")))
	}
	if len(result.Starts) == 0 {
		_, _ = fmt.Fprintln(rtOut, "No upcoming starts.")
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "Next starts (%s, shown in UTC):\n", tz)
	for _, start := range result.Starts {
		_, _ = fmt.Fprintf(rtOut, "  %s\n", dbdTime(start))
	}
	return nil
}

func streamScheduleRunsFlow(def *streamResource, args []string) error {
	const usage = "hubfly stream schedules runs <server> <schedule> [--limit <n>] [--before <cursor>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream schedules runs")
	limit := fs.Int("limit", 20, "")
	before := fs.String("before", "", "")
	asJSON := fs.Bool("json", false, "")
	target, positionals, err := streamParseTarget(fs, args, usage, 2, func([]string) error {
		if *limit < 1 || *limit > 200 {
			return fmt.Errorf("invalid --limit %d (use 1-200)", *limit)
		}
		return nil
	})
	if err != nil {
		return err
	}
	id, err := newStreamResolver(target).id(def, positionals[1])
	if err != nil {
		return err
	}
	query := map[string][]string{"limit": {strconv.Itoa(*limit)}}
	if *before != "" {
		query["before"] = []string{*before}
	}
	var result struct {
		Runs       []map[string]any `json:"runs"`
		NextCursor *string          `json:"nextCursor"`
	}
	if err := streamCall(target.token, target.instance, "GET", "/schedules/"+id+"/runs", query, nil, &result); err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(result)
	}
	if len(result.Runs) == 0 {
		_, _ = fmt.Fprintln(rtOut, "No runs yet.")
		return nil
	}
	tw := newStreamTabWriter()
	_, _ = fmt.Fprintln(tw, "FIRED\tPHASE\tSTATUS\tERROR")
	for _, run := range result.Runs {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", dbdTime(streamString(run, "firedAt")), streamString(run, "phase"), streamString(run, "status"), streamText(run["error"]))
	}
	_ = tw.Flush()
	if result.NextCursor != nil && *result.NextCursor != "" {
		_, _ = fmt.Fprintf(rtErr, "# more: hubfly stream schedules runs %s %s --before %s\n", target.instance.Name, id, *result.NextCursor)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Recordings and clips
// ---------------------------------------------------------------------------

var streamRecordingColumns = []streamColumn{
	streamCol("NAME", "name"), streamCol("KIND", "kind"), streamCol("CHANNEL", "channelId"), streamCol("STATUS", "status"),
	streamColFunc("SIZE", func(i map[string]any) string {
		if value, ok := i["bytes"].(float64); ok {
			return formatBytes(int64(value))
		}
		return "-"
	}),
	streamColFunc("STARTED", func(i map[string]any) string { return dbdTime(streamString(i, "startedAt")) }),
	streamColFunc("STORAGE", func(i map[string]any) string {
		if bucket := streamString(i, "bucket"); bucket != "" {
			return terminalSafe(bucket + "/" + streamString(i, "objectKey"))
		}
		return "-"
	}),
	streamCol("ID", "id"),
}

func streamRecordingsResource() *streamResource {
	return &streamResource{
		Name: "recordings", Aliases: []string{"recording", "rec"}, Noun: "recording", Path: "/recordings", NameField: "name",
		Columns: streamRecordingColumns,
		Delete:  true,
		Extras: map[string]func(*streamResource, []string) error{
			"list": func(def *streamResource, args []string) error { return streamRecordingsListFlow(def, "", args) },
			"ls":   func(def *streamResource, args []string) error { return streamRecordingsListFlow(def, "", args) },
		},
		Usage: `  hubfly stream recordings [list] <server> [--kind recording|clip] [--channel <channel>] [--limit <n>] [--cursor <c>] [--json]
  hubfly stream recordings delete <server> <recording> [--delete-object] [--yes]`,
	}
}

func streamClipsResource() *streamResource {
	return &streamResource{
		Name: "clips", Aliases: []string{"clip"}, Noun: "clip", Path: "/recordings", NameField: "name",
		Columns: streamRecordingColumns,
		Extras: map[string]func(*streamResource, []string) error{
			"list":   func(def *streamResource, args []string) error { return streamRecordingsListFlow(def, "clip", args) },
			"ls":     func(def *streamResource, args []string) error { return streamRecordingsListFlow(def, "clip", args) },
			"create": streamClipCreateFlow,
			"delete": func(def *streamResource, args []string) error {
				return streamResourceDelete(streamResourceByName("recordings"), args)
			},
		},
		Usage: `  hubfly stream clips [list] <server> [--channel <channel>] [--json]
  hubfly stream clips create <server> --channel <channel> --from <time|-90s> --to <time|-0s> --name <name>
                             [--storage-link <link>] [--json]   (cut from the channel's DVR)
  hubfly stream clips delete <server> <clip> [--delete-object] [--yes]`,
	}
}

func streamRecordingsListFlow(def *streamResource, kind string, args []string) error {
	usage := "hubfly stream recordings [list] <server> [--kind recording|clip] [--channel <channel>] [--limit <n>] [--cursor <cursor>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream recordings list")
	kindFlag := fs.String("kind", kind, "")
	channel := fs.String("channel", "", "")
	limit := fs.Int("limit", 50, "")
	cursor := fs.String("cursor", "", "")
	asJSON := fs.Bool("json", false, "")
	target, _, err := streamParseTarget(fs, args, usage, 1, func([]string) error {
		if *limit < 1 || *limit > 200 {
			return fmt.Errorf("invalid --limit %d (use 1-200)", *limit)
		}
		return streamOneOf("kind", *kindFlag, "recording", "clip")
	})
	if err != nil {
		return err
	}
	query := map[string][]string{"limit": {strconv.Itoa(*limit)}}
	if *kindFlag != "" {
		query["kind"] = []string{*kindFlag}
	}
	if *cursor != "" {
		query["cursor"] = []string{*cursor}
	}
	if *channel != "" {
		id, err := newStreamResolver(target).id(streamResourceByName("channels"), *channel)
		if err != nil {
			return err
		}
		query["channelId"] = []string{id}
	}
	var result struct {
		Items      []map[string]any `json:"items"`
		NextCursor *string          `json:"nextCursor"`
	}
	if err := streamCall(target.token, target.instance, "GET", "/recordings", query, nil, &result); err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return streamPrintJSON(result)
	}
	if err := writeStreamTable(def, target, result.Items); err != nil {
		return err
	}
	if result.NextCursor != nil && *result.NextCursor != "" {
		_, _ = fmt.Fprintf(rtErr, "# more: hubfly stream %s list %s --cursor %s\n", def.Name, target.instance.Name, *result.NextCursor)
	}
	return nil
}

func streamClipCreateFlow(def *streamResource, args []string) error {
	const usage = "hubfly stream clips create <server> --channel <channel> --from <time|-90s> --to <time|-0s> --name <name> [--storage-link <link>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream clips create")
	channel := fs.String("channel", "", "")
	from := fs.String("from", "", "")
	to := fs.String("to", "", "")
	name := fs.String("name", "", "")
	link := fs.String("storage-link", "", "")
	asJSON := fs.Bool("json", false, "")
	target, _, err := streamParseTarget(fs, args, usage, 1, func([]string) error {
		if *channel == "" || *from == "" || *to == "" || strings.TrimSpace(*name) == "" {
			return errors.New("invalid arguments: pass --channel, --from, --to and --name")
		}
		return nil
	})
	if err != nil {
		return err
	}
	res := newStreamResolver(target)
	channelID, err := res.id(streamResourceByName("channels"), *channel)
	if err != nil {
		return err
	}
	body := map[string]any{"channelId": channelID, "from": *from, "to": *to, "name": strings.TrimSpace(*name)}
	if *link != "" {
		linkID, linkErr := res.id(streamResourceByName("storage-links"), *link)
		if linkErr != nil {
			return linkErr
		}
		body["storageLinkId"] = linkID
	}
	var result struct {
		ClipID string `json:"clipId"`
	}
	if err := streamCall(target.token, target.instance, "POST", "/clips/create", nil, body, &result); err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(result)
	}
	_, _ = fmt.Fprintf(rtOut, "Cutting clip '%s' (%s). Follow it with: hubfly stream clips list %s\n", terminalSafe(*name), result.ClipID, target.instance.Name)
	return nil
}

// ---------------------------------------------------------------------------
// Playback keys
// ---------------------------------------------------------------------------

func streamPlaybackKeyRotateFlow(def *streamResource, args []string) error {
	const usage = "hubfly stream playback-keys rotate <server> <key> [--project <id|name>] [--yes] [--json]"
	fs := newRealtimeFlags("stream playback-keys rotate")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	asJSON := fs.Bool("json", false, "")
	target, positionals, err := streamParseTarget(fs, args, usage, 2, nil)
	if err != nil {
		return err
	}
	key, err := newStreamResolver(target).find(def, positionals[1])
	if err != nil {
		return err
	}
	confirmed, err := dbdConfirm(*yes, fmt.Sprintf("Rotate playback key '%s'? Tokens signed with it keep working for 24 hours.", terminalSafe(streamString(key, "name"))), "rotate the playback key")
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled.")
		return nil
	}
	var result map[string]any
	if err := streamCall(target.token, target.instance, "POST", "/playback-keys/"+streamString(key, "id")+"/rotate", nil, map[string]any{}, &result); err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return streamPrintOnceJSON(result, "secret")
	}
	newKey, _ := result["key"].(map[string]any)
	_, _ = fmt.Fprintf(rtOut, "New playback key '%s' (%s); the old one retires in 24 hours.\n", terminalSafe(streamString(newKey, "name")), streamString(newKey, "id"))
	writeStreamOnce(def, target, result)
	writeStreamSync(result)
	return nil
}

// ---------------------------------------------------------------------------
// Webhooks
// ---------------------------------------------------------------------------

func streamWebhooksResource() *streamResource {
	return &streamResource{
		Name: "webhooks", Aliases: []string{"webhook", "hooks"}, Noun: "webhook", Path: "/webhooks", IDPrefix: "swh_", NameField: "url", ResultKey: "webhook",
		Columns: []streamColumn{
			streamCol("URL", "url"), streamColFunc("EVENTS", func(i map[string]any) string {
				if text := streamListOf(i, "events"); text != "-" {
					return text
				}
				return "all"
			}),
			streamColFunc("ENABLED", func(i map[string]any) string { return streamOnOff(i, "enabled") }),
			streamCol("FINGERPRINT", "fingerprint"), streamCol("ID", "id"),
		},
		Define:      defineStreamWebhook,
		CreateUsage: "hubfly stream webhooks create <server> --url <https-url> [--events channel.live,input.connected] [--disabled] [--json]",
		UpdateUsage: "hubfly stream webhooks update <server> <webhook> [--url <url>] [--events <a,b>|all] [--enable|--disable] [--json]",
		Create:      true, Update: true, Delete: true,
		Once: []string{"secret"},
		Extras: map[string]func(*streamResource, []string) error{
			"test":       streamWebhookTestFlow,
			"deliveries": streamWebhookDeliveriesFlow,
		},
		Usage: `  hubfly stream webhooks [list] <server> [--json]
  hubfly stream webhooks create <server> --url <https-url> [--events channel.live,input.connected] [--disabled]
                                (the whsec_ secret prints once)
  hubfly stream webhooks update <server> <webhook> [--url <url>] [--events <a,b>|all] [--enable|--disable]
  hubfly stream webhooks test <server> <webhook> [--json]
  hubfly stream webhooks deliveries <server> <webhook> [--limit <n>] [--after <cursor>] [--json]
  hubfly stream webhooks delete <server> <webhook> [--yes]`,
	}
}

func defineStreamWebhook(fs *flag.FlagSet, mode string) streamSpec {
	rawURL := fs.String("url", "", "")
	events := &rtList{split: true}
	fs.Var(events, "events", "")
	validateEnable, applyEnable := streamEnableFlags(fs, mode)
	return streamSpec{
		validate: func(mode string) error {
			if err := validateEnable(); err != nil {
				return err
			}
			if *rawURL != "" && !strings.HasPrefix(*rawURL, "https://") {
				return fmt.Errorf("invalid --url %q (webhooks need an https URL)", redactStreamSecretText(*rawURL))
			}
			return nil
		},
		build: func(b *streamBuild) error {
			if *rawURL != "" {
				b.body["url"] = *rawURL
			}
			if len(events.values) > 0 {
				if len(events.values) == 1 && events.values[0] == "all" {
					b.body["events"] = []string{}
				} else {
					b.body["events"] = events.values
				}
			}
			if b.mode == "create" {
				if _, ok := b.body["url"]; !ok {
					return errors.New("invalid arguments: pass --url <https-url>")
				}
			}
			applyEnable(b.body)
			return nil
		},
	}
}

func streamWebhookTestFlow(def *streamResource, args []string) error {
	const usage = "hubfly stream webhooks test <server> <webhook> [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream webhooks test")
	asJSON := fs.Bool("json", false, "")
	target, positionals, err := streamParseTarget(fs, args, usage, 2, nil)
	if err != nil {
		return err
	}
	id, err := newStreamResolver(target).id(def, positionals[1])
	if err != nil {
		return err
	}
	var result struct {
		OK         bool    `json:"ok"`
		StatusCode int     `json:"statusCode"`
		DurationMs float64 `json:"durationMs"`
		Error      string  `json:"error"`
	}
	if err := streamCall(target.token, target.instance, "POST", "/webhooks/"+id+"/test", nil, map[string]any{}, &result); err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(result)
	}
	if !result.OK {
		detail := result.Error
		if detail == "" && result.StatusCode > 0 {
			detail = fmt.Sprintf("HTTP %d", result.StatusCode)
		}
		return fmt.Errorf("the test delivery failed: %s", terminalSafe(redactStreamSecretText(detail)))
	}
	_, _ = fmt.Fprintf(rtOut, "Delivered: HTTP %d in %.0f ms.\n", result.StatusCode, result.DurationMs)
	return nil
}

func streamWebhookDeliveriesFlow(def *streamResource, args []string) error {
	const usage = "hubfly stream webhooks deliveries <server> <webhook> [--limit <n>] [--after <cursor>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream webhooks deliveries")
	limit := fs.Int("limit", 20, "")
	after := fs.String("after", "", "")
	asJSON := fs.Bool("json", false, "")
	target, positionals, err := streamParseTarget(fs, args, usage, 2, func([]string) error {
		if *limit < 1 || *limit > 200 {
			return fmt.Errorf("invalid --limit %d (use 1-200)", *limit)
		}
		return nil
	})
	if err != nil {
		return err
	}
	id, err := newStreamResolver(target).id(def, positionals[1])
	if err != nil {
		return err
	}
	query := map[string][]string{"limit": {strconv.Itoa(*limit)}}
	if *after != "" {
		query["after"] = []string{*after}
	}
	var result struct {
		Deliveries []map[string]any `json:"deliveries"`
		NextCursor *string          `json:"nextCursor"`
	}
	if err := streamCall(target.token, target.instance, "GET", "/webhooks/"+id+"/deliveries", query, nil, &result); err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return streamPrintJSON(result)
	}
	if len(result.Deliveries) == 0 {
		_, _ = fmt.Fprintln(rtOut, "No deliveries yet.")
		return nil
	}
	tw := newStreamTabWriter()
	_, _ = fmt.Fprintln(tw, "AT\tTYPE\tSTATUS\tATTEMPT\tHTTP\tERROR")
	for _, delivery := range result.Deliveries {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", dbdTime(streamString(delivery, "at")), streamText(delivery["type"]), streamText(delivery["status"]),
			streamText(delivery["attempt"]), streamText(delivery["httpStatus"]), streamText(redactStreamSecrets(delivery["error"])))
	}
	_ = tw.Flush()
	if result.NextCursor != nil && *result.NextCursor != "" {
		_, _ = fmt.Fprintf(rtErr, "# more: hubfly stream webhooks deliveries %s %s --after %s\n", target.instance.Name, id, *result.NextCursor)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Storage links, domains and bindings
// ---------------------------------------------------------------------------

func defineStreamStorageLink(fs *flag.FlagSet, mode string) streamSpec {
	project := fs.String("storage-project", "", "")
	bucket := fs.String("bucket", "", "")
	linkMode := fs.String("mode", "read_write", "")
	return streamSpec{
		validate: func(string) error {
			return streamOneOf("mode", *linkMode, "read", "write", "read_write")
		},
		build: func(b *streamBuild) error {
			if *project != "" {
				projects, err := fetchManagedProjects(b.target.token, *project)
				if err != nil {
					return err
				}
				b.body["storageProjectId"] = projects[0].ID
			}
			if *bucket != "" {
				b.body["bucket"] = strings.TrimSpace(*bucket)
			}
			if _, ok := b.body["mode"]; !ok {
				b.body["mode"] = *linkMode
			}
			if b.body["storageProjectId"] == nil || b.body["bucket"] == nil {
				return errors.New("invalid arguments: pass --storage-project <id|name> and --bucket <bucket>")
			}
			return nil
		},
	}
}

func streamStorageAssetsFlow(def *streamResource, args []string) error {
	const usage = "hubfly stream storage-links assets <server> <link> [--search <text>] [--hls-ready] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream storage-links assets")
	search := fs.String("search", "", "")
	hlsReady := fs.Bool("hls-ready", false, "")
	asJSON := fs.Bool("json", false, "")
	target, positionals, err := streamParseTarget(fs, args, usage, 2, nil)
	if err != nil {
		return err
	}
	id, err := newStreamResolver(target).id(def, positionals[1])
	if err != nil {
		return err
	}
	query := map[string][]string{}
	if *search != "" {
		query["search"] = []string{*search}
	}
	if *hlsReady {
		query["hlsReady"] = []string{"true"}
	}
	var result struct {
		Assets []map[string]any `json:"assets"`
	}
	raw, err := streamGetRaw(target.token, target.instance, "/storage-links/"+id+"/assets", query)
	if err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return streamPrintJSON(raw)
	}
	if err := streamDecode(raw, &result); err != nil {
		return fmt.Errorf("read the assets: %w", err)
	}
	if len(result.Assets) == 0 {
		_, _ = fmt.Fprintln(rtOut, "No assets in this bucket.")
		return nil
	}
	tw := newStreamTabWriter()
	_, _ = fmt.Fprintln(tw, "TITLE\tDURATION\tHLS\tOBJECT\tASSET ID")
	for _, asset := range result.Assets {
		hls := "no"
		if asset["hlsReady"] == true {
			hls = "ready"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%ss\t%s\t%s\t%s\n", terminalSafe(streamString(asset, "title")), streamString(asset, "durationSeconds"), hls, terminalSafe(streamString(asset, "objectKey")), streamString(asset, "assetId"))
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintf(rtErr, "Play one in a playlist with: --item asset:%s:<asset id>\n", id)
	return nil
}

func defineStreamDomain(fs *flag.FlagSet, mode string) streamSpec {
	purpose := fs.String("purpose", "", "")
	return streamSpec{
		validate: func(string) error { return streamOneOf("purpose", *purpose, "both", "playback", "ingest") },
		build: func(b *streamBuild) error {
			if *purpose != "" {
				b.body["purpose"] = *purpose
			}
			return nil
		},
	}
}

func streamDomainVerifyFlow(def *streamResource, args []string) error {
	const usage = "hubfly stream domains verify <server> <domain> [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream domains verify")
	asJSON := fs.Bool("json", false, "")
	target, positionals, err := streamParseTarget(fs, args, usage, 2, nil)
	if err != nil {
		return err
	}
	domain, err := newStreamResolver(target).find(def, positionals[1])
	if err != nil {
		return err
	}
	var result map[string]any
	if err := streamCall(target.token, target.instance, "POST", "/domains/"+streamString(domain, "id")+"/verify", nil, map[string]any{}, &result); err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return streamPrintJSON(result)
	}
	hostname := terminalSafe(streamString(domain, "hostname"))
	if result["verified"] != true {
		detail := streamString(result, "error")
		if detail == "" {
			detail = "the CNAME record is not visible yet"
		}
		return fmt.Errorf("domain %s is not verified: %s\nAdd: %s CNAME %s (DNS can take a few minutes)", hostname, terminalSafe(detail), hostname, terminalSafe(streamString(domain, "cnameTarget")))
	}
	_, _ = fmt.Fprintf(rtOut, "Domain %s is verified; HubFly issues its certificate automatically.\n", hostname)
	writeStreamSync(result)
	return nil
}

func defineStreamBinding(fs *flag.FlagSet, mode string) streamSpec {
	to := fs.String("to", "", "")
	playbackKey := fs.String("playback-key", "", "")
	channel := fs.String("channel", "", "")
	prefix := fs.String("client-prefix", "", "")
	return streamSpec{
		validate: func(string) error {
			return streamOneOf("client-prefix", *prefix, "none", "NEXT_PUBLIC_", "VITE_", "PUBLIC_")
		},
		build: func(b *streamBuild) error {
			if *to != "" {
				projects, err := fetchManagedProjects(b.target.token, *to)
				if err != nil {
					return err
				}
				b.body["targetProjectId"] = projects[0].ID
			}
			if b.body["targetProjectId"] == nil {
				return errors.New("invalid arguments: pass --to <project>")
			}
			if *playbackKey != "" {
				id, err := b.res.id(streamResourceByName("playback-keys"), *playbackKey)
				if err != nil {
					return err
				}
				b.body["playbackKeyId"] = id
			}
			if *channel != "" {
				id, err := b.res.id(streamResourceByName("channels"), *channel)
				if err != nil {
					return err
				}
				b.body["channelId"] = id
			}
			if *prefix != "" {
				b.body["clientPrefix"] = *prefix
			}
			return nil
		},
	}
}

func newStreamTabWriter() *tabwriter.Writer {
	return tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
}
