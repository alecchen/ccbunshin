# ccbunshin proxy

This Go service exposes two loopback Anthropic-compatible HTTP listeners and forwards each profile to its configured upstream gateway.

## Configuration

Both upstream URLs are required:

```sh
export CCBUNSHIN_PROVIDER1_UPSTREAM=https://provider1.example.invalid
export CCBUNSHIN_PROVIDER2_UPSTREAM=https://provider2.example.invalid
```

Optional variables:

```text
CCBUNSHIN_PROVIDER1_LISTEN     default 127.0.0.1:3456
CCBUNSHIN_PROVIDER2_LISTEN     default 127.0.0.1:3457
CCBUNSHIN_PROVIDER1_API_KEY
CCBUNSHIN_PROVIDER2_API_KEY
CCBUNSHIN_PROVIDER1_MODEL_MAP  comma-separated source=target pairs
CCBUNSHIN_PROVIDER2_MODEL_MAP  comma-separated source=target pairs
CCBUNSHIN_PROVIDER1_TIMEOUT    Go duration, default 60s
CCBUNSHIN_PROVIDER2_TIMEOUT    Go duration, default 60s
```

Run both listeners:

```sh
go run .
```

Build and test:

```sh
gofmt -w .
go vet ./...
go test ./...
go build -o ccbunshin-proxy .
```

Each listener has a local health endpoint at `/healthz`. Requests are forwarded to the matching upstream; the proxy does not route between listeners.
