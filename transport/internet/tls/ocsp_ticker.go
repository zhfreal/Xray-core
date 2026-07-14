package tls

import (
	"context"
	"sync"
	"time"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/platform/filesystem"
)

var globalOcspTickerMap sync.Map // map[string]*ocspTickerState

type ocspTickerState struct {
	sync.Mutex
	callbacks []func(isReloaded, isOcspstapling bool)
}

func setupOcspTicker(entry *Certificate, callback func(isReloaded, isOcspstapling bool)) {
	if entry.OneTimeLoading {
		return
	}

	key := getCertCacheKey(entry)

	stateI, loaded := globalOcspTickerMap.LoadOrStore(key, &ocspTickerState{})
	state := stateI.(*ocspTickerState)

	state.Lock()
	state.callbacks = append(state.callbacks, callback)
	state.Unlock()

	if !loaded {
		go func() {
			var isOcspstapling bool
			hotReloadCertInterval := uint64(3600)
			if entry.OcspStapling != 0 {
				hotReloadCertInterval = entry.OcspStapling
				isOcspstapling = true
			}
			t := time.NewTicker(time.Duration(hotReloadCertInterval) * time.Second)

			currentCertBytes := entry.Certificate
			currentKeyBytes := entry.Key

			for {
				var isReloaded bool
				if entry.CertificatePath != "" && entry.KeyPath != "" {
					nc, err := filesystem.ReadCert(entry.CertificatePath)
					if err != nil {
						errors.LogErrorInner(context.Background(), err, "failed to parse certificate")
						return
					}
					nk, err := filesystem.ReadCert(entry.KeyPath)
					if err != nil {
						errors.LogErrorInner(context.Background(), err, "failed to parse key")
						return
					}
					if string(nc) != string(currentCertBytes) || string(nk) != string(currentKeyBytes) {
						entry.Certificate = nc
						entry.Key = nk
						currentCertBytes = nc
						currentKeyBytes = nk
						isReloaded = true
					}
				}

				state.Lock()
				cbs := make([]func(isReloaded, isOcspstapling bool), len(state.callbacks))
				copy(cbs, state.callbacks)
				state.Unlock()

				for _, cb := range cbs {
					cb(isReloaded, isOcspstapling)
				}
				<-t.C
			}
		}()
	}
}
