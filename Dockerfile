FROM golang:1.27.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG TARGET=flagguard
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/service ./cmd/${TARGET}

FROM alpine:3.23
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates && adduser -D -u 10001 app && mkdir /data && chown app:app /data
WORKDIR /app
COPY --from=build /out/service /app/service
COPY guardrails.yaml /app/guardrails.yaml
USER app
ENTRYPOINT ["/app/service"]
