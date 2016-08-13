clean:
	-rm DB maple maple.log
compile:
	go build -ldflags "-X main.Version=dev -X main.GitHash=`git rev-parse HEAD`"
