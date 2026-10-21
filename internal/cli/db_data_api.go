package cli

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Managed Databases data and administration endpoints under
// /api/v1/projects/{projectId}/managed/instances/{instanceId}. Shapes follow
// the Dashboard's generated openapi.json. Commands that print the raw response
// with --json read it as json.RawMessage so no field is dropped.

type dbdCapabilities struct {
	Databases   bool `json:"databases"`
	Users       bool `json:"users"`
	Tokens      bool `json:"tokens"`
	Query       bool `json:"query"`
	Config      bool `json:"config"`
	Certificate bool `json:"certificate"`
	Extensions  bool `json:"extensions"`
	Insights    bool `json:"insights"`
	Sessions    bool `json:"sessions"`
	Export      bool `json:"export"`
	Import      bool `json:"import"`
}

// dbdInstance is the part of GET .../instances/{id} the data commands use.
type dbdInstance struct {
	rtInstance
	EngineLabel     string  `json:"engineLabel"`
	DefaultDatabase *string `json:"defaultDatabase"`
	AdminUsername   *string `json:"adminUsername"`
	Endpoint        *struct {
		Host string `json:"host"`
		Port *int   `json:"port"`
	} `json:"endpoint"`
	PendingRestart bool            `json:"pendingRestart"`
	Capabilities   dbdCapabilities `json:"capabilities"`
}

type dbdQueryColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Kind string `json:"kind"`
}

type dbdQueryError struct {
	Message  string  `json:"message"`
	Code     *string `json:"code"`
	Position *int    `json:"position"`
	Detail   *string `json:"detail"`
	Hint     *string `json:"hint"`
}

type dbdQueryStatement struct {
	Index      int              `json:"index"`
	Command    *string          `json:"command"`
	Status     string           `json:"status"`
	Columns    []dbdQueryColumn `json:"columns"`
	Rows       [][]*string      `json:"rows"`
	RowCount   *int64           `json:"rowCount"`
	Truncated  bool             `json:"truncated"`
	DurationMs float64          `json:"durationMs"`
	Error      *dbdQueryError   `json:"error"`
}

type dbdQueryNotice struct {
	Severity string  `json:"severity"`
	Code     *string `json:"code"`
	Message  string  `json:"message"`
}

type dbdQueryResult struct {
	ReadOnly   bool                `json:"readOnly"`
	DurationMs float64             `json:"durationMs"`
	Statements []dbdQueryStatement `json:"statements"`
	Notices    []dbdQueryNotice    `json:"notices"`
}

type dbdDatabase struct {
	Name      string  `json:"name"`
	Owner     *string `json:"owner"`
	SizeBytes *int64  `json:"sizeBytes"`
	Encoding  *string `json:"encoding"`
	Collation *string `json:"collation"`
	IsDefault bool    `json:"isDefault"`
	IsSystem  bool    `json:"isSystem"`
}

type dbdUser struct {
	Name         string   `json:"name"`
	Preset       string   `json:"preset"`
	Databases    []string `json:"databases"`
	AllDatabases bool     `json:"allDatabases"`
	IsPrimary    bool     `json:"isPrimary"`
	CanLogin     bool     `json:"canLogin"`
}

type dbdUserCreateResult struct {
	User     dbdUser `json:"user"`
	Password string  `json:"password"`
}

type dbdPasswordResult struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

// dbdUserBinding is the part of an app binding the user commands warn about.
type dbdUserBinding struct {
	ID                string  `json:"id"`
	TargetProjectName string  `json:"targetProjectName"`
	EnvKey            string  `json:"envKey"`
	Username          *string `json:"username"`
	DedicatedUser     bool    `json:"dedicatedUser"`
}

type dbdToken struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Access    string  `json:"access"`
	CreatedAt string  `json:"createdAt"`
	ExpiresAt *string `json:"expiresAt"`
	Active    bool    `json:"active"`
}

type dbdTokenCreateResult struct {
	Token dbdToken `json:"token"`
	Value string   `json:"value"`
}

type dbdTokenRevokeResult struct {
	Revoked          int  `json:"revoked"`
	AllTokensRevoked bool `json:"allTokensRevoked"`
	AppsReconnected  int  `json:"appsReconnected"`
}

type dbdExtension struct {
	Name             string  `json:"name"`
	InstalledVersion *string `json:"installedVersion"`
	DefaultVersion   *string `json:"defaultVersion"`
	Allowed          bool    `json:"allowed"`
	Description      *string `json:"description"`
}

type dbdTransfer struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Status       string  `json:"status"`
	DatabaseName *string `json:"databaseName"`
	Format       string  `json:"format"`
	FileName     *string `json:"fileName"`
	SizeBytes    *int64  `json:"sizeBytes"`
	SHA256       *string `json:"sha256"`
	Error        *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	CreatedAt  string  `json:"createdAt"`
	FinishedAt *string `json:"finishedAt"`
	ExpiresAt  *string `json:"expiresAt"`
}

type dbdTransfers struct {
	Items     []dbdTransfer `json:"items"`
	Available bool          `json:"available"`
}

type dbdExportCreateResult struct {
	Export      dbdTransfer `json:"export"`
	OperationID string      `json:"operationId"`
}

type dbdDownloadLink struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
	FileName  string `json:"fileName"`
}

type dbdImportUpload struct {
	ImportID  string `json:"importId"`
	UploadURL string `json:"uploadUrl"`
	ExpiresAt string `json:"expiresAt"`
	MaxBytes  int64  `json:"maxBytes"`
}

type dbdCertificate struct {
	PEM               string  `json:"pem"`
	FingerprintSHA256 *string `json:"fingerprintSha256"`
	ExpiresAt         *string `json:"expiresAt"`
	FileName          string  `json:"fileName"`
}

type dbdSession struct {
	ID            string   `json:"id"`
	User          *string  `json:"user"`
	Database      *string  `json:"database"`
	ClientAddress *string  `json:"clientAddress"`
	ViaProxy      bool     `json:"viaProxy"`
	State         *string  `json:"state"`
	Query         *string  `json:"query"`
	StartedAt     *string  `json:"startedAt"`
	DurationMs    *float64 `json:"durationMs"`
	WaitEvent     *string  `json:"waitEvent"`
	Platform      bool     `json:"platform"`
}

type dbdInsight struct {
	QueryID  string  `json:"queryId"`
	Query    string  `json:"query"`
	Database *string `json:"database"`
	Calls    int64   `json:"calls"`
	TotalMs  float64 `json:"totalMs"`
	MeanMs   float64 `json:"meanMs"`
	Rows     int64   `json:"rows"`
}

type dbdInsights struct {
	Available bool         `json:"available"`
	Items     []dbdInsight `json:"items"`
}

type dbdConfigSetting struct {
	Key             string   `json:"key"`
	Value           *string  `json:"value"`
	DefaultValue    *string  `json:"defaultValue"`
	Type            string   `json:"type"`
	Unit            *string  `json:"unit"`
	Min             *string  `json:"min"`
	Max             *string  `json:"max"`
	EnumValues      []string `json:"enumValues"`
	Category        string   `json:"category"`
	Description     *string  `json:"description"`
	RestartRequired bool     `json:"restartRequired"`
	Locked          bool     `json:"locked"`
	Editable        bool     `json:"editable"`
	Modified        bool     `json:"modified"`
}

type dbdConfig struct {
	Generation         int64              `json:"generation"`
	LastGoodGeneration int64              `json:"lastGoodGeneration"`
	PendingRestart     []string           `json:"pendingRestart"`
	RawFormat          string             `json:"rawFormat"`
	Raw                string             `json:"raw"`
	Settings           []dbdConfigSetting `json:"settings"`
	RevisionNumber     *int64             `json:"revisionNumber"`
}

type dbdConfigProblem struct {
	Key     *string `json:"key"`
	Line    *int    `json:"line"`
	Message string  `json:"message"`
}

type dbdConfigDiff struct {
	Key             string  `json:"key"`
	From            *string `json:"from"`
	To              *string `json:"to"`
	RestartRequired bool    `json:"restartRequired"`
}

type dbdConfigValidation struct {
	Valid           bool               `json:"valid"`
	Errors          []dbdConfigProblem `json:"errors"`
	Warnings        []dbdConfigProblem `json:"warnings"`
	RestartRequired []string           `json:"restartRequired"`
	Raw             string             `json:"raw"`
	Diff            []dbdConfigDiff    `json:"diff"`
}

type dbdConfigApplyResult struct {
	RevisionID      string   `json:"revisionId"`
	RevisionNumber  int64    `json:"revisionNumber"`
	OperationID     string   `json:"operationId"`
	RestartRequired []string `json:"restartRequired"`
}

type dbdConfigRevision struct {
	ID     string          `json:"id"`
	Number int64           `json:"number"`
	State  string          `json:"state"`
	Note   *string         `json:"note"`
	Diff   []dbdConfigDiff `json:"diff"`
	Actor  *struct {
		ID   string  `json:"id"`
		Name *string `json:"name"`
	} `json:"actor"`
	CreatedAt string `json:"createdAt"`
}

// dbdQueryHTTPTimeout covers the Dashboard's agent deadline (the statement
// timeout plus a few seconds) and the exec overhead around it.
func dbdQueryHTTPTimeout(statementTimeout time.Duration) time.Duration {
	if statementTimeout <= 0 {
		statementTimeout = 30 * time.Second
	}
	return statementTimeout + 45*time.Second
}

// dbdGetRaw fetches one endpoint's data as raw JSON.
func dbdGetRaw(token, requestURL string) (json.RawMessage, error) {
	var raw json.RawMessage
	err := doJSONRequest(http.MethodGet, requestURL, token, nil, &raw)
	return raw, err
}

// dbdDecode reads raw response data into a typed view.
func dbdDecode(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func dbdWithQuery(requestURL string, query url.Values) string {
	if len(query) == 0 {
		return requestURL
	}
	return requestURL + "?" + query.Encode()
}

func fetchDBDInstance(token, projectID, instanceID string) (dbdInstance, error) {
	var payload dbdInstance
	err := doJSONRequest(http.MethodGet, rtInstancePath(projectID, instanceID), token, nil, &payload)
	return payload, err
}

func postDBDQuery(token, projectID, instanceID string, body map[string]any, timeout time.Duration) (json.RawMessage, error) {
	var raw json.RawMessage
	err := doJSONRequestWithTimeout(http.MethodPost, rtInstancePath(projectID, instanceID)+"/query", token, body, &raw, timeout)
	return raw, err
}

func fetchDBDDatabases(token, projectID, instanceID string) ([]dbdDatabase, error) {
	var payload struct {
		Items []dbdDatabase `json:"items"`
	}
	err := doJSONRequest(http.MethodGet, rtInstancePath(projectID, instanceID)+"/databases", token, nil, &payload)
	return payload.Items, err
}

func createDBDDatabase(token, projectID, instanceID, name, owner string) (string, error) {
	body := map[string]any{"name": name}
	if owner != "" {
		body["owner"] = owner
	}
	var payload struct {
		Name string `json:"name"`
	}
	err := doJSONRequest(http.MethodPost, rtInstancePath(projectID, instanceID)+"/databases/create", token, body, &payload)
	return payload.Name, err
}

func deleteDBDDatabase(token, projectID, instanceID, name string, force bool) (bool, error) {
	query := url.Values{"confirmName": {name}}
	if force {
		query.Set("force", "true")
	}
	var payload struct {
		Dropped bool `json:"dropped"`
	}
	requestURL := dbdWithQuery(rtInstancePath(projectID, instanceID)+"/databases/"+url.PathEscape(name)+"/delete", query)
	err := doJSONRequest(http.MethodDelete, requestURL, token, nil, &payload)
	return payload.Dropped, err
}

func fetchDBDUsers(token, projectID, instanceID string) ([]dbdUser, error) {
	var payload struct {
		Items []dbdUser `json:"items"`
	}
	err := doJSONRequest(http.MethodGet, rtInstancePath(projectID, instanceID)+"/users", token, nil, &payload)
	return payload.Items, err
}

func createDBDUser(token, projectID, instanceID string, body map[string]any) (dbdUserCreateResult, error) {
	var payload dbdUserCreateResult
	err := doJSONRequest(http.MethodPost, rtInstancePath(projectID, instanceID)+"/users/create", token, body, &payload)
	return payload, err
}

func updateDBDUser(token, projectID, instanceID, name string, body map[string]any) error {
	return doJSONRequest(http.MethodPut, rtInstancePath(projectID, instanceID)+"/users/"+url.PathEscape(name)+"/update", token, body, nil)
}

func deleteDBDUser(token, projectID, instanceID, name, reassignTo string) (bool, error) {
	query := url.Values{}
	if reassignTo != "" {
		query.Set("reassignOwnedTo", reassignTo)
	}
	var payload struct {
		Dropped bool `json:"dropped"`
	}
	requestURL := dbdWithQuery(rtInstancePath(projectID, instanceID)+"/users/"+url.PathEscape(name)+"/delete", query)
	err := doJSONRequest(http.MethodDelete, requestURL, token, nil, &payload)
	return payload.Dropped, err
}

func rotateDBDUserPassword(token, projectID, instanceID, name string) (dbdPasswordResult, error) {
	var payload dbdPasswordResult
	err := doJSONRequest(http.MethodPost, rtInstancePath(projectID, instanceID)+"/users/"+url.PathEscape(name)+"/password/rotate", token, map[string]any{}, &payload)
	return payload, err
}

func fetchDBDUserBindings(token, projectID, instanceID string) ([]dbdUserBinding, error) {
	var payload struct {
		Items []dbdUserBinding `json:"items"`
	}
	err := doJSONRequest(http.MethodGet, rtInstancePath(projectID, instanceID)+"/bindings", token, nil, &payload)
	return payload.Items, err
}

func fetchDBDTokens(token, projectID, instanceID string) ([]dbdToken, error) {
	var payload struct {
		Items []dbdToken `json:"items"`
	}
	err := doJSONRequest(http.MethodGet, rtInstancePath(projectID, instanceID)+"/tokens", token, nil, &payload)
	return payload.Items, err
}

func createDBDToken(token, projectID, instanceID, name, access string, expiresInDays int) (dbdTokenCreateResult, error) {
	body := map[string]any{"name": name, "access": access}
	if expiresInDays > 0 {
		body["expiresInDays"] = expiresInDays
	}
	var payload dbdTokenCreateResult
	err := doJSONRequest(http.MethodPost, rtInstancePath(projectID, instanceID)+"/tokens/create", token, body, &payload)
	return payload, err
}

func revokeDBDToken(token, projectID, instanceID, tokenID string) (dbdTokenRevokeResult, error) {
	var payload dbdTokenRevokeResult
	err := doJSONRequest(http.MethodDelete, rtInstancePath(projectID, instanceID)+"/tokens/"+url.PathEscape(tokenID)+"/delete", token, nil, &payload)
	return payload, err
}

func fetchDBDExtensions(token, projectID, instanceID, database string) ([]dbdExtension, error) {
	query := url.Values{}
	if database != "" {
		query.Set("database", database)
	}
	var payload struct {
		Items []dbdExtension `json:"items"`
	}
	err := doJSONRequest(http.MethodGet, dbdWithQuery(rtInstancePath(projectID, instanceID)+"/extensions", query), token, nil, &payload)
	return payload.Items, err
}

func installDBDExtension(token, projectID, instanceID, database, name string) error {
	body := map[string]any{"name": name}
	if database != "" {
		body["database"] = database
	}
	return doJSONRequest(http.MethodPost, rtInstancePath(projectID, instanceID)+"/extensions/install", token, body, nil)
}

func removeDBDExtension(token, projectID, instanceID, database, name string, cascade bool) (bool, error) {
	query := url.Values{}
	if database != "" {
		query.Set("database", database)
	}
	if cascade {
		query.Set("cascade", "true")
	}
	var payload struct {
		Dropped bool `json:"dropped"`
	}
	requestURL := dbdWithQuery(rtInstancePath(projectID, instanceID)+"/extensions/"+url.PathEscape(name)+"/delete", query)
	err := doJSONRequest(http.MethodDelete, requestURL, token, nil, &payload)
	return payload.Dropped, err
}

func fetchDBDTransfersRaw(token, projectID, instanceID string) (json.RawMessage, error) {
	return dbdGetRaw(token, rtInstancePath(projectID, instanceID)+"/exports")
}

func createDBDExport(token, projectID, instanceID, database, format string) (dbdExportCreateResult, error) {
	body := map[string]any{}
	if database != "" {
		body["databaseName"] = database
	}
	if format != "" {
		body["format"] = format
	}
	var payload dbdExportCreateResult
	err := doJSONRequest(http.MethodPost, rtInstancePath(projectID, instanceID)+"/exports/create", token, body, &payload)
	return payload, err
}

func createDBDExportDownload(token, projectID, instanceID, exportID string) (dbdDownloadLink, error) {
	var payload dbdDownloadLink
	err := doJSONRequest(http.MethodPost, rtInstancePath(projectID, instanceID)+"/exports/"+url.PathEscape(exportID)+"/download", token, map[string]any{}, &payload)
	return payload, err
}

func createDBDImportUpload(token, projectID, instanceID string, body map[string]any) (dbdImportUpload, error) {
	var payload dbdImportUpload
	err := doJSONRequest(http.MethodPost, rtInstancePath(projectID, instanceID)+"/imports/upload", token, body, &payload)
	return payload, err
}

func startDBDImport(token, projectID, instanceID, importID string) (string, error) {
	var payload struct {
		OperationID string `json:"operationId"`
	}
	err := doJSONRequest(http.MethodPost, rtInstancePath(projectID, instanceID)+"/imports/"+url.PathEscape(importID)+"/start", token, map[string]any{}, &payload)
	return payload.OperationID, err
}

func fetchDBDCertificate(token, projectID, instanceID string) (dbdCertificate, error) {
	var payload dbdCertificate
	err := doJSONRequest(http.MethodGet, rtInstancePath(projectID, instanceID)+"/certificate", token, nil, &payload)
	return payload, err
}

func fetchDBDSessions(token, projectID, instanceID string) ([]dbdSession, error) {
	var payload struct {
		Items []dbdSession `json:"items"`
	}
	err := doJSONRequest(http.MethodGet, rtInstancePath(projectID, instanceID)+"/sessions", token, nil, &payload)
	return payload.Items, err
}

// signalDBDSession sends "cancel" or "terminate" to one session.
func signalDBDSession(token, projectID, instanceID, sessionID, signal string) (bool, error) {
	var payload struct {
		OK bool `json:"ok"`
	}
	err := doJSONRequest(http.MethodPost, rtInstancePath(projectID, instanceID)+"/sessions/"+url.PathEscape(sessionID)+"/"+signal, token, map[string]any{}, &payload)
	return payload.OK, err
}

func fetchDBDInsightsRaw(token, projectID, instanceID, orderBy string, limit int) (json.RawMessage, error) {
	query := url.Values{}
	if orderBy != "" {
		query.Set("orderBy", orderBy)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	return dbdGetRaw(token, dbdWithQuery(rtInstancePath(projectID, instanceID)+"/insights", query))
}

func resetDBDInsights(token, projectID, instanceID string) error {
	return doJSONRequest(http.MethodPost, rtInstancePath(projectID, instanceID)+"/insights/reset", token, map[string]any{}, nil)
}

func fetchDBDConfigRaw(token, projectID, instanceID string) (json.RawMessage, error) {
	return dbdGetRaw(token, rtInstancePath(projectID, instanceID)+"/config")
}

func validateDBDConfig(token, projectID, instanceID string, changes map[string]*string) (dbdConfigValidation, error) {
	var payload dbdConfigValidation
	err := doJSONRequest(http.MethodPost, rtInstancePath(projectID, instanceID)+"/config/validate", token, map[string]any{"changes": changes}, &payload)
	return payload, err
}

func applyDBDConfig(token, projectID, instanceID string, body map[string]any) (dbdConfigApplyResult, error) {
	var payload dbdConfigApplyResult
	err := doJSONRequest(http.MethodPost, rtInstancePath(projectID, instanceID)+"/config/apply", token, body, &payload)
	return payload, err
}

func revertDBDConfig(token, projectID, instanceID string, body map[string]any) (dbdConfigApplyResult, error) {
	var payload dbdConfigApplyResult
	err := doJSONRequest(http.MethodPost, rtInstancePath(projectID, instanceID)+"/config/revert", token, body, &payload)
	return payload, err
}

func fetchDBDConfigHistoryRaw(token, projectID, instanceID string, limit int) (json.RawMessage, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	return dbdGetRaw(token, dbdWithQuery(rtInstancePath(projectID, instanceID)+"/config/history", query))
}
