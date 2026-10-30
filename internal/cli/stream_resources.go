package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
)

// Stream server resources (DASHBOARD-API.md "Stream endpoints"): inputs,
// channels, destinations, mixers, playlists, schedules, recordings, clips,
// playback keys, webhooks, graphics, storage links, domains and bindings.
// Each is a streamResource; list, get, create, update and delete are
// generic, and a resource adds its own subcommands in Extras.

// streamColumn is one column of a resource table.
type streamColumn struct {
	Header string
	Value  func(item map[string]any) string
}

// streamSpec turns parsed create/update flags into a request body.
type streamSpec struct {
	// validate runs before any request (flag combinations, formats).
	validate func(mode string) error
	// build fills b.body; it may resolve names to ids through b.res.
	build func(b *streamBuild) error
}

type streamResource struct {
	Name      string // CLI name (plural)
	Aliases   []string
	Noun      string // human noun
	Path      string // under …/stream
	IDPrefix  string // ids the Dashboard creates start with it
	NameField string // the field names resolve against
	ResultKey string // key of the resource in create/update responses
	HasGet    bool   // GET {Path}/{id} exists
	Columns   []streamColumn
	// Define registers the create/update flags of mode ("create" or "update").
	Define func(fs *flag.FlagSet, mode string) streamSpec
	// CreateArg is the positional after <server> on create ("" for none).
	CreateArg   string
	CreateUsage string
	UpdateUsage string
	Create      bool
	Update      bool
	Delete      bool
	DeleteNote  string
	// Once are top-level response fields printed unmasked after a create
	// (new keys and secrets are shown once).
	Once   []string
	Extras map[string]func(def *streamResource, args []string) error
	// Usage lists every subcommand of the resource.
	Usage string
}

// streamBuild is the state a spec builder works on.
type streamBuild struct {
	mode    string
	target  streamTarget
	res     *streamResolver
	name    string
	current map[string]any
	body    map[string]any
}

var streamResources []*streamResource

func lookupStreamResource(name string) (*streamResource, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, def := range streamResources {
		if def.Name == name {
			return def, true
		}
		for _, alias := range def.Aliases {
			if alias == name {
				return def, true
			}
		}
	}
	return nil, false
}

func streamResourceByName(name string) *streamResource {
	def, ok := lookupStreamResource(name)
	if !ok {
		panic("unknown stream resource " + name)
	}
	return def
}

// runStreamResource dispatches `hubfly stream <resource> [<sub>] …`. With no
// known subcommand the arguments go to list.
func runStreamResource(def *streamResource, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, _ = fmt.Fprintln(rtOut, "usage:\n"+def.Usage)
		return nil
	}
	sub, rest := args[0], args[1:]
	if extra, ok := def.Extras[sub]; ok {
		return extra(def, rest)
	}
	switch sub {
	case "list", "ls":
		return streamResourceList(def, rest)
	case "get", "show", "info":
		return streamResourceGet(def, rest)
	case "create", "add":
		if def.Create {
			return streamResourceWrite(def, "create", rest)
		}
	case "update", "set":
		if def.Update {
			return streamResourceWrite(def, "update", rest)
		}
	case "delete", "rm", "remove":
		if def.Delete {
			return streamResourceDelete(def, rest)
		}
	default:
		if !strings.HasPrefix(sub, "-") && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			return fmt.Errorf("unknown stream %s command %q\nusage:\n%s", def.Name, sub, def.Usage)
		}
		if list, ok := def.Extras["list"]; ok {
			return list(def, args)
		}
		return streamResourceList(def, args)
	}
	return fmt.Errorf("stream %s has no %s command\nusage:\n%s", def.Name, sub, def.Usage)
}

// ---------------------------------------------------------------------------
// Requests and resolution
// ---------------------------------------------------------------------------

// streamDecode converts a decoded JSON value into a typed value.
func streamDecode(value any, out any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, out)
}

// streamItems accepts a plain array or an {items: […]} page.
func streamItems(payload any) []map[string]any {
	var raw []any
	switch typed := payload.(type) {
	case []any:
		raw = typed
	case map[string]any:
		raw, _ = typed["items"].([]any)
	}
	items := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		if item, ok := entry.(map[string]any); ok {
			items = append(items, item)
		}
	}
	return items
}

func fetchStreamList(target streamTarget, def *streamResource, query url.Values) ([]map[string]any, error) {
	payload, err := streamGetRaw(target.token, target.instance, def.Path, query)
	if err != nil {
		return nil, streamAPIError(err)
	}
	return streamItems(payload), nil
}

// streamResolver turns resource ids or names into ids, listing each kind once.
type streamResolver struct {
	target streamTarget
	lists  map[string][]map[string]any
}

func newStreamResolver(target streamTarget) *streamResolver {
	return &streamResolver{target: target, lists: map[string][]map[string]any{}}
}

func (r *streamResolver) list(def *streamResource) ([]map[string]any, error) {
	if items, ok := r.lists[def.Name]; ok {
		return items, nil
	}
	items, err := fetchStreamList(r.target, def, nil)
	if err != nil {
		return nil, err
	}
	r.lists[def.Name] = items
	return items, nil
}

// find returns the listed resource whose id is ref, else the one whose name
// field matches ref case-insensitively.
func (r *streamResolver) find(def *streamResource, ref string) (map[string]any, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("missing %s id or name", def.Noun)
	}
	items, err := r.list(def)
	if err != nil {
		return nil, err
	}
	var byName []map[string]any
	for _, item := range items {
		if streamString(item, "id") == ref {
			return item, nil
		}
		if strings.EqualFold(streamString(item, def.NameField), ref) {
			byName = append(byName, item)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return nil, fmt.Errorf("%s '%s' not found on stream server '%s' (list them with: hubfly stream %s list %s)", def.Noun, terminalSafe(ref), r.target.instance.Name, def.Name, r.target.instance.Name)
	}
	ids := []string{}
	for _, item := range byName {
		ids = append(ids, streamString(item, "id"))
	}
	return nil, fmt.Errorf("%s name '%s' is ambiguous (%s); use the id", def.Noun, terminalSafe(ref), strings.Join(ids, ", "))
}

// id resolves ref to an id. Refs that carry the resource's id prefix are
// used as they are (no request).
func (r *streamResolver) id(def *streamResource, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if def.IDPrefix != "" && strings.HasPrefix(ref, def.IDPrefix) {
		return ref, nil
	}
	item, err := r.find(def, ref)
	if err != nil {
		return "", err
	}
	return streamString(item, "id"), nil
}

// get returns the full resource: GET {Path}/{id} when the route exists.
func (r *streamResolver) get(def *streamResource, ref string) (map[string]any, error) {
	if !def.HasGet {
		return r.find(def, ref)
	}
	id, err := r.id(def, ref)
	if err != nil {
		return nil, err
	}
	var item map[string]any
	if err := streamCall(r.target.token, r.target.instance, http.MethodGet, def.Path+"/"+url.PathEscape(id), nil, nil, &item); err != nil {
		return nil, streamAPIError(err)
	}
	return item, nil
}

// streamSourceRef parses "<kind>:<ref>" (kinds input, channel, mixer,
// playlist) into a {type, ref} object with the ref resolved to an id.
func (r *streamResolver) sourceRef(value string, kinds ...string) (map[string]any, error) {
	kind, ref, ok := strings.Cut(strings.TrimSpace(value), ":")
	kind = strings.ToLower(kind)
	allowed := false
	for _, candidate := range kinds {
		allowed = allowed || candidate == kind
	}
	if !ok || !allowed || strings.TrimSpace(ref) == "" {
		return nil, fmt.Errorf("invalid source %q (use %s:<id|name>)", value, strings.Join(kinds, ":<id|name>, "))
	}
	id, err := r.id(streamResourceByName(kind+"s"), ref)
	if err != nil {
		return nil, err
	}
	return map[string]any{"type": kind, "ref": id}, nil
}

func validStreamSource(value string, kinds ...string) error {
	if value == "" {
		return nil
	}
	kind, ref, ok := strings.Cut(strings.TrimSpace(value), ":")
	for _, candidate := range kinds {
		if ok && strings.EqualFold(kind, candidate) && strings.TrimSpace(ref) != "" {
			return nil
		}
	}
	return fmt.Errorf("invalid source %q (use %s:<id|name>)", value, strings.Join(kinds, ":<id|name>, "))
}

// streamString reads a dotted path of strings and numbers.
func streamString(item map[string]any, path string) string {
	var value any = item
	for _, key := range strings.Split(path, ".") {
		object, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		value = object[key]
	}
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	}
	return streamText(value)
}

func streamCol(header, path string) streamColumn {
	return streamColumn{Header: header, Value: func(item map[string]any) string {
		value := streamString(item, path)
		if value == "" {
			return "-"
		}
		return terminalSafe(value)
	}}
}

func streamColFunc(header string, value func(item map[string]any) string) streamColumn {
	return streamColumn{Header: header, Value: value}
}

func streamOnOff(item map[string]any, key string) string {
	if value, ok := item[key].(bool); ok {
		if value {
			return "on"
		}
		return "off"
	}
	return "-"
}

func streamListOf(item map[string]any, key string) string {
	values, _ := item[key].([]any)
	if len(values) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, streamText(value))
	}
	return strings.Join(parts, ",")
}

// ---------------------------------------------------------------------------
// Generic commands
// ---------------------------------------------------------------------------

func streamResourceList(def *streamResource, args []string) error {
	usage := fmt.Sprintf("hubfly stream %s [list] <server> [--project <id|name>] [--json]", def.Name)
	fs := newRealtimeFlags("stream " + def.Name)
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
	items, err := fetchStreamList(target, def, nil)
	if err != nil {
		return err
	}
	if *asJSON {
		return streamPrintJSON(items)
	}
	return writeStreamTable(def, target, items)
}

func writeStreamTable(def *streamResource, target streamTarget, items []map[string]any) error {
	if len(items) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No %ss on stream server '%s'.\n", def.Noun, target.instance.Name)
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	headers := make([]string, 0, len(def.Columns))
	for _, column := range def.Columns {
		headers = append(headers, column.Header)
	}
	_, _ = fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, raw := range items {
		item, _ := redactStreamSecrets(raw).(map[string]any)
		cells := make([]string, 0, len(def.Columns))
		for _, column := range def.Columns {
			cells = append(cells, column.Value(item))
		}
		_, _ = fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	return tw.Flush()
}

func streamResourceGet(def *streamResource, args []string) error {
	usage := fmt.Sprintf("hubfly stream %s get <server> <%s> [--project <id|name>] [--json]", def.Name, def.Noun)
	fs := newRealtimeFlags("stream " + def.Name + " get")
	projectRef := fs.String("project", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 {
		return dbdUsageError(usage)
	}
	target, err := resolveStreamTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	item, err := newStreamResolver(target).get(def, positionals[1])
	if err != nil {
		return err
	}
	if *asJSON {
		return streamPrintJSON(item)
	}
	writeStreamObject(rtOut, redactStreamSecrets(item), "")
	return nil
}

// writeStreamObject prints a resource as indented "key: value" lines, keys
// sorted, nested objects indented, lists of scalars joined.
func writeStreamObject(w io.Writer, value any, indent string) {
	object, ok := value.(map[string]any)
	if !ok {
		_, _ = fmt.Fprintf(w, "%s%s\n", indent, streamText(value))
		return
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, key := range keys {
		switch child := object[key].(type) {
		case map[string]any:
			_ = tw.Flush()
			if len(child) == 0 {
				_, _ = fmt.Fprintf(w, "%s%s: -\n", indent, terminalSafe(key))
				continue
			}
			_, _ = fmt.Fprintf(w, "%s%s:\n", indent, terminalSafe(key))
			writeStreamObject(w, child, indent+"  ")
		case []any:
			scalars := true
			for _, entry := range child {
				if _, isObject := entry.(map[string]any); isObject {
					scalars = false
				}
			}
			if scalars {
				text := "-"
				if len(child) > 0 {
					text = streamListOf(object, key)
				}
				_, _ = fmt.Fprintf(tw, "%s%s:\t%s\n", indent, terminalSafe(key), text)
				continue
			}
			_ = tw.Flush()
			_, _ = fmt.Fprintf(w, "%s%s:\n", indent, terminalSafe(key))
			for index, entry := range child {
				_, _ = fmt.Fprintf(w, "%s  [%d]\n", indent, index)
				writeStreamObject(w, entry, indent+"    ")
			}
		default:
			_, _ = fmt.Fprintf(tw, "%s%s:\t%s\n", indent, terminalSafe(key), streamText(child))
		}
	}
	_ = tw.Flush()
}

// readStreamSpecFile reads a JSON object from a file or stdin ("-").
func readStreamSpecFile(path string) (map[string]any, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(rtStdin)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("read --file: %w", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("--file must hold a JSON object: %v", err)
	}
	return body, nil
}

// streamFlagsSet counts the flags given, apart from the listed ones.
func streamFlagsSet(fs *flag.FlagSet, except ...string) int {
	skip := map[string]bool{}
	for _, name := range except {
		skip[name] = true
	}
	count := 0
	fs.Visit(func(f *flag.Flag) {
		if !skip[f.Name] {
			count++
		}
	})
	return count
}

func streamResourceWrite(def *streamResource, mode string, args []string) error {
	usage := def.CreateUsage
	if mode == "update" {
		usage = def.UpdateUsage
	}
	fs := newRealtimeFlags("stream " + def.Name + " " + mode)
	projectRef := fs.String("project", "", "")
	asJSON := fs.Bool("json", false, "")
	file := fs.String("file", "", "")
	spec := streamSpec{}
	if def.Define != nil {
		spec = def.Define(fs, mode)
	}
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	name := ""
	switch {
	case mode == "update" && len(positionals) == 2:
	case mode == "create" && def.CreateArg == "" && len(positionals) == 1:
	case mode == "create" && def.CreateArg != "" && len(positionals) == 2:
		name = strings.TrimSpace(positionals[1])
	case mode == "create" && def.CreateArg != "" && len(positionals) == 1 && *file != "":
	default:
		return dbdUsageError(usage)
	}
	if mode == "update" && streamFlagsSet(fs, "project", "json") == 0 {
		return fmt.Errorf("nothing to update\nusage: %s", usage)
	}
	if spec.validate != nil {
		if err := spec.validate(mode); err != nil {
			return fmt.Errorf("%v\nusage: %s", err, usage)
		}
	}
	body := map[string]any{}
	if *file != "" {
		if body, err = readStreamSpecFile(*file); err != nil {
			return err
		}
	}
	target, err := resolveStreamTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	build := &streamBuild{mode: mode, target: target, res: newStreamResolver(target), name: name, body: body}
	path := def.Path + "/create"
	method := http.MethodPost
	if mode == "update" {
		current, getErr := build.res.get(def, positionals[1])
		if getErr != nil {
			return getErr
		}
		build.current = current
		path = def.Path + "/" + url.PathEscape(streamString(current, "id")) + "/update"
		method = http.MethodPut
	}
	if name != "" && def.NameField != "" {
		body[def.NameField] = name
	}
	if spec.build != nil {
		if err := spec.build(build); err != nil {
			return err
		}
	}
	if mode == "update" {
		if _, given := body["revision"]; !given {
			body["revision"] = build.current["revision"]
		}
	}
	var result map[string]any
	if err := streamCall(target.token, target.instance, method, path, nil, body, &result); err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return streamPrintOnceJSON(result, def.Once...)
	}
	item, _ := result[def.ResultKey].(map[string]any)
	if item == nil {
		item = result
	}
	label := streamString(item, def.NameField)
	verb := "Created"
	if mode == "update" {
		verb = "Updated"
	}
	_, _ = fmt.Fprintf(rtOut, "%s %s '%s' (%s).\n", verb, def.Noun, terminalSafe(label), streamString(item, "id"))
	writeStreamSync(result)
	if mode == "create" && def.Once != nil {
		writeStreamOnce(def, target, result)
	}
	return nil
}

// streamPrintOnceJSON prints a response masked, except the fields that hold
// a secret shown once (new keys, playback key and webhook secrets).
func streamPrintOnceJSON(result map[string]any, once ...string) error {
	masked, _ := redactStreamSecrets(result).(map[string]any)
	for _, key := range once {
		if value, ok := result[key]; ok {
			masked[key] = value
		}
	}
	return rtPrintJSON(masked)
}

// writeStreamSync reports the state sync of a write: warnings and errors
// from the node, or that it applies in the background.
func writeStreamSync(result map[string]any) {
	sync, _ := result["sync"].(map[string]any)
	if sync == nil {
		return
	}
	if text := streamString(sync, "error"); text != "" {
		_, _ = fmt.Fprintf(rtErr, "Warning: the stream node has not applied it yet: %s\n", terminalSafe(redactStreamSecretText(text)))
	} else if streamString(sync, "status") == "pending" {
		_, _ = fmt.Fprintln(rtErr, "The stream node applies the change in the background.")
	}
	warnings, _ := sync["warnings"].([]any)
	for _, raw := range warnings {
		warning, _ := raw.(map[string]any)
		_, _ = fmt.Fprintf(rtErr, "Warning: %s (%s)\n", terminalSafe(redactStreamSecretText(streamString(warning, "message"))), streamString(warning, "code"))
	}
}

// writeStreamOnce prints the secrets of a create response that are shown
// only now.
func writeStreamOnce(def *streamResource, target streamTarget, result map[string]any) {
	switch def.Name {
	case "inputs":
		keys, _ := result["keys"].(map[string]any)
		input, _ := result["input"].(map[string]any)
		if len(keys) == 0 {
			return
		}
		_, _ = fmt.Fprintf(rtOut, "\nThe stream key is shown only now; reveal it later with:\n  hubfly stream inputs keys reveal %s %s\n\n", target.instance.Name, streamString(input, "id"))
		if backup := streamString(keys, "backup"); backup != "" {
			_, _ = fmt.Fprintf(rtOut, "Backup key:\t%s\n", backup)
		}
		host := streamHostOf(streamString(input, "connection.rtmps.server"))
		if host == "" {
			if server, err := fetchStreamServer(target.token, target.instance); err == nil {
				host = server.Host
			}
		}
		if host != "" {
			writeStreamOBSSettings(rtOut, host, streamString(keys, "primary"))
		} else {
			_, _ = fmt.Fprintf(rtOut, "Primary key:\t%s\n", streamString(keys, "primary"))
		}
	case "playback-keys", "webhooks":
		secret := streamString(result, "secret")
		if secret == "" {
			return
		}
		_, _ = fmt.Fprintf(rtOut, "\nSecret (shown only now, store it safely):\n  %s\n", secret)
		if def.Name == "webhooks" {
			_, _ = fmt.Fprintln(rtOut, "Verify the HubFly-Signature header of every delivery with it.")
		} else {
			_, _ = fmt.Fprintln(rtOut, "Sign viewer tokens on your backend with it (SDK signPlaybackToken).")
		}
	case "domains":
		domain, _ := result["domain"].(map[string]any)
		cname := streamString(result, "cnameTarget")
		_, _ = fmt.Fprintf(rtOut, "\nAdd this DNS record, then run: hubfly stream domains verify %s %s\n  %s CNAME %s\n",
			target.instance.Name, streamString(domain, "id"), terminalSafe(streamString(domain, "hostname")), terminalSafe(cname))
	}
}

func streamHostOf(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func streamResourceDelete(def *streamResource, args []string) error {
	usage := fmt.Sprintf("hubfly stream %s delete <server> <%s> [--project <id|name>] [--yes]", def.Name, def.Noun)
	extra := ""
	fs := newRealtimeFlags("stream " + def.Name + " delete")
	projectRef := fs.String("project", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	asJSON := fs.Bool("json", false, "")
	deleteObject := false
	removeVariables := false
	switch def.Name {
	case "recordings":
		fs.BoolVar(&deleteObject, "delete-object", false, "")
		usage = "hubfly stream recordings delete <server> <recording> [--delete-object] [--project <id|name>] [--yes]"
	case "bindings":
		fs.BoolVar(&removeVariables, "remove-variables", false, "")
		usage = "hubfly stream bindings delete <server> <binding> [--remove-variables] [--project <id|name>] [--yes]"
	}
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 {
		return dbdUsageError(usage)
	}
	target, err := resolveStreamTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	item, err := newStreamResolver(target).find(def, positionals[1])
	if err != nil {
		return err
	}
	id := streamString(item, "id")
	label := terminalSafe(streamString(item, def.NameField))
	query := url.Values{}
	if deleteObject {
		query.Set("deleteObject", "true")
		extra = " and its object in Storage"
	}
	if removeVariables {
		query.Set("removeVariables", "true")
		extra = " and its variables in the bound project"
	}
	question := fmt.Sprintf("Delete %s '%s' (%s)%s from stream server '%s'? %s", def.Noun, label, id, extra, target.instance.Name, def.DeleteNote)
	confirmed, err := dbdConfirm(*yes, strings.TrimSpace(question), "delete the "+def.Noun)
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled.")
		return nil
	}
	var result map[string]any
	if err := streamCall(target.token, target.instance, http.MethodDelete, def.Path+"/"+url.PathEscape(id)+"/delete", query, nil, &result); err != nil {
		return streamAPIError(err)
	}
	if *asJSON {
		return streamPrintJSON(result)
	}
	_, _ = fmt.Fprintf(rtOut, "Deleted %s '%s'.\n", def.Noun, label)
	writeStreamSync(result)
	return nil
}

// streamParseTarget is the common prelude of resource subcommands: it
// registers --project, parses args, checks the positional count, runs
// validate (before any request) and resolves the server.
func streamParseTarget(fs *flag.FlagSet, args []string, usage string, count int, validate func(positionals []string) error) (streamTarget, []string, error) {
	projectRef := fs.String("project", "", "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return streamTarget{}, nil, err
	}
	if len(positionals) != count {
		return streamTarget{}, nil, dbdUsageError(usage)
	}
	if validate != nil {
		if err := validate(positionals); err != nil {
			return streamTarget{}, nil, fmt.Errorf("%v\nusage: %s", err, usage)
		}
	}
	target, err := resolveStreamTarget(positionals[0], *projectRef)
	if err != nil {
		return streamTarget{}, nil, err
	}
	return target, positionals, nil
}
