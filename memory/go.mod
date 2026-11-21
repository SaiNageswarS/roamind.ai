module roamind.ai/memory

go 1.26.7

require (
	github.com/jackc/pgx/v5 v5.11.0
	github.com/questdb/go-questdb-client/v4 v4.3.1
	gopkg.in/yaml.v3 v3.0.1
)

require golang.org/x/tools v0.50.0 // indirect

require (
	github.com/SaiNageswarS/go-api-boot v1.0.45
	github.com/SaiNageswarS/go-collection-boot v1.0.8 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jinzhu/copier v0.3.2 // indirect
	github.com/kr/text v0.2.0 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

require (
	github.com/coder/websocket v1.8.14 // indirect
	github.com/klauspost/compress v1.18.4 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	roamind.ai/connectors v0.0.0
)

replace roamind.ai/connectors => ../connectors
