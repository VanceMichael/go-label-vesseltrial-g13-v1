FROM --platform=$BUILDPLATFORM golang:1.26.1-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOTOOLCHAIN=local go build -trimpath -ldflags="-s -w" -o /out/vesseltrial ./cmd/server

FROM alpine:3.23
RUN apk add --no-cache ca-certificates curl && addgroup -S app && adduser -S -G app app
WORKDIR /app
COPY --from=build /out/vesseltrial /app/vesseltrial
RUN mkdir -p /data && chown -R app:app /data
USER app
ENV VESSELTRIAL_ADDR=:8080 VESSELTRIAL_DB=/data/vesseltrial.db
EXPOSE 8080
ENTRYPOINT ["/app/vesseltrial"]
