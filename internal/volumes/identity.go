package volumes

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// ErrDeviceMismatch reports that a Hetzner Volume by-id path does not resolve to the disk carrying
// that volume's serial. See hetznercloud/csi-driver#1455.
var ErrDeviceMismatch = errors.New("device does not carry the requested volume serial")

var (
	hetznerVolumeLink = regexp.MustCompile(`^scsi-0HC_Volume_([0-9]+)$`)
	wholeSCSIDisk     = regexp.MustCompile(`^sd[a-z]+$`)
	// Replaced in tests.
	byIDDir      = "/dev/disk/by-id"
	devDir       = "/dev"
	sysBlockPath = "/sys/block"
)

// resolveVolumeDevice returns the block device node that a publish must use.
//
// For a Hetzner Volume by-id path it resolves the udev symlink once and requires the resolved disk's
// unit serial number (VPD page 0x80, which Hetzner sets to the volume ID) to equal the ID in the path.
// Callers format and mount the returned node, not the symlink, so later udev changes to the by-id
// link cannot redirect the operation. This does not pin the device's lifetime: hot-unplug and reuse
// of the same sdX name between verification and mount is out of scope. Unreadable or malformed identity fails closed. Other paths are returned
// unchanged.
func resolveVolumeDevice(devicePath string) (string, error) {
	match := hetznerVolumeLink.FindStringSubmatch(filepath.Base(devicePath))
	if match == nil || filepath.Dir(devicePath) != byIDDir {
		return devicePath, nil
	}
	volumeID := match[1]

	resolved, err := filepath.EvalSymlinks(devicePath)
	if err != nil {
		// udev may remove or replace the link between the caller's stat and this resolution.
		return "", fmt.Errorf("%w: resolving %s: %v", ErrDeviceMismatch, devicePath, err)
	}
	disk := filepath.Base(resolved)
	if filepath.Dir(resolved) != devDir || !wholeSCSIDisk.MatchString(disk) {
		return "", fmt.Errorf("%w: %s resolves to %s, not a whole SCSI disk", ErrDeviceMismatch, devicePath, resolved)
	}

	page, err := os.ReadFile(filepath.Join(sysBlockPath, disk, "device", "vpd_pg80"))
	if err != nil {
		return "", fmt.Errorf("%w: reading serial of %s: %v", ErrDeviceMismatch, disk, err)
	}
	serial, err := unitSerialNumber(page)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrDeviceMismatch, disk, err)
	}
	if serial != volumeID {
		return "", fmt.Errorf("%w: %s resolves to %s with serial %q, want %q", ErrDeviceMismatch, devicePath, disk, serial, volumeID)
	}
	return resolved, nil
}

// unitSerialNumber decodes an SCSI Unit Serial Number VPD page: byte 1 is the page code (0x80),
// bytes 2-3 the big-endian length, followed by the serial padded with spaces or NULs.
func unitSerialNumber(page []byte) (string, error) {
	if len(page) < 4 || page[1] != 0x80 {
		return "", errors.New("not a unit serial number page")
	}
	length := int(binary.BigEndian.Uint16(page[2:4]))
	if length == 0 || 4+length > len(page) {
		return "", fmt.Errorf("invalid serial length %d", length)
	}
	serial := bytes.Trim(page[4:4+length], " \x00")
	if len(serial) == 0 {
		return "", errors.New("empty serial")
	}
	return string(serial), nil
}
