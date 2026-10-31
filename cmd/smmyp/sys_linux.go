//go:build linux

package main

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// dropPrivileges: Läuft SMMYP als root (z. B. Unraid mit --user 0:0), gehört das Datenverzeichnis danach
// PUID:PGID (Standard 65532) und der Prozess gibt root sofort ab. Ohne root passiert nichts.
func dropPrivileges(dataDir string, log *slog.Logger) error {
	if os.Getuid() != 0 {
		return nil
	}
	uid, err1 := envID("PUID")
	gid, err2 := envID("PGID")
	if err1 != nil || err2 != nil {
		return fmt.Errorf("PUID/PGID müssen Zahlen sein")
	}
	if uid == 0 || gid == 0 {
		return fmt.Errorf("SMMYP läuft nicht als root: PUID und PGID dürfen nicht 0 sein")
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return err
	}
	changed := 0
	err := filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && (int(st.Uid) != uid || int(st.Gid) != gid) {
			changed++
			return os.Lchown(path, uid, gid)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("Rechte für %s setzen: %w", dataDir, err)
	}
	if err := syscall.Setgroups([]int{gid}); err != nil {
		return err
	}
	if err := syscall.Setgid(gid); err != nil {
		return err
	}
	if err := syscall.Setuid(uid); err != nil {
		return err
	}
	log.Info("root abgegeben", "uid", uid, "gid", gid, "chown", changed)
	return nil
}

func envID(key string) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return 65532, nil
	}
	return strconv.Atoi(v)
}

// networkFS erkennt NFS/SMB/CIFS-Dateisysteme (Concept R1).
func networkFS(dir string) string {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return ""
	}
	switch uint32(st.Type) {
	case 0x6969:
		return "nfs"
	case 0x517B:
		return "smb"
	case 0xFF534D42:
		return "cifs"
	case 0xFE534D42:
		return "smb2"
	case 0x65735546:
		return "fuse (z. B. Unraid /mnt/user – besser /mnt/cache oder einen Pool direkt nutzen)"
	}
	return ""
}
