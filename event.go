package rod

import "github.com/rah-0/rod/lib/proto"

// EventHandler binds one protocol event type to its callback. Construct it with On.
type EventHandler struct {
	method string
	handle func(*Message) bool
}

// On handles events of type E. Return true to stop the EachEvent wait, or false
// to continue. The session ID identifies the page that emitted a browser event.
// Events are shallow copies; treat their referenced data as read-only.
func On[E proto.Event](callback func(*E, proto.TargetSessionID) bool) EventHandler {
	if callback == nil {
		panic("rod: nil event callback")
	}
	var event E
	return EventHandler{
		method: event.ProtoEvent(),
		handle: func(msg *Message) bool {
			var event E
			msg.Load(&event)
			return callback(&event, msg.SessionID)
		},
	}
}
