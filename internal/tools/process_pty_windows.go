//go:build windows

package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	procThreadAttributePseudoConsole = 0x00020016
	procThreadAttributeJobList       = 0x0002000D
	extendedStartupInfoPresent       = 0x00080000
	ptyRows                          = 24
	ptyCols                          = 120
)

var (
	modKernel32                           = windows.NewLazySystemDLL("kernel32.dll")
	procCreatePseudoConsole               = modKernel32.NewProc("CreatePseudoConsole")
	procClosePseudoConsole                = modKernel32.NewProc("ClosePseudoConsole")
	procInitializeProcThreadAttributeList = modKernel32.NewProc("InitializeProcThreadAttributeList")
	procUpdateProcThreadAttribute         = modKernel32.NewProc("UpdateProcThreadAttribute")
	procDeleteProcThreadAttributeList     = modKernel32.NewProc("DeleteProcThreadAttributeList")
)

type startupInfoEx struct {
	windows.StartupInfo
	AttributeList uintptr
}

type conptyStdin struct {
	file       *os.File
	normalizer windowsTtyInputNormalizer
}

func (stdin *conptyStdin) Write(data []byte) (int, error) {
	normalized := stdin.normalizer.normalize(data)
	if len(normalized) == 0 {
		return len(data), nil
	}
	if _, err := stdin.file.Write(normalized); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (stdin *conptyStdin) Close() error { return nil }

type conptyCloser struct {
	once   sync.Once
	input  *os.File
	output *os.File
	hpcon  windows.Handle
}

func (closer *conptyCloser) Close() error {
	closer.once.Do(func() {
		_, _, _ = procClosePseudoConsole.Call(uintptr(closer.hpcon))
		if closer.input != nil {
			_ = closer.input.Close()
		}
		if closer.output != nil {
			_ = closer.output.Close()
		}
	})
	return nil
}

// AttachPTY starts the sandboxed command on ConPTY, matching Codex spawn_conpty_process_as_user.
func AttachPTY(command *exec.Cmd) (PTYAttach, error) {
	var ptyIn, inWrite, outRead, ptyOut windows.Handle
	if err := windows.CreatePipe(&ptyIn, &inWrite, nil, 0); err != nil {
		return PTYAttach{}, ptyWaitError(fmt.Errorf("create ConPTY input pipe: %w", err))
	}
	if err := windows.CreatePipe(&outRead, &ptyOut, nil, 0); err != nil {
		windows.CloseHandle(ptyIn)
		windows.CloseHandle(inWrite)
		return PTYAttach{}, ptyWaitError(fmt.Errorf("create ConPTY output pipe: %w", err))
	}

	var hpcon windows.Handle
	size := uint32(uint16(ptyCols)) | uint32(uint16(ptyRows))<<16
	if hr, _, err := procCreatePseudoConsole.Call(uintptr(size), uintptr(ptyIn), uintptr(ptyOut), 0, uintptr(unsafe.Pointer(&hpcon))); hr != 0 {
		windows.CloseHandle(ptyIn)
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		windows.CloseHandle(ptyOut)
		if hr != 0 && err != windows.ERROR_SUCCESS {
			return PTYAttach{}, ptyWaitError(fmt.Errorf("CreatePseudoConsole: %w", err))
		}
		return PTYAttach{}, ptyWaitError(fmt.Errorf("CreatePseudoConsole HRESULT 0x%x", hr))
	}
	windows.CloseHandle(ptyIn)
	windows.CloseHandle(ptyOut)

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		_, _, _ = procClosePseudoConsole.Call(uintptr(hpcon))
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		return PTYAttach{}, ptyWaitError(fmt.Errorf("create process job: %w", err))
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		_, _, _ = procClosePseudoConsole.Call(uintptr(hpcon))
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		return PTYAttach{}, ptyWaitError(fmt.Errorf("set job limits: %w", err))
	}

	var attrSize uintptr
	_, _, _ = procInitializeProcThreadAttributeList.Call(0, 2, 0, uintptr(unsafe.Pointer(&attrSize)))
	attrBuf := make([]byte, attrSize)
	if r1, _, err := procInitializeProcThreadAttributeList.Call(uintptr(unsafe.Pointer(&attrBuf[0])), 2, 0, uintptr(unsafe.Pointer(&attrSize))); r1 == 0 {
		windows.CloseHandle(job)
		_, _, _ = procClosePseudoConsole.Call(uintptr(hpcon))
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		return PTYAttach{}, ptyWaitError(fmt.Errorf("InitializeProcThreadAttributeList: %w", err))
	}
	defer procDeleteProcThreadAttributeList.Call(uintptr(unsafe.Pointer(&attrBuf[0])))

	if r1, _, err := procUpdateProcThreadAttribute.Call(
		uintptr(unsafe.Pointer(&attrBuf[0])), 0, procThreadAttributePseudoConsole,
		uintptr(hpcon), unsafe.Sizeof(hpcon), 0, 0,
	); r1 == 0 {
		windows.CloseHandle(job)
		_, _, _ = procClosePseudoConsole.Call(uintptr(hpcon))
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		return PTYAttach{}, ptyWaitError(fmt.Errorf("PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE: %w", err))
	}
	if r1, _, err := procUpdateProcThreadAttribute.Call(
		uintptr(unsafe.Pointer(&attrBuf[0])), 0, procThreadAttributeJobList,
		uintptr(unsafe.Pointer(&job)), unsafe.Sizeof(job), 0, 0,
	); r1 == 0 {
		windows.CloseHandle(job)
		_, _, _ = procClosePseudoConsole.Call(uintptr(hpcon))
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		return PTYAttach{}, ptyWaitError(fmt.Errorf("PROC_THREAD_ATTRIBUTE_JOB_LIST: %w", err))
	}

	cmdline, err := windows.UTF16PtrFromString(windowsCommandLine(command))
	if err != nil {
		windows.CloseHandle(job)
		_, _, _ = procClosePseudoConsole.Call(uintptr(hpcon))
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		return PTYAttach{}, ptyWaitError(err)
	}
	env, err := utf16EnvBlock(command.Env)
	if err != nil {
		windows.CloseHandle(job)
		_, _, _ = procClosePseudoConsole.Call(uintptr(hpcon))
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		return PTYAttach{}, ptyWaitError(err)
	}
	var dir *uint16
	if command.Dir != "" {
		dir, err = windows.UTF16PtrFromString(command.Dir)
		if err != nil {
			windows.CloseHandle(job)
			_, _, _ = procClosePseudoConsole.Call(uintptr(hpcon))
			windows.CloseHandle(inWrite)
			windows.CloseHandle(outRead)
			return PTYAttach{}, ptyWaitError(err)
		}
	}

	var si startupInfoEx
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput = windows.InvalidHandle
	si.StdOutput = windows.InvalidHandle
	si.StdErr = windows.InvalidHandle
	si.AttributeList = uintptr(unsafe.Pointer(&attrBuf[0]))

	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT | extendedStartupInfoPresent)
	token := windows.Token(0)
	if command.SysProcAttr != nil && command.SysProcAttr.Token != 0 {
		token = windows.Token(command.SysProcAttr.Token)
	}
	if err := createProcessWithToken(token, cmdline, flags, &env[0], dir, &si, &pi); err != nil {
		windows.CloseHandle(job)
		_, _, _ = procClosePseudoConsole.Call(uintptr(hpcon))
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		return PTYAttach{}, ptyWaitError(fmt.Errorf("CreateProcess for ConPTY: %w", err))
	}
	windows.CloseHandle(pi.Thread)

	process, err := os.FindProcess(int(pi.ProcessId))
	if err != nil {
		windows.TerminateProcess(pi.Process, 1)
		windows.CloseHandle(pi.Process)
		windows.CloseHandle(job)
		_, _, _ = procClosePseudoConsole.Call(uintptr(hpcon))
		windows.CloseHandle(inWrite)
		windows.CloseHandle(outRead)
		return PTYAttach{}, ptyWaitError(err)
	}
	command.Process = process
	sandboxJobs.Store(int(pi.ProcessId), job)

	inputFile := os.NewFile(uintptr(inWrite), "conpty-input")
	outputFile := os.NewFile(uintptr(outRead), "conpty-output")
	closer := &conptyCloser{input: inputFile, output: outputFile, hpcon: hpcon}
	if ctx, ok := pendingCommandContexts.Load(command); ok {
		go watchCommandContext(ctx.(context.Context), process, closer)
	}

	return PTYAttach{
		Stdin:  &conptyStdin{file: inputFile},
		Stdout: outputFile,
		Closer: closer,
		Wait: func() int {
			defer windows.CloseHandle(pi.Process)
			event, err := windows.WaitForSingleObject(pi.Process, windows.INFINITE)
			if err != nil || event != windows.WAIT_OBJECT_0 {
				return -1
			}
			var code uint32
			if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
				return -1
			}
			return int(code)
		},
	}, nil
}

func watchCommandContext(ctx context.Context, process *os.Process, closer ioCloser) {
	<-ctx.Done()
	if process != nil {
		_ = process.Kill()
	}
	_ = closer.Close()
}

type ioCloser interface{ Close() error }

func windowsCommandLine(command *exec.Cmd) string {
	args := command.Args
	if command.Path != "" {
		if len(args) == 0 {
			args = []string{command.Path}
		} else {
			copied := append([]string{command.Path}, args[1:]...)
			args = copied
		}
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = syscall.EscapeArg(arg)
	}
	return strings.Join(quoted, " ")
}

func utf16EnvBlock(env []string) ([]uint16, error) {
	if len(env) == 0 {
		env = os.Environ()
	}
	var block []uint16
	for _, item := range env {
		encoded, err := windows.UTF16FromString(item)
		if err != nil {
			return nil, err
		}
		block = append(block, encoded...)
	}
	return append(block, 0), nil
}

func createProcessWithToken(
	token windows.Token,
	cmdline *uint16,
	flags uint32,
	env *uint16,
	dir *uint16,
	si *startupInfoEx,
	pi *windows.ProcessInformation,
) error {
	if token != 0 {
		return windows.CreateProcessAsUser(token, nil, cmdline, nil, nil, false, flags, env, dir, &si.StartupInfo, pi)
	}
	return windows.CreateProcess(nil, cmdline, nil, nil, false, flags, env, dir, &si.StartupInfo, pi)
}
