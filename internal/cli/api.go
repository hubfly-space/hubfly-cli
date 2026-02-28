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

func fetchProject(token, projectID string) (projectDetails, error) {
	var payload projectDetails
	err := doJSONRequest(http.MethodGet, apiHost+"/api/projects/"+projectID+"/containers", token, nil, &payload)
	return payload, err
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

func doJSONRequest(method, url, token string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
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

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBytes, _ := io.ReadAll(resp.Body)
		msg := strings.TrimSpace(string(respBytes))
		if msg == "" {
			msg = "request failed"
		}
		return &apiError{Status: resp.StatusCode, Message: msg}
	}

	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
