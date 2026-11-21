module github.com/rah-0/rod/examples/custom-websocket

go 1.27.4

require (
	github.com/gobwas/ws v1.5.0
	github.com/rah-0/rod v0.128.0
)

require github.com/gobwas/httphead v0.1.0 // indirect

replace github.com/rah-0/rod => ../..
