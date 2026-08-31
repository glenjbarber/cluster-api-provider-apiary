// Package nocloud builds a cloud-init NoCloud datasource seed ISO -
// the mechanism this provider uses to get kubeadm's bootstrap data
// into a bhyve Linux VM at first boot, since Apiary itself has no
// cloud-init/bootstrap-data injection of its own (see the apiary
// repo's ADR-0031 for the base-image half of this story). The seed ISO
// is uploaded through Apiary's existing UploadISO RPC and attached as
// a VM's iso_name/CD-ROM device exactly like an installer image would
// be - cloud-init's own NoCloud datasource looks for a CD-ROM
// filesystem labeled "cidata" containing user-data/meta-data files,
// which is exactly what this package produces.
package nocloud

import (
	"fmt"
	"os"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/filesystem/iso9660"
)

// volumeLabel is fixed at "cidata" (case-insensitive) - the exact label
// cloud-init's NoCloud datasource requires to recognize a CD-ROM as a
// seed source. Not configurable; changing it would just break
// autodetection for no benefit.
const volumeLabel = "cidata"

// BuildSeedISO returns the raw bytes of a NoCloud seed ISO containing
// userData as /user-data and a minimal /meta-data naming instanceID and
// hostname (cloud-init requires both files to exist, even if meta-data
// carries little beyond an instance id - a stable id here is what lets
// cloud-init tell "first boot" apart from "reboot" correctly across
// restarts).
func BuildSeedISO(instanceID, hostname string, userData []byte) ([]byte, error) {
	// diskfs.Create insists on creating the file itself (O_EXCL) - only
	// reserve a unique path here, then remove it immediately so
	// diskfs.Create doesn't see it already existing.
	tmp, err := os.CreateTemp("", "apiary-seed-*.iso")
	if err != nil {
		return nil, fmt.Errorf("nocloud: reserving scratch path: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	if err := os.Remove(tmpPath); err != nil {
		return nil, fmt.Errorf("nocloud: clearing scratch path: %w", err)
	}
	defer os.Remove(tmpPath)

	// Sized generously for a NoCloud seed (user-data is normally a few
	// KB) - ISO9660 logical blocks are fixed at 2048 bytes regardless of
	// how little of this is actually used.
	const isoSize = 4 * 1024 * 1024

	d, err := diskfs.Create(tmpPath, isoSize, diskfs.Raw, diskfs.SectorSizeDefault)
	if err != nil {
		return nil, fmt.Errorf("nocloud: creating disk image: %w", err)
	}
	d.LogicalBlocksize = 2048

	fs, err := d.CreateFilesystem(disk.FilesystemSpec{Partition: 0, FSType: filesystem.TypeISO9660, VolumeLabel: volumeLabel})
	if err != nil {
		return nil, fmt.Errorf("nocloud: creating iso9660 filesystem: %w", err)
	}

	metaData := fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", instanceID, hostname)
	if err := writeFile(fs, "meta-data", []byte(metaData)); err != nil {
		return nil, err
	}
	if err := writeFile(fs, "user-data", userData); err != nil {
		return nil, err
	}

	isoFS, ok := fs.(*iso9660.FileSystem)
	if !ok {
		return nil, fmt.Errorf("nocloud: filesystem is not iso9660")
	}
	if err := isoFS.Finalize(iso9660.FinalizeOptions{VolumeIdentifier: volumeLabel}); err != nil {
		return nil, fmt.Errorf("nocloud: finalizing iso: %w", err)
	}

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("nocloud: reading finalized iso: %w", err)
	}
	return data, nil
}

func writeFile(fs filesystem.FileSystem, name string, data []byte) error {
	f, err := fs.OpenFile(name, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return fmt.Errorf("nocloud: creating %s: %w", name, err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("nocloud: writing %s: %w", name, err)
	}
	return nil
}
