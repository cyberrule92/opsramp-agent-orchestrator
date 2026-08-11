package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Credentials describe how to authenticate to a target VM over SSH. When the
// Bastion* fields are set the connection is tunnelled through that jump host,
// which is how private-subnet targets are reached.
type Credentials struct {
	User       string
	Password   string // used when PrivateKey is empty
	PrivateKey string // PEM-encoded private key
	Passphrase string // optional passphrase for an encrypted PrivateKey
	Port       int    // default 22
	UseSudo    bool   // prefix the installer with sudo

	// Optional jump host. When BastionHost is set, targets are dialed through it.
	BastionHost       string
	BastionUser       string
	BastionPassword   string
	BastionPrivateKey string
	BastionPassphrase string
	BastionPort       int // default 22
}

// InstallParams carry everything needed to run the OpsRamp installer on a host.
type InstallParams struct {
	APIHost       string // API server host without scheme, e.g. host.api.opsramp.com
	Key           string // -K accessKey
	Secret        string // -S securityKey
	IntegrationID string // -F integration id (optional)
	EnableLogMgmt bool   // -L true
	Script        []byte // deployAgent.sh content
}

// HostOutcome is the per-host result of a deployment.
type HostOutcome struct {
	Host     string
	OK       bool
	ExitCode int
	Output   string
	Err      string
	Duration time.Duration
}

const (
	remoteScriptPath = "/tmp/opsramp-deployAgent.sh"
	dialTimeout      = 15 * time.Second
	maxOutputBytes   = 64 << 10
)

// Runner installs agents over SSH using a TOFU host-key policy.
type Runner struct {
	hostKeys ssh.HostKeyCallback
}

// NewRunner builds a Runner backed by a persisted known-hosts store.
func NewRunner(knownHostsPath string) (*Runner, error) {
	kh, err := NewKnownHosts(knownHostsPath)
	if err != nil {
		return nil, err
	}
	return &Runner{hostKeys: kh.Callback()}, nil
}

// InstallOnHost uploads the installer to a host and runs it, returning the outcome.
func (r *Runner) InstallOnHost(ctx context.Context, host string, creds Credentials, params InstallParams) HostOutcome {
	started := time.Now()
	out := HostOutcome{Host: host}

	client, cleanup, err := r.dial(ctx, host, creds)
	if err != nil {
		out.Err = err.Error()
		out.Duration = time.Since(started)
		return out
	}
	defer cleanup()
	defer client.Close()

	if len(params.Script) == 0 {
		out.Err = "no installer script available (is the OpsRamp connector configured?)"
		out.Duration = time.Since(started)
		return out
	}
	if err := uploadFile(client, remoteScriptPath, params.Script); err != nil {
		out.Err = "upload installer: " + err.Error()
		out.Duration = time.Since(started)
		return out
	}
	command := BuildInstallCommand(creds.UseSudo, params)

	output, code, err := runCommand(ctx, client, command)
	out.Output = truncate(output, maxOutputBytes)
	out.ExitCode = code
	out.Duration = time.Since(started)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	out.OK = code == 0
	if !out.OK && out.Err == "" {
		out.Err = fmt.Sprintf("installer exited with code %d", code)
	}
	return out
}

// BuildInstallCommand renders the OpsRamp deployAgent.sh invocation:
//
//	sh /tmp/opsramp-deployAgent.sh -i silent -K <key> -S <secret> -s <apiHost> -F <intg> -L true
//
// `-i silent` is required: deployAgent.sh defaults to installType=interactive,
// where it prints a banner and blocks on `read` for a y/n confirmation. Over an
// SSH exec channel that read hits EOF immediately and the script exits 1 having
// done nothing, which looks like an instant unexplained install failure.
func BuildInstallCommand(useSudo bool, p InstallParams) string {
	var b strings.Builder
	if useSudo {
		b.WriteString("sudo ")
	}
	b.WriteString("sh ")
	b.WriteString(remoteScriptPath)
	b.WriteString(" -i silent")
	b.WriteString(" -K ")
	b.WriteString(shellQuote(p.Key))
	b.WriteString(" -S ")
	b.WriteString(shellQuote(p.Secret))
	if p.APIHost != "" {
		b.WriteString(" -s ")
		b.WriteString(shellQuote(p.APIHost))
	}
	if p.IntegrationID != "" {
		b.WriteString(" -F ")
		b.WriteString(shellQuote(p.IntegrationID))
	}
	if p.EnableLogMgmt {
		b.WriteString(" -L true")
	}
	return b.String()
}

// ProbeHost runs a read-only readiness check over SSH: it never changes the
// host. apiHost, when set, is tested for reachability from the target so a bad
// egress path is caught before an install is attempted.
func (r *Runner) ProbeHost(ctx context.Context, host string, creds Credentials, apiHost string) HostOutcome {
	started := time.Now()
	out := HostOutcome{Host: host}

	client, cleanup, err := r.dial(ctx, host, creds)
	if err != nil {
		// Unreachable is itself the most important preflight result.
		out.Output = "SSH=fail"
		out.Err = err.Error()
		out.Duration = time.Since(started)
		return out
	}
	defer cleanup()
	defer client.Close()

	output, code, err := runCommand(ctx, client, buildProbeCommand(apiHost))
	out.Output = "SSH=ok\n" + truncate(output, maxOutputBytes)
	out.ExitCode = code
	out.Duration = time.Since(started)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	// A probe "succeeds" when it ran; the UI grades the individual checks.
	out.OK = code == 0
	return out
}

// buildProbeCommand emits one KEY=VALUE line per readiness check on stdout. It
// is read-only: it inspects the host but never modifies it.
func buildProbeCommand(apiHost string) string {
	// Reachability tries curl, then nc, then a bash /dev/tcp fallback.
	reach := "echo API=skip"
	if apiHost != "" {
		h := shellQuote(apiHost)
		reach = "if command -v curl >/dev/null 2>&1; then curl -sS -o /dev/null -m 6 https://" + h +
			" && echo API=ok || echo API=fail; " +
			"elif command -v nc >/dev/null 2>&1; then nc -z -w5 " + h + " 443 && echo API=ok || echo API=fail; " +
			"else (exec 3<>/dev/tcp/" + h + "/443) 2>/dev/null && { echo API=ok; exec 3>&-; } || echo API=fail; fi"
	}
	script := strings.Join([]string{
		`echo "OS=$(. /etc/os-release 2>/dev/null; echo "${PRETTY_NAME:-$(uname -s)}")"`,
		`echo "KERNEL=$(uname -r 2>/dev/null)"`,
		`echo "ARCH=$(uname -m 2>/dev/null)"`,
		`if command -v sudo >/dev/null 2>&1; then if sudo -n true 2>/dev/null; then echo "SUDO=nopasswd"; else echo "SUDO=password-required"; fi; else echo "SUDO=absent"; fi`,
		`if [ -d /opt/opsramp/agent ] || command -v opsramp-agent >/dev/null 2>&1; then echo "AGENT=present"; else echo "AGENT=absent"; fi`,
		`echo "DISK_ROOT_MB=$(df -Pm / 2>/dev/null | awk 'NR==2{print $4}')"`,
		reach,
	}, "\n")
	return "sh -c " + shellQuote(script)
}

// UninstallOnHost detects and removes the OpsRamp agent. customCmd, when set,
// overrides the built-in detection for non-standard layouts.
func (r *Runner) UninstallOnHost(ctx context.Context, host string, creds Credentials, customCmd string) HostOutcome {
	started := time.Now()
	out := HostOutcome{Host: host}

	client, cleanup, err := r.dial(ctx, host, creds)
	if err != nil {
		out.Err = err.Error()
		out.Duration = time.Since(started)
		return out
	}
	defer cleanup()
	defer client.Close()

	output, code, err := runCommand(ctx, client, buildUninstallCommand(creds.UseSudo, customCmd))
	out.Output = truncate(output, maxOutputBytes)
	out.ExitCode = code
	out.Duration = time.Since(started)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	out.OK = code == 0
	if !out.OK && out.Err == "" {
		out.Err = fmt.Sprintf("uninstall exited with code %d", code)
	}
	return out
}

// buildUninstallCommand removes the OpsRamp agent using the same commands the
// OpsRamp installer itself uses to replace an existing agent: `dpkg -P` on
// Debian, `rpm -e` on RHEL, `pkg remove` on FreeBSD, each followed by removing
// /opt/opsramp/agent. The package is named "opsramp-agent".
func buildUninstallCommand(useSudo bool, customCmd string) string {
	sudo := ""
	if useSudo {
		sudo = "sudo "
	}
	if strings.TrimSpace(customCmd) != "" {
		return sudo + customCmd
	}
	script := strings.Join([]string{
		`if command -v dpkg >/dev/null 2>&1 && dpkg -l opsramp-agent >/dev/null 2>&1; then ` +
			sudo + `dpkg -P opsramp-agent 2>&1; ` + sudo + `rm -rf /opt/opsramp/agent; ` + sudo + `rmdir /opt/opsramp 2>/dev/null;`,
		`elif command -v rpm >/dev/null 2>&1 && rpm -qa 2>/dev/null | grep -q opsramp-agent; then ` +
			sudo + `rpm -e opsramp-agent 2>&1; ` + sudo + `rm -rf /opt/opsramp/agent; ` + sudo + `rmdir /opt/opsramp 2>/dev/null;`,
		`elif command -v pkg >/dev/null 2>&1 && pkg info opsramp-agent >/dev/null 2>&1; then ` +
			sudo + `pkg remove -y opsramp-agent 2>&1; ` + sudo + `rm -rf /opt/opsramp/agent 2>/dev/null;`,
		`elif [ -d /opt/opsramp/agent ]; then ` +
			`[ -x /opt/opsramp/agent/opsramp-agent ] && ` + sudo + `/opt/opsramp/agent/opsramp-agent uninstall 2>&1; ` +
			sudo + `rm -rf /opt/opsramp/agent; ` + sudo + `rmdir /opt/opsramp 2>/dev/null;`,
		`else echo "opsramp agent not found on host"; exit 0; fi`,
	}, "\n")
	return "sh -c " + shellQuote(script)
}

// dial opens an SSH connection to host, optionally tunnelling through a bastion.
// The returned cleanup closes any intermediate bastion connection and must be
// called after the target client is closed.
func (r *Runner) dial(ctx context.Context, host string, creds Credentials) (*ssh.Client, func(), error) {
	noop := func() {}
	targetAuth, err := authFrom(creds.PrivateKey, creds.Passphrase, creds.Password)
	if err != nil {
		return nil, noop, err
	}
	targetCfg := r.clientConfig(creds.User, targetAuth)
	targetAddr := net.JoinHostPort(host, strconv.Itoa(portOr(creds.Port)))

	// Direct connection when no bastion is configured.
	if strings.TrimSpace(creds.BastionHost) == "" {
		d := net.Dialer{Timeout: dialTimeout}
		conn, err := d.DialContext(ctx, "tcp", targetAddr)
		if err != nil {
			return nil, noop, fmt.Errorf("connect: %w", err)
		}
		client, err := clientFromConn(conn, targetAddr, targetCfg)
		if err != nil {
			return nil, noop, err
		}
		return client, noop, nil
	}

	// Tunnel: connect to the bastion, then dial the target through it.
	bastionUser := creds.BastionUser
	if bastionUser == "" {
		bastionUser = creds.User
	}
	bAuth, err := authFrom(creds.BastionPrivateKey, creds.BastionPassphrase, creds.BastionPassword)
	if err != nil {
		return nil, noop, fmt.Errorf("bastion: %w", err)
	}
	bCfg := r.clientConfig(bastionUser, bAuth)
	bAddr := net.JoinHostPort(creds.BastionHost, strconv.Itoa(portOr(creds.BastionPort)))

	d := net.Dialer{Timeout: dialTimeout}
	bConn, err := d.DialContext(ctx, "tcp", bAddr)
	if err != nil {
		return nil, noop, fmt.Errorf("connect bastion: %w", err)
	}
	bastion, err := clientFromConn(bConn, bAddr, bCfg)
	if err != nil {
		return nil, noop, fmt.Errorf("bastion handshake: %w", err)
	}
	tunnelConn, err := bastion.Dial("tcp", targetAddr)
	if err != nil {
		bastion.Close()
		return nil, noop, fmt.Errorf("dial target via bastion: %w", err)
	}
	client, err := clientFromConn(tunnelConn, targetAddr, targetCfg)
	if err != nil {
		bastion.Close()
		return nil, noop, err
	}
	return client, func() { bastion.Close() }, nil
}

func (r *Runner) clientConfig(user string, auth []ssh.AuthMethod) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: r.hostKeys, // TOFU: pin on first use, reject on change
		Timeout:         dialTimeout,
	}
}

func clientFromConn(conn net.Conn, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}
	return ssh.NewClient(sshConn, chans, reqs), nil
}

func portOr(p int) int {
	if p == 0 {
		return 22
	}
	return p
}

// authFrom builds SSH auth methods from a private key (preferred) or password.
func authFrom(privateKey, passphrase, password string) ([]ssh.AuthMethod, error) {
	if strings.TrimSpace(privateKey) != "" {
		var signer ssh.Signer
		var err error
		if passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(privateKey), []byte(passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(privateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	if password != "" {
		return []ssh.AuthMethod{
			ssh.Password(password),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				ans := make([]string, len(questions))
				for i := range questions {
					ans[i] = password
				}
				return ans, nil
			}),
		}, nil
	}
	return nil, errors.New("no SSH credentials provided (password or private key required)")
}

func uploadFile(client *ssh.Client, path string, content []byte) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdin = bytes.NewReader(content)
	cmd := fmt.Sprintf("cat > %s && chmod 700 %s", path, path)
	if out, err := session.CombinedOutput(cmd); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runCommand(ctx context.Context, client *ssh.Client, cmd string) (string, int, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", -1, err
	}
	defer session.Close()

	var buf bytes.Buffer
	session.Stdout = &buf
	session.Stderr = &buf

	if err := session.Start(cmd); err != nil {
		return "", -1, err
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return buf.String(), -1, ctx.Err()
	case werr := <-done:
		if werr == nil {
			return buf.String(), 0, nil
		}
		var exitErr *ssh.ExitError
		if errors.As(werr, &exitErr) {
			return buf.String(), exitErr.ExitStatus(), nil
		}
		return buf.String(), -1, werr
	}
}

// shellQuote single-quotes a value for safe use in a POSIX shell command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n...[truncated]"
}
