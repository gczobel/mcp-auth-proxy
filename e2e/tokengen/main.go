// Command tokengen mints a proxy-valid Bearer token for the compose e2e smoke
// test. The proxy validates JWTs with iss and aud both equal to --external-url
// and the signing key it stores at {data-path}/private_key.pem (auto-generated
// at startup unless JWT_PRIVATE_KEY is set). The smoke test reads that key back
// out of the running container, so this tool signs a token the proxy will
// accept without going through an interactive OAuth flow.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	keyPath := flag.String("key", "", "path to the proxy's PEM private key (PKCS8)")
	issuer := flag.String("iss", "http://localhost:8080/", "external URL the proxy runs at (iss + aud); must match the proxy's normalized external URL (trailing slash)")
	flag.Parse()

	if *keyPath == "" {
		fmt.Fprintln(os.Stderr, "tokengen: -key is required")
		os.Exit(2)
	}

	pemBytes, err := os.ReadFile(*keyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tokengen: read key: %v\n", err)
		os.Exit(1)
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(pemBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tokengen: parse key: %v\n", err)
		os.Exit(1)
	}

	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": *issuer,
		"aud": *issuer,
		"sub": "e2e-smoke",
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString(key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tokengen: sign: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(signed)
}
