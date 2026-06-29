//go:build windows

package single

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

	// Try SetFileValidData with SeManageVolumePrivilege elevation
	if err := preallocateWithValidData(file, size); err == nil {
		return nil
	}

	// Fallback: chunked zero-fill
	return preallocateZeroFill(file, size)
}

// preallocateWithValidData elevates SeManageVolumePrivilege and calls SetFileValidData.
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

	return windows.SetFileValidData(handle, size)
}

// preallocateZeroFill writes zeroed 1MB buffers to physically allocate the file.
func preallocateZeroFill(file *os.File, size int64) error {
	const chunkSize = 1024 * 1024 // 1MB
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
	// Seek back to start so the download writes from the beginning
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	return nil
}
