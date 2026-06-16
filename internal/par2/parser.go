package par2

import "encoding/binary"

// Par2 packet layout (all integers little-endian):
//
//   [0:8]   Magic      "PAR2\x00PKT"
//   [8:16]  Length     uint64 — total packet size including this header
//   [16:32] MD5        packet-data MD5 (bytes 32..Length)
//   [32:48] SetID      recovery set identifier
//   [48:64] Type       packet type (16-byte ASCII, null-padded)
//
// FileDesc body (bytes 64..Length of a FileDesc packet):
//   [0:16]  FileID       MD5 of this file's 16 KB block checksums
//   [16:32] FileHash     MD5 of the whole file
//   [32:48] File16kHash  MD5 of first 16 KB
//   [48:56] FileLength   uint64 — exact decoded file size in bytes
//   [56:]   FileName     UTF-8, null-padded to 4-byte boundary

var (
	packetMagic  = [8]byte{'P', 'A', 'R', '2', 0, 'P', 'K', 'T'}
	fileDescType = [16]byte{'P', 'A', 'R', ' ', '2', '.', '0', 0, 'F', 'i', 'l', 'e', 'D', 'e', 's', 'c'}
)

// FileDesc holds the filename and size from a par2 FileDescription packet.
// Used identically to nzbdav: read the primary par2 index file, extract real
// filenames and authoritative sizes — no repair, no disk writes.
type FileDesc struct {
	FileID     [16]byte
	FileName   string
	FileLength uint64
}

// ParseFileDescs scans data for par2 FileDesc packets and returns all found.
// Unrecognised or truncated packets are silently skipped.
// Only pass the primary .par2 index file; .vol*.par2 recovery files contain
// no FileDesc packets (only RecoverySlice packets).
func ParseFileDescs(data []byte) []FileDesc {
	var out []FileDesc
	for i := 0; i+64 <= len(data); {
		if [8]byte(data[i:i+8]) != packetMagic {
			i++
			continue
		}
		pktLen := int(binary.LittleEndian.Uint64(data[i+8 : i+16]))
		if pktLen < 64 || i+pktLen > len(data) {
			i++
			continue
		}
		if [16]byte(data[i+48:i+64]) == fileDescType {
			if fd, ok := parseFileDescBody(data[i+64 : i+pktLen]); ok {
				out = append(out, fd)
			}
		}
		i += pktLen
	}
	return out
}

func parseFileDescBody(body []byte) (FileDesc, bool) {
	if len(body) < 56 {
		return FileDesc{}, false
	}
	var fd FileDesc
	copy(fd.FileID[:], body[:16])
	fd.FileLength = binary.LittleEndian.Uint64(body[48:56])
	if len(body) > 56 {
		raw := body[56:]
		n := 0
		for n < len(raw) && raw[n] != 0 {
			n++
		}
		fd.FileName = string(raw[:n])
	}
	return fd, fd.FileLength > 0
}
