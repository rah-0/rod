package proto_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/proto"
	"github.com/rah-0/rod/lib/utils"
)

type Client struct {
	sessionID  string
	methodName string
	params     any
	err        error
	ret        any
}

var (
	_ proto.Client      = &Client{}
	_ proto.Sessionable = &Client{}
	_ proto.Contextable = &Client{}
)

func (c *Client) Call(_ context.Context, sessionID, methodName string, params any) (res []byte, err error) {
	c.sessionID = sessionID
	c.methodName = methodName
	c.params = params
	return utils.MustToJSONBytes(c.ret), c.err
}

func (c *Client) GetSessionID() proto.TargetSessionID { return "" }

func (c *Client) GetContext() context.Context { return nil }

func TestCallErr(t *testing.T) {
	g := testutil.New(t)
	client := &Client{err: errors.New("err")}
	g.Eq(proto.PageEnable{}.Call(client).Error(), "err")
}

func TestParseMethodName(t *testing.T) {
	g := testutil.New(t)
	d, n := proto.ParseMethodName("Page.enable")
	g.Eq("Page", d)
	g.Eq("enable", n)
}

func TestGetType(t *testing.T) {
	g := testutil.New(t)
	method := proto.GetType("Page.enable")
	g.Eq(reflect.TypeFor[proto.PageEnable](), method)
}

func TestTimeCodec(t *testing.T) {
	g := testutil.New(t)
	raw := []byte("123.123")
	var duration proto.MonotonicTime
	g.E(json.Unmarshal(raw, &duration))

	g.Eq(123123, duration.Duration().Milliseconds())
	g.Eq("2m3.123s", duration.String())

	data, err := json.Marshal(duration)
	g.E(err)
	g.Eq(raw, data)

	raw = []byte("1234567890")
	var datetime proto.TimeSinceEpoch
	g.E(json.Unmarshal(raw, &datetime))

	g.Eq(1234567890, datetime.Time().Unix())
	g.Has(datetime.String(), "2009-02")

	data, err = json.Marshal(datetime)
	g.E(err)
	g.Eq(raw, data)

	var sessionExpires proto.TimeSinceEpoch = -1
	var zeroTime time.Time
	g.Eq(sessionExpires.Time(), zeroTime)
}

func TestRect(t *testing.T) {
	g := testutil.New(t)
	rect := proto.DOMQuad{
		336, 382, 361, 382, 361, 421, 336, 412,
	}

	g.Eq(348.5, rect.Center().X)
	g.Eq(399.25, rect.Center().Y)

	res := &proto.DOMGetContentQuadsResult{}
	g.Nil(res.OnePointInside())

	res = &proto.DOMGetContentQuadsResult{Quads: []proto.DOMQuad{{1, 1, 2, 1, 2, 1, 1, 1}}}
	g.Nil(res.OnePointInside())

	res = &proto.DOMGetContentQuadsResult{Quads: []proto.DOMQuad{rect}}
	pt := res.OnePointInside()
	g.Eq(348.5, pt.X)
	g.Eq(399.25, pt.Y)
}

func TestArea(t *testing.T) {
	g := testutil.New(t)
	g.Eq(proto.DOMQuad{1, 1, 2, 1, 2, 1, 1, 1}.Area(), 0)
	g.Eq(proto.DOMQuad{1, 1, 2, 1, 2, 2, 1, 2}.Area(), 1)
	g.Eq(proto.DOMQuad{1, 1, 2, 1, 2, 4, 1, 3}.Area(), 2.5)
}

func TestBox(t *testing.T) {
	g := testutil.New(t)
	res := &proto.DOMGetContentQuadsResult{Quads: []proto.DOMQuad{
		{1, 1, 2, 1, 2, 2, 1, 2},
		{2, 0, 3, 0, 3, 1, 2, 1},
		{0, 2, 1, 2, 1, 3, 0, 3},
	}}
	g.Eq(res.Box(), &proto.DOMRect{
		X:      0,
		Y:      0,
		Width:  3,
		Height: 3,
	})

	g.Nil((&proto.DOMGetContentQuadsResult{}).Box())
}

func TestInputTouchPointMoveTo(t *testing.T) {
	g := testutil.New(t)
	p := &proto.InputTouchPoint{}
	p.MoveTo(1, 2)

	g.Eq(1, p.X)
	g.Eq(2, p.Y)
}

func TestCookiesToParams(t *testing.T) {
	g := testutil.New(t)
	list := proto.CookiesToParams([]*proto.NetworkCookie{{
		Name:  "name",
		Value: "val",
	}})

	g.Eq(list[0].Name, "name")
	g.Eq(list[0].Value, "val")
}

func TestGeneratorOptimize(t *testing.T) {
	var _ proto.TargetTargetInfoType = proto.TargetTargetInfoTypeBackgroundPage
	var _ proto.TargetTargetInfoType = proto.TargetTargetInfoTypePage

	var _ proto.PageLifecycleEventName = proto.PageLifecycleEventNameInit
	var _ proto.PageLifecycleEventName = proto.PageLifecycleEventNameFirstContentfulPaint
	var _ proto.PageLifecycleEventName = proto.PageLifecycleEventNameFirstImagePaint

	a := proto.InputDispatchKeyEvent{}
	var _ proto.TimeSinceEpoch = a.Timestamp
	b := proto.NetworkCookie{}
	var _ proto.TimeSinceEpoch = b.Expires

	c := proto.NetworkDataReceived{}
	var _ proto.MonotonicTime = c.Timestamp

	d := proto.NetworkCookie{}
	var _ proto.TimeSinceEpoch = d.Expires
}
