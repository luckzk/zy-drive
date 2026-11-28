// SPDX-License-Identifier: MPL-2.0
package mount

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

// driver.go — FUSE driver detection and mount-error classification.
//
// Detection method (not a fabricated path list): on macOS the
// installed filesystems live under /Library/Filesystems, so the
// check scans that directory for a *fuse* entry and classifies
// by name (macfuse.fs / osxfuse.fs → macFUSE, fuse-t.fs →
// FUSE-T). The exact FUSE-T install layout has NOT been verified
// on the user's Mac — if the scan misses a real install, the
// mount attempt below still runs and its error is classified
// here, so a missed detection degrades to a clear message, not
// a silent failure. On Linux the check is /dev/fuse.
//
// ZyDrive never installs a driver itself (admin password +
// system-level install): the UI shows the guidance and the user
// installs it.

// driverInstallGuidance is the human text shown when no FUSE
// driver is found.
const driverInstallGuidance = "没有装 FUSE 驱动：请先安装 FUSE-T（推荐，brew install --cask fuse-t，或到官网下载安装包，无需内核扩展），备选 macFUSE（官网下载，Apple Silicon 上需进恢复模式批准内核扩展）。装好后把网盘开关关掉再打开即可重试。"

// DetectDriver reports whether a FUSE driver is present.
func DetectDriver() DriverStatus {
	switch runtime.GOOS {
	case "darwin":
		entries, err := os.ReadDir("/Library/Filesystems")
		if err != nil {
			return DriverStatus{Kind: "missing", Detail: driverInstallGuidance}
		}
		for _, e := range entries {
			name := strings.ToLower(e.Name())
			if !strings.Contains(name, "fuse") {
				continue
			}
			switch {
			case strings.Contains(name, "macfuse"), strings.Contains(name, "osxfuse"):
				return DriverStatus{Kind: "macfuse", Installed: true}
			case strings.Contains(name, "fuse-t"):
				return DriverStatus{Kind: "fuse-t", Installed: true}
			default:
				return DriverStatus{Kind: "fuse", Installed: true}
			}
		}
		return DriverStatus{Kind: "missing", Detail: driverInstallGuidance}
	case "linux":
		if _, err := os.Stat("/dev/fuse"); err == nil {
			return DriverStatus{Kind: "fuse", Installed: true}
		}
		return DriverStatus{Kind: "missing", Detail: "没有 FUSE 支持：Linux 下需要 /dev/fuse（fuse 内核模块），请先加载 fuse 模块再重试。"}
	default:
		return DriverStatus{Kind: "missing", Detail: "当前系统不支持 FUSE 挂载（P0 只支持 macOS 与 Linux）。"}
	}
}

// ClassifyMountError turns a mount failure into human text
// (gmountie's approach: match the error against known driver
// patterns). driverMissing reports whether the cause is a
// missing driver, so the UI can show the install guidance.
func ClassifyMountError(err error) (human string, driverMissing bool) {
	if err == nil {
		return "", false
	}
	msg := strings.ToLower(err.Error())
	for _, pat := range []string{
		"macfuse", "osxfuse", "mount_macfuse", "/dev/macfuse",
		"fuse-t", "no such device", "no fuse",
	} {
		if strings.Contains(msg, pat) {
			return driverInstallGuidance, true
		}
	}
	if strings.Contains(msg, "fusermount") && strings.Contains(msg, "not found") {
		return driverInstallGuidance, true
	}
	if strings.Contains(msg, "already mounted") || strings.Contains(msg, "device or resource busy") || strings.Contains(msg, "device busy") {
		return "挂载点正被占用：可能上次异常退出留下了旧挂载，重启应用会自动清理；也可以手动在终端里执行 umount ~/ZyDrive 后重试。", false
	}
	if strings.Contains(msg, "permission denied") || strings.Contains(msg, "operation not permitted") {
		return fmt.Sprintf("挂载被系统拒绝（权限不足）：%s", firstLine(err)), false
	}
	return fmt.Sprintf("挂载失败：%s", firstLine(err)), false
}
