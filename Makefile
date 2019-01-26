everything: ts-player
clean:
	rm -f ts-player its.pb.go

its.pb.go: its.proto
	go get github.com/golang/protobuf/protoc-gen-go
	PATH=$(PATH):$(GOPATH)/bin protoc --go_out=. its.proto

ts-player: cmd.go its.pb.go play.go encode.go record.go optimize.go
	go build
