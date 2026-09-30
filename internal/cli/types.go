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
	return "https://api.hubfly.space"
}

type apiError struct {
	Status    int
	Code      string
	Message   string
	RequestID string
	ErrorID   string
}

func (e *apiError) Error() string {
	traceID := e.ErrorID
	if traceID == "" {
		traceID = e.RequestID
	}
	if traceID != "" {
		return fmt.Sprintf("%s (status %d, trace %s)", e.Message, e.Status, traceID)
	}
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
	Products        *struct {
		Cell bool `json:"cell"`
	} `json:"products,omitempty"`
}

type project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Role      string `json:"role"`
	CreatedAt string `json:"createdAt"`
	Spent     string `json:"spentAmount"`
	Monthly   string `json:"monthlyCost"`
	Region    region `json:"region"`
	// The projects endpoint now returns region data as flat fields. Keep the
	// nested Region field for compatibility with older Dashboard deployments.
	RegionID       string `json:"regionId"`
	RegionName     string `json:"regionName"`
	RegionLocation string `json:"regionLocation"`
}

type projectsResponse struct {
	Projects  []project `json:"items"`
	Page      int       `json:"page"`
	PageCount int       `json:"pageCount"`
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
	Volumes    []volume    `json:"volumes"`
}

type volume struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	SizeGb          string `json:"sizeGb"`
	PerformanceMode string `json:"performanceMode"`
	Status          string `json:"status"`
	ReadOnly        bool   `json:"readOnly"`
	HourlyCost      string `json:"hourlyCost"`
	MonthlyCost     string `json:"monthlyCost"`
}

type tunnel struct {
	TunnelID          string         `json:"tunnelId"`
	ID                string         `json:"id"`
	ProjectID         string         `json:"projectId"`
	ProjectName       string         `json:"projectName"`
	TargetContainer   string         `json:"targetContainerName"`
	TargetContainerID string         `json:"targetContainerId"`
	TargetPort        int            `json:"targetPort"`
	ConnectURL        string         `json:"connectUrl"`
	ConnectToken      string         `json:"connectToken,omitempty"`
	ProtocolVersion   int            `json:"protocolVersion"`
	Mode              string         `json:"mode"`
	Status            string         `json:"status"`
	Targets           []tunnelTarget `json:"targets"`
	Limits            tunnelLimits   `json:"limits"`
	BytesSent         string         `json:"bytesSent"`
	BytesReceived     string         `json:"bytesReceived"`
	StreamsOpened     int            `json:"streamsOpened"`
	CloseReason       string         `json:"closeReason"`
	ExpiresAt         string         `json:"expiresAt"`
}

type createTunnelRequest struct {
	ContainerID string `json:"containerId"`
	TargetPort  int    `json:"targetPort"`
	LocalPort   int    `json:"localPort,omitempty"`
	TTLSeconds  int    `json:"ttlSeconds,omitempty"`
}

type tunnelTarget struct {
	TargetID      string `json:"targetId"`
	ContainerID   string `json:"containerId"`
	ContainerName string `json:"containerName"`
	RuntimeID     string `json:"runtimeId"`
	TargetPort    int    `json:"targetPort"`
	LocalPort     int    `json:"localPort"`
}

type tunnelLimits struct {
	MaxStreams         int `json:"maxStreams"`
	IdleTimeoutSeconds int `json:"idleTimeoutSeconds"`
	MaxDurationSeconds int `json:"maxDurationSeconds"`
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

type Box struct {
	ID                   string  `json:"id"`
	ProjectID            string  `json:"projectId"`
	ProjectName          string  `json:"projectName,omitempty"`
	Name                 string  `json:"name"`
	Status               string  `json:"status"`
	ImageID              string  `json:"imageId"`
	VCPUs                int     `json:"vcpus"`
	MemoryMiB            int     `json:"memoryMib"`
	RootDiskGiB          int     `json:"rootDiskGib"`
	InitializationStatus string  `json:"initializationStatus"`
	ActiveOperationID    *string `json:"activeOperationId"`
	PrivateIPv4          *string `json:"privateIpv4"`
	CreatedAt            string  `json:"createdAt,omitempty"`
}

type BoxImage struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Family      string `json:"family"`
	Version     string `json:"version"`
	DefaultUser string `json:"defaultUser"`
}

type BoxResizeInput struct {
	VCPUs       int    `json:"vcpus,omitempty"`
	MemoryMiB   int    `json:"memoryMib,omitempty"`
	RootDiskGiB int    `json:"rootDiskGib,omitempty"`
	Mode        string `json:"mode,omitempty"`
}

type BoxCreateInput struct {
	ProjectID   string `json:"projectId"`
	Name        string `json:"name"`
	ImageID     string `json:"imageId"`
	VCPUs       int    `json:"vcpus"`
	MemoryMiB   int    `json:"memoryMib"`
	RootDiskGiB int    `json:"rootDiskGib"`
	Username    string `json:"username,omitempty"`
	SSHKeys     []string `json:"sshKeys,omitempty"`
}

type ContainerDetails struct {
	ID          string `json:"id"`
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	Region      struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Location string `json:"location"`
	} `json:"region"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Tier   string `json:"tier"`
	Status string `json:"status"`
	Source struct {
		Type          string `json:"type"`
		Template      string `json:"template,omitempty"`
		GitRepository string `json:"gitRepository,omitempty"`
		DockerImage   string `json:"dockerImage,omitempty"`
		Branch        string `json:"branch,omitempty"`
		DockerfilePath string `json:"dockerfilePath,omitempty"`
	} `json:"source"`
	Resources struct {
		CPU     float64 `json:"cpu"`
		RAM     float64 `json:"ram"`
		Storage float64 `json:"storage"`
	} `json:"resources"`
	Networking struct {
		Ports []struct {
			ID        string `json:"id,omitempty"`
			Protocol  string `json:"protocol"`
			Container int    `json:"container"`
			TunnelURL string `json:"tunnelUrl,omitempty"`
		} `json:"ports"`
	} `json:"networking"`
	PrimaryNetworkAlias string `json:"primaryNetworkAlias"`
	CreatedAt           string `json:"createdAt"`
	UpdatedAt           string `json:"updatedAt"`
	CanControl          bool   `json:"canControl"`
}

var stdin = bufio.NewReader(os.Stdin)
