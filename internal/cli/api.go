package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func fetchWhoAmI(token string) (user, error) {
	var u user
	err := doJSONRequest(http.MethodGet, apiHost+"/api/auth/whoami", token, nil, &u)
	return u, err
}

func fetchProjects(token string) ([]project, error) {
	var payload projectsResponse
	err := doJSONRequest(http.MethodGet, apiHost+"/api/projects", token, nil, &payload)
	return payload.Projects, err
}

func fetchRegions(token string) ([]region, error) {
	var payload struct {
		Regions []region `json:"regions"`
	}
	err := doJSONRequest(http.MethodGet, apiHost+"/api/cli/deploy/regions", token, nil, &payload)
	return payload.Regions, err
}

func fetchProject(token, projectID string) (projectDetails, error) {
	var payload projectDetails
	err := doJSONRequest(http.MethodGet, apiHost+"/api/projects/"+projectID+"/containers", token, nil, &payload)
	return payload, err
}

func createProjectForDeploy(token, name, regionID string) (project, error) {
	var payload struct {
		Project project `json:"project"`
	}
	err := doJSONRequest(http.MethodPost, apiHost+"/api/cli/deploy/projects", token, map[string]string{
		"name":     name,
		"regionId": regionID,
	}, &payload)
	return payload.Project, err
}

func fetchTunnels(token, projectID string) ([]tunnel, error) {
	var payload []tunnel
	err := doJSONRequest(http.MethodGet, apiHost+"/api/projects/"+projectID+"/tunnels", token, nil, &payload)
	return payload, err
}

func createTunnel(token string, req createTunnelRequest) (tunnel, error) {
	var t tunnel
	err := doJSONRequest(http.MethodPost, apiHost+"/api/tunnels", token, req, &t)
	return t, err
}

func createDeploySession(token string, req createDeploySessionRequest) (deploySessionResponse, error) {
	var payload deploySessionResponse
	err := doJSONRequest(http.MethodPost, apiHost+"/api/cli/deploy/sessions", token, req, &payload)
	return payload, err
}

func fetchDeploySession(token, buildID string) (deploySessionStatusResponse, error) {
	var payload deploySessionStatusResponse
	err := doJSONRequest(http.MethodGet, apiHost+"/api/cli/deploy/sessions/"+buildID, token, nil, &payload)
	return payload, err
}

func reportDeployCallback(buildID, status, uploadToken, errorMessage string) error {
	body := map[string]string{
		"id":          buildID,
		"status":      status,
		"uploadToken": uploadToken,
	}
	if strings.TrimSpace(errorMessage) != "" {
		body["error"] = errorMessage
	}
	return doJSONRequest(http.MethodPost, apiHost+"/api/builds/callback", "", body, nil)
}

func doJSONRequest(method, url, token string, body any, out any) error {
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

	client := &http.Client{Timeout: 20 * time.Second}
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
		if msg == "" {
			msg = "request failed"
		}
		return &apiError{Status: resp.StatusCode, Message: msg}
	}

	if out == nil || len(respBytes) == 0 {
		return nil
	}
	return json.Unmarshal(respBytes, out)
}
