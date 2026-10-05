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

// Package winacl checks the owners and discretionary access control lists
// (DACLs) of Windows files.
//
// The extension runs as LocalSystem on Windows, so the files that it trusts
// must only be modifiable by principals that can already control the system:
// LocalSystem, the Administrators group, TrustedInstaller, and the user that the
// process runs as. The checks are platform independent so that they can be
// tested on any platform, but reading and setting the security of files is only
// supported on Windows.
package winacl

import (
	"errors"
	"fmt"
)

// Access rights from the Windows SDK. For directories, writeData is the right
// to add files and appendData is the right to add subdirectories.
const (
	writeData       = 0x2        // FILE_WRITE_DATA
	appendData      = 0x4        // FILE_APPEND_DATA
	writeEA         = 0x10       // FILE_WRITE_EA
	deleteChild     = 0x40       // FILE_DELETE_CHILD
	writeAttributes = 0x100      // FILE_WRITE_ATTRIBUTES
	deleteAccess    = 0x10000    // DELETE
	writeDAC        = 0x40000    // WRITE_DAC
	writeOwner      = 0x80000    // WRITE_OWNER
	genericAll      = 0x10000000 // GENERIC_ALL
	genericWrite    = 0x40000000 // GENERIC_WRITE
)

const (
	// Replace is the set of access rights that let a principal delete or rename
	// a file or directory, delete or rename the entries of a directory, or grant
	// itself other rights.
	Replace uint32 = deleteChild | deleteAccess | writeDAC | writeOwner | genericAll
	// Modify is the set of access rights that let a principal change a file or
	// add entries to a directory, including the Replace rights.
	Modify uint32 = Replace | writeData | appendData | writeEA | writeAttributes | genericWrite
)

// ACE types and flags from the Windows SDK.
const (
	accessAllowedType              = 0x0 // ACCESS_ALLOWED_ACE_TYPE
	accessDeniedType               = 0x1 // ACCESS_DENIED_ACE_TYPE
	accessDeniedObjectType         = 0x6 // ACCESS_DENIED_OBJECT_ACE_TYPE
	accessAllowedCallbackType      = 0x9 // ACCESS_ALLOWED_CALLBACK_ACE_TYPE
	accessDeniedCallbackType       = 0xA // ACCESS_DENIED_CALLBACK_ACE_TYPE
	accessDeniedCallbackObjectType = 0xC // ACCESS_DENIED_CALLBACK_OBJECT_ACE_TYPE

	// inheritOnly marks an ACE that only applies to the objects that inherit it.
	inheritOnly = 0x8 // INHERIT_ONLY_ACE
)

// Well-known SIDs.
const (
	localSystem      = "S-1-5-18"
	administrators   = "S-1-5-32-544"
	trustedInstaller = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
	// ownerRights stands for the owner of the file in an ACE.
	ownerRights = "S-1-3-4"
)

// ACE is an access control entry of a DACL.
type ACE struct {
	Type  uint8  // The ACE type, such as ACCESS_ALLOWED_ACE_TYPE.
	Flags uint8  // The ACE flags, such as INHERIT_ONLY_ACE.
	Mask  uint32 // The access rights that the ACE allows or denies.
	// SID is the SID, in string form, of the principal that the ACE applies to.
	// It is only set for the access-allowed ACE types that store a SID at a known
	// offset: ACCESS_ALLOWED_ACE_TYPE and ACCESS_ALLOWED_CALLBACK_ACE_TYPE.
	SID string
}

// Security is the owner and DACL of a file.
type Security struct {
	Owner string // The owner's SID in string form.
	// NullDACL is true if the file has no DACL, which allows everyone all
	// access.
	NullDACL bool
	DACL     []ACE
}

// CheckOwner returns an error unless the file is owned by LocalSystem, the
// Administrators group, TrustedInstaller or the user that the process runs as.
// The owner of a file can change its DACL.
func (s Security) CheckOwner() error {
	return s.checkOwner(currentUserSID())
}

// Check returns an error unless the file has an owner that CheckOwner accepts
// and its DACL only allows the principals that CheckOwner accepts any of the
// given access rights.
func (s Security) Check(rights uint32) error {
	return s.check(rights, currentUserSID())
}

func (s Security) checkOwner(currentUser string) error {
	if !isTrusted(s.Owner, currentUser) {
		return fmt.Errorf("owned by %q, want LocalSystem, Administrators, TrustedInstaller or %q", s.Owner, currentUser)
	}
	return nil
}

func (s Security) check(rights uint32, currentUser string) error {
	if err := s.checkOwner(currentUser); err != nil {
		return err
	}
	if s.NullDACL {
		return errors.New("has no DACL, which allows everyone all access")
	}
	for _, ace := range s.DACL {
		if ace.Flags&inheritOnly != 0 || ace.Mask&rights == 0 {
			continue
		}
		switch ace.Type {
		case accessDeniedType, accessDeniedObjectType, accessDeniedCallbackType, accessDeniedCallbackObjectType:
			// Denying access can't let anyone modify the file.
		case accessAllowedType, accessAllowedCallbackType:
			// The owner is trusted, so OWNER RIGHTS is too.
			if ace.SID != ownerRights && !isTrusted(ace.SID, currentUser) {
				return fmt.Errorf("allows %q access %#x", ace.SID, ace.Mask)
			}
		default:
			return fmt.Errorf("has an unsupported ACE of type %#x with access %#x", ace.Type, ace.Mask)
		}
	}
	return nil
}

// isTrusted reports whether sid is LocalSystem, the Administrators group,
// TrustedInstaller, or currentUser.
func isTrusted(sid, currentUser string) bool {
	switch sid {
	case localSystem, administrators, trustedInstaller:
		return true
	}
	return sid != "" && sid == currentUser
}
