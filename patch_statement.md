# PATCH STATEMENT: Custom Enhancements, Upstream Migrations & Dependency Hardening

This document details the modifications applied to the custom `Xray-core` codebase repository (`github.com/zhfreal/Xray-core`). These changes integrate wildcard SNI pattern compiling and permissive ML-KEM fallback, fix splithttp resource leaks and concurrency races, add global certificate caching and OCSP deduplication, integrate the Queqiao transport protocol with smart fallback delay and dialer registration, implement XMUX connection retirement across standard mux and `sing-mux`, and integrate remote patched dependencies for `REALITY` and `sing-mux`.

---

## Repository Details
* **Base Upstream Version**: Tag `v26.9.9` (commit `52a412d9`), Go 1.27 toolchain
* **Fork Repository**: `github.com/zhfreal/Xray-core`
* **Development Branch**: `xray-wildcard-patches`
* **Rebase Working Branch**: `xray-wildcard-patches-v26.9.9`
* **REALITY Fork Dependency**: `github.com/zhfreal/REALITY` branch `reality-wildcard-patches` (`v1.26.10-0.20260911055628-1990e180fe4d`)
* **sing-mux Fork Dependency**: `github.com/zhfreal/sing-mux` branch `patch-v0.3.11` (tags `v0.3.11-patch1`, `v0.3.11+patch1`)

---

## Modular Implementation Details

### Module 1: REALITY Wildcard SNI Pattern Matching & Permissive ML-KEM Negotiation
* **Wildcard Pattern Pre-compilation (`transport/internet/reality/config.go`)**:
  - Injected `config.CompileServerNamePatterns()` inside `GetREALITYConfig()` right before returning the compiled REALITY config struct.
  - Compiles wildcard patterns (e.g. `*.example.com` or `*`) exactly once at startup so that SNI regex evaluation is fast and zero-allocation during connection handshakes.
* **Permissive Post-Quantum ML-KEM Fallback (`reality-mine/tls.go`)**:
  - Upstream commit `8cdf7bf` strictly rejected any ClientHello lacking `X25519MLKEM768`.
  - Replaced strict key share validation with a permissive key share negotiation loop supporting both post-quantum `X25519MLKEM768` and classical `X25519` key shares, falling back cleanly to classical `X25519` when post-quantum is omitted. Third-party clients (Mihomo, Sing-box, mobile presets `ios`, `edge`, `qq`) connect successfully without handshake failure.
  - Preserved 17 KiB buffer expansion (`size = 17 * 1024`) from commit `393f8de`.
  - Preserved wildcard SNI matching (`config.MatchServerName`) and 100ms polling with 10-iteration ceiling on post-handshake record detection.
* **Wildcard Active Probing (`reality-mine/record_detect.go`)**:
  - Updated `GetProbeSNI` and `GetConcreteDomain` to support random prefix generation, entropy-starvation fallbacks, and sibling SNI/IP destination fallbacks during active probing.
* **Debug Prints Elimination**:
  - Eliminated dead/debug wrappers (`flushConn` / `unwrapConn` / `reflect`) in VLESS and replaced `fmt.Println` with `errors.LogWarning` in Reality.

---

### Module 2: Dependency Routing to Remote GitHub Forks (`go.mod`)
Injected `replace` directives in `go.mod` pointing `github.com/xtls/reality` and `github.com/metacubex/sing-mux` to their respective remote GitHub forks:
```go
replace github.com/xtls/reality => github.com/zhfreal/REALITY v1.26.10-0.20260911055628-1990e180fe4d

replace github.com/metacubex/sing-mux => github.com/zhfreal/sing-mux v0.3.11-patch1
```

---

### Module 3: Global TLS Certificate Caching, OCSP Hot-Reload Deduplication & KeyLogWriter Safety
* **Global Certificate Cache (`transport/internet/tls/cert_cache.go`, `config.go`)**:
  - Implemented `globalCertCache` (`sync.Map`) to cache `*tls.Certificate` objects keyed by their file paths (when both `CertificatePath` and `KeyPath` are set) or by the SHA-256 hash of their raw certificate+key bytes.
  - Extracted `getX509KeyPair()` into `cert_cache.go` as a shared helper for parsing X.509 key pairs.
  - Refactored `BuildCertificates()` in `config.go` to pull from this cache, preventing duplicate parsing of X.509 chains across multiple inbounds.
* **OCSP Hot-Reload Deduplication & Tokenized Callbacks (`transport/internet/tls/ocsp_ticker.go`)**:
  - Replaced per-inbound OCSP hot-reload tickers with a centralized `globalOcspTickerMap` (`sync.Map`), ensuring only a single background goroutine and file watcher is launched per unique certificate file pair.
  - Tokenized callbacks with stop channels in `setupOcspTicker` to cleanly support hot-reloads and prevent goroutine leaks when configs are discarded.
  - Direct index binding: Bound reloaded certificates directly by slice index rather than fragile certificate hash comparisons that fail upon file modification.
  - Multi-inbound certificate hot-reload distribution: Updated `ocspTickerState.callbacks` to distribute the newly parsed `*tls.Certificate` directly to callbacks and `globalCertCache` (`sync.Map.Store`), eliminating stale certificate cache rewrites across multi-inbound configurations.
* **KeyLogWriter Mutex Hierarchy & Lifecycle Safety (`transport/internet/tls/config.go`, `reality/config.go`)**:
  - In `keyLogWriterWrapper.release()`, acquired `globalKeyLogCacheMu` before decrementing `refCount` and deleting the entry from `globalKeyLogCache`, eliminating the race window where another goroutine could retrieve a closed file handle.
  - Aligned Reality `keyLogWriterWrapper.release()` with `tls/config.go`. Managed open file handles using a refcounted cache and finalizers, avoiding premature closure errors.

---

### Module 4: sing-mux Multiplexing, Brutal Socket Pacing & Upstream v0.3.11-patch1 Integration
* **Protocol & Architecture Support (`common/singmux/`, `app/proxyman/`)**:
  - Supports `smux`, `yamux`, and `h2mux` protocols over standard outbound connections.
  - Created bridge adapter package `common/singmux` translating Xray's `transport.Link` pipes to `net.Conn` and `N.PacketConn`.
  - Implemented synchronous lifecycle blocking in client-side `Dispatch` and server-side `NewConnection`/`NewPacketConnection` copy loops to prevent premature stream and client TCP socket closures.
  - Updated `AlwaysOnInboundHandler` on server side to intercept sing-mux target FQDN requests (`sp.mux.sing-box.arpa`) and route parsed streams back to Xray dispatcher.
* **Adaptive Padding & Cross-Platform TCP Brutal (`common/singmux/server.go`)**:
  - Set `Padding: false` in `mux.ServiceOptions` permissive mode, allowing the server to dynamically accept both `Version0` (unpadded) and `Version1` (padded) clients on the fly without protocol version mismatches.
  - Enabled `Brutal` globally on supported platforms (`Enabled: mux.BrutalAvailable && getBrutalCapBPS() > 0`) with environment variable speed cap (`SMUX_BRUTAL_CAP_MBPS`, defaulting to 100 Mbps). On non-Linux platforms (e.g. Windows), Brutal is automatically disabled to prevent startup failures.
  - Added case-insensitive rate string handling (`StringToBps`) supporting `"mbps"` / `"kbps"` prefixes.
* **TCP Brutal Socket Pacing Compatibility via `brutalConn` (`common/singmux/server.go`, `stat/connection.go`)**:
  - Implemented `Upstream()`, `NetConn()`, and `SyscallConn()` interfaces for Xray's `stat.CounterConnection` (`transport/internet/stat/connection.go`).
  - Injected `brutalConn` connection wrapper in `server.go` to intercept `SyscallConn()` queries, extracting the raw physical TCP socket from `session.InboundFromContext(ctx)` and exposing it directly to `sing-mux`, ensuring Brutal pacing successfully applies at the kernel level.
* **UDP Header Buffer Overflow Panic Fix (`common/singmux/server.go`, `sing-mux-mine/server_conn.go`)**:
  - Pre-allocated required front headroom using `N.CalculateFrontHeadroom(conn)` with a safe minimum floor of 256 bytes (`if headroom < 256 { headroom = 256 }`).
  - Allocated `singBuf` sized with `headroom` and shifted start pointer using `singBuf.Resize(headroom, 0)` before copying payload bytes. Zero reallocation copies and ample headroom for `buffer.ExtendHeader(2)`.
  - Added defensive headroom reallocation checks (`if buffer.Start() < 2`) in `serverPacketConn.WritePacket` and `serverPacketAddrConn.WritePacket` in `sing-mux` fork.
* **UDP Packet Loop Deadlock Prevention & Outbound 443 Policy**:
  - Relocated cleanup routines (`conn.Close()`, `common.Interrupt(link.Reader)`, `common.Close(link.Writer)`) into each UDP reader/writer goroutine's defer stack in `NewPacketConnection`, preventing permanent deadlocks when peers disconnect.
  - Enforced UDP 443 policy (`"reject"` / `"skip"`) before dispatching to `sing-mux` in `app/proxyman/outbound/handler.go`.
* **XMUX Connection Retirement Parameter Forwarding**:
  - Mapped `c-max-reuse-times`, `h-max-request-times`, and `h-max-reusable-secs` in `common/singmux/client.go` and forwarded range parameters directly into `sing-mux.NewClient()`.
* **Remote Fork Hardening & Upstream v0.3.11-patch1 Integration**:
  - **Relay Wrapper Isolation**: Removed `Upstream()` on `clientConn` and `clientConnWithCloseWrite` in `sing-mux` to prevent `bufio.Copy` relay loops from unwrapping past the connection layer, enabling transparent re-routing when connections swap after Reality ticket expiration.
  - **Thread-Safe Retry & Replay Guard**: Synchronized retry mechanism in `client_conn.go` using connection locks, state locks, condition variables, and `(swapped, ok)` state check to prevent duplicate write replays.
  - **Half-Close Support (`CloseWrite`)**: Implemented `clientConnWithCloseWrite` delegating `CloseWrite()` via thread-safe `c.getConn()`. Maps half-close to `END_STREAM` (`httpConn`) and `FIN` (`yamuxWrapStream`), preserving response readability and eliminating connection dropouts on HTTP uploads and gRPC calls.
  - **Header Initialization**: Initialized `Header: make(http.Header)` in `h2MuxClientSession.Open` (upstream commit `b602024`), fixing `"http: nil Request.Header"` panics under `golang.org/x/net v0.58.0`.
  - **Yamux Half-Close Correction**: Mapped `yamuxWrapStream.CloseWrite()` to `w.Stream.CloseWrite()` to emit `FIN` without prematurely resetting the read half.

---

### Module 5: User Configuration & Environment Guide for sing-mux

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

---

### Module 6: XMUX-Style Connection Retirement for Standard Mux (v2ray-mux)
* **Problem**: Under stateful censorship (e.g. GFW), long-lived multiplexed TCP connections are easily identified through traffic duration fingerprinting and killed via injected `TCP RST` packets. Standard `mux` (v2ray-mux) previously lacked connection retirement controls.
* **Implementation Details (`common/mux/client.go`, `app/proxyman/config.proto`, `infra/conf/xray.go`)**:
  - **Protobuf Configuration Extension**: Extended `MultiplexingConfig` with `c_max_reuse_times` (field 13), `h_max_request_times` (field 14), and `h_max_reusable_secs` (field 15) as strings to support min-max randomized range values.
  - **JSON Configuration Parser**: Extended `MuxConfig` with `cMaxReuseTimes`, `hMaxRequestTimes`, and `hMaxReusableSecs` mapped to `*Int32Range`. Preserves range notation across configurations.
  - **Outbound Handler Wiring**: Forwarded retirement configuration strings from `MultiplexSettings` into `mux.ClientStrategy` for both TCP `h.mux` and UDP `h.xudp`.
  - **Worker Lifecycle & Periodic Retirement (`common/mux/client.go`)**:
    - Embedded `unreusableAt`, `leftRequests`, and `leftReuseTimes` within `ClientWorker`.
    - Extended `IncrementalWorkerPicker` with `drainingWorkers` tracking and retirement bounds checking in `findAvailable()`.
    - Added in-flight stream counters (`inFlight`) and atomic request decrement in `Dispatch()` to avoid TOCTOU races between picking and session allocation.
    - Updated `monitor()` and `cleanup()` with 5-minute hard drain timeouts to reclaim stuck retired sessions without resource leakage.

---

### Module 7: Queqiao Transport Protocol Integration (Inbound, Outbound, WAN Defaults & Dialer Registration)
* **Inbound Handler (`proxy/queqiao/inbound/inbound.go`)**:
  - Implemented server handler implementing `core.InboundHandler`, `core.Initializable`, and `protocol.UserManager`.
  - Dynamic user tracking via `AddUser`, `RemoveUser`, `GetUser`, and `GetUsersCount` with thread-safe `sync.Map`.
  - Extracted TLS certificates, private keys, and hop port counts from `streamSettings.SecuritySettings.(*xtls.Config)` and `streamSettings.ProtocolSettings.(*queqiao.TransportConfig)`, with fallback to inbound root config.
  - Zero-copy stream adaptation linking `libqueqiao.Server` directly to Xray's `dispatcher.DispatchLink`.
  - Panic-safe UDP destination handling: type-assertion fast path for `*net.UDPAddr` and fallback `xnet.ParseDestination`.
* **Outbound Handler (`proxy/queqiao/outbound/outbound.go`)**:
  - Implemented client outbound handler implementing `core.OutboundHandler`.
  - Zero-copy stream adaptation via `BufferedReader`/`BufferedWriter` piped directly to `client.Pipe`.
  - Zero-DNS-leak UDP forwarding: wraps domain destinations in `domainUDPAddr` for remote egress resolution on the Queqiao gateway.
  - Goroutine leak prevention via `done` channel synchronization ensuring clean socket closure.
* **Configuration Schema & StreamSettings (`infra/conf/queqiao.go` & `infra/conf/transport_internet.go`)**:
  - Implemented `QueqiaoServerConfig`, `QueqiaoClientConfig`, and `QueqiaoConfig` (transport settings).
  - Registered `queqiaoSettings` under `StreamConfig` and wired `Build()` into `internet.TransportConfig`.
  - Added nil-pointer protection on `c.Address` in `QueqiaoClientConfig.Build()`.
* **Configurable Fallback Delay & WAN Adaptive Defaults (`proxy/queqiao/config.proto`, `proxy/queqiao/outbound/outbound.go`)**:
  - Added `fallback_delay`, `fallback_grace`, `udp_cooldown`, and `udp_failure_threshold` to Protobuf and JSON schemas.
  - If `fallbackDelay <= 0` (unconfigured), dynamically scales from `HandshakeTimeout` (20% clamped between 500ms and 2000ms), or defaults to a WAN-robust **1000ms**, eliminating premature TCP fallback on high-latency WAN transits where physical RTT is ~240ms.
* **Transport Dialer Registration & Graceful Fallback (`proxy/queqiao/outbound/outbound.go`)**:
  - Registered `internet.RegisterTransportDialer("queqiao", DialQueqiao)` where `DialQueqiao` invokes `internet.DialSystem` to establish underlying TCP connections with complete socket option support. Eliminates `queqiao dialer not registered` errors in Style B configuration (`streamSettings.network = "queqiao"`).
  - Enhanced `DialContextFunc` so that if `d.Dial(ctx, dest)` encounters any error, it gracefully falls back to `internet.DialSystem(ctx, dest, sockopt)` instead of aborting the TCP fallback lane.
* **CLI Key Generation & Doctor Tools (`main/commands/all/queqiao.go`)**:
  - Implemented `xray queqiao` matching `xray x25519` key-value output format.
  - Implemented `xray queqiao doctor` subcommand wrapping `configgen.RunDoctorCLI`.

---

### Module 8: SplitHTTP Xmux Connection Lifecycle & Concurrency Fixes
* **Idle Connection Reclamation (`transport/internet/splithttp/client.go`)**:
  - Updated `DefaultDialerClient.Close()` to invoke `tr.CloseIdleConnections()` on the internal HTTP transport. When an `XmuxClient` reaches its `cMaxReuseTimes` limit, Go immediately drops the idle HTTP/2 connection on the client side rather than holding it in `ESTABLISHED` state.
* **Protobuf Copylocks Warning Fix (`transport/internet/splithttp/dialer.go`, `mux.go`)**:
  - Changed `XmuxManager` and related functions to accept `XmuxConfig` by pointer (`*XmuxConfig`) rather than by value (`XmuxConfig`), eliminating `go vet` copylocks warnings and mutex state duplication.
* **Data Race Elimination (`transport/internet/splithttp/client.go`, `dialer.go`)**:
  - Reconciled upstream atomic `WaitReadCloser` (`atomic.Pointer[io.ReadCloser]` + `done.Instance`) with `DefaultDialerClient.Close()` idle connection cleanup.
  - Captured `bLen := int(buff.Len())` prior to pipe handoff in `uploadWriter.Write`, fully passing `go test -race`.

---

### Module 9: Burst Observatory Latency Reporting, Scheduling & Concurrency Fixes
* **Timestamp Tracking in `HealthPingRTTS` (`app/observatory/burst/healthping_result.go`)**:
  - Added `lastSeen`, `lastTry`, and `totalCount` fields to `HealthPingRTTS`. `Put()` tracks `lastTry` for every probe attempt and `lastSeen` only for successful probes. Exported accessor methods `LastSeen()`, `LastTry()`, and `TotalCount()`.
* **Real Timestamps & CPU Optimization in `createResult` (`app/observatory/burst/burstobserver.go`)**:
  - Populated `LastSeenTime` and `LastTryTime` fields in `OutboundStatus` from `HealthPingRTTS` timestamps (previously hardcoded to `0`).
  - Reduced 7 redundant `getStatistics()` calls per node to 1, removing wasted CPU during status queries.
* **Context Propagation in `MeasureDelay` (`app/observatory/burst/ping.go`)**:
  - Changed `MeasureDelay` to accept `ctx context.Context` and use `http.NewRequestWithContext`, enabling probe cancellation. Prefers request `ctx` but falls back to captured root `ctxv` when `core.FromContext(ctx)` is nil (preserving Xray routing metadata). Moved `resp.Body.Close()` to `defer`.
* **Wait-First Scheduler Startup Race Fix (`app/observatory/burst/healthping.go`)**:
  - Moved `select { case <-ticker.C }` to the top of the scheduler loop (wait-first pattern), preventing the initial check goroutine and scheduler goroutine from firing `doCheck` simultaneously at startup (eliminating startup connection storms).
* **Deadline Safety Buffer & Concurrency Limiter (`app/observatory/burst/healthping.go`)**:
  - Capped random probe delay to `maxDelay = duration - timeout - 500ms` (clamped to 0), ensuring all probes complete before the next ticker fires.
  - Added a semaphore (`chan struct{}` with capacity `defaultMaxConcurrency = 16`) to `HealthPing`, acquired/released in `time.AfterFunc` callbacks, limiting simultaneous in-flight probes to 16.

---

## Guidelines for Upstream Maintenance & Updates

When updating upstream REALITY or merging newer changes:
1. Fetch latest changes from the upstream `XTLS/REALITY` repository.
2. Rebase or merge local branch `reality-wildcard-patches` on top of newer upstream commits.
3. Push the updated branch/commits to the remote fork repository `github.com/zhfreal/REALITY`.
4. Calculate the new pseudo-version of the commit using the format:
   `v0.0.0-YYYYMMDDHHMMSS-12charhash`
5. Update `Xray-core-mine/go.mod`'s `replace` directive to point to the new remote pseudo-version, then run `go mod tidy` and test compilation.
