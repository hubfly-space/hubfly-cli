package cli

import (
	"net/http"
	"net/url"
	"time"
)

// Web Analytics (Dashboard API under /api/v1/projects/{projectId}/analytics
// and /api/v1/analytics). Requests and responses use the Dashboard's
// camelCase fields, as in openapi.json; these types keep the fields the CLI
// shows. GET and DELETE routes take their input from the query string.

type analyticsRoute struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Domain string `json:"domain"`
}

type analyticsSyncState struct {
	Status   string  `json:"status"`
	Error    *string `json:"error"`
	SyncedAt *string `json:"syncedAt"`
}

type analyticsInstall struct {
	ScriptURL    string `json:"scriptUrl"`
	Snippet      string `json:"snippet"`
	EventsAPIURL string `json:"eventsApiUrl"`
	AutoInjected bool   `json:"autoInjected"`
}

type analyticsSite struct {
	ID                 string             `json:"id"`
	PublicID           string             `json:"publicId"`
	ProjectID          string             `json:"projectId"`
	ProjectName        string             `json:"projectName,omitempty"`
	RegionID           string             `json:"regionId"`
	Name               string             `json:"name"`
	PrimaryDomain      string             `json:"primaryDomain"`
	Domains            []string           `json:"domains"`
	IncludeSubdomains  bool               `json:"includeSubdomains"`
	Timezone           string             `json:"timezone"`
	Currency           string             `json:"currency"`
	RetentionDays      int                `json:"retentionDays"`
	IdentityMode       string             `json:"identityMode"`
	Source             string             `json:"source"`
	Route              *analyticsRoute    `json:"route"`
	InjectEnabled      bool               `json:"injectEnabled"`
	InjectExcludePaths []string           `json:"injectExcludePaths"`
	Proxy              analyticsSyncState `json:"proxy"`
	State              string             `json:"state"`
	Sync               analyticsSyncState `json:"sync"`
	Install            analyticsInstall   `json:"install"`
	LastEventAt        *string            `json:"lastEventAt"`
	DBBytes            int64              `json:"dbBytes"`
	CreatedAt          string             `json:"createdAt"`
}

type analyticsAvailability struct {
	Open   bool    `json:"open"`
	Reason *string `json:"reason"`
}

type analyticsProjectOverview struct {
	ProjectID       string                `json:"projectId"`
	ProjectType     string                `json:"projectType"`
	RegionID        string                `json:"regionId"`
	Availability    analyticsAvailability `json:"availability"`
	MonthlyEventCap *float64              `json:"monthlyEventCap"`
	Sites           []analyticsSite       `json:"sites"`
	CanEdit         bool                  `json:"canEdit"`
	CanManageRoutes bool                  `json:"canManageRoutes"`
}

type analyticsRegion struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location"`
}

// analyticsServedRoute is a domain the project serves through HubProxy.
type analyticsServedRoute struct {
	RouteType string  `json:"routeType"`
	RouteID   string  `json:"routeId"`
	Hostname  string  `json:"hostname"`
	Label     string  `json:"label"`
	Source    string  `json:"source"`
	SiteID    *string `json:"siteId"`
}

type analyticsDomainVerification struct {
	Domain   string  `json:"domain"`
	Status   string  `json:"status"`
	TxtHost  *string `json:"txtHost"`
	TxtValue *string `json:"txtValue"`
	Message  string  `json:"message"`
}

type analyticsSiteStatus struct {
	SiteID      string  `json:"siteId"`
	Reachable   bool    `json:"reachable"`
	Installed   bool    `json:"installed"`
	LastEventAt *string `json:"lastEventAt"`
	EventsToday int64   `json:"eventsToday"`
	RawEvents   int64   `json:"rawEvents"`
	DBBytes     int64   `json:"dbBytes"`
	SummaryDays int     `json:"summaryDays"`
}

type analyticsDeleteResult struct {
	Deleted bool `json:"deleted"`
	Pending bool `json:"pending"`
}

type analyticsUsage struct {
	Month  string `json:"month"`
	Events struct {
		Billable          int64 `json:"billable"`
		Filtered          int64 `json:"filtered"`
		NotCollected      int64 `json:"notCollected"`
		Last24h           int64 `json:"last24h"`
		ProjectedMonthEnd int64 `json:"projectedMonthEnd"`
	} `json:"events"`
	Sites []struct {
		SiteID   string `json:"siteId"`
		PublicID string `json:"publicId"`
		Name     string `json:"name"`
		Billable int64  `json:"billable"`
	} `json:"sites"`
	Allowance struct {
		FreeEventsPerMonth int64 `json:"freeEventsPerMonth"`
		OwnerFreeUsed      int64 `json:"ownerFreeUsed"`
		OwnerFreeRemaining int64 `json:"ownerFreeRemaining"`
	} `json:"allowance"`
	MonthlyEventCap *float64 `json:"monthlyEventCap"`
	Collection      []struct {
		RegionID        string  `json:"regionId"`
		PaidAllowed     bool    `json:"paidAllowed"`
		Reason          *string `json:"reason"`
		Paused          bool    `json:"paused"`
		EventCapReached bool    `json:"eventCapReached"`
	} `json:"collection"`
	Cost *struct {
		PaidEvents                int64 `json:"paidEvents"`
		BilledMicroUSD            int64 `json:"billedMicroUsd"`
		ProjectedMonthEndMicroUSD int64 `json:"projectedMonthEndMicroUsd"`
		HistoryMonthlyMicroUSD    int64 `json:"historyMonthlyMicroUsd"`
	} `json:"cost"`
	UpdatedThrough *string `json:"updatedThrough"`
}

type analyticsIngestKey struct {
	ID         string  `json:"id"`
	SiteID     string  `json:"siteId"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	CreatedAt  string  `json:"createdAt"`
	LastUsedAt *string `json:"lastUsedAt"`
	RevokedAt  *string `json:"revokedAt"`
}

// analyticsNewIngestKey is a new ingest key. Key is returned once.
type analyticsNewIngestKey struct {
	Key       string             `json:"key"`
	IngestKey analyticsIngestKey `json:"ingestKey"`
	Synced    bool               `json:"synced"`
}

type analyticsExport struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
	Truncated   bool   `json:"truncated"`
	Bytes       int64  `json:"bytes"`
}

// analyticsQuery is the body of a report query.
type analyticsQuery struct {
	Type     string               `json:"type"`
	Range    *analyticsRange      `json:"range,omitempty"`
	Compare  string               `json:"compare,omitempty"`
	Filters  []analyticsFilter    `json:"filters,omitempty"`
	Metrics  []string             `json:"metrics,omitempty"`
	Dim      string               `json:"dim,omitempty"`
	Limit    int                  `json:"limit,omitempty"`
	Order    *analyticsQueryOrder `json:"order,omitempty"`
	Interval string               `json:"interval,omitempty"`
}

type analyticsRange struct {
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Period string `json:"period,omitempty"`
}

type analyticsFilter struct {
	Dim    string   `json:"dim"`
	Op     string   `json:"op"`
	Values []string `json:"values"`
}

type analyticsQueryOrder struct {
	Metric string `json:"metric"`
	Dir    string `json:"dir"`
}

type analyticsQueryMeta struct {
	From        *string `json:"from"`
	To          *string `json:"to"`
	Timezone    *string `json:"timezone"`
	Partial     bool    `json:"partial"`
	CoveredFrom *string `json:"coveredFrom"`
	Sampled     bool    `json:"sampled"`
	TotalValues *int64  `json:"totalValues"`
	MergedMove  bool    `json:"mergedMove"`
}

type analyticsMetricValue struct {
	Value           any      `json:"value"`
	ComparisonValue any      `json:"comparisonValue"`
	Change          *float64 `json:"change"`
}

type analyticsAggregate struct {
	Results map[string]analyticsMetricValue `json:"results"`
	Meta    analyticsQueryMeta              `json:"meta"`
}

type analyticsBreakdownRow struct {
	Value  string         `json:"value"`
	Values map[string]any `json:"values"`
}

type analyticsBreakdown struct {
	Results []analyticsBreakdownRow `json:"results"`
	Meta    analyticsQueryMeta      `json:"meta"`
}

type analyticsRealtimeTop struct {
	Value    string `json:"value"`
	Visitors int64  `json:"visitors"`
}

type analyticsRealtime struct {
	Visitors     int64                  `json:"visitors"`
	Pageviews    int64                  `json:"pageviews"`
	TopPages     []analyticsRealtimeTop `json:"topPages"`
	TopSources   []analyticsRealtimeTop `json:"topSources"`
	TopCountries []analyticsRealtimeTop `json:"topCountries"`
	Recent       []struct {
		TS      string  `json:"ts"`
		Type    string  `json:"type"`
		Name    *string `json:"name"`
		Page    string  `json:"page"`
		Country *string `json:"country"`
		City    *string `json:"city"`
		Device  *string `json:"device"`
		Browser *string `json:"browser"`
		Source  *string `json:"source"`
	} `json:"recent"`
}

// analyticsExportTimeout bounds one export call (up to 8 MB read from the region).
const analyticsExportTimeout = 2 * time.Minute

func analyticsAPIPath(projectID string) string {
	return apiHost + "/api/v1/projects/" + url.PathEscape(projectID) + "/analytics"
}

func analyticsSitePath(projectID, siteID string) string {
	return analyticsAPIPath(projectID) + "/sites/" + url.PathEscape(siteID)
}

// analyticsSecretRequest runs a request whose response holds a secret (a new
// ingest key). api.go's debug redaction does not know the field, so debug
// output shows the request line only and never the response body.
func analyticsSecretRequest(method, requestURL, token string, body, out any) error {
	debugf("HTTP request: %s %s (response holds a secret; body not logged)", method, requestURL)
	previous := debugEnabled
	debugEnabled = false
	defer func() { debugEnabled = previous }()
	return doJSONRequest(method, requestURL, token, body, out)
}

func fetchAnalyticsRegions(token string) ([]analyticsRegion, error) {
	var payload []analyticsRegion
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/analytics/regions", token, nil, &payload)
	return payload, err
}

func createAnalyticsProject(token, name, regionID string) (project, error) {
	body := map[string]any{"name": name, "type": "analytics", "regionId": regionID}
	var payload project
	err := doJSONRequest(http.MethodPost, apiHost+"/api/v1/projects/create", token, body, &payload)
	return normalizeProject(payload), err
}

func fetchAnalyticsOverview(token, projectID string) (analyticsProjectOverview, error) {
	var payload analyticsProjectOverview
	err := doJSONRequest(http.MethodGet, analyticsAPIPath(projectID), token, nil, &payload)
	return payload, err
}

func fetchAnalyticsRoutes(token, projectID string) ([]analyticsServedRoute, error) {
	var payload []analyticsServedRoute
	err := doJSONRequest(http.MethodGet, analyticsAPIPath(projectID)+"/routes", token, nil, &payload)
	return payload, err
}

func fetchAnalyticsUsage(token, projectID string) (analyticsUsage, error) {
	var payload analyticsUsage
	err := doJSONRequest(http.MethodGet, analyticsAPIPath(projectID)+"/usage", token, nil, &payload)
	return payload, err
}

func verifyAnalyticsDomain(token, projectID, domain string) (analyticsDomainVerification, error) {
	var payload analyticsDomainVerification
	err := doJSONRequest(http.MethodPost, analyticsAPIPath(projectID)+"/domains/verify", token, map[string]any{"domain": domain}, &payload)
	return payload, err
}

func createAnalyticsSite(token, projectID string, body map[string]any) (analyticsSite, error) {
	var payload analyticsSite
	err := doJSONRequest(http.MethodPost, analyticsAPIPath(projectID)+"/sites/create", token, body, &payload)
	return payload, err
}

func updateAnalyticsSite(token, projectID, siteID string, body map[string]any) (analyticsSite, error) {
	var payload analyticsSite
	err := doJSONRequest(http.MethodPut, analyticsSitePath(projectID, siteID)+"/update", token, body, &payload)
	return payload, err
}

func deleteAnalyticsSite(token, projectID, siteID string) (analyticsDeleteResult, error) {
	var payload analyticsDeleteResult
	err := doJSONRequest(http.MethodDelete, analyticsSitePath(projectID, siteID)+"/delete", token, nil, &payload)
	return payload, err
}

func fetchAnalyticsSiteStatus(token, projectID, siteID string) (analyticsSiteStatus, error) {
	var payload analyticsSiteStatus
	err := doJSONRequest(http.MethodGet, analyticsSitePath(projectID, siteID)+"/status", token, nil, &payload)
	return payload, err
}

// runAnalyticsQuery posts a report query and decodes the result into out
// (analyticsAggregate, analyticsBreakdown, analyticsRealtime or any).
func runAnalyticsQuery(token, projectID, siteID string, query analyticsQuery, out any) error {
	return doJSONRequest(http.MethodPost, analyticsSitePath(projectID, siteID)+"/query", token, query, out)
}

func exportAnalyticsData(token, projectID, siteID, from, to, kind, format string) (analyticsExport, error) {
	values := url.Values{"from": {from}, "to": {to}}
	if kind != "" {
		values.Set("kind", kind)
	}
	if format != "" {
		values.Set("format", format)
	}
	var payload analyticsExport
	err := doJSONRequestWithTimeout(http.MethodGet, analyticsSitePath(projectID, siteID)+"/export?"+values.Encode(), token, nil, &payload, analyticsExportTimeout)
	return payload, err
}

func fetchAnalyticsIngestKeys(token, projectID, siteID string) ([]analyticsIngestKey, error) {
	var payload []analyticsIngestKey
	err := doJSONRequest(http.MethodGet, analyticsSitePath(projectID, siteID)+"/keys", token, nil, &payload)
	return payload, err
}

func createAnalyticsIngestKey(token, projectID, siteID, name string) (analyticsNewIngestKey, error) {
	var payload analyticsNewIngestKey
	err := analyticsSecretRequest(http.MethodPost, analyticsSitePath(projectID, siteID)+"/keys/create", token, map[string]any{"name": name}, &payload)
	return payload, err
}

func revokeAnalyticsIngestKey(token, projectID, siteID, keyID string) (analyticsIngestKey, error) {
	var payload analyticsIngestKey
	err := doJSONRequest(http.MethodPost, analyticsSitePath(projectID, siteID)+"/keys/"+url.PathEscape(keyID)+"/revoke", token, map[string]any{}, &payload)
	return payload, err
}
