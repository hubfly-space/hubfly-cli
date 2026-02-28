package cli

import (
	"bufio"
	"fmt"
	"os"
)

const apiHost = "https://hubfly.space"

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s (status %d)", e.Message, e.Status)
}

type storeConfig struct {
	Token string `json:"token"`
}

type user struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Image string `json:"image"`
}

type region struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Location string `json:"location"`
}

type project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Role      string `json:"role"`
	CreatedAt string `json:"createdAt"`
	Region    region `json:"region"`
}

type projectsResponse struct {
	Projects []project `json:"projects"`
}

type container struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Tier    string `json:"tier"`
	Status  string `json:"status"`
	Created string `json:"createdAt"`
	Source  struct {
		Type string `json:"type"`
	} `json:"source"`
	Resources struct {
		CPU int `json:"cpu"`
		RAM int `json:"ram"`
	} `json:"resources"`
}

type projectDetails struct {
	Containers []container `json:"containers"`
}

type tunnel struct {
	TunnelID          string `json:"tunnelId"`
	ID                string `json:"id"`
	SSHHost           string `json:"sshHost"`
	SSHPort           int    `json:"sshPort"`
	SSHUser           string `json:"sshUser"`
	TargetPort        int    `json:"targetPort"`
	TargetContainer   string `json:"targetContainer"`
	TargetContainerID string `json:"targetContainerId"`
	TargetNetwork     struct {
		IPAddress string `json:"ipAddress"`
	} `json:"targetNetwork"`
	ExpiresAt string `json:"expiresAt"`
}

type createTunnelRequest struct {
	ProjectID       string `json:"projectId"`
	TargetContainer string `json:"targetContainer"`
	TargetPort      int    `json:"targetPort"`
	ContainerID     string `json:"containerId"`
	PublicKey       string `json:"publicKey"`
}

var stdin = bufio.NewReader(os.Stdin)
