package nocloud

import (
	"bytes"
	"testing"
)

func TestBuildSeedISO_ProducesGenuineISO9660WithExpectedLabel(t *testing.T) {
	data, err := BuildSeedISO("machine-1", "node-1", []byte("#cloud-config\nhostname: node-1\n"))
	if err != nil {
		t.Fatalf("BuildSeedISO() error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("BuildSeedISO() returned no data")
	}

	// The ISO9660 standard identifier "CD001" must appear at byte
	// offset 32769 (the Primary Volume Descriptor) - the same check
	// apiary's own internal/isostore.IsISO9660 uses to distinguish a
	// genuine ISO from a raw disk image, since that's exactly what
	// determines whether Apiary's reconciler attaches this as a CD-ROM
	// (ahci-cd) or a raw disk (ahci-hd) - only the former boots cloud-init
	// correctly.
	const cd001Offset = 32769
	if len(data) < cd001Offset+5 {
		t.Fatalf("iso too small (%d bytes) to contain a primary volume descriptor", len(data))
	}
	if got := data[cd001Offset : cd001Offset+5]; !bytes.Equal(got, []byte("CD001")) {
		t.Errorf("standard identifier at offset %d = %q, want CD001", cd001Offset, got)
	}

	// The volume label ("cidata") is what cloud-init's NoCloud datasource
	// scans for - confirm it landed somewhere in the primary volume
	// descriptor's volume-identifier field rather than trusting Finalize
	// silently ignored it.
	if !bytes.Contains(data[32808:32808+32], []byte("cidata")) {
		t.Error("volume identifier does not contain \"cidata\" - cloud-init's NoCloud datasource would not recognize this ISO")
	}
}

func TestBuildSeedISO_EmbedsUserDataVerbatim(t *testing.T) {
	userData := []byte("#cloud-config\nhostname: my-test-host\nssh_pwauth: false\n")
	data, err := BuildSeedISO("machine-2", "my-test-host", userData)
	if err != nil {
		t.Fatalf("BuildSeedISO() error: %v", err)
	}
	if !bytes.Contains(data, userData) {
		t.Error("finalized ISO does not contain the user-data bytes verbatim")
	}
}
