package debugbundle

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/debugbundle/debugbundle-go/redaction"
	"github.com/debugbundle/debugbundle-go/transport"
)

const (
	Version              = "1.0.0"
	defaultEndpoint      = "https://api.debugbundle.com/v1/events"
	defaultLocalDir      = ".debugbundle/local/events"
	defaultSpoolDir      = ".debugbundle/local/browser-relay-spool"
	defaultBatchSize     = 25
	defaultFlushPeriod   = 5 * time.Second
	defaultLogLevel      = LevelWarning
	defaultTimeout       = 5 * time.Second
	defaultSDKName       = "@debugbundle/sdk-go"
	defaultSchemaVersion = "2026-03-01"
	maxRetryBackoff      = 5 * time.Minute
)

type ProjectMode string

const (
	ProjectModeConnected ProjectMode = "connected"
	ProjectModeLocalOnly ProjectMode = "local-only"
)

type Config struct {
	ProjectToken            string
	Enabled                 *bool
	Environment             string
	Service                 string
	Endpoint                string
	ProjectMode             ProjectMode
	LocalEventsDir          string
	SpoolDir                string
	BatchSize               int
	FlushInterval           time.Duration
	ProbesPollInterval      time.Duration
	SampleRate              float64
	LogLevel                LogLevel
	RequestTimeout          time.Duration
	RedactFields            []string
	MaxProbeLabels          int
	MaxProbeEntriesPerLabel int
	ProbeFlushOnError       *bool
	Transport               transport.Sender
	RemoteConfigFetcher     RemoteConfigFetcher
}

type resolvedConfig struct {
	projectToken            string
	enabled                 bool
	environment             string
	service                 string
	endpoint                string
	projectMode             ProjectMode
	localEventsDir          string
	spoolDir                string
	batchSize               int
	flushInterval           time.Duration
	probesPollInterval      time.Duration
	sampleRate              float64
	logLevel                LogLevel
	requestTimeout          time.Duration
	redactFields            []string
	maxProbeLabels          int
	maxProbeEntriesPerLabel int
	probeFlushOnError       bool
	transport               transport.Sender
	remoteConfigFetcher     RemoteConfigFetcher
}

func (config Config) resolve() resolvedConfig {
	resolved := resolvedConfig{
		projectToken:            strings.TrimSpace(config.ProjectToken),
		enabled:                 strings.TrimSpace(config.ProjectToken) != "",
		environment:             normalizeEnvironment(config.Environment),
		service:                 normalizeService(config.Service),
		endpoint:                config.Endpoint,
		projectMode:             config.ProjectMode,
		localEventsDir:          config.LocalEventsDir,
		spoolDir:                config.SpoolDir,
		batchSize:               config.BatchSize,
		flushInterval:           config.FlushInterval,
		probesPollInterval:      config.ProbesPollInterval,
		sampleRate:              config.SampleRate,
		logLevel:                normalizeLogLevel(config.LogLevel),
		requestTimeout:          config.RequestTimeout,
		redactFields:            append([]string{}, config.RedactFields...),
		maxProbeLabels:          config.MaxProbeLabels,
		maxProbeEntriesPerLabel: config.MaxProbeEntriesPerLabel,
		probeFlushOnError:       true,
		transport:               config.Transport,
		remoteConfigFetcher:     config.RemoteConfigFetcher,
	}

	if config.Enabled != nil {
		resolved.enabled = *config.Enabled && resolved.projectToken != ""
	}
	if resolved.endpoint == "" {
		resolved.endpoint = defaultEndpoint
	}
	if resolved.projectMode == "" {
		resolved.projectMode = ProjectModeConnected
	}
	if resolved.localEventsDir == "" {
		resolved.localEventsDir = resolveAbsolutePath(defaultLocalDir)
	} else {
		resolved.localEventsDir = resolveAbsolutePath(resolved.localEventsDir)
	}
	if resolved.spoolDir == "" {
		resolved.spoolDir = resolveAbsolutePath(defaultSpoolDir)
	} else {
		resolved.spoolDir = resolveAbsolutePath(resolved.spoolDir)
	}
	if resolved.batchSize <= 0 {
		resolved.batchSize = defaultBatchSize
	}
	if resolved.flushInterval <= 0 {
		resolved.flushInterval = defaultFlushPeriod
	}
	if resolved.probesPollInterval <= 0 {
		resolved.probesPollInterval = 60 * time.Second
	}
	if resolved.sampleRate <= 0 || resolved.sampleRate > 1 {
		if resolved.sampleRate <= 0 {
			resolved.sampleRate = 1
		}
		if resolved.sampleRate > 1 {
			resolved.sampleRate = 1
		}
	}
	if resolved.requestTimeout <= 0 {
		resolved.requestTimeout = defaultTimeout
	}
	if resolved.maxProbeLabels <= 0 {
		resolved.maxProbeLabels = 50
	}
	if resolved.maxProbeEntriesPerLabel <= 0 {
		resolved.maxProbeEntriesPerLabel = 10
	}
	if len(resolved.redactFields) == 0 {
		resolved.redactFields = append([]string{}, redaction.DefaultSensitiveFields...)
	}
	if config.ProbeFlushOnError != nil {
		resolved.probeFlushOnError = *config.ProbeFlushOnError
	}

	return resolved
}

func normalizeEnvironment(value string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	for _, key := range []string{"DEBUGBUNDLE_ENVIRONMENT", "APP_ENV", "ENVIRONMENT", "GO_ENV"} {
		candidate := strings.TrimSpace(os.Getenv(key))
		if candidate != "" {
			return candidate
		}
	}
	return "development"
}

func normalizeService(value string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	if executable, err := os.Executable(); err == nil {
		name := strings.TrimSpace(filepath.Base(executable))
		if name != "" && name != "." {
			return name
		}
	}
	return "go-service"
}

func resolveAbsolutePath(path string) string {
	if path == "" {
		return ""
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(resolved)
}
