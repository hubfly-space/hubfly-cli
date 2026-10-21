package cli

import "net/http"

// Dashboard API types and calls behind the shared managed lifecycle commands
// (managed_lifecycle.go) and the core `hubfly db` commands (db_core.go).

// mlInstance is a managed instance as the list and detail endpoints return it,
// with the fields the realtime-only rtInstanceDetail does not carry.
type mlInstance struct {
	rtInstanceDetail
	EngineLabel        string `json:"engineLabel,omitempty"`
	StorageUsedBytes   *int64 `json:"storageUsedBytes"`
	StorageTotalBytes  *int64 `json:"storageTotalBytes"`
	PublicAccess       bool   `json:"publicAccess"`
	AllowlistCount     int    `json:"allowlistCount"`
	DeletionProtection bool   `json:"deletionProtection"`
	PendingRestart     bool   `json:"pendingRestart"`
	Endpoint           *struct {
		Host string `json:"host"`
		Port *int   `json:"port"`
	} `json:"endpoint,omitempty"`
	AdminUsername   *string         `json:"adminUsername,omitempty"`
	DefaultDatabase *string         `json:"defaultDatabase,omitempty"`
	Capabilities    map[string]bool `json:"capabilities,omitempty"`
	Backups         string          `json:"backups,omitempty"`
	UpdateAvailable *struct {
		VersionLabel string `json:"versionLabel"`
	} `json:"updateAvailable,omitempty"`
	TLS *struct {
		FingerprintSha256 string  `json:"fingerprintSha256"`
		ExpiresAt         *string `json:"expiresAt"`
	} `json:"tls,omitempty"`
	CredentialsRotatedAt *string `json:"credentialsRotatedAt,omitempty"`
	UpdatedAt            string  `json:"updatedAt,omitempty"`
}

type mlAllowlistEntry struct {
	CIDR      string  `json:"cidr"`
	Label     *string `json:"label"`
	CreatedAt string  `json:"createdAt,omitempty"`
}

type mlNetwork struct {
	PublicAccess bool `json:"publicAccess"`
	Endpoint     struct {
		Host string `json:"host"`
		Port *int   `json:"port"`
	} `json:"endpoint"`
	Allowlist            []mlAllowlistEntry `json:"allowlist"`
	OpenToAll            bool               `json:"openToAll"`
	Revision             int                `json:"revision"`
	TLSRequired          bool               `json:"tlsRequired"`
	AllowlistSupported   bool               `json:"allowlistSupported"`
	MaxEntries           int                `json:"maxEntries"`
	AllowlistScope       string             `json:"allowlistScope"`
	PublicAccessEditable bool               `json:"publicAccessEditable"`
}

type mlAllowlistInput struct {
	CIDR  string `json:"cidr"`
	Label string `json:"label,omitempty"`
}

type mlAllowlistResult struct {
	OperationID string `json:"operationId"`
	Revision    int    `json:"revision"`
	Entries     int    `json:"entries"`
}

type mlPublicAccessResult struct {
	OperationID  string `json:"operationId"`
	PublicAccess bool   `json:"publicAccess"`
}

type mlResizeResult struct {
	OperationID        string  `json:"operationId"`
	Price              rtPrice `json:"price"`
	RestartRecommended bool    `json:"restartRecommended"`
}

type mlClientIP struct {
	IP     *string `json:"ip"`
	Family *int    `json:"family"`
}

// mlCatalogSizes is the part of GET /managed/catalog the size flags need.
type mlCatalogSizes struct {
	Sizes struct {
		Presets []struct {
			ID          string  `json:"id"`
			Label       string  `json:"label"`
			CPU         float64 `json:"cpu"`
			MemoryMib   int     `json:"memoryMib"`
			Description string  `json:"description"`
		} `json:"presets"`
	} `json:"sizes"`
	Realtime struct {
		Sizes []rtCatalogSize `json:"sizes"`
	} `json:"realtime"`
}

func fetchManagedInstancesDetailed(token, projectID string) ([]mlInstance, error) {
	var payload struct {
		Items []mlInstance `json:"items"`
	}
	err := doJSONRequest(http.MethodGet, rtProjectPath(projectID)+"/managed/instances", token, nil, &payload)
	return payload.Items, err
}

func fetchManagedInstanceDetailed(token string, instance rtInstance) (mlInstance, error) {
	var payload mlInstance
	err := doJSONRequest(http.MethodGet, rtInstancePath(instance.ProjectID, instance.ID), token, nil, &payload)
	payload.ProjectID, payload.ProjectName = instance.ProjectID, instance.ProjectName
	return payload, err
}

// postManagedInstanceAction runs start, stop, restart or upgrade.
func postManagedInstanceAction(token string, instance rtInstance, action string) (string, error) {
	var payload struct {
		OperationID string `json:"operationId"`
	}
	err := doJSONRequest(http.MethodPost, rtInstancePath(instance.ProjectID, instance.ID)+"/"+action, token, map[string]any{}, &payload)
	return payload.OperationID, err
}

func resizeManagedInstance(token string, instance rtInstance, body map[string]any) (mlResizeResult, error) {
	var payload mlResizeResult
	err := doJSONRequest(http.MethodPost, rtInstancePath(instance.ProjectID, instance.ID)+"/resize", token, body, &payload)
	return payload, err
}

func renameManagedInstance(token string, instance rtInstance, name string) (mlInstance, error) {
	var payload mlInstance
	err := doJSONRequest(http.MethodPut, rtInstancePath(instance.ProjectID, instance.ID)+"/update", token, map[string]any{"name": name}, &payload)
	return payload, err
}

func setManagedDeletionProtection(token string, instance rtInstance, enabled bool) (bool, error) {
	var payload struct {
		DeletionProtection bool `json:"deletionProtection"`
	}
	err := doJSONRequest(http.MethodPut, rtInstancePath(instance.ProjectID, instance.ID)+"/deletion-protection/update", token, map[string]any{"enabled": enabled}, &payload)
	return payload.DeletionProtection, err
}

func fetchManagedNetwork(token string, instance rtInstance) (mlNetwork, error) {
	var payload mlNetwork
	err := doJSONRequest(http.MethodGet, rtInstancePath(instance.ProjectID, instance.ID)+"/network", token, nil, &payload)
	return payload, err
}

func updateManagedAllowlist(token string, instance rtInstance, entries []mlAllowlistInput, revision int) (mlAllowlistResult, error) {
	var payload mlAllowlistResult
	body := map[string]any{"entries": entries, "revision": revision}
	err := doJSONRequest(http.MethodPut, rtInstancePath(instance.ProjectID, instance.ID)+"/network/allowlist/update", token, body, &payload)
	return payload, err
}

func updateManagedPublicAccess(token string, instance rtInstance, enabled bool) (mlPublicAccessResult, error) {
	var payload mlPublicAccessResult
	err := doJSONRequest(http.MethodPut, rtInstancePath(instance.ProjectID, instance.ID)+"/network/public-access/update", token, map[string]any{"enabled": enabled}, &payload)
	return payload, err
}

func fetchManagedClientIP(token string) (mlClientIP, error) {
	var payload mlClientIP
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/managed/client-ip", token, nil, &payload)
	return payload, err
}

func fetchManagedCatalogSizes(token string) (mlCatalogSizes, error) {
	var payload mlCatalogSizes
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/managed/catalog", token, nil, &payload)
	return payload, err
}
