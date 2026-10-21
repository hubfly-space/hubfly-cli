package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
)

// `hubfly storage buckets|ls|put|get|rm|url`.

// storageListPageSize is the largest page the storage region returns.
const storageListPageSize = 200

// storageSkipSuffix sorts after every valid UTF-8 key continuation, so a
// continuation token of "<dir>/" + storageSkipSuffix skips the rest of that
// "directory" (the region lists keys greater than the token).
const storageSkipSuffix = "\U0010FFFF"

// ---------------------------------------------------------------------------
// buckets
// ---------------------------------------------------------------------------

func runStorageBucketsGroup(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return storageBucketsListFlow(args)
	}
	command, rest := args[0], args[1:]
	switch command {
	case "list", "ls":
		return storageBucketsListFlow(rest)
	case "create", "new":
		return storageBucketsCreateFlow(rest)
	case "update", "set":
		return storageBucketsUpdateFlow(rest)
	case "delete", "rm", "remove":
		return storageBucketsDeleteFlow(rest)
	case "help":
		printStorageUsage()
		return nil
	default:
		return fmt.Errorf("unknown storage buckets command %q (run: hubfly storage help)", command)
	}
}

func printStorageBuckets(buckets []storageBucket, target project) error {
	if len(buckets) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No buckets yet. Create one with: hubfly storage buckets create <bucket> --project %s\n", target.ID)
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "BUCKET\tVERSIONING\tIMAGE RETENTION\tCREATED")
	for _, bucket := range buckets {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", bucket.Name, valueOrDash(bucket.Versioning), storageRetention(bucket.ImageRetentionSeconds), storageTime(bucket.CreatedAt))
	}
	return tw.Flush()
}

func storageBucketsListFlow(args []string) error {
	const usage = "hubfly storage buckets [list] [--project <p>] [--json]"
	fs := newStorageFlags("buckets list")
	projectRef := storageProjectFlag(fs)
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) > 0 {
		return fmt.Errorf("unexpected argument: %s\nusage: %s", positionals[0], usage)
	}
	target, err := resolveStorageTarget(*projectRef, !*asJSON)
	if err != nil {
		return err
	}
	buckets, err := fetchStorageBuckets(target.token, target.project.ID)
	if err != nil {
		return storageAPIError(err, "")
	}
	if *asJSON {
		if buckets == nil {
			buckets = []storageBucket{}
		}
		return rtPrintJSON(buckets)
	}
	return printStorageBuckets(buckets, target.project)
}

func normalizeStorageVersioning(value string) (string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(value)); mode {
	case "enabled", "suspended", "disabled":
		return mode, nil
	case "on":
		return "enabled", nil
	case "off":
		return "disabled", nil
	default:
		return "", fmt.Errorf("invalid --versioning %q (expected enabled, suspended or disabled)", value)
	}
}

// parseStorageImageRetention accepts "keep"/"0" or a duration from 1h to 365d.
func parseStorageImageRetention(value string) (int64, error) {
	if strings.EqualFold(strings.TrimSpace(value), "keep") {
		return 0, nil
	}
	seconds, err := parseRealtimeDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid --image-retention: %v", err)
	}
	if seconds != 0 && (seconds < 3600 || seconds > 365*86400) {
		return 0, fmt.Errorf("invalid --image-retention %q: use keep (or 0) or a duration from 1h to 365d", value)
	}
	return int64(seconds), nil
}

func storageBucketsCreateFlow(args []string) error {
	const usage = "hubfly storage buckets create <bucket> [--versioning enabled|suspended|disabled] [--project <p>] [--json]"
	fs := newStorageFlags("buckets create")
	projectRef := storageProjectFlag(fs)
	versioningText := fs.String("versioning", "disabled", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	name := strings.TrimSpace(positionals[0])
	if err := validateStorageBucketName(name); err != nil {
		return err
	}
	versioning, err := normalizeStorageVersioning(*versioningText)
	if err != nil {
		return err
	}
	target, err := resolveStorageTarget(*projectRef, !*asJSON)
	if err != nil {
		return err
	}
	bucket, err := createStorageBucket(target.token, target.project.ID, name)
	if err != nil {
		return storageAPIError(err, "")
	}
	if versioning != "disabled" {
		updated, updateErr := updateStorageBucket(target.token, target.project.ID, name, versioning, nil)
		if updateErr != nil {
			return fmt.Errorf("bucket '%s' was created but versioning could not be set: %w\nRetry with: hubfly storage buckets update %s --versioning %s --project %s",
				name, storageAPIError(updateErr, ""), name, versioning, target.project.ID)
		}
		bucket = updated
	}
	if *asJSON {
		return rtPrintJSON(bucket)
	}
	_, _ = fmt.Fprintf(rtOut, "Created bucket '%s' in '%s' (versioning %s).\n", bucket.Name, target.project.Name, valueOrDash(bucket.Versioning))
	_, _ = fmt.Fprintf(rtOut, "Upload with: hubfly storage put <file> %s/ --project %s\n", bucket.Name, target.project.ID)
	return nil
}

func findStorageBucket(target storageTarget, name string) (storageBucket, error) {
	buckets, err := fetchStorageBuckets(target.token, target.project.ID)
	if err != nil {
		return storageBucket{}, storageAPIError(err, "")
	}
	for _, bucket := range buckets {
		if bucket.Name == name {
			return bucket, nil
		}
	}
	return storageBucket{}, fmt.Errorf("bucket '%s' not found in '%s' (list them with: hubfly storage buckets --project %s)", name, target.project.Name, target.project.ID)
}

func storageBucketsUpdateFlow(args []string) error {
	const usage = "hubfly storage buckets update <bucket> [--versioning <mode>] [--image-retention <duration>] [--project <p>]"
	fs := newStorageFlags("buckets update")
	projectRef := storageProjectFlag(fs)
	var versioningText, retentionText rtOptString
	fs.Var(&versioningText, "versioning", "")
	fs.Var(&retentionText, "image-retention", "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 || (!versioningText.set && !retentionText.set) {
		return fmt.Errorf("usage: %s", usage)
	}
	name := strings.TrimSpace(positionals[0])
	var retention *int64
	if retentionText.set {
		seconds, parseErr := parseStorageImageRetention(retentionText.value)
		if parseErr != nil {
			return parseErr
		}
		retention = &seconds
	}
	requested := ""
	if versioningText.set {
		if requested, err = normalizeStorageVersioning(versioningText.value); err != nil {
			return err
		}
	}
	target, err := resolveStorageTarget(*projectRef, true)
	if err != nil {
		return err
	}
	current, err := findStorageBucket(target, name)
	if err != nil {
		return err
	}
	versioning := current.Versioning
	if requested != "" {
		if requested == "disabled" && current.Versioning != "disabled" {
			return fmt.Errorf("invalid --versioning: versioning cannot be turned off once it was on; use --versioning suspended to stop keeping new versions")
		}
		versioning = requested
	}
	if versioning == "disabled" {
		if retention == nil {
			_, _ = fmt.Fprintf(rtOut, "Bucket '%s' already has versioning disabled; nothing to change.\n", name)
			return nil
		}
		// The storage region applies the versioning field first and only
		// accepts enabled or suspended there.
		return fmt.Errorf("the image retention of bucket '%s' can only be changed together with a versioning mode; add --versioning suspended (HubFly Storage treats new uploads and deletes the same as with versioning disabled) or --versioning enabled", name)
	}
	updated, err := updateStorageBucket(target.token, target.project.ID, name, versioning, retention)
	if err != nil {
		return storageAPIError(err, fmt.Sprintf("bucket '%s' not found", name))
	}
	_, _ = fmt.Fprintf(rtOut, "Updated bucket '%s': versioning %s, image retention %s.\n", updated.Name, valueOrDash(updated.Versioning), storageRetention(updated.ImageRetentionSeconds))
	return nil
}

func storageBucketsDeleteFlow(args []string) error {
	const usage = "hubfly storage buckets delete <bucket> [--yes] [--project <p>]"
	fs := newStorageFlags("buckets delete")
	projectRef := storageProjectFlag(fs)
	yes := storageYesFlag(fs)
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	name := strings.TrimSpace(positionals[0])
	target, err := resolveStorageTarget(*projectRef, true)
	if err != nil {
		return err
	}
	page, err := listStorageObjects(target.token, target.project.ID, storageObjectQuery{Bucket: name, PageSize: 1})
	if err != nil {
		return storageAPIError(err, fmt.Sprintf("bucket '%s' not found", name))
	}
	if len(page.Objects) > 0 {
		return fmt.Errorf("bucket '%s' is not empty; delete its objects first with: hubfly storage rm %s/ --recursive --project %s", name, name, target.project.ID)
	}
	confirmed, err := storageConfirm(*yes, fmt.Sprintf("Delete empty bucket '%s' from '%s'?", name, target.project.Name), "delete it")
	if err != nil || !confirmed {
		return err
	}
	if err := deleteStorageBucket(target.token, target.project.ID, name); err != nil {
		return storageAPIError(err, fmt.Sprintf("bucket '%s' not found", name))
	}
	_, _ = fmt.Fprintf(rtOut, "Deleted bucket '%s'.\n", name)
	return nil
}

// ---------------------------------------------------------------------------
// ls
// ---------------------------------------------------------------------------

// storageListing is the result of `hubfly storage ls` on a bucket.
type storageListing struct {
	Bucket    string          `json:"bucket"`
	Prefix    string          `json:"prefix"`
	Prefixes  []string        `json:"prefixes"`
	Objects   []storageObject `json:"objects"`
	Truncated bool            `json:"truncated"`
}

// listStorageEntries pages through a bucket. Without recursive it groups keys
// into "directories" at the next "/" after the prefix (the API returns keys
// flat) and skips past each directory it found. It stops after limit
// entries (directories plus objects); limit <= 0 means no limit.
func listStorageEntries(target storageTarget, bucket, prefix, search string, recursive bool, limit int) (storageListing, error) {
	listing := storageListing{Bucket: bucket, Prefix: prefix, Prefixes: []string{}, Objects: []storageObject{}}
	query := storageObjectQuery{Bucket: bucket, Prefix: prefix, PageSize: storageListPageSize, View: "raw"}
	if search != "" {
		// Only the managed view applies the search filter.
		query.Search, query.View, recursive = search, "managed", true
	}
	count := 0
	for {
		page, err := listStorageObjects(target.token, target.project.ID, query)
		if err != nil {
			return listing, storageAPIError(err, fmt.Sprintf("bucket '%s' not found", bucket))
		}
		skipTo := ""
		for _, object := range page.Objects {
			if limit > 0 && count >= limit {
				listing.Truncated = true
				return listing, nil
			}
			if !recursive {
				if index := strings.Index(object.Key[len(prefix):], "/"); index >= 0 {
					directory := object.Key[:len(prefix)+index+1]
					listing.Prefixes = append(listing.Prefixes, directory)
					count++
					skipTo = directory + storageSkipSuffix
					break
				}
			}
			listing.Objects = append(listing.Objects, object)
			count++
		}
		switch {
		case skipTo != "":
			query.ContinuationToken = skipTo
		case page.IsTruncated && page.NextToken != "":
			query.ContinuationToken = page.NextToken
		default:
			return listing, nil
		}
	}
}

func storageLsFlow(args []string) error {
	const usage = "hubfly storage ls [<bucket>[/<prefix>]] [--recursive] [--search <text>] [--limit <n>] [--project <p>] [--json]"
	fs := newStorageFlags("ls")
	projectRef := storageProjectFlag(fs)
	recursive := fs.Bool("recursive", false, "")
	fs.BoolVar(recursive, "r", false, "")
	search := fs.String("search", "", "")
	limit := fs.Int("limit", 1000, "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) > 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	if *limit < 0 {
		return errors.New("invalid --limit: use 0 (no limit) or a positive number")
	}
	if len(positionals) == 0 {
		if *recursive || *search != "" {
			return fmt.Errorf("invalid arguments: --recursive and --search need a bucket\nusage: %s", usage)
		}
		forwarded := []string{}
		if *projectRef != "" {
			forwarded = append(forwarded, "--project", *projectRef)
		}
		if *asJSON {
			forwarded = append(forwarded, "--json")
		}
		return storageBucketsListFlow(forwarded)
	}
	bucket, prefix, err := splitStoragePath(positionals[0])
	if err != nil {
		return err
	}
	target, err := resolveStorageTarget(*projectRef, !*asJSON)
	if err != nil {
		return err
	}
	listing, err := listStorageEntries(target, bucket, prefix, strings.TrimSpace(*search), *recursive, *limit)
	if err != nil {
		return err
	}
	if *asJSON {
		return rtPrintJSON(listing)
	}
	if len(listing.Prefixes) == 0 && len(listing.Objects) == 0 {
		where := "bucket '" + bucket + "'"
		if prefix != "" {
			where = "'" + bucket + "/" + prefix + "'"
		}
		_, _ = fmt.Fprintf(rtOut, "No objects in %s.\n", where)
		return nil
	}
	// Names are shown relative to the "directory" of the prefix, except in
	// recursive and search listings, which show full keys.
	base := ""
	if !*recursive && *search == "" {
		base = prefix[:strings.LastIndex(prefix, "/")+1]
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	for _, directory := range listing.Prefixes {
		_, _ = fmt.Fprintf(tw, "PRE\t\t%s\n", strings.TrimPrefix(directory, base))
	}
	for _, object := range listing.Objects {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", formatBytes(object.Size), storageTime(object.CreatedAt), strings.TrimPrefix(object.Key, base))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if listing.Truncated {
		_, _ = fmt.Fprintf(rtErr, "Showing the first %d entries; raise --limit (0 lists everything).\n", *limit)
	}
	return nil
}

// ---------------------------------------------------------------------------
// put / get / url
// ---------------------------------------------------------------------------

func storagePutFlow(args []string) error {
	const usage = "hubfly storage put <file|dir> <bucket>[/<key>] [--recursive] [--content-type <type>] [--project <p>]"
	fs := newStorageFlags("put")
	projectRef := storageProjectFlag(fs)
	recursive := fs.Bool("recursive", false, "")
	fs.BoolVar(recursive, "r", false, "")
	contentType := fs.String("content-type", "", "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 {
		return fmt.Errorf("usage: %s", usage)
	}
	localPath := positionals[0]
	bucket, key, err := splitStoragePath(positionals[1])
	if err != nil {
		return err
	}
	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	var items []storageUploadItem
	switch {
	case info.IsDir() && !*recursive:
		return fmt.Errorf("%s is a directory; pass --recursive to upload its files", localPath)
	case info.IsDir():
		var skipped []string
		items, skipped, err = storageWalkUploads(localPath, key)
		if err != nil {
			return err
		}
		for _, item := range skipped {
			_, _ = fmt.Fprintf(rtErr, "Skipping %s (not a regular file)\n", item)
		}
		if len(items) == 0 {
			return fmt.Errorf("%s has no files to upload", localPath)
		}
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file", localPath)
	default:
		items = []storageUploadItem{{LocalPath: localPath, Key: storageUploadKey(localPath, key), Size: info.Size()}}
	}
	for _, item := range items {
		if item.Size > storageMaxPutBytes {
			return storageTooLargeError(item.LocalPath, item.Size)
		}
	}
	target, err := resolveStorageTarget(*projectRef, true)
	if err != nil {
		return err
	}
	single := len(items) == 1
	upload := func(item storageUploadItem) error {
		kind, typeErr := storageContentType(item.LocalPath, *contentType)
		if typeErr != nil {
			return typeErr
		}
		session, sessionErr := createStorageUploadSession(target.token, target.project.ID, bucket, item.Key, kind)
		if sessionErr != nil {
			return storageAPIError(sessionErr, fmt.Sprintf("bucket '%s' not found", bucket))
		}
		if session.ContentType != "" {
			kind = session.ContentType
		}
		return storageUploadFile(session.UploadURL, item.LocalPath, kind, item.Size, !single)
	}
	if single {
		if err := upload(items[0]); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(rtOut, "Uploaded %s to %s/%s (%s).\n", items[0].LocalPath, bucket, items[0].Key, formatBytes(items[0].Size))
		return nil
	}
	var total int64
	for _, item := range items {
		total += item.Size
	}
	_, _ = fmt.Fprintf(rtErr, "Uploading %d files (%s) to %s/%s\n", len(items), formatBytes(total), bucket, key)
	var printMu sync.Mutex
	errs := storageParallel(len(items), 4, func(index int) error {
		item := items[index]
		uploadErr := upload(item)
		printMu.Lock()
		defer printMu.Unlock()
		if uploadErr != nil {
			_, _ = fmt.Fprintf(rtErr, "Failed %s: %v\n", item.LocalPath, uploadErr)
		} else {
			_, _ = fmt.Fprintf(rtOut, "Uploaded %s to %s/%s (%s)\n", item.LocalPath, bucket, item.Key, formatBytes(item.Size))
		}
		return uploadErr
	})
	failed := 0
	for _, uploadErr := range errs {
		if uploadErr != nil {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d uploads failed", failed, len(items))
	}
	_, _ = fmt.Fprintf(rtOut, "Uploaded %d files (%s).\n", len(items), formatBytes(total))
	return nil
}

func storageGetFlow(args []string) error {
	const usage = "hubfly storage get <bucket>/<key> [<file>|-] [--force] [--project <p>]"
	fs := newStorageFlags("get")
	projectRef := storageProjectFlag(fs)
	force := fs.Bool("force", false, "")
	fs.BoolVar(force, "f", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) < 1 || len(positionals) > 2 {
		return fmt.Errorf("usage: %s", usage)
	}
	bucket, key, err := splitStorageObjectPath(positionals[0])
	if err != nil {
		return err
	}
	dest := storageDownloadName(key)
	if len(positionals) == 2 {
		dest = positionals[1]
	}
	toStdout := dest == "-"
	if !toStdout {
		if info, statErr := os.Stat(dest); statErr == nil && info.IsDir() {
			dest = filepath.Join(dest, storageDownloadName(key))
		}
	}
	target, err := resolveStorageTarget(*projectRef, !toStdout)
	if err != nil {
		return err
	}
	download, err := createStorageDownload(target.token, target.project.ID, bucket, key)
	if err != nil {
		return storageAPIError(err, fmt.Sprintf("object '%s/%s' not found", bucket, key))
	}
	if toStdout {
		_, err = storageDownloadTo(download.DownloadURL, rtOut, "Downloading "+storageDownloadName(key), false)
		return err
	}
	written, err := storageDownloadFile(download.DownloadURL, dest, *force, false)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(rtOut, "Downloaded %s/%s to %s (%s).\n", bucket, key, dest, formatBytes(written))
	return nil
}

func storageURLFlow(args []string) error {
	const usage = "hubfly storage url <bucket>/<key> [--project <p>] [--json]"
	fs := newStorageFlags("url")
	projectRef := storageProjectFlag(fs)
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	bucket, key, err := splitStorageObjectPath(positionals[0])
	if err != nil {
		return err
	}
	target, err := resolveStorageTarget(*projectRef, !*asJSON)
	if err != nil {
		return err
	}
	download, err := createStorageDownload(target.token, target.project.ID, bucket, key)
	if err != nil {
		return storageAPIError(err, fmt.Sprintf("object '%s/%s' not found", bucket, key))
	}
	if *asJSON {
		return rtPrintJSON(map[string]string{"bucket": bucket, "key": key, "url": download.DownloadURL, "expiresAt": download.ExpiresAt})
	}
	_, _ = fmt.Fprintln(rtOut, download.DownloadURL)
	_, _ = fmt.Fprintf(rtErr, "Expires %s. Anyone with this link can download the object until then.\n", storageExpiry(download.ExpiresAt))
	return nil
}

// ---------------------------------------------------------------------------
// rm
// ---------------------------------------------------------------------------

func storageRmFlow(args []string) error {
	const usage = "hubfly storage rm <bucket>/<key>... [--recursive] [--yes] [--project <p>]"
	fs := newStorageFlags("rm")
	projectRef := storageProjectFlag(fs)
	recursive := fs.Bool("recursive", false, "")
	fs.BoolVar(recursive, "r", false, "")
	yes := storageYesFlag(fs)
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) == 0 {
		return fmt.Errorf("usage: %s", usage)
	}
	type storageRef struct{ bucket, key string }
	refs := make([]storageRef, 0, len(positionals))
	for _, value := range positionals {
		bucket, key, splitErr := splitStoragePath(value)
		if splitErr != nil {
			return splitErr
		}
		if !*recursive && (key == "" || strings.HasSuffix(key, "/")) {
			return fmt.Errorf("%s names a prefix, not an object; pass --recursive to delete everything under it", value)
		}
		refs = append(refs, storageRef{bucket, key})
	}
	target, err := resolveStorageTarget(*projectRef, true)
	if err != nil {
		return err
	}
	targets := []storageRef{}
	if *recursive {
		for _, ref := range refs {
			listing, listErr := listStorageEntries(target, ref.bucket, ref.key, "", true, 0)
			if listErr != nil {
				return listErr
			}
			for _, object := range listing.Objects {
				targets = append(targets, storageRef{ref.bucket, object.Key})
			}
		}
		if len(targets) == 0 {
			_, _ = fmt.Fprintln(rtOut, "No objects match; nothing to delete.")
			return nil
		}
	} else {
		targets = refs
	}
	if !*yes {
		_, _ = fmt.Fprintf(rtOut, "%d object(s) in '%s' will be deleted:\n", len(targets), target.project.Name)
		for index, item := range targets {
			if index == 10 {
				_, _ = fmt.Fprintf(rtOut, "  ... and %d more\n", len(targets)-10)
				break
			}
			_, _ = fmt.Fprintf(rtOut, "  %s/%s\n", item.bucket, item.key)
		}
	}
	confirmed, err := storageConfirm(*yes, fmt.Sprintf("Delete %d object(s)?", len(targets)), "delete them")
	if err != nil || !confirmed {
		return err
	}
	markers := 0
	var printMu sync.Mutex
	errs := storageParallel(len(targets), 4, func(index int) error {
		item := targets[index]
		result, deleteErr := deleteStorageObject(target.token, target.project.ID, item.bucket, item.key)
		printMu.Lock()
		defer printMu.Unlock()
		if deleteErr != nil {
			deleteErr = storageAPIError(deleteErr, fmt.Sprintf("object '%s/%s' not found", item.bucket, item.key))
			if len(targets) > 1 {
				_, _ = fmt.Fprintf(rtErr, "Failed %s/%s: %v\n", item.bucket, item.key, deleteErr)
			}
			return deleteErr
		}
		if result.DeleteMarker {
			markers++
		}
		if len(targets) > 1 {
			_, _ = fmt.Fprintf(rtOut, "Deleted %s/%s\n", item.bucket, item.key)
		}
		return nil
	})
	failed := 0
	for _, deleteErr := range errs {
		if deleteErr != nil {
			failed++
		}
	}
	if len(targets) == 1 && failed == 0 {
		_, _ = fmt.Fprintf(rtOut, "Deleted %s/%s.\n", targets[0].bucket, targets[0].key)
	}
	if markers > 0 {
		_, _ = fmt.Fprintln(rtOut, "Versioning is enabled: earlier versions are kept (behind a delete marker) and still count toward the quota.")
	}
	if failed > 0 {
		if len(targets) == 1 {
			return errs[0]
		}
		return fmt.Errorf("%d of %d deletions failed", failed, len(targets))
	}
	if len(targets) > 1 {
		_, _ = fmt.Fprintf(rtOut, "Deleted %d objects.\n", len(targets))
	}
	return nil
}
