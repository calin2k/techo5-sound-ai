package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/calin2k/techo5-sound-ai/internal/addon"
	"github.com/calin2k/techo5-sound-ai/internal/asp"
)

const (
	defaultDaemonBin = "/data/techo5-sound-ai/techo5-sound-ai"
	defaultOrigASP   = "/data/techo5-linux/external-asp/bin/external-asp.orig"
	defaultLockFile  = "/run/techo5-sound-ai.lock"
	defaultLogFile   = "/data/techo5-sound-ai/sound-ai.log"
	maxLogBytes      = 256 << 10
)

func isASPInvocation(arg0 string) bool {
	return filepath.Base(arg0) == "external-asp" || arg0 == "/proc/self/fd/3"
}

func main() {
	isASP := isASPInvocation(os.Args[0])
	aspFlag := flag.Bool("asp", false, "run as TECHO5 External ASP v1 stdin/stdout helper")
	daemonFlag := flag.Bool("daemon", false, "run as standalone classifier and web UI daemon")
	listenAddr := flag.String("listen", ":8182", "HTTP listen address for setup and status UI")
	cfgPath := flag.String("config", addon.DefaultConfigPath, "path to JSON configuration file")
	modelPath := flag.String("model", addon.DefaultModelPath, "path to yamnet.tflite model")
	sockPath := flag.String("sock", asp.DefaultSocketPath, "Unix datagram socket path for PCM frames")
	origASP := flag.String("orig-asp", defaultOrigASP, "optional upstream external-asp binary to chain")
	versionFlag := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println("techo5-sound-ai " + addon.Version)
		return
	}

	if (isASP || *aspFlag) && !*daemonFlag {
		ensureDaemonStarted(*sockPath)
		sink, closeSink := asp.NewUnixDatagramSink(*sockPath)
		defer closeSink()
		if err := asp.Run(os.Stdin, os.Stdout, *origASP, sink); err != nil {
			os.Exit(1)
		}
		return
	}

	rotateLogFile(defaultLogFile, maxLogBytes)

	lockF, err := acquireLock(defaultLockFile)
	if err != nil {
		// Another instance is already running.
		return
	}
	defer lockF.Close()

	d, err := addon.NewDaemon(*cfgPath, *modelPath, *sockPath)
	if err != nil {
		log.Printf("ERROR failed to initialize daemon err=%v", err)
		os.Exit(1)
	}
	defer d.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	openFirewallPort(*listenAddr)

	srv := &http.Server{
		Addr:              *listenAddr,
		Handler:           d.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("INFO techo5-sound-ai listening addr=%s sock=%s model=%s", *listenAddr, *sockPath, *modelPath)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("ERROR HTTP server error err=%v", err)
		}
	}()

	if err := d.Run(ctx); err != nil {
		log.Printf("ERROR daemon loop error err=%v", err)
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}

func openFirewallPort(listenAddr string) {
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil || port == "" {
		return
	}
	for _, tool := range []string{"iptables-legacy", "ip6tables-legacy"} {
		for _, chain := range []string{"TECHO5-IN", "TECHO5-TS"} {
			if exec.Command(tool, "-C", chain, "-p", "tcp", "--dport", port, "-j", "ACCEPT").Run() != nil {
				_ = exec.Command(tool, "-I", chain, "1", "-p", "tcp", "--dport", port, "-j", "ACCEPT").Run()
			}
		}
	}
}

func acquireLock(path string) (*os.File, error) {
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	_ = f.Truncate(0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	return f, nil
}

func rotateLogFile(path string, maxBytes int64) {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() || st.Size() <= maxBytes {
		return
	}
	_ = os.Rename(path, path+".1")
}

func isDaemonLocked(lockPath string) bool {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// ensureDaemonStarted spawns the background daemon if no live process holds defaultLockFile
// (including after a SIGKILL where a stale socket file may still exist on disk).
func ensureDaemonStarted(sockPath string) {
	if isDaemonLocked(defaultLockFile) {
		return
	}
	bin := defaultDaemonBin
	if _, err := os.Stat(bin); err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(defaultLogFile), 0700)
	rotateLogFile(defaultLogFile, maxLogBytes)
	logF, err := os.OpenFile(defaultLogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer logF.Close()

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer devNull.Close()

	cmd := exec.Command(bin, "--daemon")
	cmd.Stdin = devNull
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = cmd.Start()
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
}
