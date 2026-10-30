package cli

import (
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Live control (POST …/stream/control): go live, stop, destinations,
// recordings, slates, disconnects and raw ops; mixer scene switches; and the
// graphics store.

const streamControlUsage = "hubfly stream control <server> [--op '<json>'...] [--go-live <channel>]... [--stop <channel>]... [--destination-start <destination>]... [--destination-stop <destination>]... [--record-start <channel>]... [--record-stop <channel>]... [--slate <channel>=on|off]... [--disconnect <input>[:primary|backup]]... [--project <id|name>] [--json]"

// runStreamControlOps sends ops in one control request and prints one line
// per op. Any failed op makes the command fail.
func runStreamControlOps(target streamTarget, ops []map[string]any, asJSON bool) error {
	var result struct {
		ID      string `json:"id"`
		Results []struct {
			OK    bool   `json:"ok"`
			Clip  string `json:"clip"`
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"results"`
	}
	body := map[string]any{"id": randomRealtimeID("ctl_"), "ops": ops}
	if err := streamCall(target.token, target.instance, "POST", "/control", nil, body, &result); err != nil {
		return streamAPIError(err)
	}
	failed := 0
	for _, item := range result.Results {
		if !item.OK {
			failed++
		}
	}
	if asJSON {
		if err := streamPrintJSON(result); err != nil {
			return err
		}
	} else {
		for index, op := range ops {
			label := streamControlLabel(op)
			if index >= len(result.Results) {
				_, _ = fmt.Fprintf(rtOut, "?      %s (no result)\n", label)
				continue
			}
			item := result.Results[index]
			switch {
			case item.OK && item.Clip != "":
				_, _ = fmt.Fprintf(rtOut, "ok     %s (clip %s)\n", label, item.Clip)
			case item.OK:
				_, _ = fmt.Fprintf(rtOut, "ok     %s\n", label)
			default:
				code, message := "error", ""
				if item.Error != nil {
					code, message = item.Error.Code, item.Error.Message
				}
				_, _ = fmt.Fprintf(rtOut, "failed %s: %s %s\n", label, terminalSafe(code), terminalSafe(redactStreamSecretText(message)))
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d control operations failed", failed, len(ops))
	}
	return nil
}

// streamControlLabel renders an op as "<op> <target>".
func streamControlLabel(op map[string]any) string {
	name, _ := op["op"].(string)
	parts := []string{terminalSafe(name)}
	for _, key := range []string{"channel", "mixer", "scene", "destination", "input", "graphic", "playlist", "source"} {
		if value, ok := op[key].(string); ok && value != "" {
			parts = append(parts, terminalSafe(value))
		}
	}
	if on, ok := op["on"].(bool); ok {
		parts = append(parts, map[bool]string{true: "on", false: "off"}[on])
	}
	return strings.Join(parts, " ")
}

func streamControlFlow(args []string) error {
	fs := newRealtimeFlags("stream control")
	asJSON := fs.Bool("json", false, "")
	raw := &rtList{}
	fs.Var(raw, "op", "")
	lists := map[string]*rtList{}
	for _, name := range []string{"go-live", "stop", "destination-start", "destination-stop", "record-start", "record-stop", "slate", "disconnect"} {
		lists[name] = &rtList{split: true}
		fs.Var(lists[name], name, "")
	}
	var rawOps []map[string]any
	target, _, err := streamParseTarget(fs, args, streamControlUsage, 1, func([]string) error {
		var opsErr error
		if rawOps, opsErr = parseStreamOps(raw.values, "op"); opsErr != nil {
			return opsErr
		}
		total := len(rawOps)
		for name, list := range lists {
			total += len(list.values)
			for _, value := range list.values {
				switch name {
				case "slate":
					_, state, ok := strings.Cut(value, "=")
					if !ok || (state != "on" && state != "off") {
						return fmt.Errorf("invalid --slate %q (use <channel>=on or <channel>=off)", value)
					}
				case "disconnect":
					if _, role, ok := strings.Cut(value, ":"); ok && role != "primary" && role != "backup" {
						return fmt.Errorf("invalid --disconnect %q (use <input> or <input>:primary|backup)", value)
					}
				}
			}
		}
		if total == 0 {
			return errors.New("invalid arguments: pass at least one operation")
		}
		return nil
	})
	if err != nil {
		return err
	}
	res := newStreamResolver(target)
	channels, destinations, inputs := streamResourceByName("channels"), streamResourceByName("destinations"), streamResourceByName("inputs")
	ops := []map[string]any{}
	add := func(def *streamResource, ref, op, field string, extra map[string]any) error {
		id, err := res.id(def, ref)
		if err != nil {
			return err
		}
		entry := map[string]any{"op": op, field: id}
		for key, value := range extra {
			entry[key] = value
		}
		ops = append(ops, entry)
		return nil
	}
	steps := []struct {
		flag, op, field string
		def             *streamResource
	}{
		{"go-live", "channel.start", "channel", channels},
		{"stop", "channel.stop", "channel", channels},
		{"destination-start", "destination.start", "destination", destinations},
		{"destination-stop", "destination.stop", "destination", destinations},
		{"record-start", "recording.start", "channel", channels},
		{"record-stop", "recording.stop", "channel", channels},
	}
	for _, step := range steps {
		for _, ref := range lists[step.flag].values {
			if err := add(step.def, ref, step.op, step.field, nil); err != nil {
				return err
			}
		}
	}
	for _, value := range lists["slate"].values {
		ref, state, _ := strings.Cut(value, "=")
		if err := add(channels, ref, "channel.slate", "channel", map[string]any{"on": state == "on"}); err != nil {
			return err
		}
	}
	for _, value := range lists["disconnect"].values {
		ref, role, _ := strings.Cut(value, ":")
		extra := map[string]any{}
		if role != "" {
			extra["role"] = role
		}
		if err := add(inputs, ref, "input.disconnect", "input", extra); err != nil {
			return err
		}
	}
	ops = append(ops, rawOps...)
	return runStreamControlOps(target, ops, *asJSON)
}

// ---------------------------------------------------------------------------
// Scenes
// ---------------------------------------------------------------------------

func streamSceneFlow(args []string) error {
	const usage = "hubfly stream scene list|switch <server> <mixer> [<scene>] [--fade <duration>|--cut] [--json]"
	if len(args) == 0 {
		return dbdUsageError(usage)
	}
	switch args[0] {
	case "list", "ls":
		return streamSceneListFlow(args[1:])
	case "switch", "set", "go":
		return streamSceneSwitchFlow(args[1:])
	}
	return fmt.Errorf("unknown stream scene command %q\nusage: %s", args[0], usage)
}

func streamSceneListFlow(args []string) error {
	const usage = "hubfly stream scene list <server> <mixer> [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream scene list")
	asJSON := fs.Bool("json", false, "")
	target, positionals, err := streamParseTarget(fs, args, usage, 2, nil)
	if err != nil {
		return err
	}
	mixer, err := newStreamResolver(target).get(streamResourceByName("mixers"), positionals[1])
	if err != nil {
		return err
	}
	if *asJSON {
		return streamPrintJSON(mixer["scenes"])
	}
	scenes, _ := mixer["scenes"].([]any)
	if len(scenes) == 0 {
		_, _ = fmt.Fprintln(rtOut, "This mixer has no scenes.")
		return nil
	}
	tw := newStreamTabWriter()
	_, _ = fmt.Fprintln(tw, "SCENE\tLAYERS\tDEFAULT\tID")
	for _, raw := range scenes {
		scene, _ := raw.(map[string]any)
		layers, _ := scene["layers"].([]any)
		isDefault := ""
		if streamString(scene, "id") == streamString(mixer, "defaultScene") {
			isDefault = "yes"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", terminalSafe(streamString(scene, "name")), len(layers), isDefault, streamString(scene, "id"))
	}
	return tw.Flush()
}

func streamSceneSwitchFlow(args []string) error {
	const usage = "hubfly stream scene switch <server> <mixer> <scene> [--fade <duration>|--cut] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream scene switch")
	fade := &rtOptString{}
	fs.Var(fade, "fade", "")
	cut := fs.Bool("cut", false, "")
	asJSON := fs.Bool("json", false, "")
	var transition map[string]any
	target, positionals, err := streamParseTarget(fs, args, usage, 3, func([]string) error {
		if fade.set && *cut {
			return errors.New("invalid flags: pass --fade or --cut, not both")
		}
		var transitionErr error
		switch {
		case *cut:
			transition, transitionErr = parseStreamTransition("cut")
		case fade.set:
			transition, transitionErr = parseStreamTransition("fade:" + fade.value)
		}
		return transitionErr
	})
	if err != nil {
		return err
	}
	mixer, err := newStreamResolver(target).get(streamResourceByName("mixers"), positionals[1])
	if err != nil {
		return err
	}
	scene, err := pickStreamScene(mixer, positionals[2])
	if err != nil {
		return err
	}
	op := map[string]any{"op": "mixer.scene", "mixer": streamString(mixer, "id"), "scene": streamString(scene, "id")}
	if transition != nil {
		op["transition"] = transition
	}
	return runStreamControlOps(target, []map[string]any{op}, *asJSON)
}

// ---------------------------------------------------------------------------
// Graphics
// ---------------------------------------------------------------------------

const streamGraphicMaxBytes = 5 << 20

var streamGraphicKinds = map[string][2]string{
	".png": {"image", "image/png"}, ".jpg": {"image", "image/jpeg"}, ".jpeg": {"image", "image/jpeg"},
	".gif": {"image", "image/gif"}, ".webp": {"image", "image/webp"}, ".svg": {"svg", "image/svg+xml"},
	".html": {"html", "text/html"}, ".htm": {"html", "text/html"}, ".ttf": {"font", "font/ttf"},
	".otf": {"font", "font/otf"}, ".woff": {"font", "font/woff"}, ".woff2": {"font", "font/woff2"},
}

func streamGraphicsResource() *streamResource {
	return &streamResource{
		Name: "graphics", Aliases: []string{"graphic"}, Noun: "graphic", Path: "/graphics", IDPrefix: "sgr_", NameField: "name", ResultKey: "graphic",
		Columns: []streamColumn{
			streamCol("NAME", "name"), streamCol("KIND", "kind"), streamCol("TYPE", "contentType"),
			streamColFunc("SIZE", func(i map[string]any) string {
				if value, ok := i["bytes"].(float64); ok {
					return formatBytes(int64(value))
				}
				return "-"
			}),
			streamCol("ID", "id"),
		},
		Define:      defineStreamGraphic,
		UpdateUsage: "hubfly stream graphics update <server> <graphic> [--name <name>] [--var <key>=<value>]... [--json]",
		Update:      true, Delete: true,
		DeleteNote: "Mixers and slates that show it lose it.",
		Extras:     map[string]func(*streamResource, []string) error{"upload": streamGraphicUploadFlow},
		Usage: `  hubfly stream graphics [list] <server> [--json]
  hubfly stream graphics get <server> <graphic> [--json]
  hubfly stream graphics upload <server> <file> [--name <name>] [--kind image|svg|html|font] [--content-type <type>] [--json]
                                (≤ 5 MiB)
  hubfly stream graphics update <server> <graphic> [--name <name>] [--var <key>=<value>]... (--var <key>= removes one)
  hubfly stream graphics delete <server> <graphic> [--yes]`,
	}
}

func defineStreamGraphic(fs *flag.FlagSet, mode string) streamSpec {
	name := fs.String("name", "", "")
	vars := &rtList{}
	fs.Var(vars, "var", "")
	return streamSpec{
		validate: func(string) error {
			for _, value := range vars.values {
				if key, _, ok := strings.Cut(value, "="); !ok || strings.TrimSpace(key) == "" {
					return fmt.Errorf("invalid --var %q (use <key>=<value>)", value)
				}
			}
			return nil
		},
		build: func(b *streamBuild) error {
			if *name != "" {
				b.body["name"] = strings.TrimSpace(*name)
			}
			if len(vars.values) > 0 {
				variables := streamNested(b, "variables", map[string]any{})
				for _, value := range vars.values {
					key, text, _ := strings.Cut(value, "=")
					key = strings.TrimSpace(key)
					if text == "" {
						delete(variables, key)
					} else {
						variables[key] = text
					}
				}
			}
			return nil
		},
	}
}

func streamGraphicUploadFlow(def *streamResource, args []string) error {
	const usage = "hubfly stream graphics upload <server> <file> [--name <name>] [--kind image|svg|html|font] [--content-type <type>] [--project <id|name>] [--json]"
	fs := newRealtimeFlags("stream graphics upload")
	name := fs.String("name", "", "")
	kind := fs.String("kind", "", "")
	contentType := fs.String("content-type", "", "")
	asJSON := fs.Bool("json", false, "")
	var data []byte
	target, _, err := streamParseTarget(fs, args, usage, 2, func(positionals []string) error {
		path := positionals[1]
		guess := streamGraphicKinds[strings.ToLower(filepath.Ext(path))]
		if *kind == "" {
			*kind = guess[0]
		}
		if *contentType == "" {
			*contentType = guess[1]
		}
		if *kind == "" || *contentType == "" {
			return fmt.Errorf("invalid file type %q: pass --kind and --content-type", filepath.Ext(path))
		}
		if err := streamOneOf("kind", *kind, "image", "svg", "html", "font"); err != nil {
			return err
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			return fmt.Errorf("invalid file: %v", statErr)
		}
		if info.Size() > streamGraphicMaxBytes {
			return fmt.Errorf("invalid file: %s is larger than 5 MiB", formatBytes(info.Size()))
		}
		var readErr error
		data, readErr = os.ReadFile(path)
		if *name == "" {
			*name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
		return readErr
	})
	if err != nil {
		return err
	}
	body := map[string]any{"name": *name, "kind": *kind, "contentType": *contentType, "dataBase64": base64.StdEncoding.EncodeToString(data)}
	var result map[string]any
	if err := streamCall(target.token, target.instance, "POST", "/graphics/upload", nil, body, &result); err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return streamPrintJSON(result)
	}
	graphic, _ := result["graphic"].(map[string]any)
	_, _ = fmt.Fprintf(rtOut, "Uploaded graphic '%s' (%s, %s).\n", terminalSafe(streamString(graphic, "name")), streamString(graphic, "id"), formatBytes(int64(len(data))))
	writeStreamSync(result)
	return nil
}

// streamNow is the clock of tail and push (tests replace it).
var streamNow = time.Now
