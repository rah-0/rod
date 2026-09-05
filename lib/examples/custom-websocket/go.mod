module github.com/rah-0/rod/lib/examples/custom-websocket

go 1.27.1

require (
	github.com/gobwas/ws v1.1.0
	github.com/rah-0/rod v0.0.0
)

require (
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/rah-0/rod => ../../..
