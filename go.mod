module xionia-xtp

go 1.24.4

replace web5-mesh => ../Web5-Mesh

require (
	github.com/flynn/noise v1.1.0
	github.com/gorilla/websocket v1.5.3
	github.com/mr-tron/base58 v1.3.0
	golang.org/x/crypto v0.33.0
	web5-mesh v0.0.0-00010101000000-000000000000
)

require (
	github.com/kr/text v0.2.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
)
