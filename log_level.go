package debugbundle

import "strings"

type LogLevel string

const (
	LevelDebug    LogLevel = "debug"
	LevelInfo     LogLevel = "info"
	LevelWarning  LogLevel = "warning"
	LevelError    LogLevel = "error"
	LevelCritical LogLevel = "critical"
)

var logLevelRanks = map[LogLevel]int{
	LevelDebug:    10,
	LevelInfo:     20,
	LevelWarning:  30,
	LevelError:    40,
	LevelCritical: 50,
}

func normalizeLogLevel(level LogLevel) LogLevel {
	switch LogLevel(strings.ToLower(string(level))) {
	case LevelDebug, LevelInfo, LevelWarning, LevelError, LevelCritical:
		return LogLevel(strings.ToLower(string(level)))
	default:
		return defaultLogLevel
	}
}

func shouldCaptureLog(configured LogLevel, candidate LogLevel) bool {
	return logLevelRanks[normalizeLogLevel(candidate)] >= logLevelRanks[normalizeLogLevel(configured)]
}
