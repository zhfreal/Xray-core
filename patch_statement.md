# PATCH STATEMENT: Wildcard SNI Matching Setup, Splithttp Fixes & Certificate Caching

This document details the modifications applied to the custom `Xray-core` codebase repository (`github.com/zhfreal/Xray-core`). These changes integrate wildcard SNI pattern compiling, fix splithttp resource leaks and linter warnings, add global certificate caching, and use a remote GitHub fork for the `reality` package dependency.

---

## Repository Details
* **Base Upstream Version**: Tag `v26.7.11`
* **Fork Repository**: `github.com/zhfreal/Xray-core`
* **Development Branch**: `xray-wildcard-patches`
* **Latest Local Patch Commit**: `8835be57`

---

## Detailed Changes

### 1. Wildcard Pattern Compilation Trigger (`transport/internet/reality/config.go`)
* Added a call to `config.CompileServerNamePatterns()` inside `GetREALITYConfig()` right before returning the compiled REALITY config struct. This compiles wildcard patterns (like `*.example.com` or `*`) exactly once at startup so that SNI regex evaluation is fast and efficient during connection handshakes.

### 2. Dependency Routing to Remote GitHub Fork (`go.mod`)
* Injected a `replace` directive pointing `github.com/xtls/reality` to the remote GitHub fork `github.com/zhfreal/REALITY` to fetch and compile our custom branch remotely:
  ```go
  replace github.com/xtls/reality => github.com/zhfreal/REALITY v0.0.0-20260706073825-6686691e6dfb
  ```
* The `require` section references:
  ```go
  github.com/xtls/reality v0.0.0-20260630031543-79cb6080a68f
  ```
  The `replace` directive overrides this to point to the latest fork commit (`6686691e6dfb`).

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

---

## Guidelines for Upstream Maintenance & Updates

When updating upstream REALITY or merging newer changes:
1. Fetch latest changes from the upstream `XTLS/REALITY` repository.
2. Rebase or merge local branch `reality-wildcard-patches` on top of newer upstream commits.
3. Push the updated branch/commits to the remote fork repository `github.com/zhfreal/REALITY`.
4. Calculate the new pseudo-version of the commit using the format:
   `v0.0.0-YYYYMMDDHHMMSS-12charhash`
5. Update `Xray-core-mine/go.mod`'s `replace` directive to point to the new remote pseudo-version, then run `go mod tidy` and test compilation.
