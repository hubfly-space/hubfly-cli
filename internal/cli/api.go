package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func fetchWhoAmI(token string) (user, error) {
	var u user
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/auth/me", token, nil, &u)
	return u, err
}

func fetchProjects(token string) ([]project, error) {
	var payload projectsResponse
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/projects", token, nil, &payload)
	return payload.Projects, err
}

func fetchProjectsWithOrg(token, orgID string) ([]project, error) {
	var payload projectsResponse
	requestURL := apiHost + "/api/v1/projects"
	if orgID != "" {
		requestURL += "?organizationId=" + url.QueryEscape(orgID)
	}
	err := doJSONRequest(http.MethodGet, requestURL, token, nil, &payload)
	return payload.Projects, err
}

func fetchRegions(token string) ([]region, error) {
	var payload []region
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/regions", token, nil, &payload)
	return payload, err
}

func fetchProject(token, projectID string) (projectDetails, error) {
	var payload projectDetails
	err := doJSONRequest(http.MethodGet, apiHost+"/api/v1/projects/"+projectID, token, nil, &payload)
	return payload, err
}

func createProjectForDeploy(token, name, regionID, orgID string) (project, error) {
	var payload project
	body := map[string]string{
		"name":     name,
		"regionId": regionID,
	}
	if orgID != "" {
		body["organizationId"] = orgID
	}
	err := doJSONRequest(http.MethodPost, apiHost+"/api/v1/projects/create", token, body, &payload)
	return payload, err
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

func createTerminalSession(token, projectID, containerID string) (terminalSession, error) {
	var payload terminalSession
	url := apiHost + "/api/v1/projects/" + projectID + "/containers/" + containerID + "/terminal/session"
	err := doJSONRequest(http.MethodPost, url, token, nil, &payload)
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
	return doJSONRequestWithTimeout(method, url, token, body, out, 20*time.Second)
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
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	debugf("HTTP request: %s %s", method, url)
	if token != "" {
		debugf("Authorization: Bearer %s", maskToken(token))
	}
	if len(requestBytes) > 0 {
		debugf("Request body: %s", string(requestBytes))
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
		debugf("Response body: %s", string(respBytes))
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
