package rod_test

import (
	"testing"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/proto"
)

func TestEvalOptionsString(t *testing.T) {
	g := testutil.New(t)
	object := &proto.RuntimeRemoteObject{Description: "button"}
	g.Eq(rod.Eval(`() => this.parentElement`).This(object).String(), "() => this.parentElement() button")
}
