FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/handoffd ./cmd/handoffd

FROM alpine:3.22
RUN addgroup -S handoff && adduser -S -G handoff handoff
USER handoff
WORKDIR /app
COPY --from=build /out/handoffd /usr/local/bin/handoffd
VOLUME ["/data"]
EXPOSE 7391
ENV HANDOFF_LISTEN_ADDR=:7391 HANDOFF_DATA_DIR=/data
ENTRYPOINT ["handoffd"]
