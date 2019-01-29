everything: ts-player doc/ts-player.1
clean:
	rm -f ts-player its.pb.go
	cd doc && make clean

its.pb.go: its.proto
	go get github.com/golang/protobuf/protoc-gen-go
	PATH=$(PATH):$(GOPATH)/bin protoc --go_out=. its.proto

ts-player: cmd.go its.pb.go play.go encode.go record.go optimize.go
	go build

doc/ts-player.1:
	cd doc && make
