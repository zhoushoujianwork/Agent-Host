package harness

// FailureKind describes a native failure without exposing its diagnostics to
// the channel. It does not establish whether tool effects were rolled back.
type FailureKind string

const (
	FailureStartup  FailureKind = "startup"
	FailureProvider FailureKind = "provider"
	FailureProcess  FailureKind = "process"
	FailureDeadline FailureKind = "deadline"
	FailureCanceled FailureKind = "canceled"
	FailureUnknown  FailureKind = "unknown"
)

// ExecutionError retains the original error chain for local evidence while
// allowing presentation to use a category rather than interpret error strings.
type ExecutionError struct {
	Kind  FailureKind
	Cause error
}

func (e *ExecutionError) Error() string { return e.Cause.Error() }
func (e *ExecutionError) Unwrap() error { return e.Cause }
