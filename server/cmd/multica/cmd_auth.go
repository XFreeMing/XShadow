package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/cli"
)

// loginTokenPrefixes are the token prefixes `multica login --token` accepts.
// The CLI used to hardcode `mul_` only, which made it impossible to log in
// with a Multica Cloud Node PAT (`mcn_`) even though the server happily
// authenticates both kinds. Keep this list in sync with the prefix branches
// in server/internal/middleware/auth.go.
var loginTokenPrefixes = []string{"mul_", auth.CloudPATPrefix}

// validateLoginTokenPrefix returns nil if token starts with one of the
// CLI-recognised PAT prefixes, or an error describing the accepted set.
// Extracted so the prefix list has one obvious test surface.
func validateLoginTokenPrefix(token string) error {
	for _, p := range loginTokenPrefixes {
		if strings.HasPrefix(token, p) {
			return nil
		}
	}
	return fmt.Errorf("invalid token format: must start with %s", strings.Join(loginTokenPrefixes, " or "))
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Authenticate multica with Multica",
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current authentication status",
	RunE:  runAuthStatus,
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored authentication token",
	RunE:  runAuthLogout,
}

// callbackHostFlag lets users override the host that goes into the OAuth
// cli_callback URL when the auto-detected address isn't one the browser can
// reach. The value must pass isSupportedCallbackHost; hostnames other than
// localhost are rejected because the web login never accepts them.
const callbackHostFlag = "callback-host"

const callbackHostFlagHelp = "Callback host reachable from the browser: localhost, 127.0.0.1, or an RFC 1918 IPv4 address. For SSH-only machines, use the printed tunnel hint instead."

func init() {
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authLogoutCmd)
}

func resolveToken(cmd *cobra.Command) string {
	if v := strings.TrimSpace(os.Getenv("MULTICA_TOKEN")); v != "" {
		return v
	}
	// Inside a daemon-managed task, never fall back to the user-global config
	// token: that silent fallback is how agent writes land as the wrong actor.
	// inDaemonManagedExecutionContext already covers the MULTICA_DAEMON_PORT
	// signal for subprocesses that lost MULTICA_AGENT_ID / MULTICA_TASK_ID.
	if inDaemonManagedExecutionContext() {
		return ""
	}
	profile := resolveProfile(cmd)
	cfg, _ := cli.LoadCLIConfigForProfile(profile)
	return cfg.Token
}

func resolveAppURL(cmd *cobra.Command) string {
	for _, key := range []string{"MULTICA_APP_URL", "FRONTEND_ORIGIN"} {
		if val := strings.TrimSpace(os.Getenv(key)); val != "" {
			return strings.TrimRight(val, "/")
		}
	}
	profile := resolveProfile(cmd)
	cfg, err := cli.LoadCLIConfigForProfile(profile)
	if err == nil && cfg.AppURL != "" {
		return strings.TrimRight(cfg.AppURL, "/")
	}
	fmt.Fprintln(os.Stderr, "No app URL configured. Run 'multica setup' first.")
	os.Exit(1)
	return "" // unreachable
}

func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "linux":
		cmd = "xdg-open"
		args = []string{url}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return exec.Command(cmd, args...).Start()
}

func runAuthLogin(cmd *cobra.Command, args []string) error {
	if err := requireHumanLocalCommand("login"); err != nil {
		return err
	}
	if cmd.Flags().Changed("token") {
		tokenFlag, _ := cmd.Flags().GetString("token")
		// `--token mul_xxx` (space form) is what users actually type — that's
		// the form from the docs and from #1994. NoOptDefVal prevents pflag
		// from consuming the next arg as the flag value, so it lands here as
		// a positional. Promote it to the token value.
		if tokenFlag == tokenPromptSentinel && len(args) == 1 {
			tokenFlag = args[0]
		}
		return runAuthLoginToken(cmd, tokenFlag)
	}
	return runAuthLoginBrowser(cmd)
}

// resolveCallbackBinding picks the host that goes into the `cli_callback`
// URL and the interface the CLI should bind its local HTTP listener to.
//
// The browser running the login flow is on the *server's* machine (or
// wherever the user clicked the link), not on the CLI host. That means the
// callback URL must resolve to an address the browser can actually reach,
// which is different in each topology:
//
//   - hosted / public app URL: browser and CLI are on the same machine,
//     localhost works.
//   - self-host, CLI on server box: same as above.
//   - self-host, CLI on a different LAN box: the callback URL must point at
//     the CLI's own LAN IP, not the server's.
//   - routes through a virtual adapter: the detected address may not be a
//     supported callback host; ask the user to supply --callback-host.
//
// detectOutbound is injected so tests can exercise the routing decisions
// without real network calls.
func resolveCallbackBinding(flagHost, serverURL, appURL string, detectOutbound func(string) net.IP) (callbackHost, bindAddr string, err error) {
	// Explicit flag always wins. Bind on all interfaces so the browser can
	// reach us regardless of which interface the host name resolves to.
	if h := strings.TrimSpace(flagHost); h != "" {
		if !isSupportedCallbackHost(h) {
			return "", "", callbackHostError(h)
		}
		return h, "0.0.0.0", nil
	}

	appIP := urlPrivateIP(appURL)
	if appIP == nil {
		// Public hostname, FQDN without private-IP mapping, or parse error.
		// Loopback is the only safe default — on hosted/public setups the
		// browser and CLI live on the same machine.
		return "localhost", "127.0.0.1", nil
	}

	// app_url is a private LAN IP. Figure out whether the CLI is on that
	// same box or a different one by asking the kernel which local address
	// it would use to reach the server. Same box → loopback is fine.
	// Different box → use the CLI's outbound IP so the browser can reach us.
	cliIP := detectOutbound(serverURL)
	if cliIP == nil {
		// Detection failed (offline, unreachable server, etc.). Fall back to
		// the app IP — preserves the pre-existing same-machine behaviour.
		return appIP.String(), "0.0.0.0", nil
	}
	if cliIP.Equal(appIP) {
		return "localhost", "127.0.0.1", nil
	}
	if !isSupportedCallbackHost(cliIP.String()) {
		return "", "", callbackHostError(cliIP.String())
	}
	return cliIP.String(), "0.0.0.0", nil
}

// Keep this host policy aligned with validateCliCallback in packages/views/auth.
func isSupportedCallbackHost(host string) bool {
	if host == "localhost" || host == "127.0.0.1" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && !strings.Contains(host, ":") && ip.To4() != nil && ip.IsPrivate()
}

func callbackHostError(host string) error {
	return fmt.Errorf("cannot use %q as the login callback host: the web app accepts only localhost, 127.0.0.1, or RFC 1918 IPv4 addresses. If the browser runs on this machine, retry with --callback-host 127.0.0.1; otherwise use --callback-host <this-machine-LAN-IP> or sign in with multica login --token", host)
}

// urlPrivateIP returns the hostname of rawURL parsed as an RFC 1918 IP, or
// nil if the URL is unparsable or the host is not a private literal.
func urlPrivateIP(rawURL string) net.IP {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || ip.To4() == nil || !ip.IsPrivate() {
		return nil
	}
	return ip
}

// detectOutboundIP returns the local IPv4 address the OS would use to reach
// serverURL, or nil if detection fails. The UDP dial does not send packets —
// it just causes the kernel to pick a source IP for the destination route.
func detectOutboundIP(serverURL string) net.IP {
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.Hostname() == "" {
		return nil
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	conn, err := net.Dial("udp4", net.JoinHostPort(parsed.Hostname(), port))
	if err != nil {
		return nil
	}
	defer conn.Close()
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || local.IP == nil {
		return nil
	}
	// Normalise to 4-byte form so Equal() comparisons match net.ParseIP
	// output consistently.
	if v4 := local.IP.To4(); v4 != nil {
		return v4
	}
	return local.IP
}

func runAuthLoginBrowser(cmd *cobra.Command) error {
	serverURL := resolveHumanServerURL(cmd)
	appURL := resolveAppURL(cmd)

	flagHost := callbackHostFlagValue(cmd)
	callbackHost, bindAddr, err := resolveCallbackBinding(flagHost, serverURL, appURL, detectOutboundIP)
	if err != nil {
		return err
	}

	// Pin to "tcp4" — a bare "tcp" on macOS can produce an IPv6-only socket
	// that IPv4 clients (including browsers resolving localhost → 127.0.0.1)
	// cannot reach. The callback host is always localhost or an IPv4 literal,
	// so an IPv4 listener is what the browser actually needs.
	listener, err := net.Listen("tcp4", bindAddr+":0")
	if err != nil {
		return fmt.Errorf("could not start the local login callback server (used to receive the browser sign-in); a firewall or another process may be blocking local ports: %w", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	callbackURL := fmt.Sprintf("http://%s:%d/callback", callbackHost, port)

	// Generate a random state parameter for CSRF protection.
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return fmt.Errorf("failed to generate state: %w", err)
	}
	state := hex.EncodeToString(stateBytes)

	loginURL := fmt.Sprintf("%s/login?cli_callback=%s&cli_state=%s", appURL, url.QueryEscape(callbackURL), url.QueryEscape(state))

	// Channel to receive the JWT from the browser callback.
	jwtCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}
		returnedState := r.URL.Query().Get("state")
		if returnedState != state {
			http.Error(w, "invalid state parameter", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(callbackSuccessHTML))
		jwtCh <- token
	})

	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	defer srv.Close()

	// Open the browser.
	fmt.Fprintln(os.Stderr, "Opening browser to authenticate...")
	if err := openBrowser(loginURL); err != nil {
		fmt.Fprintf(os.Stderr, "Could not open browser automatically.\n")
	}
	fmt.Fprint(os.Stderr, browserLoginInstructions(loginURL, callbackHost, port, runningInSSHSession()))

	// Wait for the JWT from the callback (timeout 5 minutes).
	var jwtToken string
	select {
	case jwtToken = <-jwtCh:
	case err := <-errCh:
		return fmt.Errorf("local server error: %w", err)
	case <-time.After(5 * time.Minute):
		return fmt.Errorf("timed out waiting for authentication")
	}

	// Use the JWT to create a PAT via the existing API.
	client := cli.NewAPIClient(serverURL, "", jwtToken)

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	patName := fmt.Sprintf("CLI (%s)", hostname)
	expiresInDays := 90

	var patResp struct {
		Token string `json:"token"`
	}
	err = client.PostJSON(ctx, "/api/tokens", map[string]any{
		"name":            patName,
		"expires_in_days": expiresInDays,
	}, &patResp)
	if err != nil {
		return cli.WithUserMessageUnlessNetwork("Sign-in did not complete: the server could not issue an access token for the CLI. Run `multica login` again.", err)
	}

	// Verify the PAT works.
	patClient := cli.NewAPIClient(serverURL, "", patResp.Token)
	var me struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := patClient.GetJSON(ctx, "/api/me", &me); err != nil {
		return cli.WithUserMessageUnlessNetwork("Sign-in did not complete: the server did not accept the new credential. Run `multica login` again.", err)
	}

	// Save to config. Reset workspace data on every login — the user or
	// server may have changed, so stale workspaces must not persist.
	profile := resolveProfile(cmd)
	cfg, _ := cli.LoadCLIConfigForProfile(profile)
	cfg.WorkspaceID = ""
	cfg.Token = patResp.Token
	cfg.ServerURL = serverURL
	cfg.AppURL = appURL
	if err := cli.SaveCLIConfigForProfile(cfg, profile); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Authenticated as %s (%s)\nToken saved to config.\n", me.Name, me.Email)
	return nil
}

func runningInSSHSession() bool {
	for _, key := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

func callbackHostFlagValue(cmd *cobra.Command) string {
	for c := cmd; c != nil; c = c.Parent() {
		if value := nonEmptyFlagValue(c.Flags(), callbackHostFlag); value != "" {
			return value
		}
		if value := nonEmptyFlagValue(c.PersistentFlags(), callbackHostFlag); value != "" {
			return value
		}
		if value := nonEmptyFlagValue(c.InheritedFlags(), callbackHostFlag); value != "" {
			return value
		}
	}
	return ""
}

func nonEmptyFlagValue(flags *pflag.FlagSet, name string) string {
	if flag := flags.Lookup(name); flag != nil {
		return strings.TrimSpace(flag.Value.String())
	}
	return ""
}

func callbackHostIsLoopback(host string) bool {
	h := strings.Trim(strings.TrimSpace(host), "[]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func browserLoginInstructions(loginURL, callbackHost string, port int, remoteSSH bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "If the browser didn't open, visit:\n  %s\n", loginURL)
	if remoteSSH && callbackHostIsLoopback(callbackHost) {
		fmt.Fprintf(&b, "\nRemote SSH session detected. Before opening that URL on your local computer, forward the callback port in another terminal:\n  ssh -L %d:127.0.0.1:%d <user>@<remote-host>\nThen open the URL above in your local browser.\n", port, port)
	}
	fmt.Fprintln(&b, "\nWaiting for authentication...")
	return b.String()
}

func runAuthLoginToken(cmd *cobra.Command, providedToken string) error {
	// The prompt sentinel is what pflag substitutes for `--token` with no
	// value (see loginCmd init); treat it the same as an empty string so we
	// fall through to the interactive prompt.
	if providedToken == tokenPromptSentinel {
		providedToken = ""
	}
	token := strings.TrimSpace(providedToken)
	if token == "" {
		fmt.Print("Enter your personal access token: ")
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			return fmt.Errorf("no input")
		}
		token = strings.TrimSpace(scanner.Text())
	}
	if token == "" {
		return fmt.Errorf("token is required")
	}
	if err := validateLoginTokenPrefix(token); err != nil {
		return err
	}

	serverURL := resolveLoginTokenServerURL(cmd)
	client := cli.NewAPIClient(serverURL, "", token)

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var me struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := client.GetJSON(ctx, "/api/me", &me); err != nil {
		return cli.WithUserMessageUnlessNetwork("Could not sign in with that token — make sure it is valid and not expired, then run `multica login --token <token>` again.", err)
	}

	profile := resolveProfile(cmd)
	cfg, _ := cli.LoadCLIConfigForProfile(profile)
	cfg.WorkspaceID = ""
	cfg.Token = token
	cfg.ServerURL = serverURL
	if cfg.AppURL == "" && serverURL == defaultCloudServerURL {
		cfg.AppURL = defaultCloudAppURL
	}
	if err := cli.SaveCLIConfigForProfile(cfg, profile); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Authenticated as %s (%s)\nToken saved to config.\n", me.Name, me.Email)
	return nil
}

func runAuthStatus(cmd *cobra.Command, _ []string) error {
	if err := requireTaskLocalConfigRoot(); err != nil {
		return err
	}
	taskContext := inDaemonManagedExecutionContext()
	token := resolveToken(cmd)
	if taskContext && !strings.HasPrefix(token, "mat_") {
		return fmt.Errorf("agent execution context requires MULTICA_TOKEN to be a task-scoped mat_ token")
	}
	serverURL := resolveServerURL(cmd)

	if token == "" {
		fmt.Fprintln(os.Stderr, "Not authenticated. Run 'multica login' to authenticate.")
		return nil
	}

	client := cli.NewAPIClient(serverURL, "", token)

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var me struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := client.GetJSON(ctx, "/api/me", &me); err != nil {
		fmt.Fprintf(os.Stderr, "Token is invalid or expired: %v\nRun 'multica login' to re-authenticate.\n", err)
		return nil
	}

	if taskContext {
		fmt.Fprintf(os.Stderr, "Server:  %s\nUser:    %s (%s)\n", serverURL, me.Name, me.Email)
		return nil
	}

	prefix := token
	if len(prefix) > 12 {
		prefix = prefix[:12] + "..."
	}
	fmt.Fprintf(os.Stderr, "Server:  %s\nUser:    %s (%s)\nToken:   %s\n", serverURL, me.Name, me.Email, prefix)
	return nil
}

// The .brand SVG is written by `node scripts/brand/sync.mjs` from
// packages/ui/lib/baishadow-brand.ts; CI fails when it drifts.
const callbackSuccessHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>BaiShadow — Authenticated</title>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  @media (prefers-color-scheme: dark) {
    :root { --bg: #0b0b0f; --card-bg: #16161d; --border: rgba(255,255,255,0.10); --fg: #f5f5f5; --fg2: #a1a1aa; --accent: #22c55e; --accent-bg: rgba(34,197,94,0.12); }
  }
  @media (prefers-color-scheme: light) {
    :root { --bg: #f8f8fa; --card-bg: #ffffff; --border: rgba(0,0,0,0.08); --fg: #0f0f12; --fg2: #71717a; --accent: #16a34a; --accent-bg: rgba(22,163,74,0.08); }
  }
  body { font-family: -apple-system, "Segoe UI", Helvetica, Arial, sans-serif; background: var(--bg); color: var(--fg); display: flex; align-items: center; justify-content: center; min-height: 100vh; }
  .card { width: 100%; max-width: 380px; border: 1px solid var(--border); border-radius: 12px; background: var(--card-bg); padding: 40px 32px; text-align: center; }
  .icon-wrap { width: 48px; height: 48px; margin: 0 auto 24px; background: var(--accent-bg); border-radius: 50%; display: flex; align-items: center; justify-content: center; }
  .icon-wrap svg { width: 24px; height: 24px; color: var(--accent); }
  .brand { display: flex; align-items: center; justify-content: center; gap: 6px; margin-bottom: 8px; }
  .brand svg { display: block; color: var(--fg); }
  h1 { font-size: 20px; font-weight: 600; margin-bottom: 8px; }
  p { font-size: 14px; color: var(--fg2); line-height: 1.5; }
  .hint { margin-top: 24px; font-size: 13px; color: var(--fg2); opacity: 0.7; }
</style>
</head>
<body>
  <div class="card">
    <div class="icon-wrap">
      <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M4.5 12.75l6 6 9-13.5"/></svg>
    </div>
    <div class="brand"><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 264 100" role="img" aria-label="百影 BaiShadow" height="32" width="84"><defs><linearGradient id="wordmark" gradientUnits="userSpaceOnUse" x1="12" y1="6" x2="88" y2="94"><stop offset="0" stop-color="#12ADAA"/><stop offset="0.48" stop-color="#087FAD"/><stop offset="1" stop-color="#4C49AE"/></linearGradient></defs><g fill="currentColor" fill-rule="evenodd"><path fill="url(#wordmark)" d="M50.18 12.24C61.66 12.24 71.09 21.424 71.582 33.396C72.156 43.728 66.416 52.994 56.74 56.684C59.774 50.862 58.38 45.696 54.116 42.744C50.016 39.874 45.588 40.776 42.062 40.94C34.108 41.35 29.844 36.676 30.254 30.608C30.664 22.572 38.864 12.24 50.18 12.24ZM86.941 76.536C81.201 86.478 68.533 90.052 57.919 84.493C48.684 79.824 43.529 70.22 45.172 59.995C48.697 65.534 53.868 66.909 58.556 64.693C63.092 62.577 64.524 58.291 66.145 55.155C69.767 48.062 75.947 46.706 80.997 50.095C87.751 54.469 92.599 66.736 86.941 76.536ZM12.879 76.224C7.139 66.282 10.377 53.524 20.499 47.111C29.16 41.448 40.055 41.786 48.088 48.321C41.529 48.604 37.752 52.395 37.328 57.563C36.892 62.549 39.888 65.933 41.793 68.905C46.125 75.588 44.209 81.618 38.749 84.297C31.585 87.959 18.537 86.024 12.879 76.224Z"/><path d="M123.275 10L177.425 10Q181.7 10 181.7 14.275L181.7 14.275Q181.7 18.55 177.425 18.55L123.275 18.55Q119 18.55 119 14.275L119 14.275Q119 10 123.275 10ZM146.55 18.55L156.05 18.55L151.3 28.05L141.8 28.05ZM129.45 28.05L171.25 28.05Q176.95 28.05 176.95 33.75L176.95 64.15Q176.95 69.85 171.25 69.85L129.45 69.85Q123.75 69.85 123.75 64.15L123.75 33.75Q123.75 28.05 129.45 28.05ZM135.15 37.55L165.55 37.55Q167.45 37.55 167.45 39.45L167.45 43.25Q167.45 45.15 165.55 45.15L135.15 45.15Q133.25 45.15 133.25 43.25L133.25 39.45Q133.25 37.55 135.15 37.55ZM135.15 53.7L165.55 53.7Q167.45 53.7 167.45 55.6L167.45 59.4Q167.45 61.3 165.55 61.3L135.15 61.3Q133.25 61.3 133.25 59.4L133.25 55.6Q133.25 53.7 135.15 53.7ZM192.75 10L223.15 10Q227.9 10 227.9 14.75L227.9 27.1Q227.9 31.85 223.15 31.85L192.75 31.85Q188 31.85 188 27.1L188 14.75Q188 10 192.75 10ZM198.925 15.7L216.975 15.7Q218.4 15.7 218.4 17.125L218.4 18.075Q218.4 19.5 216.975 19.5L198.925 19.5Q197.5 19.5 197.5 18.075L197.5 17.125Q197.5 15.7 198.925 15.7ZM198.925 23.3L216.975 23.3Q218.4 23.3 218.4 24.725L218.4 25.675Q218.4 27.1 216.975 27.1L198.925 27.1Q197.5 27.1 197.5 25.675L197.5 24.725Q197.5 23.3 198.925 23.3ZM203.2 31.85L212.7 31.85Q212.7 31.85 212.7 31.85L212.7 35.65Q212.7 35.65 212.7 35.65L203.2 35.65Q203.2 35.65 203.2 35.65L203.2 31.85Q203.2 31.85 203.2 31.85ZM189.425 35.65L226.475 35.65Q229.8 35.65 229.8 38.975L229.8 38.975Q229.8 42.3 226.475 42.3L189.425 42.3Q186.1 42.3 186.1 38.975L186.1 38.975Q186.1 35.65 189.425 35.65ZM192.75 45.15L223.15 45.15Q226.95 45.15 226.95 48.95L226.95 54.65Q226.95 58.45 223.15 58.45L192.75 58.45Q188.95 58.45 188.95 54.65L188.95 48.95Q188.95 45.15 192.75 45.15ZM198.925 49.9L216.975 49.9Q218.4 49.9 218.4 51.325L218.4 52.275Q218.4 53.7 216.975 53.7L198.925 53.7Q197.5 53.7 197.5 52.275L197.5 51.325Q197.5 49.9 198.925 49.9ZM203.2 58.45L212.7 58.45L212.7 67Q212.7 71.75 207.95 71.75L200.35 71.75Q197.5 71.75 197.5 68.9L197.5 66.05L203.2 66.05ZM190.85 61.3Q192.75 59.4 195.6 61.3L197.5 63.2L192.75 70.8Q191.8 72.7 188.95 70.8L187.05 68.9Q186.1 67.95 188 65.1ZM218.4 60.35Q221.25 59.4 223.15 62.25L227.9 67.95Q229.8 70.8 226 71.75L222.2 71.75Q220.3 71.75 219.35 69.85L215.55 64.15Q214.6 61.3 218.4 60.35ZM248.8 10Q251.65 8.1 254.5 10.95L257.35 13.8Q259.25 16.65 256.4 19.5L238.35 34.7Q235.5 36.6 232.65 33.75L230.75 30.9Q228.85 28.05 231.7 25.2ZM248.8 30.9Q251.65 29 254.5 31.85L257.35 34.7Q259.25 37.55 256.4 40.4L238.35 55.6Q235.5 57.5 232.65 54.65L230.75 51.8Q228.85 48.95 231.7 46.1ZM248.8 51.8Q251.65 49.9 254.5 52.75L257.35 55.6Q259.25 58.45 256.4 61.3L238.35 76.5Q235.5 78.4 232.65 75.55L230.75 72.7Q228.85 69.85 231.7 67Z"/><path opacity=".82" d="M120.442 97.0V79.867H127.395Q129.294 79.867 130.569 80.426Q131.845 80.985 132.485 81.969Q133.125 82.954 133.125 84.237Q133.125 85.236 132.719 85.996Q132.313 86.756 131.608 87.245Q130.902 87.734 129.991 87.933V88.109Q130.99 88.148 131.856 88.657Q132.723 89.167 133.257 90.08Q133.792 90.992 133.792 92.248Q133.792 93.611 133.114 94.687Q132.436 95.763 131.119 96.381Q129.801 97.0 127.882 97.0ZM124.226 93.917H127.055Q128.51 93.917 129.191 93.356Q129.872 92.795 129.872 91.851Q129.872 91.153 129.539 90.627Q129.207 90.101 128.595 89.806Q127.982 89.511 127.14 89.511H124.226ZM124.226 86.998H126.787Q127.505 86.998 128.069 86.748Q128.634 86.498 128.959 86.031Q129.283 85.564 129.283 84.911Q129.283 84.012 128.642 83.466Q128.0 82.92 126.856 82.92H124.226ZM140.783 97.239Q139.556 97.239 138.592 96.813Q137.627 96.387 137.078 95.539Q136.529 94.69 136.529 93.418Q136.529 92.344 136.913 91.617Q137.298 90.89 137.972 90.44Q138.646 89.991 139.513 89.755Q140.38 89.518 141.344 89.433Q142.449 89.329 143.125 89.226Q143.801 89.123 144.114 88.909Q144.427 88.694 144.427 88.29V88.236Q144.427 87.738 144.207 87.393Q143.986 87.048 143.568 86.865Q143.149 86.682 142.544 86.682Q141.938 86.682 141.477 86.865Q141.016 87.048 140.72 87.363Q140.423 87.679 140.299 88.09L136.897 87.591Q137.189 86.478 137.947 85.671Q138.704 84.864 139.876 84.424Q141.049 83.983 142.569 83.983Q143.683 83.983 144.692 84.247Q145.701 84.511 146.481 85.045Q147.261 85.58 147.709 86.393Q148.156 87.206 148.156 88.309V97.0H144.634V95.207H144.526Q144.195 95.835 143.671 96.29Q143.147 96.745 142.429 96.992Q141.711 97.239 140.783 97.239ZM141.879 94.701Q142.625 94.701 143.206 94.402Q143.786 94.103 144.122 93.586Q144.457 93.069 144.457 92.414V91.103Q144.293 91.2 144.01 91.287Q143.726 91.374 143.382 91.444Q143.038 91.513 142.698 91.565Q142.358 91.617 142.074 91.659Q141.461 91.744 141.013 91.946Q140.565 92.148 140.324 92.474Q140.083 92.801 140.083 93.275Q140.083 93.74 140.315 94.058Q140.548 94.375 140.952 94.538Q141.355 94.701 141.879 94.701ZM152.133 97.0V84.144H155.862V97.0ZM153.994 82.556Q153.176 82.556 152.593 82.011Q152.011 81.466 152.011 80.703Q152.011 79.925 152.593 79.386Q153.176 78.847 153.994 78.847Q154.814 78.847 155.397 79.385Q155.981 79.923 155.981 80.703Q155.981 81.467 155.397 82.011Q154.814 82.556 153.994 82.556ZM166.334 97.239Q164.236 97.239 162.679 96.6Q161.122 95.961 160.255 94.688Q159.387 93.415 159.344 91.514H162.999Q163.061 92.36 163.486 92.931Q163.911 93.501 164.634 93.787Q165.356 94.074 166.289 94.074Q167.182 94.074 167.835 93.821Q168.487 93.569 168.845 93.114Q169.202 92.66 169.202 92.065Q169.202 91.521 168.878 91.15Q168.555 90.778 167.919 90.504Q167.283 90.231 166.341 90.015L164.495 89.571Q162.298 89.051 161.047 87.91Q159.797 86.77 159.797 84.866Q159.797 83.297 160.646 82.121Q161.495 80.945 162.975 80.291Q164.454 79.637 166.357 79.637Q168.301 79.637 169.739 80.298Q171.176 80.96 171.975 82.138Q172.773 83.317 172.796 84.863H169.147Q169.058 83.879 168.329 83.337Q167.6 82.794 166.333 82.794Q165.483 82.794 164.892 83.03Q164.301 83.265 163.998 83.678Q163.695 84.092 163.695 84.62Q163.695 85.196 164.044 85.585Q164.392 85.974 164.992 86.227Q165.593 86.479 166.333 86.652L167.845 87.011Q169.006 87.27 169.959 87.704Q170.912 88.138 171.6 88.755Q172.288 89.372 172.657 90.198Q173.026 91.023 173.026 92.077Q173.026 93.667 172.229 94.828Q171.431 95.988 169.935 96.614Q168.439 97.239 166.334 97.239ZM180.237 89.61V97.0H176.508V79.867H180.162V87.373H179.895Q180.385 85.8 181.404 84.891Q182.423 83.983 184.086 83.983Q185.43 83.983 186.428 84.574Q187.426 85.165 187.976 86.251Q188.526 87.338 188.526 88.821V97.0H184.793V89.509Q184.793 88.37 184.207 87.721Q183.622 87.072 182.583 87.072Q181.895 87.072 181.364 87.374Q180.834 87.675 180.535 88.24Q180.237 88.805 180.237 89.61ZM196.087 97.239Q194.861 97.239 193.896 96.813Q192.932 96.387 192.383 95.539Q191.833 94.69 191.833 93.418Q191.833 92.344 192.218 91.617Q192.603 90.89 193.277 90.44Q193.951 89.991 194.818 89.755Q195.684 89.518 196.649 89.433Q197.754 89.329 198.429 89.226Q199.105 89.123 199.419 88.909Q199.732 88.694 199.732 88.29V88.236Q199.732 87.738 199.512 87.393Q199.291 87.048 198.872 86.865Q198.454 86.682 197.849 86.682Q197.243 86.682 196.781 86.865Q196.32 87.048 196.024 87.363Q195.728 87.679 195.603 88.09L192.201 87.591Q192.494 86.478 193.251 85.671Q194.008 84.864 195.181 84.424Q196.354 83.983 197.873 83.983Q198.988 83.983 199.997 84.247Q201.006 84.511 201.786 85.045Q202.565 85.58 203.013 86.393Q203.461 87.206 203.461 88.309V97.0H199.938V95.207H199.83Q199.5 95.835 198.976 96.29Q198.452 96.745 197.734 96.992Q197.015 97.239 196.087 97.239ZM197.184 94.701Q197.93 94.701 198.511 94.402Q199.091 94.103 199.426 93.586Q199.762 93.069 199.762 92.414V91.103Q199.598 91.2 199.315 91.287Q199.031 91.374 198.687 91.444Q198.342 91.513 198.003 91.565Q197.663 91.617 197.379 91.659Q196.766 91.744 196.318 91.946Q195.87 92.148 195.629 92.474Q195.387 92.801 195.387 93.275Q195.387 93.74 195.62 94.058Q195.853 94.375 196.257 94.538Q196.66 94.701 197.184 94.701ZM212.113 97.201Q210.665 97.201 209.483 96.448Q208.301 95.695 207.611 94.223Q206.921 92.751 206.921 90.592Q206.921 88.362 207.635 86.895Q208.35 85.429 209.528 84.706Q210.706 83.983 212.088 83.983Q213.147 83.983 213.871 84.341Q214.596 84.699 215.052 85.241Q215.509 85.783 215.738 86.33H215.84V79.867H219.569V97.0H215.894V94.928H215.738Q215.494 95.479 215.03 95.999Q214.565 96.519 213.85 96.86Q213.134 97.201 212.113 97.201ZM213.328 94.251Q214.15 94.251 214.731 93.793Q215.311 93.335 215.617 92.511Q215.922 91.688 215.922 90.584Q215.922 89.464 215.619 88.647Q215.315 87.83 214.735 87.382Q214.154 86.933 213.328 86.933Q212.491 86.933 211.915 87.394Q211.34 87.854 211.046 88.675Q210.752 89.496 210.752 90.584Q210.752 91.667 211.048 92.496Q211.344 93.326 211.921 93.788Q212.499 94.251 213.328 94.251ZM229.4 97.246Q227.428 97.246 225.996 96.414Q224.563 95.583 223.794 94.092Q223.024 92.601 223.024 90.623Q223.024 88.633 223.794 87.14Q224.563 85.647 225.996 84.815Q227.428 83.983 229.4 83.983Q231.376 83.983 232.803 84.815Q234.23 85.647 235.0 87.14Q235.769 88.633 235.769 90.623Q235.769 92.601 235.0 94.092Q234.23 95.583 232.803 96.414Q231.376 97.246 229.4 97.246ZM229.4 94.356Q230.244 94.356 230.818 93.876Q231.392 93.396 231.682 92.548Q231.972 91.701 231.972 90.608Q231.972 89.503 231.682 88.663Q231.392 87.823 230.818 87.348Q230.244 86.874 229.4 86.874Q228.553 86.874 227.979 87.348Q227.406 87.823 227.115 88.661Q226.825 89.499 226.825 90.608Q226.825 91.701 227.115 92.548Q227.406 93.396 227.979 93.876Q228.553 94.356 229.4 94.356ZM241.978 97L238.15 84.144H241.981L244.082 92.538L246.276 84.144H249.66L251.835 92.538L253.939 84.144H257.733L253.905 97H250.298L247.954 88.954L245.61 97Z"/></g></svg></div>
    <h1>Authentication successful</h1>
    <p>You can close this tab and return to the terminal.</p>
    <p class="hint">Your CLI session is now authenticated.</p>
  </div>
  <script>setTimeout(function(){window.close()},3000)</script>
</body>
</html>`

func runAuthLogout(cmd *cobra.Command, _ []string) error {
	if err := requireHumanLocalCommand("logout"); err != nil {
		return err
	}
	profile := resolveProfile(cmd)
	cfg, _ := cli.LoadCLIConfigForProfile(profile)
	if cfg.Token == "" {
		fmt.Fprintln(os.Stderr, "Not authenticated.")
		return nil
	}

	cfg.Token = ""
	if err := cli.SaveCLIConfigForProfile(cfg, profile); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Fprintln(os.Stderr, "Token removed. You are now logged out.")
	return nil
}
