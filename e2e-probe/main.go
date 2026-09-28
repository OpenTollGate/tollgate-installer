// e2e-probe: does an unconstrained Go SSH client negotiate RSA when the server
// offers BOTH ed25519 and rsa? That is exactly the tollgate-installer defect:
// ssh.go's ssh.ClientConfig sets no HostKeyAlgorithms, while every operator-facing
// verification path (dropbearkey on the router console, ssh-keyscan -t ed25519)
// reports the ED25519 key.
//
// Run: go run ./e2e-probe 127.0.0.1:2222
package main

import (
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
)

func probe(addr string, algos []string) (string, string, error) {
	var got ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User: "root",
		Auth: []ssh.AuthMethod{ssh.Password("not-a-real-password")},
		HostKeyCallback: func(h string, r net.Addr, k ssh.PublicKey) error {
			got = k
			return fmt.Errorf("captured") // abort: we only want the key
		},
		Timeout: 5 * time.Second,
	}
	if len(algos) > 0 {
		cfg.HostKeyAlgorithms = algos
	}
	// The handshake is expected to fail at our callback; that is the capture.
	_, _ = ssh.Dial("tcp", addr, cfg)
	if got == nil {
		return "", "", fmt.Errorf("no host key presented")
	}
	return got.Type(), ssh.FingerprintSHA256(got), nil
}

func main() {
	addr := "127.0.0.1:2222"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}

	fmt.Println("server:", addr)

	unconstrainedType, unconstrainedFP, err := probe(addr, nil)
	if err != nil {
		fmt.Println("UNCONSTRAINED: error:", err)
	} else {
		fmt.Printf("UNCONSTRAINED (current installer behaviour): type=%s fp=%s\n", unconstrainedType, unconstrainedFP)
	}

	constrainedType, constrainedFP, err := probe(addr, []string{ssh.KeyAlgoED25519})
	if err != nil {
		fmt.Println("ED25519-CONSTRAINED: error:", err)
	} else {
		fmt.Printf("ED25519-CONSTRAINED (the fix):                type=%s fp=%s\n", constrainedType, constrainedFP)
	}

	if unconstrainedType != "" && unconstrainedType != constrainedType {
		fmt.Println("REPRODUCED: unconstrained negotiation picked a DIFFERENT key type than the ed25519-only path.")
		fmt.Println("=> a pin/store entry captured as ed25519 can never match the unconstrained result.")
	} else if unconstrainedType == constrainedType && unconstrainedType != "" {
		fmt.Println("NOT REPRODUCED on this fixture: both paths picked", unconstrainedType)
	}
}
