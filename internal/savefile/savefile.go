// Package savefile reads CK3 save files without ever writing to them.
//
// It exists so ck3-index can answer questions about a save that arrived from
// an untrusted source, such as a chat group upload. Every entry point is
// bounded, and malformed input is reported as a typed error rather than a
// panic or a silently wrong answer.
//
// The reference implementation for the binary format is the development-only
// Rust suite under tools/jomini-oracle, which stays the differential-test
// authority for this package. Nothing here shells out to it.
package savefile

import (
	"errors"
	"fmt"
)

// Limits bounds every resource a single read may consume.
//
// The zero value is not usable; call DefaultLimits.
type Limits struct {
	// MaxFileBytes caps the whole save file.
	MaxFileBytes int64
	// MaxSectionBytes caps one decoded section that is held in memory. It
	// sizes the metadata, which is small and read whole.
	MaxSectionBytes int64
	// MaxGamestateBytes caps the decompressed gamestate. It is far larger
	// than MaxSectionBytes because the gamestate is streamed rather than
	// held, and a long campaign legitimately reaches hundreds of megabytes.
	// Exceeding it is refused outright; a truncated gamestate would silently
	// turn "not found" into a lie.
	MaxGamestateBytes int64
	// MaxTokens caps how many tokens a single section may yield.
	MaxTokens int64
	// MaxDepth caps container nesting.
	MaxDepth int
	// MaxStringBytes caps one quoted or unquoted scalar.
	MaxStringBytes int
	// MaxArrayItems caps how many elements a reported list may hold.
	MaxArrayItems int
}

// DefaultLimits are sized for real CK3 saves with generous headroom, not for
// the largest value the format could express.
//
// A 1066-start save has 34 KiB of metadata; 8 MiB leaves room for very late
// megacampaigns while still refusing an obviously hostile declaration.
func DefaultLimits() Limits {
	return Limits{
		MaxFileBytes:      512 << 20,
		MaxSectionBytes:   8 << 20,
		MaxGamestateBytes: 2 << 30,
		MaxTokens:         4 << 20,
		MaxDepth:          128,
		MaxStringBytes:    64 << 10,
		MaxArrayItems:     4096,
	}
}

// gamestateCeiling is the effective gamestate cap, falling back to the
// section cap for a zero-valued Limits so a partially filled struct cannot
// silently allow an unbounded read.
func (l Limits) gamestateCeiling() int64 {
	if l.MaxGamestateBytes > 0 {
		return l.MaxGamestateBytes
	}
	return l.MaxSectionBytes
}

// Error classifies a refusal so callers can map it onto a stable code.
type Error struct {
	// Kind is a short, stable machine-readable reason.
	Kind ErrorKind
	// Detail explains the refusal without echoing untrusted bytes.
	Detail string
	// Err is an optional wrapped cause.
	Err error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Kind, e.Detail, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Detail)
}

func (e *Error) Unwrap() error { return e.Err }

// ErrorKind enumerates every way a save read can be refused.
type ErrorKind string

const (
	// ErrHeader means the 24-byte save header is absent or malformed.
	ErrHeader ErrorKind = "malformed_header"
	// ErrUnsupportedLayout means the header names a layout this package
	// does not read, such as a text save.
	ErrUnsupportedLayout ErrorKind = "unsupported_layout"
	// ErrContainerMismatch means the header and the container disagree.
	ErrContainerMismatch ErrorKind = "container_mismatch"
	// ErrBounds means a declared offset or length does not fit the file.
	ErrBounds ErrorKind = "bounds"
	// ErrTooLarge means a documented limit was exceeded.
	ErrTooLarge ErrorKind = "too_large"
	// ErrTruncated means the token stream ended mid-value.
	ErrTruncated ErrorKind = "truncated"
	// ErrMalformedToken means an unreadable or unsupported token was found.
	ErrMalformedToken ErrorKind = "malformed_token"
	// ErrArchive means the embedded ZIP could not be read.
	ErrArchive ErrorKind = "archive"
	// ErrTokenMap means no supplied token map matches this save.
	ErrTokenMap ErrorKind = "token_map"
)

func newError(kind ErrorKind, detail string) *Error {
	return &Error{Kind: kind, Detail: detail}
}

func wrapError(kind ErrorKind, detail string, err error) *Error {
	return &Error{Kind: kind, Detail: detail, Err: err}
}

// KindOf reports the ErrorKind carried by err, or an empty kind.
func KindOf(err error) ErrorKind {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	return ""
}
