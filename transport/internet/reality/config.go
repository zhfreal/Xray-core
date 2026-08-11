package reality

import (
	"context"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
	"github.com/xtls/reality"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/transport/internet"
)

func (c *Config) GetREALITYConfig() *reality.Config {
	var dialer net.Dialer
	config := &reality.Config{
		DialContext: dialer.DialContext,

		Show: c.Show,
		Type: c.Type,
		Dest: c.Dest,
		Xver: byte(c.Xver),

		PrivateKey:   c.PrivateKey,
		MinClientVer: c.MinClientVer,
		MaxClientVer: c.MaxClientVer,
		MaxTimeDiff:  time.Duration(c.MaxTimeDiff) * time.Millisecond,

		NextProtos:             nil, // should be nil
		SessionTicketsDisabled: true,

		KeyLogWriter:           nil, // Moved below to attach finalizer to config
	}
	if c.Mldsa65Seed != nil {
		_, key := mldsa65.NewKeyFromSeed((*[32]byte)(c.Mldsa65Seed))
		config.Mldsa65Key = key.Bytes()
	}
	if c.LimitFallbackUpload != nil {
		config.LimitFallbackUpload.AfterBytes = c.LimitFallbackUpload.AfterBytes
		config.LimitFallbackUpload.BytesPerSec = c.LimitFallbackUpload.BytesPerSec
		config.LimitFallbackUpload.BurstBytesPerSec = c.LimitFallbackUpload.BurstBytesPerSec
	}
	if c.LimitFallbackDownload != nil {
		config.LimitFallbackDownload.AfterBytes = c.LimitFallbackDownload.AfterBytes
		config.LimitFallbackDownload.BytesPerSec = c.LimitFallbackDownload.BytesPerSec
		config.LimitFallbackDownload.BurstBytesPerSec = c.LimitFallbackDownload.BurstBytesPerSec
	}
	config.ServerNames = make(map[string]bool)
	for _, serverName := range c.ServerNames {
		config.ServerNames[serverName] = true
	}
	config.ShortIds = make(map[[8]byte]bool)
	for _, shortId := range c.ShortIds {
		config.ShortIds[*(*[8]byte)(shortId)] = true
	}
	config.KeyLogWriter = KeyLogWriterFromConfig(c)
	if w, ok := config.KeyLogWriter.(*keyLogWriterWrapper); ok {
		runtime.SetFinalizer(config, func(cfg *reality.Config) {
			w.release()
		})
	}
	config.CompileServerNamePatterns()
	return config
}

var (
	globalKeyLogCacheMu  sync.Mutex
	globalKeyLogCacheSeq uint64
	globalKeyLogCache    = make(map[string]*keyLogWriterWrapper)
)

type keyLogWriterWrapper struct {
	sync.Mutex
	file     *os.File
	refCount int
	path     string
}

func (w *keyLogWriterWrapper) Write(p []byte) (n int, err error) {
	w.Lock()
	defer w.Unlock()
	if w.file == nil {
		return 0, os.ErrClosed
	}
	return w.file.Write(p)
}

func (w *keyLogWriterWrapper) addRef() {
	w.Lock()
	w.refCount++
	w.Unlock()
}

func (w *keyLogWriterWrapper) release() {
	w.Lock()
	w.refCount--
	shouldClose := w.refCount <= 0
	if shouldClose {
		if w.file != nil {
			w.file.Close()
			w.file = nil
		}
	}
	w.Unlock()

	if shouldClose {
		globalKeyLogCacheMu.Lock()
		if globalKeyLogCache[w.path] == w {
			delete(globalKeyLogCache, w.path)
		}
		globalKeyLogCacheMu.Unlock()
	}
}

func getKeyLogWriter(path string) (*keyLogWriterWrapper, error) {
	globalKeyLogCacheMu.Lock()
	defer globalKeyLogCacheMu.Unlock()

	if w, ok := globalKeyLogCache[path]; ok {
		w.addRef()
		return w, nil
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}

	w := &keyLogWriterWrapper{
		file:     file,
		refCount: 1,
		path:     path,
	}
	globalKeyLogCache[path] = w
	return w, nil
}

func KeyLogWriterFromConfig(c *Config) io.Writer {
	if len(c.MasterKeyLog) <= 0 || c.MasterKeyLog == "none" {
		return nil
	}

	writer, err := getKeyLogWriter(c.MasterKeyLog)
	if err != nil {
		errors.LogErrorInner(context.Background(), err, "failed to open ", c.MasterKeyLog, " as master key log")
		return nil
	}

	return writer
}

func ConfigFromStreamSettings(settings *internet.MemoryStreamConfig) *Config {
	if settings == nil {
		return nil
	}
	config, ok := settings.SecuritySettings.(*Config)
	if !ok {
		return nil
	}
	return config
}
