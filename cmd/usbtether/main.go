// Command usbtether is the all-in-one desktop app for Matt's own USB tether.
//
//	usbtether                 = usbtether up -route -v6 (supervised, auto-reconnect)
//	usbtether up [flags]      supervisor: phone-app check/install, start, reconnect on exit 3/4
//	usbtether run [flags]     bare host (needs CAP_NET_ADMIN or root); what "up" runs each time
//	usbtether setup           one password popup: install to /usr/local/bin + cap_net_admin + udev rule,
//	                          then (as you) NM profile + menu launcher
//	usbtether phone           install/update the embedded phone app over adb
//	usbtether apk [file]      write the embedded APK (for installing without adb after a wipe)
//	usbtether status          what is installed / attached / running
//
// The phone APK is embedded at build time (dist.sh), so this one file restores both halves.
// No root process: the installed binary carries cap_net_admin and drops it after bring-up.
package main

import (
	"bufio"
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tetrd-clone/internal/host"
)

const (
	installPath = "/usr/local/bin/usbtether"
	pkg         = "com.matt.usbtether"
	activity    = pkg + "/.MainActivity"
	tunName     = "mxtun0"
	udevPath    = "/etc/udev/rules.d/70-usbtether.rules"
)

// assets/ holds usbtether.apk + apk_version.txt, copied in by dist.sh (gitignored); README keeps
// the pattern valid in a fresh checkout, where the binary then simply has no phone app inside.
//
//go:embed assets/*
var assets embed.FS

var apk, _ = assets.ReadFile("assets/usbtether.apk")

var apkVersionRaw = func() string { b, _ := assets.ReadFile("assets/apk_version.txt"); return string(b) }()

var version = "dev" // -ldflags "-X main.version=..."

const udevRules = `# usbtether: give the logged-in user the phone's USB node (AOA switch + accessory bulk I/O).
SUBSYSTEM=="usb", ATTR{idVendor}=="04e8", TAG+="uaccess"
SUBSYSTEM=="usb", ATTR{idVendor}=="18d1", ATTR{idProduct}=="2d0[0-5]", TAG+="uaccess"
`

func apkVersion() int { n, _ := strconv.Atoi(strings.TrimSpace(apkVersionRaw)); return n }

func main() {
	args := os.Args[1:]
	cmd := "up"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "run":
		host.Main(args)
	case "up":
		if len(args) == 0 {
			args = []string{"-route", "-v6"}
		}
		os.Exit(up(args))
	case "setup":
		os.Exit(setup())
	case "phone":
		if !adbReady() {
			fail("adb sees no authorized phone: enable USB debugging and accept the RSA prompt (or use 'usbtether apk')")
		}
		if err := installApp(true); err != nil {
			fail(err.Error())
		}
	case "apk":
		out := filepath.Join(os.Getenv("HOME"), "usbtether.apk")
		if len(args) > 0 {
			out = args[0]
		}
		if err := os.WriteFile(out, apk, 0o644); err != nil {
			fail(err.Error())
		}
		fmt.Printf("wrote %s (versionCode %d). Copy it to the phone (USB file transfer), open it in Files,\n"+
			"allow \"install unknown apps\" for Files when asked. Then plug in and run usbtether.\n", out, apkVersion())
	case "status":
		status()
	case "selftest":
		if err := host.SelfTest(); err != nil {
			fail("selftest: " + err.Error())
		}
	case "version", "-version", "--version":
		fmt.Printf("usbtether %s, embedded phone app versionCode %d (%d bytes)\n", version, apkVersion(), len(apk))
	case "help", "-h", "--help":
		fmt.Print(helpText)
	default:
		fail("unknown command " + cmd + " (try: usbtether help)")
	}
}

const helpText = `usbtether - own USB tether (phone app com.matt.usbtether + this desktop host)

  usbtether               connect (default route + IPv6), reconnect by itself; Ctrl-C stops
  usbtether up [flags]    same with your own host flags, e.g. up -hostroute 1.1.1.1/32
  usbtether setup         one-time install on this desktop (one password popup)
  usbtether phone         install/update the phone app over adb
  usbtether apk [file]    save the phone app as an .apk (install without adb)
  usbtether status        show what is installed/attached/running
  usbtether selftest      check the no-root path (scratch TUN, no phone, no routes)
  usbtether run -h        host flags

After a phone wipe:  enable Developer options > USB debugging, plug in, accept the prompt, run usbtether.
Without USB debugging: usbtether apk, copy ~/usbtether.apk to the phone, install it, run usbtether.
`

func fail(msg string) { fmt.Fprintln(os.Stderr, "usbtether: "+msg); os.Exit(1) }

// ---------- supervisor ----------

var logf *os.File

func say(format string, a ...any) {
	line := fmt.Sprintf("usbtether: "+format, a...)
	fmt.Println(line)
	if logf != nil {
		fmt.Fprintf(logf, "%s %s\n", time.Now().Format("2006/01/02 15:04:05"), line)
	}
}

func up(hostArgs []string) int {
	if os.Geteuid() == 0 {
		fmt.Fprintln(os.Stderr, "usbtether: run me as your user, not root (adb keys + no root process)")
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fail(err.Error())
	}
	if !host.HasNetAdmin() {
		if exe != installPath && fileExists(installPath) {
			// a dev copy was started: hand over to the installed (capability-carrying) one
			return execInstalled(append([]string{"up"}, hostArgs...))
		}
		fmt.Fprintln(os.Stderr, "usbtether: not installed with cap_net_admin yet: run 'usbtether setup' once")
		return 1
	}
	// The supervisor itself needs no privilege. Clear caps (but NOT no_new_privs, or the
	// re-exec'd host would not get its file capability back).
	if err := host.DropCapsKeepExec(); err != nil {
		fail("drop caps: " + err.Error())
	}
	if fileExists("/sys/class/net/" + tunName) {
		// A non-persistent TUN exists only while a process holds it: another host is live. Never
		// adb-install over it (that kills the phone side) or fight it for the phone.
		fmt.Fprintln(os.Stderr, "usbtether: a tunnel is already up ("+tunName+", old usbtether-host wrapper?): stop it first (Ctrl-C in its window)")
		return 1
	}
	if tetrdRunning() {
		fmt.Fprintln(os.Stderr, "usbtether: stock Tetrd is running and holds the phone. Quit Tetrd (tray -> Quit), replug once, rerun.")
		return 1
	}
	stateDir := filepath.Join(os.Getenv("HOME"), ".local/state/usbtether")
	_ = os.MkdirAll(stateDir, 0o700)
	if f, err := os.OpenFile(filepath.Join(stateDir, "host.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		logf = f
		defer f.Close()
	}

	adb := adbReady()
	if adb {
		if err := installApp(false); err != nil {
			say("phone app: %v", err)
		}
	} else {
		say("adb: no authorized phone (fine if the app is installed; Android opens it on attach, tap OK if asked)")
	}

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	fails, dead4, fresh := 0, 0, false
	for {
		args := append([]string(nil), hostArgs...)
		if accessoryPresent() && !hasFlag(args, "-reuse") {
			args = append(args, "-reuse")
		}
		adb = adbReady()
		if adb {
			adbRun("shell", "input", "keyevent", "KEYCODE_WAKEUP")
			if fresh { // last session never came up: give the phone a fresh core too
				adbRun("shell", "am", "force-stop", pkg)
			}
		}
		helperDone := make(chan struct{})
		if adb {
			go func() { // start the phone app once the TUN exists (accessory dispatch may not fire on -reuse)
				for i := 0; i < 90; i++ {
					select {
					case <-helperDone:
						return
					case <-time.After(500 * time.Millisecond):
					}
					if fileExists("/sys/class/net/" + tunName) {
						adbRun("shell", "am", "start", "-n", activity)
						return
					}
				}
			}()
		}
		rc, stopped := runHost(exe, args, sig)
		close(helperDone)
		if stopped {
			say("stopped")
			return 0
		}
		var delay time.Duration
		switch rc {
		case 0:
			say("host exited normally")
			return 0
		case 3:
			fails, dead4, fresh, delay = 0, 0, false, 2*time.Second
			say("tunnel died, reconnecting in %s (Wi-Fi carries traffic meanwhile)", delay)
		case 2:
			say("bad host flags (see: usbtether run -h)")
			return 2
		case 4:
			dead4++
			fresh, delay = true, 3*time.Second
			if dead4 >= 3 {
				say("tunnel never came up %d times: replug the phone cable and rerun", dead4)
				return 4
			}
			say("tunnel never came up (try %d/3), retrying with a fresh phone app in %s", dead4, delay)
		default:
			fails++
			fresh, delay = false, 5*time.Second
			if fails >= 12 {
				say("giving up after %d failed starts (last rc=%d): plug the phone in and rerun", fails, rc)
				return 1
			}
			say("start failed (rc=%d, try %d/12), retrying in %s", rc, fails, delay)
		}
		select {
		case <-sig:
			say("stopped")
			return 0
		case <-time.After(delay):
		}
	}
}

// runHost runs "<exe> run args" (the exec re-grants cap_net_admin), mirroring output to the log.
func runHost(exe string, args []string, sig chan os.Signal) (rc int, stopped bool) {
	c := exec.Command(exe, append([]string{"run"}, args...)...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // we forward signals ourselves
	pr, pw := io.Pipe()
	c.Stdout, c.Stderr = pw, pw
	c.WaitDelay = 2 * time.Second // a leftover grandchild holding the pipe must not hang the supervisor
	if err := c.Start(); err != nil {
		say("start host: %v", err)
		return 1, false
	}
	copied := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			fmt.Println(sc.Text())
			if logf != nil {
				fmt.Fprintln(logf, sc.Text())
			}
		}
		close(copied)
	}()
	done := make(chan error, 1)
	go func() { done <- c.Wait(); pw.Close() }()
	var err error
	select {
	case err = <-done:
	case <-sig:
		stopped = true
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGTERM) // host's group: close TUN (routes vanish), release USB
		select {
		case err = <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
			err = <-done
		}
	}
	<-copied
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), stopped
	}
	if err != nil {
		return 1, stopped
	}
	return 0, stopped
}

func execInstalled(args []string) int {
	err := syscall.Exec(installPath, append([]string{installPath}, args...), os.Environ())
	fmt.Fprintln(os.Stderr, "usbtether: exec", installPath, err)
	return 1
}

func hasFlag(args []string, f string) bool {
	for _, a := range args {
		if a == f || strings.HasPrefix(a, f+"=") {
			return true
		}
	}
	return false
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// accessoryPresent: phone already switched to AOA (18d1:2d00-2d05).
func accessoryPresent() bool {
	ms, _ := filepath.Glob("/sys/bus/usb/devices/*/idVendor")
	for _, m := range ms {
		v, _ := os.ReadFile(m)
		p, _ := os.ReadFile(filepath.Join(filepath.Dir(m), "idProduct"))
		if strings.TrimSpace(string(v)) == "18d1" && strings.HasPrefix(strings.TrimSpace(string(p)), "2d0") {
			return true
		}
	}
	return false
}

func tetrdRunning() bool {
	ms, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	for _, m := range ms {
		b, _ := os.ReadFile(m)
		if bytes.HasPrefix(b, []byte("/opt/Tetrd/bin/tetrd")) {
			return true
		}
	}
	return false
}

// ---------- adb / phone app ----------

func adbPath() string { p, _ := exec.LookPath("adb"); return p }

func adbOut(args ...string) (string, error) {
	if adbPath() == "" {
		return "", errors.New("adb not installed")
	}
	c := exec.Command("adb", args...)
	var b bytes.Buffer
	c.Stdout, c.Stderr = &b, &b
	done := make(chan error, 1)
	if err := c.Start(); err != nil {
		return "", err
	}
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		return b.String(), err
	case <-time.After(90 * time.Second):
		_ = c.Process.Kill()
		return b.String(), errors.New("adb timed out")
	}
}

func adbRun(args ...string) { _, _ = adbOut(args...) }

func adbReady() bool {
	out, err := adbOut("get-state")
	return err == nil && strings.TrimSpace(out) == "device"
}

// phoneAppVersion: installed versionCode (0 = installed without one), -1 = not installed, -2 = adb failed.
func phoneAppVersion() int {
	out, err := adbOut("shell", "dumpsys", "package", pkg)
	if err != nil {
		return -2
	}
	if !strings.Contains(out, "Package ["+pkg+"]") {
		return -1
	}
	for _, f := range strings.Fields(out) {
		if v, ok := strings.CutPrefix(f, "versionCode="); ok {
			n, _ := strconv.Atoi(v)
			return n
		}
	}
	return 0
}

func installApp(force bool) error {
	if len(apk) < 1000 {
		return errors.New("this build has no embedded APK (build with dist.sh)")
	}
	have := phoneAppVersion()
	if have == -2 {
		return errors.New("adb dumpsys failed")
	}
	if !force && have >= apkVersion() {
		say("phone app versionCode %d installed (embedded %d): ok", have, apkVersion())
		return nil
	}
	if have >= 0 && accessoryPresent() {
		say("note: updating the phone app ends any running tunnel session")
	}
	f, err := os.CreateTemp("", "usbtether-*.apk")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(apk); err != nil {
		return err
	}
	f.Close()
	say("installing phone app versionCode %d (phone had %d)...", apkVersion(), have)
	out, err := adbOut("install", "-r", f.Name())
	if err != nil || !strings.Contains(out, "Success") {
		if strings.Contains(out, "UPDATE_INCOMPATIBLE") {
			return fmt.Errorf("signing key differs from the installed app; uninstall it on the phone first: adb uninstall %s\n%s", pkg, out)
		}
		return fmt.Errorf("adb install failed: %v\n%s", err, out)
	}
	// Android 13+: allow the tunnel notification on a fresh install (harmless if already granted).
	adbRun("shell", "pm", "grant", pkg, "android.permission.POST_NOTIFICATIONS")
	say("phone app installed")
	return nil
}

// ---------- setup ----------

func setup() int {
	exe, err := os.Executable()
	if err != nil {
		fail(err.Error())
	}
	if os.Geteuid() == 0 {
		fail("run setup as your user; it asks for the password itself")
	}
	if _, err := exec.LookPath("setcap"); err != nil {
		fail("setcap missing: sudo apt install libcap2-bin")
	}
	tmp, err := os.MkdirTemp("", "usbtether-setup-")
	if err != nil {
		fail(err.Error())
	}
	defer os.RemoveAll(tmp)
	rules := filepath.Join(tmp, "70-usbtether.rules")
	_ = os.WriteFile(rules, []byte(udevRules), 0o644)
	// Copy the binary first: installing a file onto itself (setup run from /usr/local/bin) would truncate it.
	bin := filepath.Join(tmp, "usbtether")
	if b, err := os.ReadFile(exe); err != nil || os.WriteFile(bin, b, 0o755) != nil {
		fail("copy self failed")
	}
	script := filepath.Join(tmp, "setup.sh")
	_ = os.WriteFile(script, []byte(`#!/bin/sh
set -e
install -o root -g root -m 0755 "$1" `+installPath+`
setcap cap_net_admin+ep `+installPath+`
install -o root -g root -m 0644 "$2" `+udevPath+`
udevadm control --reload-rules
echo "installed: $(getcap `+installPath+`)"
`), 0o755)
	var esc []string
	if p, err := exec.LookPath("sudo-gui"); err == nil {
		esc = []string{p}
	} else if p, err := exec.LookPath("pkexec"); err == nil {
		esc = []string{p}
	} else {
		esc = []string{"sudo"}
	}
	fmt.Println("usbtether: one password prompt: install", installPath, "+ cap_net_admin + udev rule")
	c := exec.Command(esc[0], append(esc[1:], "/bin/sh", script, bin, rules)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		fail("privileged install failed: " + err.Error())
	}
	nmProfile()
	launcher()
	fmt.Print("\nusbtether: setup done. Connect with: usbtether   (or the \"USB Tether\" menu entry).\nFirst time on this desktop: replug the phone once so the new udev rule applies.\n")
	return 0
}

// nmProfile creates the "usbtether" NetworkManager profile (DNS via the tunnel) if missing.
func nmProfile() {
	if _, err := exec.LookPath("nmcli"); err != nil {
		return
	}
	if out, err := exec.Command("nmcli", "-g", "connection.id", "con", "show", "usbtether").Output(); err == nil && len(out) > 0 {
		fmt.Println("usbtether: NetworkManager profile usbtether exists")
		return
	}
	out, err := exec.Command("nmcli", "con", "add", "type", "tun", "con-name", "usbtether", "ifname", tunName,
		"tun.mode", "1", "connection.autoconnect", "no",
		"ipv4.method", "manual", "ipv4.addresses", "10.77.0.1/24", "ipv4.dns", "1.1.1.1,8.8.8.8",
		"ipv4.never-default", "yes", "ipv4.dns-priority", "-100",
		"ipv6.method", "manual", "ipv6.addresses", "fd77:7777::1/64",
		"ipv6.dns", "2606:4700:4700::1111,2001:4860:4860::8888", "ipv6.never-default", "yes", "ipv6.dns-priority", "-100",
	).CombinedOutput()
	if err != nil {
		fmt.Printf("usbtether: NM profile not created (%v: %s); DNS then stays on the other link\n", err, strings.TrimSpace(string(out)))
		return
	}
	fmt.Println("usbtether: created NetworkManager profile usbtether (DNS via tunnel)")
}

func launcher() {
	term := ""
	for _, t := range []string{"konsole", "kitty", "x-terminal-emulator", "xterm"} {
		if p, err := exec.LookPath(t); err == nil {
			term = p
			break
		}
	}
	if term == "" {
		fmt.Println("usbtether: no terminal found, skipped the menu launcher")
		return
	}
	run := term + " -e " + installPath
	if strings.HasSuffix(term, "/konsole") {
		run = term + " --hide-menubar -p tabtitle=usbtether -e " + installPath
	}
	dir := filepath.Join(os.Getenv("HOME"), ".local/share/applications")
	_ = os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, "usbtether.desktop")
	err := os.WriteFile(p, []byte(`[Desktop Entry]
Type=Application
Name=USB Tether
Comment=Own USB tether through the phone (com.matt.usbtether); close the window or Ctrl-C to stop
Exec=`+run+`
Icon=network-wired
Terminal=false
Categories=Network;
Actions=status;

[Desktop Action status]
Name=Status
Exec=`+term+` --hold -e `+installPath+` status
`), 0o644)
	if err == nil {
		fmt.Println("usbtether: menu launcher", p)
	}
}

// ---------- status ----------

func status() {
	row := func(k, v string) { fmt.Printf("%-14s %s\n", k, v) }
	row("binary", version+fmt.Sprintf(" (embedded app versionCode %d)", apkVersion()))
	if out, err := exec.Command("getcap", installPath).Output(); err == nil && len(out) > 0 {
		row("installed", strings.TrimSpace(string(out)))
	} else if fileExists(installPath) {
		row("installed", installPath+" WITHOUT cap_net_admin: run usbtether setup")
	} else {
		row("installed", "no: run usbtether setup")
	}
	row("udev rule", map[bool]string{true: udevPath, false: "missing: run usbtether setup"}[fileExists(udevPath)])
	usb := "not attached"
	if accessoryPresent() {
		usb = "accessory mode (18d1:2d0x)"
	} else if out, _ := exec.Command("lsusb", "-d", "04e8:").Output(); len(out) > 0 {
		usb = "normal mode: " + strings.TrimSpace(string(out))
	}
	row("phone USB", usb)
	if adbReady() {
		v := phoneAppVersion()
		s := fmt.Sprintf("versionCode %d", v)
		if v == -1 {
			s = "NOT installed: usbtether phone"
		} else if v == -2 {
			s = "adb error"
		} else if v < apkVersion() {
			s += fmt.Sprintf(" (older than embedded %d: usbtether phone)", apkVersion())
		}
		row("phone app", s)
	} else {
		row("phone app", "unknown (adb not authorized)")
	}
	t := "down"
	if fileExists("/sys/class/net/" + tunName) {
		t = "up"
		if out, _ := exec.Command("ip", "-br", "addr", "show", "dev", tunName).Output(); len(out) > 0 {
			t = strings.TrimSpace(string(out))
		}
	}
	row(tunName, t)
	if out, _ := exec.Command("ip", "route", "show", "default").Output(); len(out) > 0 {
		row("default route", strings.ReplaceAll(strings.TrimSpace(string(out)), "\n", " | "))
	}
	row("stock Tetrd", map[bool]string{true: "RUNNING (quit it before usbtether)", false: "not running"}[tetrdRunning()])
}
