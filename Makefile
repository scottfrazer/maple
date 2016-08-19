LD_FLAGS="-X main.Version=dev -X main.GitHash=`git rev-parse HEAD`"

clean:
	-rm -rf maple DB maple.log $(GOPATH)/bin/maple $(GOPATH)/pkg/darwin_amd64/github.com/scottfrazer/
install:
	go install -ldflags $(LD_FLAGS) github.com/scottfrazer/maple
	go install -ldflags $(LD_FLAGS) github.com/scottfrazer/maple/cmd/maple
test:
	go test -v github.com/scottfrazer/maple
