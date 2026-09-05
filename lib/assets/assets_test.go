package assets

import (
	"strings"
	"testing"
)

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
