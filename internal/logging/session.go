package logging

import (
	"time"

	"github.com/google/uuid"
)

// Context carries per-session metadata that is attached to every dispatched
// log entry for audit-grade querying. Built once per `devsandbox claude`
// invocation and applied to the dispatcher via Dispatcher.SetSessionFields.
type Context struct {
	SessionID   string
	SandboxName string
	SandboxPath string
	ProjectDir  string
	Isolator    string
	PID         int
	Version     string
	StartTime   time.Time
}

// NewContext constructs a Context with a freshly generated UUIDv7 session ID
// and the current wall-clock start time. Caller fills the remaining fields.
func NewContext(sandboxName, sandboxPath, projectDir, isolator, version string, pid int) (*Context, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	return &Context{
		SessionID:   id.String(),
		SandboxName: sandboxName,
		SandboxPath: sandboxPath,
		ProjectDir:  projectDir,
		Isolator:    isolator,
		PID:         pid,
		Version:     version,
		StartTime:   time.Now(),
	}, nil
}

// Fields returns the per-entry merge map applied to every dispatched entry
// when the Context is attached to a Dispatcher via SetSessionFields.
func (c *Context) Fields() map[string]any {
	if c == nil {
		return nil
	}
	return map[string]any{
		"session_id":         c.SessionID,
		"sandbox_name":       c.SandboxName,
		"sandbox_path":       c.SandboxPath,
		"project_dir":        c.ProjectDir,
		"isolator":           c.Isolator,
		"pid":                c.PID,
		"devsandbox_version": c.Version,
	}
}
