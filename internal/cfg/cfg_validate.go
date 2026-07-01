//  Copyright 2026 Google Inc. All Rights Reserved.
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

package cfg

import (
	"fmt"

	"github.com/GoogleCloudPlatform/galog"
)

// validateMWLID performs semantic validation on the MWLID configuration section.
func validateMWLID(sections *Sections) error {
	if sections.MWLID == nil || !sections.MWLID.Enabled {
		return nil
	}

	if sections.MWLID.CredentialRefreshMinutes < 0 {
		galog.Errorf("MWLID credential refresh minutes must be > 0, got %d", sections.MWLID.CredentialRefreshMinutes)
		return fmt.Errorf("invalid configuration: [MWLID] credential_refresh_minutes must be > 0, got %d", sections.MWLID.CredentialRefreshMinutes)
	}

	if sections.MWLID.CredentialRefreshMinutes == 0 {
		galog.Infof("MWLID credential refresh minutes is set to 0, using default value of 10 minutes")
		sections.MWLID.CredentialRefreshMinutes = 10
	}

	return nil
}

// validateSections runs all semantic validations against the loaded configuration.
func validateSections(sections *Sections) error {
	if err := validateMWLID(sections); err != nil {
		return err
	}

	return nil
}
