# WebTransport Example

## Certificate

The server deterministically generates a P-256 certificate. The certificate hash is stable for the current UTC week and rotates weekly to satisfy the W3C [`serverCertificateHashes` certificate-hash requirements](https://www.w3.org/TR/webtransport/#verify-a-certificate-hash), which limit certificate validity to less than two weeks. This is a browser API requirement, not a WebTransport protocol requirement.

## Running the Server

From the `server` directory:

```bash
go run .
```

This prints the base64-encoded certificate hash, serves the browser client at `http://localhost:6120`, and starts the WebTransport server at `https://localhost:6121/webtransport`.

To use different listen addresses:

By default, the server offers `webtransport-test,webtransport-test-2`. To change that:

```bash
go run . -protocols webtransport-test,webtransport-test-2
```

## Running the Go Client

Start the server first. From the `client` directory:

```bash
go run . -url https://localhost:6121/webtransport -protocols webtransport-test
```

## Running the Browser Client

Start the server first, then open `http://localhost:6120` in a browser. The server pre-fills the certificate hash, so click `Connect` to start the WebTransport session and see the status update.
