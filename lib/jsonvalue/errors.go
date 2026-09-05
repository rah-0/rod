package jsonvalue

import "errors"

var (
	// ErrNoValue means an uninitialized Value cannot decode into a destination.
	ErrNoValue = errors.New("jsonvalue: no value to unmarshal")
	// ErrValueDecoded means the raw JSON has already been decoded.
	ErrValueDecoded = errors.New("jsonvalue: value has already been decoded")
)
