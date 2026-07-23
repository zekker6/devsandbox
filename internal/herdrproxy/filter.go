package herdrproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

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

	// CurrentPaneID is the pane herdr created for this process, read from the
	// host environment. It is host-derived: a report naming any other pane is
	// denied, and an empty value denies every report.
	CurrentPaneID string

	// ExpectedAgent is the canonical agent devsandbox was asked to launch,
	// derived host-side from the command argv and never from a report. A report
	// claiming a different agent is denied, and an empty value denies every
	// report.
	ExpectedAgent string

	// AgentSessionDir bounds agent_session_path. It is host-derived from the
	// launched agent's own bindings, expressed as the path the sandbox sees,
	// because that is what the in-sandbox integration reports.
	AgentSessionDir string
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
	case methodPaneReportAgentSession:
		return f.decidePaneReportAgentSession(d, req)
	case methodPaneReportAgent:
		return f.decidePaneReportAgent(d, req)
	case methodPaneReleaseAgent:
		return f.decidePaneReleaseAgent(d, req)
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

// maxSessionIDBytes and maxStartSourceBytes bound the two sandbox-controlled
// tokens this filter forwards.
const (
	maxSessionIDBytes   = 128
	maxStartSourceBytes = 64
	maxSessionPathBytes = 4096
)

// paneReportAgentSessionParams mirrors herdr v0.7.4's PaneReportAgentSessionParams
// (7 fields). Anything else is rejected by strictUnmarshal.
type paneReportAgentSessionParams struct {
	PaneID             string `json:"pane_id"`
	Source             string `json:"source"`
	Agent              string `json:"agent"`
	Seq                *int64 `json:"seq,omitempty"`
	AgentSessionID     string `json:"agent_session_id,omitempty"`
	AgentSessionPath   string `json:"agent_session_path,omitempty"`
	SessionStartSource string `json:"session_start_source,omitempty"`
}

// decidePaneReportAgentSession vets a native session report.
//
// Every anchor it checks against is host-derived: the pane herdr created for
// this process and the agent devsandbox was asked to launch. The report can
// therefore name only itself. No deny or allow reason repeats a session id —
// these reasons are logged, and the id is the one secret this method carries.
func (f *Filter) decidePaneReportAgentSession(d Decision, req request) Decision {
	var p paneReportAgentSessionParams
	if err := strictUnmarshal(req.Params, &p); err != nil {
		d.Reason = "pane.report_agent_session: " + err.Error()
		return d
	}

	if why := f.agentAnchorReason(p.PaneID, p.Source, p.Agent, p.Seq); why != "" {
		d.Reason = "pane.report_agent_session: " + why
		return d
	}

	if p.AgentSessionID == "" && p.AgentSessionPath == "" {
		d.Reason = "pane.report_agent_session: neither agent_session_id nor agent_session_path is present"
		return d
	}

	// herdr shell-quotes this value and types it into a host pane shell on
	// restore. Restricting the charset here means the safety of that never
	// depends on a third-party quoter this project does not version.
	if p.AgentSessionID != "" && !isRestrictedToken(p.AgentSessionID, maxSessionIDBytes) {
		d.Reason = "pane.report_agent_session: agent_session_id is not a permitted token"
		return d
	}

	// The transcript filename embeds the session id, so a reason naming the path
	// would leak it just as surely as naming the id.
	if p.AgentSessionPath != "" {
		if why := f.agentSessionPathReason(p.AgentSessionPath); why != "" {
			d.Reason = "pane.report_agent_session: " + why
			return d
		}
	}

	if p.SessionStartSource != "" && !isRestrictedToken(p.SessionStartSource, maxStartSourceBytes) {
		d.Reason = "pane.report_agent_session: session_start_source is not a permitted token"
		return d
	}

	d.Allow = true
	d.Reason = "pane.report_agent_session for this pane's launched agent"
	return d
}

// paneReportAgentParams mirrors herdr v0.7.4's PaneReportAgentParams (8 fields).
// Pi's integration sends this on every lifecycle transition; Claude's never
// does. Anything else is rejected by strictUnmarshal.
type paneReportAgentParams struct {
	PaneID           string `json:"pane_id"`
	Source           string `json:"source"`
	Agent            string `json:"agent"`
	State            string `json:"state"`
	Message          string `json:"message,omitempty"`
	Seq              *int64 `json:"seq,omitempty"`
	AgentSessionID   string `json:"agent_session_id,omitempty"`
	AgentSessionPath string `json:"agent_session_path,omitempty"`
}

// reportAgentStates is the set of lifecycle states a report may carry. herdr's
// PaneAgentState also decodes "unknown", which is left out because no shipped
// integration sends it and a state nothing produces is one less value the
// sandbox can put on the user's screen.
var reportAgentStates = []string{"idle", "working", "blocked"}

// decidePaneReportAgent vets a lifecycle state report.
//
// It carries the same host-derived anchors as the session report, plus a state
// and a free-text message herdr renders in the pane. The session ref is
// optional here: Pi attaches it once it knows one, and omits it before then.
func (f *Filter) decidePaneReportAgent(d Decision, req request) Decision {
	var p paneReportAgentParams
	if err := strictUnmarshal(req.Params, &p); err != nil {
		d.Reason = "pane.report_agent: " + err.Error()
		return d
	}

	if why := f.agentAnchorReason(p.PaneID, p.Source, p.Agent, p.Seq); why != "" {
		d.Reason = "pane.report_agent: " + why
		return d
	}

	if !slices.Contains(reportAgentStates, p.State) {
		d.Reason = fmt.Sprintf("pane.report_agent: state %q is not one of %v", p.State, reportAgentStates)
		return d
	}

	// The message is a one-line status label, so a control character in it is
	// either an escape sequence aimed at the host terminal or a multi-line
	// provider error that does not belong in a label. Deny rather than rewrite:
	// forwarding the line untouched is what keeps this method auditable.
	if why := agentMessageReason(p.Message); why != "" {
		d.Reason = "pane.report_agent: " + why
		return d
	}

	if p.AgentSessionID != "" && !isRestrictedToken(p.AgentSessionID, maxSessionIDBytes) {
		d.Reason = "pane.report_agent: agent_session_id is not a permitted token"
		return d
	}
	if p.AgentSessionPath != "" {
		if why := f.agentSessionPathReason(p.AgentSessionPath); why != "" {
			d.Reason = "pane.report_agent: " + why
			return d
		}
	}

	d.Allow = true
	d.Reason = "pane.report_agent for this pane's launched agent"
	return d
}

// paneReleaseAgentParams mirrors herdr v0.7.4's PaneReleaseAgentParams
// (4 fields). Pi sends it when the agent process quits.
type paneReleaseAgentParams struct {
	PaneID string `json:"pane_id"`
	Source string `json:"source"`
	Agent  string `json:"agent"`
	Seq    *int64 `json:"seq,omitempty"`
}

// decidePaneReleaseAgent vets the release herdr receives when the agent exits.
// It carries no payload beyond the anchors, so releasing is only ever possible
// for this pane's own launched agent.
func (f *Filter) decidePaneReleaseAgent(d Decision, req request) Decision {
	var p paneReleaseAgentParams
	if err := strictUnmarshal(req.Params, &p); err != nil {
		d.Reason = "pane.release_agent: " + err.Error()
		return d
	}

	if why := f.agentAnchorReason(p.PaneID, p.Source, p.Agent, p.Seq); why != "" {
		d.Reason = "pane.release_agent: " + why
		return d
	}

	d.Allow = true
	d.Reason = "pane.release_agent for this pane's launched agent"
	return d
}

// agentAnchorReason applies the host-derived checks every agent report shares,
// returning the failing rule or "" when the report names only itself. An empty
// anchor denies rather than matching, so a proxy started without one grants
// nothing.
func (f *Filter) agentAnchorReason(paneID, source, agent string, seq *int64) string {
	if f.cfg.CurrentPaneID == "" {
		return "no current pane id configured"
	}
	if paneID != f.cfg.CurrentPaneID {
		return fmt.Sprintf("pane %q is not this sandbox's pane", paneID)
	}

	if f.cfg.ExpectedAgent == "" {
		return "no launched agent configured"
	}
	if agent != f.cfg.ExpectedAgent {
		return fmt.Sprintf("agent %q is not the launched agent", agent)
	}
	// herdr's own source string for every agent in the v0.7.4 table.
	if want := "herdr:" + f.cfg.ExpectedAgent; source != want {
		return fmt.Sprintf("source %q is not %q", source, want)
	}

	if seq != nil && *seq < 0 {
		return "seq is negative"
	}
	return ""
}

// agentMessageReason bounds the free-text label a state report puts on screen,
// returning the failing rule or "" when it is acceptable. The message is never
// repeated in the reason: it can carry an error string naming a session file.
func agentMessageReason(msg string) string {
	if len(msg) > maxLabelBytes {
		return "message exceeds the length limit"
	}
	if hasControlRune(msg) {
		return "message contains a control character"
	}
	return ""
}

// hasControlRune reports whether s contains a character a terminal may act on
// rather than render.
//
// The check is per rune, not per byte: a byte-wise scan for < 0x20 sees only C0,
// while the C1 controls (U+0080-U+009F, U+009B being CSI) arrive UTF-8-encoded
// as bytes >= 0xC2 and would pass it - defeating the check in exactly the case
// it exists for, since herdr renders this text in a host pane. Format characters
// (Cf) are refused with them: a bidi override or zero-width joiner in a
// one-line status label can only misrepresent what the user is looking at.
func hasControlRune(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	return false
}

// agentSessionPathReason validates agent_session_path, returning the failing
// rule or "" when the path is acceptable. The path is forwarded unmodified.
//
// Confinement is deliberately LEXICAL, and pathWithin is deliberately NOT used
// here: pathWithin resolves symlinks against the host filesystem, while this
// path names a directory inside the sandbox's overlay that the host cannot see.
// Resolving a sandbox path host-side proves nothing about what the sandbox
// would reach, so every ".." component is rejected outright instead of
// pretending the resolution means something.
//
// The charset is restricted for the same reason agent_session_id's is: herdr
// persists a path-kind session ref exactly as it persists an id, and on restore
// agent_resume::plan turns either into resume argv that shell_command_from_argv
// quotes and types into a host pane shell. Pi reports only a path, so for Pi
// the path IS the value that reaches that shell. Confinement to the session
// directory bounds where it points, not what it contains - a file the sandbox
// creates under that directory can still be named `x'; curl ...|sh; '.jsonl` -
// so the safety of the whole scheme would otherwise rest on an unversioned
// third-party quoter, which is the trust this filter refuses everywhere else.
func (f *Filter) agentSessionPathReason(p string) string {
	if len(p) > maxSessionPathBytes {
		return "agent_session_path exceeds the length limit"
	}
	if !filepath.IsAbs(p) {
		return "agent_session_path is not absolute"
	}
	if hasControlRune(p) {
		return "agent_session_path contains a control character"
	}
	if !isRestrictedPath(p) {
		return "agent_session_path contains a character outside [A-Za-z0-9._-] and the separator"
	}
	if slices.Contains(strings.Split(p, string(filepath.Separator)), "..") {
		return "agent_session_path contains a \"..\" component"
	}

	// An unbounded path is not a path this filter can vouch for: deny rather
	// than skip the confinement check.
	base := f.cfg.AgentSessionDir
	if base == "" || !filepath.IsAbs(base) {
		return "no agent session directory configured to bound agent_session_path"
	}
	base = filepath.Clean(base)
	if !strings.HasPrefix(filepath.Clean(p), base+string(filepath.Separator)) {
		return "agent_session_path is outside the launched agent's session directory"
	}
	return ""
}

// isRestrictedToken reports whether s is safe to hand to a host shell as a bare
// word: 1..maxLen bytes of [A-Za-z0-9._-], not starting with a hyphen so it can
// never be read as a flag, and neither "." nor ".." in case herdr ever uses the
// value as a path component.
func isRestrictedToken(s string, maxLen int) bool {
	if s == "" || len(s) > maxLen {
		return false
	}
	if s[0] == '-' || s == "." || s == ".." {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isRestrictedByte(s[i]) {
			return false
		}
	}
	return true
}

// isRestrictedPath reports whether every byte of p is safe to hand to a host
// shell as a bare word: the token charset plus the path separator. Component
// shape is not constrained beyond that - a leading hyphen is harmless in a
// component of an absolute path, and Claude's own project directories start
// with one.
func isRestrictedPath(p string) bool {
	for i := 0; i < len(p); i++ {
		if p[i] != '/' && !isRestrictedByte(p[i]) {
			return false
		}
	}
	return true
}

func isRestrictedByte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z':
	case c >= 'a' && c <= 'z':
	case c >= '0' && c <= '9':
	case c == '.' || c == '_' || c == '-':
	default:
		return false
	}
	return true
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
