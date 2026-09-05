package assets

import (
	"os/exec"
	"strings"
	"testing"
)

func TestMonitorPollingRecovers(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is required for monitor behavior tests")
	}
	_, script, ok := strings.Cut(Monitor, "<script>")
	if !ok {
		t.Fatal("monitor script missing")
	}
	script, _, ok = strings.Cut(script, "</script>")
	if !ok {
		t.Fatal("monitor script end missing")
	}
	fixture := `
const assert = require('node:assert/strict')
const timers = []
const failures = []
let step = 0
let rendered = []
global.setTimeout = (fn, delay) => { assert.equal(delay, 1000); timers.push(fn) }
global.console.error = (...args) => failures.push(args)
global.window = { location: { origin: 'http://fixture.test' } }
global.document = {
 getElementById() { return { replaceChildren(fragment) {
  if (step === 4) throw new Error('render failed')
  rendered = fragment.children
 } } },
 createDocumentFragment() { return { children: [], appendChild(child) { this.children.push(child) } } },
 createElement() { return {} }
}
global.fetch = async () => {
 step++
 if (step === 1) throw new Error('request failed')
 return { ok: step !== 2, status: step === 2 ? 500 : 200, json: async () => {
  if (step === 3) throw new Error('invalid JSON')
  return [{ targetId: 'a/b', title: '<b>safe text</b>', url: 'https://example.test/' }]
 } }
}
`
	assertions := `
;(async () => {
 await new Promise(setImmediate)
 for (let index = 1; index < 5; index++) {
  assert.equal(timers.length, 1, 'one next poll after each failure')
  await timers.shift()()
 }
 assert.equal(step, 5)
 assert.equal(failures.length, 4)
 assert.equal(timers.length, 1)
 assert.equal(rendered.length, 1)
 assert.equal(rendered[0].textContent, '<b>safe text</b>')
 assert.equal(rendered[0].href, 'http://fixture.test/page/a%2Fb')
})().catch(error => { process.stderr.write(String(error)); process.exitCode = 1 })
`
	cmd := exec.CommandContext(t.Context(), "node", "-")
	cmd.Stdin = strings.NewReader(fixture + script + assertions)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("monitor polling: %v\n%s", err, output)
	}
}

func TestMonitorRendersTargetDataAsText(t *testing.T) {
	for _, forbidden := range []string{
		"innerHTML",
		"${target.targetId}",
		"${target.title}",
		"${target.url}",
	} {
		if strings.Contains(Monitor, forbidden) {
			t.Errorf("monitor contains unsafe target interpolation %q", forbidden)
		}
	}

	for _, required := range []string{
		"document.createElement('a')",
		"link.textContent = target.title",
		"link.title = target.url",
		"encodeURIComponent(target.targetId)",
		"url.origin !== window.location.origin",
		"targets.replaceChildren(links)",
	} {
		if !strings.Contains(Monitor, required) {
			t.Errorf("monitor is missing safe target rendering %q", required)
		}
	}
}
