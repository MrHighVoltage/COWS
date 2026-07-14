package domain

// TemplateConfiguration contains only typed, administrator-controlled runtime
// configuration. It is never populated from a user workspace request.
type TemplateConfiguration struct {
	Command     []string              `json:"command,omitempty"`
	Environment []TemplateEnvironment `json:"environment,omitempty"`
	Secrets     []TemplateSecret      `json:"secrets,omitempty"`
	Mounts      []TemplateMount       `json:"mounts,omitempty"`
	Services    []TemplateService     `json:"services,omitempty"`
}

type TemplateEnvironment struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

// TemplateSecret is administrator-controlled input. Generated values are
// resolved once when a workspace is created and are not user-editable.
type TemplateSecret struct {
	Name     string `json:"name"`
	Value    string `json:"value,omitempty"`
	Generate bool   `json:"generate,omitempty"`
	Length   int    `json:"length,omitempty"`
}

type TemplateMount struct {
	Name          string `json:"name"`
	ContainerPath string `json:"container_path"`
	ReadOnly      bool   `json:"read_only,omitempty"`
}

type TemplateService struct {
	Name           string `json:"name"`
	Protocol       string `json:"protocol"`
	ContainerPort  int    `json:"container_port"`
	PortPool       string `json:"port_pool"`
	HostPortStart  int    `json:"host_port_start"`
	HostPortEnd    int    `json:"host_port_end"`
	PasswordSecret string `json:"password_secret,omitempty"`
}

type PortAllocation struct {
	WorkspaceID   string
	ServiceName   string
	Protocol      string
	ContainerPort int
	PortPool      string
	HostPort      int
}
