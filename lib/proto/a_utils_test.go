package proto_test

import (
	"testing"

	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/proto"
)

func TestPatternToReg(t *testing.T) {
	g := testutil.New(t)
	g.Eq(``, proto.PatternToReg(""))
	g.Eq(`\A.*\z`, proto.PatternToReg("*"))
	g.Eq(`\A.\z`, proto.PatternToReg("?"))
	g.Eq(`\Aa\z`, proto.PatternToReg("a"))
	g.Eq(`\Aa.com/.*/test\z`, proto.PatternToReg("a.com/*/test"))
	g.Eq(`\A\?\*\z`, proto.PatternToReg(`\?\*`))
	g.Eq(`\Aa.com\?a=10&b=\*\z`, proto.PatternToReg(`a.com\?a=10&b=\*`))
}
