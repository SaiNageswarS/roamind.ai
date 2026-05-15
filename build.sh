rm -Rf build

cd proto

rm -Rf ./generated
mkdir -p ./generated

protoc --go_out=./generated --go_opt=paths=source_relative \
    --go-grpc_out=./generated --go-grpc_opt=paths=source_relative \
    *.proto

cd ..

cd gateway

go build \
    -ldflags="-s -w -X main.Version=$(git describe --tags --always 2>/dev/null || echo 'dev')" \
    -trimpath \
    -o ../build/roamind-gateway .

cd ..

cd cli

go build \
    -ldflags="-s -w" \
    -trimpath \
    -o ../build/roamind-cli .

cd ..