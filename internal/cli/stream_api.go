package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Dashboard API of Managed Streams (extra/managed-streams/DASHBOARD-API.md;
// the generated openapi.json is the source of truth). Every stream route
// lives under /api/v1/projects/{projectId}/managed/instances/{instanceId}/stream.

const managedServiceStream = "stream"

// streamServer is StreamServerSummary plus the settings of GET …/stream.
type streamServer struct {
	Host           string `json:"host"`
	SpeedMbps      int    `json:"speedMbps"`
	ProcessingMbps int    `json:"processingMbps"`
	WebrtcSessions int    `json:"webrtcSessions"`
	Timezone       string `json:"timezone"`
	Sync           struct {
		Generation        int     `json:"generation"`
		AppliedGeneration int     `json:"appliedGeneration"`
		LastSyncedAt      *string `json:"lastSyncedAt"`
		LastError         *string `json:"lastError"`
	} `json:"sync"`
	Billing struct {
		Allowed bool    `json:"allowed"`
		Reason  *string `json:"reason"`
	} `json:"billing"`
	Ingest struct {
		RTMP        string `json:"rtmp"`
		RTMPS       string `json:"rtmps"`
		SRT         string `json:"srt"`
		WhipBase    string `json:"whipBase"`
		IcecastBase string `json:"icecastBase"`
	} `json:"ingest"`
	Live *struct {
		InputsLive   int     `json:"inputsLive"`
		ChannelsLive int     `json:"channelsLive"`
		Viewers      int     `json:"viewers"`
		EgressMbps   float64 `json:"egressMbps"`
		At           string  `json:"at"`
	} `json:"live"`
	Price struct {
		SpeedMicroUsdPerHour float64 `json:"speedMicroUsdPerHour"`
		SpeedUsdPerMonth     float64 `json:"speedUsdPerMonth"`
	} `json:"price"`
	OfflineSlate   *string  `json:"offlineSlate"`
	AllowedOrigins []string `json:"allowedOrigins"`
	StudioOrigins  []string `json:"studioOrigins"`
}

// streamPriceEstimate is GET /api/v1/managed/streams/price.
type streamPriceEstimate struct {
	Hourly struct {
		SpeedMicroUsd          float64 `json:"speedMicroUsd"`
		SpeedMicroUsdAfterFree float64 `json:"speedMicroUsdAfterFree"`
	} `json:"hourly"`
	Monthly struct {
		SpeedUsd          float64 `json:"speedUsd"`
		SpeedUsdAfterFree float64 `json:"speedUsdAfterFree"`
	} `json:"monthly"`
	FreeSpeedMbps float64 `json:"freeSpeedMbps"`
	Pricing       struct {
		ProcessingMicroUsdPerMbpsHour float64 `json:"processingMicroUsdPerMbpsHour"`
		BandwidthMicroUsdPerGb        float64 `json:"bandwidthMicroUsdPerGb"`
	} `json:"pricing"`
}

// quotedHourlyTotal is the list price per hour in USD that create and resize
// send back (DASHBOARD-API.md "Speed quotes").
func (p streamPriceEstimate) quotedHourlyTotal() float64 {
	return p.Hourly.SpeedMicroUsd / 1e6
}

type streamCreateResult struct {
	Instance    rtInstance `json:"instance"`
	OperationID string     `json:"operationId"`
	Price       rtPrice    `json:"price"`
	Stream      *struct {
		Host       string `json:"host"`
		FirstInput *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Keys struct {
				Primary string `json:"primary"`
			} `json:"keys"`
		} `json:"firstInput"`
	} `json:"stream"`
}

// streamStudioToken is POST …/stream/studio-token/create.
type streamStudioToken struct {
	Token     string   `json:"token"`
	URL       string   `json:"url"`
	ExpiresAt string   `json:"expiresAt"`
	Scopes    []string `json:"scopes"`
}

// streamPlayback is GET …/stream/channels/{id}/playback.
type streamPlayback struct {
	URLs struct {
		HLS   string `json:"hls"`
		WHEP  string `json:"whep"`
		Embed string `json:"embed"`
		Thumb string `json:"thumb"`
		State string `json:"status"`
		Audio []struct {
			Format string `json:"format"`
			URL    string `json:"url"`
		} `json:"audio"`
		RTMPPull string `json:"rtmpPull,omitempty"`
		SRTPull  string `json:"srtPull,omitempty"`
	} `json:"urls"`
	EmbedHTML     string `json:"embedHtml"`
	TokenRequired bool   `json:"tokenRequired"`
}

// streamInputKeys is POST …/stream/inputs/{id}/keys/reveal.
type streamInputKeys struct {
	Primary       string `json:"primary,omitempty"`
	Backup        string `json:"backup,omitempty"`
	SRTPassphrase string `json:"srtPassphrase,omitempty"`
	PullURL       string `json:"pullUrl,omitempty"`
}

func streamPath(instance rtInstance) string {
	return rtInstancePath(instance.ProjectID, instance.ID) + "/stream"
}

// streamCall sends one request to …/stream<suffix> and decodes the envelope's
// data into out (raw JSON when out is *json.RawMessage).
func streamCall(token string, instance rtInstance, method, suffix string, query url.Values, body any, out any) error {
	target := streamPath(instance) + suffix
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	return doJSONRequest(method, target, token, body, out)
}

func streamGetRaw(token string, instance rtInstance, suffix string, query url.Values) (any, error) {
	var payload any
	err := streamCall(token, instance, http.MethodGet, suffix, query, nil, &payload)
	return payload, err
}

func fetchStreamServer(token string, instance rtInstance) (streamServer, error) {
	var payload streamServer
	err := streamCall(token, instance, http.MethodGet, "", nil, nil, &payload)
	return payload, err
}

func fetchStreamPrice(token string, speedMbps, processingMbps int) (streamPriceEstimate, error) {
	query := url.Values{"speedMbps": {fmt.Sprint(speedMbps)}}
	if processingMbps > 0 {
		query.Set("processingMbps", fmt.Sprint(processingMbps))
	}
	var payload streamPriceEstimate
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/managed/streams/price?"+query.Encode(), token, nil, &payload)
	return payload, err
}

func createStreamStudioToken(token string, instance rtInstance, origin string) (streamStudioToken, error) {
	var payload streamStudioToken
	err := streamCall(token, instance, http.MethodPost, "/studio-token/create", nil, map[string]any{"origin": origin}, &payload)
	return payload, err
}

func fetchStreamPlayback(token string, instance rtInstance, channelID string) (streamPlayback, error) {
	var payload streamPlayback
	err := streamCall(token, instance, http.MethodGet, "/channels/"+url.PathEscape(channelID)+"/playback", nil, nil, &payload)
	return payload, err
}

func createStreamTestToken(token string, instance rtInstance, channelID string, body map[string]any) (string, string, error) {
	var payload struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	err := streamCall(token, instance, http.MethodPost, "/channels/"+url.PathEscape(channelID)+"/token/create", nil, body, &payload)
	return payload.Token, payload.ExpiresAt, err
}

func revealStreamInputKeys(token string, instance rtInstance, inputID string) (streamInputKeys, error) {
	var payload streamInputKeys
	err := streamCall(token, instance, http.MethodPost, "/inputs/"+url.PathEscape(inputID)+"/keys/reveal", nil, map[string]any{}, &payload)
	return payload, err
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// streamErrorHints are the next steps for the STREAM_* codes of the Dashboard.
var streamErrorHints = map[string]string{
	"STREAM_CAPACITY_FULL":           "No stream node has free capacity for this speed right now. Choose a lower speed or try again later.",
	"STREAM_SPEED_LIMIT":             "Speeds above 250 Mbps need an account with a real deposit. Add funds in the Dashboard (Billing) or choose a lower speed.",
	"MANAGED_INSTANCE_LIMIT":         "This account reached its stream server limit (5 by default). Delete one or contact support.",
	"STREAM_REVISION_CONFLICT":       "Someone changed it in the meantime. Run the command again to update the newest version.",
	"STREAM_IN_USE":                  "Other resources still use it (see above). Remove those references first.",
	"STREAM_NODE_UNAVAILABLE":        "The stream node did not answer. Try again in a few seconds.",
	"STREAM_NODE_CONFLICT":           "The stream server changed on its node. Try again in a few seconds.",
	"STREAM_NOT_LIVE":                "This needs the channel to be live (clips also need DVR on the channel).",
	"STREAM_PLAYBACK_KEY_MISSING":    "Create a playback key first: hubfly stream playback-keys create <server> --name <name>",
	"STREAM_PLAYBACK_KEY_NOT_ACTIVE": "That playback key is retiring. Use an active key or create a new one.",
	"STREAM_KEY_MISSING":             "This input has no key for that role. Rotate one in: hubfly stream inputs keys rotate <server> <input> --role <role>",
	"STREAM_INVALID_MONTH":           "Use --month YYYY-MM.",
	"STREAM_INVALID_TIMEZONE":        "Use an IANA time zone such as Africa/Kigali or America/New_York.",
	"STREAM_NAME_TAKEN":              "Choose another name.",
	"STREAM_DOMAIN_TAKEN":            "That hostname is already used by another stream server.",
	"STREAM_PROVISIONING":            "The stream server is still being created. Wait for it to be running.",
	"STREAM_SECRETS_UNAVAILABLE":     "Secrets cannot be read right now. Try again later.",
	"STREAM_STUDIO_ORIGIN":           "The stream server refused the studio origin.",
	"STREAM_TOO_LARGE":               "The request is too large for the stream node.",
	"MANAGED_FEATURE_UNSUPPORTED":    "That managed instance is not a stream server.",
	"MANAGED_INSTANCE_NOT_RUNNING":   "Start the stream server first: hubfly stream start <server>",
	"MANAGED_PRICE_CHANGED":          "Run the command again to see the new price.",
}

// streamAPIError adds validation details and a hint to Dashboard errors. The
// wrapped *apiError keeps exit codes intact. Messages pass through the secret
// masks, so an echoed key never reaches the terminal.
func streamAPIError(err error) error {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return err
	}
	lines := []string{}
	if details, ok := apiErr.Details.([]any); ok {
		for _, item := range details {
			problem, _ := item.(map[string]any)
			if problem == nil {
				continue
			}
			path, _ := problem["path"].(string)
			message, _ := problem["message"].(string)
			code, _ := problem["code"].(string)
			line := "  - "
			if path != "" {
				line += path + ": "
			}
			line += redactStreamSecretText(message)
			if code != "" {
				line += " (" + code + ")"
			}
			lines = append(lines, terminalSafe(line))
		}
		if apiErr.Code == "STREAM_IN_USE" && len(lines) == 0 {
			lines = append(lines, "  - "+terminalSafe(fmt.Sprint(redactStreamSecrets(apiErr.Details))))
		}
	}
	if hint := streamErrorHints[apiErr.Code]; hint != "" {
		lines = append(lines, hint)
	}
	if len(lines) == 0 {
		return err
	}
	return fmt.Errorf("%w\n%s", err, strings.Join(lines, "\n"))
}

// ---------------------------------------------------------------------------
// Secret masking
// ---------------------------------------------------------------------------

const streamMasked = "[masked]"

var (
	// hfls_<input id>_<secret>: keep the input id, hide the secret.
	streamKeyPattern     = regexp.MustCompile(`hfls_([A-Za-z0-9]+)_[A-Za-z0-9]+`)
	streamWebhookPattern = regexp.MustCompile(`whsec_[A-Za-z0-9_\-]+`)
	// JWS/JWT tokens (playback and studio tokens).
	streamJWTPattern = regexp.MustCompile(`eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+`)
)

// streamSecretFields are keys whose string values are secrets. Hints and
// fingerprints are not secrets and stay visible.
var streamSecretFields = map[string]bool{
	"secret": true, "token": true, "key": true, "primary": true, "backup": true,
	"streamkey": true, "srtpassphrase": true, "passphrase": true, "password": true,
	"bearertoken": true, "authorization": true, "pullurl": true, "secretaccesskey": true,
}

// redactStreamSecrets returns a copy of an API payload with stream keys,
// tokens, webhook secrets, passphrases and URL credentials masked. Nothing
// the CLI prints passes around it except an explicit reveal.
func redactStreamSecrets(value any) any {
	return redactStreamValue("", value)
}

func redactStreamValue(field string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		parent := strings.ToLower(field)
		hints := strings.HasSuffix(parent, "hints") || strings.HasSuffix(parent, "fingerprints")
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			if text, ok := child.(string); ok && !hints && streamSecretFields[strings.ToLower(key)] && text != "" {
				result[key] = streamMasked
				continue
			}
			result[key] = redactStreamValue(key, child)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = redactStreamValue(field, child)
		}
		return result
	case string:
		return redactStreamSecretText(typed)
	}
	return value
}

// redactStreamSecretText masks secrets inside free text: stream keys, webhook
// secrets, JWTs, URL user info and secret-looking query parameters.
func redactStreamSecretText(text string) string {
	if text == "" {
		return text
	}
	text = streamKeyPattern.ReplaceAllString(text, "hfls_${1}_"+streamMasked)
	text = streamWebhookPattern.ReplaceAllString(text, "whsec_"+streamMasked)
	text = streamJWTPattern.ReplaceAllString(text, streamMasked)
	if !strings.Contains(text, "://") || strings.ContainsAny(text, " \n\t") {
		return text
	}
	parsed, err := url.Parse(text)
	if err != nil || parsed.Scheme == "" {
		return text
	}
	changed := false
	if parsed.User != nil {
		if _, hasPassword := parsed.User.Password(); hasPassword {
			parsed.User = url.UserPassword(parsed.User.Username(), "masked")
			changed = true
		}
	}
	if parsed.RawQuery != "" {
		query := parsed.Query()
		keys := make([]string, 0, len(query))
		for key := range query {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") || strings.Contains(lower, "key") || strings.Contains(lower, "secret") ||
				lower == "passphrase" || lower == "streamid" || lower == "password" {
				query.Set(key, streamMasked)
				changed = true
			}
		}
		if changed {
			parsed.RawQuery = query.Encode()
		}
	}
	if !changed {
		return text
	}
	return parsed.String()
}

// streamPrintJSON prints an API payload with secrets masked.
func streamPrintJSON(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var generic any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		return err
	}
	return rtPrintJSON(redactStreamSecrets(generic))
}
