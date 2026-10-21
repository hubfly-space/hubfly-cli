package cli

import (
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// `hubfly storage assets|publish|share|unpublish|encode|media`: managed
// assets (public or protected delivery on the region's stream host) and HLS
// video encoding.

// storageAssetMediaURL is the direct file URL of an asset: asset_url itself
// is a viewer page, "/media" serves the file (hubfly-storage stream routes).
func storageAssetMediaURL(asset storageAsset) string {
	if asset.AssetURL == "" {
		return ""
	}
	return strings.TrimSuffix(asset.AssetURL, "/") + "/media"
}

// resolveStorageAsset finds an asset by id, URL id or "<bucket>/<key>".
func resolveStorageAsset(target storageTarget, ref string) (storageAsset, error) {
	assets, err := fetchStorageAssets(target.token, target.project.ID)
	if err != nil {
		return storageAsset{}, storageAPIError(err, "")
	}
	ref = strings.TrimSpace(ref)
	if bucket, key, found := strings.Cut(ref, "/"); found {
		for _, asset := range assets {
			if asset.Bucket == bucket && asset.ObjectKey == key {
				return asset, nil
			}
		}
		return storageAsset{}, fmt.Errorf("'%s' is not published; publish it with: hubfly storage publish %s --project %s", ref, ref, target.project.ID)
	}
	for _, asset := range assets {
		if asset.ID == ref || asset.URLID == ref {
			return asset, nil
		}
	}
	return storageAsset{}, fmt.Errorf("asset '%s' not found (list them with: hubfly storage assets --project %s)", ref, target.project.ID)
}

func runStorageAssetsGroup(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return storageAssetsListFlow(args)
	}
	switch args[0] {
	case "list", "ls":
		return storageAssetsListFlow(args[1:])
	case "help":
		printStorageUsage()
		return nil
	default:
		return fmt.Errorf("unknown storage assets command %q (run: hubfly storage help)", args[0])
	}
}

func storageAssetsListFlow(args []string) error {
	const usage = "hubfly storage assets [list] [--bucket <bucket>] [--project <p>] [--json]"
	fs := newStorageFlags("assets list")
	projectRef := storageProjectFlag(fs)
	bucket := fs.String("bucket", "", "")
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
	all, err := fetchStorageAssets(target.token, target.project.ID)
	if err != nil {
		return storageAPIError(err, "")
	}
	assets := []storageAsset{}
	for _, asset := range all {
		if *bucket == "" || asset.Bucket == *bucket {
			assets = append(assets, asset)
		}
	}
	if *asJSON {
		return rtPrintJSON(assets)
	}
	if len(assets) == 0 {
		_, _ = fmt.Fprintln(rtOut, "No published assets. Publish an object with: hubfly storage publish <bucket>/<key>")
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tOBJECT\tTYPE\tSIZE\tACCESS\tSTATE\tHLS\tURL")
	for _, asset := range assets {
		hls := "-"
		if asset.HLSReady {
			hls = "ready"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s/%s\t%s\t%s\t%s\t%s\t%s\t%s\n", asset.ID, asset.Bucket, asset.ObjectKey, valueOrDash(asset.FileType),
			formatBytes(asset.SizeBytes), valueOrDash(asset.AccessMode), valueOrDash(asset.State), hls, valueOrDash(asset.AssetURL))
	}
	return tw.Flush()
}

func storagePublishFlow(args []string) error {
	const usage = "hubfly storage publish <bucket>/<key> [--protected] [--title <text>] [--project <p>] [--json]"
	fs := newStorageFlags("publish")
	projectRef := storageProjectFlag(fs)
	protected := fs.Bool("protected", false, "")
	title := fs.String("title", "", "")
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
	if existing, findErr := resolveStorageAsset(target, bucket+"/"+key); findErr == nil {
		return fmt.Errorf("%s/%s is already published as asset %s (%s, %s)", bucket, key, existing.ID, existing.AccessMode, valueOrDash(existing.AssetURL))
	}
	created, err := createStorageAsset(target.token, target.project.ID, bucket, key, strings.TrimSpace(*title))
	if err != nil {
		return storageAPIError(err, fmt.Sprintf("object '%s/%s' not found", bucket, key))
	}
	asset := created.Asset
	if asset.AssetURL == "" {
		asset.AssetURL, asset.PlayerURL = created.AssetURL, created.PlayerURL
	}
	// The Dashboard always creates assets protected.
	if !*protected {
		updated, updateErr := updateStorageAsset(target.token, target.project.ID, asset.ID, map[string]any{"accessMode": "public"})
		if updateErr != nil {
			return fmt.Errorf("asset %s was published as protected, but making it public failed: %w\nRemove it with 'hubfly storage unpublish %s' (this also deletes the object) or change its access in the Dashboard",
				asset.ID, storageAPIError(updateErr, ""), asset.ID)
		}
		if updated.AssetURL == "" {
			updated.AssetURL, updated.PlayerURL = asset.AssetURL, asset.PlayerURL
		}
		asset = updated
	}
	mediaURL := storageAssetMediaURL(asset)
	if *asJSON {
		out := map[string]any{"asset": asset, "assetUrl": asset.AssetURL, "mediaUrl": mediaURL, "playerUrl": asset.PlayerURL}
		if *protected {
			out["accessKey"] = created.AccessKey
		}
		return rtPrintJSON(out)
	}
	_, _ = fmt.Fprintf(rtOut, "Published %s/%s as a %s asset (%s).\n", bucket, key, asset.AccessMode, asset.ID)
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  Page:\t%s\n", valueOrDash(asset.AssetURL))
	_, _ = fmt.Fprintf(tw, "  File:\t%s\n", valueOrDash(mediaURL))
	if asset.FileType == "video" || asset.FileType == "audio" {
		_, _ = fmt.Fprintf(tw, "  Player:\t%s\n", valueOrDash(asset.PlayerURL))
	}
	if *protected {
		_, _ = fmt.Fprintf(tw, "  Access key:\t%s\n", created.AccessKey)
	}
	_ = tw.Flush()
	if *protected {
		_, _ = fmt.Fprintln(rtOut, "Protected: add ?key=<access key> to the URLs (or send it as the X-Asset-Key header), or create a temporary link with: hubfly storage share "+asset.ID)
	}
	if asset.FileType == "video" && !asset.HLSReady {
		_, _ = fmt.Fprintf(rtOut, "Encode it for streaming with: hubfly storage encode %s/%s\n", bucket, key)
	}
	return nil
}

func storageShareFlow(args []string) error {
	const usage = "hubfly storage share <bucket>/<key>|<assetId> [--expires <duration>] [--project <p>] [--json]"
	fs := newStorageFlags("share")
	projectRef := storageProjectFlag(fs)
	expires := fs.String("expires", "1h", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	ttl, err := parseRealtimeDuration(*expires)
	if err != nil {
		return fmt.Errorf("invalid --expires: %v", err)
	}
	if ttl < 300 || ttl > 86400 {
		return fmt.Errorf("invalid --expires %q: use 5m to 24h", *expires)
	}
	target, err := resolveStorageTarget(*projectRef, !*asJSON)
	if err != nil {
		return err
	}
	asset, err := resolveStorageAsset(target, positionals[0])
	if err != nil {
		return err
	}
	share, err := createStorageShare(target.token, target.project.ID, asset.ID, ttl)
	if err != nil {
		return storageAPIError(err, fmt.Sprintf("asset '%s' not found", asset.ID))
	}
	if *asJSON {
		return rtPrintJSON(share)
	}
	link := share.PlayerURL
	if link == "" {
		link = share.MediaURL
	}
	_, _ = fmt.Fprintln(rtOut, link)
	if share.ManifestURL != "" {
		_, _ = fmt.Fprintf(rtErr, "HLS manifest: %s\n", share.ManifestURL)
	} else if share.MediaURL != "" && share.MediaURL != link {
		_, _ = fmt.Fprintf(rtErr, "File: %s\n", share.MediaURL)
	}
	_, _ = fmt.Fprintf(rtErr, "Expires %s. Anyone with the link can view %s/%s until then.\n", storageExpiry(share.ExpiresAt), asset.Bucket, asset.ObjectKey)
	return nil
}

func storageUnpublishFlow(args []string) error {
	const usage = "hubfly storage unpublish <assetId|bucket/key> [--yes] [--project <p>]"
	fs := newStorageFlags("unpublish")
	projectRef := storageProjectFlag(fs)
	yes := storageYesFlag(fs)
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: %s", usage)
	}
	target, err := resolveStorageTarget(*projectRef, true)
	if err != nil {
		return err
	}
	asset, err := resolveStorageAsset(target, positionals[0])
	if err != nil {
		return err
	}
	// Deleting an asset in HubFly Storage also deletes its source object,
	// its HLS renditions and its cached image variants.
	if !*yes {
		_, _ = fmt.Fprintf(rtOut, "Unpublishing asset %s deletes the object %s/%s itself, its video renditions and image variants, and stops all its links.\n", asset.ID, asset.Bucket, asset.ObjectKey)
		_, _ = fmt.Fprintf(rtOut, "Download it first if you want to keep it: hubfly storage get %s/%s\n", asset.Bucket, asset.ObjectKey)
	}
	confirmed, err := storageConfirm(*yes, "Unpublish and delete it?", "unpublish it (the object is deleted too)")
	if err != nil || !confirmed {
		return err
	}
	if err := deleteStorageAsset(target.token, target.project.ID, asset.ID); err != nil {
		return storageAPIError(err, fmt.Sprintf("asset '%s' not found", asset.ID))
	}
	_, _ = fmt.Fprintf(rtOut, "Unpublished asset %s and deleted %s/%s.\n", asset.ID, asset.Bucket, asset.ObjectKey)
	return nil
}

// ---------------------------------------------------------------------------
// encode / media
// ---------------------------------------------------------------------------

func storageEncodeFlow(args []string) error {
	const usage = "hubfly storage encode <bucket>/<key> [--max-height 360|480|720|1080] [--wait] [--project <p>] [--json]"
	fs := newStorageFlags("encode")
	projectRef := storageProjectFlag(fs)
	maxHeightText := fs.String("max-height", "720", "")
	wait := fs.Bool("wait", false, "")
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
	maxHeight, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(*maxHeightText), "p"))
	if err != nil || (maxHeight != 360 && maxHeight != 480 && maxHeight != 720 && maxHeight != 1080) {
		return fmt.Errorf("invalid --max-height %q (expected 360, 480, 720 or 1080)", *maxHeightText)
	}
	target, err := resolveStorageTarget(*projectRef, !*asJSON)
	if err != nil {
		return err
	}
	job, err := createStorageMediaJob(target.token, target.project.ID, bucket, key, maxHeight)
	if err != nil {
		return storageAPIError(err, fmt.Sprintf("object '%s/%s' not found", bucket, key))
	}
	if !*wait {
		if *asJSON {
			return rtPrintJSON(job)
		}
		_, _ = fmt.Fprintf(rtOut, "Encoding %s/%s up to %dp (job %s, %s).\n", bucket, key, maxHeight, job.ID, valueOrDash(job.State))
		_, _ = fmt.Fprintf(rtOut, "Follow it with: hubfly storage media --project %s\n", target.project.ID)
		return nil
	}
	finished, err := waitForStorageMediaJob(target, job.ID, 2*time.Hour, *asJSON)
	if err != nil {
		return err
	}
	if finished.State == "failed" {
		reason := "unknown error"
		if finished.Error != nil && *finished.Error != "" {
			reason = *finished.Error
		}
		return fmt.Errorf("encoding job %s failed: %s", finished.ID, reason)
	}
	playback, err := createStorageMediaPlayback(target.token, target.project.ID, finished.ID)
	if err != nil {
		return storageAPIError(err, "")
	}
	if *asJSON {
		return rtPrintJSON(map[string]any{"job": finished, "playback": playback})
	}
	_, _ = fmt.Fprintf(rtOut, "Encoded %s/%s up to %dp (job %s).\n", bucket, key, maxHeight, finished.ID)
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  Player:\t%s\n", valueOrDash(playback.PlayerURL))
	if playback.ManifestURL != "" {
		_, _ = fmt.Fprintf(tw, "  HLS manifest:\t%s\n", playback.ManifestURL)
	}
	_, _ = fmt.Fprintf(tw, "  Expires:\t%s\n", storageExpiry(playback.ExpiresAt))
	return tw.Flush()
}

// waitForStorageMediaJob polls the media job list until the job is ready or
// failed.
func waitForStorageMediaJob(target storageTarget, jobID string, timeout time.Duration, quiet bool) (storageMediaJob, error) {
	spinner := newRealtimeSpinner(quiet)
	defer spinner.stop()
	deadline := time.Now().Add(timeout)
	for {
		jobs, err := fetchStorageMediaJobs(target.token, target.project.ID)
		if err != nil {
			debugf("media jobs of %s: %v", target.project.ID, err)
		}
		for _, job := range jobs {
			if job.ID != jobID {
				continue
			}
			if job.State == "ready" || job.State == "failed" {
				return job, nil
			}
			spinner.update(fmt.Sprintf("Encoding (%s, %d%%)", job.State, job.Progress), quiet)
		}
		if time.Now().After(deadline) {
			return storageMediaJob{}, fmt.Errorf("encoding job %s is still running; follow it with: hubfly storage media --project %s", jobID, target.project.ID)
		}
		time.Sleep(rtPollInterval)
	}
}

func runStorageMediaGroup(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return storageMediaListFlow(args)
	}
	switch args[0] {
	case "list", "ls", "jobs":
		return storageMediaListFlow(args[1:])
	case "help":
		printStorageUsage()
		return nil
	default:
		return fmt.Errorf("unknown storage media command %q (run: hubfly storage help)", args[0])
	}
}

func storageMediaListFlow(args []string) error {
	const usage = "hubfly storage media [list] [--project <p>] [--json]"
	fs := newStorageFlags("media list")
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
	jobs, err := fetchStorageMediaJobs(target.token, target.project.ID)
	if err != nil {
		return storageAPIError(err, "")
	}
	if *asJSON {
		if jobs == nil {
			jobs = []storageMediaJob{}
		}
		return rtPrintJSON(jobs)
	}
	if len(jobs) == 0 {
		_, _ = fmt.Fprintln(rtOut, "No media jobs. Encode a video with: hubfly storage encode <bucket>/<key>")
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "JOB\tSOURCE\tMAX\tSTATE\tPROGRESS\tUPDATED\tERROR")
	for _, job := range jobs {
		errText := "-"
		if job.Error != nil && *job.Error != "" {
			errText = *job.Error
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s/%s\t%dp\t%s\t%d%%\t%s\t%s\n", job.ID, job.Bucket, job.SourceKey, job.MaxHeight, job.State, job.Progress, storageTime(job.UpdatedAt), errText)
	}
	return tw.Flush()
}
