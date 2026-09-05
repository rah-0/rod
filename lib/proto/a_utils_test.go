package proto_test

import (
	"testing"

	"github.com/rah-0/rod/internal/testutil"
	"github.com/rah-0/rod/lib/proto"
)

type T struct {
	testutil.G
}

func Test(t *testing.T) {
	testutil.Each(t, T{})
}

func (t T) PatternToReg() {
	t.Eq(``, proto.PatternToReg(""))
	t.Eq(`\A.*\z`, proto.PatternToReg("*"))
	t.Eq(`\A.\z`, proto.PatternToReg("?"))
	t.Eq(`\Aa\z`, proto.PatternToReg("a"))
	t.Eq(`\Aa.com/.*/test\z`, proto.PatternToReg("a.com/*/test"))
	t.Eq(`\A\?\*\z`, proto.PatternToReg(`\?\*`))
	t.Eq(`\Aa.com\?a=10&b=\*\z`, proto.PatternToReg(`a.com\?a=10&b=\*`))
}
