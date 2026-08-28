package chat

import "errors"

var (
	// errStopBeforeDrain is what makes the teardown ordering assertable
	// rather than merely documented: a Room whose Stop runs before its
	// Drain fails loudly instead of quietly getting the order wrong.
	errStopBeforeDrain = errors.New("chat: Stop called before Drain")

	// ErrBadRoomName is returned by NewRoom for a name no room may have.
	ErrBadRoomName = errors.New("chat: invalid room name")

	// ErrStopRefused is what FailStopKey's Stop returns, so the suite can
	// check that an eviction which does not come out clean is counted.
	ErrStopRefused = errors.New("chat: this room refuses to stop")
)
