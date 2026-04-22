// Package codaexit defines the exit code contract that coda-core emits
// and that callers (tests, plugins via exec, tooling) can pre-handle.
// These values are stable across v2 releases — see docs/v2-lifecycle.md.
package codaexit

const (
	// Success indicates the operation completed.
	Success = 0

	// UserError indicates bad arguments, not-found, duplicate, or similar
	// caller-correctable conditions.
	UserError = 1

	// DBError indicates a schema or SQLite failure. Typically not
	// recoverable without intervention.
	DBError = 2

	// LifecycleBlocked indicates a fatal hook refused the lifecycle
	// transition. Callers MUST NOT retry — user action is required.
	// Reserved for the plugin system (card #150); coda-core does not
	// yet emit this value but the contract is stable.
	LifecycleBlocked = 3
)
