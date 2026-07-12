package domain

// TemplateConfiguration contains only typed, administrator-controlled runtime
// configuration. It is never populated from a user workspace request.
type TemplateConfiguration struct {
	Command     []string              `json:"command,omitempty"`
	Environment []TemplateEnvironment `json:"environment,omitempty"`
	Mounts      []TemplateMount       `json:"mounts,omitempty"`
	Services    []TemplateService     `json:"services,omitempty"`
}

type TemplateEnvironment struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

type TemplateMount struct {
	Name          string `json:"name"`
	ContainerPath string `json:"container_path"`
	ReadOnly      bool   `json:"read_only,omitempty"`
}

type TemplateService struct {
	Name          string `json:"name"`
	Protocol      string `json:"protocol"`
	ContainerPort int    `json:"container_port"`
	PortPool      string `json:"port_pool"`
	HostPortStart int    `json:"host_port_start"`
	HostPortEnd   int    `json:"host_port_end"`
}

type PortAllocation struct {
	WorkspaceID   string
	ServiceName   string
	Protocol      string
	ContainerPort int
	PortPool      string
	HostPort      int
}
