//go:build windows

package concurrent

import (
	"os"

	"golang.org/x/sys/windows"
)

// FORK-PATCH: Windows SetFileValidData preallocation with
// SeManageVolumePrivilege elevation. Falls back to chunked zero-fill on failure.

func preallocateFile(file *os.File, size int64) error {
	if size <= 0 {
		return nil
	}

	if err := preallocateWithValidData(file, size); err == nil {
		return nil
	}

	return preallocateZeroFill(file, size)
}

func preallocateWithValidData(file *os.File, size int64) error {
	handle := windows.Handle(file.Fd())

	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()

	var luid windows.LUID
	privName, _ := windows.UTF16PtrFromString("SeManageVolumePrivilege")
	if err := windows.LookupPrivilegeValue(nil, privName, &luid); err != nil {
		return err
	}

	privileges := windows.Tokenprivileges{
		PrivilegeCount: 1,
		Privileges: [1]windows.LUIDAndAttributes{
			{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED},
		},
	}
	if err := windows.AdjustTokenPrivileges(token, false, &privileges, 0, nil, nil); err != nil {
		return err
	}

	// SetFileValidData requires nValidDataLength <= current EOF.
	// The file starts at size 0 (precreateWorkingFile), so extend EOF first
	// via Truncate (SetEndOfFile, sparse-instant), then set valid data length.
	if err := file.Truncate(size); err != nil {
		return err
	}

	if err := windows.SetFileValidData(handle, size); err != nil {
		return err
	}

	// Seek back to start — concurrent path uses WriteAt but seek is harmless
	// and keeps the file pointer consistent across both engine implementations.
	_, err := file.Seek(0, 0)
	return err
}

func preallocateZeroFill(file *os.File, size int64) error {
	const chunkSize = 1024 * 1024
	buf := make([]byte, chunkSize)
	remaining := size
	for remaining > 0 {
		toWrite := int64(chunkSize)
		if remaining < toWrite {
			toWrite = remaining
		}
		if _, err := file.Write(buf[:toWrite]); err != nil {
			return err
		}
		remaining -= toWrite
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	return nil
}
