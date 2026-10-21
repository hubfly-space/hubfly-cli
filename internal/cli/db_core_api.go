package cli

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Dashboard API types and calls behind the core `hubfly db` commands.

type dbcEngineVersion struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	IsDefault bool   `json:"isDefault"`
	LTS       bool   `json:"lts"`
	Status    string `json:"status"`
}

type dbcEngine struct {
	ID            string             `json:"id"`
	Label         string             `json:"label"`
	ShortLabel    string             `json:"shortLabel"`
	Description   string             `json:"description"`
	MinMemoryMib  int                `json:"minMemoryMib"`
	Transport     string             `json:"transport"`
	ExportFormats []string           `json:"exportFormats"`
	Capabilities  map[string]bool    `json:"capabilities"`
	Versions      []dbcEngineVersion `json:"versions"`
	StartingPrice rtPrice            `json:"startingPrice"`
}

type dbcSizePreset struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	CPU         float64 `json:"cpu"`
	MemoryMib   int     `json:"memoryMib"`
	Description string  `json:"description"`
}

type dbcRange struct {
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Step    float64 `json:"step,omitempty"`
	Default float64 `json:"default,omitempty"`
}

type dbcCatalog struct {
	Available bool `json:"available"`
	Services  []struct {
		ID     string `json:"id"`
		Label  string `json:"label"`
		Status string `json:"status"`
	} `json:"services"`
	Engines []dbcEngine `json:"engines"`
	Sizes   struct {
		Presets    []dbcSizePreset `json:"presets"`
		CPU        dbcRange        `json:"cpu"`
		MemoryMib  dbcRange        `json:"memoryMib"`
		StorageGib dbcRange        `json:"storageGib"`
	} `json:"sizes"`
	MaxAllowlistEntries    int `json:"maxAllowlistEntries"`
	MaxInstancesPerAccount int `json:"maxInstancesPerAccount"`
}

// databasesAvailable reports whether Managed Databases can be created.
func (c dbcCatalog) databasesAvailable() bool {
	if !c.Available {
		return false
	}
	for _, service := range c.Services {
		if service.ID == managedServiceDatabase {
			return service.Status == "available"
		}
	}
	return len(c.Engines) > 0
}

func (c dbcCatalog) engine(id string) (dbcEngine, bool) {
	for _, engine := range c.Engines {
		if strings.EqualFold(engine.ID, id) {
			return engine, true
		}
	}
	return dbcEngine{}, false
}

type dbcConnection struct {
	Engine       string  `json:"engine"`
	Host         string  `json:"host"`
	Port         *int    `json:"port"`
	Database     *string `json:"database"`
	Username     *string `json:"username"`
	PublicAccess bool    `json:"publicAccess"`
	Ready        bool    `json:"ready"`
	TLS          struct {
		Required             bool   `json:"required"`
		Verification         string `json:"verification"`
		CertificateAvailable bool   `json:"certificateAvailable"`
	} `json:"tls"`
	LibsqlURL            *string `json:"libsqlUrl"`
	HTTPURL              *string `json:"httpUrl"`
	SecretKind           string  `json:"secretKind"`
	CredentialsRotatedAt *string `json:"credentialsRotatedAt"`
}

// dbcSecret is the answer of credentials/reveal and credentials/rotate.
// kind "password" carries username and value; kind "signing_key" (libSQL
// rotation) carries the counts only.
type dbcSecret struct {
	Kind            string `json:"kind"`
	Username        string `json:"username,omitempty"`
	Value           string `json:"value,omitempty"`
	RevokedTokens   int    `json:"revokedTokens,omitempty"`
	AppsReconnected int    `json:"appsReconnected,omitempty"`
}

type dbcBinding struct {
	ID                string  `json:"id"`
	TargetProjectID   string  `json:"targetProjectId"`
	TargetProjectName string  `json:"targetProjectName"`
	EnvKey            string  `json:"envKey"`
	AuthTokenEnvKey   *string `json:"authTokenEnvKey"`
	DatabaseName      *string `json:"databaseName"`
	Username          *string `json:"username"`
	DedicatedUser     bool    `json:"dedicatedUser"`
	CreatedAt         string  `json:"createdAt"`
}

type dbcBindingInput struct {
	TargetProjectID string `json:"targetProjectId"`
	EnvKey          string `json:"envKey,omitempty"`
	DatabaseName    string `json:"databaseName,omitempty"`
	CreateUser      bool   `json:"createUser,omitempty"`
	Access          string `json:"access,omitempty"`
}

func fetchDBCatalog(token string) (dbcCatalog, error) {
	var payload dbcCatalog
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/managed/catalog", token, nil, &payload)
	return payload, err
}

func fetchDBConnection(token string, instance rtInstance) (dbcConnection, error) {
	var payload dbcConnection
	err := doJSONRequest(http.MethodGet, rtInstancePath(instance.ProjectID, instance.ID)+"/connection", token, nil, &payload)
	return payload, err
}

// withoutDebugBodies runs fn with HTTP debug logging off. The credential
// endpoints answer with a plain secret ("value"). Suppress the bodies as
// an additional safeguard alongside the shared credential redaction.
func withoutDebugBodies(fn func() error) error {
	previous := debugEnabled
	debugEnabled = false
	defer func() { debugEnabled = previous }()
	return fn()
}

// revealDBCredentials returns the admin password (audited by HubFly).
func revealDBCredentials(token string, instance rtInstance) (dbcSecret, error) {
	var payload dbcSecret
	err := withoutDebugBodies(func() error {
		return doJSONRequest(http.MethodPost, rtInstancePath(instance.ProjectID, instance.ID)+"/credentials/reveal", token, map[string]any{}, &payload)
	})
	return payload, err
}

func rotateDBCredentials(token string, instance rtInstance) (dbcSecret, error) {
	var payload dbcSecret
	err := withoutDebugBodies(func() error {
		return doJSONRequest(http.MethodPost, rtInstancePath(instance.ProjectID, instance.ID)+"/credentials/rotate", token, map[string]any{}, &payload)
	})
	return payload, err
}

func createDBShellSession(token string, instance rtInstance) (terminalSession, error) {
	var payload terminalSession
	err := doJSONRequest(http.MethodPost, rtInstancePath(instance.ProjectID, instance.ID)+"/shell/session", token, map[string]any{}, &payload)
	return payload, err
}

func fetchDBBindings(token string, instance rtInstance) ([]dbcBinding, error) {
	var payload struct {
		Items []dbcBinding `json:"items"`
	}
	err := doJSONRequest(http.MethodGet, rtInstancePath(instance.ProjectID, instance.ID)+"/bindings", token, nil, &payload)
	return payload.Items, err
}

func createDBBinding(token string, instance rtInstance, input dbcBindingInput) (dbcBinding, error) {
	var payload dbcBinding
	err := doJSONRequest(http.MethodPost, rtInstancePath(instance.ProjectID, instance.ID)+"/bindings/create", token, input, &payload)
	return payload, err
}

func deleteDBBinding(token string, instance rtInstance, bindingID string, removeVariable bool) error {
	requestURL := rtInstancePath(instance.ProjectID, instance.ID) + "/bindings/" + url.PathEscape(bindingID) + "/delete?" +
		url.Values{"removeVariable": {strconv.FormatBool(removeVariable)}}.Encode()
	return doJSONRequest(http.MethodDelete, requestURL, token, nil, nil)
}
