clean:
	-rm -rf DB maple.log $(GOPATH)/bin/maple $(GOPATH)/pkg/darwin_amd64/github.com/scottfrazer/
compile:
	#go build -ldflags "-X main.Version=dev -X main.GitHash=`git rev-parse HEAD`" github.com/scottfrazer/maple
	go install -ldflags "-X main.Version=dev -X main.GitHash=`git rev-parse HEAD`" github.com/scottfrazer/maple
	#go build -ldflags "-X main.Version=dev -X main.GitHash=`git rev-parse HEAD`" github.com/scottfrazer/maple/cmd/maple
	go install -ldflags "-X main.Version=dev -X main.GitHash=`git rev-parse HEAD`" github.com/scottfrazer/maple/cmd/maple
	ls $(GOPATH)/bin/
	ls $(GOPATH)/pkg/darwin_amd64/github.com/scottfrazer/
