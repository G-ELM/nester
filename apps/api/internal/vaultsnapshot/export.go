package vaultsnapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

// Exporter writes one named snapshot envelope's bytes somewhere outside the
// primary database. name is a plain file-safe identifier (see Job's
// timestamped naming) — implementations choose how to turn it into a
// path/key/URL.
type Exporter interface {
	Export(ctx context.Context, name string, data []byte) error
}

// LocalExporter writes to a directory on disk. "Cold storage" here means
// whatever filesystem Dir actually resolves to: point it at a path backed
// by something genuinely independent of the primary database — a separate
// disk/volume, an NFS/EFS mount, or a cloud bucket mounted locally via
// s3fs/rclone/gcsfuse (a common, zero-SDK way to target real object storage
// from a plain filesystem writer). See this package's README for how to set
// that mount up; LocalExporter itself has no cloud-provider dependency.
type LocalExporter struct {
	Dir string
}

// Export writes data to Dir/name, refusing to overwrite an existing file
// (O_EXCL) and then marking it read-only — a best-effort immutability
// signal at the filesystem level. It's not a guarantee against a
// privileged actor, but it does mean this process's own code can never
// accidentally overwrite a prior snapshot, and the intent is explicit for
// anything backed by a real write-once/object-lock-capable mount.
func (e LocalExporter) Export(_ context.Context, name string, data []byte) error {
	if e.Dir == "" {
		return errors.New("vaultsnapshot: LocalExporter.Dir is empty")
	}
	if err := os.MkdirAll(e.Dir, 0o750); err != nil {
		return fmt.Errorf("vaultsnapshot: create dir: %w", err)
	}

	path := filepath.Join(e.Dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("vaultsnapshot: open %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("vaultsnapshot: write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("vaultsnapshot: close %s: %w", path, err)
	}

	if err := os.Chmod(path, 0o440); err != nil {
		return fmt.Errorf("vaultsnapshot: mark %s read-only: %w", path, err)
	}
	return nil
}

// URLFunc resolves name to a URL this process can PUT the snapshot bytes
// to — typically a presigned upload URL for an S3-compatible bucket,
// generated however the operator prefers (the cloud provider's own CLI/SDK
// run as a sidecar, a small internal signing service, …). Keeping that
// generation out of this package is deliberate: it avoids pulling a cloud
// SDK into this module only to hand-roll one call, see this package's
// README for why.
type URLFunc func(ctx context.Context, name string) (string, error)

// HTTPPutExporter uploads via a plain HTTP PUT to a URL from URLFn. Works
// against any presigned-URL object store (S3, GCS, R2, Backblaze B2, a
// self-hosted MinIO/garage gateway) without this package needing to know
// which one.
type HTTPPutExporter struct {
	URLFn      URLFunc
	HTTPClient *http.Client
}

func (e HTTPPutExporter) Export(ctx context.Context, name string, data []byte) error {
	if e.URLFn == nil {
		return errors.New("vaultsnapshot: HTTPPutExporter.URLFn is nil")
	}
	url, err := e.URLFn(ctx, name)
	if err != nil {
		return fmt.Errorf("vaultsnapshot: resolve upload url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := e.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("vaultsnapshot: upload %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("vaultsnapshot: upload %s returned status %d", name, resp.StatusCode)
	}
	return nil
}

// MultiExporter fans one export out to every backend, for redundancy across
// independent cold-storage targets (e.g. a local/mounted copy AND a
// presigned-URL bucket upload). Export succeeds once at least MinSuccess of
// them do; otherwise it returns a combined error naming every failure, so a
// partial outage is loud rather than silently losing redundancy.
type MultiExporter struct {
	Exporters  []Exporter
	MinSuccess int
}

func (e MultiExporter) Export(ctx context.Context, name string, data []byte) error {
	var errs []error
	succeeded := 0
	for _, exp := range e.Exporters {
		if err := exp.Export(ctx, name, data); err != nil {
			errs = append(errs, err)
			continue
		}
		succeeded++
	}

	min := e.MinSuccess
	if min <= 0 {
		min = len(e.Exporters)
	}
	if succeeded < min {
		return fmt.Errorf("vaultsnapshot: only %d/%d exports of %q succeeded (need %d): %w",
			succeeded, len(e.Exporters), name, min, errors.Join(errs...))
	}
	return nil
}
