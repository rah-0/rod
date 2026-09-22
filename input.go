package rod

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rah-0/rod/lib/input"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

// Keyboard represents the keyboard on a page, it's always related the main frame.
type Keyboard struct {
	*keyboardState
	page *Page
}

type keyboardState struct {
	sync.Mutex
	pressed map[input.Key]struct{}
	// Read the last confirmed state without waiting for an in-flight key command.
	modifierBits atomic.Int32
}

func (p *Page) newKeyboard() *Page {
	p.Keyboard = &Keyboard{page: p, keyboardState: &keyboardState{pressed: map[input.Key]struct{}{}}}
	return p
}

func (k *Keyboard) getModifiers() int {
	return int(k.modifierBits.Load())
}

func (k *Keyboard) modifiers() int {
	ms := 0
	for key := range k.pressed {
		ms |= key.Modifier()
	}
	return ms
}

// Press the key down.
// To input characters that are not on the keyboard, such as Chinese or Japanese, you should
// use method like [Page.InsertText].
func (k *Keyboard) Press(key input.Key) error {
	defer k.page.tryTrace(TraceTypeInput, "press key: "+key.Info().Code)()
	k.page.browser.trySlowMotion()

	k.Lock()
	defer k.Unlock()

	if err := key.Encode(proto.InputDispatchKeyEventTypeKeyDown, k.modifiers()|key.Modifier()).Call(k.page); err != nil {
		return err
	}
	k.pressed[key] = struct{}{}
	k.modifierBits.Store(int32(k.modifiers()))
	return nil
}

// Release the key.
func (k *Keyboard) Release(key input.Key) error {
	defer k.page.tryTrace(TraceTypeInput, "release key: "+key.Info().Code)()

	k.Lock()
	defer k.Unlock()

	if _, has := k.pressed[key]; !has {
		return nil
	}

	modifiers := 0
	for pressed := range k.pressed {
		if pressed != key {
			modifiers |= pressed.Modifier()
		}
	}
	if err := key.Encode(proto.InputDispatchKeyEventTypeKeyUp, modifiers).Call(k.page); err != nil {
		return err
	}
	delete(k.pressed, key)
	k.modifierBits.Store(int32(modifiers))
	return nil
}

// Type releases the key after the press.
func (k *Keyboard) Type(keys ...input.Key) (err error) {
	for _, key := range keys {
		err = k.Press(key)
		if err != nil {
			return
		}
		err = k.Release(key)
		if err != nil {
			return
		}
	}
	return
}

// KeyActionType enum.
type KeyActionType int

// KeyActionTypes.
const (
	KeyActionPress KeyActionType = iota
	KeyActionRelease
	KeyActionTypeKey
)

// KeyAction to perform.
type KeyAction struct {
	Type KeyActionType
	Key  input.Key
}

// KeyActions to simulate.
type KeyActions struct {
	keyboard *Keyboard

	Actions []KeyAction
}

// KeyActions simulates the type actions on a physical keyboard.
// Useful when input shortcuts like ctrl+enter .
func (p *Page) KeyActions() *KeyActions {
	return &KeyActions{keyboard: p.Keyboard}
}

// Press keys schedules their release at the end of actions. If an action fails,
// Do also attempts to release keys introduced by this sequence within five seconds.
func (ka *KeyActions) Press(keys ...input.Key) *KeyActions {
	for _, key := range keys {
		ka.Actions = append(ka.Actions, KeyAction{KeyActionPress, key})
	}
	return ka
}

// Release keys.
func (ka *KeyActions) Release(keys ...input.Key) *KeyActions {
	for _, key := range keys {
		ka.Actions = append(ka.Actions, KeyAction{KeyActionRelease, key})
	}
	return ka
}

// Type will release the key immediately after the pressing.
func (ka *KeyActions) Type(keys ...input.Key) *KeyActions {
	for _, key := range keys {
		ka.Actions = append(ka.Actions, KeyAction{KeyActionTypeKey, key})
	}
	return ka
}

// Do the actions.
func (ka *KeyActions) Do() (err error) {
	ka.keyboard.Lock()
	original := make(map[input.Key]bool, len(ka.keyboard.pressed))
	for key := range ka.keyboard.pressed {
		original[key] = true
	}
	ka.keyboard.Unlock()
	defer func() {
		if err == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ka.keyboard.page.ctx), 5*time.Second)
		defer cancel()
		keyboard := ka.keyboard.page.Context(ctx).Keyboard
		// Release only keys introduced by this sequence, preserving held modifiers
		// that belonged to the caller before it started.
		for _, action := range ka.Actions {
			if !original[action.Key] {
				err = errors.Join(err, keyboard.Release(action.Key))
			}
		}
	}()
	for _, a := range ka.balance() {
		switch a.Type {
		case KeyActionPress:
			err = ka.keyboard.Press(a.Key)
		case KeyActionRelease:
			err = ka.keyboard.Release(a.Key)
		case KeyActionTypeKey:
			err = ka.keyboard.Type(a.Key)
		}
		if err != nil {
			return
		}
	}
	return
}

// Make sure there's at least one release after the presses, such as:
//
//	p1,p2,p1,r1 => p1,p2,p1,r1,r2
func (ka *KeyActions) balance() []KeyAction {
	actions := ka.Actions

	h := map[input.Key]bool{}
	for _, a := range actions {
		switch a.Type {
		case KeyActionPress:
			h[a.Key] = true
		case KeyActionRelease, KeyActionTypeKey:
			h[a.Key] = false
		}
	}

	for key, needRelease := range h {
		if needRelease {
			actions = append(actions, KeyAction{KeyActionRelease, key})
		}
	}

	return actions
}

// InsertText is like pasting text into the page.
func (p *Page) InsertText(text string) error {
	defer p.tryTrace(TraceTypeInput, "insert text "+text)()
	p.browser.trySlowMotion()

	err := proto.InputInsertText{Text: text}.Call(p)
	return err
}

// Mouse represents the mouse on a page, it's always related the main frame.
type Mouse struct {
	*mouseState
	page *Page
}

type mouseState struct {
	sync.Mutex
	id string // mouse svg dom element id

	pos proto.Point

	// the buttons is currently being pressed, reflects the press order
	buttons []proto.InputMouseButton
}

func (p *Page) newMouse() *Page {
	p.Mouse = &Mouse{page: p, mouseState: &mouseState{id: utils.RandString(8)}}
	return p
}

// Position of current cursor.
func (m *Mouse) Position() proto.Point {
	m.Lock()
	defer m.Unlock()
	return m.pos
}

// MoveTo the absolute position.
func (m *Mouse) MoveTo(p proto.Point) error {
	m.Lock()
	defer m.Unlock()

	button, buttons := input.EncodeMouseButton(m.buttons)

	m.page.browser.trySlowMotion()

	err := proto.InputDispatchMouseEvent{
		Type:      proto.InputDispatchMouseEventTypeMouseMoved,
		X:         p.X,
		Y:         p.Y,
		Button:    button,
		Buttons:   new(buttons),
		Modifiers: m.page.Keyboard.getModifiers(),
	}.Call(m.page)
	if err != nil {
		return err
	}

	// to make sure set only when call is successful
	m.pos = p

	if m.page.browser.trace {
		if !m.updateMouseTracer() {
			m.initMouseTracer()
			m.updateMouseTracer()
		}
	}

	return nil
}

// MoveAlong the guide function.
// Every time the guide function is called it should return the next mouse position, return true to stop.
// Read the source code of [Mouse.MoveLinear] as an example to use this method.
func (m *Mouse) MoveAlong(guide func() (proto.Point, bool)) error {
	for {
		p, stop := guide()
		if stop {
			return m.MoveTo(p)
		}

		err := m.MoveTo(p)
		if err != nil {
			return err
		}
	}
}

// MoveLinear to the absolute position with the given steps.
// Such as move from (0,0) to (6,6) with 3 steps, the mouse will first move to (2,2) then (4,4) then (6,6).
func (m *Mouse) MoveLinear(to proto.Point, steps int) error {
	p := m.Position()
	step := to.Minus(p).Scale(1 / float64(steps))
	count := 0

	return m.MoveAlong(func() (proto.Point, bool) {
		count++
		if count == steps {
			return to, true
		}

		p = p.Add(step)
		return p, false
	})
}

// Scroll the relative offset with specified steps.
func (m *Mouse) Scroll(offsetX, offsetY float64, steps int) error {
	m.Lock()
	defer m.Unlock()

	defer m.page.tryTrace(TraceTypeInput, fmt.Sprintf("scroll (%.2f, %.2f)", offsetX, offsetY))()
	m.page.browser.trySlowMotion()

	if steps < 1 {
		steps = 1
	}

	button, buttons := input.EncodeMouseButton(m.buttons)

	stepX := offsetX / float64(steps)
	stepY := offsetY / float64(steps)

	for range steps {
		err := proto.InputDispatchMouseEvent{
			Type:      proto.InputDispatchMouseEventTypeMouseWheel,
			Button:    button,
			Buttons:   new(buttons),
			Modifiers: m.page.Keyboard.getModifiers(),
			DeltaX:    stepX,
			DeltaY:    stepY,
			X:         m.pos.X,
			Y:         m.pos.Y,
		}.Call(m.page)
		if err != nil {
			return err
		}
	}

	return nil
}

// Down holds the button down.
func (m *Mouse) Down(button proto.InputMouseButton, clickCount int) error {
	m.Lock()
	defer m.Unlock()

	toButtons := append(append([]proto.InputMouseButton{}, m.buttons...), button)

	_, buttons := input.EncodeMouseButton(toButtons)

	err := proto.InputDispatchMouseEvent{
		Type:       proto.InputDispatchMouseEventTypeMousePressed,
		Button:     button,
		Buttons:    new(buttons),
		ClickCount: clickCount,
		Modifiers:  m.page.Keyboard.getModifiers(),
		X:          m.pos.X,
		Y:          m.pos.Y,
	}.Call(m.page)
	if err != nil {
		return err
	}
	m.buttons = toButtons
	return nil
}

// Up releases the button.
func (m *Mouse) Up(button proto.InputMouseButton, clickCount int) error {
	m.Lock()
	defer m.Unlock()

	toButtons := []proto.InputMouseButton{}
	for _, btn := range m.buttons {
		if btn == button {
			continue
		}
		toButtons = append(toButtons, btn)
	}

	_, buttons := input.EncodeMouseButton(toButtons)

	err := proto.InputDispatchMouseEvent{
		Type:       proto.InputDispatchMouseEventTypeMouseReleased,
		Button:     button,
		Buttons:    new(buttons),
		ClickCount: clickCount,
		Modifiers:  m.page.Keyboard.getModifiers(),
		X:          m.pos.X,
		Y:          m.pos.Y,
	}.Call(m.page)
	if err != nil {
		return err
	}
	m.buttons = toButtons
	return nil
}

// Click the button. It's the combination of [Mouse.Down] and [Mouse.Up].
func (m *Mouse) Click(button proto.InputMouseButton, clickCount int) error {
	m.page.browser.trySlowMotion()

	err := m.Down(button, clickCount)
	if err != nil {
		return err
	}

	return m.Up(button, clickCount)
}

// Touch presents a touch device, such as a hand with fingers, each finger is a [proto.InputTouchPoint].
// Touch events is stateless, we use the struct here only as a namespace to make the API style unified.
type Touch struct {
	page *Page
}

func (p *Page) newTouch() *Page {
	p.Touch = &Touch{page: p}
	return p
}

// Start a touch action.
func (t *Touch) Start(points ...*proto.InputTouchPoint) error {
	// TODO: https://crbug.com/613219
	if err := t.page.WaitRepaint(); err != nil {
		return err
	}
	if err := t.page.WaitRepaint(); err != nil {
		return err
	}

	return proto.InputDispatchTouchEvent{
		Type:        proto.InputDispatchTouchEventTypeTouchStart,
		TouchPoints: points,
		Modifiers:   t.page.Keyboard.getModifiers(),
	}.Call(t.page)
}

// Move touch points. Use the [proto.InputTouchPoint.ID] (Touch.identifier) to track points.
// Doc: https://developer.mozilla.org/en-US/docs/Web/API/Touch_events
func (t *Touch) Move(points ...*proto.InputTouchPoint) error {
	return proto.InputDispatchTouchEvent{
		Type:        proto.InputDispatchTouchEventTypeTouchMove,
		TouchPoints: points,
		Modifiers:   t.page.Keyboard.getModifiers(),
	}.Call(t.page)
}

// End touch action.
func (t *Touch) End() error {
	return proto.InputDispatchTouchEvent{
		Type:        proto.InputDispatchTouchEventTypeTouchEnd,
		TouchPoints: []*proto.InputTouchPoint{},
		Modifiers:   t.page.Keyboard.getModifiers(),
	}.Call(t.page)
}

// Cancel touch action.
func (t *Touch) Cancel() error {
	return proto.InputDispatchTouchEvent{
		Type:        proto.InputDispatchTouchEventTypeTouchCancel,
		TouchPoints: []*proto.InputTouchPoint{},
		Modifiers:   t.page.Keyboard.getModifiers(),
	}.Call(t.page)
}

// Tap dispatches a touchstart and touchend event.
func (t *Touch) Tap(x, y float64) error {
	defer t.page.tryTrace(TraceTypeInput, "touch")()
	t.page.browser.trySlowMotion()

	p := &proto.InputTouchPoint{X: x, Y: y}

	err := t.Start(p)
	if err != nil {
		return err
	}

	return t.End()
}

// Each view routes requests through its own context while retaining the physical
// device state shared by every view of this page.
func (p *Page) rebindInput() {
	if p.Keyboard != nil {
		p.Keyboard = &Keyboard{page: p, keyboardState: p.Keyboard.keyboardState}
	}
	if p.Mouse != nil {
		p.Mouse = &Mouse{page: p, mouseState: p.Mouse.mouseState}
	}
	if p.Touch != nil {
		p.Touch = &Touch{page: p}
	}
}
