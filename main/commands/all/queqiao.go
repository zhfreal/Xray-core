package all

import (
	"flag"
	"fmt"
	"github.com/xtls/xray-core/main/commands/base"
	"github.com/zhfreal/queqiao/configgen"
)

var cmdQueqiao = &base.Command{
	UsageLine:   `{{.Exec}} queqiao [-ca] [-server] [-client] [-provider <id>] [-gateway <id>] [-device <id>] [doctor] [enroll]`,
	Short:       `Generate cryptographic keypairs, enroll devices, and run diagnostics for Queqiao`,
	CustomFlags: true,
	Long: `
Generate cryptographic keypairs, enroll devices, or run network path diagnostics for Queqiao in clean key: value format.

Full bundle:         {{.Exec}} queqiao [-provider <id>] [-gateway <id>] [-device <id>]
Root CA only:        {{.Exec}} queqiao -ca [-provider <id>]
Gateway server cert: {{.Exec}} queqiao -server -ca-cert "ca.crt" -ca-key "ca.key" [-provider <id>] [-gateway <id>]
Device client cert:  {{.Exec}} queqiao -client -ca-cert "ca.crt" -ca-key "ca.key" -user "alice" [-provider <id>] [-device <id>]
Device enrollment:   {{.Exec}} queqiao enroll -invite "queqiao://enroll/..." [-device "my-laptop"]
Path diagnostics:    {{.Exec}} queqiao doctor -server "10.0.0.4:443" -sni "qq.zhfreal.top"
`,
}

func init() {
	cmdQueqiao.Run = executeQueqiao
}

func executeQueqiao(cmd *base.Command, args []string) {
	if len(args) > 0 && args[0] == "doctor" {
		configgen.RunDoctorCLI(args[1:])
		return
	}
	if len(args) > 0 && args[0] == "enroll" {
		configgen.RunEnrollCLI(args[1:])
		return
	}

	fs := flag.NewFlagSet("queqiao", flag.ContinueOnError)
	ca := fs.Bool("ca", false, "Generate CA keypair only")
	server := fs.Bool("server", false, "Generate Gateway keypair only")
	client := fs.Bool("client", false, "Generate Device keypair only")
	caCert := fs.String("ca-cert", "", "Path or PEM of CA certificate")
	caKey := fs.String("ca-key", "", "Path or PEM of CA private key")
	user := fs.String("user", "default", "User/Account identifier")
	provider := fs.String("provider", "provider.example", "Provider identifier")
	gateway := fs.String("gateway", "default", "Gateway identifier")
	device := fs.String("device", "dev-01", "Device identifier")
	if err := fs.Parse(args); err != nil {
		return
	}

	if *server && *caCert != "" && *caKey != "" {
		svrBundle, err := configgen.GenerateServerCert(*caCert, *caKey, *provider, *gateway)
		if err != nil {
			fmt.Println("Error generating gateway cert:", err)
			return
		}
		fmt.Printf("GatewayCertificate: %s\n", svrBundle.GatewayCertPEM)
		fmt.Printf("GatewayPrivateKey: %s\n", svrBundle.GatewayKeyPEM)
		return
	}
	if *client && *caCert != "" && *caKey != "" {
		cliBundle, err := configgen.GenerateClientCert(*caCert, *caKey, *user, *provider, *device)
		if err != nil {
			fmt.Println("Error generating client cert:", err)
			return
		}
		fmt.Printf("DeviceCertificate: %s\n", cliBundle.DeviceCertPEM)
		fmt.Printf("DevicePrivateKey: %s\n", cliBundle.DeviceKeyPEM)
		fmt.Printf("DevicePublicKey: %s\n", cliBundle.DevicePubKey)
		return
	}

	bundle, err := configgen.GenerateBundle(*provider, *gateway, *device, *user)
	if err != nil {
		fmt.Println("Error generating queqiao bundle:", err)
		return
	}
	if *ca {
		fmt.Printf("RootPin: %s\n", bundle.RootPin)
		fmt.Printf("RootCertificate: %s\n", bundle.RootCertPEM)
		fmt.Printf("RootPrivateKey: %s\n", bundle.RootKeyPEM)
		return
	}
	// Output strictly in xray x25519 format (Key: Value lines):
	fmt.Printf("RootPin: %s\n", bundle.RootPin)
	fmt.Printf("RootCertificate: %s\n", bundle.RootCertPEM)
	fmt.Printf("RootPrivateKey: %s\n", bundle.RootKeyPEM)
	fmt.Printf("GatewayCertificate: %s\n", bundle.GatewayCertPEM)
	fmt.Printf("GatewayPrivateKey: %s\n", bundle.GatewayKeyPEM)
	fmt.Printf("DeviceCertificate: %s\n", bundle.DeviceCertPEM)
	fmt.Printf("DevicePrivateKey: %s\n", bundle.DeviceKeyPEM)
	fmt.Printf("DevicePublicKey: %s\n", bundle.DevicePubKey)
}

var cmdQueqiaoDoctor = &base.Command{
	UsageLine:   `{{.Exec}} queqiao doctor -server <address> [-sni <sni>]`,
	Short:       `Probe Queqiao path latency, packet loss, and mTLS handshake`,
	CustomFlags: true,
	Run: func(cmd *base.Command, args []string) {
		configgen.RunDoctorCLI(args)
	},
}

var cmdQueqiaoEnroll = &base.Command{
	UsageLine:   `{{.Exec}} queqiao enroll -invite <uri> [-device <name>]`,
	Short:       `Enroll device with a Queqiao invitation token`,
	CustomFlags: true,
	Run: func(cmd *base.Command, args []string) {
		configgen.RunEnrollCLI(args)
	},
}
