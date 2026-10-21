package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
)

// `hubfly db export|exports|import`: manual exports to a file and imports
// from one. Files move through presigned object storage URLs; the CLI never
// sends them through the Dashboard. Exports are kept for 7 days.

const (
	dbdTransferTimeout = 2 * time.Hour
	// dbdTransferHTTPTimeout bounds one presigned upload or download.
	dbdTransferHTTPTimeout = 6 * time.Hour
)

// dbdFormatsFor are the export/import formats of an engine; the first is the
// default (as in the Dashboard: PostgreSQL native = pg_dump -Fc, sql = plain
// SQL; MySQL and libSQL export gzip-compressed SQL).
func dbdFormatsFor(engine string) []string {
	if engine == "postgresql" {
		return []string{"native", "sql"}
	}
	return []string{"sql"}
}

func dbdCheckFormat(target dbdTarget, format string) (string, error) {
	formats := dbdFormatsFor(target.engine())
	if format == "" {
		return formats[0], nil
	}
	format = strings.ToLower(strings.TrimSpace(format))
	for _, candidate := range formats {
		if candidate == format {
			return format, nil
		}
	}
	if format == "native" {
		return "", fmt.Errorf("invalid --format native: '%s' is a %s instance, which uses the sql format (native is pg_dump's custom format, PostgreSQL only)", target.name(), target.engineLabel())
	}
	return "", fmt.Errorf("invalid --format %q (use %s)", format, strings.Join(formats, " or "))
}

// dbdTransferDatabase is the logical database an export or import uses
// (libSQL has a single database and takes none).
func dbdTransferDatabase(target dbdTarget, database string) string {
	if target.engine() == "libsql" {
		return ""
	}
	if database != "" {
		return database
	}
	return target.defaultDatabase()
}

func dbdDatabasePhrase(target dbdTarget, database string) string {
	if database == "" {
		return fmt.Sprintf("'%s'", target.name())
	}
	return fmt.Sprintf("database '%s' of '%s'", database, target.name())
}

// ---------------------------------------------------------------------------
// export
// ---------------------------------------------------------------------------

func dbExportFlow(args []string) error {
	const usage = "hubfly db export <db> [--database <name>] [--format native|sql] [--output <file|->] [--no-wait] [--project <id|name>]"
	fs := newRealtimeFlags("db export")
	projectRef := fs.String("project", "", "")
	database := fs.String("database", "", "")
	fs.StringVar(database, "d", "", "")
	format := fs.String("format", "", "")
	output := fs.String("output", "", "")
	fs.StringVar(output, "o", "", "")
	noWait := fs.Bool("no-wait", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	outputPath := strings.TrimSpace(*output)
	if outputPath != "" && outputPath != "-" && !*noWait {
		if err := dbdRefuseOverwrite(outputPath); err != nil {
			return err
		}
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("export"); err != nil {
		return err
	}
	chosenFormat, err := dbdCheckFormat(target, *format)
	if err != nil {
		return err
	}
	databaseName := dbdTransferDatabase(target, strings.TrimSpace(*database))
	created, err := createDBDExport(target.token, target.instance.ProjectID, target.instance.ID, databaseName, chosenFormat)
	if err != nil {
		return target.apiError(err)
	}
	exportID := created.Export.ID
	if *noWait {
		_, _ = fmt.Fprintf(rtOut, "Export %s of %s started (operation %s).\n", exportID, dbdDatabasePhrase(target, databaseName), created.OperationID)
		_, _ = fmt.Fprintf(rtOut, "Download it when it is ready with: hubfly db exports download %s %s\n", target.name(), exportID)
		return nil
	}
	if _, err := waitForManagedOperation(target.token, target.instance, created.OperationID, "Exporting "+dbdDatabasePhrase(target, databaseName), dbdTransferTimeout, false, false); err != nil {
		return target.apiError(err)
	}
	path, size, err := dbdDownloadExport(target, exportID, outputPath)
	if err != nil {
		return fmt.Errorf("%w\nThe export is kept for 7 days; retry the download with: hubfly db exports download %s %s", err, target.name(), exportID)
	}
	if path != "-" {
		_, _ = fmt.Fprintf(rtOut, "Exported %s to %s (%s).\n", dbdDatabasePhrase(target, databaseName), path, formatBytes(size))
	}
	return nil
}

// dbdRefuseOverwrite keeps the export commands from replacing a file.
func dbdRefuseOverwrite(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists; choose another file with --output", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// dbdDownloadExport asks for a download link and streams the file to
// outputPath ("" = the export's file name in the current directory, "-" =
// stdout). It returns the path written and the byte count.
func dbdDownloadExport(target dbdTarget, exportID, outputPath string) (string, int64, error) {
	link, err := createDBDExportDownload(target.token, target.instance.ProjectID, target.instance.ID, exportID)
	if err != nil {
		return "", 0, target.apiError(err)
	}
	if outputPath == "" {
		outputPath = filepath.Base(strings.TrimSpace(link.FileName))
		if outputPath == "" || outputPath == "." || outputPath == string(filepath.Separator) {
			outputPath = exportID + ".dump"
		}
	}
	if outputPath != "-" {
		if err := dbdRefuseOverwrite(outputPath); err != nil {
			return "", 0, err
		}
	}
	size, err := dbdDownloadToFile(link.URL, outputPath)
	return outputPath, size, err
}

// dbdDownloadToFile streams a presigned URL to path ("-" = stdout) through a
// temporary file, so an interrupted download leaves no partial export behind.
func dbdDownloadToFile(rawURL, path string) (int64, error) {
	client := &http.Client{Timeout: dbdTransferHTTPTimeout}
	resp, err := client.Get(rawURL)
	if err != nil {
		return 0, fmt.Errorf("download the export: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("download the export: storage answered %s", resp.Status)
	}
	if path == "-" {
		return io.Copy(rtOut, resp.Body)
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.part")
	if err != nil {
		return 0, fmt.Errorf("write %s: %w", path, err)
	}
	tempPath := temp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = temp.Close()
			_ = os.Remove(tempPath)
		}
	}()
	progress := newUploadProgress("Downloading", resp.ContentLength)
	progress.writer = rtErr
	progress.Start()
	written, copyErr := io.Copy(temp, progress.Wrap(resp.Body))
	progress.Finish()
	if copyErr != nil {
		return written, fmt.Errorf("download the export: %w", copyErr)
	}
	if resp.ContentLength > 0 && written != resp.ContentLength {
		return written, fmt.Errorf("download the export: received %s of %s", formatBytes(written), formatBytes(resp.ContentLength))
	}
	if err := temp.Close(); err != nil {
		return written, fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		return written, fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return written, fmt.Errorf("write %s: %w", path, err)
	}
	keep = true
	return written, nil
}

// ---------------------------------------------------------------------------
// exports list / download
// ---------------------------------------------------------------------------

func dbdExportsListFlow(args []string) error {
	const usage = "hubfly db exports [list] <db> [--project <id|name>] [--json]"
	fs := newRealtimeFlags("db exports list")
	projectRef := fs.String("project", "", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return dbdUsageError(usage)
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("export"); err != nil {
		return err
	}
	raw, err := fetchDBDTransfersRaw(target.token, target.instance.ProjectID, target.instance.ID)
	if err != nil {
		return target.apiError(err)
	}
	if *asJSON {
		return rtPrintJSON(raw)
	}
	var transfers dbdTransfers
	if err := dbdDecode(raw, &transfers); err != nil {
		return fmt.Errorf("read the exports: %w", err)
	}
	if !transfers.Available {
		_, _ = fmt.Fprintf(rtOut, "Exports and imports are not configured for the region of '%s' yet.\n", target.name())
		if len(transfers.Items) == 0 {
			return nil
		}
	}
	if len(transfers.Items) == 0 {
		_, _ = fmt.Fprintf(rtOut, "No exports or imports on '%s'. Create one with: hubfly db export %s\n", target.name(), target.name())
		return nil
	}
	tw := tabwriter.NewWriter(rtOut, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tKIND\tSTATUS\tDATABASE\tFORMAT\tSIZE\tCREATED\tEXPIRES")
	failures := []dbdTransfer{}
	for _, item := range transfers.Items {
		size := "-"
		if item.SizeBytes != nil {
			size = formatBytes(*item.SizeBytes)
		}
		expires := "-"
		if item.Kind == "export" {
			expires = dbdTimePtr(item.ExpiresAt)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", item.ID, item.Kind, item.Status, dbdText(item.DatabaseName, "-"),
			item.Format, size, dbdTime(item.CreatedAt), expires)
		if item.Status == "failed" && item.Error != nil && item.Error.Message != "" {
			failures = append(failures, item)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, item := range failures {
		_, _ = fmt.Fprintf(rtOut, "%s failed: %s\n", item.ID, item.Error.Message)
	}
	return nil
}

func dbdExportsDownloadFlow(args []string) error {
	const usage = "hubfly db exports download <db> <exportId> [--output <file|->] [--project <id|name>]"
	fs := newRealtimeFlags("db exports download")
	projectRef := fs.String("project", "", "")
	output := fs.String("output", "", "")
	fs.StringVar(output, "o", "", "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 || strings.TrimSpace(positionals[1]) == "" {
		return dbdUsageError(usage)
	}
	outputPath := strings.TrimSpace(*output)
	if outputPath != "" && outputPath != "-" {
		if err := dbdRefuseOverwrite(outputPath); err != nil {
			return err
		}
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("export"); err != nil {
		return err
	}
	path, size, err := dbdDownloadExport(target, strings.TrimSpace(positionals[1]), outputPath)
	if err != nil {
		return err
	}
	if path != "-" {
		_, _ = fmt.Fprintf(rtOut, "Downloaded %s (%s).\n", path, formatBytes(size))
	}
	return nil
}

// ---------------------------------------------------------------------------
// import
// ---------------------------------------------------------------------------

// dbdDetectImportFormat picks the format from the file name like the
// Dashboard: .dump and .backup are PostgreSQL native dumps (pg_dump -Fc),
// .sql and .sql.gz are SQL. Other names need --format on PostgreSQL and are
// SQL elsewhere (MySQL and libSQL only import SQL, optionally gzipped).
func dbdDetectImportFormat(target dbdTarget, fileName string) (string, error) {
	lower := strings.ToLower(fileName)
	switch {
	case strings.HasSuffix(lower, ".sql"), strings.HasSuffix(lower, ".sql.gz"):
		return "sql", nil
	case strings.HasSuffix(lower, ".dump"), strings.HasSuffix(lower, ".backup"):
		if target.engine() != "postgresql" {
			return "", fmt.Errorf("%s looks like a PostgreSQL native dump; '%s' is a %s instance and imports SQL (.sql or .sql.gz)", fileName, target.name(), target.engineLabel())
		}
		return "native", nil
	}
	if target.engine() == "postgresql" {
		return "", fmt.Errorf("cannot tell the format of %s from its name; pass --format native (pg_dump -Fc) or --format sql", fileName)
	}
	return "sql", nil
}

func dbImportFlow(args []string) error {
	const usage = "hubfly db import <db> <file> [--database <name>] [--format native|sql] [--yes] [--no-wait] [--project <id|name>]"
	fs := newRealtimeFlags("db import")
	projectRef := fs.String("project", "", "")
	database := fs.String("database", "", "")
	fs.StringVar(database, "d", "", "")
	format := fs.String("format", "", "")
	yes := fs.Bool("yes", false, "")
	fs.BoolVar(yes, "y", false, "")
	noWait := fs.Bool("no-wait", false, "")
	positionals, err := parseRealtimeArgs(fs, args, usage)
	if err != nil {
		return err
	}
	if len(positionals) != 2 || strings.TrimSpace(positionals[1]) == "" {
		return dbdUsageError(usage)
	}
	filePath := positionals[1]
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", filePath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory; pass a dump or SQL file", filePath)
	}
	if info.Size() == 0 {
		return fmt.Errorf("%s is empty", filePath)
	}
	target, err := resolveDBDTarget(positionals[0], *projectRef)
	if err != nil {
		return err
	}
	if err := target.require("import"); err != nil {
		return err
	}
	chosenFormat := ""
	if strings.TrimSpace(*format) != "" {
		chosenFormat, err = dbdCheckFormat(target, *format)
	} else {
		chosenFormat, err = dbdDetectImportFormat(target, filepath.Base(filePath))
	}
	if err != nil {
		return err
	}
	if info.Size() > 5*1024*1024*1024 {
		return fmt.Errorf("%s is %s; imports are limited to 5 GB per file", filePath, formatBytes(info.Size()))
	}
	databaseName := dbdTransferDatabase(target, strings.TrimSpace(*database))
	question := fmt.Sprintf("Import %s (%s, %s) into %s? It runs against the existing data (nothing is emptied first) and stops at the first error; statements before it may stay applied.",
		filepath.Base(filePath), formatBytes(info.Size()), chosenFormat, dbdDatabasePhrase(target, databaseName))
	confirmed, err := dbdConfirm(*yes, question, "import into the database")
	if err != nil {
		return err
	}
	if !confirmed {
		_, _ = fmt.Fprintln(rtOut, "Cancelled.")
		return nil
	}
	body := map[string]any{"fileName": filepath.Base(filePath), "sizeBytes": info.Size(), "format": chosenFormat}
	if databaseName != "" {
		body["databaseName"] = databaseName
	}
	upload, err := createDBDImportUpload(target.token, target.instance.ProjectID, target.instance.ID, body)
	if err != nil {
		return target.apiError(err)
	}
	if upload.MaxBytes > 0 && info.Size() > upload.MaxBytes {
		return fmt.Errorf("%s is %s; imports are limited to %s per file", filePath, formatBytes(info.Size()), formatBytes(upload.MaxBytes))
	}
	if err := dbdUploadFile(upload.UploadURL, filePath, info.Size()); err != nil {
		return err
	}
	operationID, err := startDBDImport(target.token, target.instance.ProjectID, target.instance.ID, upload.ImportID)
	if err != nil {
		return target.apiError(err)
	}
	if *noWait {
		_, _ = fmt.Fprintf(rtOut, "Import %s into %s started (operation %s).\n", upload.ImportID, dbdDatabasePhrase(target, databaseName), operationID)
		_, _ = fmt.Fprintf(rtOut, "Follow it with: hubfly db operations %s\n", target.name())
		return nil
	}
	if _, err := waitForManagedOperation(target.token, target.instance, operationID, "Importing into "+dbdDatabasePhrase(target, databaseName), dbdTransferTimeout, false, false); err != nil {
		return target.apiError(err)
	}
	_, _ = fmt.Fprintf(rtOut, "Imported %s into %s.\n", filepath.Base(filePath), dbdDatabasePhrase(target, databaseName))
	return nil
}

// dbdUploadFile PUTs a file to a presigned URL with a progress line on stderr.
func dbdUploadFile(rawURL, path string, size int64) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	progress := newUploadProgress("Uploading", size)
	progress.writer = rtErr
	body := progress.Wrap(file)
	defer func() { _ = body.Close() }()
	req, err := http.NewRequest(http.MethodPut, rawURL, body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	progress.Start()
	resp, err := (&http.Client{Timeout: dbdTransferHTTPTimeout}).Do(req)
	progress.Finish()
	if err != nil {
		return fmt.Errorf("upload %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("upload %s: storage answered %s %s", path, resp.Status, strings.TrimSpace(dbdStorageErrorCode(detail)))
	}
	return nil
}

// dbdStorageErrorCode pulls <Code> out of an S3 XML error body.
func dbdStorageErrorCode(body []byte) string {
	text := string(body)
	start := strings.Index(text, "<Code>")
	end := strings.Index(text, "</Code>")
	if start < 0 || end <= start {
		return ""
	}
	return "(" + text[start+len("<Code>"):end] + ")"
}
