module github.com/drakkar-media/drakkar

go 1.26.6

require (
	github.com/bodgit/sevenzip v1.5.1
	github.com/go-chi/chi/v5 v5.2.3
	github.com/jackc/pgx/v5 v5.9.2
	github.com/klauspost/compress v1.17.7
	github.com/mnightingale/rapidyenc v0.0.0-20260606125752-cdd7bcd89529
	github.com/redis/go-redis/v9 v9.16.0
	github.com/rs/zerolog v1.34.0
	github.com/vishvananda/netlink v1.3.1
	go.codycody31.dev/gobullmq v1.0.3
	golang.org/x/crypto v0.51.0
	golang.org/x/net v0.55.0
	golang.org/x/sys v0.45.0
	golang.zx2c4.com/wireguard v0.0.0-20260522210424-ecfc5a8d5446
	gopkg.in/vansante/go-ffprobe.v2 v2.3.0
)

require (
	github.com/andybalholm/brotli v1.1.0 // indirect
	github.com/bodgit/plumbing v1.3.0 // indirect
	github.com/bodgit/windows v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	github.com/pierrec/lz4/v4 v4.1.21 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/ulikunitz/xz v0.5.15 // indirect
	github.com/vishvananda/netns v0.0.5 // indirect
	github.com/vmihailenco/msgpack/v5 v5.4.1 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	go4.org v0.0.0-20200411211856-f5505b9728dd // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/text v0.39.0 // indirect
	golang.org/x/time v0.7.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c // indirect
)

replace go.codycody31.dev/gobullmq => ./third_party/gobullmq
