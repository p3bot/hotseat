// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/store"
)

const (
	logName    = "hotseat.log"
	recordName = "hotseat.pid"
)

// statusFailure is a finished status report that exits 1.
// The report is already on stdout. The error text stays empty so the process
// does not add a second line.
type statusFailure struct{}

func (statusFailure) Error() string { return "" }

// StatusFailure reports a finished status check that exits 1.
func StatusFailure(err error) bool {
	var failed statusFailure
	return errors.As(err, &failed)
}

type busConfig struct {
	store     string
	bind      string
	token     string
	tokenFile string
	maxBody   int
}

type busProc struct {
	running  bool
	lockHeld bool
	pid      int
	addr     string
}

type pidRecord struct {
	PID  int
	Addr string
}

type startupReport struct {
	OK     bool   `json:"ok"`
	PID    int    `json:"pid"`
	Listen string `json:"listen"`
	Store  string `json:"store"`
	Error  string `json:"error,omitempty"`
}

type procStat struct {
	state byte
	ppid  int
	pgrp  int
	sid   int
	tty   int
}

// acceptReady reports when Serve enters Accept, which is the first moment a client can be handled.
type acceptReady struct {
	net.Listener
	once sync.Once
	ch   chan struct{}
}

func newAcceptReady(ln net.Listener) *acceptReady {
	return &acceptReady{Listener: ln, ch: make(chan struct{})}
}

func (l *acceptReady) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.ch) })
	return l.Listener.Accept()
}

func configureBus(storeDir, addr, tokenFile string, maxBody int) (busConfig, error) {
	if storeDir != "" {
		storeDir = filepath.Clean(storeDir)
	}
	if maxBody < 1 {
		return busConfig{}, errors.New("max body must be a positive number of bytes")
	}
	bind, class, err := bus.ResolveListen(addr)
	if err != nil {
		return busConfig{}, err
	}
	token, err := loadToken(class, tokenFile)
	if err != nil {
		return busConfig{}, err
	}
	resolved, err := resolveStore(storeDir)
	if err != nil {
		return busConfig{}, err
	}
	return busConfig{
		store:     resolved,
		bind:      bind,
		token:     token,
		tokenFile: tokenFile,
		maxBody:   maxBody,
	}, nil
}

func resolveStore(storeDir string) (string, error) {
	if storeDir == "" {
		return defaultStoreDir()
	}
	abs, err := filepath.Abs(storeDir)
	if err != nil {
		return "", fmt.Errorf("store directory: %w", err)
	}
	return abs, nil
}

func listenChecked(bind, token string) (net.Listener, error) {
	ln, err := net.Listen(bus.ListenNetwork(bind), bind)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", bind, err)
	}
	if token == "" && !bus.AddrLoopback(ln.Addr()) {
		err = errors.New("non-loopback listen address requires a token file")
		if cerr := ln.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return nil, err
	}
	return ln, nil
}

// probeListen rejects a bind that cannot succeed before start re-execs.
// Closing the probe socket leaves no listener and no store.
func probeListen(bind, token string) error {
	ln, err := listenChecked(bind, token)
	if err != nil {
		return err
	}
	return ln.Close()
}

func openBus(storeDir, addr, tokenFile string, maxBody int) (net.Listener, *store.Store, string, error) {
	cfg, err := configureBus(storeDir, addr, tokenFile, maxBody)
	if err != nil {
		return nil, nil, "", err
	}
	ln, err := listenChecked(cfg.bind, cfg.token)
	if err != nil {
		return nil, nil, "", err
	}
	st, err := store.Open(cfg.store)
	if err != nil {
		if cerr := ln.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return nil, nil, "", err
	}
	return ln, st, cfg.token, nil
}

func serveListener(ctx context.Context, ln net.Listener, st *store.Store, token string, maxBody int, log *slog.Logger) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if log == nil {
		log = newLogger(false)
	}
	log.Info("listening", "addr", ln.Addr().String(), "store", st.Path(), "token_required", token != "")
	if log.Enabled(ctx, slog.LevelDebug) {
		log.Debug("debug logging enabled", "max_body", maxBody)
	}
	err := bus.Serve(ctx, ln, st, bus.Options{
		MaxBody: maxBody,
		Logger:  log,
		Token:   token,
	})
	if err == nil || errors.Is(err, context.Canceled) {
		log.Info("stopped", "addr", ln.Addr().String())
		return nil
	}
	return err
}

func startDaemon(ctx context.Context, out io.Writer, storeDir, addr, tokenFile string, maxBody int, debug bool) error {
	if out == nil {
		out = os.Stdout
	}
	// A running bus is reported before listen, token, and body size are read.
	// Those flags apply only when a new process would be started.
	if storeDir != "" {
		storeDir = filepath.Clean(storeDir)
	}
	dir, err := resolveStore(storeDir)
	if err != nil {
		return err
	}
	got, err := inspectBus(dir)
	if err != nil {
		return err
	}
	if got.running {
		return printBus(out, got.pid, got.addr, dir)
	}
	cfg, err := configureBus(storeDir, addr, tokenFile, maxBody)
	if err != nil {
		return err
	}
	got, err = inspectBus(cfg.store)
	if err != nil {
		return err
	}
	if got.running {
		return printBus(out, got.pid, got.addr, cfg.store)
	}
	if got.lockHeld {
		return store.ErrHeld
	}
	if err := probeListen(cfg.bind, cfg.token); err != nil {
		return err
	}
	return spawnBus(ctx, out, cfg, debug)
}

func spawnBus(ctx context.Context, out io.Writer, cfg busConfig, debug bool) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("bus executable: %w", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()

	// The child receives the resolved address, so a hostname is looked up once.
	args := []string{
		"bus", "serve",
		"--store", cfg.store,
		"--listen", cfg.bind,
		"--max-body", strconv.Itoa(cfg.maxBody),
	}
	if cfg.tokenFile != "" {
		args = append(args, "--token-file", cfg.tokenFile)
	}
	if debug {
		args = append(args, "--debug")
	}
	cmd := exec.Command(exe, args...)
	// nil stdin is /dev/null. nil stdout and stderr are /dev/null until the child opens the log.
	cmd.ExtraFiles = []*os.File{w}
	// Re-exec into a new session. A fork inside this process is unsafe after the runtime starts threads.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		w.Close()
		return fmt.Errorf("start bus: %w", err)
	}
	if err := w.Close(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}

	// Do not tie the child to ctx after it is listening. Execute cancels ctx when start returns.
	type decoded struct {
		rep startupReport
		err error
	}
	done := make(chan decoded, 1)
	go func() {
		var rep startupReport
		err := json.NewDecoder(r).Decode(&rep)
		done <- decoded{rep, err}
	}()
	var got decoded
	select {
	case got = <-done:
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return ctx.Err()
	}
	if got.err != nil {
		_ = cmd.Wait()
		return fmt.Errorf("bus did not start: %w", got.err)
	}
	if got.rep.Error != "" || !got.rep.OK {
		_ = cmd.Wait()
		if strings.Contains(got.rep.Error, store.ErrHeld.Error()) {
			if live, ok := awaitRunning(ctx, cfg.store); ok {
				return printBus(out, live.pid, live.addr, cfg.store)
			}
		}
		if got.rep.Error != "" {
			return errors.New(got.rep.Error)
		}
		return errors.New("bus did not start")
	}
	if got.rep.PID != cmd.Process.Pid || got.rep.Listen == "" || got.rep.Store == "" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.New("bus did not report its listener")
	}
	if err := cmd.Process.Release(); err != nil {
		return err
	}
	return printBus(out, got.rep.PID, got.rep.Listen, got.rep.Store)
}

func awaitRunning(ctx context.Context, dir string) (busProc, bool) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, err := inspectBus(dir)
		if err == nil && got.running {
			return got, true
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			return busProc{}, false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func stopBus(ctx context.Context, storeDir string) error {
	dir, err := resolveStore(storeDir)
	if err != nil {
		return err
	}
	pids, err := busPIDs(dir)
	if err != nil {
		return err
	}
	if len(pids) == 0 {
		return nil
	}
	for _, pid := range pids {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
	}
	return waitStopped(ctx, pids, dir)
}

func waitStopped(ctx context.Context, pids []int, dir string) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		held, err := lockHeld(dir)
		if err != nil {
			return err
		}
		if busesGone(pids, dir) && !held {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type healthReport struct {
	health string
	listen string
	reason string
	pid    int
	store  string
}

var healthClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		Proxy:                 nil,
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 5 * time.Second,
		DialContext: (&net.Dialer{
			Timeout: 5 * time.Second,
		}).DialContext,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func statusBus(ctx context.Context, w io.Writer, address string) error {
	if w == nil {
		w = os.Stdout
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rep, err := collectStatus(ctx, address)
	if err != nil {
		return err
	}
	if err := printHealth(w, rep); err != nil {
		return err
	}
	if rep.health != "pass" {
		return statusFailure{}
	}
	return nil
}

func collectStatus(ctx context.Context, address string) (healthReport, error) {
	body, code, err := getHealth(ctx, address)
	if err != nil && ctx.Err() != nil {
		return healthReport{}, ctx.Err()
	}
	return interpretHealth(address, body, code, err)
}

func interpretHealth(address, body string, code int, dialErr error) (healthReport, error) {
	if dialErr != nil {
		return healthReport{
			health: "connection_failure",
			listen: address,
			reason: dialErr.Error(),
		}, nil
	}
	rep := healthReport{health: healthWord(code, body), listen: address}
	pid, dir, ok, err := localServeBound(address)
	if err != nil {
		return healthReport{}, err
	}
	if ok {
		rep.pid = pid
		rep.store = dir
	}
	return rep, nil
}

func healthWord(code int, body string) string {
	if code == http.StatusOK && (body == "pass" || body == "pass\n") {
		return "pass"
	}
	return "fail"
}

func getHealth(ctx context.Context, address string) (body string, code int, err error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, fmt.Errorf("connection failed: %s: %w", address, err)
	}
	url := "http://" + net.JoinHostPort(host, port) + bus.PathHealth
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, fmt.Errorf("connection failed: %s: %w", address, err)
	}
	resp, err := healthClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("connection failed: %s: %w", address, err)
	}
	defer resp.Body.Close()
	// pass plus one newline is the longest accepted body. One extra byte makes it fail.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8))
	if err != nil {
		return "", 0, fmt.Errorf("connection failed: %s: %w", address, err)
	}
	return string(raw), resp.StatusCode, nil
}

func printHealth(w io.Writer, rep healthReport) error {
	if _, err := fmt.Fprintf(w, "health: %s\nlisten: %s\n", rep.health, rep.listen); err != nil {
		return err
	}
	if rep.reason != "" {
		if _, err := fmt.Fprintf(w, "reason: %s\n", rep.reason); err != nil {
			return err
		}
	}
	if rep.pid != 0 {
		if _, err := fmt.Fprintf(w, "pid: %d\nstore: %s\n", rep.pid, rep.store); err != nil {
			return err
		}
	}
	return nil
}

func localServeBound(address string) (pid int, dir string, ok bool, err error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, "", false, fmt.Errorf("list processes: %w", err)
	}
	ownNS, nsErr := ownNetNS()
	if nsErr != nil {
		ownNS = nil
	}
	var foundPid int
	var foundDir string
	var found int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 || !pidAlive(pid) {
			continue
		}
		args, err := processArgs(pid)
		if err != nil || !isBusServe(args) {
			continue
		}
		// Loopback in another network is not the socket that answered.
		// A namespace that cannot be read is skipped. When our own namespace
		// cannot be read, the scan keeps today's address match.
		if ownNS != nil && !sameNetNS(ownNS, pid) {
			continue
		}
		addrs, err := processListenAddrs(pid)
		if err != nil {
			continue
		}
		matched := false
		for _, addr := range addrs {
			if sameListen(addr, address) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		storeDir, err := processStore(pid, args)
		if err != nil {
			return 0, "", false, err
		}
		found++
		foundPid = pid
		foundDir = storeDir
	}
	if found == 0 {
		return 0, "", false, nil
	}
	if found > 1 {
		return 0, "", false, fmt.Errorf("more than one bus is bound to %s", address)
	}
	return foundPid, foundDir, true, nil
}

func ownNetNS() (os.FileInfo, error) {
	info, err := os.Stat("/proc/self/ns/net")
	if err != nil {
		return nil, err
	}
	return info, nil
}

func sameNetNS(own os.FileInfo, pid int) bool {
	info, err := os.Stat(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return false
	}
	return os.SameFile(own, info)
}

func processStore(pid int, args [][]byte) (string, error) {
	if arg := argValue(args, "--store"); filepath.IsAbs(arg) {
		return arg, nil
	}
	dir, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	if err != nil {
		return "", fmt.Errorf("bus store: %w", err)
	}
	return dir, nil
}

func sameListen(a, b string) bool {
	ah, ap, err := net.SplitHostPort(a)
	if err != nil {
		return false
	}
	bh, bp, err := net.SplitHostPort(b)
	if err != nil {
		return false
	}
	an, aerr := strconv.Atoi(ap)
	bn, berr := strconv.Atoi(bp)
	if aerr != nil || berr != nil || an != bn {
		return false
	}
	aip := net.ParseIP(ah)
	bip := net.ParseIP(bh)
	if aip == nil || bip == nil {
		return strings.EqualFold(ah, bh)
	}
	// 127.0.0.1 and ::1 are different addresses. A mapped IPv4 is still IPv4.
	if (aip.To4() == nil) != (bip.To4() == nil) {
		return false
	}
	return aip.Equal(bip)
}

func printBus(w io.Writer, pid int, addr, dir string) error {
	_, err := fmt.Fprintf(w, "pid: %d\nlisten: %s\nstore: %s\n", pid, addr, dir)
	return err
}

func serveDetached(ctx context.Context, storeDir, addr, tokenFile string, maxBody int, debug bool) error {
	startup, err := startupFile()
	if err != nil {
		return errors.New("bus serve is internal")
	}
	ln, st, token, err := openBus(storeDir, addr, tokenFile, maxBody)
	if err != nil {
		return reportBus(startup, startupReport{Error: err.Error()})
	}
	dir := filepath.Dir(st.Path())
	if err := os.Chdir(dir); err != nil {
		_ = ln.Close()
		_ = st.Close()
		return reportBus(startup, startupReport{Error: err.Error()})
	}
	if err := attachLog(dir); err != nil {
		_ = ln.Close()
		_ = st.Close()
		return reportBus(startup, startupReport{Error: err.Error()})
	}

	ctx, stop := context.WithCancel(ctx)
	defer stop()
	ready := newAcceptReady(ln)
	errCh := make(chan error, 1)
	go func() {
		errCh <- serveListener(ctx, ready, st, token, maxBody, newLogger(debug))
	}()
	fromServe, startErr := waitListening(ctx, ready.ch, errCh)
	if startErr != nil {
		stop()
		if !fromServe {
			<-errCh
		}
		_ = st.Close()
		return reportBus(startup, startupReport{Error: startErr.Error()})
	}

	pid := os.Getpid()
	bound := ln.Addr().String()
	if err := writeRecord(dir, pid, bound); err != nil {
		stop()
		<-errCh
		_ = st.Close()
		return reportBus(startup, startupReport{Error: err.Error()})
	}
	if err := reportBus(startup, startupReport{OK: true, PID: pid, Listen: bound, Store: dir}); err != nil {
		stop()
		<-errCh
		_ = removeRecord(dir, pid)
		_ = st.Close()
		return err
	}

	// Remove the record before releasing the lock so a later start cannot delete a newer record.
	err = <-errCh
	if rerr := removeRecord(dir, pid); err == nil {
		err = rerr
	}
	if cerr := st.Close(); err == nil {
		err = cerr
	}
	return err
}

func waitListening(ctx context.Context, ready <-chan struct{}, errCh <-chan error) (fromServe bool, err error) {
	select {
	case <-ready:
		return false, nil
	case err := <-errCh:
		if err == nil {
			err = errors.New("bus stopped before listening")
		}
		return true, err
	case <-ctx.Done():
		return false, errors.New("bus stopped before listening")
	}
}

func startupFile() (*os.File, error) {
	// start has already called setsid and passed the readiness pipe on fd 3.
	sid, err := unix.Getsid(0)
	if err != nil || sid != os.Getpid() {
		return nil, errors.New("bus serve is internal")
	}
	var st unix.Stat_t
	if err := unix.Fstat(3, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFIFO {
		return nil, errors.New("bus serve is internal")
	}
	f := os.NewFile(3, "startup")
	if f == nil {
		return nil, errors.New("bus serve is internal")
	}
	return f, nil
}

func reportBus(f *os.File, rep startupReport) error {
	defer f.Close()
	if err := json.NewEncoder(f).Encode(&rep); err != nil && rep.Error == "" {
		return err
	}
	if rep.Error != "" {
		return errors.New(rep.Error)
	}
	return nil
}

func attachLog(dir string) error {
	path := filepath.Join(dir, logName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return fmt.Errorf("log file is a symlink: %s", path)
		}
		return fmt.Errorf("open log: %w", err)
	}
	if err := unix.Fchmod(int(f.Fd()), 0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("set log permissions: %w", err)
	}
	if err := unix.Dup2(int(f.Fd()), 1); err != nil {
		_ = f.Close()
		return err
	}
	if err := unix.Dup2(int(f.Fd()), 2); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// inspectBus reports the live serve process for this store.
// The pid file records the listen address. The process is the bus, including
// before that file is written and after it has been removed.
func inspectBus(dir string) (busProc, error) {
	rec, recErr := readRecord(dir)
	if recErr == nil && busProcessMatches(rec.PID, dir) {
		return busProc{running: true, pid: rec.PID, addr: rec.Addr}, nil
	}
	pids, err := busPIDs(dir)
	if err != nil {
		return busProc{}, err
	}
	if len(pids) == 0 {
		held, herr := lockHeld(dir)
		if herr != nil {
			return busProc{}, herr
		}
		return busProc{lockHeld: held}, nil
	}
	pid := chooseBus(pids, dir)
	addr, err := busListenAddr(pid)
	if err != nil {
		return busProc{}, err
	}
	return busProc{running: true, pid: pid, addr: addr}, nil
}

func busPIDs(dir string) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if busProcessMatches(pid, dir) {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	return pids, nil
}

func chooseBus(pids []int, dir string) int {
	for _, pid := range pids {
		addrs, err := processListenAddrs(pid)
		if err == nil && len(addrs) == 1 {
			return pid
		}
	}
	for _, pid := range pids {
		cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
		if err == nil && sameFile(cwd, dir) {
			return pid
		}
	}
	return pids[0]
}

func busProcessMatches(pid int, storeDir string) bool {
	if pid <= 1 || !pidAlive(pid) {
		return false
	}
	args, err := processArgs(pid)
	if err != nil || !isBusServe(args) {
		return false
	}
	if storeArgMatches(args, storeDir) {
		return true
	}
	cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	if err != nil {
		return false
	}
	if sameFile(cwd, storeDir) {
		return true
	}
	arg := argValue(args, "--store")
	if arg != "" && !filepath.IsAbs(arg) {
		return sameFile(filepath.Join(cwd, arg), storeDir)
	}
	return false
}

func processArgs(pid int) ([][]byte, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil, err
	}
	return splitCmdline(raw), nil
}

func splitCmdline(raw []byte) [][]byte {
	return bytes.Split(bytes.TrimRight(raw, "\x00"), []byte{0})
}

func isBusServe(args [][]byte) bool {
	for i := 0; i+1 < len(args); i++ {
		if string(args[i]) == "bus" && string(args[i+1]) == "serve" {
			return true
		}
	}
	return false
}

func argValue(args [][]byte, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if string(args[i]) == name {
			return string(args[i+1])
		}
	}
	return ""
}

func storeArgMatches(args [][]byte, storeDir string) bool {
	arg := argValue(args, "--store")
	if arg == "" || !filepath.IsAbs(arg) {
		return false
	}
	return sameFile(arg, storeDir)
}

func busGone(pid int, dir string) bool {
	if !pidAlive(pid) {
		return true
	}
	return !busProcessMatches(pid, dir)
}

func busesGone(pids []int, dir string) bool {
	for _, pid := range pids {
		if !busGone(pid, dir) {
			return false
		}
	}
	return true
}

func busListenAddr(pid int) (string, error) {
	addrs, err := processListenAddrs(pid)
	if err != nil {
		return "", err
	}
	switch len(addrs) {
	case 1:
		return addrs[0], nil
	case 0:
		if addr := requestedListen(pid); addr != "" {
			return addr, nil
		}
		return "", errors.New("bus listen address is unknown")
	default:
		return "", errors.New("bus has more than one listen address")
	}
}

func requestedListen(pid int) string {
	args, err := processArgs(pid)
	if err != nil {
		return ""
	}
	return argValue(args, "--listen")
}

func processListenAddrs(pid int) ([]string, error) {
	inodes, err := socketInodes(pid)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	var addrs []string
	for _, name := range []string{"tcp", "tcp6"} {
		found, err := procListenAddrs(pid, name, inodes)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, addr := range found {
			if _, ok := seen[addr]; ok {
				continue
			}
			seen[addr] = struct{}{}
			addrs = append(addrs, addr)
		}
	}
	sort.Strings(addrs)
	return addrs, nil
}

func socketInodes(pid int) (map[string]struct{}, error) {
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return nil, err
	}
	inodes := make(map[string]struct{})
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(fmt.Sprintf("/proc/%d/fd", pid), entry.Name()))
		if err != nil {
			continue
		}
		inode, ok := strings.CutPrefix(target, "socket:[")
		if !ok || !strings.HasSuffix(inode, "]") {
			continue
		}
		inodes[strings.TrimSuffix(inode, "]")] = struct{}{}
	}
	return inodes, nil
}

func procListenAddrs(pid int, proto string, inodes map[string]struct{}) ([]string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/net/%s", pid, proto))
	if err != nil {
		return nil, err
	}
	var addrs []string
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		if i == 0 || line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 10 || !strings.EqualFold(fields[3], "0A") {
			continue
		}
		if _, ok := inodes[fields[9]]; !ok {
			continue
		}
		addr, err := decodeProcAddr(fields[1])
		if err != nil {
			return nil, err
		}
		addrs = append(addrs, addr)
	}
	return addrs, nil
}

func decodeProcAddr(raw string) (string, error) {
	ipHex, portHex, ok := strings.Cut(raw, ":")
	if !ok {
		return "", errors.New("process address is unreadable")
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return "", errors.New("process address is unreadable")
	}
	ip, err := decodeProcIP(ipHex)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(ip.String(), strconv.FormatUint(port, 10)), nil
}

func decodeProcIP(ipHex string) (net.IP, error) {
	if len(ipHex) != 8 && len(ipHex) != 32 {
		return nil, errors.New("process address is unreadable")
	}
	b, err := hex.DecodeString(ipHex)
	if err != nil {
		return nil, errors.New("process address is unreadable")
	}
	ip := make(net.IP, len(b))
	for i := 0; i < len(b); i += 4 {
		ip[i] = b[i+3]
		ip[i+1] = b[i+2]
		ip[i+2] = b[i+1]
		ip[i+3] = b[i]
	}
	if len(ip) == 4 {
		return net.IPv4(ip[0], ip[1], ip[2], ip[3]), nil
	}
	return ip, nil
}

func pidAlive(pid int) bool {
	st, err := readProcStat(pid)
	if err != nil {
		return false
	}
	return st.state != 'Z' && st.state != 'X' && st.state != 'x'
}

func readProcStat(pid int) (procStat, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return procStat{}, err
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 || i+2 >= len(b) {
		return procStat{}, errors.New("process stat is unreadable")
	}
	var st procStat
	_, err = fmt.Sscanf(string(b[i+2:]), "%c %d %d %d %d", &st.state, &st.ppid, &st.pgrp, &st.sid, &st.tty)
	if err != nil {
		return procStat{}, err
	}
	return st, nil
}

func sameFile(a, b string) bool {
	ia, err := os.Stat(a)
	if err != nil {
		return false
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ia, ib)
}

func lockHeld(dir string) (bool, error) {
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("store path is not a directory: %s", dir)
	}
	f, err := os.OpenFile(filepath.Join(dir, store.LockName), os.O_RDWR|unix.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		if uerr := unix.Flock(int(f.Fd()), unix.LOCK_UN); uerr != nil {
			return false, uerr
		}
		return false, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return true, nil
	}
	return false, err
}

func readRecord(dir string) (pidRecord, error) {
	b, err := os.ReadFile(filepath.Join(dir, recordName))
	if err != nil {
		return pidRecord{}, err
	}
	var rec pidRecord
	sawPID := false
	sawAddr := false
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return pidRecord{}, errors.New("process record is corrupt")
		}
		switch key {
		case "pid":
			n, err := strconv.Atoi(val)
			if err != nil || n <= 0 {
				return pidRecord{}, errors.New("process record is corrupt")
			}
			rec.PID = n
			sawPID = true
		case "addr":
			if val == "" || strings.ContainsAny(val, "\r\n") {
				return pidRecord{}, errors.New("process record is corrupt")
			}
			rec.Addr = val
			sawAddr = true
		default:
			return pidRecord{}, errors.New("process record is corrupt")
		}
	}
	if !sawPID || !sawAddr {
		return pidRecord{}, errors.New("process record is corrupt")
	}
	return rec, nil
}

func writeRecord(dir string, pid int, addr string) error {
	if pid <= 1 || addr == "" || strings.ContainsAny(addr, "\r\n=") {
		return errors.New("process record is invalid")
	}
	tmp, err := os.CreateTemp(dir, ".hotseat.pid.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := fmt.Fprintf(tmp, "pid=%d\naddr=%s\n", pid, addr); err != nil {
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, recordName)); err != nil {
		return err
	}
	ok = true
	return nil
}

func removeRecord(dir string, pid int) error {
	rec, err := readRecord(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if rec.PID != pid {
		return nil
	}
	err = os.Remove(filepath.Join(dir, recordName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
