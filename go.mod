module agora

go 1.26.5

require (
	github.com/jackc/pgx/v5 v5.10.0
	golang.org/x/crypto v0.54.0
)

require (
	github.com/klauspost/compress v1.18.6 // indirect
	github.com/pierrec/lz4/v4 v4.1.26 // indirect
	github.com/twmb/franz-go v1.21.5 // indirect
	github.com/twmb/franz-go/pkg/kmsg v1.13.1 // indirect
)

require (
	github.com/NichoHo/outboxkit v0.1.0
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
)

replace github.com/NichoHo/outboxkit => ./outboxkit
