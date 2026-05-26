// Package push abstracts how a status payload is delivered to its final
// resting place. Two destinations are supported: a local filesystem path
// (single-PC setup) and a path on a remote host reached via SSH (multi-PC
// setup). Both implementations write atomically: callers always see a fully
// formed JSON or the previous one, never a half-written file.
package push

import "context"

type Pusher interface {
	// Push delivers payload to the configured destination. Implementations
	// must be safe to call concurrently with a reader of the destination.
	Push(ctx context.Context, payload []byte) error

	// Describe returns a human-readable identifier of the destination,
	// used by `clawtopd doctor` and log output.
	Describe() string
}
