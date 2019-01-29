everything: ts-player doc/ts-player.1
clean:
	rm -f ts-player its.pb.go
	cd doc && rm -f ts-player.1

its.pb.go: its.proto
	go get github.com/golang/protobuf/protoc-gen-go
	PATH=$(PATH):$(GOPATH)/bin protoc --go_out=. its.proto

ts-player: cmd.go its.pb.go play.go encode.go record.go optimize.go color-profile.go
	go build

doc/ts-player.1: doc/ts-player.1.txt
	cd doc && a2x --doctype manpage --format manpage ts-player.1.txt
