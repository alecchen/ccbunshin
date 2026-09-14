package main

import (
	"fmt"
	"log"
	"strings"
)

// logLevel is a log4j-style severity. A line is written when its own level is at or
// above the process threshold, so CCBUNSHIN_LOG=error leaves only failures.
type logLevel int

const (
	levelDebug logLevel = iota
	levelInfo
	levelWarn
	levelError
)

func (l logLevel) String() string {
	switch l {
	case levelDebug:
		return "DEBUG"
	case levelWarn:
		return "WARN"
	case levelError:
		return "ERROR"
	}
	return "INFO"
}

// logThreshold is the process threshold. It is package state rather than a parameter
// because the call sites are ordinary logging statements and the value is fixed for the
// daemon's lifetime: main sets it once, from the environment, before serving.
var logThreshold = levelInfo

// parseLogLevel reads CCBUNSHIN_LOG. An empty value is info, which is what the proxy
// logged before levels existed. A value that names no level is an error rather than a
// silent fallback: a typo would otherwise look like logging that stopped working.
func parseLogLevel(value string) (logLevel, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "info":
		return levelInfo, nil
	case "debug":
		return levelDebug, nil
	case "warn", "warning":
		return levelWarn, nil
	case "error":
		return levelError, nil
	}
	return levelInfo, fmt.Errorf("CCBUNSHIN_LOG must be one of debug, info, warn, error (got %q)", value)
}

// logf writes one level-tagged line when the level passes the threshold. The standard
// logger supplies the timestamp the log file has always carried.
func logf(level logLevel, format string, args ...any) {
	if level < logThreshold {
		return
	}
	log.Printf("%s %s", level, fmt.Sprintf(format, args...))
}

// logfContext appends the diagnostic suffix to a line. The fields there are structural
// facts about the request rather than a format string, so the caller does not restate
// them at every failure site.
func logfContext(p requestPlan, level logLevel, format string, args ...any) {
	logf(level, format+p.diagnosticSuffix(), args...)
}

// diagnosticSuffix renders the fields every failure line carries. A failure inside
// stream.go has no requestPlan in scope, so it passes one describing what it knows.
func (p requestPlan) diagnosticSuffix() string {
	shape := "buffered"
	if p.isStreaming {
		shape = "stream"
	}
	return fmt.Sprintf(" [provider=%s dialect=%s shape=%s request=%dB upstream=%s]",
		p.providerName, p.dialect, shape, p.requestBytes, upstreamAddress(p.provider))
}

// upstreamAddress returns the upstream URL with any credential redacted. The failure
// lines name the upstream, so this is the one place a password could reach the log.
func upstreamAddress(p loadedProvider) string {
	if p.upstream == nil {
		return "none"
	}
	return p.upstream.Redacted()
}
