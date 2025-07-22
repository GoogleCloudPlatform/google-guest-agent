//  Copyright 2024 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package agentcrypto

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/google/tink/go/aead/subtle"
)

// parseCertificate validates certificate is in valid PEM format.
func parseCertificate(cert []byte) (*x509.Certificate, error) {
	galog.V(2).Debug("Parsing PEM certificate")
	block, _ := pem.Decode(cert)
	if block == nil {
		return nil, fmt.Errorf("failed to parse PEM certificate")
	}
	galog.V(2).Debug("Successfully parsed PEM certificate")

	galog.V(2).Debug("Parsing X509 certificate")
	x509Cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}
	galog.V(2).Debug("Successfully parsed X509 certificate")

	return x509Cert, nil
}

// parsePvtKey validates the key is in valid format and returns the EC Private
// Key.
func parsePvtKey(pemKey []byte) (*ecdsa.PrivateKey, error) {
	galog.V(3).Debug("Parsing PEM private key")
	key, _ := pem.Decode(pemKey)
	if key == nil {
		return nil, fmt.Errorf("failed to decode PEM Key")
	}
	galog.V(3).Debug("Successfully parsed PEM private key")

	galog.V(3).Debug("Parsing EC Private Key")
	ecKey, err := x509.ParseECPrivateKey(key.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse EC Private Key: %w", err)
	}
	galog.V(3).Debug("Successfully parsed EC Private Key")

	return ecKey, nil
}

// serialNumber reads the certificate from file and returns the serial number in
// hex.
func serialNumber(f string) (string, error) {
	galog.V(1).Debugf("Reading serial number from certificate %q", f)

	galog.V(2).Debugf("Reading certificate from %q", f)
	d, err := os.ReadFile(f)
	if err != nil {
		return "", fmt.Errorf("unable to read previous client credential file %q: %w", f, err)
	}
	galog.V(2).Debugf("Successfully read certificate from %q", f)

	galog.V(2).Debugf("Parsing certificate from file %q", f)
	crt, err := parseCertificate(d)
	if err != nil {
		return "", fmt.Errorf("unable to parse certificate at %q: %w", f, err)
	}
	galog.V(2).Debugf("Successfully parsed certificate from file %q", f)

	galog.V(1).Debugf("Successfully read serial number from certificate %q", f)
	return fmt.Sprintf("%x", crt.SerialNumber), nil
}

// verifySign verifies the client certificate is valid and signed by root CA.
func verifySign(cert []byte, rootCAFile string) error {
	galog.V(2).Debugf("Verifying client certificate against root CA %q", rootCAFile)

	galog.V(3).Debugf("Reading CA PEM file(%q) for verifying signature", rootCAFile)
	caCertPEM, err := os.ReadFile(rootCAFile)
	if err != nil {
		return fmt.Errorf("failed to read CA PEM file for verifying signature: %w", err)
	}
	galog.V(3).Debugf("Successfully read CA PEM file(%q) for verifying signature", rootCAFile)

	x509Cert, err := parseCertificate(cert)
	if err != nil {
		return fmt.Errorf("failed to parse client certificate for verifying signature: %w", err)
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caCertPEM) {
		return fmt.Errorf("failed to add %q to new certpool for verifying client certificate", rootCAFile)
	}

	opts := x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	galog.V(3).Debugf("Verifying client certificate with opts %+v", opts)
	if _, err := x509Cert.Verify(opts); err != nil {
		return fmt.Errorf("failed to verify client certificate against root CA %q: %w", rootCAFile, err)
	}
	galog.V(2).Debug("Successfully verified client certificate")

	return nil
}

// encrypt plaintext using AES-GCM.
func encrypt(aesKey []byte, plaintext []byte, associatedData []byte) ([]byte, error) {
	cipher, err := subtle.NewAESGCM(aesKey)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize cipher: %w", err)
	}
	return cipher.Encrypt(plaintext, associatedData)
}

// decrypt ciphertext using AES-GCM.
func decrypt(aesKey []byte, ciphertext []byte, associatedData []byte) ([]byte, error) {
	cipher, err := subtle.NewAESGCM(aesKey)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize cipher: %w", err)
	}
	return cipher.Decrypt(ciphertext, associatedData)
}
