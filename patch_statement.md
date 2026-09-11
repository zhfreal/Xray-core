# PATCH STATEMENT: Wildcard SNI Matching, Splithttp Fixes, Certificate Caching & Burst Observatory

This document details the modifications applied to the custom `Xray-core` codebase repository (`github.com/zhfreal/Xray-core`). These changes integrate wildcard SNI pattern compiling, fix splithttp resource leaks and linter warnings, add global certificate caching, and use a remote GitHub fork for the `reality` package dependency.

---

## Repository Details
* **Base Upstream Version**: Tag `v26.9.9` (commit `52a412d9`)
* **Fork Repository**: `github.com/zhfreal/Xray-core`
* **Development Branch**: `xray-wildcard-patches`
* **Rebase Working Branch**: `xray-wildcard-patches-v26.9.9`
* **REALITY Fork Dependency**: `github.com/zhfreal/REALITY` branch `reality-wildcard-patches` (tag `v1.26.9+patch1`)

---

## Detailed Changes

### 1. Wildcard Pattern Compilation Trigger (`transport/internet/reality/config.go`)
* Added a call to `config.CompileServerNamePatterns()` inside `GetREALITYConfig()` right before returning the compiled REALITY config struct. This compiles wildcard patterns (like `*.example.com` or `*`) exactly once at startup so that SNI regex evaluation is fast and efficient during connection handshakes.

### 2. Dependency Routing to Remote GitHub Fork (`go.mod`)
* Injected `replace` directives pointing `github.com/xtls/reality` and `github.com/metacubex/sing-mux` to their respective remote GitHub forks to compile our custom branches:
  ```go
  replace github.com/xtls/reality => github.com/zhfreal/REALITY v1.26.10-0.20260911055628-1990e180fe4d
  replace github.com/metacubex/sing-mux => github.com/zhfreal/sing-mux v0.3.10-patch8
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
  - Enabled `Brutal` globally on supported platforms (`Enabled: mux.BrutalAvailable && getBrutalCapBPS() > 0`) with an environment-variable-driven maximum speed cap (`SMUX_BRUTAL_CAP_MBPS`, defaulting to 100 Mbps). On non-Linux platforms (e.g. Windows), Brutal is automatically disabled, preventing TCP Brutal startup failures (`TCP Brutal is only supported on Linux`) while allowing normal multiplexed connections to proceed smoothly.

### 9. sing-mux TCP Brutal Socket Pacing Compatibility
* **Problem**: TCP Brutal in `metacubex/sing-mux` configures TCP socket pacing via the `TCP_CONGESTION` and `TCP_BRUTAL_PARAMS` (23301) syscalls. Because Xray-core runs `sing-mux` deep within a multiplexed connection wrapped by memory pipes (`cnc.Connection`), `sing-mux` failed to cast the connection to a `syscall.Conn` and rejected client Brutal requests, breaking proxy connectivity.
* **Solution**:
  - Implemented `Upstream()`, `NetConn()`, and `SyscallConn()` interfaces for Xray's `stat.CounterConnection` (`transport/internet/stat/connection.go`) to allow robust connection unwrapping and raw syscall access.
  - Injected a `brutalConn` connection wrapper in `common/singmux/server.go` just before the connection is passed to `sing-mux`. This intercepts `SyscallConn()` queries, extracting the raw physical TCP socket from `session.InboundFromContext(ctx)` and exposing it directly to `sing-mux`, ensuring Brutal pacing successfully applies at the kernel level.

---

### 10. Remote sing-mux Dependency & Reconnect Patch
* **Problem**: When a multiplexed connection retry occurred in Xray/Mihomo client (due to a Reality session ticket expiration after server reboot), the `clientConn` wrapper dynamically swapped the underlying stream. However, the connection copy loop (`bufio.Copy`) recursively unwrapped the connection via `Upstream() any` and kept referencing the old, closed stream object directly, leading to write failures and connection crashes.
* **Solution**:
  - Forked `metacubex/sing-mux` to `zhfreal/sing-mux` using `gh`.
  - Switched the `github.com/metacubex/sing-mux` dependency to the remote fork repository and tag `github.com/zhfreal/sing-mux v0.3.10-patch7` in `go.mod`.
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

---

### 12. Concurrency Safety & Multi-Inbound TLS Hardening (August 2026 Audit)
* **Multi-Inbound Certificate Hot-Reload Distribution (`transport/internet/tls/ocsp_ticker.go` & `config.go`)**: Updated `ocspTickerState.callbacks` to distribute the newly parsed `*tls.Certificate` directly to callbacks and `globalCertCache` (`sync.Map.Store`). Eliminates stale certificate cache rewrites across multi-inbound configurations.
* **KeyLogWriter Mutex Hierarchy (`transport/internet/tls/config.go`)**: In `keyLogWriterWrapper.release()`, acquired `globalKeyLogCacheMu` before decrementing `refCount` and deleting the entry from `globalKeyLogCache`, eliminating the race window where another goroutine could retrieve a closed file handle.
* **Remote Patched Dependency Replace Directives (`go.mod`)**: Updated `go.mod` to reference remote patched dependencies (`github.com/zhfreal/REALITY v1.26.6-0.20260816141302-f0d1b9ba263b` and `github.com/zhfreal/sing-mux v0.3.10-patch7`).
* **Graceful ECH Live Testing (`transport/internet/tls/ech_test.go`)**: Handled external server ECH probe rejections gracefully in tests without panicking.

---

### 13. Burst Observatory Latency Reporting, Scheduling & Concurrency Fixes (August 2026)
* **Timestamp Tracking in `HealthPingRTTS` (`app/observatory/burst/healthping_result.go`)**: Added `lastSeen`, `lastTry`, and `totalCount` fields to `HealthPingRTTS`. `Put()` now tracks `lastTry` for every probe attempt and `lastSeen` only for successful probes (non-`rttFailed`). Exported accessor methods `LastSeen()`, `LastTry()`, and `TotalCount()`.
* **Real Timestamps in `createResult` (`app/observatory/burst/burstobserver.go`)**: Fixed `LastSeenTime` and `LastTryTime` fields in `OutboundStatus` — previously hardcoded to `0`, now populated from `HealthPingRTTS` timestamps. Also eliminated 7 redundant `getStatistics()` calls per node (reduced to 1), removing wasted CPU during status queries.
* **Context Propagation in `MeasureDelay` (`app/observatory/burst/ping.go`)**: Changed `MeasureDelay` to accept `ctx context.Context` and use `http.NewRequestWithContext`, enabling probe cancellation. The `DialContext` closure now prefers the request `ctx` (for cancellation) but falls back to the captured root `ctxv` when `core.FromContext(ctx)` is nil (preserving Xray routing metadata). Moved `resp.Body.Close()` to a `defer` to prevent resource leaks on early returns.
* **Scheduler Startup Race Fix (`app/observatory/burst/healthping.go`)**: Moved the `select { case <-ticker.C }` block to the top of the scheduler loop (wait-first pattern). Previously, the initial check goroutine and the scheduler goroutine both fired `doCheck` simultaneously at startup, doubling the connection storm (e.g., 144 concurrent probes instead of 72 for 72 nodes).
* **Deadline Safety Buffer (`app/observatory/burst/healthping.go`)**: Capped the random probe delay to `maxDelay = duration - timeout - 500ms` (clamped to 0), ensuring all probes complete before the next ticker fires, preventing scheduling pile-ups.
* **Concurrency Limiter (`app/observatory/burst/healthping.go`)**: Added a semaphore (`chan struct{}` with capacity `defaultMaxConcurrency = 16`) to `HealthPing`, acquired/released in `time.AfterFunc` callbacks. Limits simultaneous in-flight probes to 16, preventing connection storms on large node sets. Semaphore acquisition respects context cancellation.
* **`checkConnectivity` Context Threading (`app/observatory/burst/healthping.go`)**: Added `ctx context.Context` parameter to `checkConnectivity()`, propagating cancellation to the connectivity check's `MeasureDelay` call.
* **Unit Tests (`app/observatory/burst/healthping_result_test.go`, `healthping_test.go`, `healthping_internal_test.go`)**: Added timestamp assertion tests (verifying `LastSeen` unchanged on failure, `LastTry` updated on failure), `PutResult`/`Cleanup` integration tests, deadline buffer computation tests (6 sub-cases), `MeasureDelay` context cancellation test (mock slow server, assert <2s return), and semaphore concurrency limit test (24 workers, peak capped at 16). All pass with `-race`.

---

### 14. Splithttp Data Races, sing-mux Concurrency, Certificate Reload & KeyLogWriter Fixes (September 2026 Audit)
* **Splithttp Data Race Elimination (`transport/internet/splithttp/client.go` & `dialer.go`)**: Fixed pre-existing upstream race in `WaitReadCloser` by wrapping the field in `sync.RWMutex` and `sync.Once` channel closure. Captured `bLen := int(buff.Len())` prior to pipe handoff in `uploadWriter.Write`, fully passing `go test -race`.
* **sing-mux UDP Packet Loop Deadlock Prevention (`common/singmux/server.go`)**: Relocated cleanup routines (`conn.Close()`, `common.Interrupt(link.Reader)`, `common.Close(link.Writer)`) from the outer function scope directly into each UDP reader/writer goroutine's defer stack in `NewPacketConnection`, ensuring loops unblock each other and preventing permanent deadlocks when remote peers disconnect.
* **Case-Insensitive Rate String Handling (`common/singmux/client.go`)**: Added `(?i)` flag and unit normalization to `rateStringRegexp` in `StringToBps` to support `"mbps"` / `"kbps"` prefixes.
* **`stat.CounterConnection` Unwrapping & Syscall Support (`transport/internet/stat/connection.go`)**: Implemented `Upstream()`, `NetConn()`, and `SyscallConn()` on `CounterConnection` as specified in Section 9.
* **Reality KeyLogWriter Mutex Hierarchy Alignment (`transport/internet/reality/config.go`)**: Aligned `keyLogWriterWrapper.release()` with `tls/config.go` by locking `globalKeyLogCacheMu` before file closure and refcount decrements.
* **TLS Certificate Hot-Reload Direct Index Binding (`transport/internet/tls/config.go`)**: Bound reloaded certificates directly by slice index rather than fragile certificate hash comparisons that fail upon file modification. Cleaned up unused `encoding/pem` import, unused `addRefLocked`, and unused `globalCaCertMutex`.
* **Outbound UDP 443 Policy Enforcement (`app/proxyman/outbound/handler.go`)**: Moved `h.udp443` configuration outside the legacy mux `else` branch and enforced UDP 443 policy (`"reject"` / `"skip"`) before dispatching to `sing-mux`.

---

### 15. sing-mux UDP Header Buffer Overflow Panic Fix (September 2026)
* **Problem**: Under high-load conditions or when clients (e.g. Mihomo / Clash Meta with `smux: enabled: true, only-tcp: false`) send UDP traffic over `sing-mux` to virtual domain `sp.mux.sing-box.arpa:444`, `xray.service` crashed repeatedly with:
  `panic: buffer overflow: capacity 48/32/7/2, start 0, need 2`
  Inside `Xray-core-mine/common/singmux/server.go:NewPacketConnection`, UDP packets received from Xray's `link.Reader` were copied into new buffers created via `singbuf.NewPacket()` or `singbuf.NewSize()`. These new buffers were initialized with `start = 0`. When returning response packets to `sing-mux` via `conn.WritePacket(singBuf, destAddr)`, `sing-mux` (`serverPacketConn.WritePacket` / `serverPacketAddrConn.WritePacket`) prepended a 2-byte header with `buffer.ExtendHeader(2)`. Because `start < 2`, `metacubex/sing/common/buf/buffer.go` panicked with buffer overflow, causing service crash and systemd start-limit-burst lockouts on high-traffic nodes.
* **Solution**:
  - **Headroom Pre-allocation in `Xray-core` (`common/singmux/server.go`)**: Calculated required front headroom using `N.CalculateFrontHeadroom(conn)` with a safe minimum floor of 256 bytes (`if headroom < 256 { headroom = 256 }`). Allocated `singBuf` sized with `headroom` and shifted its start pointer using `singBuf.Resize(headroom, 0)` before copying the payload bytes. This guarantees zero reallocation copies and leaves ample headroom for `sing-mux` packet headers.
  - **Defensive Headroom Fallback in `sing-mux` (`server_conn.go`)**: In our patched fork `github.com/zhfreal/sing-mux`, added defensive checks (`if buffer.Start() < 2`) in both `serverPacketConn.WritePacket` and `serverPacketAddrConn.WritePacket` to dynamically allocate a headroom-capable buffer rather than crashing if an unpadded buffer is passed from any component.
  - **Unit Tests (`common/singmux/singmux_test.go`)**: Added `TestNewPacketConnectionHeadroom` to simulate UDP packet dispatch from outbound back into `sing-mux`, asserting that `Start() >= 256` and verifying that `buffer.ExtendHeader(2)` succeeds without panic.

---

### 16. XMUX-Style Connection Retirement for Mux and sing-mux (September 2026)
* **Problem**: Under stateful censorship (e.g. GFW), long-lived multiplexed TCP connections are easily identified through traffic duration fingerprinting and frequently killed via injected `TCP RST` packets. Although Xray's `splithttp` (xhttp) implemented XMUX to retire connections after `hMaxReusableSecs`, standard `mux` (v2ray-mux) and `sing-mux` lacked connection retirement controls.
* **Solution**:
  - **Protobuf Configuration Extension (`app/proxyman/config.proto` & `config.pb.go`)**: Extended `MultiplexingConfig` with `c_max_reuse_times` (field 13), `h_max_request_times` (field 14), and `h_max_reusable_secs` (field 15) as strings to support range values. Updated generated Go protobuf structs and accessors.
  - **JSON Configuration Parser (`infra/conf/xray.go` & `xray_test.go`)**: Extended `MuxConfig` with `cMaxReuseTimes`, `hMaxRequestTimes`, and `hMaxReusableSecs` mapped to `*Int32Range`. Implemented string conversion in `Build()` to preserve range notation across configurations.
  - **Outbound Handler Wiring (`app/proxyman/outbound/handler.go`)**: Forwarded retirement configuration strings from `MultiplexSettings` into `mux.ClientStrategy` for both TCP `h.mux` and UDP `h.xudp`.
  - **sing-mux Bridge (`common/singmux/client.go`)**: Forwarded retirement ranges directly to `sing-mux.NewClient()` options.
  - **Worker Lifecycle & Periodic Retirement (`common/mux/client.go`)**:
    - Embedded `unreusableAt`, `leftRequests`, and `leftReuseTimes` within `ClientWorker`.
    - Extended `IncrementalWorkerPicker` with `drainingWorkers` tracking and retirement bounds checking in `findAvailable()`.
    - Added in-flight stream counters (`inFlight`) and atomic request decrement in `Dispatch()` to avoid TOCTOU races between picking and session allocation.
    - Updated `monitor()` and `cleanup()` with 5-minute hard drain timeouts to reclaim stuck retired sessions without resource leakage.
  - **Unit Tests (`common/mux/client_test.go` & `infra/conf/xray_test.go`)**: Added test coverage verifying worker retirement on request limits, reusable seconds, and picker rollover.

---

### 17. Queqiao Transport & Inbound/Outbound Protocol Integration (Client, Server & CLI) (September 2026)
- **Status:** **Implemented & Verified**
- **Changes:**
  - **Inbound Handler (`proxy/queqiao/inbound/inbound.go`)**:
    - Implemented server handler implementing `core.InboundHandler`, `core.Initializable`, and `protocol.UserManager`.
    - Dynamic user tracking via `AddUser`, `RemoveUser`, `GetUser`, and `GetUsersCount` with thread-safe `sync.Map`.
    - Extracted TLS certificates, private keys, and hop port counts from `streamSettings.SecuritySettings.(*xtls.Config)` and `streamSettings.ProtocolSettings.(*queqiao.TransportConfig)`, with fallback to inbound root config.
    - Zero-copy stream adaptation linking `libqueqiao.Server` directly to Xray's `dispatcher.DispatchLink`.
    - Panic-safe UDP destination handling: type-assertion fast path for `*net.UDPAddr` and fallback `xnet.ParseDestination`.
  - **Outbound Handler (`proxy/queqiao/outbound/outbound.go`)**:
    - Implemented client outbound handler implementing `core.OutboundHandler`.
    - Zero-copy stream adaptation via `BufferedReader`/`BufferedWriter` piped directly to `client.Pipe`.
    - Zero-DNS-leak UDP forwarding: wraps domain destinations in `domainUDPAddr` for remote egress resolution on the Queqiao gateway.
    - Goroutine leak prevention via `done` channel synchronization ensuring clean socket closure.
  - **Configuration Schema & StreamSettings (`infra/conf/queqiao.go` & `infra/conf/transport_internet.go`)**:
    - Implemented `QueqiaoServerConfig`, `QueqiaoClientConfig`, and `QueqiaoConfig` (transport settings).
    - Registered `queqiaoSettings` under `StreamConfig` and wired `Build()` into `internet.TransportConfig`.
    - Added nil-pointer protection on `c.Address` in `QueqiaoClientConfig.Build()`.
  - **CLI Key Generation & Path Diagnostics (`main/commands/all/queqiao.go`)**:
    - Implemented `xray queqiao` matching `xray x25519` key-value output format.
    - Implemented `xray queqiao doctor` subcommand wrapping `configgen.RunDoctorCLI`.
  - **Unit Testing (`infra/conf/queqiao_test.go` & `main/commands/all/queqiao_test.go`)**:
    - Validated configuration schema unmarshaling and CLI output formatting.

### 18. Upstream v26.9.9 Upgrade & Permissive REALITY Post-Quantum Fallback (September 2026)
- **Status:** **Implemented & Verified**
- **Upstream Release Base**: `v26.9.9` (commit `52a412d9`), specifying `go 1.27` toolchain.
- **Key Technical Reconciliations**:
  - **Permissive ML-KEM Fallback (`reality-mine/tls.go`)**:
    - Upstream commit `8cdf7bf` strictly rejected any ClientHello lacking `X25519MLKEM768`.
    - Patched key share loop to support both post-quantum `X25519MLKEM768` and classical `X25519` key shares, falling back cleanly to classical `X25519` when post-quantum is omitted. Third-party clients (Mihomo, Sing-box, mobile presets `ios`, `edge`, `qq`) connect successfully without rejection.
    - Preserved 17 KiB buffer expansion (`size = 17 * 1024`) from commit `393f8de`.
    - Preserved wildcard SNI matching (`config.MatchServerName`) and 100ms polling with 10-iteration ceiling on post-handshake record detection.
  - **SplitHTTP Race & Concurrency Alignment (`splithttp/client.go` & `splithttp/dialer.go`)**:
    - Reconciled upstream atomic `WaitReadCloser` (`atomic.Pointer[io.ReadCloser]` + `done.Instance`) with our `DefaultDialerClient.Close()` idle connection cleanup (`tr.CloseIdleConnections()`).
    - Adopted upstream's buffer length caching in `uploadWriter.Write()`.
  - **Protobuf & Multiplexing Compatibility (`app/proxyman/config.proto` & `config.pb.go`)**:
    - Reconciled upstream's `reserved 3;` in `SenderConfig` with our custom fields 5–15 in `MultiplexingConfig`.
  - **Verification**:
    - Restored `ios`, `edge`, `qq` in `testing/scenarios/vless_test.go` and verified 100% pass across all 6 uTLS fingerprints (`safari`, `chrome`, `firefox`, `ios`, `edge`, `qq`).
    - Validated Queqiao, sing-mux, burst observatory, and SplitHTTP test suites.
    - Verified binary builds (`./xray version` reporting `Xray 26.9.9 ... 9a76b69 (go1.27.1 linux/amd64)`) and local server configuration validation.

