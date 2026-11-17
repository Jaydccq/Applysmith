package guard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// commitAll commits the working tree of repo with a fixed synthetic identity.
func commitAll(t *testing.T, repo, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "--allow-empty", "-m", msg}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=Alex Example", "GIT_AUTHOR_EMAIL=alex@example.com",
			"GIT_COMMITTER_NAME=Alex Example", "GIT_COMMITTER_EMAIL=alex@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestScanHistory_FindsASecretADeletedFileOnceHeld(t *testing.T) {
	repo := gitRepo(t, map[string]string{"README.md": "hello\n"})
	commitAll(t, repo, "first")
	secret := join("gh", "p_", strings.Repeat("a1B2", 8))
	writeFile(t, repo, "config/old.env", "TOKEN="+secret+"\n")
	commitAll(t, repo, "add config")
	if err := os.Remove(filepath.Join(repo, "config", "old.env")); err != nil {
		t.Fatal(err)
	}
	commitAll(t, repo, "remove config")

	found, stats, err := ScanHistory(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Rule != ruleHistorySecret {
		t.Fatalf("findings = %v, want one history-secret", found)
	}
	if !strings.HasSuffix(found[0].Path, ":config/old.env") || found[0].Line != 1 {
		t.Errorf("finding location = %s:%d", found[0].Path, found[0].Line)
	}
	if strings.Contains(found[0].String(), secret) {
		t.Errorf("finding prints the secret: %s", found[0])
	}
	if stats.Commits != 3 || stats.Blobs < 2 {
		t.Errorf("stats = %+v", stats)
	}
}

func TestScanHistory_ReachesEveryRef(t *testing.T) {
	repo := gitRepo(t, map[string]string{"README.md": "hello\n"})
	commitAll(t, repo, "first")
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("checkout", "-q", "-b", "side")
	writeFile(t, repo, "side.txt", join("-----BEGIN ", "RSA PRIVATE", " KEY-----")+"\n")
	commitAll(t, repo, "side")
	run("checkout", "-q", "-")
	if found, _, err := ScanHistory(repo); err != nil || len(found) != 1 {
		t.Fatalf("found = %v, err = %v; want the key on the unmerged branch", found, err)
	}
}

func TestScanHistory_MarkedFakesAndBinariesPass(t *testing.T) {
	repo := gitRepo(t, map[string]string{
		"fixture.txt": join("gh", "p_", "FAKE", strings.Repeat("a1B2", 8)) + "\n",
		"blob.bin":    "\x00" + join("gh", "p_", strings.Repeat("a1B2", 8)),
	})
	commitAll(t, repo, "fixtures")
	found, stats, err := ScanHistory(repo)
	if err != nil || len(found) != 0 {
		t.Fatalf("found = %v, err = %v", found, err)
	}
	if stats.Skipped != 1 {
		t.Errorf("skipped = %d, want the one binary blob", stats.Skipped)
	}
}

func TestScanBlobs_AnUnexpectedHeaderReturnsWhileGitStillHasOutput(t *testing.T) {
	repo := gitRepo(t, map[string]string{"big.txt": strings.Repeat("x", 4<<20)})
	commitAll(t, repo, "first")
	cmd := exec.Command("git", "rev-parse", "HEAD:big.txt")
	cmd.Dir = repo
	sha, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := scanBlobs(repo, []string{strings.Repeat("0", 40), strings.TrimSpace(string(sha))}, nil, &HistoryStats{})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "unexpected git cat-file header") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("scanBlobs waited on a git still writing into an unread pipe")
	}
}
