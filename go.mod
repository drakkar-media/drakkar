module github.com/hjongedijk/drakkar

go 1.26

require (
	github.com/bodgit/sevenzip v1.5.1
	github.com/go-chi/chi/v5 v5.2.3
	github.com/hanwen/go-fuse/v2 v2.8.0
	github.com/jackc/pgx/v5 v5.7.6
	github.com/mnightingale/rapidyenc v0.0.0-20260606125752-cdd7bcd89529
	github.com/redis/go-redis/v9 v9.16.0
	github.com/rs/zerolog v1.34.0
	go.codycody31.dev/gobullmq v1.0.3
	golang.org/x/crypto v0.51.0
	golang.org/x/net v0.55.0
)

require (
	github.com/andybalholm/brotli v1.1.0 // indirect
	github.com/bodgit/plumbing v1.3.0 // indirect
	github.com/bodgit/windows v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/errwrap v1.0.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.17.7 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	github.com/pierrec/lz4/v4 v4.1.21 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/ulikunitz/xz v0.5.12 // indirect
	github.com/vmihailenco/msgpack/v5 v5.4.1 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	go4.org v0.0.0-20200411211856-f5505b9728dd // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.37.0 // indirect
)

replace go.codycody31.dev/gobullmq => ./third_party/gobullmq
