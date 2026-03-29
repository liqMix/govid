package govid

import "errors"

var (
	ErrSeekNotSupported = errors.New("govid: seek not supported")
	ErrNoVideoTrack     = errors.New("govid: no video track found")
	ErrInvalidData      = errors.New("govid: invalid data")
)
