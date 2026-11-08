//go:build windows

package node

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// linkDir makes name in the space a link to the folder target, outside the space: a junction,
// which, unlike a symlink, needs no privilege.
func linkDir(space *os.Root, spaceDir, name, target string) error {
	mkdirError := space.Mkdir(name, 0o755)
	if mkdirError != nil {
		return mkdirError
	}
	junctionError := makeJunction(filepath.Join(spaceDir, filepath.FromSlash(name)), target)
	if junctionError != nil {
		_ = space.Remove(name)

		return junctionError
	}

	return nil
}

// makeJunction turns the empty folder at link into a junction to target: a mount point reparse
// point, holding target as `\??\target` and as itself.
func makeJunction(link, target string) error {
	substitute := utf16.Encode([]rune(`\??\` + target))
	printed := utf16.Encode([]rune(target))
	// REPARSE_DATA_BUFFER, mount point: tag, length, reserved, four offsets and lengths, then
	// the two names, each ending in a NUL
	names := make([]uint16, 0, len(substitute)+len(printed)+2)
	names = append(append(append(append(names, substitute...), 0), printed...), 0)
	data := make([]byte, 8+8+2*len(names))
	binary.LittleEndian.PutUint32(data[0:], windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(data[4:], uint16(8+2*len(names)))
	binary.LittleEndian.PutUint16(data[8:], 0)
	binary.LittleEndian.PutUint16(data[10:], uint16(2*len(substitute)))
	binary.LittleEndian.PutUint16(data[12:], uint16(2*(len(substitute)+1)))
	binary.LittleEndian.PutUint16(data[14:], uint16(2*len(printed)))
	for i, unit := range names {
		binary.LittleEndian.PutUint16(data[16+2*i:], unit)
	}
	path, pathError := windows.UTF16PtrFromString(link)
	if pathError != nil {
		return pathError
	}
	handle, createError := windows.CreateFile(path, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if createError != nil {
		return createError
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	var returned uint32

	return windows.DeviceIoControl(handle, windows.FSCTL_SET_REPARSE_POINT, (*byte)(unsafe.Pointer(&data[0])), uint32(len(data)), nil, 0, &returned, nil)
}
