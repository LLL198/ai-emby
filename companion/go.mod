module github.com/LLL198/ai-emby/companion

go 1.26

require (
	github.com/LLL198/ai-emby/security v0.0.0
	github.com/lib/pq v1.10.9
	golang.org/x/crypto v0.48.0
	golang.org/x/image v0.34.0
	golang.org/x/net v0.50.0
)

replace github.com/LLL198/ai-emby/security => ../security

require (
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/mozillazg/go-pinyin v0.21.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
)
