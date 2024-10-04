//  Copyright 2022 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package ssh

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"slices"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestGetUserKey(t *testing.T) {
	pubKey := MakeRandRSAPubKey(t)

	table := []struct {
		key    string
		user   string
		keyVal string
		haserr bool
	}{
		{fmt.Sprintf(`usera:ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":"2095-04-23T12:34:56+0000"}`, pubKey), "usera", fmt.Sprintf(`ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":"2095-04-23T12:34:56+0000"}`, pubKey), false},
		{fmt.Sprintf(`usera:restrict,pty ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":"2095-04-23T12:34:56+0000"}`, pubKey), "usera", fmt.Sprintf(`restrict,pty ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":"2095-04-23T12:34:56+0000"}`, pubKey), false},
		{"    ", "", "", true},
		{fmt.Sprintf("ssh-rsa %s", pubKey), "", "", true},
		{fmt.Sprintf(":ssh-rsa %s", pubKey), "", "", true},
		{"userb:", "", "", true},
		{fmt.Sprintf("userc:ssh-rsa %s info text", pubKey), "userc", fmt.Sprintf("ssh-rsa %s info text", pubKey), false},
	}

	for _, tt := range table {
		u, k, err := GetUserKey(tt.key)
		e := err != nil
		if u != tt.user || k != tt.keyVal || e != tt.haserr {
			t.Errorf("GetUserKey(%s) incorrect return: got user: %s, key: %s, error: %v - want user: %s, key: %s, error: %v", tt.key, u, k, e, tt.user, tt.keyVal, tt.haserr)
		}
	}
}

func TestValidateUserKey(t *testing.T) {
	pubKey := MakeRandRSAPubKey(t)

	table := []struct {
		user   string
		key    string
		haserr bool
	}{
		{"usera", fmt.Sprintf(`ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":"2095-04-23T12:34:56+0000"}`, pubKey), false},
		{"user a", fmt.Sprintf(`ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":"2095-04-23T12:34:56+0000"}`, pubKey), true},
		{"usera", fmt.Sprintf(`ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":"2021-04-23T12:34:56+0000"}`, pubKey), true},
		{"usera", fmt.Sprintf(`ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":"Apri 4, 2056"}`, pubKey), true},
		{"usera", fmt.Sprintf(`ssh-rsa %s google-ssh`, pubKey), true},
		{"usera", fmt.Sprintf(`ssh-rsa %s test info`, pubKey), false},
		{"", fmt.Sprintf("ssh-rsa %s", pubKey), true},
		// Ignore safetext/shsprintf linter suggestion.
		{"usera", fmt.Sprintf(`command="echo hi" ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":"2095-04-23T12:34:56+0000"}`, pubKey), false},
		{"usera", fmt.Sprintf(`command="echo hi" ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":"2021-04-23T12:34:56+0000"}`, pubKey), true},
		{"usera", fmt.Sprintf(`restrict,pty ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":"2095-04-23T12:34:56+0000"}`, pubKey), false},
		{"    ", "", true},
		{"userb", "", true},
	}

	for _, tt := range table {
		err := ValidateUserKey(tt.user, tt.key)
		e := err != nil
		if e != tt.haserr {
			t.Errorf("ValidateUserKey(%s, %s) incorrect return: expected: %t - got: %t", tt.user, tt.key, tt.haserr, e)
		}
	}
}

func TestCheckExpiredKey(t *testing.T) {
	pubKey := MakeRandRSAPubKey(t)

	table := []struct {
		key     string
		expired bool
	}{
		{fmt.Sprintf(`usera:ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":"2095-04-23T12:34:56+0000"}`, pubKey), false},
		{fmt.Sprintf(`usera:ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":"2021-04-23T12:34:56+0000"}`, pubKey), true},
		{fmt.Sprintf(`usera:ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":"Apri 4, 2056"}`, pubKey), true},
		{fmt.Sprintf(`usera:ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":1234}`, pubKey), true},
		{fmt.Sprintf(`usera:ssh-rsa %s google-ssh`, pubKey), true},
		{"    ", true},
		{fmt.Sprintf("ssh-rsa %s", pubKey), false},
		{fmt.Sprintf(":ssh-rsa %s", pubKey), false},
		{fmt.Sprintf("usera:ssh-rsa %s", pubKey), false},
	}

	for _, tt := range table {
		err := CheckExpiredKey(tt.key)
		isExpired := err != nil
		if isExpired != tt.expired {
			t.Errorf("CheckExpiredKey(%s) incorrect return: expired: %t - want expired: %t, got err: %v", tt.key, isExpired, tt.expired, err)
		}
	}
}

func TestValidateUser(t *testing.T) {
	table := []struct {
		user  string
		valid bool
	}{
		{"username", true},
		{"username:key", true},
		{"user -g", false},
		{"user -g 27", false},
		{"user\t-g", false},
		{"user\n-g", false},
		{"username\t-g\n27", false},
	}
	for _, tt := range table {
		err := ValidateUser(tt.user)
		isValid := err == nil
		if isValid != tt.valid {
			t.Errorf("ValidateUser(%s) incorrect return: expected: %t - got: %t", tt.user, tt.valid, isValid)
		}
	}
}

// MakeRandRSAPubKey generates base64 encoded 2048 bit RSA public key for use in tests.
func MakeRandRSAPubKey(t *testing.T) string {
	t.Helper()
	prv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("error generating RSA key: %v", err)
	}
	sshPublic, err := ssh.NewPublicKey(prv.Public())
	if err != nil {
		t.Fatalf("error wrapping ssh public key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(sshPublic.Marshal())
}

func TestParseSSHKeys(t *testing.T) {
	pubKeyA := MakeRandRSAPubKey(t)
	pubKeyB := MakeRandRSAPubKey(t)
	pubKey := MakeRandRSAPubKey(t)

	keys := []string{
		"# Here is some random data in the file.",
		fmt.Sprint("invalid:"),
		fmt.Sprintf("usera:ssh-rsa %s", pubKeyA),
		fmt.Sprintf("userb:ssh-rsa %s", pubKeyB),
		fmt.Sprintf(`usera:ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":"2095-04-23T12:34:56+0000"}`, pubKey),
		fmt.Sprintf(`usera:ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":"2020-04-23T12:34:56+0000"}`, pubKey),
	}

	expected := []string{
		fmt.Sprintf("ssh-rsa %s", pubKeyA),
		fmt.Sprintf(`ssh-rsa %s google-ssh {"userName":"usera@example.com","expireOn":"2095-04-23T12:34:56+0000"}`, pubKey),
	}

	user := "usera"

	if got, want := ParseKeys(user, keys), expected; !slices.Equal(got, want) {
		t.Errorf("ParseSSHKeys(%s,%s) incorrect return: got %v, want %v", user, keys, got, want)
	}

}
