# syntax=docker/dockerfile:1
# Agent image: a single Go binary + a data volume. No rclone, no extra
# engine binary — the binary is the whole Agent.
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/zy-drive-agent ./cmd/agent

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/zy-drive-agent /usr/local/bin/zy-drive-agent
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/zy-drive-agent", "--addr", "0.0.0.0:8080", "--config-dir", "/data"]
