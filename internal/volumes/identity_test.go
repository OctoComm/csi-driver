package volumes

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func serialPage(serial string) []byte {
	return append([]byte{0x00, 0x80, 0x00, byte(len(serial))}, serial...)
}

func TestUnitSerialNumber(t *testing.T) {
	cases := []struct {
		name    string
		page    []byte
		want    string
		wantErr bool
	}{
		{"exact", serialPage("106963404"), "106963404", false},
		{"kernel padding", serialPage("106963404   \x00"), "106963404", false},
		{"wrong page", []byte{0x00, 0x83, 0x00, 0x01, '1'}, "", true},
		{"truncated", []byte{0x00, 0x80, 0x00, 0x09, '1'}, "", true},
		{"empty", serialPage("    "), "", true},
		{"short header", []byte{0x00, 0x80}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := unitSerialNumber(tc.page)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got %q, %v; want %q, error %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestResolveVolumeDeviceIgnoresForeignPaths(t *testing.T) {
	for _, path := range []string{"/dev/mapper/scsi-0HC_Volume_1", "/tmp/devpath", "/dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_1"} {
		got, err := resolveVolumeDevice(path)
		if err != nil || got != path {
			t.Fatalf("%s: got %q, %v", path, got, err)
		}
	}
}

func fakeNode(t *testing.T) (link func(volumeID, disk string) string, disk func(name, serial string)) {
	root := t.TempDir()
	saved := []string{byIDDir, devDir, sysBlockPath}
	byIDDir = filepath.Join(root, "dev", "disk", "by-id")
	devDir = filepath.Join(root, "dev")
	sysBlockPath = filepath.Join(root, "sys", "block")
	t.Cleanup(func() { byIDDir, devDir, sysBlockPath = saved[0], saved[1], saved[2] })
	if err := os.MkdirAll(byIDDir, 0o755); err != nil {
		t.Fatal(err)
	}
	disk = func(name, serial string) {
		if err := os.WriteFile(filepath.Join(devDir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(sysBlockPath, name, "device")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if serial != "" {
			if err := os.WriteFile(filepath.Join(dir, "vpd_pg80"), serialPage(serial), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	link = func(volumeID, target string) string {
		path := filepath.Join(byIDDir, "scsi-0HC_Volume_"+volumeID)
		if err := os.Symlink(filepath.Join("..", "..", target), path); err != nil {
			t.Fatal(err)
		}
		return path
	}
	return link, disk
}

func TestResolveVolumeDevice(t *testing.T) {
	link, disk := fakeNode(t)
	disk("sdb", "106963404")
	disk("sdc", "106963405")
	disk("sdd", "")
	disk("sdb1", "106963406")

	good := link("106963404", "sdb")
	got, err := resolveVolumeDevice(good)
	if err != nil || got != filepath.Join(devDir, "sdb") {
		t.Fatalf("matching link: got %q, %v", got, err)
	}

	// hetznercloud/csi-driver#1455: the link for 106963405 resolves to the disk of 106963404.
	aliased := link("106963405", "sdb")
	if _, err := resolveVolumeDevice(aliased); !errors.Is(err, ErrDeviceMismatch) {
		t.Fatalf("aliased link accepted: %v", err)
	}
	if _, err := resolveVolumeDevice(link("10696340", "sdb")); !errors.Is(err, ErrDeviceMismatch) {
		t.Fatalf("prefix ID accepted: %v", err)
	}
	if _, err := resolveVolumeDevice(link("106963407", "sdd")); !errors.Is(err, ErrDeviceMismatch) {
		t.Fatalf("unreadable serial accepted: %v", err)
	}
	if _, err := resolveVolumeDevice(link("106963406", "sdb1")); !errors.Is(err, ErrDeviceMismatch) {
		t.Fatalf("partition accepted: %v", err)
	}
	// A link removed after the caller's stat is retried like a mismatch.
	if _, err := resolveVolumeDevice(filepath.Join(byIDDir, "scsi-0HC_Volume_1")); !errors.Is(err, ErrDeviceMismatch) {
		t.Fatalf("vanished link not retryable: %v", err)
	}
}

// Captured on qubit-live-03 on 2026-10-04 from /sys/block/sdb/device/vpd_pg80 for volume 106963407,
// read both on the host and inside the node plugin container.
func TestUnitSerialNumberHetznerFixture(t *testing.T) {
	page := []byte{0x00, 0x80, 0x00, 0x09, '1', '0', '6', '9', '6', '3', '4', '0', '7'}
	got, err := unitSerialNumber(page)
	if err != nil || got != "106963407" {
		t.Fatalf("got %q, %v", got, err)
	}
}
