package logging

import (
	"encoding/json"
	"fmt"
	"log/syslog"
)

// SyslogConfig contains syslog writer configuration.
type SyslogConfig struct {
	// Network is empty for local syslog, or "udp"/"tcp" for remote.
	Network string

	// Address is the remote syslog server address (e.g., "logs.example.com:514").
	// Empty for local syslog.
	Address string

	// Facility is the syslog facility (e.g., "local0", "user", "daemon").
	Facility string

	// Tag is the program name/tag for syslog messages.
	Tag string

	// ErrorLogger logs internal errors to a file (optional).
	ErrorLogger *ErrorLogger
}

// SyslogWriter sends logs to syslog (local or remote).
type SyslogWriter struct {
	writer      *syslog.Writer
	errorLogger *ErrorLogger
	address     string // for error messages
}

// NewSyslogWriter creates a new syslog writer.
func NewSyslogWriter(cfg SyslogConfig) (*SyslogWriter, error) {
	facility := parseFacility(cfg.Facility)
	priority := facility | syslog.LOG_INFO

	tag := cfg.Tag
	if tag == "" {
		tag = "devsandbox"
	}

	var writer *syslog.Writer
	var err error
	address := "local"

	if cfg.Network != "" && cfg.Address != "" {
		// Remote syslog
		writer, err = syslog.Dial(cfg.Network, cfg.Address, priority, tag)
		address = cfg.Network + "://" + cfg.Address
	} else {
		// Local syslog
		writer, err = syslog.New(priority, tag)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect to syslog: %w", err)
	}

	return &SyslogWriter{
		writer:      writer,
		errorLogger: cfg.ErrorLogger,
		address:     address,
	}, nil
}

// Write sends a log entry to syslog.
func (s *SyslogWriter) Write(entry *Entry) error {
	// Format as JSON for structured logging
	msg, err := json.Marshal(entry)
	if err != nil {
		msg = []byte(entry.Message)
	}

	// Route to appropriate syslog level
	var writeErr error
	switch entry.Level {
	case LevelDebug:
		writeErr = s.writer.Debug(string(msg))
	case LevelInfo:
		writeErr = s.writer.Info(string(msg))
	case LevelWarn:
		writeErr = s.writer.Warning(string(msg))
	case LevelError:
		writeErr = s.writer.Err(string(msg))
	default:
		writeErr = s.writer.Info(string(msg))
	}

	if writeErr != nil {
		s.errorLogger.LogErrorf("syslog", "failed to write to %s: %v", s.address, writeErr)
	}
	return writeErr
}

// Close closes the syslog connection.
func (s *SyslogWriter) Close() error {
	if s.writer != nil {
		return s.writer.Close()
	}
	return nil
}

// parseFacility converts a facility name to syslog.Priority.
func parseFacility(name string) syslog.Priority {
	switch name {
	case "kern":
		return syslog.LOG_KERN
	case "user":
		return syslog.LOG_USER
	case "mail":
		return syslog.LOG_MAIL
	case "daemon":
		return syslog.LOG_DAEMON
	case "auth":
		return syslog.LOG_AUTH
	case "syslog":
		return syslog.LOG_SYSLOG
	case "lpr":
		return syslog.LOG_LPR
	case "news":
		return syslog.LOG_NEWS
	case "uucp":
		return syslog.LOG_UUCP
	case "cron":
		return syslog.LOG_CRON
	case "authpriv":
		return syslog.LOG_AUTHPRIV
	case "ftp":
		return syslog.LOG_FTP
	case "local0":
		return syslog.LOG_LOCAL0
	case "local1":
		return syslog.LOG_LOCAL1
	case "local2":
		return syslog.LOG_LOCAL2
	case "local3":
		return syslog.LOG_LOCAL3
	case "local4":
		return syslog.LOG_LOCAL4
	case "local5":
		return syslog.LOG_LOCAL5
	case "local6":
		return syslog.LOG_LOCAL6
	case "local7":
		return syslog.LOG_LOCAL7
	default:
		return syslog.LOG_LOCAL0
	}
}
