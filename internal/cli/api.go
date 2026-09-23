package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func fetchWhoAmI(token string) (user, error) {
	var u user
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/auth/me", token, nil, &u)
	return u, err
}

type deviceLoginStart struct {
	DeviceCode      string `json:"deviceCode"`
	UserCode        string `json:"userCode"`
	VerificationURL string `json:"verificationUrl"`
	ExpiresIn       int    `json:"expiresIn"`
	Interval        int    `json:"interval"`
}

type deviceLoginToken struct {
	Status string `json:"status"`
	Token  string `json:"token,omitempty"`
}

func startDeviceLogin() (deviceLoginStart, error) {
	var payload deviceLoginStart
	err := doJSONRequest(http.MethodPost, apiHost+"/api/v1/cli/auth/device/start", "", struct {
		Scopes []string `json:"scopes"`
	}{Scopes: []string{}}, &payload)
	return payload, err
}

func pollDeviceLogin(deviceCode string) (deviceLoginToken, error) {
	var payload deviceLoginToken
	err := doJSONRequest(http.MethodPost, apiHost+"/api/v1/cli/auth/device/token", "", struct {
		DeviceCode string `json:"deviceCode"`
	}{DeviceCode: deviceCode}, &payload)
	return payload, err
}

func revokeCurrentToken(token string) error {
	return doJSONRequest(http.MethodPost, apiHost+"/api/v1/cli/auth/logout", token, struct{}{}, nil)
}

func fetchProjects(token string) ([]project, error) {
	return fetchAllProjects(token, "")
}

func fetchProjectsWithOrg(token, orgID string) ([]project, error) {
	return fetchAllProjects(token, orgID)
}

func fetchAllProjects(token, orgID string) ([]project, error) {
	projects := make([]project, 0)
	for page := 1; ; page++ {
		query := url.Values{}
		query.Set("page", fmt.Sprintf("%d", page))
		if orgID != "" {
			query.Set("organizationId", orgID)
		}
		var payload projectsResponse
		requestURL := apiHost + "/api/v1/projects?" + query.Encode()
		if err := doJSONRequest(http.MethodGet, requestURL, token, nil, &payload); err != nil {
			return nil, err
		}
		for _, item := range payload.Projects {
			// Deploy/stack commands operate on cell projects. Newer project-list
			// responses also include box and storage projects.
			if item.Type != "" && item.Type != "cell" {
				continue
			}
			projects = append(projects, normalizeProject(item))
		}
		if payload.PageCount <= page || payload.PageCount == 0 {
			break
		}
	}
	return projects, nil
}

func normalizeProject(item project) project {
	if item.Region.ID == "" {
		item.Region.ID = item.RegionID
	}
	if item.Region.Name == "" {
		item.Region.Name = item.RegionName
	}
	if item.Region.Location == "" {
		item.Region.Location = item.RegionLocation
	}
	return item
}

func fetchRegions(token string) ([]region, error) {
	var payload []region
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/regions", token, nil, &payload)
	for index := range payload {
		payload[index] = normalizeRegion(payload[index])
	}
	return payload, err
}

func normalizeRegion(item region) region {
	if item.Products != nil {
		item.Available = item.Products.Cell
	}
	return item
}

func fetchProject(token, projectID string) (projectDetails, error) {
	var payload projectDetails
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/projects/"+projectID, token, nil, &payload)
	return payload, err
}

func createProjectContainer(
	token, projectID string,
	req map[string]any,
) (map[string]any, error) {
	var payload map[string]any
	err := doJSONRequest(
		http.MethodPost,
		apiHost+"/api/v1/projects/"+projectID+"/containers/create",
		token,
		req,
		&payload,
	)
	return payload, err
}

func patchProjectContainerConfig(
	token, projectID, containerID string,
	req map[string]any,
) (map[string]any, error) {
	var payload map[string]any
	err := doJSONRequest(
		http.MethodPost,
		apiHost+"/api/v1/projects/"+projectID+"/containers/"+containerID+"/config",
		token,
		req,
		&payload,
	)
	return payload, err
}

func removeProjectContainer(token, projectID, containerID string) error {
	return doJSONRequest(
		http.MethodPost,
		apiHost+"/api/v1/projects/"+projectID+"/containers/"+containerID+"/remove",
		token,
		map[string]any{},
		nil,
	)
}

func createProjectVolume(
	token, projectID string,
	req map[string]any,
) (map[string]any, error) {
	var payload map[string]any
	err := doJSONRequest(
		http.MethodPost,
		apiHost+"/api/v1/projects/"+projectID+"/volumes/create",
		token,
		req,
		&payload,
	)
	return payload, err
}

func removeProjectVolume(token, projectID, volumeID string) error {
	return doJSONRequest(
		http.MethodPost,
		apiHost+"/api/v1/projects/"+projectID+"/volumes/"+volumeID+"/remove",
		token,
		map[string]any{},
		nil,
	)
}

func createProjectForDeploy(token, name, regionID, orgID string) (project, error) {
	var payload project
	body := map[string]string{
		"name":     name,
		"regionId": regionID,
		"type":     "cell",
	}
	if orgID != "" {
		body["organizationId"] = orgID
	}
	err := doJSONRequest(http.MethodPost, apiHost+"/api/v1/projects/create", token, body, &payload)
	return normalizeProject(payload), err
}

func fetchTunnels(token, projectID string) ([]tunnel, error) {
	var payload []tunnel
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/projects/"+projectID+"/tunnels", token, nil, &payload)
	return payload, err
}

func createTunnel(token, projectID string, req createTunnelRequest) (tunnel, error) {
	var t tunnel
	err := doJSONRequest(http.MethodPost, apiHost+"/api/v1/projects/"+projectID+"/tunnels/create", token, req, &t)
	return t, err
}

func createDeploySession(token string, req createDeploySessionRequest) (deploySessionResponse, error) {
	var payload deploySessionResponse
	err := doJSONRequest(http.MethodPost, apiHost+"/api/v1/cli/deploy/sessions", token, req, &payload)
	return payload, err
}

func createDeployPlan(token string, req createDeployPlanRequest) (deployPlanResponse, error) {
	var payload deployPlanResponse
	err := doJSONRequest(http.MethodPost, apiHost+"/api/v1/cli/deploy/plans", token, req, &payload)
	return payload, err
}

func fetchActiveDeploySession(token, containerID string) (activeDeploySessionResponse, error) {
	var payload activeDeploySessionResponse
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/cli/deploy/containers/"+url.PathEscape(containerID)+"/active-session", token, nil, &payload)
	return payload, err
}

func cancelDeploySession(token, buildID string) error {
	return doJSONRequest(http.MethodPost, apiHost+"/api/v1/cli/deploy/sessions/"+url.PathEscape(buildID)+"/cancel", token, struct{}{}, nil)
}

func retryDeploySession(token, buildID string) error {
	return doJSONRequest(http.MethodPost, apiHost+"/api/v1/cli/deploy/sessions/"+url.PathEscape(buildID)+"/retry", token, struct{}{}, nil)
}

func fetchDeploySessionEvents(token, buildID string) (deploySessionEventsResponse, error) {
	var payload deploySessionEventsResponse
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/cli/deploy/sessions/"+url.PathEscape(buildID)+"/events", token, nil, &payload)
	return payload, err
}

func fetchDeploySession(token, buildID string) (deploySessionStatusResponse, error) {
	var payload deploySessionStatusResponse
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/cli/deploy/sessions/"+buildID, token, nil, &payload)
	return payload, err
}

func fetchDeployContainerSnapshot(token, containerID string) (deployContainerSnapshotResponse, error) {
	var payload deployContainerSnapshotResponse
	err := doJSONRequest(
		http.MethodGet,
		apiHost+"/api/v1/cli/deploy/containers/"+containerID,
		token,
		nil,
		&payload,
	)
	return payload, err
}

func reportDeployFailure(token, buildID, uploadToken, errorMessage string) error {
	body := map[string]string{
		"uploadToken": uploadToken,
		"error":       errorMessage,
	}
	return doJSONRequest(
		http.MethodPost,
		apiHost+"/api/v1/cli/deploy/sessions/"+url.PathEscape(buildID)+"/fail",
		token,
		body,
		nil,
	)
}

func completeDeployUpload(token string, session deploySessionResponse, digest string) error {
	body := struct {
		UploadToken    string `json:"uploadToken"`
		Digest         string `json:"digest"`
		CanonicalRef   string `json:"canonicalRef"`
		IdempotencyKey string `json:"idempotencyKey"`
	}{
		UploadToken:    session.Upload.Token,
		Digest:         digest,
		CanonicalRef:   session.Upload.CanonicalRef,
		IdempotencyKey: "cli-complete:" + session.BuildID + ":" + digest,
	}
	return doJSONRequest(
		http.MethodPost,
		apiHost+"/api/v1/cli/deploy/sessions/"+url.PathEscape(session.BuildID)+"/complete",
		token,
		body,
		nil,
	)
}

func createTerminalSession(token, projectID, containerID string) (terminalSession, error) {
	var payload terminalSession
	url := apiHost + "/api/v1/projects/" + projectID + "/containers/" + containerID + "/terminal/session"
	err := doJSONRequest(http.MethodPost, url, token, map[string]any{}, &payload)
	return payload, err
}

func execInContainer(token, projectID, containerID string, command []string, timeout time.Duration) (execResult, error) {
	var payload execResult
	url := apiHost + "/api/v1/projects/" + projectID + "/containers/" + containerID + "/exec"
	body := map[string]any{
		"command":   command,
		"timeoutMs": timeout.Milliseconds(),
	}
	err := doJSONRequestWithTimeout(http.MethodPost, url, token, body, &payload, timeout+5*time.Second)
	return payload, err
}

func fetchOrganizations(token string) ([]organization, error) {
	var payload []organization
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/organizations", token, nil, &payload)
	return payload, err
}

type containerLogsOutput struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

func fetchContainerLogs(token, projectID, containerID string) (containerLogsOutput, error) {
	var payload containerLogsOutput
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/projects/"+projectID+"/containers/"+containerID+"/logs", token, nil, &payload)
	return payload, err
}

func doJSONRequest(method, url, token string, body any, out any) error {
	// Container snapshots may perform a regional Hubnet inspection and can
	// legitimately take longer than a normal API request. Keep the CLI from
	// reporting a false timeout while retaining an explicit upper bound.
	return doJSONRequestWithTimeout(method, url, token, body, out, 45*time.Second)
}

func doJSONRequestWithTimeout(method, url, token string, body any, out any, timeout time.Duration) error {
	var requestBytes []byte
	var reqBody io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		requestBytes = payload
		reqBody = bytes.NewBuffer(payload)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if subaccount := strings.TrimSpace(os.Getenv("HUBFLY_SUBACCOUNT")); subaccount != "" {
		req.Header.Set("X-HubFly-Subaccount", subaccount)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	debugf("HTTP request: %s %s", method, url)
	if token != "" {
		debugf("Authorization: Bearer %s", maskToken(token))
	}
	if len(requestBytes) > 0 {
		debugf("Request body: %s", redactJSONForDebug(requestBytes))
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		debugf("HTTP transport error: %v", err)
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return readErr
	}

	debugf("HTTP response status: %d", resp.StatusCode)
	if len(respBytes) > 0 {
		debugf("Response body: %s", redactJSONForDebug(respBytes))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(respBytes))
		code := ""
		requestID := ""
		errorID := ""
		if len(respBytes) > 0 {
			var apiPayload struct {
				Error   any    `json:"error"`
				Message string `json:"message"`
				Meta    struct {
					RequestID string `json:"requestId"`
				} `json:"meta"`
			}
			if err := json.Unmarshal(respBytes, &apiPayload); err == nil {
				requestID = strings.TrimSpace(apiPayload.Meta.RequestID)
				if apiErrorMessage, ok := apiPayload.Error.(string); ok && strings.TrimSpace(apiErrorMessage) != "" {
					msg = strings.TrimSpace(apiErrorMessage)
				} else if errorObject, ok := apiPayload.Error.(map[string]any); ok {
					if value, ok := errorObject["message"].(string); ok && strings.TrimSpace(value) != "" {
						msg = strings.TrimSpace(value)
					}
					if value, ok := errorObject["code"].(string); ok {
						code = strings.TrimSpace(value)
					}
					if value, ok := errorObject["errorId"].(string); ok {
						errorID = strings.TrimSpace(value)
					}
				} else if strings.TrimSpace(apiPayload.Message) != "" {
					msg = strings.TrimSpace(apiPayload.Message)
				}
			}
		}
		if msg == "" {
			msg = "request failed"
		}
		return &apiError{
			Status:    resp.StatusCode,
			Code:      code,
			Message:   msg,
			RequestID: requestID,
			ErrorID:   errorID,
		}
	}

	if out == nil || len(respBytes) == 0 {
		return nil
	}

	var env struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(respBytes, &env); err == nil && (env.OK || env.Error != nil) {
		if !env.OK {
			if env.Error != nil {
				return &apiError{
					Status:  resp.StatusCode,
					Code:    env.Error.Code,
					Message: env.Error.Message,
				}
			}
			return &apiError{Status: resp.StatusCode, Message: "request failed"}
		}
		return json.Unmarshal(env.Data, out)
	}

	return json.Unmarshal(respBytes, out)
}

func redactJSONForDebug(payload []byte) string {
	var value any
	if json.Unmarshal(payload, &value) != nil {
		return "<non-json payload omitted>"
	}
	var redact func(any, string) any
	redact = func(item any, parentKey string) any {
		switch typed := item.(type) {
		case map[string]any:
			secret := typed["isSecret"] == true || typed["secret"] == true
			result := make(map[string]any, len(typed))
			for key, child := range typed {
				lower := strings.ToLower(key)
				if lower == "token" || lower == "uploadtoken" || lower == "accesstoken" || lower == "valueciphertext" || (secret && lower == "value") {
					result[key] = "***"
					continue
				}
				result[key] = redact(child, key)
			}
			return result
		case []any:
			result := make([]any, len(typed))
			for index, child := range typed {
				result[index] = redact(child, parentKey)
			}
			return result
		default:
			return item
		}
	}
	redacted, err := json.Marshal(redact(value, ""))
	if err != nil {
		return "<json payload omitted>"
	}
	return string(redacted)
}
