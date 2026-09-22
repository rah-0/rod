package rod_test

import (
	"testing"

	"github.com/rah-0/rod"
)

func TestWorkerEcho(t *testing.T) {
	g := setup(t)
	server := g.Serve()
	server.Route("/", repoPath("fixtures/worker.html"))
	server.Route("/worker.js", repoPath("fixtures/worker.js"))

	page := g.newPage(server.URL()).MustWaitLoad()
	g.E(page.Wait(rod.Eval(`() => document.querySelector('#result').textContent !== 'Waiting for worker'`)))
	g.Eq("worker echo: ready", page.MustElement("#result").MustText())
}
