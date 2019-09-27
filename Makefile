.PHONY: install linux
all:
	go build .

linux:
	GOOS=linux go build . && goupx traefiker

install: linux
	scp traefiker blinker:blinker/
