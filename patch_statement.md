# PATCH STATEMENT: Wildcard SNI Matching Setup, Splithttp Fixes & Certificate Caching

This document details the modifications applied to the custom `Xray-core` codebase repository (`github.com/zhfreal/Xray-core`). These changes integrate wildcard SNI pattern compiling, fix splithttp resource leaks and linter warnings, add global certificate caching, and use a remote GitHub fork for the `reality` package dependency.

---

## Repository Details
* **Base Upstream Version**: Tag `v26.7.28`
* **Fork Repository**: `github.com/zhfreal/Xray-core`
* **Development Branch**: `xray-wildcard-patches`
* **Latest Local Patch Commit**: `53c3c975`

---

## Detailed Changes

### 1. Wildcard Pattern Compilation Trigger (`transport/internet/reality/config.go`)
* Added a call to `config.CompileServerNamePatterns()` inside `GetREALITYConfig()` right before returning the compiled REALITY config struct. This compiles wildcard patterns (like `*.example.com` or `*`) exactly once at startup so that SNI regex evaluation is fast and efficient during connection handshakes.

### 2. Dependency Routing to Remote GitHub Fork (`go.mod`)
* Injected `replace` directives pointing `github.com/xtls/reality` and `github.com/metacubex/sing-mux` to their respective remote GitHub forks to compile our custom branches:
  ```go
  replace github.com/xtls/reality => github.com/zhfreal/REALITY v1.26.5-patch2
  replace github.com/metacubex/sing-mux => github.com/zhfreal/sing-mux v0.3.10-patch4
  ```

### 3. Xmux TCP Connection Leak Fix (`transport/internet/splithttp/client.go`)
* Updated `DefaultDialerClient.Close()` to invoke `tr.CloseIdleConnections()` on the internal HTTP transport. When an `XmuxClient` reaches its `cMaxReuseTimes` limit, it is cleanly retired and closed. This fix ensures that Go immediately drops the idle HTTP/2 connection on the client side rather than indefinitely holding it in the `ESTABLISHED` state until the remote server's timeout drops it.

### 4. Protobuf Copylocks Warning Fix (`transport/internet/splithttp/dialer.go` & `mux.go` & `mux_test.go`)
* Changed `XmuxManager` and related functions to accept `XmuxConfig` by pointer (`*XmuxConfig`) rather than by value (`XmuxConfig`). `XmuxConfig` is a Protobuf-generated struct containing an internal `sync.Mutex`; passing it by value caused `go vet` copylocks warnings and incorrect lock state duplication.
* Updated the `splithttp` testing suite (`mux_test.go`) to pass `&xmuxConfig` pointers to `NewXmuxManager`, fixing compilation errors caused by the pointer refactoring.

### 5. Global Certificate Parsing & OCSP Hot-Reload Deduplication (`transport/internet/tls/config.go`, `cert_cache.go`, `ocsp_ticker.go`)
* **Problem**: In configurations with many TLS inbounds referencing the same TLS certificates, Xray would duplicate certificate parsing in memory. More importantly, it spawned duplicate background hot-reload and OCSP stapling goroutines (`setupOcspTicker`) for every parsed certificate instance. This led to hundreds of leaked goroutines reading the same files from disk on every config reload.
* **Solution**:
  - Implemented `globalCertCache` (`sync.Map`) in `cert_cache.go` to cache `*tls.Certificate` objects keyed by their file paths (when both `CertificatePath` and `KeyPath` are set) or by the SHA-256 hash of their raw certificate+key bytes.
  - Extracted `getX509KeyPair()` into `cert_cache.go` as a shared helper for parsing X.509 key pairs.
  - Refactored `BuildCertificates()` in `config.go` to pull from this cache, preventing duplicate parsing of X.509 chains across multiple inbounds.
  - Extracted and refactored `setupOcspTicker` into `ocsp_ticker.go` using a global `sync.Map` (`globalOcspTickerMap`). This ensures only a single background goroutine and file watcher is launched per unique certificate file pair. The single goroutine broadcasts file changes to all registered inbounds' callbacks, eliminating goroutine leaks and redundant disk I/O.

### 6. sing-mux Multiplexing Support (`common/singmux/`, `app/proxyman/config.proto`, `app/proxyman/outbound/handler.go`, `app/proxyman/inbound/always.go`, `infra/conf/xray.go`)
* **Problem**: Lack of compatibility with `mihomo` (clash.meta) and `sing-box` configurations that utilize `sing-mux` for multiplexing over standard outbound connections.
* **Solution**:
  - Integrated `github.com/metacubex/sing-mux v0.3.10` dependency.
  - Extended `MultiplexingConfig` protobuf definition with `protocol` (supporting `smux`, `yamux`, `h2mux`), padding, max/min stream settings, and TCP Brutal options.
  - Updated JSON parsing in `infra/conf/xray.go` to support these options in the configuration files.
  - Created a bridge adapter package `common/singmux` to translate Xray's `transport.Link` pipes to `net.Conn` and `N.PacketConn` required by the `sing-mux` client and server.
  - **Synchronous Lifecycle Blocking**: Implemented blocking behavior in the client-side `Dispatch` and server-side `NewConnection`/`NewPacketConnection` copy loops to prevent premature stream and client TCP socket closures.
  - Updated `AlwaysOnInboundHandler` on the server side to use a unified `singmux.Server` that intercepts sing-mux target FQDN requests (`sp.mux.sing-box.arpa`) and routes parsed streams back to Xray dispatcher.
  - Updated outbound `Handler` on the client side to initialize `singmux.SingMuxClientManager` and multiplex client requests onto sing-mux streams when configured.

### 7. User Configuration & Environment Guide

To enable `sing-mux` compatibility between Xray and Mihomo, configurations should be updated as detailed below.

#### A. Xray Server Config (`xray_server.json`)
The server's inbound block is standard. The server automatically detects `sing-mux` requests aimed at the virtual domain `sp.mux.sing-box.arpa`:
```json
{
  "inbounds": [
    {
      "port": 23001,
      "listen": "127.0.0.1",
      "protocol": "vless",
      "settings": {
        "clients": [{ "id": "c4af696b-177e-4ad5-9af1-3632df9d31f6" }],
        "decryption": "none"
      },
      "streamSettings": {
        "network": "tcp",
        "security": "reality",
        "realitySettings": {
          "show": true,
          "dest": "127.0.0.1:23456",
          "serverNames": ["localhost"],
          "privateKey": "kDfIzEiK9FJfLzpj8j4ID87L6IvilQswQJDezHeSEks",
          "shortIds": ["0123456789abcdef"]
        }
      }
    }
  ]
}
```

#### B. Mihomo Server Config (`mihomo_server.yaml`)
Mihomo's server configuration enables `sing-mux` packet padding:
```yaml
listeners:
  - name: vless-in
    type: vless
    port: 23001
    listen: 127.0.0.1
    users:
      - uuid: c4af696b-177e-4ad5-9af1-3632df9d31f6
    reality-config:
      dest: 127.0.0.1:23456
      private-key: kDfIzEiK9FJfLzpj8j4ID87L6IvilQswQJDezHeSEks
      short-id: [ "0123456789abcdef" ]
      server-names: [ "localhost" ]
    mux-option:
      padding: true
```

#### C. Xray Client Config (`xray_client.json`)
The client enables `sing-mux` by specifying the `protocol` and setting `enabled: true` in the `mux` block of the outbound settings.

##### JSON Structure:
```json
{
  "outbounds": [
    {
      "protocol": "vless",
      "settings": {
        "vnext": [{
          "address": "127.0.0.1",
          "port": 23001,
          "users": [{ "id": "c4af696b-177e-4ad5-9af1-3632df9d31f6", "encryption": "none" }]
        }]
      },
      "streamSettings": {
        "network": "tcp",
        "security": "reality",
        "realitySettings": {
          "serverName": "localhost",
          "publicKey": "b89nCxXUcNsPrT5pGtEsrZJ5CQxIoA11_wbrart7_UQ",
          "shortId": "0123456789abcdef"
        }
      },
      "mux": {
        "enabled": true,
        "concurrency": 8,         // Max concurrent streams legacy v2ray-mux can handle (or ignored when using sing-mux)
        "xudpConcurrency": 8,     // Max concurrent XUDP streams in another mux
        "xudpProxyUDP443": "reject", // UDP 443 proxy policy: reject, allow, or skip
        "protocol": "smux",       // Supports: "smux", "yamux", "h2mux"
        "maxConnections": 4,      // Max concurrent physical TCP connections for multiplexing
        "minStreams": 2,          // Min streams per connection before dialing a new connection
        "maxStreams": 10,         // Max multiplexed streams per connection (0 for unlimited)
        "padding": true,          // Enable sing-mux protocol padding to obfuscate traffic
        "brutal": false,          // Enable TCP Brutal congestion control protocol
        "brutalUp": "10 Mbps",    // Upload bandwidth limit for TCP Brutal
        "brutalDown": "20 Mbps"   // Download bandwidth limit for TCP Brutal
      }
    }
  ]
}
```

##### Field Descriptions:
* **`concurrency`**: Used by legacy `v2ray-mux`. Specifies the maximum concurrent streams per TCP connection. (Ignored when `protocol` is a `sing-mux` protocol).
* **`xudpConcurrency`**: Specifies the concurrency setting for proxying XUDP over Mux connections.
* **`xudpProxyUDP443`**: The policy for proxying UDP traffic targeting port 443 (QUIC/HTTP3). Can be `"reject"`, `"allow"`, or `"skip"`.
* **`protocol`**: Specifies the multiplexing protocol. Must be `"smux"`, `"yamux"`, or `"h2mux"` to activate `sing-mux`. If set to `"mux"` or omitted, legacy `v2ray-mux` is used.
* **`maxConnections`**: The maximum number of concurrent physical TCP connections that `sing-mux` will maintain. If `maxConnections > 0`, it takes precedence, and `maxStreams` is completely ignored.
* **`minStreams`**: Used when `maxConnections > 0`. The minimum number of active streams before `sing-mux` allocates another physical connection.
* **`maxStreams`**: The maximum number of multiplexed logical streams per physical connection. (Only evaluated when `maxConnections <= 0` or omitted; otherwise bypassed).
* **`padding`**: Enables padding at the `sing-mux` protocol layer for anti-censorship.
* **`brutalUp` / `brutalDown`**: Rate limit strings (e.g. `"10 Mbps"`, `"20 Mbps"`, or raw numbers representing Mbps) mapping to upload and download bandwidth limits for TCP Brutal.

##### How to use legacy `v2ray-mux` instead of `sing-mux`:
- To select the original, legacy `v2ray-mux` protocol, configure `"protocol": "mux"` (or omit the `protocol` key entirely) and set `"enabled": true`.
- In legacy mode, `concurrency` specifies the maximum concurrent streams per TCP connection (defaults to `8` if concurrency is set to `0`).
- Setting `concurrency` to `< 0` (e.g., `-1`) completely disables all multiplexing (both legacy and sing-mux).

#### D. Mihomo Client Config (`mihomo_client.yaml`)
Mihomo enables multiplexing via the `smux` block inside the proxy definitions:
```yaml
proxies:
  - name: "vless-reality-singmux"
    type: vless
    server: 127.0.0.1
    port: 23001
    uuid: c4af696b-177e-4ad5-9af1-3632df9d31f6
    tls: true
    servername: localhost
    reality-opts:
      public-key: b89nCxXUcNsPrT5pGtEsrZJ5CQxIoA11_wbrart7_UQ
      short-id: 0123456789abcdef
    client-fingerprint: chrome
    smux:
      enabled: true
      protocol: smux              # Supports: "smux", "yamux", "h2mux"
      max-streams: 10
      min-streams: 2
```

### 8. Adaptive sing-mux Padding & TCP Brutal (`common/singmux/server.go`)
* **Problem**: Previously, `sing-mux` server options for padding and TCP Brutal were statically bound. This caused protocol version mismatches (dropping clients that requested padding) and prevented clients from dynamically negotiating TCP Brutal speeds.
* **Solution**:
  - Set `Padding: false` in `mux.ServiceOptions`. In `sing-mux`, this activates permissive mode, allowing the server to dynamically accept both `Version0` (unpadded) and `Version1` (padded) clients on the fly without conflicts.
  - Enabled `Brutal` globally (`Enabled: true`) with an environment-variable-driven maximum speed cap (`SMUX_BRUTAL_CAP_MBPS`, defaulting to 100 Mbps). This allows well-behaved clients to dictate their speed adaptively up to the cap, while protecting the server against greedy clients requesting unlimited bandwidth.

### 9. sing-mux TCP Brutal Socket Pacing Compatibility
* **Problem**: TCP Brutal in `metacubex/sing-mux` configures TCP socket pacing via the `TCP_CONGESTION` and `TCP_BRUTAL_PARAMS` (23301) syscalls. Because Xray-core runs `sing-mux` deep within a multiplexed connection wrapped by memory pipes (`cnc.Connection`), `sing-mux` failed to cast the connection to a `syscall.Conn` and rejected client Brutal requests, breaking proxy connectivity.
* **Solution**:
  - Implemented `Upstream()` and `NetConn()` interfaces for Xray's `stat.CounterConnection` (`transport/internet/stat/connection.go`) to allow robust connection unwrapping.
  - Injected a `brutalConn` connection wrapper in `common/singmux/server.go` just before the connection is passed to `sing-mux`. This intercepts `SyscallConn()` queries, extracting the raw physical TCP socket from `session.InboundFromContext(ctx)` and exposing it directly to `sing-mux`, ensuring Brutal pacing successfully applies at the kernel level.

---

### 10. Remote sing-mux Dependency & Reconnect Patch
* **Problem**: When a multiplexed connection retry occurred in Xray/Mihomo client (due to a Reality session ticket expiration after server reboot), the `clientConn` wrapper dynamically swapped the underlying stream. However, the connection copy loop (`bufio.Copy`) recursively unwrapped the connection via `Upstream() any` and kept referencing the old, closed stream object directly, leading to write failures and connection crashes.
* **Solution**:
  - Forked `metacubex/sing-mux` to `zhfreal/sing-mux` using `gh`.
  - Switched the `github.com/metacubex/sing-mux` dependency to the remote fork repository and tag `github.com/zhfreal/sing-mux v0.3.10-patch4` in `go.mod`.
  - Removed `Upstream()` on the `clientConn` wrapper class inside `sing-mux` to prevent copy loops from bypassing the wrapper, ensuring transparent re-routing of packets to the active retried stream.

---

## Guidelines for Upstream Maintenance & Updates

When updating upstream REALITY or merging newer changes:
1. Fetch latest changes from the upstream `XTLS/REALITY` repository.
2. Rebase or merge local branch `reality-wildcard-patches` on top of newer upstream commits.
3. Push the updated branch/commits to the remote fork repository `github.com/zhfreal/REALITY`.
4. Calculate the new pseudo-version of the commit using the format:
   `v0.0.0-YYYYMMDDHHMMSS-12charhash`
5. Update `Xray-core-mine/go.mod`'s `replace` directive to point to the new remote pseudo-version, then run `go mod tidy` and test compilation.

---

### 11. Refined Bug Fixes (August 2026 Patches)
* **OCSP Ticker Callback Tokenization (BUG-1, BUG-2, BUG-3)**: Implemented tokenized callbacks with stop channels in `setupOcspTicker` to cleanly support hot-reloads and prevent goroutine leaks when configs are discarded.
* **VLESS Debug Prints Removal (BUG-4)**: Eliminated dead/debug wrappers (`flushConn` / `unwrapConn` / `reflect`) in VLESS and replaced `fmt.Println` with `errors.LogWarning` in Reality.
* **sing-mux Thread-Safe retry & Replay Guard (BUG-11, BUG-12)**: Redesigned the retry mechanism in `sing-mux-mine/client_conn.go` to use connection and state locks, condition variables, and check `swapped` flags to prevent concurrent duplicate buffer replays.
* **KeyLogWriter Lifecycle Safety (BUG-6)**: Managed open `KeyLogWriter` file handles using a refcounted cache and `tls.Config` / `reality.Config` finalizers, avoiding premature closure errors.
* **wildcard Active Probing (BUG-14)**: Updated `GetProbeSNI` and `GetConcreteDomain` in `reality-mine/record_detect.go` to support random prefix generation, entropy-starvation fallbacks, and sibling SNI/IP destination fallbacks.


