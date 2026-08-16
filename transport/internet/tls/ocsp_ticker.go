package tls

import (
	"context"
	"crypto/tls"
	"sync"
	"time"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/platform/filesystem"
)

var (
	globalCallbackID    uint64
	globalOcspMapMutex  sync.Mutex
	globalOcspTickerMap = make(map[string]*ocspTickerState)
)

type ocspTickerState struct {
	sync.Mutex
	callbacks map[uint64]func(isReloaded, isOcspstapling bool, cert *tls.Certificate)
	stopChan  chan struct{}
}

func setupOcspTicker(entry *Certificate, callback func(isReloaded, isOcspstapling bool, cert *tls.Certificate)) func() {
	if entry.OneTimeLoading {
		return func() {}
	}

	key := getCertCacheKey(entry)

	globalOcspMapMutex.Lock()
	state, ok := globalOcspTickerMap[key]
	if !ok {
		state = &ocspTickerState{
			callbacks: make(map[uint64]func(isReloaded, isOcspstapling bool, cert *tls.Certificate)),
			stopChan:  make(chan struct{}),
		}
		globalOcspTickerMap[key] = state
	}

	state.Lock()
	globalCallbackID++
	id := globalCallbackID
	state.callbacks[id] = callback
	state.Unlock()
	globalOcspMapMutex.Unlock()

	if !ok {
		go func() {
			var isOcspstapling bool
			hotReloadCertInterval := uint64(3600)
			if entry.OcspStapling != 0 {
				hotReloadCertInterval = entry.OcspStapling
				isOcspstapling = true
			}
			t := time.NewTicker(time.Duration(hotReloadCertInterval) * time.Second)
			defer t.Stop()

			currentCertBytes := entry.Certificate
			currentKeyBytes := entry.Key

			for {
				select {
				case <-state.stopChan:
					return
				case <-t.C:
				}

				var isReloaded bool
				if entry.CertificatePath != "" && entry.KeyPath != "" {
					nc, err := filesystem.ReadCert(entry.CertificatePath)
					if err != nil {
						errors.LogErrorInner(context.Background(), err, "failed to parse certificate")
						continue
					}
					nk, err := filesystem.ReadCert(entry.KeyPath)
					if err != nil {
						errors.LogErrorInner(context.Background(), err, "failed to parse key")
						continue
					}
					if string(nc) != string(currentCertBytes) || string(nk) != string(currentKeyBytes) {
						newCert := getX509KeyPair(nc, nk)
						if newCert == nil {
							errors.LogWarning(context.Background(), "failed to parse updated keypair")
							continue
						}
						entry.Certificate = nc
						entry.Key = nk
						currentCertBytes = nc
						currentKeyBytes = nk
						isReloaded = true
						globalCertCache.Store(key, newCert)
					}
				}

				state.Lock()
				cbs := make([]func(isReloaded, isOcspstapling bool, cert *tls.Certificate), 0, len(state.callbacks))
				for _, cb := range state.callbacks {
					cbs = append(cbs, cb)
				}
				state.Unlock()

				var currentCert *tls.Certificate
				if val, ok := globalCertCache.Load(key); ok {
					currentCert = val.(*tls.Certificate)
				}

				for _, cb := range cbs {
					cb(isReloaded, isOcspstapling, currentCert)
				}
			}
		}()
	}

	return func() {
		globalOcspMapMutex.Lock()
		defer globalOcspMapMutex.Unlock()

		state.Lock()
		delete(state.callbacks, id)
		empty := len(state.callbacks) == 0
		state.Unlock()

		if empty {
			close(state.stopChan)
			delete(globalOcspTickerMap, key)
		}
	}
}
