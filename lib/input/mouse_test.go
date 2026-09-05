package input_test

import (
	"testing"

	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/input"
	"github.com/rah-0/rod/lib/proto"
)

func TestMouseEncode(t *testing.T) {
	g := testutil.T(t)

	b, flag := input.EncodeMouseButton([]proto.InputMouseButton{proto.InputMouseButtonLeft})

	g.Eq(b, proto.InputMouseButtonLeft)
	g.Eq(flag, 1)
}
