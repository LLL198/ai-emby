module github.com/LLL198/ai-emby/companion

go 1.26.0

require (
	github.com/LLL198/ai-emby/security v0.0.0
	github.com/fsnotify/fsnotify v1.10.1
	github.com/lib/pq v1.10.9
	github.com/mozillazg/go-pinyin v0.21.0
	golang.org/x/crypto v0.48.0
	golang.org/x/image v0.34.0
	golang.org/x/net v0.50.0
	golang.org/x/sys v0.41.0
	golift.io/udf v0.1.0
)

replace github.com/LLL198/ai-emby/security => ../security

require golang.org/x/text v0.42.0 // indirect
