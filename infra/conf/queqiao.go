package conf

import (
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/proxy/queqiao"
	"google.golang.org/protobuf/proto"
)

type QueqiaoDeviceConfig struct {
	DeviceID  string `json:"deviceId"`
	PublicKey string `json:"publicKey"`
}

func (c *QueqiaoDeviceConfig) Build() (*queqiao.Device, error) {
	return &queqiao.Device{
		DeviceId:  c.DeviceID,
		PublicKey: c.PublicKey,
	}, nil
}

type QueqiaoAccountConfig struct {
	AccountID string                 `json:"accountId"`
	Devices   []*QueqiaoDeviceConfig `json:"devices"`
}

func (c *QueqiaoAccountConfig) Build() (*queqiao.Account, error) {
	acc := &queqiao.Account{
		AccountId: c.AccountID,
		Devices:   make([]*queqiao.Device, 0, len(c.Devices)),
	}
	for _, d := range c.Devices {
		dev, err := d.Build()
		if err != nil {
			return nil, err
		}
		acc.Devices = append(acc.Devices, dev)
	}
	return acc, nil
}

type QueqiaoServerConfig struct {
	ProviderID          string                  `json:"providerId"`
	GatewayID           string                  `json:"gatewayId"`
	AllowPrivate        bool                    `json:"allowPrivate"`
	Accounts            []*QueqiaoAccountConfig `json:"accounts"`
	RootPin             string                  `json:"rootPin"`
	RootCertificate     string                  `json:"rootCertificate"`
	RootCertificatePath string                  `json:"rootCertificatePath"`
	Certificate         string                  `json:"certificate"`
	CertificatePath     string                  `json:"certificatePath"`
	PrivateKey          string                  `json:"privateKey"`
	PrivateKeyPath      string                  `json:"privateKeyPath"`
	RootPrivateKey      string                  `json:"rootPrivateKey"`
	RootPrivateKeyPath  string                  `json:"rootPrivateKeyPath"`
	HopPortCount        int32                   `json:"hopPortCount"`
	Congestion          string                  `json:"congestion"`
	MaxSessions         int32                   `json:"maxSessions"`
	FlowIdleTimeout     int32                   `json:"flowIdleTimeout"`
}

func (c *QueqiaoServerConfig) Build() (proto.Message, error) {
	config := &queqiao.ServerConfig{
		ProviderId:          c.ProviderID,
		GatewayId:           c.GatewayID,
		AllowPrivate:        c.AllowPrivate,
		RootPin:             c.RootPin,
		RootCertificate:     c.RootCertificate,
		RootCertificatePath: c.RootCertificatePath,
		Certificate:         c.Certificate,
		CertificatePath:     c.CertificatePath,
		PrivateKey:          c.PrivateKey,
		PrivateKeyPath:      c.PrivateKeyPath,
		RootPrivateKey:      c.RootPrivateKey,
		RootPrivateKeyPath:  c.RootPrivateKeyPath,
		HopPortCount:        c.HopPortCount,
		Congestion:          c.Congestion,
		MaxSessions:         c.MaxSessions,
		FlowIdleTimeout:     c.FlowIdleTimeout,
	}
	for _, a := range c.Accounts {
		acc, err := a.Build()
		if err != nil {
			return nil, err
		}
		config.Accounts = append(config.Accounts, acc)
	}
	return config, nil
}

type QueqiaoClientConfig struct {
	Address               *Address `json:"address"`
	Port                  uint16   `json:"port"`
	ProviderID            string   `json:"providerId"`
	GatewayID             string   `json:"gatewayId"`
	AccountID             string   `json:"accountId"`
	DeviceID              string   `json:"deviceId"`
	DeviceName            string   `json:"deviceName"`
	SNI                   string   `json:"sni"`
	RootPin               string   `json:"rootPin"`
	RootCertificate       string   `json:"rootCertificate"`
	RootCertificatePath   string   `json:"rootCertificatePath"`
	DeviceCertificate     string   `json:"deviceCertificate"`
	DeviceCertificatePath string   `json:"deviceCertificatePath"`
	DevicePrivateKey      string   `json:"devicePrivateKey"`
	DevicePrivateKeyPath  string   `json:"devicePrivateKeyPath"`
	HopPortCount          int32    `json:"hopPortCount"`
	CongestionController  string   `json:"congestionController"`
	QuicPool              bool     `json:"quicPool"`
	WaitForOpenAck        bool     `json:"waitForOpenAck"`
	UdpOverStream         bool     `json:"udpOverStream"`
	TcpFallbackLanes      int32    `json:"tcpFallbackLanes"`
	HandshakeTimeout      int32    `json:"handshakeTimeout"`
	FlowIdleTimeout       int32    `json:"flowIdleTimeout"`
	MaxSessions           int32    `json:"maxSessions"`
	ChunkSize             int32    `json:"chunkSize"`
	Transport             string   `json:"transport"`
}

func (c *QueqiaoClientConfig) Build() (proto.Message, error) {
	config := &queqiao.ClientConfig{
		ProviderId:            c.ProviderID,
		GatewayId:             c.GatewayID,
		AccountId:             c.AccountID,
		DeviceId:              c.DeviceID,
		DeviceName:            c.DeviceName,
		Sni:                   c.SNI,
		RootPin:               c.RootPin,
		RootCertificate:       c.RootCertificate,
		RootCertificatePath:   c.RootCertificatePath,
		DeviceCertificate:     c.DeviceCertificate,
		DeviceCertificatePath: c.DeviceCertificatePath,
		DevicePrivateKey:      c.DevicePrivateKey,
		DevicePrivateKeyPath:  c.DevicePrivateKeyPath,
		HopPortCount:          c.HopPortCount,
		CongestionController:  c.CongestionController,
		QuicPool:              c.QuicPool,
		WaitForOpenAck:        c.WaitForOpenAck,
		UdpOverStream:         c.UdpOverStream,
		TcpFallbackLanes:      c.TcpFallbackLanes,
		HandshakeTimeout:      c.HandshakeTimeout,
		FlowIdleTimeout:       c.FlowIdleTimeout,
		MaxSessions:           c.MaxSessions,
		ChunkSize:             c.ChunkSize,
		Transport:             c.Transport,
	}
	if c.Address != nil {
		config.Server = &net.Endpoint{
			Address: c.Address.Build(),
			Port:    uint32(c.Port),
			Network: net.Network_UDP,
		}
	}
	return config, nil
}

type QueqiaoConfig struct {
	HopPortCount     int32  `json:"hopPortCount"`
	Congestion       string `json:"congestion"`
	ChunkSize        int32  `json:"chunkSize"`
	QuicPool         bool   `json:"quicPool"`
	WaitForOpenAck   bool   `json:"waitForOpenAck"`
	UdpOverStream    bool   `json:"udpOverStream"`
	HandshakeTimeout int32  `json:"handshakeTimeout"`
	FlowIdleTimeout  int32  `json:"flowIdleTimeout"`
	MaxSessions      int32  `json:"maxSessions"`
	TcpFallbackLanes int32  `json:"tcpFallbackLanes"`
	Transport        string `json:"transport"`
}

func (c *QueqiaoConfig) Build() (proto.Message, error) {
	return &queqiao.TransportConfig{
		HopPortCount:     c.HopPortCount,
		Congestion:       c.Congestion,
		ChunkSize:        c.ChunkSize,
		QuicPool:         c.QuicPool,
		WaitForOpenAck:   c.WaitForOpenAck,
		UdpOverStream:    c.UdpOverStream,
		HandshakeTimeout: c.HandshakeTimeout,
		FlowIdleTimeout:  c.FlowIdleTimeout,
		MaxSessions:      c.MaxSessions,
		TcpFallbackLanes: c.TcpFallbackLanes,
		Transport:        c.Transport,
	}, nil
}
