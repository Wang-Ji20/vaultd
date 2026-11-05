package upstream

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x2000
)

// These mirror JOBOBJECT_EXTENDED_LIMIT_INFORMATION from the Windows API.
type basicLimitInformation struct {
	perProcessUserTimeLimit int64
	perJobUserTimeLimit     int64
	limitFlags              uint32
	minimumWorkingSetSize   uintptr
	maximumWorkingSetSize   uintptr
	activeProcessLimit      uint32
	affinity                uintptr
	priorityClass           uint32
	schedulingClass         uint32
}

type ioCounters struct {
	readOperationCount, writeOperationCount, otherOperationCount uint64
	readTransferCount, writeTransferCount, otherTransferCount    uint64
}

type extendedLimitInformation struct {
	basic                 basicLimitInformation
	io                    ioCounters
	processMemoryLimit    uintptr
	jobMemoryLimit        uintptr
	peakProcessMemoryUsed uintptr
	peakJobMemoryUsed     uintptr
}

var (
	jobOnce sync.Once
	jobErr  error
	// jobHandle stays open for the life of the process. Windows closes it when
	// vaultd exits for any reason, even a crash, and that kills the children.
	jobHandle uintptr
)

// killChildrenWithParent puts vaultd itself in a job object that kills its
// members when closed. Children inherit the job from the moment they start, so
// no child can slip out between starting and being assigned.
func killChildrenWithParent() error {
	jobOnce.Do(func() { jobErr = joinKillOnCloseJob() })
	return jobErr
}

func joinKillOnCloseJob() error {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	handle, _, err := kernel32.NewProc("CreateJobObjectW").Call(0, 0)
	if handle == 0 {
		return fmt.Errorf("create job object: %w", err)
	}
	info := extendedLimitInformation{basic: basicLimitInformation{limitFlags: jobObjectLimitKillOnJobClose}}
	ok, _, err := kernel32.NewProc("SetInformationJobObject").Call(
		handle, jobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	runtime.KeepAlive(&info)
	if ok == 0 {
		return fmt.Errorf("configure job object: %w", err)
	}
	self, _, _ := kernel32.NewProc("GetCurrentProcess").Call()
	ok, _, err = kernel32.NewProc("AssignProcessToJobObject").Call(handle, self)
	if ok == 0 {
		return fmt.Errorf("join job object: %w", err)
	}
	jobHandle = handle
	return nil
}
