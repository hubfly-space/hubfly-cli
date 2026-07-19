package cli

import (
	"bufio"
	"fmt"
	"os"
)

var apiHost = getAPIHost()

func getAPIHost() string {
	if url := os.Getenv("HUBFLY_API_URL"); url != "" {
		return url
	}
	return "https://hubfly.space"
}

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
	ID              string `json:"id"`
	Name            string `json:"name"`
	Location        string `json:"location"`
	Available       bool   `json:"available"`
	PrimaryIP       string `json:"primaryIP"`
	PrimaryProvider string `json:"primaryProvider"`
}

type project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Role      string `json:"role"`
	CreatedAt string `json:"createdAt"`
	Spent     string `json:"spentAmount"`
	Monthly   string `json:"monthlyCost"`
	Region    region `json:"region"`
}

type projectsResponse struct {
	Projects []project `json:"items"`
}

type container struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Tier    string `json:"tier"`
	Status  string `json:"status"`
	Created string `json:"createdAt"`
	Updated string `json:"updatedAt"`
	Source  struct {
		Type string `json:"type"`
	} `json:"source"`
	Resources struct {
		CPU     float64 `json:"cpu"`
		RAM     float64 `json:"ram"`
		Storage float64 `json:"storage"`
	} `json:"resources"`
	Networking struct {
		Ports []struct {
			Protocol  string `json:"protocol"`
			Container int    `json:"container"`
			TunnelURL string `json:"tunnelUrl"`
		} `json:"ports"`
	} `json:"networking"`
	PrimaryNetworkAlias string `json:"primaryNetworkAlias"`
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
	DockerName        string `json:"dockerName"`
	Instructions      string `json:"instructions"`
	TargetNetwork     struct {
		IPAddress string   `json:"ipAddress"`
		Aliases   []string `json:"aliases"`
	} `json:"targetNetwork"`
	ExpiresAt string `json:"expiresAt"`
}

type jwk struct {
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type createTunnelRequest struct {
	ContainerID   string `json:"containerId"`
	TargetPort    int    `json:"targetPort"`
	PublicKeyJWK  jwk    `json:"publicKeyJwk"`
	PrivateKeyPEM string `json:"privateKeyPem"`
}

type terminalSession struct {
	SessionID    string `json:"sessionId"`
	ConnectToken string `json:"connectToken"`
	ConnectURL   string `json:"connectUrl"`
	Shell        string `json:"shell"`
	ExpiresAt    string `json:"expiresAt"`
}

type execResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type organization struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Role      string `json:"role"`
	CreatedAt string `json:"createdAt"`
}

var stdin = bufio.NewReader(os.Stdin)
