package deploy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// KnownHosts holds the SSH host keys of the big Git hosts, so the first
// clone from them is already verified. Other hosts are trusted on first use
// and remembered per app.
const KnownHosts = `github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
gitlab.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAfuCHKVTjquxvt6CM6tdG4SLp1Btn/nOeHHE5UOzRdf
bitbucket.org ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIazEu89wgQZ4bqs3d63QSMzYVa0MuJ2e2gKTKqu+UUO
`

// NewDeployKey generates an Ed25519 key pair for cloning over SSH. The
// public key is in authorized_keys format, ready to paste into GitHub or
// GitLab; the private key is OpenSSH PEM.
func NewDeployKey(comment string) (private, public string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", "", err
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	public = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " " + comment
	return string(pem.EncodeToMemory(block)), public, nil
}

type cloneOpts struct {
	URL, Branch string
	Token       string // HTTPS access token
	DeployKey   string // SSH private key, for git@ URLs
	KnownHosts  string // remembered SSH host keys
}

// gitClone runs git with an argument list (never a shell). An HTTPS token
// is passed via GIT_CONFIG_* environment variables so it never appears in
// the command line or in the cloned repo's config. SSH clones use the
// app's deploy key and check host keys; the returned known_hosts includes
// keys seen for the first time.
func gitClone(ctx context.Context, o cloneOpts, dst, home string, w io.Writer) (string, error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	args := []string{"clone", "--depth", "1", "--single-branch", "--no-tags", "--recurse-submodules=no"}
	if o.Branch != "" {
		args = append(args, "--branch", o.Branch)
	}
	args = append(args, "--", o.URL, dst)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = []string{
		"HOME=" + home,
		"PATH=" + os.Getenv("PATH"),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	}
	// Respect outbound proxy / CA settings of the host environment.
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR", "GIT_SSL_CAINFO"} {
		if v, ok := os.LookupEnv(k); ok {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	khFile := ""
	if IsSSHURL(o.URL) {
		if _, _, err := ParseSSHURL(o.URL); err != nil {
			return "", err
		}
		if o.DeployKey == "" {
			return "", errors.New("SSH URLs need a deploy key: generate one in the app's settings, or use an https:// URL")
		}
		keyFile := filepath.Join(home, "deploy_key")
		khFile = filepath.Join(home, "known_hosts")
		if err := os.WriteFile(keyFile, []byte(o.DeployKey), 0o600); err != nil {
			return "", err
		}
		kh := o.KnownHosts
		if kh == "" {
			kh = KnownHosts
		}
		if err := os.WriteFile(khFile, []byte(kh), 0o600); err != nil {
			return "", err
		}
		cmd.Env = append(cmd.Env,
			"GIT_ALLOW_PROTOCOL=ssh",
			"GIT_SSH_COMMAND=ssh -F /dev/null -i "+shellQuote(keyFile)+" -o IdentitiesOnly=yes -o BatchMode=yes"+
				" -o UserKnownHostsFile="+shellQuote(khFile)+" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=20",
		)
	} else {
		cmd.Env = append(cmd.Env, "GIT_ALLOW_PROTOCOL=https")
		if o.Token != "" {
			u, _ := url.Parse(o.URL)
			user := "oauth2"
			if u.Host == "github.com" {
				user = "x-access-token"
			}
			cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + o.Token))
			cmd.Env = append(cmd.Env,
				"GIT_CONFIG_COUNT=1",
				"GIT_CONFIG_KEY_0=http.https://"+u.Host+"/.extraHeader",
				"GIT_CONFIG_VALUE_0=Authorization: Basic "+cred,
			)
		}
	}
	cmd.Stdout, cmd.Stderr = w, w
	err := cmd.Run()
	kh := ""
	if khFile != "" {
		if b, rerr := os.ReadFile(khFile); rerr == nil {
			kh = string(b)
		}
	}
	if err != nil {
		hint := "check the URL, branch and access token"
		if khFile != "" {
			hint = "check the URL and branch, and that the deploy key is added to the repository"
		}
		return kh, fmt.Errorf("git clone failed (%s): %w", hint, err)
	}
	return kh, nil
}

// DeployKeyURL is where to add a deploy key for a GitHub or GitLab
// repository, or "" for other hosts.
func DeployKeyURL(gitURL string) string {
	host, p := "", ""
	if IsSSHURL(gitURL) {
		h, rp, err := ParseSSHURL(gitURL)
		if err != nil {
			return ""
		}
		host, p = h, rp
	} else if u, err := url.Parse(gitURL); err == nil {
		host, p = u.Host, strings.TrimPrefix(u.Path, "/")
	}
	p = strings.TrimSuffix(strings.TrimSuffix(p, "/"), ".git")
	if p == "" {
		return ""
	}
	switch {
	case host == "github.com":
		return "https://github.com/" + p + "/settings/keys/new"
	case host == "gitlab.com" || strings.HasPrefix(host, "gitlab."):
		return "https://" + host + "/" + p + "/-/settings/repository#js-deploy-keys-settings"
	}
	return ""
}
