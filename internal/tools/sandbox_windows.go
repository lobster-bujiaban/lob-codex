//go:build windows

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	disableMaxPrivilege = 0x1
	luaToken            = 0x4
	writeRestricted     = 0x8
)

type windowsRestrictedTokenBackend struct{}

func (windowsRestrictedTokenBackend) Name() string { return "windows-restricted-token" }

func (windowsRestrictedTokenBackend) Command(ctx context.Context, policy SandboxPolicy, command string) (*exec.Cmd, error) {
	token, err := createWriteRestrictedToken(policy)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "cmd.exe", "/C", command)
	cmd.Dir = policy.WorkingDirectory
	cmd.Env = append(os.Environ(), "PATH="+execSearchPath(), fmt.Sprintf("CODEX_SANDBOX_NETWORK_DISABLED=%d", boolInt(!policy.NetworkAccess)))
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
		Token:      syscall.Token(token),
	}
	pendingRestrictedTokens.Store(cmd, windows.Handle(token))
	pendingCommandContexts.Store(cmd, ctx)
	return cmd, nil
}

func createWriteRestrictedToken(policy SandboxPolicy) (windows.Token, error) {
	var current windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_ADJUST_DEFAULT, &current); err != nil {
		return 0, fmt.Errorf("open process token: %w", err)
	}
	defer current.Close()

	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		return 0, fmt.Errorf("everyone SID: %w", err)
	}
	logon, err := tokenLogonSID(current)
	if err != nil {
		return 0, fmt.Errorf("logon SID: %w", err)
	}
	restricting := []windows.SIDAndAttributes{
		{Sid: everyone},
		{Sid: logon},
	}
	if policy.WorkspaceWrite {
		capability, err := workspaceCapabilitySID(policy.WorkspaceRoot)
		if err != nil {
			return 0, err
		}
		restricting = append(restricting, windows.SIDAndAttributes{Sid: capability})
		if err := grantSIDWrite(policy.WorkspaceRoot, capability); err != nil {
			return 0, err
		}
		for _, protected := range []string{".git", ".codex"} {
			path := filepath.Join(policy.WorkspaceRoot, protected)
			if _, err := os.Stat(path); err == nil {
				_ = denySIDWrite(path, capability)
			}
		}
	}

	var restricted windows.Token
	err = createRestrictedToken(
		current,
		disableMaxPrivilege|luaToken|writeRestricted,
		restricting,
		&restricted,
	)
	if err != nil {
		return 0, fmt.Errorf("CreateRestrictedToken: %w", err)
	}
	return restricted, nil
}

func tokenLogonSID(token windows.Token) (*windows.SID, error) {
	var needed uint32
	_ = windows.GetTokenInformation(token, windows.TokenLogonSid, nil, 0, &needed)
	if needed == 0 {
		return nil, fmt.Errorf("token logon SID is unavailable")
	}
	buf := make([]byte, needed)
	if err := windows.GetTokenInformation(token, windows.TokenLogonSid, &buf[0], needed, &needed); err != nil {
		return nil, err
	}
	groups := (*windows.Tokengroups)(unsafe.Pointer(&buf[0]))
	all := groups.AllGroups()
	if len(all) == 0 || all[0].Sid == nil {
		return nil, fmt.Errorf("token logon SID is empty")
	}
	return all[0].Sid.Copy()
}

func workspaceCapabilitySID(root string) (*windows.SID, error) {
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	var sid *windows.SID
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		5,
		110,
		binary.LittleEndian.Uint32(sum[0:4]),
		binary.LittleEndian.Uint32(sum[4:8]),
		binary.LittleEndian.Uint32(sum[8:12]),
		binary.LittleEndian.Uint32(sum[12:16]),
		0, 0, 0,
		&sid,
	)
	if err != nil {
		return nil, fmt.Errorf("workspace capability SID: %w", err)
	}
	return sid, nil
}

var (
	modAdvapi32               = windows.NewLazySystemDLL("advapi32.dll")
	procCreateRestrictedToken = modAdvapi32.NewProc("CreateRestrictedToken")
)

func createRestrictedToken(existing windows.Token, flags uint32, sids []windows.SIDAndAttributes, out *windows.Token) error {
	var sidPtr *windows.SIDAndAttributes
	if len(sids) > 0 {
		sidPtr = &sids[0]
	}
	r1, _, err := procCreateRestrictedToken.Call(
		uintptr(existing),
		uintptr(flags),
		0, 0,
		0, 0,
		uintptr(len(sids)),
		uintptr(unsafe.Pointer(sidPtr)),
		uintptr(unsafe.Pointer(out)),
	)
	if r1 == 0 {
		return err
	}
	return nil
}

func grantSIDWrite(path string, sid *windows.SID) error {
	return setSIDAccess(path, sid, windows.GRANT_ACCESS, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE)
}

func denySIDWrite(path string, sid *windows.SID) error {
	return setSIDAccess(path, sid, windows.DENY_ACCESS, windows.FILE_GENERIC_WRITE|windows.DELETE)
}

func setSIDAccess(path string, sid *windows.SID, mode windows.ACCESS_MODE, access windows.ACCESS_MASK) error {
	entries := []windows.EXPLICIT_ACCESS{{
		AccessPermissions: access,
		AccessMode:        mode,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}
	var oldACL *windows.ACL
	if sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION); err == nil {
		oldACL, _, _ = sd.DACL()
	}
	acl, err := windows.ACLFromEntries(entries, oldACL)
	if err != nil {
		return fmt.Errorf("set entries in ACL: %w", err)
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
