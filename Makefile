clean:
	rm DB kernel maple.log
compile:
	go build kernel.go db.go log.go parse.go
