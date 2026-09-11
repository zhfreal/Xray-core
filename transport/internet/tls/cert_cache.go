package tls

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"sync"

	"github.com/xtls/xray-core/common/errors"
)

var globalCertCache sync.Map // map[string]*tls.Certificate

func getCertCacheKey(entry *Certificate) string {
	if entry.CertificatePath != "" && entry.KeyPath != "" {
		return "path|" + entry.CertificatePath + "|" + entry.KeyPath
	}
	buf := make([]byte, len(entry.Certificate)+len(entry.Key))
	copy(buf, entry.Certificate)
	copy(buf[len(entry.Certificate):], entry.Key)
	hash := sha256.Sum256(buf)
	return "bytes|" + hex.EncodeToString(hash[:])
}

func getX509KeyPair(certBytes, keyBytes []byte) *tls.Certificate {
	keyPair, err := tls.X509KeyPair(certBytes, keyBytes)
	if err != nil {
		errors.LogWarningInner(context.Background(), err, "ignoring invalid X509 key pair")
		return nil
	}
	keyPair.Leaf, err = x509.ParseCertificate(keyPair.Certificate[0])
	if err != nil {
		errors.LogWarningInner(context.Background(), err, "ignoring invalid certificate")
		return nil
	}
	return &keyPair
}
