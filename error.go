package rod

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

// ErrBrowserDisconnected means the browser event stream closed before a wait completed.
var ErrBrowserDisconnected = errors.New("browser connection closed")

// errWaitCompleted ends an event subscription after an internal idle timer succeeds.
var errWaitCompleted = errors.New("wait completed")

// missingField returns the error that [proto.DecodeStrict] reports for protocol
// data of the type typ without the required field at path. Rod's methods
// return it for a field that they use and that [proto.DecodeLenient] left
// missing.
func missingField(typ, path string) error {
	return &proto.MissingFieldError{Type: typ, Path: path}
}

// missingEventField returns the error that [Message.Load] returns with
// [proto.DecodeStrict] for an event of method, whose type typ lacks the
// required field at path.
func missingEventField(method, typ, path string) error {
	return fmt.Errorf("rod: decode %s event: %w", method, missingField(typ, path))
}

// requireEntries returns the error of [missingField] for a required list at
// path that is nil, or that has a nil entry. [proto.DecodeLenient] leaves a
// missing list nil and a null entry nil; [proto.DecodeStrict] rejects both.
func requireEntries[T any](list []*T, typ, path string) error {
	if list == nil {
		return missingField(typ, path)
	}
	for i, entry := range list {
		if entry == nil {
			return missingField(typ, path+"["+strconv.Itoa(i)+"]")
		}
	}
	return nil
}

// lenientMissing reports whether value is the zero value that
// [proto.DecodeLenient] leaves for a missing field. Rod's methods treat such an
// identifier as missing instead of using it, for example, to send page
// commands to the browser session. With [proto.DecodeStrict], a missing field
// fails to decode, and they use the identifier that the endpoint sent.
func lenientMissing[T comparable](mode proto.Decoding, value T) bool {
	var zero T
	return value == zero && mode == proto.DecodeLenient
}

// TryError error.
type TryError struct {
	Value any
	Stack string
}

func (e *TryError) Error() string {
	return fmt.Sprintf("error value: %#v\n%s", e.Value, e.Stack)
}

// Is interface.
func (e *TryError) Is(err error) bool { _, ok := err.(*TryError); return ok }

// Unwrap stdlib interface.
func (e *TryError) Unwrap() error {
	if err, ok := e.Value.(error); ok {
		return err
	}
	return fmt.Errorf("%v", e.Value)
}

// ExpectElementError error.
type ExpectElementError struct {
	*proto.RuntimeRemoteObject
}

func (e *ExpectElementError) Error() string {
	return fmt.Sprintf("expect js to return an element, but got: %s", utils.MustToJSON(e))
}

// Is interface.
func (e *ExpectElementError) Is(err error) bool { _, ok := err.(*ExpectElementError); return ok }

// ExpectElementsError error.
type ExpectElementsError struct {
	*proto.RuntimeRemoteObject
}

func (e *ExpectElementsError) Error() string {
	return fmt.Sprintf("expect js to return an array of elements, but got: %s", utils.MustToJSON(e))
}

// Is interface.
func (e *ExpectElementsError) Is(err error) bool { _, ok := err.(*ExpectElementsError); return ok }

// ElementNotFoundError error.
type ElementNotFoundError struct{}

func (e *ElementNotFoundError) Error() string {
	return "cannot find element"
}

// NotFoundSleeper returns ErrElementNotFound on the first call.
func NotFoundSleeper() utils.Sleeper {
	return func(context.Context) error {
		return &ElementNotFoundError{}
	}
}

// ObjectNotFoundError error.
type ObjectNotFoundError struct {
	*proto.RuntimeRemoteObject
}

func (e *ObjectNotFoundError) Error() string {
	return fmt.Sprintf("cannot find object: %s", utils.MustToJSON(e))
}

// Is interface.
func (e *ObjectNotFoundError) Is(err error) bool { _, ok := err.(*ObjectNotFoundError); return ok }

// EvalError error.
type EvalError struct {
	*proto.RuntimeExceptionDetails
}

func (e *EvalError) Error() string {
	if e.RuntimeExceptionDetails == nil {
		return "eval js error"
	}
	exp := e.Exception
	if exp == nil {
		return "eval js error: " + e.Text
	}
	return fmt.Sprintf("eval js error: %s %s", exp.Description, exp.Value)
}

// Is interface.
func (e *EvalError) Is(err error) bool { _, ok := err.(*EvalError); return ok }

// NavigationError error.
type NavigationError struct {
	Reason string
}

func (e *NavigationError) Error() string {
	return "navigation failed: " + e.Reason
}

// Is interface.
func (e *NavigationError) Is(err error) bool { _, ok := err.(*NavigationError); return ok }

// PageCloseCanceledError error.
type PageCloseCanceledError struct{}

func (e *PageCloseCanceledError) Error() string {
	return "page close canceled"
}

// NotInteractableError error. Check the doc of Element.Interactable for details.
type NotInteractableError struct{}

func (e *NotInteractableError) Error() string {
	return "element is not cursor interactable"
}

// InvisibleShapeError error.
type InvisibleShapeError struct {
	*Element
}

// Error ...
func (e *InvisibleShapeError) Error() string {
	return fmt.Sprintf("element has no visible shape or outside the viewport: %s", e.String())
}

// Is interface.
func (e *InvisibleShapeError) Is(err error) bool { _, ok := err.(*InvisibleShapeError); return ok }

// Unwrap ...
func (e *InvisibleShapeError) Unwrap() error {
	return &NotInteractableError{}
}

// CoveredError error.
type CoveredError struct {
	*Element
}

// Error ...
func (e *CoveredError) Error() string {
	return fmt.Sprintf("element covered by: %s", e.String())
}

// Unwrap ...
func (e *CoveredError) Unwrap() error {
	return &NotInteractableError{}
}

// Is interface.
func (e *CoveredError) Is(err error) bool { _, ok := err.(*CoveredError); return ok }

// NoPointerEventsError error.
type NoPointerEventsError struct {
	*Element
}

// Error ...
func (e *NoPointerEventsError) Error() string {
	return fmt.Sprintf("element's pointer-events is none: %s", e.String())
}

// Unwrap ...
func (e *NoPointerEventsError) Unwrap() error {
	return &NotInteractableError{}
}

// Is interface.
func (e *NoPointerEventsError) Is(err error) bool { _, ok := err.(*NoPointerEventsError); return ok }

// PageNotFoundError error.
type PageNotFoundError struct{}

func (e *PageNotFoundError) Error() string {
	return "cannot find page"
}

// NoShadowRootError error.
type NoShadowRootError struct {
	*Element
}

// Error ...
func (e *NoShadowRootError) Error() string {
	return fmt.Sprintf("element has no shadow root: %s", e.String())
}

// Is interface.
func (e *NoShadowRootError) Is(err error) bool { _, ok := err.(*NoShadowRootError); return ok }
