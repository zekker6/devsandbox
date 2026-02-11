package sandbox

// Logger is used by the builder and mounts engine to report
// warnings and errors during sandbox setup.
type Logger interface {
	Warnf(format string, args ...any)
	Infof(format string, args ...any)
	Errorf(format string, args ...any)
}
