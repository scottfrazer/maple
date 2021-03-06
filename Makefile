LD_FLAGS="-X main.Version=dev -X main.GitHash=`git rev-parse HEAD` -X main.BuildDate=`date -u '+%Y-%m-%d_%H:%M:%S'`"

clean:
	-rm -rf maple maple.sqlite3.db maple.log $(GOPATH)/bin/maple $(GOPATH)/pkg/darwin_amd64/github.com/scottfrazer/
deps:
	go get github.com/mattn/go-sqlite3
	go get github.com/satori/go.uuid
	go get golang.org/x/net/context
parser:
	hermes generate grammar.hgr --language=go --go-package=maple --go-imports=strconv --name=wdl
	go fmt wdl_parser.go
test:
	go test -v github.com/scottfrazer/maple
install:
	go install -ldflags $(LD_FLAGS) github.com/scottfrazer/maple
	go install -ldflags $(LD_FLAGS) github.com/scottfrazer/maple/cmd/maple
