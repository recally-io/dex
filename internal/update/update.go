// Package update downloads verified releases and replaces a standalone dex executable.
package update

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"uuid"

	"github.com/vaayne/dex/internal/service"
)

// 256 MiB bounds download and extraction memory; raise when a release needs more.
const maxAsset = 256 << 20

var ErrBusy = errors.New("update is busy")

type Options struct {
	Check, Scheduled bool
	Channel, Tag     string
}
type Updater struct {
	current, executable, base string
	client                    *http.Client
	token                     string
}
type asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}
type release struct {
	Tag        string  `json:"tag_name"`
	Prerelease bool    `json:"prerelease"`
	Draft      bool    `json:"draft"`
	Assets     []asset `json:"assets"`
}

func New(current, executable string) (*Updater, error) {
	if current == "dev" || current == "" {
		return nil, errors.New("self-update is not available for dev builds; install a release binary")
	}
	if _, err := compare(current, current); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, err
	}
	if strings.Contains(resolved, "/Cellar/") || strings.HasPrefix(resolved, "/nix/store/") || strings.HasPrefix(resolved, "/usr/bin/") || strings.HasPrefix(resolved, "/bin/") {
		return nil, errors.New("package-managed executable; use its package manager to update dex")
	}

	return &Updater{current: current, executable: resolved, base: releaseBase(), client: releaseClient(5 * time.Minute)}, nil
}
func releaseClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if req.URL.Host != via[0].URL.Host || req.URL.Scheme != via[0].URL.Scheme {
			req.Header.Del("Authorization")
		}
		return nil
	}}
}

func auth(ctx context.Context, stored bool, searchPath string) (string, string, error) {
	if !stored {
		for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
			if value := os.Getenv(key); value != "" {
				return value, key, nil
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	gh, err := exec.LookPath("gh")
	if stored {
		gh = ""
		for _, dir := range filepath.SplitList(searchPath) {
			candidate := filepath.Join(dir, "gh")
			if info, e := os.Stat(candidate); e == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
				gh = candidate
				break
			}
		}
		if gh == "" {
			return "", "", errors.New("automatic updates need gh on the installed PATH and stored credentials; run gh auth login --hostname github.com")
		}
	} else if err != nil {
		return "", "", errors.New("updates need GH_TOKEN, GITHUB_TOKEN, or gh auth login --hostname github.com")
	}
	//nolint:gosec // G204: run the user's gh credential command without a shell.
	cmd := exec.CommandContext(ctx, gh, "auth", "token", "--hostname", "github.com")
	if stored {
		cmd.Env = []string{"PATH=" + searchPath, "HOME=" + os.Getenv("HOME"), "USER=" + os.Getenv("USER")}
		if config := os.Getenv("GH_CONFIG_DIR"); config != "" {
			return "", "", errors.New("GH_CONFIG_DIR is shell-only; store gh credentials in the default location before enabling automatic updates")
		}
	}
	b, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return "", "", errors.New("stored gh credentials are unavailable; run gh auth login --hostname github.com (shell-only tokens cannot enable automatic updates)")
	}
	return strings.TrimSpace(string(b)), "gh stored credentials", nil
}
func CheckStoredAccess(ctx context.Context, searchPath string) error {
	token, _, err := auth(ctx, true, searchPath)
	if err != nil {
		return err
	}
	u := &Updater{base: releaseBase(), client: releaseClient(10 * time.Second), token: token}
	_, err = u.get(ctx, u.base+"/repos/vaayne/dex/releases?per_page=1", "application/vnd.github+json", 1<<20)
	return err
}
func Access(ctx context.Context) (string, error) {
	token, source, err := auth(ctx, false, "")
	if err != nil {
		return "", err
	}
	u := &Updater{base: releaseBase(), client: releaseClient(10 * time.Second), token: token}
	_, err = u.get(ctx, u.base+"/repos/vaayne/dex/releases?per_page=1", "application/vnd.github+json", 1<<20)
	return source, err
}
func (u *Updater) get(ctx context.Context, target, accept string, limit int64) ([]byte, error) {
	//nolint:gosec // G704: release and asset URLs come from GitHub, or an explicit test constructor.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	base, err := url.Parse(u.base)
	if err != nil {
		return nil, err
	}
	if req.URL.Host == base.Host && req.URL.Scheme == base.Scheme && u.token != "" {
		req.Header.Set("Authorization", "Bearer "+u.token)
	}
	//nolint:gosec // G704: authenticated GitHub release download, with redirects stripped of credentials.
	resp, err := u.client.Do(req)
	if err != nil {
		if e, ok := errors.AsType[*url.Error](err); ok {
			return nil, fmt.Errorf("release request failed: %w", e.Err)
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, os.ErrNotExist
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release API returned %s (check access to vaayne/dex)", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download interrupted: %w", err)
	}
	if int64(len(b)) > limit {
		return nil, errors.New("release asset exceeds size limit")
	}
	return b, nil
}
func (u *Updater) selectRelease(ctx context.Context, opt Options) (release, error) {
	var r release
	endpoint := u.base + "/repos/vaayne/dex/releases"
	switch {
	case opt.Tag != "":
		endpoint += "/tags/" + url.PathEscape(opt.Tag)
	case opt.Channel == "stable":
		endpoint += "/latest"
	default:
		endpoint += "?per_page=100"
	}
	b, err := u.get(ctx, endpoint, "application/vnd.github+json", 8<<20)
	if errors.Is(err, os.ErrNotExist) && opt.Tag == "" && opt.Channel == "stable" {
		return r, errors.New("no stable release available")
	}
	if err != nil {
		return r, err
	}
	if opt.Tag != "" || opt.Channel == "stable" {
		if err := json.Unmarshal(b, &r); err != nil {
			return r, err
		}
	} else {
		var releases []release
		if err := json.Unmarshal(b, &releases); err != nil {
			return r, err
		}
		for _, candidate := range releases {
			if candidate.Draft {
				continue
			}
			if r.Tag == "" {
				r = candidate
				continue
			}
			newer, err := compare(candidate.Tag, r.Tag)
			if err != nil {
				return r, err
			}
			if newer > 0 {
				r = candidate
			}
		}
	}
	if opt.Tag != "" && r.Tag != opt.Tag {
		return r, errors.New("release API returned a different tag")
	}
	if r.Tag == "" || r.Draft || (opt.Channel == "stable" && opt.Tag == "" && r.Prerelease) {
		return r, errors.New("no release available for the selected channel")
	}
	return r, nil
}
func (u *Updater) Run(ctx context.Context, opt Options, m service.Manager, out io.Writer) error {
	lock, err := lockUpdate(u.executable + ".update.lock")
	if err != nil {
		if opt.Scheduled && errors.Is(err, ErrBusy) {
			fmt.Fprintln(out, "Update skipped: another updater is running.")
			return nil
		}
		return err
	}
	defer lock()
	if u.token == "" {
		u.token, _, err = auth(ctx, false, "")
		if err != nil {
			return err
		}
	}
	r, err := u.selectRelease(ctx, opt)
	if err != nil {
		return err
	}
	order, err := compare(r.Tag, u.current)
	if err != nil {
		return err
	}
	if order <= 0 {
		fmt.Fprintf(out, "up to date: %s (selected %s; downgrades are refused)\n", u.current, r.Tag)
		return nil
	}
	if opt.Check {
		fmt.Fprintf(out, "Update available: %s -> %s\n", u.current, r.Tag)
		return nil
	}
	// Stage and validate everything before acquiring maintenance or stopping a service.
	stage, err := u.stage(ctx, r)
	if err != nil {
		return err
	}
	defer os.Remove(stage)
	state, err := m.Status(ctx)
	if err != nil {
		return err
	}
	same := false
	if state.Installed {
		resolved, e := filepath.EvalSymlinks(state.Config.Executable)
		if e != nil {
			return e
		}
		same = resolved == u.executable
	}
	if state.Running && same {
		if !state.Healthy || state.Version != u.current {
			return errors.New("installed instance does not match this executable or its worker is not ready")
		}
		lease := uuid.NewV7().String()
		// Rename and restart finish before the two-minute server lease can expire.
		operation, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		if err := maintenance(operation, state.Config, lease, http.MethodPost); err != nil {
			if opt.Scheduled && errors.Is(err, ErrBusy) {
				fmt.Fprintln(out, "Update skipped: dex is busy.")
				return nil
			}
			return err
		}
		restartRequested := false
		defer func() {
			if restartRequested {
				// A failed manager command can still stop the old process. Let its lease expire.
				return
			}
			release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			_ = maintenance(release, state.Config, lease, http.MethodDelete)
		}()
		if err := u.replace(stage); err != nil {
			return err
		}
		if err := maintenance(operation, state.Config, lease, http.MethodPost); err != nil {
			return fmt.Errorf("binary updated, maintenance renewal failed; restart not requested; previous binary: %s.previous: %w", u.executable, err)
		}
		restartRequested = true
		if err := m.Restart(operation); err != nil {
			return fmt.Errorf("binary updated, restart failed; previous binary: %s.previous: %w", u.executable, err)
		}
		if err := service.Ready(operation, state.Config, r.Tag); err != nil {
			return fmt.Errorf("updated service is not ready; logs: %s; previous binary: %s.previous: %w", m.LogPath(), u.executable, err)
		}
	} else {
		if err := u.replace(stage); err != nil {
			return err
		}
		if !state.Installed || !same {
			fmt.Fprintln(out, "Restart any foreground dex process to use the new version.")
		}
	}
	fmt.Fprintf(out, "Updated dex: %s -> %s\nPrevious binary: %s.previous\n", u.current, r.Tag, u.executable)
	return nil
}
func (u *Updater) stage(ctx context.Context, r release) (string, error) {
	name := fmt.Sprintf("dex_%s_%s_%s.tar.gz", r.Tag, runtime.GOOS, runtime.GOARCH)
	find := func(name string) (string, error) {
		found := ""
		for _, a := range r.Assets {
			if a.Name == name {
				if found != "" {
					return "", fmt.Errorf("duplicate release asset %s", name)
				}
				found = a.URL
			}
		}
		if found == "" {
			return "", fmt.Errorf("missing release asset %s", name)
		}
		return found, nil
	}
	assetURL, err := find(name)
	if err != nil {
		return "", err
	}
	checksumURL, err := find("checksums.txt")
	if err != nil {
		return "", err
	}
	checksums, err := u.get(ctx, checksumURL, "application/octet-stream", 8<<20)
	if err != nil {
		return "", err
	}
	expected := ""
	for line := range strings.SplitSeq(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			if expected != "" {
				return "", errors.New("duplicate checksum line")
			}
			expected = fields[0]
		}
	}
	digest, err := hex.DecodeString(expected)
	if err != nil || len(digest) != sha256.Size {
		return "", fmt.Errorf("missing or invalid checksum for %s", name)
	}
	archive, err := u.get(ctx, assetURL, "application/octet-stream", maxAsset)
	if err != nil {
		return "", err
	}
	actual := sha256.Sum256(archive)
	if !strings.EqualFold(expected, hex.EncodeToString(actual[:])) {
		return "", errors.New("release checksum mismatch")
	}
	file, err := os.CreateTemp(filepath.Dir(u.executable), ".dex-update-*")
	if err != nil {
		return "", fmt.Errorf("executable directory is not writable: %w", err)
	}
	stage := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(stage)
		}
	}()
	if err := extract(archive, file); err != nil {
		return "", err
	}
	if err := file.Chmod(0755); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	verify, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	//nolint:gosec // G204: run the checksum-verified staged release, without a shell.
	b, err := exec.CommandContext(verify, stage, "version").Output()
	if err != nil {
		return "", fmt.Errorf("staged executable did not run: %w", err)
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 || fields[0] != "dex" || fields[1] != r.Tag {
		return "", errors.New("staged executable reported an unexpected version")
	}
	keep = true
	return stage, nil
}
func extract(archive []byte, out io.Writer) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("release gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, maxAsset+1))
	seen := map[string]bool{}
	found := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("release tar: %w", err)
		}
		if path.IsAbs(h.Name) || path.Clean(h.Name) != h.Name || h.Name == ".." || strings.HasPrefix(h.Name, "../") || strings.Contains(h.Name, "\\") || seen[h.Name] {
			return fmt.Errorf("unsafe or duplicate archive entry %q", h.Name)
		}
		seen[h.Name] = true
		if h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > maxAsset {
			return fmt.Errorf("archive entry is not a regular file within the size limit: %q", h.Name)
		}
		if h.Name == "dex" {
			found = true
			if _, err := io.CopyN(out, tr, h.Size); err != nil {
				return err
			}
		}
	}
	if !found {
		return errors.New("archive has no regular file named dex")
	}
	return nil
}
func (u *Updater) replace(stage string) error {
	backup := stage + ".previous"
	if err := os.Link(u.executable, backup); err != nil {
		return fmt.Errorf("retain previous executable: %w", err)
	}
	defer os.Remove(backup)
	if err := os.Rename(backup, u.executable+".previous"); err != nil {
		return err
	}
	return os.Rename(stage, u.executable)
}
func maintenance(ctx context.Context, c service.Config, id, method string) error {
	b, err := json.Marshal(map[string]string{"leaseId": id})
	if err != nil {
		return err
	}
	//nolint:gosec // G704: validated installed loopback address.
	req, err := http.NewRequestWithContext(ctx, method, c.URL()+"/api/maintenance", strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	//nolint:gosec // G704: loopback maintenance request, without redirects.
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return errors.Join(ErrBusy, errors.New("dex is running work or another updater holds maintenance"))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("maintenance returned %s", resp.Status)
	}
	return nil
}

// compare implements SemVer ordering without a dependency. Build metadata does not affect order.
func compare(a, b string) (int, error) {
	parse := func(raw string) ([3]string, []string, error) {
		var nums [3]string
		raw = strings.TrimPrefix(raw, "v")
		core, metadata, hasMetadata := strings.Cut(raw, "+")
		if hasMetadata && !identifiers(metadata) {
			return nums, nil, errors.New("invalid version metadata")
		}
		core, pre, hasPre := strings.Cut(core, "-")
		parts := strings.Split(core, ".")
		if len(parts) != 3 {
			return nums, nil, errors.New("version must be a release SemVer tag")
		}
		for i, part := range parts {
			if !numeric(part) || len(part) > 1 && part[0] == '0' {
				return nums, nil, errors.New("invalid release version")
			}
			nums[i] = part
		}
		var prerelease []string
		if hasPre {
			if !identifiers(pre) {
				return nums, nil, errors.New("invalid prerelease version")
			}
			prerelease = strings.Split(pre, ".")
			for _, part := range prerelease {
				if numeric(part) && len(part) > 1 && part[0] == '0' {
					return nums, nil, errors.New("prerelease numbers must not have leading zeroes")
				}
			}
		}
		return nums, prerelease, nil
	}
	an, ap, err := parse(a)
	if err != nil {
		return 0, err
	}
	bn, bp, err := parse(b)
	if err != nil {
		return 0, err
	}
	for i := range 3 {
		if order := compareNumber(an[i], bn[i]); order != 0 {
			return order, nil
		}
	}
	if len(ap) == 0 && len(bp) > 0 {
		return 1, nil
	}
	if len(bp) == 0 && len(ap) > 0 {
		return -1, nil
	}
	for i := range min(len(ap), len(bp)) {
		if ap[i] == bp[i] {
			continue
		}
		switch {
		case numeric(ap[i]) && numeric(bp[i]):
			return compareNumber(ap[i], bp[i]), nil
		case numeric(ap[i]):
			return -1, nil
		case numeric(bp[i]):
			return 1, nil
		default:
			return strings.Compare(ap[i], bp[i]), nil
		}
	}
	return cmp.Compare(len(ap), len(bp)), nil
}
func numeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func compareNumber(a, b string) int {
	if len(a) != len(b) {
		return cmp.Compare(len(a), len(b))
	}
	return strings.Compare(a, b)
}
func identifiers(value string) bool {
	for part := range strings.SplitSeq(value, ".") {
		if part == "" {
			return false
		}
		for _, r := range part {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}
