package rod

import "github.com/rah-0/rod/lib/proto"

// EventHandler binds one protocol event type to its callback. Construct it with On.
type EventHandler struct {
	method string
	handle func(*Message) (stop bool, err error)
}

// On handles events of type E. Return true to stop the EachEvent wait, or false
// to continue. The session ID identifies the page that emitted a browser event.
// Events are shallow copies; treat their referenced data as read-only. An event
// that cannot be decoded as E, including one that lacks a field the protocol
// requires (see [Message.Load]), ends the wait, which returns the decoding error.
func On[E proto.Event](callback func(*E, proto.TargetSessionID) bool) EventHandler {
	if callback == nil {
		panic("rod: nil event callback")
	}
	var event E
	return EventHandler{
		method: event.ProtoEvent(),
		handle: func(msg *Message) (bool, error) {
			var event E
			ok, err := msg.Load(&event)
			if err != nil {
				return true, err
			}
			if !ok {
				return false, nil
			}
			return callback(&event, msg.SessionID), nil
		},
	}
}

// onEvent is On for Rod's own waits: a callback that returns an error, such as
// one for an event that lacks a field the wait uses, ends the wait with it.
func onEvent[E proto.Event](callback func(*E, proto.TargetSessionID) (bool, error)) EventHandler {
	var event E
	return EventHandler{
		method: event.ProtoEvent(),
		handle: func(msg *Message) (bool, error) {
			var event E
			ok, err := msg.Load(&event)
			if err != nil || !ok {
				return err != nil, err
			}
			stop, err := callback(&event, msg.SessionID)
			return stop || err != nil, err
		},
	}
}
