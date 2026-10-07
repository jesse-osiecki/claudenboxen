package oci

import (
	"bufio"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// IdentityMounts synthesizes /etc/passwd and /etc/group entries for the
// host user and returns bind mounts layering them over the image's copies
// (extended, not replaced, so the image's system accounts stay intact).
// The cached image can't know the host uid, and NSS lookups (getpwuid,
// Node's os.userInfo()) would otherwise resolve to an unknown user.
// workDir receives the synthesized files.
func IdentityMounts(rootfsPath, workDir string) ([]specs.Mount, error) {
	u, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("look up current user: %w", err)
	}
	group, err := user.LookupGroupId(u.Gid)
	groupName := u.Username
	if err == nil {
		groupName = group.Name
	}

	passwdLine := fmt.Sprintf("%s:x:%s:%s::%s:/bin/bash\n", u.Username, u.Uid, u.Gid, u.HomeDir)
	groupLine := fmt.Sprintf("%s:x:%s:\n", groupName, u.Gid)

	passwdPath, err := mergeIntoFile(
		filepath.Join(rootfsPath, "etc", "passwd"),
		filepath.Join(workDir, "passwd"),
		passwdLine, ":", 2, u.Uid,
	)
	if err != nil {
		return nil, fmt.Errorf("synthesize /etc/passwd: %w", err)
	}
	groupPath, err := mergeIntoFile(
		filepath.Join(rootfsPath, "etc", "group"),
		filepath.Join(workDir, "group"),
		groupLine, ":", 2, u.Gid,
	)
	if err != nil {
		return nil, fmt.Errorf("synthesize /etc/group: %w", err)
	}

	return []specs.Mount{
		{Destination: "/etc/passwd", Type: "bind", Source: passwdPath, Options: []string{"rbind", "ro"}},
		{Destination: "/etc/group", Type: "bind", Source: groupPath, Options: []string{"rbind", "ro"}},
	}, nil
}

// mergeIntoFile copies srcPath to destPath, appending newLine unless a line
// already has idValue in its idField'th colon-separated field (1-indexed) —
// avoids a duplicate/conflicting entry on the rare host where the real
// uid/gid happens to collide with one of the image's own system accounts.
func mergeIntoFile(srcPath, destPath, newLine, sep string, idField int, idValue string) (string, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer src.Close()

	dest, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	defer dest.Close()

	haveID := false
	scanner := bufio.NewScanner(src)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Split(line, sep)
		if len(fields) >= idField && fields[idField-1] == idValue {
			haveID = true
		}
		fmt.Fprintln(dest, line)
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if !haveID {
		fmt.Fprint(dest, newLine)
	}
	return destPath, nil
}
