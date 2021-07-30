.PHONY: install linux
all:
	go build .

linux:
	GOARCH=amd64 GOOS=linux go build .

install: linux
	# scp traefiker blinker:blinker/
	scp -C traefiker apio:live/
	scp -C traefiker apio.staging:staging/
