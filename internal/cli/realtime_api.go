package cli

import (
	"net/http"
	"net/url"
	"strconv"
)

// Managed Realtime (Dashboard API under /api/v1/projects/{projectId}/managed
// and .../managed/instances/{instanceId}/realtime). Shapes follow the
// Dashboard's generated openapi.json. Event `data` is always a string.

type rtPriceParts struct {
	Compute    float64 `json:"compute"`
	Storage    float64 `json:"storage"`
	Management float64 `json:"management"`
	Total      float64 `json:"total"`
}

type rtPrice struct {
	Currency      string       `json:"currency"`
	HoursPerMonth float64      `json:"hoursPerMonth"`
	Hourly        rtPriceParts `json:"hourly"`
	Monthly       rtPriceParts `json:"monthly"`
}

type rtActiveOperation struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Status    string  `json:"status"`
	StepIndex int     `json:"stepIndex"`
	StepCount int     `json:"stepCount"`
	StepLabel *string `json:"stepLabel"`
}

type rtSize struct {
	CPU        float64 `json:"cpu"`
	MemoryMib  int     `json:"memoryMib"`
	StorageGib int     `json:"storageGib"`
}

type rtInstance struct {
	ID              string             `json:"id"`
	Name            string             `json:"name"`
	Service         string             `json:"service"`
	Engine          string             `json:"engine"`
	Version         string             `json:"version"`
	VersionLabel    string             `json:"versionLabel"`
	EngineVersion   *string            `json:"engineVersion"`
	Status          string             `json:"status"`
	StatusReason    *string            `json:"statusReason"`
	Size            rtSize             `json:"size"`
	Price           rtPrice            `json:"price"`
	ActiveOperation *rtActiveOperation `json:"activeOperation"`
	CreatedAt       string             `json:"createdAt"`

	// Filled in by the CLI.
	ProjectID   string `json:"projectId,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
}

type rtProfile struct {
	MemoryMib            int     `json:"memoryMib"`
	CPU                  float64 `json:"cpu"`
	MaxConnections       int     `json:"maxConnections"`
	DeliveriesPerSecond  int     `json:"deliveriesPerSecond"`
	PublishesPerSecond   int     `json:"publishesPerSecond"`
	MaxPayloadBytes      int     `json:"maxPayloadBytes"`
	BandwidthMbps        float64 `json:"bandwidthMbps"`
	HistoryMaxTTLSeconds int     `json:"historyMaxTtlSeconds"`
}

type rtInstanceDetail struct {
	rtInstance
	Realtime *struct {
		Host    string    `json:"host"`
		WsURL   string    `json:"wsUrl"`
		HTTPURL string    `json:"httpUrl"`
		Apps    int       `json:"apps"`
		Profile rtProfile `json:"profile"`
		Sync    struct {
			Generation        int     `json:"generation"`
			AppliedGeneration int     `json:"appliedGeneration"`
			InSync            bool    `json:"inSync"`
			LastError         *string `json:"lastError"`
		} `json:"sync"`
	} `json:"realtime"`
}

type rtCatalogSize struct {
	ID          string    `json:"id"`
	Label       string    `json:"label"`
	CPU         float64   `json:"cpu"`
	MemoryMib   int       `json:"memoryMib"`
	Description string    `json:"description"`
	Profile     rtProfile `json:"profile"`
	Price       rtPrice   `json:"price"`
}

type rtCatalog struct {
	Available bool `json:"available"`
	Engine    struct {
		Versions []struct {
			ID        string `json:"id"`
			Label     string `json:"label"`
			IsDefault bool   `json:"isDefault"`
		} `json:"versions"`
	} `json:"engine"`
	Sizes      []rtCatalogSize `json:"sizes"`
	StorageGib struct {
		Min     int `json:"min"`
		Max     int `json:"max"`
		Default int `json:"default"`
	} `json:"storageGib"`
	KeyGraceSeconds int `json:"keyGraceSeconds"`
}

type rtOperation struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Steps  []struct {
		Key    string `json:"key"`
		Label  string `json:"label"`
		Status string `json:"status"`
	} `json:"steps"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type rtCreateInstanceResult struct {
	Instance    rtInstance `json:"instance"`
	OperationID string     `json:"operationId"`
	Price       rtPrice    `json:"price"`
	InitialApp  *struct {
		AppID string `json:"appId"`
		Name  string `json:"name"`
		KeyID string `json:"keyId"`
		Key   string `json:"key"`
	} `json:"initialApp"`
}

type rtAppSettings struct {
	AllowedOrigins            []string `json:"allowedOrigins"`
	ClientEvents              bool     `json:"clientEvents"`
	SubscriptionCountEvents   bool     `json:"subscriptionCountEvents"`
	RequireUserAuthentication bool     `json:"requireUserAuthentication"`
	MaxConnections            int      `json:"maxConnections"`
	MaxChannelsPerConnection  int      `json:"maxChannelsPerConnection"`
	MaxPresenceMembers        int      `json:"maxPresenceMembers"`
	MaxMessageBytes           int      `json:"maxMessageBytes"`
	MaxPublishesPerSecond     int      `json:"maxPublishesPerSecond"`
	MaxClientEventsPerSecond  int      `json:"maxClientEventsPerSecond"`
	History                   struct {
		Mode        string `json:"mode"`
		TTLSeconds  int    `json:"ttlSeconds"`
		MaxMessages int    `json:"maxMessages"`
	} `json:"history"`
	Sessions struct {
		DetachTTLSeconds int `json:"detachTtlSeconds"`
	} `json:"sessions"`
	OfflineQueue struct {
		Enabled     bool `json:"enabled"`
		TTLSeconds  int  `json:"ttlSeconds"`
		MaxMessages int  `json:"maxMessages"`
	} `json:"offlineQueue"`
	Encryption              string `json:"encryption"`
	InspectorPayloadPreview bool   `json:"inspectorPayloadPreview"`
}

type rtKey struct {
	ID                string  `json:"id"`
	AppID             string  `json:"appId"`
	Key               string  `json:"key"`
	SecretHint        string  `json:"secretHint"`
	SecretFingerprint string  `json:"secretFingerprint"`
	Status            string  `json:"status"`
	ExpiresAt         *string `json:"expiresAt"`
	RevokedAt         *string `json:"revokedAt"`
	LastRevealedAt    *string `json:"lastRevealedAt"`
	CreatedAt         string  `json:"createdAt"`
}

type rtApp struct {
	AppID            string        `json:"appId"`
	InstanceID       string        `json:"instanceId"`
	Name             string        `json:"name"`
	Enabled          bool          `json:"enabled"`
	Source           string        `json:"source"`
	Settings         rtAppSettings `json:"settings"`
	SettingsRevision int           `json:"settingsRevision"`
	Keys             []rtKey       `json:"keys"`
	CreatedAt        string        `json:"createdAt"`
	UpdatedAt        string        `json:"updatedAt"`
}

type rtSync struct {
	Status     string  `json:"status"`
	Generation int     `json:"generation"`
	Error      *string `json:"error"`
}

type rtAppCreateResult struct {
	App    rtApp  `json:"app"`
	Key    rtKey  `json:"key"`
	Secret string `json:"secret"`
	Sync   rtSync `json:"sync"`
}

type rtAppUpdateResult struct {
	App  rtApp  `json:"app"`
	Sync rtSync `json:"sync"`
}

type rtAppDeleteResult struct {
	Deleted          bool   `json:"deleted"`
	VariablesRemoved int    `json:"variablesRemoved"`
	Sync             rtSync `json:"sync"`
}

type rtKeyCreateResult struct {
	Key    rtKey  `json:"key"`
	Secret string `json:"secret"`
	Sync   rtSync `json:"sync"`
}

type rtKeyRotateResult struct {
	Key         rtKey  `json:"key"`
	Secret      string `json:"secret"`
	Sync        rtSync `json:"sync"`
	Retiring    rtKey  `json:"retiring"`
	AppsUpdated int    `json:"appsUpdated"`
}

type rtKeyRevokeResult struct {
	Key         rtKey  `json:"key"`
	AppsUpdated int    `json:"appsUpdated"`
	Sync        rtSync `json:"sync"`
}

type rtKeyReveal struct {
	KeyID  string `json:"keyId"`
	AppID  string `json:"appId"`
	Key    string `json:"key"`
	Secret string `json:"secret"`
}

type rtBinding struct {
	ID                string   `json:"id"`
	AppID             string   `json:"appId"`
	AppName           string   `json:"appName"`
	KeyID             *string  `json:"keyId"`
	Key               *string  `json:"key"`
	TargetProjectID   string   `json:"targetProjectId"`
	TargetProjectName string   `json:"targetProjectName"`
	Preset            string   `json:"preset"`
	ClientPrefix      string   `json:"clientPrefix"`
	EnvKeys           []string `json:"envKeys"`
	CreatedAt         string   `json:"createdAt"`
}

type rtBindingCreateInput struct {
	AppID           string `json:"appId"`
	TargetProjectID string `json:"targetProjectId"`
	Preset          string `json:"preset,omitempty"`
	ClientPrefix    string `json:"clientPrefix,omitempty"`
	KeyID           string `json:"keyId,omitempty"`
}

type rtConnectionInfo struct {
	Host    string `json:"host"`
	WsURL   string `json:"wsUrl"`
	HTTPURL string `json:"httpUrl"`
	Port    int    `json:"port"`
	Ready   bool   `json:"ready"`
	Apps    []struct {
		AppID      string  `json:"appId"`
		Name       string  `json:"name"`
		Enabled    bool    `json:"enabled"`
		Key        *string `json:"key"`
		Encryption string  `json:"encryption"`
	} `json:"apps"`
	Env struct {
		Hubfly  []string `json:"hubfly"`
		Laravel []string `json:"laravel"`
		Secret  []string `json:"secret"`
		E2EKeys string   `json:"e2eKeys"`
	} `json:"env"`
}

type rtPublishInput struct {
	Event           string   `json:"event"`
	Data            string   `json:"data"`
	Channels        []string `json:"channels"`
	ExcludeSocketID string   `json:"excludeSocketId,omitempty"`
	MessageID       string   `json:"messageId,omitempty"`
}

type rtPublishResult struct {
	MessageID string `json:"messageId"`
	Channels  map[string]struct {
		Epoch  *string `json:"epoch"`
		Offset *int64  `json:"offset"`
	} `json:"channels"`
	Bytes int `json:"bytes"`
}

type rtSendInput struct {
	Event     string   `json:"event"`
	Data      string   `json:"data"`
	SocketIDs []string `json:"socketIds,omitempty"`
	UserID    string   `json:"userId,omitempty"`
	MessageID string   `json:"messageId,omitempty"`
}

type rtSendResult struct {
	MessageID string `json:"messageId"`
	Results   []struct {
		SocketID    *string `json:"socketId"`
		UserID      *string `json:"userId"`
		Status      string  `json:"status"`
		Connections *int    `json:"connections"`
	} `json:"results"`
	Counts struct {
		Delivered int `json:"delivered"`
		Buffered  int `json:"buffered"`
		Queued    int `json:"queued"`
		Offline   int `json:"offline"`
	} `json:"counts"`
	Bytes  int     `json:"bytes"`
	Notice *string `json:"notice"`
}

type rtInsights struct {
	WindowSeconds int `json:"windowSeconds"`
	TopChannels   []struct {
		Name            string `json:"name"`
		Messages        int64  `json:"messages"`
		Bytes           int64  `json:"bytes"`
		SubscribersPeak int64  `json:"subscribersPeak"`
	} `json:"topChannels"`
	TopEvents []struct {
		Name  string `json:"name"`
		Count int64  `json:"count"`
		Bytes int64  `json:"bytes"`
	} `json:"topEvents"`
	AuthFailures []struct {
		Reason string `json:"reason"`
		Count  int64  `json:"count"`
	} `json:"authFailures"`
	RateLimited     map[string]int64 `json:"rateLimited"`
	Closes          map[string]int64 `json:"closes"`
	ConnectionsPeak int64            `json:"connectionsPeak"`
	Libraries       []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Count   int64  `json:"count"`
	} `json:"libraries"`
	Webhooks struct {
		SuccessRate  *float64 `json:"successRate"`
		LatencyMsP95 *float64 `json:"latencyMsP95"`
		Backlog      int64    `json:"backlog"`
	} `json:"webhooks"`
}

type rtInspectorTokenInput struct {
	Origin         string   `json:"origin,omitempty"`
	Apps           []string `json:"apps,omitempty"`
	PayloadPreview bool     `json:"payloadPreview,omitempty"`
}

type rtInspectorToken struct {
	Token          string   `json:"token"`
	URL            string   `json:"url"`
	ExpiresAt      string   `json:"expiresAt"`
	Origin         string   `json:"origin"`
	Apps           []string `json:"apps"`
	PayloadPreview bool     `json:"payloadPreview"`
}

func rtProjectPath(projectID string) string {
	return apiHost + "/api/v1/projects/" + url.PathEscape(projectID)
}

func rtInstancePath(projectID, instanceID string) string {
	return rtProjectPath(projectID) + "/managed/instances/" + url.PathEscape(instanceID)
}

func rtAppPath(projectID, instanceID, appID string) string {
	return rtInstancePath(projectID, instanceID) + "/realtime/apps/" + url.PathEscape(appID)
}

func fetchManagedCatalogRealtime(token string) (rtCatalog, error) {
	var payload struct {
		Realtime rtCatalog `json:"realtime"`
	}
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/managed/catalog", token, nil, &payload)
	return payload.Realtime, err
}

func fetchManagedPriceEstimate(token string, size rtSize) (rtPrice, error) {
	query := url.Values{}
	query.Set("cpu", strconv.FormatFloat(size.CPU, 'f', -1, 64))
	query.Set("memoryMib", strconv.Itoa(size.MemoryMib))
	query.Set("storageGib", strconv.Itoa(size.StorageGib))
	var payload rtPrice
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/pricing/managed/estimate?"+query.Encode(), token, nil, &payload)
	return payload, err
}

// createManagedProject creates a managed project for realtime servers.
func createManagedProject(token, name string) (project, error) {
	return createManagedProjectFor(token, name, managedServiceRealtime)
}

// createManagedProjectFor creates a managed project placed for one service
// ("database" or "realtime"). HubFly places managed projects itself (the API
// ignores a requested region).
func createManagedProjectFor(token, name, service string) (project, error) {
	body := map[string]any{
		"name":           name,
		"type":           "managed",
		"managedService": service,
	}
	var payload project
	err := doJSONRequest(http.MethodPost, apiHost+"/api/v1/projects/create", token, body, &payload)
	return normalizeProject(payload), err
}

func fetchManagedInstances(token, projectID string) ([]rtInstance, error) {
	var payload struct {
		Items []rtInstance `json:"items"`
	}
	err := doJSONRequest(http.MethodGet, rtProjectPath(projectID)+"/managed/instances", token, nil, &payload)
	return payload.Items, err
}

func fetchManagedInstance(token, projectID, instanceID string) (rtInstanceDetail, error) {
	var payload rtInstanceDetail
	err := doJSONRequest(http.MethodGet, rtInstancePath(projectID, instanceID), token, nil, &payload)
	return payload, err
}

func createRealtimeInstance(token, projectID string, body map[string]any) (rtCreateInstanceResult, error) {
	var payload rtCreateInstanceResult
	err := doJSONRequest(http.MethodPost, rtProjectPath(projectID)+"/managed/instances/create", token, body, &payload)
	return payload, err
}

func deleteRealtimeInstance(token, projectID, instanceID, confirmName string) (string, error) {
	var payload struct {
		OperationID string `json:"operationId"`
	}
	requestURL := rtInstancePath(projectID, instanceID) + "/delete?" + url.Values{"confirmName": {confirmName}}.Encode()
	err := doJSONRequest(http.MethodDelete, requestURL, token, nil, &payload)
	return payload.OperationID, err
}

func fetchManagedOperation(token, projectID, instanceID, operationID string) (rtOperation, error) {
	var payload rtOperation
	err := doJSONRequest(http.MethodGet, rtInstancePath(projectID, instanceID)+"/operations/"+url.PathEscape(operationID), token, nil, &payload)
	return payload, err
}

func fetchRealtimeApps(token, projectID, instanceID string) ([]rtApp, error) {
	var payload struct {
		Items []rtApp `json:"items"`
	}
	err := doJSONRequest(http.MethodGet, rtInstancePath(projectID, instanceID)+"/realtime/apps", token, nil, &payload)
	return payload.Items, err
}

func fetchRealtimeApp(token, projectID, instanceID, appID string) (rtApp, error) {
	var payload rtApp
	err := doJSONRequest(http.MethodGet, rtAppPath(projectID, instanceID, appID), token, nil, &payload)
	return payload, err
}

func createRealtimeApp(token, projectID, instanceID, name string, settings map[string]any) (rtAppCreateResult, error) {
	body := map[string]any{"name": name}
	if len(settings) > 0 {
		body["settings"] = settings
	}
	var payload rtAppCreateResult
	err := doJSONRequest(http.MethodPost, rtInstancePath(projectID, instanceID)+"/realtime/apps/create", token, body, &payload)
	return payload, err
}

func updateRealtimeApp(token, projectID, instanceID, appID string, body map[string]any) (rtAppUpdateResult, error) {
	var payload rtAppUpdateResult
	err := doJSONRequest(http.MethodPut, rtAppPath(projectID, instanceID, appID)+"/update", token, body, &payload)
	return payload, err
}

func updateRealtimeAppSettings(token, projectID, instanceID, appID string, settings map[string]any, revision int) (rtAppUpdateResult, error) {
	body := map[string]any{"settings": settings, "revision": revision}
	var payload rtAppUpdateResult
	err := doJSONRequest(http.MethodPut, rtAppPath(projectID, instanceID, appID)+"/settings/update", token, body, &payload)
	return payload, err
}

func deleteRealtimeApp(token, projectID, instanceID, appID, confirmName string) (rtAppDeleteResult, error) {
	var payload rtAppDeleteResult
	requestURL := rtAppPath(projectID, instanceID, appID) + "/delete?" + url.Values{"confirmName": {confirmName}}.Encode()
	err := doJSONRequest(http.MethodDelete, requestURL, token, nil, &payload)
	return payload, err
}

func fetchRealtimeKeys(token, projectID, instanceID, appID string) ([]rtKey, error) {
	var payload struct {
		Items []rtKey `json:"items"`
	}
	err := doJSONRequest(http.MethodGet, rtAppPath(projectID, instanceID, appID)+"/keys", token, nil, &payload)
	return payload.Items, err
}

func createRealtimeKey(token, projectID, instanceID, appID string) (rtKeyCreateResult, error) {
	var payload rtKeyCreateResult
	err := doJSONRequest(http.MethodPost, rtAppPath(projectID, instanceID, appID)+"/keys/create", token, map[string]any{}, &payload)
	return payload, err
}

func rotateRealtimeKey(token, projectID, instanceID, appID, keyID string, graceSeconds *int) (rtKeyRotateResult, error) {
	body := map[string]any{}
	if graceSeconds != nil {
		body["graceSeconds"] = *graceSeconds
	}
	var payload rtKeyRotateResult
	err := doJSONRequest(http.MethodPost, rtAppPath(projectID, instanceID, appID)+"/keys/"+url.PathEscape(keyID)+"/rotate", token, body, &payload)
	return payload, err
}

func revokeRealtimeKey(token, projectID, instanceID, appID, keyID string) (rtKeyRevokeResult, error) {
	var payload rtKeyRevokeResult
	err := doJSONRequest(http.MethodPost, rtAppPath(projectID, instanceID, appID)+"/keys/"+url.PathEscape(keyID)+"/revoke", token, map[string]any{}, &payload)
	return payload, err
}

func revealRealtimeKey(token, projectID, instanceID, appID, keyID string) (rtKeyReveal, error) {
	var payload rtKeyReveal
	err := doJSONRequest(http.MethodPost, rtAppPath(projectID, instanceID, appID)+"/keys/"+url.PathEscape(keyID)+"/reveal", token, map[string]any{}, &payload)
	return payload, err
}

func fetchRealtimeBindings(token, projectID, instanceID string) ([]rtBinding, error) {
	var payload struct {
		Items []rtBinding `json:"items"`
	}
	err := doJSONRequest(http.MethodGet, rtInstancePath(projectID, instanceID)+"/realtime/bindings", token, nil, &payload)
	return payload.Items, err
}

func createRealtimeBinding(token, projectID, instanceID string, input rtBindingCreateInput) (rtBinding, error) {
	var payload rtBinding
	err := doJSONRequest(http.MethodPost, rtInstancePath(projectID, instanceID)+"/realtime/bindings/create", token, input, &payload)
	return payload, err
}

func deleteRealtimeBinding(token, projectID, instanceID, bindingID string, removeVariables bool) (int, error) {
	var payload struct {
		VariablesRemoved int `json:"variablesRemoved"`
	}
	requestURL := rtInstancePath(projectID, instanceID) + "/realtime/bindings/" + url.PathEscape(bindingID) + "/delete?" +
		url.Values{"removeVariables": {strconv.FormatBool(removeVariables)}}.Encode()
	err := doJSONRequest(http.MethodDelete, requestURL, token, nil, &payload)
	return payload.VariablesRemoved, err
}

func fetchRealtimeConnectionInfo(token, projectID, instanceID string) (rtConnectionInfo, error) {
	var payload rtConnectionInfo
	err := doJSONRequest(http.MethodGet, rtInstancePath(projectID, instanceID)+"/realtime/connection", token, nil, &payload)
	return payload, err
}

func publishRealtimeEvent(token, projectID, instanceID, appID string, input rtPublishInput) (rtPublishResult, error) {
	var payload rtPublishResult
	err := doJSONRequest(http.MethodPost, rtAppPath(projectID, instanceID, appID)+"/events/publish", token, input, &payload)
	return payload, err
}

func sendRealtimeEvent(token, projectID, instanceID, appID string, input rtSendInput) (rtSendResult, error) {
	var payload rtSendResult
	err := doJSONRequest(http.MethodPost, rtAppPath(projectID, instanceID, appID)+"/events/send", token, input, &payload)
	return payload, err
}

func fetchRealtimeInsights(token, projectID, instanceID, appID string, windowSeconds int) (rtInsights, error) {
	query := url.Values{}
	if appID != "" {
		query.Set("appId", appID)
	}
	if windowSeconds > 0 {
		query.Set("windowSeconds", strconv.Itoa(windowSeconds))
	}
	requestURL := rtInstancePath(projectID, instanceID) + "/realtime/insights"
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	var payload rtInsights
	err := doJSONRequest(http.MethodGet, requestURL, token, nil, &payload)
	return payload, err
}

func createRealtimeInspectorToken(token, projectID, instanceID string, input rtInspectorTokenInput) (rtInspectorToken, error) {
	var payload rtInspectorToken
	err := doJSONRequest(http.MethodPost, rtInstancePath(projectID, instanceID)+"/realtime/inspector/token", token, input, &payload)
	return payload, err
}
