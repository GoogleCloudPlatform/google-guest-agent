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

//go:build windows

package agentcrypto

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/GoogleCloudPlatform/galog"
	"github.com/GoogleCloudPlatform/google-guest-agent/internal/utils/file"
	"golang.org/x/sys/windows"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

const (
	// rootCACertFileName is the root CA cert.
	rootCACertFileName = "mds-mtls-root.crt"
	// clientCredsFileName are client credentials, its basically the file
	// that has the EC private key and the client certificate concatenated.
	clientCredsFileName = "mds-mtls-client.key"
	// pfxFile stores client credentials in PFX format.
	pfxFile = "mds-mtls-client.key.pfx"
	// https://learn.microsoft.com/en-us/windows/win32/seccrypto/system-store-locations
	// my is predefined personal cert store.
	my = "MY"
	// root is predefined cert store for root trusted CA certs.
	root = "ROOT"
	// certificateIssuer is the issuer of client/root certificates for MDS mTLS.
	certificateIssuer = "google.internal"
	// maxCertEnumeration specifies the maximum number of times to search for a
	// certificate with a serial number from a given issuer before giving up.
	maxCertEnumeration = 5
)

var (
	// defaultCredsDir is the directory location for MTLS MDS credentials.
	defaultCredsDir = filepath.Join(os.Getenv("ProgramData"), "Google", "Compute Engine")
	prevCtx         *windows.CertContext
)

// writeRootCACert writes Root CA cert from UEFI variable to output file.
func (j *CredsJob) writeRootCACert(ctx context.Context, cacert []byte, outputFile string) error {
	galog.Debugf("Writing root CA cert to %q", outputFile)

	// Try to fetch previous certificate's serial number before it gets
	// overwritten.
	num, err := serialNumber(outputFile)
	if err != nil {
		galog.Debugf("No previous MDS root certificate was found, will skip cleanup: %v", err)
	}

	if err := file.SaferWriteFile(ctx, cacert, outputFile, file.Options{Perm: 0644}); err != nil {
		return err
	}
	galog.Debugf("Successfully wrote root CA cert to %q", outputFile)

	if !j.useNativeStore {
		galog.Debugf("Skipping system store update as it is disabled in the configuration")
		return nil
	}

	x509Cert, err := parseCertificate(cacert)
	if err != nil {
		return fmt.Errorf("failed to parse root CA cert: %w", err)
	}

	// https://learn.microsoft.com/en-us/windows/win32/api/wincrypt/nf-wincrypt-certcreatecertificatecontext
	galog.V(3).Debug("Creating certificate context for root CA cert")
	certContext, err := windows.CertCreateCertificateContext(
		windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING,
		&x509Cert.Raw[0],
		uint32(len(x509Cert.Raw)))
	if err != nil {
		return fmt.Errorf("CertCreateCertificateContext returned: %w", err)
	}
	galog.V(3).Debug("Successfully created certificate context for root CA cert")
	// https://learn.microsoft.com/en-us/windows/win32/api/wincrypt/nf-wincrypt-certfreecertificatecontext
	defer windows.CertFreeCertificateContext(certContext)

	// Adds certificate to Root Trusted certificates.
	if err := addCtxToLocalSystemStore(root, certContext, uint32(windows.CERT_STORE_ADD_REPLACE_EXISTING)); err != nil {
		return fmt.Errorf("failed to store root cert ctx in store: %w", err)
	}

	// MDS root cert was not refreshed or there's no previous cert, nothing to do,
	// return.
	if num == "" || fmt.Sprintf("%x", x509Cert.SerialNumber) == num {
		return nil
	}

	// Certificate is refreshed. Best effort to find the cert context and delete
	// it. Don't throw error here, it would skip client credential generation
	// which may be about to expire.
	oldCtx, err := findCert(root, certificateIssuer, num)
	if err != nil {
		galog.Warnf("Failed to find previous MDS root certificate with error: %v", err)
		return nil
	}

	if err := deleteCert(oldCtx, root); err != nil {
		galog.Warnf("Failed to delete previous MDS root certificate(%s) with error: %v", num, err)
		return nil
	}

	return nil
}

// findCert finds and returns certificate issued by issuer with the serial
// number in the given the store.
func findCert(storeName, issuer, certID string) (*windows.CertContext, error) {
	galog.Debugf("Searching for certificate with serial number %s in store %s by issuer %s", certID, storeName, issuer)

	st, err := windows.CertOpenStore(
		windows.CERT_STORE_PROV_SYSTEM,
		0,
		0,
		windows.CERT_SYSTEM_STORE_LOCAL_MACHINE,
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(storeName))))
	if err != nil {
		return nil, fmt.Errorf("failed to open cert store: %w", err)
	}
	defer windows.CertCloseStore(st, 0)

	// prev is used for enumerating through all the certificates that matches the
	// issuer. On the first call to the function this parameter is NULL. On all
	// subsequent calls, this parameter is the last CertContext pointer returned
	// by the CertFindCertificateInStore function.
	var prev *windows.CertContext

	// maxCertEnumeration would avoid requiring a infinite loop that relies on
	// enumerating until we get nil crt.
	for i := 1; i <= maxCertEnumeration; i++ {
		galog.Debugf("Attempt %d, searching certificate...", i)

		// https://learn.microsoft.com/en-us/windows/win32/api/wincrypt/nf-wincrypt-certfindcertificateinstore
		crt, err := windows.CertFindCertificateInStore(
			st,
			windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING,
			0,
			windows.CERT_FIND_ISSUER_STR,
			unsafe.Pointer(syscall.StringToUTF16Ptr(issuer)),
			prev)
		if err != nil {
			return nil, fmt.Errorf("unable to find certificate: %w", err)
		}
		if crt == nil {
			return nil, fmt.Errorf("no certificate by issuer %s with ID %s", issuer, certID)
		}

		x509Cert, err := certContextToX509(crt)
		if err != nil {
			return nil, fmt.Errorf("failed to parse certificate context: %w", err)
		}

		if fmt.Sprintf("%x", x509Cert.SerialNumber) == certID {
			galog.Debugf("Found certificate with serial number %s in store %s by issuer %s", certID, storeName, issuer)
			return crt, nil
		}

		prev = crt
	}

	return nil, nil
}

// writeClientCredentials stores client credentials (certificate and private
// key).
func (j *CredsJob) writeClientCredentials(ctx context.Context, creds []byte, outputFile string) error {
	galog.Debugf("Writing client credentials to %q", outputFile)

	num, err := serialNumber(outputFile)
	if err != nil {
		galog.Warnf("Could not get previous serial number, will skip cleanup: %v", err)
	}

	if err := file.SaferWriteFile(ctx, creds, outputFile, file.Options{Perm: 0644}); err != nil {
		return fmt.Errorf("failed to write client key: %w", err)
	}
	galog.Debugf("Successfully wrote client credentials to %q", outputFile)

	galog.V(1).Debug("Generating PFX data from client credentials")
	pfx, err := generatePFX(creds)
	if err != nil {
		return fmt.Errorf("failed to generate PFX data from client credentials: %w", err)
	}
	galog.V(1).Debug("Successfully generated PFX data from client credentials")

	galog.V(1).Debugf("Writing PFX file to %q", pfxFile)
	p := filepath.Join(filepath.Dir(outputFile), pfxFile)
	if err := file.SaferWriteFile(ctx, pfx, p, file.Options{Perm: 0644}); err != nil {
		return fmt.Errorf("failed to write PFX file: %w", err)
	}
	galog.V(1).Debugf("Successfully wrote PFX file to %q", p)

	if !j.useNativeStore {
		galog.Info("Skipping client credentials write to system store update as it is disabled in the configuration")
		return nil
	}

	galog.Debugf("Writing client credentials to system store")
	blob := windows.CryptDataBlob{
		Size: uint32(len(pfx)),
		Data: &pfx[0],
	}

	// https://learn.microsoft.com/en-us/windows/win32/api/wincrypt/nf-wincrypt-pfximportcertstore
	galog.V(1).Debug("Importing PFX into cert store")
	handle, err := windows.PFXImportCertStore(&blob, syscall.StringToUTF16Ptr(""), windows.CRYPT_MACHINE_KEYSET)
	if err != nil {
		return fmt.Errorf("failed to import PFX in cert store: %w", err)
	}
	galog.V(1).Debug("Successfully imported PFX into cert store")
	defer windows.CertCloseStore(handle, 0)

	var crtCtx *windows.CertContext

	// https://learn.microsoft.com/en-us/windows/win32/api/wincrypt/nf-wincrypt-certenumcertificatesinstore
	galog.V(1).Debug("Fetching cert context for PFX from store")
	crtCtx, err = windows.CertEnumCertificatesInStore(handle, crtCtx)
	if err != nil {
		return fmt.Errorf("failed to get cert context for PFX from store: %w", err)
	}
	galog.V(1).Debug("Successfully fetched cert context for PFX from store")
	defer windows.CertFreeCertificateContext(crtCtx)

	// Add certificate to personal store.
	galog.V(1).Debug("Adding PFX cert to local system store")
	if err := addCtxToLocalSystemStore(my, crtCtx, uint32(windows.CERT_STORE_ADD_NEWER)); err != nil {
		return fmt.Errorf("failed to store pfx cert context: %w", err)
	}
	galog.V(1).Debug("Successfully added PFX cert to local system store")

	// Search for previous certificate if its not already in memory.
	if prevCtx == nil && num != "" {
		prevCtx, err = findCert(my, certificateIssuer, num)
		if err != nil {
			galog.Warnf("Failed to find previous certificate with error: %v", err)
		}
	}

	// Remove previous certificate only after successful refresh.
	galog.V(1).Debugf("Deleting previous certificate %v from local system store", prevCtx)
	if err := deleteCert(prevCtx, my); err != nil {
		galog.Warnf("Failed to delete previous certificate(%s) with error: %v", num, err)
	} else {
		galog.V(1).Debugf("Successfully deleted previous certificate %v from local system store", prevCtx)
	}

	prevCtx = windows.CertDuplicateCertificateContext(crtCtx)
	galog.Debug("Successfully wrote certificate to system store")

	return nil
}

// certContextToX509 creates an x509 Certificate from a Windows cert context.
func certContextToX509(ctx *windows.CertContext) (*x509.Certificate, error) {
	der := unsafe.Slice(ctx.EncodedCert, int(ctx.Length))
	return x509.ParseCertificate(der)
}

// generatePFX accepts certificate concatenated with private key and generates a
// PFX out of it.
// https://learn.microsoft.com/en-us/windows-hardware/drivers/install/personal-information-exchange---pfx--files
func generatePFX(creds []byte) (pfxData []byte, err error) {
	cert, key := pem.Decode(creds)
	x509Cert, err := x509.ParseCertificate(cert.Bytes)
	if err != nil {
		return []byte{}, fmt.Errorf("failed to parse client certificate: %w", err)
	}

	ecpvt, err := parsePvtKey(key)
	if err != nil {
		return []byte{}, fmt.Errorf("failed to parse EC PrivateKey from client credentials: %w", err)
	}

	return pkcs12.Encode(rand.Reader, ecpvt, x509Cert, nil, "")
}

// addCtxToLocalSystemStore adds the certificate context to the local system
// store.
func addCtxToLocalSystemStore(storeName string, certContext *windows.CertContext, disposition uint32) error {
	// https://learn.microsoft.com/en-us/windows/win32/api/wincrypt/nf-wincrypt-certopenstore
	// https://learn.microsoft.com/en-us/windows-hardware/drivers/install/local-machine-and-current-user-certificate-stores
	// https://learn.microsoft.com/en-us/windows/win32/seccrypto/system-store-locations#cert_system_store_local_machine
	galog.V(2).Debugf("Adding certificate context(%v) to store %s", certContext, storeName)
	st, err := windows.CertOpenStore(
		windows.CERT_STORE_PROV_SYSTEM,
		0,
		0,
		windows.CERT_SYSTEM_STORE_LOCAL_MACHINE,
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(storeName))))
	if err != nil {
		return fmt.Errorf("failed to open cert store: %w", err)
	}
	// https://learn.microsoft.com/en-us/windows/win32/api/wincrypt/nf-wincrypt-certclosestore
	defer windows.CertCloseStore(st, 0)

	// https://learn.microsoft.com/en-us/windows/win32/api/wincrypt/nf-wincrypt-certaddcertificatecontexttostore
	if err := windows.CertAddCertificateContextToStore(st, certContext, disposition, nil); err != nil {
		return fmt.Errorf("failed to add certificate context to store: %w", err)
	}

	galog.V(2).Debugf("Successfully added certificate context(%v) to store %s", certContext, storeName)
	return nil
}

// deleteCert deletes the certificate from the given store.
func deleteCert(crtCtx *windows.CertContext, storeName string) error {
	if crtCtx == nil {
		return nil
	}

	galog.V(2).Debugf("Deleting certificate context(%v) from store %s", crtCtx, storeName)
	st, err := windows.CertOpenStore(
		windows.CERT_STORE_PROV_SYSTEM,
		0,
		0,
		windows.CERT_SYSTEM_STORE_LOCAL_MACHINE,
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(storeName))))
	if err != nil {
		return fmt.Errorf("failed to open cert store: %w", err)
	}
	defer windows.CertCloseStore(st, 0)

	galog.V(3).Debugf("Finding certificate context(%v) in store %s", crtCtx, storeName)
	var dlCtx *windows.CertContext
	dlCtx, err = windows.CertFindCertificateInStore(
		st,
		windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING,
		0,
		windows.CERT_FIND_EXISTING,
		unsafe.Pointer(crtCtx),
		dlCtx,
	)
	if err != nil {
		return fmt.Errorf("unable to find the certificate in %q store to delete: %w", storeName, err)
	}
	galog.V(3).Debugf("Successfully found certificate context(%v) in store %s", crtCtx, storeName)

	galog.V(3).Debugf("Deleting certificate context(%v) from store %s", crtCtx, storeName)
	err = windows.CertDeleteCertificateFromStore(dlCtx)
	if err == nil {
		galog.V(2).Debugf("Successfully deleted certificate context(%v) from store %s", crtCtx, storeName)
	}
	return err
}
