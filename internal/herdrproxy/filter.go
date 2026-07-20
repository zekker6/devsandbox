package herdrproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"devsandbox/internal/cmdpattern"
)

// maxLabelBytes bounds free-text fields the sandbox can put on screen.
const maxLabelBytes = 512

// Decision is the filter's verdict on one request line.
type Decision struct {
	Allow  bool
	ID     string
	Method string
	Reason string

	// Rewritten, when non-nil, replaces the original line before forwarding.
	// Used when a launch script has been relocated out of sandbox reach.
	Rewritten []byte
}

// FilterConfig configures a Filter.
type FilterConfig struct {
	// Capabilities determines which methods are reachable at all.
	Capabilities []Capability

	// LaunchScript validates the body of a relocated launch script.
	LaunchScript cmdpattern.ScriptPattern

	// LaunchPatterns validates inline launch commands (the non-script form).
	LaunchPatterns []cmdpattern.CommandPattern

	// OwnedTabs and OwnedPanes hold ids created by this sandbox. Mutations are
	// permitted only against these.
	OwnedTabs  *cmdpattern.OwnedSet[string]
	OwnedPanes *cmdpattern.OwnedSet[string]

	// Relocator moves launch scripts out of sandbox reach. Required whenever
	// CapLaunchOverlay is enabled.
	Relocator *Relocator

	// ProjectDir bounds where a new tab may be opened.
	ProjectDir string

	// WorkspaceID, when set, is the only workspace a tab may be created in.
	WorkspaceID string
}

// Filter decides whether a request may reach the herdr server.
type Filter struct {
	cfg     FilterConfig
	methods map[string]struct{}
}

func NewFilter(cfg FilterConfig) *Filter {
	return &Filter{cfg: cfg, methods: allowedMethods(cfg.Capabilities)}
}

// Decide evaluates one request line. Anything not explicitly permitted is
// denied, including every method the configured capabilities do not name.
func (f *Filter) Decide(raw []byte) Decision {
	req, err := parseRequest(raw)
	if err != nil {
		return Decision{Reason: err.Error()}
	}

	d := Decision{ID: req.ID, Method: req.Method}

	if _, ok := f.methods[req.Method]; !ok {
		d.Reason = fmt.Sprintf("method %q is not permitted by the enabled capabilities", req.Method)
		return d
	}

	switch req.Method {
	case methodPing:
		// No parameters to vet: ping carries none and changes nothing.
		d.Allow = true
		d.Reason = "ping (liveness only)"
		return d
	case methodTabCreate:
		return f.decideTabCreate(d, req)
	case methodPaneSendInput:
		return f.decidePaneSendInput(d, req, raw)
	case methodTabClose:
		return f.decideTabClose(d, req)
	case methodNotificationShow:
		return f.decideNotificationShow(d, req)
	}

	// Unreachable while methodsFor and this switch agree; deny rather than
	// assume, so adding a method to a capability cannot silently bypass
	// validation.
	d.Reason = fmt.Sprintf("method %q has no validator", req.Method)
	return d
}

// tabCreateParams mirrors the params herdr's CLI sends for `tab create`.
type tabCreateParams struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	Cwd         string `json:"cwd,omitempty"`
	Focus       bool   `json:"focus,omitempty"`
	Label       string `json:"label,omitempty"`
}

func (f *Filter) decideTabCreate(d Decision, req request) Decision {
	var p tabCreateParams
	if err := strictUnmarshal(req.Params, &p); err != nil {
		d.Reason = "tab.create: " + err.Error()
		return d
	}

	if len(p.Label) > maxLabelBytes {
		d.Reason = "tab.create: label exceeds the length limit"
		return d
	}

	// A new tab must open inside the project. Resolve symlinks first: the
	// project directory is sandbox-writable, so a plain prefix check could be
	// satisfied by a link pointing anywhere on the host.
	if p.Cwd != "" {
		if f.cfg.ProjectDir == "" {
			d.Reason = "tab.create: no project directory configured to bound cwd"
			return d
		}
		if !pathWithin(p.Cwd, f.cfg.ProjectDir) {
			d.Reason = fmt.Sprintf("tab.create: cwd %q is outside the project directory", p.Cwd)
			return d
		}
	}

	// Pin the tab to the caller's own workspace so the sandbox cannot open
	// tabs in whatever workspace the user happens to be looking at.
	if f.cfg.WorkspaceID != "" && p.WorkspaceID != "" && p.WorkspaceID != f.cfg.WorkspaceID {
		d.Reason = fmt.Sprintf("tab.create: workspace_id %q is not the sandbox's workspace", p.WorkspaceID)
		return d
	}

	d.Allow = true
	d.Reason = "tab.create within project"
	return d
}

// paneSendInputParams mirrors what `herdr pane run` sends.
type paneSendInputParams struct {
	PaneID string   `json:"pane_id"`
	Text   string   `json:"text"`
	Keys   []string `json:"keys,omitempty"`
}

func (f *Filter) decidePaneSendInput(d Decision, req request, raw []byte) Decision {
	var p paneSendInputParams
	if err := strictUnmarshal(req.Params, &p); err != nil {
		d.Reason = "pane.send_input: " + err.Error()
		return d
	}

	// pane.send_input is generic keystroke injection: without an ownership
	// check it would type into any pane the user has open.
	if f.cfg.OwnedPanes == nil || !f.cfg.OwnedPanes.Contains(p.PaneID) {
		d.Reason = fmt.Sprintf("pane.send_input: pane %q was not created by this sandbox", p.PaneID)
		return d
	}

	// Only the launcher's shape is accepted: one command plus Enter. Anything
	// else is a keystroke stream this filter cannot reason about.
	if !slices.Equal(p.Keys, []string{"Enter"}) {
		d.Reason = fmt.Sprintf("pane.send_input: keys %v is not exactly [\"Enter\"]", p.Keys)
		return d
	}

	text, err := f.validateLaunchText(p.Text)
	if err != nil {
		d.Reason = "pane.send_input: " + err.Error()
		return d
	}

	if text != p.Text {
		rewritten, err := rewriteSendInputText(raw, text)
		if err != nil {
			d.Reason = "pane.send_input: " + err.Error()
			return d
		}
		d.Rewritten = rewritten
	}

	d.Allow = true
	d.Reason = "pane.send_input into an owned pane"
	return d
}

// validateLaunchText vets the command a pane is asked to run, relocating a
// script payload when present. It returns the text to forward.
func (f *Filter) validateLaunchText(text string) (string, error) {
	if f.cfg.Relocator != nil {
		rewritten, isScript, err := f.cfg.Relocator.Relocate(text, f.cfg.LaunchScript)
		if err != nil {
			return "", err
		}
		if isScript {
			return rewritten, nil
		}
	} else if _, isScript := parseShScript(text); isScript {
		// A script payload with nowhere safe to put it must not run in place.
		return "", fmt.Errorf("script launches are unavailable (no relocation directory)")
	}

	// Inline form: match the command as plain argv.
	argv := strings.Fields(text)
	if len(argv) == 0 {
		return "", fmt.Errorf("empty command")
	}
	for _, pat := range f.cfg.LaunchPatterns {
		if pat.MatchesArgv(argv) {
			return text, nil
		}
	}
	return "", fmt.Errorf("command does not match any declared launch pattern")
}

// rewriteSendInputText replaces params.text in a request line, preserving every
// other field exactly as the client sent it.
func rewriteSendInputText(raw []byte, text string) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("rewrite: %w", err)
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(envelope["params"], &params); err != nil {
		return nil, fmt.Errorf("rewrite params: %w", err)
	}
	encoded, err := json.Marshal(text)
	if err != nil {
		return nil, fmt.Errorf("rewrite text: %w", err)
	}
	params["text"] = encoded

	encodedParams, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("rewrite params: %w", err)
	}
	envelope["params"] = encodedParams

	out, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("rewrite envelope: %w", err)
	}
	if bytes.Contains(out, []byte("\n")) {
		return nil, fmt.Errorf("rewrite produced an embedded newline")
	}
	return out, nil
}

type tabCloseParams struct {
	TabID string `json:"tab_id"`
}

func (f *Filter) decideTabClose(d Decision, req request) Decision {
	var p tabCloseParams
	if err := strictUnmarshal(req.Params, &p); err != nil {
		d.Reason = "tab.close: " + err.Error()
		return d
	}
	if f.cfg.OwnedTabs == nil || !f.cfg.OwnedTabs.Contains(p.TabID) {
		d.Reason = fmt.Sprintf("tab.close: tab %q was not created by this sandbox", p.TabID)
		return d
	}
	d.Allow = true
	d.Reason = "tab.close on an owned tab"
	return d
}

type notificationShowParams struct {
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Position string `json:"position,omitempty"`
	Sound    string `json:"sound,omitempty"`
}

func (f *Filter) decideNotificationShow(d Decision, req request) Decision {
	var p notificationShowParams
	if err := strictUnmarshal(req.Params, &p); err != nil {
		d.Reason = "notification.show: " + err.Error()
		return d
	}
	if p.Title == "" {
		d.Reason = "notification.show: title is empty"
		return d
	}
	if len(p.Title) > maxLabelBytes || len(p.Body) > maxLabelBytes {
		d.Reason = "notification.show: title or body exceeds the length limit"
		return d
	}
	d.Allow = true
	d.Reason = "notification.show"
	return d
}

// strictUnmarshal decodes params and rejects any field the struct does not
// declare, so a parameter this filter does not understand cannot ride along
// unchecked.
func strictUnmarshal(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return nil
}

// pathWithin reports whether path resolves to base or somewhere beneath it.
//
// Both sides are symlink-resolved before comparison. The sandbox can create
// symlinks inside the project directory, so comparing the literal strings
// would let `<project>/link -> /` pass a prefix test.
func pathWithin(path, base string) bool {
	realPath, err := resolveExisting(path)
	if err != nil {
		return false
	}
	realBase, err := resolveExisting(base)
	if err != nil {
		return false
	}
	if realPath == realBase {
		return true
	}
	return strings.HasPrefix(realPath, realBase+string(filepath.Separator))
}

// resolveExisting canonicalizes p, walking up to the nearest existing ancestor
// so a not-yet-created directory still resolves against real components.
func resolveExisting(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("path %q is not absolute", p)
	}
	clean := filepath.Clean(p)
	resolved, err := filepath.EvalSymlinks(clean)
	if err == nil {
		return resolved, nil
	}

	parent := filepath.Dir(clean)
	if parent == clean {
		return "", fmt.Errorf("resolve %q: %w", p, err)
	}
	resolvedParent, perr := resolveExisting(parent)
	if perr != nil {
		return "", perr
	}
	return filepath.Join(resolvedParent, filepath.Base(clean)), nil
}
