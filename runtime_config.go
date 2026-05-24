package debugbundle

type RuntimeConfig struct {
	ProjectToken   string
	Environment    string
	Service        string
	Endpoint       string
	ProjectMode    ProjectMode
	LocalEventsDir string
	SpoolDir       string
}

func (client *Client) RuntimeConfig() RuntimeConfig {
	client.mu.Lock()
	defer client.mu.Unlock()
	return RuntimeConfig{
		ProjectToken:   client.config.projectToken,
		Environment:    client.config.environment,
		Service:        client.config.service,
		Endpoint:       client.config.endpoint,
		ProjectMode:    client.config.projectMode,
		LocalEventsDir: client.config.localEventsDir,
		SpoolDir:       client.config.spoolDir,
	}
}
