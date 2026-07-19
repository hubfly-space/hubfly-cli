package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type stackState struct {
	Version     int                          `json:"version"`
	ProjectID   string                       `json:"projectId"`
	ProjectName string                       `json:"projectName"`
	RegionID    string                       `json:"regionId"`
	StackName   string                       `json:"stackName"`
	ComposeFile string                       `json:"composeFile"`
	ComposeHash string                       `json:"composeHash"`
	Services    map[string]stackServiceState `json:"services"`
	Volumes     map[string]stackVolumeState  `json:"volumes"`
}

type stackServiceState struct {
	ContainerID   string `json:"containerId"`
	ContainerName string `json:"containerName"`
	LastImage     string `json:"lastImage"`
	LastBuildID   string `json:"lastBuildId"`
	ConfigHash    string `json:"configHash"`
}

type stackVolumeState struct {
	VolumeID string `json:"volumeId"`
	Name     string `json:"name"`
	SizeGb   int    `json:"sizeGb"`
}

func stackStatePath(projectDir string) string {
	return filepath.Join(projectDir, ".hubfly", "compose-state.json")
}

func loadStackState(projectDir string) (stackState, error) {
	var state stackState
	content, err := os.ReadFile(stackStatePath(projectDir))
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(content, &state); err != nil {
		return stackState{}, err
	}
	if state.Services == nil {
		state.Services = map[string]stackServiceState{}
	}
	if state.Volumes == nil {
		state.Volumes = map[string]stackVolumeState{}
	}
	return state, nil
}

func loadStackStateIfExists(projectDir string) (stackState, bool, error) {
	state, err := loadStackState(projectDir)
	if err == nil {
		return state, true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return stackState{
			Version:  1,
			Services: map[string]stackServiceState{},
			Volumes:  map[string]stackVolumeState{},
		}, false, nil
	}
	return stackState{}, false, err
}

func saveStackState(projectDir string, state stackState) error {
	if state.Version == 0 {
		state.Version = 1
	}
	if state.Services == nil {
		state.Services = map[string]stackServiceState{}
	}
	if state.Volumes == nil {
		state.Volumes = map[string]stackVolumeState{}
	}
	path := stackStatePath(projectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(path, payload, 0o644)
}

func deleteStackState(projectDir string) error {
	err := os.Remove(stackStatePath(projectDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func stackConfigHash(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}
