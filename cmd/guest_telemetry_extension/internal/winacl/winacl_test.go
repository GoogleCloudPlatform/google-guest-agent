/*
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package winacl

import "testing"

const (
	testUser           = "S-1-5-21-1111111111-2222222222-3333333333-1001"
	everyone           = "S-1-1-0"
	authenticatedUsers = "S-1-5-11"
	users              = "S-1-5-32-545"

	fileRead   = 0x1200a9 // FILE_GENERIC_READ | FILE_GENERIC_EXECUTE
	fileModify = 0x1301bf // FILE_GENERIC_READ | FILE_GENERIC_WRITE | FILE_GENERIC_EXECUTE | DELETE
	fileAll    = 0x1f01ff // FILE_ALL_ACCESS
)

// allow returns an ACCESS_ALLOWED_ACE_TYPE ACE.
func allow(sid string, mask uint32) ACE {
	return ACE{Type: accessAllowedType, Mask: mask, SID: sid}
}

// secured returns the security of a file owned by testUser with the given ACEs.
func secured(aces ...ACE) Security {
	return Security{Owner: testUser, DACL: aces}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name    string
		sec     Security
		rights  uint32
		wantErr bool
	}{
		{
			name:   "private file",
			sec:    secured(allow(localSystem, fileAll), allow(administrators, fileAll), allow(testUser, fileAll)),
			rights: Modify,
		},
		{
			name:   "system file readable by everyone",
			sec:    Security{Owner: trustedInstaller, DACL: []ACE{allow(trustedInstaller, fileAll), allow(everyone, fileRead)}},
			rights: Modify,
		},
		{
			name:   "owned by LocalSystem",
			sec:    Security{Owner: localSystem},
			rights: Modify,
		},
		{
			name:   "owned by Administrators",
			sec:    Security{Owner: administrators},
			rights: Modify,
		},
		{
			name:    "owned by another user",
			sec:     Security{Owner: users},
			rights:  Modify,
			wantErr: true,
		},
		{
			name:    "unknown owner",
			sec:     Security{},
			rights:  Modify,
			wantErr: true,
		},
		{
			name:    "no DACL",
			sec:     Security{Owner: testUser, NullDACL: true},
			rights:  Modify,
			wantErr: true,
		},
		{
			name:    "writable by everyone",
			sec:     secured(allow(everyone, writeData)),
			rights:  Modify,
			wantErr: true,
		},
		{
			name:   "directory that everyone can add files to",
			sec:    secured(allow(everyone, writeData|appendData)),
			rights: Replace,
		},
		{
			name:    "directory that users can delete",
			sec:     secured(allow(users, deleteAccess)),
			rights:  Replace,
			wantErr: true,
		},
		{
			name:    "directory that users can delete entries of",
			sec:     secured(allow(users, deleteChild)),
			rights:  Replace,
			wantErr: true,
		},
		{
			name:    "users can change the DACL",
			sec:     secured(allow(users, writeDAC)),
			rights:  Replace,
			wantErr: true,
		},
		{
			name:    "users can take ownership",
			sec:     secured(allow(users, writeOwner)),
			rights:  Replace,
			wantErr: true,
		},
		{
			name:    "generic all access",
			sec:     secured(allow(users, genericAll)),
			rights:  Replace,
			wantErr: true,
		},
		{
			name:    "generic write access",
			sec:     secured(allow(users, genericWrite)),
			rights:  Modify,
			wantErr: true,
		},
		{
			name:   "generic write access to a directory above",
			sec:    secured(allow(users, genericWrite)),
			rights: Replace,
		},
		{
			name:   "inherit-only entry",
			sec:    secured(ACE{Type: accessAllowedType, Flags: 0xb, Mask: fileModify, SID: authenticatedUsers}),
			rights: Modify,
		},
		{
			name:    "inherited entry",
			sec:     secured(ACE{Type: accessAllowedType, Flags: 0x10, Mask: fileModify, SID: authenticatedUsers}),
			rights:  Modify,
			wantErr: true,
		},
		{
			name:   "denied entries",
			sec:    secured(ACE{Type: accessDeniedType, Mask: fileAll, SID: everyone}, ACE{Type: accessDeniedObjectType, Mask: fileAll}),
			rights: Modify,
		},
		{
			name:    "callback entry for another user",
			sec:     secured(ACE{Type: accessAllowedCallbackType, Mask: fileModify, SID: users}),
			rights:  Modify,
			wantErr: true,
		},
		{
			name:   "callback entry for a trusted principal",
			sec:    secured(ACE{Type: accessAllowedCallbackType, Mask: fileModify, SID: localSystem}),
			rights: Modify,
		},
		{
			name:   "owner rights",
			sec:    secured(allow(ownerRights, fileAll)),
			rights: Modify,
		},
		{
			name:    "unsupported entry that may allow writing",
			sec:     secured(ACE{Type: 0x5, Mask: writeData}),
			rights:  Modify,
			wantErr: true,
		},
		{
			name:   "unsupported entry that allows reading",
			sec:    secured(ACE{Type: 0x5, Mask: fileRead}),
			rights: Modify,
		},
		{
			name:    "entry with an unknown SID",
			sec:     secured(allow("", writeData)),
			rights:  Modify,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.sec.check(tc.rights, testUser); (err != nil) != tc.wantErr {
				t.Errorf("%+v.check(%#x, %q) returned error %v, want error: %t", tc.sec, tc.rights, testUser, err, tc.wantErr)
			}
		})
	}
}

func TestCheckOwner(t *testing.T) {
	tests := []struct {
		owner       string
		currentUser string
		wantErr     bool
	}{
		{owner: localSystem},
		{owner: administrators},
		{owner: trustedInstaller},
		{owner: testUser, currentUser: testUser},
		{owner: testUser, currentUser: localSystem, wantErr: true},
		{owner: users, currentUser: testUser, wantErr: true},
		{owner: ownerRights, currentUser: testUser, wantErr: true},
		{owner: "", currentUser: "", wantErr: true},
	}
	for _, tc := range tests {
		s := Security{Owner: tc.owner}
		if err := s.checkOwner(tc.currentUser); (err != nil) != tc.wantErr {
			t.Errorf("Security{Owner: %q}.checkOwner(%q) returned error %v, want error: %t", tc.owner, tc.currentUser, err, tc.wantErr)
		}
	}
}

// TestExportedChecks checks that CheckOwner and Check apply the checks, with
// owners and ACEs whose trust doesn't depend on the user that the test runs as.
func TestExportedChecks(t *testing.T) {
	tests := []struct {
		name         string
		sec          Security
		wantOwnerErr bool
		wantCheckErr bool
	}{
		{
			name: "private file owned by LocalSystem",
			sec:  Security{Owner: localSystem, DACL: []ACE{allow(localSystem, fileAll), allow(users, fileRead)}},
		},
		{
			name:         "writable by users",
			sec:          Security{Owner: localSystem, DACL: []ACE{allow(users, writeData)}},
			wantCheckErr: true,
		},
		{
			name:         "owned by users",
			sec:          Security{Owner: users},
			wantOwnerErr: true,
			wantCheckErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.sec.CheckOwner(); (err != nil) != tc.wantOwnerErr {
				t.Errorf("%+v.CheckOwner() returned error %v, want error: %t", tc.sec, err, tc.wantOwnerErr)
			}
			if err := tc.sec.Check(Modify); (err != nil) != tc.wantCheckErr {
				t.Errorf("%+v.Check(Modify) returned error %v, want error: %t", tc.sec, err, tc.wantCheckErr)
			}
		})
	}
}
