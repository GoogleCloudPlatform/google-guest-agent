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

// Package ssh includes Google SSH related Utilities for Google Guest Agent and Google Authorized Keys.
package ssh

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/galog"
	"golang.org/x/crypto/ssh"
)

// sshExpiration represents the google-ssh key comment structure with expiry and user name.
// It is used to parse JSON and identify if key is expired.
type sshExpiration struct {
	ExpireOn string
	UserName string
}

var (
	// ErrExpiredKey is an error type for expired keys.
	ErrExpiredKey = errors.New("invalid ssh key entry - expired key")
)

// CheckExpiredKey validates whether a key has expired.
// Keys with invalid expiration formats will result in an error.
func CheckExpiredKey(key string) error {
	trimmedKey := strings.Trim(key, " ")
	if trimmedKey == "" {
		return errors.New("invalid ssh key entry - empty key")
	}
	_, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(trimmedKey))
	if err != nil {
		return err
	}
	if !strings.HasPrefix(comment, "google-ssh") {
		// Non-expiring key.
		return nil
	}
	fields := strings.SplitN(comment, " ", 2)
	if len(fields) < 2 {
		// expiring key without expiration format.
		return errors.New("invalid ssh key entry - expiration missing")
	}
	lkey := &sshExpiration{}
	if err := json.Unmarshal([]byte(fields[1]), lkey); err != nil {
		// invalid expiration format.
		return err
	}
	expired, err := CheckExpired(lkey.ExpireOn)
	if err != nil {
		return err
	}
	if expired {
		return ErrExpiredKey
	}
	return nil
}

// CheckExpired takes a time string and determines if it represents a time in the past.
func CheckExpired(expireOn string) (bool, error) {
	t, err := time.Parse(time.RFC3339, expireOn)
	if err != nil {
		t2, err2 := time.Parse("2006-01-02T15:04:05-0700", expireOn)
		if err2 != nil {
			// Return original RFC3339 parsing error.
			return true, err
		}
		t = t2
	}
	return t.Before(time.Now()), nil

}

// ValidateUser checks for the presence of a characters which should not be
// allowed in a username string, returns an error if any such characters are
// detected, nil otherwise.
// Currently, the only banned characters are whitespace characters.
func ValidateUser(user string) error {
	if user == "" {
		return errors.New("invalid username - it is empty")
	}

	whiteSpaceRegexp, err := regexp.Compile(`\s`)
	if err != nil {
		return fmt.Errorf("whitespace regex failed to compile, err: %w", err)
	}

	if whiteSpaceRegexp.MatchString(user) {
		return errors.New("invalid username - whitespace detected")
	}
	return nil
}

// GetUserKey returns a user and a SSH key if a rawKey has a correct format, nil otherwise.
// It doesn't validate entries.
func GetUserKey(rawKey string) (string, string, error) {
	key := strings.Trim(rawKey, " ")
	if key == "" {
		return "", "", errors.New("invalid ssh key entry - empty key")
	}
	idx := strings.Index(key, ":")
	if idx == -1 {
		return "", "", errors.New("invalid ssh key entry - unrecognized format. Expecting user:ssh-key")
	}
	user := key[:idx]
	if user == "" {
		return "", "", errors.New("invalid ssh key entry - user missing")
	}
	if key[idx+1:] == "" {
		return "", "", errors.New("invalid ssh key entry - key missing")
	}

	return user, key[idx+1:], nil
}

// ValidateUserKey takes a user and a key received from GetUserKey() and
// validate the user for special characters and the key for expiration
func ValidateUserKey(user, key string) error {
	if err := ValidateUser(user); err != nil {
		return err
	}
	if err := CheckExpiredKey(key); err != nil {
		return err
	}

	return nil
}

// ParseKeys extracts the user's ssh key from the gce's format.
func ParseKeys(username string, keys []string) []string {
	var keyList []string

	for _, key := range keys {
		keySplit := strings.SplitN(key, ":", 2)
		if len(keySplit) != 2 {
			continue
		}

		user, keyVal, err := GetUserKey(key)
		if err != nil {
			galog.Debugf("Failed to get user key: %v", err)
			continue
		}

		if err := ValidateUserKey(user, keyVal); err != nil {
			galog.Debugf("Failed to validate user key: %v", err)
			continue
		}

		if user == username {
			keyList = append(keyList, keyVal)
		}
	}

	return keyList
}
