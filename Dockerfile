FROM golang:1.22.3-alpine3.19 AS build
WORKDIR /go/src/proglog
COPY . .
# The flag -ldflags="-s -w" delete debug information of binary reducing its size.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /go/bin/proglog ./cmd/proglog

# Set environment variable
ENV GRPC_HEALTH_PROBE_VERSION=v0.3.2

# Create directory for the binary and download it
RUN wget -qO /go/bin/grpc_health_probe \
    https://github.com/grpc-ecosystem/grpc-health-probe/releases/download/${GRPC_HEALTH_PROBE_VERSION}/grpc_health_probe-linux-amd64 && \
    chmod +x /go/bin/grpc_health_probe

FROM alpine
COPY --from=build /go/bin/proglog /bin/proglog
COPY --from=build /go/bin/grpc_health_probe /bin/grpc_health_probe

ENTRYPOINT ["/bin/proglog"]
