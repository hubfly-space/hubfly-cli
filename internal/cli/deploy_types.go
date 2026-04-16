package cli

type deployProjectBinding struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Region string `json:"region,omitempty"`
}

type deployContainerBinding struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

type deployBuildSettings struct {
	Mode               string   `json:"mode,omitempty"`
	WorkingDir         string   `json:"workingDir,omitempty"`
	ContextDir         string   `json:"contextDir,omitempty"`
	Runtime            string   `json:"runtime,omitempty"`
	Framework          string   `json:"framework,omitempty"`
	Version            string   `json:"version,omitempty"`
	InstallCommand     string   `json:"installCommand,omitempty"`
	SetupCommands      []string `json:"setupCommands,omitempty"`
	BuildCommand       string   `json:"buildCommand,omitempty"`
	PostBuildCommands  []string `json:"postBuildCommands,omitempty"`
	RunCommand         string   `json:"runCommand,omitempty"`
	RuntimeInitCommand string   `json:"runtimeInitCommand,omitempty"`
	ExposePort         string   `json:"exposePort,omitempty"`
}

type deployResources struct {
	CPU     float64 `json:"cpu"`
	RAM     int     `json:"ram"`
	Storage int     `json:"storage"`
	MaxCPU  float64 `json:"maxCpu,omitempty"`
	MaxRAM  int     `json:"maxRam,omitempty"`
}

type deployRuntime struct {
	AutoSleep    bool   `json:"autoSleep"`
	AutoScale    bool   `json:"autoScale"`
	Is24x7       bool   `json:"is24x7"`
	AutoScaleMode string `json:"autoScaleMode,omitempty"`
}

type deployPort struct {
	Container int    `json:"container"`
	Protocol  string `json:"protocol"`
	Host      int    `json:"host,omitempty"`
	HostIP    string `json:"hostIp,omitempty"`
}

type deployVolume struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

type deployProcess struct {
	Command    []string `json:"command,omitempty"`
	Entrypoint []string `json:"entrypoint,omitempty"`
	WorkingDir string   `json:"workingDir,omitempty"`
}

type deployHealthcheck struct {
	Test        []string `json:"test,omitempty"`
	Interval    string   `json:"interval,omitempty"`
	Timeout     string   `json:"timeout,omitempty"`
	StartPeriod string   `json:"startPeriod,omitempty"`
	Retries     int      `json:"retries,omitempty"`
}

type deployRestartPolicy struct {
	Name              string `json:"name"`
	MaximumRetryCount int    `json:"maximumRetryCount,omitempty"`
}

type deployEnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
	Scope  string `json:"scope,omitempty"`
}

type deployConfigFile struct {
	Version   int                    `json:"version"`
	Project   deployProjectBinding   `json:"project"`
	Container deployContainerBinding `json:"container"`
	Build     deployBuildSettings    `json:"build"`
	Deploy    struct {
		Tier               string                `json:"tier"`
		Resources          deployResources       `json:"resources"`
		Runtime            deployRuntime         `json:"runtime"`
		NetworkPrimaryAlias string               `json:"networkPrimaryAlias,omitempty"`
		NetworkAliases     []string              `json:"networkAliases,omitempty"`
		Ports              []deployPort          `json:"ports,omitempty"`
		Volumes            []deployVolume        `json:"volumes,omitempty"`
		Process            deployProcess         `json:"process,omitempty"`
		Healthcheck        *deployHealthcheck    `json:"healthcheck,omitempty"`
		RestartPolicy      *deployRestartPolicy  `json:"restartPolicy,omitempty"`
		Labels             map[string]string     `json:"labels,omitempty"`
	} `json:"deploy"`
	Env []deployEnvVar `json:"env,omitempty"`
	Metadata struct {
		BuilderVersion string `json:"builderVersion,omitempty"`
		LastBuildID    string `json:"lastBuildId,omitempty"`
		LastImageTag   string `json:"lastImageTag,omitempty"`
		LastDeployedAt string `json:"lastDeployedAt,omitempty"`
	} `json:"metadata,omitempty"`
}

type builderInspectBuildConfig struct {
	IsAutoBuild        bool     `json:"isAutoBuild"`
	Runtime            string   `json:"runtime"`
	Framework          string   `json:"framework,omitempty"`
	Version            string   `json:"version,omitempty"`
	InstallCommand     string   `json:"installCommand,omitempty"`
	SetupCommands      []string `json:"setupCommands,omitempty"`
	BuildCommand       string   `json:"buildCommand,omitempty"`
	PostBuildCommands  []string `json:"postBuildCommands,omitempty"`
	RunCommand         string   `json:"runCommand,omitempty"`
	RuntimeInitCommand string   `json:"runtimeInitCommand,omitempty"`
	ExposePort         string   `json:"exposePort,omitempty"`
	BuildContextDir    string   `json:"buildContextDir,omitempty"`
	AppDir             string   `json:"appDir,omitempty"`
	ValidationWarnings []string `json:"validationWarnings,omitempty"`
}

type builderInspectOutput struct {
	BuildConfig     builderInspectBuildConfig `json:"buildConfig"`
	Dockerfile      string                    `json:"dockerfile"`
	BuildArgKeys    []string                  `json:"buildArgKeys,omitempty"`
	BuildSecretKeys []string                  `json:"buildSecretKeys,omitempty"`
}

type cliDeploymentConfig struct {
	ProjectName         string                       `json:"projectName"`
	ContainerName       string                       `json:"containerName"`
	NetworkPrimaryAlias string                       `json:"networkPrimaryAlias,omitempty"`
	NetworkAliases      []string                     `json:"networkAliases,omitempty"`
	Region              string                       `json:"region"`
	ProjectID           string                       `json:"projectId"`
	Tier                string                       `json:"tier"`
	Resources           cliDeploymentResources       `json:"resources"`
	AttachedVolumes     []cliDeploymentVolume        `json:"attachedVolumes,omitempty"`
	Runtime             cliDeploymentRuntime         `json:"runtime"`
	Process             *cliDeploymentProcess        `json:"process,omitempty"`
	Healthcheck         *cliDeploymentHealthcheck    `json:"healthcheck,omitempty"`
	RestartPolicy       *cliDeploymentRestartPolicy  `json:"restartPolicy,omitempty"`
	Labels              map[string]string            `json:"labels,omitempty"`
	Networking          cliDeploymentNetworking      `json:"networking"`
	EnvironmentVariables []cliDeploymentEnvVar       `json:"environmentVariables,omitempty"`
	Source              cliDeploymentSource          `json:"source"`
}

type cliDeploymentSource struct {
	Type        string `json:"type"`
	DockerImage string `json:"dockerImage,omitempty"`
}

type cliDeploymentResources struct {
	CPU     float64 `json:"cpu"`
	RAM     int     `json:"ram"`
	Storage int     `json:"storage"`
	MaxCPU  float64 `json:"maxCpu,omitempty"`
	MaxRAM  int     `json:"maxRam,omitempty"`
}

type cliDeploymentVolume struct {
	DockerVolumeName string `json:"dockerVolumeName"`
	MountPoint       string `json:"mountPoint"`
}

type cliDeploymentRuntime struct {
	AutoSleep    bool   `json:"autoSleep"`
	AutoScale    bool   `json:"autoScale"`
	Is24x7       bool   `json:"is24x7"`
	AutoScaleMode string `json:"autoScaleMode,omitempty"`
}

type cliDeploymentProcess struct {
	Command    []string `json:"command,omitempty"`
	Entrypoint []string `json:"entrypoint,omitempty"`
	WorkingDir string   `json:"workingDir,omitempty"`
}

type cliDeploymentHealthcheck struct {
	Test        []string `json:"test"`
	Interval    string   `json:"interval,omitempty"`
	Timeout     string   `json:"timeout,omitempty"`
	StartPeriod string   `json:"startPeriod,omitempty"`
	Retries     int      `json:"retries,omitempty"`
}

type cliDeploymentRestartPolicy struct {
	Name              string `json:"name"`
	MaximumRetryCount int    `json:"maximumRetryCount,omitempty"`
}

type cliDeploymentNetworking struct {
	Ports []cliDeploymentPort `json:"ports,omitempty"`
}

type cliDeploymentPort struct {
	Container int    `json:"container"`
	Protocol  string `json:"protocol"`
	Host      int    `json:"host,omitempty"`
	HostIP    string `json:"hostIp,omitempty"`
}

type cliDeploymentEnvVar struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Value    string `json:"value"`
	IsSecret bool   `json:"isSecret"`
	Scope    string `json:"scope,omitempty"`
}

type createDeploySessionRequest struct {
	BuilderVersion   string              `json:"builderVersion,omitempty"`
	BoundContainerID string              `json:"boundContainerId,omitempty"`
	Config           cliDeploymentConfig `json:"config"`
}

type deploySessionResponse struct {
	BuildID   string `json:"buildId"`
	ProjectID string `json:"projectId"`
	ProjectName string `json:"projectName"`
	Region    struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Location  string `json:"location"`
		PrimaryIP string `json:"primaryIP"`
	} `json:"region"`
	Upload struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	} `json:"upload"`
	Status string `json:"status"`
}

type deploySessionStatusResponse struct {
	Build struct {
		ID               string `json:"id"`
		Status           string `json:"status"`
		Error            string `json:"error"`
		ImageTag         string `json:"imageTag"`
		StartedAt        string `json:"startedAt"`
		FinishedAt       string `json:"finishedAt"`
		CreatedAt        string `json:"createdAt"`
		UpdatedAt        string `json:"updatedAt"`
		ProjectID        string `json:"projectId"`
		ProjectName      string `json:"projectName"`
		RegionID         string `json:"regionId"`
		RegionName       string `json:"regionName"`
		BoundContainerID string `json:"boundContainerId"`
		BuilderVersion   string `json:"builderVersion"`
	} `json:"build"`
}
