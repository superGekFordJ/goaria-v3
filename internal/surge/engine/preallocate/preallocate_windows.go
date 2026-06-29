//go:build windows

// FORK-PATCH: Windows SetFileValidData preallocation with
// SeManageVolumePrivilege elevation. Falls back to chunked zero-fill on failure.

package preallocate

import (
	"os"

	"golang.org/x/sys/windows"
)

func preallocate(file *os.File, size int64) error {
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

	// AdjustTokenPrivileges may return nil even when the privilege was not
	// actually assigned (ERROR_NOT_ALL_ASSIGNED). Detect this edge case so
	// the caller falls back to zero-fill instead of proceeding with an
	// unprivileged SetFileValidData that will fail with access denied.
	if lastErr := windows.GetLastError(); lastErr == windows.ERROR_NOT_ALL_ASSIGNED {
		return lastErr
	}

	// SetFileValidData requires nValidDataLength <= current EOF.
	// Extend EOF first via Truncate (SetEndOfFile, sparse-instant), then set
	// valid data length.
	if err := file.Truncate(size); err != nil {
		return err
	}

	if err := windows.SetFileValidData(handle, size); err != nil {
		return err
	}

	// Truncate moves the file pointer to EOF; seek back so the downloader
	// writes from offset 0.
	_, err := file.Seek(0, 0)
	return err
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
