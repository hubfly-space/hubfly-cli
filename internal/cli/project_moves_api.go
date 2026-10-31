package cli

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// Project moves on the Dashboard API: transfers to another owner
// (/api/v1/projects/{projectId}/transfer/…, /api/v1/project-transfers/…) and
// the project history. Region migration routes (/migration/…, /rehome/…,
// /migration-batches/…) go in the "Region migration" section at the end of
// this file. Shapes follow hubfly-dashboard/shared/project-moves.ts; only
// the fields the CLI prints are decoded, and --json prints the raw `data`
// object so no field is lost.

// projectJobView is the long-running job behind a transfer or a migration.
type projectJobView struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Status    string   `json:"status"`
	Phase     string   `json:"phase"`
	Lock      *string  `json:"lock"`
	Step      *string  `json:"step"`
	StepLabel *string  `json:"stepLabel"`
	StepIndex int      `json:"stepIndex"`
	StepCount int      `json:"stepCount"`
	Progress  *float64 `json:"progress"`
	ETA       *int     `json:"etaSeconds"`
	ErrorCode *string  `json:"errorCode"`
	ErrorMsg  *string  `json:"errorMessage"`
	NextRunAt *string  `json:"nextRunAt"`
}

type ownerRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// moveIssue is a blocker, warning or note of a transfer or migration preview.
type moveIssue struct {
	Code     string         `json:"code"`
	Severity string         `json:"severity"`
	Message  string         `json:"message"`
	Detail   map[string]any `json:"detail,omitempty"`
}

type moveCount struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

type projectTransferView struct {
	ID             string          `json:"id"`
	ProjectID      *string         `json:"projectId"`
	ProjectName    string          `json:"projectName"`
	ProjectType    string          `json:"projectType"`
	Status         string          `json:"status"`
	RecipientEmail string          `json:"recipientEmail"`
	From           ownerRef        `json:"from"`
	To             *ownerRef       `json:"to"`
	ExpiresAt      string          `json:"expiresAt"`
	CreatedAt      string          `json:"createdAt"`
	AcceptedAt     *string         `json:"acceptedAt"`
	CompletedAt    *string         `json:"completedAt"`
	HandoverAt     *string         `json:"handoverAt"`
	Job            *projectJobView `json:"job"`
	FailureCode    *string         `json:"failureCode"`
	FailureMessage *string         `json:"failureMessage"`
}

type projectTransferPreview struct {
	ProjectID                  string      `json:"projectId"`
	ProjectName                string      `json:"projectName"`
	ProjectType                string      `json:"projectType"`
	CanTransfer                bool        `json:"canTransfer"`
	Blockers                   []moveIssue `json:"blockers"`
	Warnings                   []moveIssue `json:"warnings"`
	Moves                      []moveCount `json:"moves"`
	RevokedIfSenderLosesAccess []moveCount `json:"revokedIfSenderLosesAccess"`
	EnvValueCount              int         `json:"envValueCount"`
	RequiresOTP                bool        `json:"requiresOtp"`
}

type projectTransferOTP struct {
	SentTo    string `json:"sentTo"`
	ExpiresAt string `json:"expiresAt"`
}

type projectTransferOfferSummary struct {
	TransferID  string  `json:"transferId"`
	BatchID     *string `json:"batchId"`
	ProjectName string  `json:"projectName"`
	ProjectType string  `json:"projectType"`
	SenderName  string  `json:"senderName"`
	ExpiresAt   string  `json:"expiresAt"`
	Status      string  `json:"status"`
}

type projectTransferList struct {
	Incoming []projectTransferOfferSummary `json:"incoming"`
	Outgoing []projectTransferView         `json:"outgoing"`
}

type projectTransferOfferMember struct {
	UserID      string   `json:"userId"`
	Name        string   `json:"name"`
	Email       string   `json:"email"`
	Permissions []string `json:"permissions"`
	Credentials []string `json:"credentials"`
}

type projectTransferOfferProject struct {
	TransferID  string `json:"transferId"`
	ProjectName string `json:"projectName"`
	ProjectType string `json:"projectType"`
	Hosting     struct {
		Hostnames     []string `json:"hostnames"`
		CustomDomains []string `json:"customDomains"`
		MailDomains   []string `json:"mailDomains"`
		Buckets       int      `json:"buckets"`
		Containers    int      `json:"containers"`
		Volumes       int      `json:"volumes"`
		Boxes         int      `json:"boxes"`
	} `json:"hosting"`
	Members []projectTransferOfferMember `json:"members"`
}

type projectTransferDestination struct {
	Type     string  `json:"type"`
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Eligible bool    `json:"eligible"`
	Reason   *string `json:"reason"`
}

type projectTransferOffer struct {
	BatchID      *string                       `json:"batchId"`
	SenderName   string                        `json:"senderName"`
	ExpiresAt    string                        `json:"expiresAt"`
	Status       string                        `json:"status"`
	Projects     []projectTransferOfferProject `json:"projects"`
	Destinations []projectTransferDestination  `json:"destinations"`
}

type projectTransferBilledPlan struct {
	Kind           string `json:"kind"`
	Label          string `json:"label"`
	AmountMicroUSD int64  `json:"amountMicroUsd"`
	Period         string `json:"period"`
}

type projectTransferAcceptancePreview struct {
	Eligible                bool                        `json:"eligible"`
	Blockers                []moveIssue                 `json:"blockers"`
	Warnings                []moveIssue                 `json:"warnings"`
	BalanceMicroUSD         int64                       `json:"balanceMicroUsd"`
	RequiredBalanceMicroUSD int64                       `json:"requiredBalanceMicroUsd"`
	HourlyBurnMicroUSD      int64                       `json:"hourlyBurnMicroUsd"`
	BilledPlans             []projectTransferBilledPlan `json:"billedPlans"`
	CredentialPolicy        string                      `json:"credentialPolicy"`
	Revoked                 []moveCount                 `json:"revoked"`
}

// projectTransferDestinationInput names where an accepted transfer goes:
// type "user" (your personal account) or "organization" with its id.
type projectTransferDestinationInput struct {
	Type string
	ID   string
}

type projectTransferKeepMembers struct {
	TransferID string   `json:"transferId"`
	UserIDs    []string `json:"userIds"`
}

type projectHistoryEntry struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"`
	Status string  `json:"status"`
	Title  string  `json:"title"`
	Detail *string `json:"detail"`
	Actor  *string `json:"actor"`
	At     string  `json:"at"`
	Href   *string `json:"href"`
}

type projectHistory struct {
	Entries []projectHistoryEntry `json:"entries"`
}

// projectMovesCall sends one Dashboard request and returns the raw `data`
// object (null when the API answered null). decode, when set, receives the
// same data.
func projectMovesCall(method, path, token string, body any, decode any) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := doJSONRequest(method, apiHost+path, token, body, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	if decode != nil {
		if err := json.Unmarshal(raw, decode); err != nil {
			return nil, err
		}
	}
	return raw, nil
}

func projectMovesProjectPath(projectID, suffix string) string {
	return "/api/v1/projects/" + url.PathEscape(projectID) + suffix
}

func projectTransferOfferPath(token, suffix string) string {
	return "/api/v1/project-transfers/" + url.PathEscape(token) + suffix
}

// ---------------------------------------------------------------------------
// Project transfer
// ---------------------------------------------------------------------------

// fetchProjectTransfer returns the open transfer of the project, or its
// latest one; nil when the project never had one.
func fetchProjectTransfer(token, projectID string) (*projectTransferView, json.RawMessage, error) {
	var view *projectTransferView
	raw, err := projectMovesCall(http.MethodGet, projectMovesProjectPath(projectID, "/transfer"), token, nil, &view)
	return view, raw, err
}

func previewProjectTransfer(token, projectID string) (projectTransferPreview, error) {
	var preview projectTransferPreview
	_, err := projectMovesCall(http.MethodGet, projectMovesProjectPath(projectID, "/transfer/preview"), token, nil, &preview)
	return preview, err
}

// createProjectTransferOtp emails the sender a code bound to this project
// and recipient.
func createProjectTransferOtp(token, projectID, recipientEmail string) (projectTransferOTP, error) {
	var sent projectTransferOTP
	_, err := projectMovesCall(http.MethodPost, projectMovesProjectPath(projectID, "/transfer/otp/create"), token, map[string]any{
		"recipientEmail": recipientEmail,
	}, &sent)
	return sent, err
}

// createProjectTransfer offers the project to recipientEmail. rotate asks
// for every credential of the project to be revoked at the handover
// (credentialPolicy "rotate"); otherwise the API decides ("auto").
func createProjectTransfer(token, projectID, recipientEmail, confirmName, otp string, rotate bool) (projectTransferView, json.RawMessage, error) {
	body := map[string]any{
		"recipientEmail": recipientEmail,
		"confirmName":    confirmName,
		"otp":            otp,
	}
	if rotate {
		body["options"] = map[string]any{"credentialPolicy": "rotate"}
	}
	var view projectTransferView
	raw, err := projectMovesCall(http.MethodPost, projectMovesProjectPath(projectID, "/transfer/create"), token, body, &view)
	return view, raw, err
}

func cancelProjectTransfer(token, projectID string) (projectTransferView, json.RawMessage, error) {
	var view projectTransferView
	raw, err := projectMovesCall(http.MethodPost, projectMovesProjectPath(projectID, "/transfer/cancel"), token, map[string]any{}, &view)
	return view, raw, err
}

// listProjectTransfers returns offers waiting for you (without tokens: those
// are only in the email) and transfers you started.
func listProjectTransfers(token string) (projectTransferList, json.RawMessage, error) {
	var list projectTransferList
	raw, err := projectMovesCall(http.MethodGet, "/api/v1/project-transfers", token, nil, &list)
	return list, raw, err
}

func fetchProjectTransferOffer(token, offerToken string) (projectTransferOffer, json.RawMessage, error) {
	var offer projectTransferOffer
	raw, err := projectMovesCall(http.MethodGet, projectTransferOfferPath(offerToken, ""), token, nil, &offer)
	return offer, raw, err
}

func previewProjectTransferAcceptance(token, offerToken string, destination projectTransferDestinationInput, transferIDs []string) (projectTransferAcceptancePreview, json.RawMessage, error) {
	query := url.Values{}
	query.Set("destinationType", destination.Type)
	if destination.ID != "" {
		query.Set("destinationId", destination.ID)
	}
	for _, id := range transferIDs {
		query.Add("transferIds", id)
	}
	var preview projectTransferAcceptancePreview
	raw, err := projectMovesCall(http.MethodGet, projectTransferOfferPath(offerToken, "/preview?"+query.Encode()), token, nil, &preview)
	return preview, raw, err
}

func acceptProjectTransfer(token, offerToken string, destination projectTransferDestinationInput, transferIDs []string, keep []projectTransferKeepMembers) ([]projectTransferView, json.RawMessage, error) {
	body := map[string]any{"destinationType": destination.Type}
	if destination.ID != "" {
		body["destinationId"] = destination.ID
	}
	if len(transferIDs) > 0 {
		body["transferIds"] = transferIDs
	}
	if len(keep) > 0 {
		body["keepMembers"] = keep
	}
	var views []projectTransferView
	raw, err := projectMovesCall(http.MethodPost, projectTransferOfferPath(offerToken, "/accept"), token, body, &views)
	return views, raw, err
}

func declineProjectTransfer(token, offerToken string) error {
	_, err := projectMovesCall(http.MethodPost, projectTransferOfferPath(offerToken, "/decline"), token, map[string]any{}, nil)
	return err
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

func fetchProjectHistory(token, projectID string) (projectHistory, json.RawMessage, error) {
	var history projectHistory
	raw, err := projectMovesCall(http.MethodGet, projectMovesProjectPath(projectID, "/history"), token, nil, &history)
	return history, raw, err
}

// projectTransferTokenPrefix starts every offer token
// (hub_project_transfer_<64 hex>.v1).
const projectTransferTokenPrefix = "hub_project_transfer_"

// normalizeProjectTransferToken accepts the token or the whole link from the
// offer email (…/projects/transfers/<token>) and returns the token.
func normalizeProjectTransferToken(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		value = segments[len(segments)-1]
	}
	hexPart, ok := strings.CutPrefix(value, projectTransferTokenPrefix)
	if !ok {
		return "", false
	}
	hexPart, ok = strings.CutSuffix(hexPart, ".v1")
	if !ok || len(hexPart) != 64 {
		return "", false
	}
	for _, r := range hexPart {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return "", false
		}
	}
	return value, true
}

// ---------------------------------------------------------------------------
// Region migration
// ---------------------------------------------------------------------------
//
// Migration, re-home and batch requests (CONTRACTS §10.1, WP-C2) go here and
// reuse projectMovesCall, projectMovesProjectPath and projectJobView.
