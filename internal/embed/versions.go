package embed

// BwrapVersion and PastaVersion are set via ldflags at build time.
// They default to "unknown" which causes cache extraction to use a
// predictable directory, but `devsandbox doctor` will flag the mismatch.
var (
	// BwrapVersion is the bubblewrap version embedded in this build.
	BwrapVersion = "unknown"

	// PastaVersion is the passt/pasta version embedded in this build.
	PastaVersion = "unknown"
)

const (
	// PastaHasMapHostLoopback indicates whether the embedded pasta supports --map-host-loopback.
	// This eliminates the runtime `pasta --help` check for the embedded binary.
	PastaHasMapHostLoopback = true
)
