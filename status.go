package debugbundle

type SDKStatus string

const (
	StatusHealthy      SDKStatus = "healthy"
	StatusDegraded     SDKStatus = "degraded"
	StatusDisconnected SDKStatus = "disconnected"
)
