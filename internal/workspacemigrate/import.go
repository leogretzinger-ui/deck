package workspacemigrate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

const importRecord = ".deck-import.json"
const importStage = ".deck-import-stage"
const importNext = ".deck-import-next"

type importReceipt struct {
	Version int    `json:"version"`
	Digest  string `json:"digest"`
	Phase   string `json:"phase"`
}

// Import publishes a snapshot into a fresh encrypted root. The caller holds
// exclusive installation ownership and a descriptor to the qualified mount.
// The native journal alone ends import. A completed on-volume receipt still
// requires verification when repairing a missing native completion record.
func Import(ctx context.Context, snapshot, target *os.Root, digest string) error {
	return importSnapshot(ctx, snapshot, target, digest, nil)
}

func importSnapshot(ctx context.Context, snapshot, target *os.Root, digest string, checkpoint func(string) error) error {
	if !validDigest(digest) {
		return ErrSnapshot
	}
	receipt, err := readReceipt(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrSnapshot
	}
	if err == nil {
		if receipt.Version != 1 || receipt.Digest != digest {
			return ErrSnapshot
		}
		if receipt.Phase != "copying" && receipt.Phase != "publishing" && receipt.Phase != "complete" {
			return ErrSnapshot
		}
	} else {
		names, err := names(target)
		if err != nil || len(names) != 0 {
			return ErrSnapshot
		}
		receipt = importReceipt{Version: 1, Digest: digest, Phase: "copying"}
		if writeReceipt(target, receipt) != nil {
			return ErrSnapshot
		}
	}
	if snapshot == nil {
		return ErrSnapshot
	}
	manifest, err := ReadManifest(snapshot, digest)
	if err != nil {
		return err
	}
	if receipt.Phase == "complete" {
		return verifyPublication(ctx, target, manifest)
	}
	source, err := snapshot.OpenRoot("tree")
	if err != nil {
		return ErrSnapshot
	}
	defer func() { _ = source.Close() }()
	if Verify(ctx, source, manifest) != nil {
		return ErrSnapshot
	}
	current, err := names(target)
	if err != nil {
		return ErrSnapshot
	}
	for _, name := range current {
		if name == importRecord || name == importNext || name == importStage {
			continue
		}
		if receipt.Phase != "publishing" || !isStore(name) {
			return ErrSnapshot
		}
	}
	if receipt.Phase == "copying" {
		if err := discardStage(target); err != nil {
			return err
		}
		if target.Mkdir(importStage, 0700) != nil {
			return ErrSnapshot
		}
		stage, err := target.OpenRoot(importStage)
		if err != nil {
			return ErrSnapshot
		}
		err = Copy(ctx, source, stage, manifest)
		_ = stage.Close()
		if err != nil {
			return err
		}
		if err := pause(checkpoint, "copied"); err != nil {
			return err
		}
		receipt.Phase = "publishing"
		if writeReceipt(target, receipt) != nil {
			return ErrSnapshot
		}
	}
	st, err := target.Lstat(importStage)
	if err != nil || !st.IsDir() {
		return ErrSnapshot
	}
	stage, err := target.OpenRoot(importStage)
	if err != nil {
		return ErrSnapshot
	}
	defer func() { _ = stage.Close() }()
	stageNames, err := names(stage)
	if err != nil {
		return ErrSnapshot
	}
	for _, name := range stageNames {
		if !isStore(name) {
			return ErrSnapshot
		}
	}
	for _, store := range stores {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, staged := stage.Lstat(store)
		_, published := target.Lstat(store)
		switch {
		case staged == nil && errors.Is(published, os.ErrNotExist):
			if verifyStore(ctx, stage, manifest, store) != nil {
				return ErrSnapshot
			}
			if target.Rename(importStage+"/"+store, store) != nil || syncDir(target, ".") != nil || syncDir(stage, ".") != nil {
				return ErrSnapshot
			}
		case errors.Is(staged, os.ErrNotExist) && published == nil:
			if verifyStore(ctx, target, manifest, store) != nil {
				return ErrSnapshot
			}
		default:
			return ErrSnapshot
		}
		if err := pause(checkpoint, "published-"+store); err != nil {
			return err
		}
	}
	// Recheck every published store before the receipt admits any workload.
	for _, store := range stores {
		if verifyStore(ctx, target, manifest, store) != nil {
			return ErrSnapshot
		}
	}
	receipt.Phase = "complete"
	if writeReceipt(target, receipt) != nil {
		return ErrSnapshot
	}
	return nil
}

func pause(checkpoint func(string) error, phase string) error {
	if checkpoint == nil {
		return nil
	}
	return checkpoint(phase)
}

func verifyStore(ctx context.Context, root *os.Root, m Manifest, store string) error {
	st, err := root.Lstat(store)
	if err != nil || !st.IsDir() {
		return ErrSnapshot
	}
	sub, err := root.OpenRoot(store)
	if err != nil {
		return ErrSnapshot
	}
	defer func() { _ = sub.Close() }()
	entries, err := scan(ctx, sub)
	if err != nil {
		return err
	}
	actual := []Entry{{Path: store, Kind: "directory", Mode: st.Mode().Perm(), Mtime: st.ModTime().UnixNano()}}
	for _, e := range entries {
		e.Path = store + "/" + e.Path
		actual = append(actual, e)
	}
	var wanted []Entry
	for _, e := range m.Entries {
		if e.Path == store || strings.HasPrefix(e.Path, store+"/") {
			wanted = append(wanted, e)
		}
	}
	return compare(actual, wanted)
}

func names(root *os.Root) ([]string, error) {
	f, err := root.Open(".")
	if err != nil {
		return nil, ErrSnapshot
	}
	defer func() { _ = f.Close() }()
	return f.Readdirnames(-1)
}

func syncDir(root *os.Root, name string) error {
	f, err := root.Open(name)
	if err != nil {
		return ErrSnapshot
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

func readReceipt(root *os.Root) (importReceipt, error) {
	var r importReceipt
	st, err := root.Lstat(importRecord)
	if err != nil {
		return r, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() > 1024 {
		return r, ErrSnapshot
	}
	f, err := root.Open(importRecord)
	if err != nil {
		return r, ErrSnapshot
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(io.LimitReader(f, 1025))
	dec.DisallowUnknownFields()
	if dec.Decode(&r) != nil || dec.Decode(new(any)) != io.EOF {
		return r, ErrSnapshot
	}
	return r, nil
}

func writeReceipt(root *os.Root, r importReceipt) error {
	if st, err := root.Lstat(importNext); err == nil {
		if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || root.Remove(importNext) != nil {
			return ErrSnapshot
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrSnapshot
	}
	file, err := root.OpenFile(importNext, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrSnapshot
	}
	err = json.NewEncoder(file).Encode(r)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err != nil || syncErr != nil || closeErr != nil || root.Rename(importNext, importRecord) != nil {
		return ErrSnapshot
	}
	return syncDir(root, ".")
}

func discardStage(root *os.Root) error {
	st, err := root.Lstat(importStage)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !st.IsDir() {
		return ErrSnapshot
	}
	stage, err := root.OpenRoot(importStage)
	if err != nil {
		return ErrSnapshot
	}
	err = writableDirectories(stage, ".")
	_ = stage.Close()
	if err != nil || root.RemoveAll(importStage) != nil {
		return ErrSnapshot
	}
	return syncDir(root, ".")
}

func writableDirectories(root *os.Root, name string) error {
	if root.Chmod(name, 0700) != nil {
		return ErrSnapshot
	}
	dir, err := root.OpenRoot(name)
	if err != nil {
		return ErrSnapshot
	}
	defer func() { _ = dir.Close() }()
	list, err := names(dir)
	if err != nil {
		return err
	}
	for _, child := range list {
		st, err := dir.Lstat(child)
		if err != nil {
			return ErrSnapshot
		}
		if st.IsDir() {
			if err := writableDirectories(dir, child); err != nil {
				return err
			}
		}
	}
	return nil
}

// ReadManifest authenticates the exact private manifest bytes against the
// installation's admin-pinned digest. No browser provides either value.
func ReadManifest(snapshot *os.Root, digest string) (Manifest, error) {
	var m Manifest
	if !validDigest(digest) {
		return m, ErrSnapshot
	}
	st, err := snapshot.Lstat("manifest.json")
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() > 256*1024*1024 {
		return m, ErrSnapshot
	}
	f, err := snapshot.Open("manifest.json")
	if err != nil {
		return m, ErrSnapshot
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, 256*1024*1024+1))
	if err != nil || len(raw) > 256*1024*1024 || hashBytes(raw) != digest {
		return m, ErrSnapshot
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if dec.Decode(&m) != nil || dec.Decode(new(any)) != io.EOF || !validManifest(m) {
		return m, ErrSnapshot
	}
	return m, nil
}

// Publication alone cannot admit the app after an interrupted journal commit.
// There have been no legitimate workload writes until native completion, so
// every published byte must still match the admin-pinned manifest.
func verifyPublication(ctx context.Context, target *os.Root, m Manifest) error {
	current, err := names(target)
	if err != nil {
		return ErrSnapshot
	}
	for _, name := range current {
		if isStore(name) || name == importRecord {
			continue
		}
		if name != importStage {
			return ErrSnapshot
		}
		info, err := target.Lstat(name)
		if err != nil || !info.IsDir() {
			return ErrSnapshot
		}
		stage, err := target.OpenRoot(name)
		if err != nil {
			return ErrSnapshot
		}
		list, err := names(stage)
		_ = stage.Close()
		if err != nil || len(list) != 0 {
			return ErrSnapshot
		}
	}
	for _, store := range stores {
		if verifyStore(ctx, target, m, store) != nil {
			return ErrSnapshot
		}
	}
	return nil
}
