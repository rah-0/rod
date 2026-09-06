package proto_test

import (
	"regexp"
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
	g.Eq(`\Aa\.com/.*/test\z`, proto.PatternToReg("a.com/*/test"))
	g.Eq(`\A\?\*\z`, proto.PatternToReg(`\?\*`))
	g.Eq(`\Aa\.com\?a=10&b=\*\z`, proto.PatternToReg(`a.com\?a=10&b=\*`))
}

func TestPatternToRegMatching(t *testing.T) {
	for _, test := range []struct {
		pattern string
		match   []string
		miss    []string
	}{
		{pattern: "", match: []string{"", "https://example.test/"}},
		{pattern: "**.example.test/**", match: []string{"https://a.example.test/path", ".example.test/"}, miss: []string{"https://aXexample.test/path"}},
		{pattern: "https://example.test/[a]+(b){c}^$|", match: []string{"https://example.test/[a]+(b){c}^$|"}, miss: []string{"https://example.test/abccc"}},
		{pattern: `https://example.test/\?x=\*`, match: []string{"https://example.test/?x=*"}, miss: []string{"https://example.test/ax=anything"}},
		{pattern: `a??b`, match: []string{"axyb", "a界🙂b"}, miss: []string{"ab", "axb", "axyzb"}},
		{pattern: `a\*?**b`, match: []string{"a*xb", "a*xyzb"}, miss: []string{"a*b"}},
		{pattern: `a\\b`, match: []string{`a\b`}, miss: []string{"ab"}},
		{pattern: " a ", match: []string{" a "}, miss: []string{"a"}},
	} {
		t.Run(test.pattern, func(t *testing.T) {
			matcher, err := regexp.Compile(proto.PatternToReg(test.pattern))
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range test.match {
				if !matcher.MatchString(value) {
					t.Errorf("did not match %q", value)
				}
			}
			for _, value := range test.miss {
				if matcher.MatchString(value) {
					t.Errorf("unexpectedly matched %q", value)
				}
			}
		})
	}
}
