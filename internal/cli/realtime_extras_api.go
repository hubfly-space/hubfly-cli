package cli

import (
	"net/http"
	"net/url"
	"strconv"
)

// Managed Realtime webhooks, live state (channels, connections, history) and
// daily usage: the Dashboard API under .../managed/instances/{instanceId}/realtime.
// Shapes follow the Dashboard's generated openapi.json.

type rtWebhook struct {
	ID      string   `json:"id"`
	AppID   string   `json:"appId"`
	URL     string   `json:"url"`
	Events  []string `json:"events"`
	Enabled bool     `json:"enabled"`
	// KeyID is the key whose secret signs the deliveries; null when that key
	// is gone and the app's newest active key (Key) signs instead.
	KeyID     *string `json:"keyId"`
	Key       *string `json:"key"`
	CreatedAt string  `json:"createdAt"`
	UpdatedAt string  `json:"updatedAt"`
}

type rtWebhookResult struct {
	Webhook rtWebhook `json:"webhook"`
	Sync    rtSync    `json:"sync"`
}

type rtWebhookDeleteResult struct {
	Deleted bool   `json:"deleted"`
	Sync    rtSync `json:"sync"`
}

type rtWebhookTestResult struct {
	OK         bool    `json:"ok"`
	StatusCode *int    `json:"statusCode"`
	DurationMs *int64  `json:"durationMs"`
	Error      *string `json:"error"`
}

type rtWebhookDelivery struct {
	ID        string `json:"id"`
	WebhookID string `json:"webhookId"`
	Status    string `json:"status"`
	Attempts  int64  `json:"attempts"`
	// Events is the number of events in the batch; FirstEvent names the first.
	Events         int64   `json:"events"`
	FirstEvent     *string `json:"firstEvent"`
	CreatedAt      *string `json:"createdAt"`
	LastAttemptAt  *string `json:"lastAttemptAt"`
	NextAttemptAt  *string `json:"nextAttemptAt"`
	DeliveredAt    *string `json:"deliveredAt"`
	LastStatusCode *int    `json:"lastStatusCode"`
	LastError      *string `json:"lastError"`
	DurationMs     *int64  `json:"durationMs"`
}

type rtWebhookDeliveryPage struct {
	Items      []rtWebhookDelivery `json:"items"`
	NextCursor *string             `json:"nextCursor"`
}

type rtWebhookRedeliverResult struct {
	Queued     bool   `json:"queued"`
	DeliveryID string `json:"deliveryId"`
}

type rtChannel struct {
	Name              string `json:"name"`
	Type              string `json:"type"`
	SubscriptionCount int64  `json:"subscriptionCount"`
	// UserCount is set for presence channels only.
	UserCount  *int64  `json:"userCount"`
	OccupiedAt *string `json:"occupiedAt"`
}

type rtChannelPage struct {
	Items      []rtChannel `json:"items"`
	NextCursor *string     `json:"nextCursor"`
	Total      *int64      `json:"total"`
}

type rtChannelMember struct {
	UserID string `json:"userId"`
	// UserInfo is the stored user_info as JSON text; a sealed end-to-end
	// envelope stays sealed.
	UserInfo    *string `json:"userInfo"`
	Connections int64   `json:"connections"`
}

type rtChannelHistoryState struct {
	Mode             string  `json:"mode"`
	Epoch            *string `json:"epoch"`
	Head             *int64  `json:"head"`
	Oldest           *int64  `json:"oldest"`
	RetainedMessages int64   `json:"retainedMessages"`
	RetainedBytes    int64   `json:"retainedBytes"`
}

type rtChannelCacheState struct {
	Present  bool    `json:"present"`
	Event    *string `json:"event"`
	CachedAt *string `json:"cachedAt"`
}

type rtChannelDetail struct {
	Name              string                 `json:"name"`
	Type              string                 `json:"type"`
	Occupied          bool                   `json:"occupied"`
	SubscriptionCount int64                  `json:"subscriptionCount"`
	UserCount         *int64                 `json:"userCount"`
	Members           []rtChannelMember      `json:"members"`
	MembersTruncated  bool                   `json:"membersTruncated"`
	History           *rtChannelHistoryState `json:"history"`
	Cache             *rtChannelCacheState   `json:"cache"`
}

type rtHistoryMessage struct {
	Offset      int64   `json:"offset"`
	Event       string  `json:"event"`
	Data        string  `json:"data"`
	MessageID   *string `json:"messageId"`
	PublishedAt *string `json:"publishedAt"`
	// UserID is the sender of a client event on a presence channel.
	UserID *string `json:"userId"`
	Size   int64   `json:"size"`
}

type rtHistory struct {
	Channel  string             `json:"channel"`
	Mode     string             `json:"mode"`
	Epoch    *string            `json:"epoch"`
	Head     *int64             `json:"head"`
	Oldest   *int64             `json:"oldest"`
	Messages []rtHistoryMessage `json:"messages"`
	// Truncated: older messages are retained beyond the limit.
	Truncated bool `json:"truncated"`
	Gap       bool `json:"gap"`
}

type rtConnection struct {
	SocketID string  `json:"socketId"`
	AppID    string  `json:"appId"`
	UserID   *string `json:"userId"`
	Protocol string  `json:"protocol"`
	Client   struct {
		Name    *string `json:"name"`
		Version *string `json:"version"`
	} `json:"client"`
	IP                *string  `json:"ip"`
	Origin            *string  `json:"origin"`
	State             string   `json:"state"`
	ConnectedAt       *string  `json:"connectedAt"`
	SubscriptionCount int64    `json:"subscriptionCount"`
	Subscriptions     []string `json:"subscriptions"`
	MessagesIn        int64    `json:"messagesIn"`
	MessagesOut       int64    `json:"messagesOut"`
	BytesIn           int64    `json:"bytesIn"`
	BytesOut          int64    `json:"bytesOut"`
}

type rtConnectionPage struct {
	Items      []rtConnection `json:"items"`
	NextCursor *string        `json:"nextCursor"`
}

type rtTerminateResult struct {
	Terminated bool   `json:"terminated"`
	Mode       string `json:"mode"`
}

type rtUsageDay struct {
	Day               string `json:"day"`
	AppID             string `json:"appId"`
	PeakConnections   int64  `json:"peakConnections"`
	ConnectionMinutes int64  `json:"connectionMinutes"`
	MessagesIn        int64  `json:"messagesIn"`
	MessagesOut       int64  `json:"messagesOut"`
	BytesIn           int64  `json:"bytesIn"`
	BytesOut          int64  `json:"bytesOut"`
	WebhookDeliveries int64  `json:"webhookDeliveries"`
}

type rtUsage struct {
	Days []rtUsageDay `json:"days"`
}

func rtWebhookPath(projectID, instanceID, appID, webhookID string) string {
	return rtAppPath(projectID, instanceID, appID) + "/webhooks/" + url.PathEscape(webhookID)
}

func withRealtimeQuery(requestURL string, query url.Values) string {
	if len(query) == 0 {
		return requestURL
	}
	return requestURL + "?" + query.Encode()
}

// fetchRealtimeWebhookEvents returns the webhook event names the Dashboard
// accepts (managed catalog realtime.webhookEvents); empty when it names none.
func fetchRealtimeWebhookEvents(token string) ([]string, error) {
	var payload struct {
		Realtime struct {
			WebhookEvents []string `json:"webhookEvents"`
		} `json:"realtime"`
	}
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/managed/catalog", token, nil, &payload)
	return payload.Realtime.WebhookEvents, err
}

func fetchRealtimeWebhooks(token, projectID, instanceID, appID string) ([]rtWebhook, error) {
	var payload struct {
		Items []rtWebhook `json:"items"`
	}
	err := doJSONRequest(http.MethodGet, rtAppPath(projectID, instanceID, appID)+"/webhooks", token, nil, &payload)
	return payload.Items, err
}

func createRealtimeWebhook(token, projectID, instanceID, appID string, body map[string]any) (rtWebhookResult, error) {
	var payload rtWebhookResult
	err := doJSONRequest(http.MethodPost, rtAppPath(projectID, instanceID, appID)+"/webhooks/create", token, body, &payload)
	return payload, err
}

func updateRealtimeWebhook(token, projectID, instanceID, appID, webhookID string, body map[string]any) (rtWebhookResult, error) {
	var payload rtWebhookResult
	err := doJSONRequest(http.MethodPut, rtWebhookPath(projectID, instanceID, appID, webhookID)+"/update", token, body, &payload)
	return payload, err
}

func deleteRealtimeWebhook(token, projectID, instanceID, appID, webhookID string) (rtWebhookDeleteResult, error) {
	var payload rtWebhookDeleteResult
	err := doJSONRequest(http.MethodDelete, rtWebhookPath(projectID, instanceID, appID, webhookID)+"/delete", token, nil, &payload)
	return payload, err
}

func testRealtimeWebhook(token, projectID, instanceID, appID, webhookID string) (rtWebhookTestResult, error) {
	var payload rtWebhookTestResult
	err := doJSONRequest(http.MethodPost, rtWebhookPath(projectID, instanceID, appID, webhookID)+"/test", token, map[string]any{}, &payload)
	return payload, err
}

func fetchRealtimeWebhookDeliveries(token, projectID, instanceID, appID string, query url.Values) (rtWebhookDeliveryPage, error) {
	var payload rtWebhookDeliveryPage
	requestURL := withRealtimeQuery(rtAppPath(projectID, instanceID, appID)+"/webhooks/deliveries", query)
	err := doJSONRequest(http.MethodGet, requestURL, token, nil, &payload)
	return payload, err
}

func redeliverRealtimeWebhookDelivery(token, projectID, instanceID, appID, deliveryID string) (rtWebhookRedeliverResult, error) {
	var payload rtWebhookRedeliverResult
	requestURL := rtAppPath(projectID, instanceID, appID) + "/webhooks/deliveries/" + url.PathEscape(deliveryID) + "/redeliver"
	err := doJSONRequest(http.MethodPost, requestURL, token, map[string]any{}, &payload)
	return payload, err
}

func fetchRealtimeChannels(token, projectID, instanceID, appID string, query url.Values) (rtChannelPage, error) {
	var payload rtChannelPage
	requestURL := withRealtimeQuery(rtAppPath(projectID, instanceID, appID)+"/channels", query)
	err := doJSONRequest(http.MethodGet, requestURL, token, nil, &payload)
	return payload, err
}

func inspectRealtimeChannel(token, projectID, instanceID, appID, channel string, membersLimit int) (rtChannelDetail, error) {
	query := url.Values{"channel": {channel}}
	if membersLimit > 0 {
		query.Set("membersLimit", strconv.Itoa(membersLimit))
	}
	var payload rtChannelDetail
	requestURL := withRealtimeQuery(rtAppPath(projectID, instanceID, appID)+"/channels/inspect", query)
	err := doJSONRequest(http.MethodGet, requestURL, token, nil, &payload)
	return payload, err
}

func fetchRealtimeHistory(token, projectID, instanceID, appID, channel string, limit int) (rtHistory, error) {
	query := url.Values{"channel": {channel}}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	var payload rtHistory
	requestURL := withRealtimeQuery(rtAppPath(projectID, instanceID, appID)+"/history", query)
	err := doJSONRequest(http.MethodGet, requestURL, token, nil, &payload)
	return payload, err
}

// purgeRealtimeHistory deletes the retained messages of one channel, or of
// every channel of the app when channel is empty.
func purgeRealtimeHistory(token, projectID, instanceID, appID, channel string) (int64, error) {
	body := map[string]any{}
	if channel != "" {
		body["channel"] = channel
	}
	var payload struct {
		PurgedMessages int64 `json:"purgedMessages"`
	}
	err := doJSONRequest(http.MethodPost, rtAppPath(projectID, instanceID, appID)+"/history/purge", token, body, &payload)
	return payload.PurgedMessages, err
}

func fetchRealtimeConnections(token, projectID, instanceID string, query url.Values) (rtConnectionPage, error) {
	var payload rtConnectionPage
	requestURL := withRealtimeQuery(rtInstancePath(projectID, instanceID)+"/realtime/connections", query)
	err := doJSONRequest(http.MethodGet, requestURL, token, nil, &payload)
	return payload, err
}

// terminateRealtimeConnection closes one connection; an empty mode leaves the
// choice to the Dashboard (terminate).
func terminateRealtimeConnection(token, projectID, instanceID, appID, socketID, mode string) (rtTerminateResult, error) {
	body := map[string]any{"socketId": socketID}
	if mode != "" {
		body["mode"] = mode
	}
	var payload rtTerminateResult
	err := doJSONRequest(http.MethodPost, rtAppPath(projectID, instanceID, appID)+"/connections/terminate", token, body, &payload)
	return payload, err
}

func terminateRealtimeUser(token, projectID, instanceID, appID, userID string) (int64, error) {
	var payload struct {
		Terminated int64 `json:"terminated"`
	}
	err := doJSONRequest(http.MethodPost, rtAppPath(projectID, instanceID, appID)+"/users/terminate", token, map[string]any{"userId": userID}, &payload)
	return payload.Terminated, err
}

// fetchRealtimeUsage returns daily usage; since is a YYYY-MM-DD date (empty:
// the Dashboard default of 30 days).
func fetchRealtimeUsage(token, projectID, instanceID, appID, since string) (rtUsage, error) {
	query := url.Values{}
	if appID != "" {
		query.Set("appId", appID)
	}
	if since != "" {
		query.Set("since", since)
	}
	var payload rtUsage
	requestURL := withRealtimeQuery(rtInstancePath(projectID, instanceID)+"/realtime/usage", query)
	err := doJSONRequest(http.MethodGet, requestURL, token, nil, &payload)
	return payload, err
}
