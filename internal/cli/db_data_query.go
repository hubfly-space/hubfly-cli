package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// `hubfly db query`: runs SQL through the Dashboard's SQL console (the same
// endpoint as the web console). Read-only unless --write.

const dbdQueryUsage = "hubfly db query <db> [<sql>|--file <path|->] [--database <name>] [--write] [--max-rows <n>] [--timeout <duration>] [--format table|json|csv] [--project <id|name>]"

const (
	// dbdMaxSQLBytes is the Dashboard's limit for one console request.
	dbdMaxSQLBytes = 100 * 1024
	// dbdMaxRows and dbdMaxTimeout are the Dashboard's caps; its defaults
	// are 500 rows and 30 s.
	dbdMaxRows    = 5000
	dbdMaxTimeout = 60 * time.Second
)

type dbdQueryOptions struct {
	ProjectRef string
	Ref        string
	SQL        string
	Database   string
	Write      bool
	MaxRows    int
	Timeout    time.Duration
	Format     string
}

func parseDBDQueryOptions(args []string) (dbdQueryOptions, error) {
	var opts dbdQueryOptions
	fs := newRealtimeFlags("db query")
	fs.StringVar(&opts.ProjectRef, "project", "", "")
	file := fs.String("file", "", "")
	fs.StringVar(file, "f", "", "")
	fs.StringVar(&opts.Database, "database", "", "")
	fs.StringVar(&opts.Database, "d", "", "")
	fs.BoolVar(&opts.Write, "write", false, "")
	fs.IntVar(&opts.MaxRows, "max-rows", 0, "")
	timeout := fs.String("timeout", "", "")
	var format rtOptString
	fs.Var(&format, "format", "")
	asJSON := fs.Bool("json", false, "")
	positionals, err := parseRealtimeArgs(fs, args, dbdQueryUsage)
	if err != nil {
		return opts, err
	}
	if len(positionals) == 0 {
		return opts, dbdUsageError(dbdQueryUsage)
	}
	if len(positionals) > 2 {
		return opts, fmt.Errorf("invalid arguments: pass the SQL as one quoted argument\nusage: %s", dbdQueryUsage)
	}
	opts.Ref = positionals[0]

	opts.Format = "table"
	if format.set {
		opts.Format = strings.ToLower(strings.TrimSpace(format.value))
	}
	if *asJSON {
		if format.set && opts.Format != "json" {
			return opts, errors.New("invalid arguments: use --json or --format, not both")
		}
		opts.Format = "json"
	}
	switch opts.Format {
	case "table", "json", "csv":
	default:
		return opts, fmt.Errorf("invalid --format %q (use table, json or csv)", format.value)
	}
	if opts.MaxRows < 0 || opts.MaxRows > dbdMaxRows {
		return opts, fmt.Errorf("invalid --max-rows %d (use 1-%d)", opts.MaxRows, dbdMaxRows)
	}
	if strings.TrimSpace(*timeout) != "" {
		parsed, parseErr := dbdParseTimeout(*timeout)
		if parseErr != nil {
			return opts, parseErr
		}
		opts.Timeout = parsed
	}

	sqlArg, hasSQLArg := "", len(positionals) == 2
	if hasSQLArg {
		sqlArg = positionals[1]
	}
	opts.SQL, err = readDBDQuerySQL(sqlArg, hasSQLArg, strings.TrimSpace(*file))
	if err != nil {
		return opts, err
	}
	return opts, nil
}

// dbdParseTimeout accepts Go durations ("15s", "1m") or plain seconds.
func dbdParseTimeout(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	parsed, err := time.ParseDuration(value)
	if err != nil {
		seconds, convErr := strconv.ParseFloat(value, 64)
		if convErr != nil {
			return 0, fmt.Errorf("invalid --timeout %q (use values like 15s or 1m)", value)
		}
		parsed = time.Duration(seconds * float64(time.Second))
	}
	if parsed < time.Second || parsed > dbdMaxTimeout {
		return 0, fmt.Errorf("invalid --timeout %q (use 1s to 60s)", value)
	}
	return parsed, nil
}

// readDBDQuerySQL takes the SQL from the argument, --file (a path or "-") or
// piped stdin.
func readDBDQuerySQL(sqlArg string, hasSQLArg bool, file string) (string, error) {
	var sql string
	switch {
	case hasSQLArg && file != "":
		return "", errors.New("invalid arguments: give the SQL as an argument or with --file, not both")
	case hasSQLArg:
		sql = sqlArg
	case file != "" && file != "-":
		handle, err := os.Open(file)
		if err != nil {
			return "", fmt.Errorf("read SQL: %w", err)
		}
		defer func() { _ = handle.Close() }()
		sql, err = readDBDLimitedSQL(handle, file)
		if err != nil {
			return "", err
		}
	case file == "-" || !rtStdinIsTerminal():
		var err error
		sql, err = readDBDLimitedSQL(rtStdin, "stdin")
		if err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("invalid arguments: pass the SQL as an argument, with --file <path|-> or on stdin\nusage: %s", dbdQueryUsage)
	}
	if strings.TrimSpace(sql) == "" {
		return "", errors.New("invalid arguments: the SQL is empty")
	}
	if len(sql) > dbdMaxSQLBytes {
		return "", errors.New("the SQL is larger than 100 KB, the console limit; run large scripts with: hubfly db import <db> <file>")
	}
	return sql, nil
}

func readDBDLimitedSQL(reader io.Reader, source string) (string, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, dbdMaxSQLBytes+1))
	if err != nil {
		return "", fmt.Errorf("read SQL from %s: %w", source, err)
	}
	return string(payload), nil
}

func dbQueryFlow(args []string) error {
	opts, err := parseDBDQueryOptions(args)
	if err != nil {
		return err
	}
	target, err := resolveDBDTarget(opts.Ref, opts.ProjectRef)
	if err != nil {
		return err
	}
	if err := target.require("query"); err != nil {
		return err
	}
	body := map[string]any{"sql": opts.SQL, "readOnly": !opts.Write}
	if opts.Database != "" {
		body["database"] = opts.Database
	}
	if opts.MaxRows > 0 {
		body["maxRows"] = opts.MaxRows
	}
	if opts.Timeout > 0 {
		body["timeoutMs"] = opts.Timeout.Milliseconds()
	}
	raw, err := postDBDQuery(target.token, target.instance.ProjectID, target.instance.ID, body, dbdQueryHTTPTimeout(opts.Timeout))
	if err != nil {
		return target.apiError(err)
	}
	var result dbdQueryResult
	if err := dbdDecode(raw, &result); err != nil {
		return fmt.Errorf("read the query result: %w", err)
	}

	switch opts.Format {
	case "json":
		if err := rtPrintJSON(raw); err != nil {
			return err
		}
	case "csv":
		if err := writeDBDQueryCSV(rtOut, result); err != nil {
			return err
		}
		writeDBDQueryNotices(rtErr, result.Notices)
	default:
		writeDBDQueryTables(rtOut, result, opts.MaxRows)
		writeDBDQueryNotices(rtErr, result.Notices)
	}

	failed := dbdFailedStatement(result)
	if failed == nil {
		return nil
	}
	if opts.Format != "json" {
		writeDBDStatementError(rtErr, *failed)
	}
	message := fmt.Sprintf("the query failed at statement %d", failed.Index+1)
	if !opts.Write && dbdLooksReadOnlyError(failed.Error) {
		message += "\nThe query ran read-only (the default); re-run with --write to change data."
	}
	return errors.New(message)
}

func dbdFailedStatement(result dbdQueryResult) *dbdQueryStatement {
	for index := range result.Statements {
		if result.Statements[index].Status == "error" {
			return &result.Statements[index]
		}
	}
	return nil
}

// dbdLooksReadOnlyError recognizes the errors a write gets in a read-only
// run: SQLSTATE 25006 (PostgreSQL and MySQL read-only transactions) and the
// libSQL console's READ_ONLY/FORBIDDEN/ABORTED codes.
func dbdLooksReadOnlyError(queryErr *dbdQueryError) bool {
	if queryErr == nil {
		return false
	}
	if queryErr.Code != nil {
		switch strings.ToUpper(*queryErr.Code) {
		case "25006", "READ_ONLY", "FORBIDDEN", "ABORTED":
			return true
		}
	}
	message := strings.ToLower(queryErr.Message)
	return strings.Contains(message, "read-only") || strings.Contains(message, "read only")
}

// dbdCellText makes a value fit on one table line.
var dbdCellReplacer = strings.NewReplacer("\r\n", `\n`, "\n", `\n`, "\r", `\r`, "\t", `\t`)

func dbdCellText(value *string) string {
	if value == nil {
		return "NULL"
	}
	return dbdCellReplacer.Replace(*value)
}

// writeDBDQueryTables prints each statement's result like psql: a table per
// result set with a row count, or the command for statements without rows.
func writeDBDQueryTables(w io.Writer, result dbdQueryResult, maxRows int) {
	printed := 0
	for _, statement := range result.Statements {
		if statement.Status == "error" {
			continue
		}
		if printed > 0 {
			_, _ = fmt.Fprintln(w)
		}
		printed++
		if len(statement.Columns) == 0 {
			_, _ = fmt.Fprintln(w, dbdCommandSummary(statement))
			continue
		}
		writeDBDResultTable(w, statement.Columns, statement.Rows)
		shown := int64(len(statement.Rows))
		switch {
		case statement.Truncated && statement.RowCount != nil && *statement.RowCount > shown:
			_, _ = fmt.Fprintf(w, "(showing the first %s of %s rows; %s)\n", formatThousands64(shown), formatThousands64(*statement.RowCount), dbdTruncationAdvice(maxRows))
		case statement.Truncated:
			_, _ = fmt.Fprintf(w, "(showing the first %s; the result was truncated; %s)\n", dbdPlural(shown, "row", "rows"), dbdTruncationAdvice(maxRows))
		default:
			_, _ = fmt.Fprintf(w, "(%s)\n", dbdPlural(shown, "row", "rows"))
		}
	}
}

func dbdTruncationAdvice(maxRows int) string {
	if maxRows >= dbdMaxRows {
		return "add a LIMIT or narrow the query"
	}
	return fmt.Sprintf("raise --max-rows (up to %d) or add a LIMIT", dbdMaxRows)
}

func dbdCommandSummary(statement dbdQueryStatement) string {
	command := "OK"
	if statement.Command != nil && strings.TrimSpace(*statement.Command) != "" {
		command = strings.TrimSpace(*statement.Command)
	}
	if statement.RowCount == nil {
		return command
	}
	return fmt.Sprintf("%s (%s)", command, dbdPlural(*statement.RowCount, "row", "rows"))
}

// writeDBDResultTable aligns the columns (numbers to the right) and
// underlines the header.
func writeDBDResultTable(w io.Writer, columns []dbdQueryColumn, rows [][]*string) {
	widths := make([]int, len(columns))
	for index, column := range columns {
		widths[index] = utf8.RuneCountInString(column.Name)
	}
	cells := make([][]string, len(rows))
	for rowIndex, row := range rows {
		cells[rowIndex] = make([]string, len(columns))
		for index := range columns {
			var value *string
			if index < len(row) {
				value = row[index]
			}
			text := dbdCellText(value)
			cells[rowIndex][index] = text
			if width := utf8.RuneCountInString(text); width > widths[index] {
				widths[index] = width
			}
		}
	}
	line := func(values []string, numeric func(int) bool) string {
		parts := make([]string, len(values))
		for index, value := range values {
			padding := strings.Repeat(" ", widths[index]-utf8.RuneCountInString(value))
			if numeric(index) {
				parts[index] = padding + value
			} else {
				parts[index] = value + padding
			}
		}
		return strings.TrimRight(strings.Join(parts, "  "), " ")
	}
	headers := make([]string, len(columns))
	rules := make([]string, len(columns))
	for index, column := range columns {
		headers[index] = column.Name
		rules[index] = strings.Repeat("-", widths[index])
	}
	never := func(int) bool { return false }
	_, _ = fmt.Fprintln(w, line(headers, never))
	_, _ = fmt.Fprintln(w, line(rules, never))
	isNumber := func(index int) bool { return columns[index].Kind == "number" }
	for _, row := range cells {
		_, _ = fmt.Fprintln(w, line(row, isNumber))
	}
}

// writeDBDQueryCSV prints the one result set as CSV (RFC 4180, like psql
// --csv): NULL is an empty field and an empty string is "".
func writeDBDQueryCSV(w io.Writer, result dbdQueryResult) error {
	var sets []dbdQueryStatement
	for _, statement := range result.Statements {
		if statement.Status == "ok" && len(statement.Columns) > 0 {
			sets = append(sets, statement)
		}
	}
	if len(sets) > 1 {
		return fmt.Errorf("--format csv prints one result set, but the SQL returned %d (the statements ran); run them separately or use --format json", len(sets))
	}
	if len(sets) == 0 {
		return nil
	}
	set := sets[0]
	header := make([]*string, len(set.Columns))
	for index := range set.Columns {
		header[index] = &set.Columns[index].Name
	}
	_, _ = io.WriteString(w, dbdCSVLine(header))
	for _, row := range set.Rows {
		_, _ = io.WriteString(w, dbdCSVLine(row))
	}
	if set.Truncated {
		_, _ = fmt.Fprintf(rtErr, "Warning: the result was truncated to %s; raise --max-rows (up to %d) or add a LIMIT.\n", dbdPlural(int64(len(set.Rows)), "row", "rows"), dbdMaxRows)
	}
	return nil
}

func dbdCSVLine(values []*string) string {
	fields := make([]string, len(values))
	for index, value := range values {
		switch {
		case value == nil:
			fields[index] = ""
		case *value == "":
			fields[index] = `""`
		case strings.ContainsAny(*value, ",\"\r\n") || strings.HasPrefix(*value, " ") || strings.HasSuffix(*value, " "):
			fields[index] = `"` + strings.ReplaceAll(*value, `"`, `""`) + `"`
		default:
			fields[index] = *value
		}
	}
	return strings.Join(fields, ",") + "\n"
}

func writeDBDQueryNotices(w io.Writer, notices []dbdQueryNotice) {
	for _, notice := range notices {
		severity := strings.ToUpper(strings.TrimSpace(notice.Severity))
		if severity == "" {
			severity = "NOTICE"
		}
		line := fmt.Sprintf("%s: %s", severity, notice.Message)
		if notice.Code != nil && *notice.Code != "" {
			line += fmt.Sprintf(" (code %s)", *notice.Code)
		}
		_, _ = fmt.Fprintln(w, line)
	}
}

func writeDBDStatementError(w io.Writer, statement dbdQueryStatement) {
	queryErr := statement.Error
	if queryErr == nil {
		_, _ = fmt.Fprintf(w, "ERROR (statement %d): the statement failed\n", statement.Index+1)
		return
	}
	_, _ = fmt.Fprintf(w, "ERROR (statement %d): %s\n", statement.Index+1, queryErr.Message)
	details := []string{}
	if queryErr.Code != nil && *queryErr.Code != "" {
		details = append(details, "code "+*queryErr.Code)
	}
	if queryErr.Position != nil {
		details = append(details, fmt.Sprintf("position %d", *queryErr.Position))
	}
	if len(details) > 0 {
		_, _ = fmt.Fprintf(w, "  %s\n", strings.Join(details, ", "))
	}
	if queryErr.Detail != nil && *queryErr.Detail != "" {
		_, _ = fmt.Fprintf(w, "  Detail: %s\n", *queryErr.Detail)
	}
	if queryErr.Hint != nil && *queryErr.Hint != "" {
		_, _ = fmt.Fprintf(w, "  Hint: %s\n", *queryErr.Hint)
	}
}
