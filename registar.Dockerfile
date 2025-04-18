# adding deps

FROM golang:1.23.5-alpine3.20 AS deps
WORKDIR /go/src/app

COPY go.mod go.sum ./
RUN apk --no-cache add make=4.4.1-r2 && \
    go mod download

# building

FROM deps AS builder
ARG BUILD_DIR=/go/src/build

COPY . .

RUN GOOS=linux GOARCH=amd64 go build -v -o $BUILD_DIR/registar ./cmd/registar/...

# certs

FROM golang:1.23.1-alpine3.20 AS certs
RUN apk add --no-cache ca-certificates=20240705-r0

# copy to scratch

FROM scratch
ARG BUILD_DIR=/go/src/build
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder $BUILD_DIR/registar /registar
ENTRYPOINT [ "/registar" ]
