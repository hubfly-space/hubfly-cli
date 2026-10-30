package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// `hubfly stream` (alias `live`): Managed Streams servers, their resources
// (inputs, channels, destinations, mixers, playlists, schedules, recordings,
// playback keys, webhooks, …), live control, the studio socket (tail), local
// publishing with ffmpeg (push) and playback (play).

const streamUsageText = `Managed Streams (live video and audio: ingest, transcode, restream, play back):
  hubfly stream list [--project <id|name>] [--json]
  hubfly stream info <server> [--json]
  hubfly stream create [<name>] [--speed <mbps>] [--processing <mbps>] [--timezone <tz>]
                       [--input <name>|--no-input] [--project <id|name|new>] [--yes] [--no-wait] [--json]
                       (shows the first input's stream key once, with OBS settings)
  hubfly stream speed <server> <mbps> [--processing <mbps>] [--yes] [--no-wait] [--json]
  hubfly stream start|stop <server> [--no-wait] [--json]
  hubfly stream rename <server> <new-name>
  hubfly stream protect <server> on|off
  hubfly stream delete <server> [--yes] [--wait]
  hubfly stream operations <server> [--limit <n>] [--json]
  hubfly stream settings <server> [--timezone <tz>] [--offline-slate <graphic>|none] [--origins <a,b>|none]
                         [--webrtc-sessions <n>] [--json]
  hubfly stream live <server> [--json]
  hubfly stream usage <server> [--month YYYY-MM] [--json]
  hubfly stream events <server> [--after <cursor>] [--limit <n>] [--json]
  hubfly stream stats <server> [--from <time>] [--to <time>] [--channel <channel>] [--json]

  hubfly stream inputs [list] <server> [--json]
  hubfly stream inputs get <server> <input> [--json]          (connection details per protocol, keys masked)
  hubfly stream inputs create <server> <name> [--protocols rtmp,rtmps,srt,whip,icecast] [--backup-key]
                              [--pull <url>] [--max-bitrate <kbps>] [--allow <cidr>...] [--srt-passphrase-stdin]
  hubfly stream inputs update <server> <input> [--name <name>] [--protocols <list>] [--enable|--disable] ...
  hubfly stream inputs delete <server> <input> [--yes]
  hubfly stream inputs keys reveal <server> <input> [--json]
  hubfly stream inputs keys rotate <server> <input> [--role primary|backup] [--remove] [--yes] [--json]
  hubfly stream channels [list|get|create|update|delete] <server> ...
                              create/update: --source input:<id>|mixer:<id>|playlist:<id> [--latency standard|low|ultra]
                              [--access public|token] [--dvr <minutes>] [--max-viewers <n>] [--no-webrtc] [--no-ll-hls]
  hubfly stream channels playback <server> <channel> [--json]   (URLs and embed code)
  hubfly stream channels token <server> <channel> [--ttl 1h] [--sub <viewer>] [--ip <cidr>] [--json]
  hubfly stream destinations [list|get|create|update|delete] <server> ...
                              create: <name> --platform youtube|twitch|facebook|kick|x|linkedin|custom
                              --source channel:<id>|input:<id> [--url <url>] --stream-key-stdin [--auto-start[=false]]
  hubfly stream destinations start|stop <server> <destination>
  hubfly stream mixers|playlists|schedules [list|get|create|update|delete] <server> ... [--file <spec.json|->]
  hubfly stream scene list <server> <mixer>
  hubfly stream scene switch <server> <mixer> <scene> [--fade 500ms|--cut]
  hubfly stream schedules preview <server> --start <time> --duration <minutes> [--rrule <rule>] [--timezone <tz>]
  hubfly stream schedules runs <server> <schedule> [--limit <n>] [--json]
  hubfly stream recordings [list] <server> [--kind recording|clip] [--channel <channel>] [--json]
  hubfly stream recordings delete <server> <recording> [--delete-object] [--yes]
  hubfly stream clips [list] <server> [--channel <channel>] [--json]
  hubfly stream clips create <server> --channel <channel> --from <time|-90s> --to <time> --name <name> [--storage-link <link>]
  hubfly stream playback-keys [list|create|rotate|delete] <server> ...   (secrets print once)
  hubfly stream webhooks [list|create|update|delete|test|deliveries] <server> ...
                              create: --url <https-url> [--events channel.live,input.connected] (secret prints once)
  hubfly stream graphics [list|get|upload|update|delete] <server> ...
  hubfly stream storage-links [list|create|delete|assets] <server> ...
  hubfly stream domains [list|create|verify|delete] <server> ...
  hubfly stream bindings [list|create|delete] <server> ...      create: --to <project> [--playback-key <id>] [--channel <id>]

  hubfly stream control <server> [--op '<json>'...] [--go-live <channel>] [--stop <channel>]
                        [--destination-start <id>] [--destination-stop <id>] [--record-start <channel>]
                        [--record-stop <channel>] [--slate <channel>=on|off] [--disconnect <input>[:role]] [--json]
  hubfly stream tail <server> [--meters] [--watch in:<input>,ch:<channel>,mx:<mixer>] [--json]
  hubfly stream push <server> <file|device[:<spec>]> [--input <input>] [--protocol srt|rtmps] [--loop]
                     [--key-stdin] [--role primary|backup] [--bitrate <kbps>] [--ffmpeg <path>] [--dry-run]
  hubfly stream play <server> <channel> [--url-only] [--ttl <duration>] [--player mpv|ffplay] [--json]

Run "hubfly stream <resource> help" for every flag of a resource. Creates and updates of
resources also take --file <spec.json|-> with the Dashboard API body (camelCase; flags win).
<server> is a stream server id or name (narrow with --project); <input>, <channel>, … are
ids or names. Keys and secrets are masked unless you run a reveal; new keys and secrets print
once when created or rotated. push needs a local ffmpeg; the key never reaches its arguments.
"live" is an alias of "stream".`

func printStreamUsage() {
	_, _ = fmt.Fprintln(rtOut, streamUsageText)
}

func runStreamGroup(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printStreamUsage()
		return nil
	}
	command, rest := args[0], args[1:]
	switch command {
	case "list", "ls":
		return streamListFlow(rest)
	case "info", "show", "get":
		return streamInfoFlow(rest)
	case "create":
		return streamCreateFlow(rest)
	case "delete", "rm":
		return streamDeleteFlow(rest)
	case "speed":
		return streamSpeedFlow(rest)
	case "settings":
		return streamSettingsFlow(rest)
	case "live", "status":
		return streamLiveFlow(rest)
	case "usage":
		return streamUsageFlow(rest)
	case "events":
		return streamEventsFlow(rest)
	case "stats":
		return streamStatsFlow(rest)
	case "start", "stop", "rename", "protect":
		return runManagedLifecycleCommand(managedServiceStream, command, rest)
	case "operations", "ops":
		return runManagedObserveCommand(managedServiceStream, "operations", rest)
	case "control":
		return streamControlFlow(rest)
	case "scene", "scenes":
		return streamSceneFlow(rest)
	case "tail":
		return streamTailFlow(rest)
	case "push", "publish":
		return streamPushFlow(rest)
	case "play":
		return streamPlayFlow(rest)
	}
	if def, ok := lookupStreamResource(command); ok {
		return runStreamResource(def, rest)
	}
	return fmt.Errorf("unknown stream command %q (available: list, info, create, delete, speed, start, stop, rename, protect, operations, settings, live, usage, events, stats, inputs, channels, destinations, mixers, playlists, schedules, recordings, clips, playback-keys, webhooks, graphics, storage-links, domains, bindings, control, scene, tail, push, play)", command)
}

// streamTarget is a resolved stream server with the caller's token.
type streamTarget struct {
	token    string
	instance rtInstance
}

func resolveStreamTarget(ref, projectRef string) (streamTarget, error) {
	token, err := ensureAuth(true)
	if err != nil {
		return streamTarget{}, err
	}
	instance, err := resolveManagedInstanceOf(token, ref, projectRef, managedServiceStream)
	if err != nil {
		return streamTarget{}, err
	}
	return streamTarget{token: token, instance: instance}, nil
}

// ---------------------------------------------------------------------------
// list / info
// ---------------------------------------------------------------------------

func streamListFlow(args []string) error {
	const usage = "hubfly stream list [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream list")
	projectRef := fs.String("project", "", "")
	fs.StringVar(projectRef, "p", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) > 1 {
		return dbdUnexpected(positionals[1], usage)
	}
	if len(positionals) == 1 && *projectRef == "" {
		*projectRef = positionals[0]
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	instances, err := listManagedInstancesOf(token, *projectRef, managedServiceStream)
	if err != nil {
		return err
	}
	if *asJSON {
		return rtPrintJSON(instances)
	}
	if len(instances) == 0 {
		_, _ = fmt.Fprintln(rtOut, "No stream servers found. Create one with: hubfly stream create <name>")
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tSTATUS\tPRICE\tPROJECT\tID")
	for _, instance := range instances {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", instance.Name, managedStatusLabel(instance), rtMonthly(instance.Price), instance.ProjectName, instance.ID)
	}
	return tw.Flush()
}

func streamInfoFlow(args []string) error {
	const usage = "hubfly stream info <server> [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream info")
	projectRef := fs.String("project", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	target, err := resolveStreamTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	server, err := fetchStreamServer(target.token, target.instance)
	if err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return streamPrintJSON(map[string]any{"instance": target.instance, "stream": server})
	}
	instance := target.instance
	status := instance.Status
	if instance.StatusReason != nil && *instance.StatusReason != "" {
		status += " (" + *instance.StatusReason + ")"
	}
	rows := [][2]string{
		{"Stream server", instance.Name},
		{"ID", instance.ID},
		{"Project", fmt.Sprintf("%s (%s)", instance.ProjectName, instance.ProjectID)},
		{"Status", status},
		{"Host", server.Host},
		{"Speed", fmt.Sprintf("%d Mbps (%s)", server.SpeedMbps, streamMonthlyPrice(server.Price.SpeedUsdPerMonth))},
		{"Processing", fmt.Sprintf("%d Mbps", server.ProcessingMbps)},
		{"WebRTC sessions", formatThousands(server.WebrtcSessions)},
		{"Time zone", server.Timezone},
		{"Config", streamSyncText(server)},
	}
	if !server.Billing.Allowed {
		reason := "billing"
		if server.Billing.Reason != nil {
			reason = *server.Billing.Reason
		}
		rows = append(rows, [2]string{"Billing", "blocked (" + reason + "): media is paused until it is resolved"})
	}
	if server.Live != nil {
		rows = append(rows, [2]string{"Live", fmt.Sprintf("%d inputs, %d channels, %s viewers, %.1f Mbps out", server.Live.InputsLive, server.Live.ChannelsLive, formatThousands(server.Live.Viewers), server.Live.EgressMbps)})
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	for _, row := range rows {
		_, _ = fmt.Fprintf(tw, "%s:\t%s\n", row[0], row[1])
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(rtOut, "\nIngest:")
	tw = tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	for _, row := range [][2]string{
		{"RTMP", server.Ingest.RTMP}, {"RTMPS", server.Ingest.RTMPS}, {"SRT", server.Ingest.SRT},
		{"WHIP", server.Ingest.WhipBase + "<input id>"}, {"Icecast", server.Ingest.IcecastBase + "<input id>"},
	} {
		if strings.TrimSpace(row[1]) != "" && row[1] != "<input id>" {
			_, _ = fmt.Fprintf(tw, "  %s:\t%s\n", row[0], row[1])
		}
	}
	return tw.Flush()
}

func streamSyncText(server streamServer) string {
	if server.Sync.Generation == server.Sync.AppliedGeneration {
		return fmt.Sprintf("in sync (generation %d)", server.Sync.Generation)
	}
	text := fmt.Sprintf("pending (generation %d, applied %d)", server.Sync.Generation, server.Sync.AppliedGeneration)
	if server.Sync.LastError != nil && *server.Sync.LastError != "" {
		text += ": " + *server.Sync.LastError
	}
	return text
}

func streamMonthlyPrice(usd float64) string {
	return fmt.Sprintf("$%.2f/month", usd)
}

// ---------------------------------------------------------------------------
// create / speed / delete
// ---------------------------------------------------------------------------

type streamCreateOptions struct {
	Name           string
	SpeedMbps      int
	ProcessingMbps int
	Timezone       string
	InputName      string
	NoInput        bool
	ProjectRef     string
	ProjectName    string
	Yes            bool
	NoWait         bool
	JSON           bool
	Timeout        time.Duration
}

const streamCreateUsage = "hubfly stream create [<name>] [--speed <mbps>] [--processing <mbps>] [--timezone <tz>] [--input <name>|--no-input] [--project <id|name|new>] [--project-name <name>] [--yes] [--no-wait] [--timeout <duration>] [--json]"

func parseStreamCreateOptions(args []string) (streamCreateOptions, error) {
	opts := streamCreateOptions{}
	fs := newRealtimeFlags("stream create")
	fs.StringVar(&opts.Name, "name", "", "")
	fs.IntVar(&opts.SpeedMbps, "speed", 25, "")
	fs.IntVar(&opts.ProcessingMbps, "processing", 0, "")
	fs.StringVar(&opts.Timezone, "timezone", "", "")
	fs.StringVar(&opts.InputName, "input", "Main", "")
	fs.BoolVar(&opts.NoInput, "no-input", false, "")
	fs.StringVar(&opts.ProjectRef, "project", "", "")
	fs.StringVar(&opts.ProjectRef, "p", "", "")
	fs.StringVar(&opts.ProjectName, "project-name", "", "")
	fs.BoolVar(&opts.Yes, "yes", false, "")
	fs.BoolVar(&opts.Yes, "y", false, "")
	fs.BoolVar(&opts.NoWait, "no-wait", false, "")
	fs.BoolVar(&opts.JSON, "json", false, "")
	timeout := fs.String("timeout", "15m", "")
	positionals, err := parseRealtimeArgs(fs, args, streamCreateUsage)
	if err != nil {
		return opts, err
	}
	if len(positionals) > 1 {
		return opts, dbdUnexpected(positionals[1], streamCreateUsage)
	}
	if len(positionals) == 1 {
		if opts.Name != "" && opts.Name != positionals[0] {
			return opts, fmt.Errorf("name given twice\nusage: %s", streamCreateUsage)
		}
		opts.Name = positionals[0]
	}
	opts.Name = strings.TrimSpace(opts.Name)
	if opts.Name == "" {
		return opts, dbdUsageError(streamCreateUsage)
	}
	if opts.SpeedMbps < 1 || opts.SpeedMbps > 10000 {
		return opts, fmt.Errorf("invalid --speed %d (Mbps, at least 1)", opts.SpeedMbps)
	}
	if opts.ProcessingMbps < 0 {
		return opts, fmt.Errorf("invalid --processing %d (Mbps, 0 or more)", opts.ProcessingMbps)
	}
	seconds, err := parseRealtimeDuration(*timeout)
	if err != nil || seconds <= 0 {
		return opts, fmt.Errorf("invalid --timeout %q", *timeout)
	}
	opts.Timeout = time.Duration(seconds) * time.Second
	return opts, nil
}

// resolveStreamProject returns the managed project to create in; a zero
// project with create=true means "create a new managed project".
func resolveStreamProject(token, ref string) (project, bool, error) {
	ref = strings.TrimSpace(ref)
	if strings.EqualFold(ref, "new") {
		return project{}, true, nil
	}
	if ref != "" {
		projects, err := fetchManagedProjects(token, ref)
		if err != nil {
			return project{}, false, err
		}
		chosen := projects[0]
		if chosen.Type != "" && chosen.Type != "managed" {
			return project{}, false, fmt.Errorf("project '%s' is a %s project; stream servers live in managed projects (use --project new)", chosen.Name, chosen.Type)
		}
		return chosen, false, nil
	}
	managed, err := fetchManagedProjects(token, "")
	if err != nil {
		return project{}, false, err
	}
	if len(managed) == 0 {
		return project{}, true, nil
	}
	if !rtInteractive() {
		return project{}, false, errors.New("invalid arguments: choose a managed project with --project <id|name|new>")
	}
	_, _ = fmt.Fprintln(rtOut, "Managed projects:")
	for index, item := range managed {
		_, _ = fmt.Fprintf(rtOut, "  %d) %s (%s)\n", index+1, item.Name, item.ID)
	}
	_, _ = fmt.Fprintf(rtOut, "  %d) New managed project\n", len(managed)+1)
	choice, err := promptNumber(fmt.Sprintf("Project [1-%d]: ", len(managed)+1), len(managed)+1)
	if err != nil {
		return project{}, false, err
	}
	if choice == 0 {
		return project{}, false, errors.New("cancelled")
	}
	if choice == len(managed)+1 {
		return project{}, true, nil
	}
	return managed[choice-1], false, nil
}

// writeStreamQuote prints the speed price: the list price, and the price
// after the owner's free speed "if this is your only running server".
func writeStreamQuote(tw *tabwriter.Writer, speedMbps, processingMbps int, price streamPriceEstimate) {
	_, _ = fmt.Fprintf(tw, "Speed:\t%d Mbps, %s ($%.4f/hour)\n", speedMbps, streamMonthlyPrice(price.Monthly.SpeedUsd), price.quotedHourlyTotal())
	if price.FreeSpeedMbps > 0 {
		_, _ = fmt.Fprintf(tw, "\t%s if this is your only running server (%s Mbps are free)\n",
			streamMonthlyPrice(price.Monthly.SpeedUsdAfterFree), strconv.FormatFloat(price.FreeSpeedMbps, 'f', -1, 64))
	}
	if processingMbps > 0 {
		_, _ = fmt.Fprintf(tw, "Processing:\t%d Mbps reserved (billed by use, like bandwidth)\n", processingMbps)
	}
}

func streamCreateFlow(args []string) error {
	opts, err := parseStreamCreateOptions(args)
	if err != nil {
		return err
	}
	token, err := ensureAuth(true)
	if err != nil {
		return err
	}
	target, createProject, err := resolveStreamProject(token, opts.ProjectRef)
	if err != nil {
		return err
	}
	price, err := fetchStreamPrice(token, opts.SpeedMbps, opts.ProcessingMbps)
	if err != nil {
		return streamAPIError(err)
	}
	projectName := strings.TrimSpace(opts.ProjectName)
	if projectName == "" {
		projectName = opts.Name
	}
	projectLabel := fmt.Sprintf("%s (%s)", target.Name, target.ID)
	if createProject {
		projectLabel = fmt.Sprintf("new managed project '%s' (HubFly chooses the region)", projectName)
	}
	quoteOut := rtOut
	if opts.JSON {
		quoteOut = rtErr // keep stdout clean for --json
	}
	tw := tabwriter.NewWriter(quoteOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Stream server:\t%s\n", opts.Name)
	writeStreamQuote(tw, opts.SpeedMbps, opts.ProcessingMbps, price)
	_, _ = fmt.Fprintf(tw, "Project:\t%s\n", projectLabel)
	_ = tw.Flush()
	_, _ = fmt.Fprintln(quoteOut, "Bandwidth and processing are billed by use; a stopped server pays nothing for speed.")

	confirmed, err := dbdConfirm(opts.Yes, "Create this stream server?", "create the stream server")
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled.")
		return nil
	}
	if createProject {
		created, createErr := createManagedProjectFor(token, projectName, managedServiceStream)
		if createErr != nil {
			return fmt.Errorf("failed to create managed project: %w", createErr)
		}
		target = created
		_, _ = fmt.Fprintf(rtErr, "Created managed project '%s' (%s).\n", created.Name, created.ID)
	}
	stream := map[string]any{"speedMbps": opts.SpeedMbps}
	if opts.ProcessingMbps > 0 {
		stream["processingMbps"] = opts.ProcessingMbps
	}
	if tz := strings.TrimSpace(opts.Timezone); tz != "" {
		stream["timezone"] = tz
	}
	if !opts.NoInput {
		stream["firstInput"] = map[string]any{"name": strings.TrimSpace(opts.InputName), "kind": "push"}
	}
	body := map[string]any{
		"name":              opts.Name,
		"service":           managedServiceStream,
		"stream":            stream,
		"quotedHourlyTotal": price.quotedHourlyTotal(),
		"idempotencyKey":    randomRealtimeID("cli-stream-"),
	}
	var created streamCreateResult
	if err := doJSONRequest(http.MethodPost, rtProjectPath(target.ID)+"/managed/instances/create", token, body, &created); err != nil {
		return streamAPIError(err)
	}
	instance := created.Instance
	instance.ProjectID, instance.ProjectName = target.ID, target.Name
	if instance.Service == "" {
		instance.Service = managedServiceStream
	}
	var operation *rtOperation
	if !opts.NoWait && created.OperationID != "" {
		finished, waitErr := waitForManagedOperation(token, instance, created.OperationID, "Creating stream server", opts.Timeout, opts.JSON, false)
		if waitErr != nil {
			return waitErr
		}
		operation = &finished
	}
	host := ""
	if created.Stream != nil {
		host = created.Stream.Host
	}
	if opts.JSON {
		// The first key is returned once, so it is printed unmasked here.
		result := map[string]any{"instance": instance, "operationId": created.OperationID, "operation": operation, "price": created.Price}
		if created.Stream != nil {
			result["stream"] = created.Stream
		}
		return rtPrintJSON(result)
	}
	if operation != nil {
		_, _ = fmt.Fprintf(rtOut, "Stream server '%s' is ready.\n", instance.Name)
	} else {
		_, _ = fmt.Fprintf(rtOut, "Creating stream server '%s' (operation %s).\n", instance.Name, created.OperationID)
	}
	tw = tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  ID:\t%s\n", instance.ID)
	if host != "" {
		_, _ = fmt.Fprintf(tw, "  Host:\t%s\n", host)
	}
	_ = tw.Flush()
	if created.Stream != nil && created.Stream.FirstInput != nil && host != "" {
		input := created.Stream.FirstInput
		_, _ = fmt.Fprintf(rtOut, "\nInput '%s' (%s). The stream key is shown only now; reveal it later with:\n  hubfly stream inputs keys reveal %s %s\n\n", input.Name, input.ID, instance.Name, input.ID)
		writeStreamOBSSettings(rtOut, host, input.Keys.Primary)
	}
	_, _ = fmt.Fprintln(rtOut, "\nNext steps:")
	_, _ = fmt.Fprintf(rtOut, "  hubfly stream channels create %s \"Main\" --source input:<input id>   # play an input\n", instance.Name)
	_, _ = fmt.Fprintf(rtOut, "  hubfly stream push %s video.mp4                                     # test with ffmpeg\n", instance.Name)
	return nil
}

// writeStreamOBSSettings prints the encoder settings of INGEST.md
// "Recommended encoder settings" for OBS-style encoders.
func writeStreamOBSSettings(w interface{ Write([]byte) (int, error) }, host, key string) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "OBS (Settings > Stream, Service: Custom):")
	_, _ = fmt.Fprintf(tw, "  Server:\trtmps://%s:443/live\n", host)
	_, _ = fmt.Fprintf(tw, "  Stream key:\t%s\n", key)
	_, _ = fmt.Fprintln(tw, "  Output:\tH.264 (x264 or hardware), CBR 4,500-6,000 kbps, keyframe interval 2 s,")
	_, _ = fmt.Fprintln(tw, "\tB-frames 0 for WebRTC playback; audio AAC 160 kbps, 48 kHz")
	_, _ = fmt.Fprintf(tw, "  SRT instead:\tsrt://%s:9000?streamid=publish:<stream key>&latency=2000000\n", host)
	_ = tw.Flush()
}

func streamSpeedFlow(args []string) error {
	const usage = "hubfly stream speed <server> <mbps> [--processing <mbps>] [--project <id|name>] [--yes] [--no-wait] [--json]"
	fs := newRealtimeFlags("stream speed")
	projectRef := fs.String("project", "", "")
	processing := fs.Int("processing", -1, "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	noWait := fs.Bool("no-wait", false, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 {
		return dbdUsageError(usage)
	}
	speed, err := strconv.Atoi(strings.TrimSuffix(strings.ToLower(positionals[1]), "mbps"))
	if err != nil || speed < 1 {
		return fmt.Errorf("invalid speed %q (Mbps, at least 1)\nusage: %s", positionals[1], usage)
	}
	target, err := resolveStreamTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	current, err := fetchStreamServer(target.token, target.instance)
	if err != nil {
		return streamAPIError(err)
	}
	processingMbps := current.ProcessingMbps
	if *processing >= 0 {
		processingMbps = *processing
	}
	if speed == current.SpeedMbps && processingMbps == current.ProcessingMbps {
		_, _ = fmt.Fprintf(rtOut, "Stream server '%s' already runs at %d Mbps.\n", target.instance.Name, speed)
		return nil
	}
	price, err := fetchStreamPrice(target.token, speed, processingMbps)
	if err != nil {
		return streamAPIError(err)
	}
	quoteOut := rtOut
	if *asJSON {
		quoteOut = rtErr
	}
	tw := tabwriter.NewWriter(quoteOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Stream server:\t%s (now %d Mbps, %s)\n", target.instance.Name, current.SpeedMbps, streamMonthlyPrice(current.Price.SpeedUsdPerMonth))
	writeStreamQuote(tw, speed, processingMbps, price)
	_ = tw.Flush()
	_, _ = fmt.Fprintln(quoteOut, "The change applies live; publishers and viewers stay connected.")
	confirmed, err := dbdConfirm(*yes, "Change the speed?", "change the speed")
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled.")
		return nil
	}
	body := map[string]any{"speedMbps": speed, "quotedHourlyTotal": price.quotedHourlyTotal()}
	if *processing >= 0 {
		body["processingMbps"] = processingMbps
	}
	result, err := resizeManagedInstance(target.token, target.instance, body)
	if err != nil {
		return managedAPIError(managedGroupFor(managedServiceStream), target.instance.Name, err)
	}
	var operation *rtOperation
	if !*noWait && result.OperationID != "" {
		finished, waitErr := waitForManagedOperation(target.token, target.instance, result.OperationID, "Changing speed", managedWaitTimeouts["resize"], *asJSON, false)
		if waitErr != nil {
			return waitErr
		}
		operation = &finished
	}
	if *asJSON {
		return rtPrintJSON(map[string]any{"operationId": result.OperationID, "operation": operation, "speedMbps": speed, "processingMbps": processingMbps})
	}
	if operation == nil {
		_, _ = fmt.Fprintf(rtOut, "Changing '%s' to %d Mbps (operation %s).\n", target.instance.Name, speed, result.OperationID)
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "Stream server '%s' now runs at %d Mbps.\n", target.instance.Name, speed)
	return nil
}

func streamDeleteFlow(args []string) error {
	const usage = "hubfly stream delete <server> [--project <id|name>] [--yes] [--wait]"
	fs := newRealtimeFlags("stream delete")
	projectRef := fs.String("project", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	wait := fs.Bool("wait", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	target, err := resolveStreamTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	instance := target.instance
	confirmed, err := dbdConfirmTyped(*yes, fmt.Sprintf(
		"This permanently deletes stream server '%s' (%s) in project '%s': its inputs, channels, keys, DVR and local files. Publishers and viewers are disconnected. Recordings already in Storage stay.",
		instance.Name, instance.ID, instance.ProjectName), instance.Name, "delete")
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled: the name did not match.")
		return nil
	}
	operationID, err := deleteRealtimeInstance(target.token, instance.ProjectID, instance.ID, instance.Name)
	if err != nil {
		return managedAPIError(managedGroupFor(managedServiceStream), instance.Name, err)
	}
	if *wait && operationID != "" {
		if _, waitErr := waitForManagedOperation(target.token, instance, operationID, "Deleting stream server", 15*time.Minute, false, true); waitErr != nil {
			return waitErr
		}
		_, _ = fmt.Fprintf(rtOut, "Stream server '%s' deleted.\n", instance.Name)
		return nil
	}
	_, _ = fmt.Fprintf(rtOut, "Deleting stream server '%s' (operation %s).\n", instance.Name, operationID)
	return nil
}

// ---------------------------------------------------------------------------
// settings / live / usage / events / stats
// ---------------------------------------------------------------------------

func streamSettingsFlow(args []string) error {
	const usage = "hubfly stream settings <server> [--timezone <tz>] [--offline-slate <graphic>|none] [--origins <a,b>|none] [--webrtc-sessions <n>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream settings")
	projectRef := fs.String("project", "", "")
	timezone := &rtOptString{}
	fs.Var(timezone, "timezone", "")
	slate := &rtOptString{}
	fs.Var(slate, "offline-slate", "")
	origins := &rtOptString{}
	fs.Var(origins, "origins", "")
	fs.Var(origins, "allowed-origins", "")
	webrtc := fs.Int("webrtc-sessions", -1, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	body := map[string]any{}
	if timezone.set {
		body["timezone"] = strings.TrimSpace(timezone.value)
	}
	if slate.set {
		if value := strings.TrimSpace(slate.value); value == "" || strings.EqualFold(value, "none") {
			body["offlineSlate"] = nil
		} else {
			body["offlineSlate"] = value
		}
	}
	if origins.set {
		list := []string{}
		if !strings.EqualFold(strings.TrimSpace(origins.value), "none") {
			list = dbdSplitList(origins.value)
		}
		body["allowedOrigins"] = list
	}
	if *webrtc >= 0 {
		body["webrtcSessions"] = *webrtc
	}
	target, err := resolveStreamTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	server := streamServer{}
	if len(body) == 0 {
		server, err = fetchStreamServer(target.token, target.instance)
	} else {
		var payload struct {
			Server streamServer `json:"server"`
		}
		err = streamCall(target.token, target.instance, http.MethodPut, "/settings/update", nil, body, &payload)
		server = payload.Server
	}
	if err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return streamPrintJSON(server)
	}
	if len(body) > 0 {
		_, _ = fmt.Fprintf(rtOut, "Updated the settings of '%s'.\n", target.instance.Name)
	}
	slateText := "HubFly default card"
	if server.OfflineSlate != nil && *server.OfflineSlate != "" {
		slateText = *server.OfflineSlate
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Time zone:\t%s\n", server.Timezone)
	_, _ = fmt.Fprintf(tw, "Offline slate:\t%s\n", slateText)
	_, _ = fmt.Fprintf(tw, "Allowed origins:\t%s\n", streamListText(server.AllowedOrigins, "any"))
	_, _ = fmt.Fprintf(tw, "WebRTC sessions:\t%s\n", formatThousands(server.WebrtcSessions))
	_, _ = fmt.Fprintf(tw, "Config:\t%s\n", streamSyncText(server))
	return tw.Flush()
}

func streamListText(items []string, empty string) string {
	if len(items) == 0 {
		return empty
	}
	return strings.Join(items, ", ")
}

func streamLiveFlow(args []string) error {
	const usage = "hubfly stream live <server> [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream live")
	projectRef := fs.String("project", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	target, err := resolveStreamTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	live, err := streamGetRaw(target.token, target.instance, "/live", nil)
	if err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return streamPrintJSON(live)
	}
	writeStreamLive(rtOut, redactStreamSecrets(live))
	return nil
}

// writeStreamLive renders GET …/live: the server line, then inputs, channels
// and destinations with their state as text (never color alone).
func writeStreamLive(w interface{ Write([]byte) (int, error) }, live any) {
	payload, _ := live.(map[string]any)
	if server, ok := payload["server"].(map[string]any); ok {
		_, _ = fmt.Fprintf(w, "Server: %s, %s viewers, %s/%s Mbps out", streamText(server["runState"]), streamText(server["viewers"]), streamText(server["egressMbps"]), streamText(server["speedMbps"]))
		if server["throttled"] == true {
			_, _ = fmt.Fprint(w, " (throttled at the speed cap)")
		}
		if billing, ok := server["billing"].(map[string]any); ok && billing["allowed"] == false {
			_, _ = fmt.Fprintf(w, " (billing blocked: %s)", streamText(billing["reason"]))
		}
		_, _ = fmt.Fprintln(w)
	}
	for _, section := range []struct{ key, title string }{{"inputs", "INPUT"}, {"channels", "CHANNEL"}, {"destinations", "DESTINATION"}, {"mixers", "MIXER"}} {
		items, _ := payload[section.key].([]any)
		if len(items) == 0 {
			continue
		}
		_, _ = fmt.Fprintln(w)
		tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintf(tw, "%s\tSTATE\tDETAIL\n", section.title)
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", streamText(item["id"]), streamText(item["state"]), streamLiveDetail(section.key, item))
		}
		_ = tw.Flush()
	}
}

func streamLiveDetail(section string, item map[string]any) string {
	parts := []string{}
	switch section {
	case "inputs":
		if role := streamText(item["activeRole"]); role != "-" {
			parts = append(parts, "role "+role)
		}
		publishers, _ := item["publishers"].([]any)
		for _, raw := range publishers {
			publisher, _ := raw.(map[string]any)
			text := streamText(publisher["role"]) + " via " + streamText(publisher["protocol"])
			if video, ok := publisher["video"].(map[string]any); ok {
				text += fmt.Sprintf(" %sx%s %s fps %s kbps", streamText(video["width"]), streamText(video["height"]), streamText(video["fps"]), streamText(video["bitrateKbps"]))
				if video["bframes"] == true {
					text += " (B-frames: no WebRTC passthrough)"
				}
			}
			parts = append(parts, text)
		}
	default:
		for _, key := range []string{"viewers", "source", "scene", "error", "lastError", "reason"} {
			if value, ok := item[key]; ok && value != nil && value != "" {
				parts = append(parts, key+" "+streamText(value))
			}
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "; ")
}

// streamText renders a JSON value for a table cell.
func streamText(value any) string {
	switch typed := value.(type) {
	case nil:
		return "-"
	case string:
		if typed == "" {
			return "-"
		}
		return terminalSafe(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	case map[string]any:
		if kind, ok := typed["type"].(string); ok {
			if ref, ok := typed["ref"].(string); ok {
				return terminalSafe(kind + ":" + ref)
			}
		}
	}
	return formatRealtimeTailValue(value)
}

var streamMonthPattern = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

func streamUsageFlow(args []string) error {
	const usage = "hubfly stream usage <server> [--month YYYY-MM] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream usage")
	projectRef := fs.String("project", "", "")
	month := fs.String("month", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	query := url.Values{}
	if value := strings.TrimSpace(*month); value != "" {
		if !streamMonthPattern.MatchString(value) {
			return fmt.Errorf("invalid --month %q (use YYYY-MM)", value)
		}
		query.Set("month", value)
	}
	target, err := resolveStreamTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	var report struct {
		Month  string `json:"month"`
		Totals struct {
			SpeedMbpsHours      float64 `json:"speedMbpsHours"`
			ProcessingMbpsHours float64 `json:"processingMbpsHours"`
			BandwidthGb         float64 `json:"bandwidthGb"`
			CostUsd             float64 `json:"costUsd"`
		} `json:"totals"`
		Free struct {
			BandwidthGbUsed          float64 `json:"bandwidthGbUsed"`
			BandwidthGbTotal         float64 `json:"bandwidthGbTotal"`
			ProcessingMbpsHoursUsed  float64 `json:"processingMbpsHoursUsed"`
			ProcessingMbpsHoursTotal float64 `json:"processingMbpsHoursTotal"`
			FreeSpeedMbps            float64 `json:"freeSpeedMbps"`
		} `json:"free"`
		EstimatedMonthUsd float64 `json:"estimatedMonthUsd"`
		Hourly            []any   `json:"hourly"`
	}
	var raw any
	if err := streamCall(target.token, target.instance, http.MethodGet, "/usage", query, nil, &raw); err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(raw)
	}
	if err := streamDecode(raw, &report); err != nil {
		return fmt.Errorf("read the usage: %w", err)
	}
	f := func(value float64) string { return strconv.FormatFloat(value, 'f', 2, 64) }
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "Month:\t%s\n", report.Month)
	_, _ = fmt.Fprintf(tw, "Speed:\t%s Mbps-hours (%s Mbps free at any time)\n", f(report.Totals.SpeedMbpsHours), strconv.FormatFloat(report.Free.FreeSpeedMbps, 'f', -1, 64))
	_, _ = fmt.Fprintf(tw, "Processing:\t%s Mbps-hours (free: %s of %s used)\n", f(report.Totals.ProcessingMbpsHours), f(report.Free.ProcessingMbpsHoursUsed), f(report.Free.ProcessingMbpsHoursTotal))
	_, _ = fmt.Fprintf(tw, "Bandwidth:\t%s GB (free: %s of %s GB used)\n", f(report.Totals.BandwidthGb), f(report.Free.BandwidthGbUsed), f(report.Free.BandwidthGbTotal))
	_, _ = fmt.Fprintf(tw, "Cost so far:\t$%.2f\n", report.Totals.CostUsd)
	_, _ = fmt.Fprintf(tw, "Estimate for the month:\t$%.2f\n", report.EstimatedMonthUsd)
	return tw.Flush()
}

func streamEventsFlow(args []string) error {
	const usage = "hubfly stream events <server> [--after <cursor>] [--limit <n>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream events")
	projectRef := fs.String("project", "", "")
	after := fs.String("after", "", "")
	limit := fs.Int("limit", 50, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	if *limit < 1 || *limit > 1000 {
		return fmt.Errorf("invalid --limit %d (use 1-1000)", *limit)
	}
	query := url.Values{"limit": {strconv.Itoa(*limit)}}
	if *after != "" {
		query.Set("after", *after)
	}
	target, err := resolveStreamTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	var payload struct {
		Events     []map[string]any `json:"events"`
		NextCursor *string          `json:"nextCursor"`
	}
	if err := streamCall(target.token, target.instance, http.MethodGet, "/events", query, nil, &payload); err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return streamPrintJSON(payload)
	}
	if len(payload.Events) == 0 {
		_, _ = fmt.Fprintln(rtOut, "No events.")
		return nil
	}
	for _, event := range payload.Events {
		_, _ = fmt.Fprintln(rtOut, formatStreamEvent(redactStreamSecrets(event).(map[string]any)))
	}
	if payload.NextCursor != nil && *payload.NextCursor != "" {
		_, _ = fmt.Fprintf(rtErr, "# more: hubfly stream events %s --after %s\n", target.instance.Name, *payload.NextCursor)
	}
	return nil
}

// formatStreamEvent renders "2006-01-02 15:04:05Z <type> key=value ...".
func formatStreamEvent(event map[string]any) string {
	at, _ := event["at"].(string)
	parts := []string{dbdTime(at), streamText(event["type"])}
	if data, ok := event["data"].(map[string]any); ok {
		parts = append(parts, strings.TrimPrefix(formatRealtimeTailEvent(data), "--:--:--.--- - - "))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func streamStatsFlow(args []string) error {
	const usage = "hubfly stream stats <server> [--from <time>] [--to <time>] [--channel <channel>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream stats")
	projectRef := fs.String("project", "", "")
	from := fs.String("from", "", "")
	to := fs.String("to", "", "")
	channel := fs.String("channel", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	query := url.Values{}
	for key, value := range map[string]string{"from": *from, "to": *to, "channelId": *channel} {
		if strings.TrimSpace(value) != "" {
			query.Set(key, strings.TrimSpace(value))
		}
	}
	target, err := resolveStreamTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	var payload struct {
		Points []struct {
			Minute        string  `json:"minute"`
			Channel       string  `json:"channel"`
			Viewers       float64 `json:"viewers"`
			ViewerSeconds float64 `json:"viewerSeconds"`
			EgressBytes   float64 `json:"egressBytes"`
		} `json:"points"`
	}
	var raw any
	if err := streamCall(target.token, target.instance, http.MethodGet, "/stats", query, nil, &raw); err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return rtPrintJSON(raw)
	}
	if err := streamDecode(raw, &payload); err != nil {
		return fmt.Errorf("read the stats: %w", err)
	}
	if len(payload.Points) == 0 {
		_, _ = fmt.Fprintln(rtOut, "No viewer statistics in that window.")
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "MINUTE\tCHANNEL\tVIEWERS\tVIEWER-MINUTES\tEGRESS")
	for _, point := range payload.Points {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%.1f\t%s\n", dbdTime(point.Minute), point.Channel, strconv.FormatFloat(point.Viewers, 'f', -1, 64), point.ViewerSeconds/60, formatBytes(int64(point.EgressBytes)))
	}
	return tw.Flush()
}
