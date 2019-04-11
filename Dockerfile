# Build the manager binary
FROM golang:1.10.3 as builder

# Copy in the go src
WORKDIR /go/src/github.com/takaishi/openstack-sg-controller
COPY pkg/    pkg/
COPY cmd/    cmd/
COPY vendor/ vendor/

# Build
ENV GO111MODULE on

RUN go get github.com/takaishi/openstack-sg-controller/cmd/manager
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o manager github.com/takaishi/openstack-sg-controller/cmd/manager

# Copy the controller-manager into a thin image
FROM alpine:3.8 as app
RUN apk --no-cache add ca-certificates
WORKDIR /
COPY --from=builder /go/src/github.com/takaishi/openstack-sg-controller/manager .
ENTRYPOINT ["/manager"]
