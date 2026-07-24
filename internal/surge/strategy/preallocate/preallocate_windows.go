//go:build windows

// FORK-PATCH: Windows SetFileValidData preallocation with
// SeManageVolumePrivilege elevation. Falls back to sparse file + Truncate on
// privilege failure (eliminates non-admin startup latency); degrades to
// chunked zero-fill only when FSCTL_SET_SPARSE is unsupported.

package preallocate

import (
	"os"

	"golang.org/x/sys/windows"
)

func preallocate(file *os.File, size int64) error {
	// Fast path: SetFileValidData with SeManageVolumePrivilege elevation.
	// Physically allocates contiguous space (no fragmentation). Requires admin;
	// fails naturally with ERROR_ACCESS_DENIED when the privilege is absent.
	if err := preallocateWithValidData(file, size); err == nil {
		return nil
	}

	// Fallback: sparse file + Truncate. Sparse unallocated regions read as 0,
	// and random WriteAt to the file end does NOT trigger NTFS synchronous
	// zero-fill of preceding unwritten space — avoiding the concurrent I/O
	// stall that plain Truncate on a non-sparse file causes. Trade-off: the
	// non-admin path produces disk fragmentation (the standard Windows platform
	// compromise to eliminate UX startup latency).
	if err := preallocateSparse(file, size); err == nil {
		return nil
	}

	// Tertiary fallback: chunked zero-fill, only when FSCTL_SET_SPARSE is
	// unsupported (non-NTFS/FAT32/exFAT/network shares). Preserves
	// no-fragmentation but retains the startup latency.
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

	// Note: AdjustTokenPrivileges may return nil even when the privilege was
	// not assigned (ERROR_NOT_ALL_ASSIGNED). GetLastError cannot detect this
	// because Go's syscall wrapper clears LastErrorValue before every syscall,
	// so it always reads ERROR_SUCCESS here. Rely on SetFileValidData failing
	// naturally with ERROR_ACCESS_DENIED to trigger the non-admin fallback chain.
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

// preallocateSparse marks the file sparse via FSCTL_SET_SPARSE, then extends
// EOF via Truncate. Sparse unallocated regions read as 0 without physical
// allocation, and random WriteAt to the file end does not trigger NTFS
// synchronous zero-fill of preceding unwritten space.
func preallocateSparse(file *os.File, size int64) error {
	handle := windows.Handle(file.Fd())
	// FSCTL_SET_SPARSE is idempotent on an already-sparse file. On a file with
	// existing written data (resume), already-allocated regions stay allocated;
	// only the gap between current EOF and the new Truncate size becomes sparse.
	var bytesReturned uint32
	if err := windows.DeviceIoControl(
		handle,
		windows.FSCTL_SET_SPARSE,
		nil, 0,
		nil, 0,
		&bytesReturned,
		nil,
	); err != nil {
		return err
	}
	if err := file.Truncate(size); err != nil {
		return err
	}
	// Truncate moves the file pointer to EOF; seek back so the downloader
	// writes from offset 0.
	_, err := file.Seek(0, 0)
	return err
}

// preallocateZeroFill writes zeroed 1MB buffers to physically allocate the file.
func preallocateZeroFill(file *os.File, size int64) error {
	// Seek to start first: a failed SetFileValidData fallback may leave the
	// pointer at EOF after Truncate, which would double the file size.
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
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
