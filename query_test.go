package rod_test

import (
	"testing"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/internal/testutil"
)

func TestPagesOthers(t *testing.T) {
	g := testutil.New(t)

	list := rod.Pages{}
	g.Nil(list.First())
	g.Nil(list.Last())

	list = append(list, &rod.Page{})

	g.NotNil(list.First())
	g.NotNil(list.Last())
}

func TestElementsOthers(t *testing.T) {
	g := testutil.New(t)

	list := rod.Elements{}
	g.Nil(list.First())
	g.Nil(list.Last())
}
