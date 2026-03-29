package h264

import (
	"encoding/binary"
	"fmt"
)

// NAL unit type constants from the H.264 spec.
const (
	NALSlice    = 1
	NALSliceDPA = 2
	NALSliceDPB = 3
	NALSliceDPC = 4
	NALSliceIDR = 5
	NALSEI      = 6
	NALSPS      = 7
	NALPPS      = 8
)

// NALUnit represents a parsed Network Abstraction Layer unit.
type NALUnit struct {
	RefIDC uint8  // nal_ref_idc (2 bits)
	Type   uint8  // nal_unit_type (5 bits)
	Data   []byte // RBSP data after emulation prevention byte removal
}

// ParseNALUnits extracts NAL units from length-prefixed data.
// lengthSize is the number of bytes used for each NAL length prefix (typically 4).
func ParseNALUnits(data []byte, lengthSize int) ([]NALUnit, error) {
	var units []NALUnit
	offset := 0
	for offset < len(data) {
		if offset+lengthSize > len(data) {
			return nil, fmt.Errorf("truncated NAL length at offset %d", offset)
		}
		var nalLen uint32
		switch lengthSize {
		case 1:
			nalLen = uint32(data[offset])
		case 2:
			nalLen = uint32(binary.BigEndian.Uint16(data[offset:]))
		case 4:
			nalLen = binary.BigEndian.Uint32(data[offset:])
		default:
			return nil, fmt.Errorf("unsupported NAL length size %d", lengthSize)
		}
		offset += lengthSize
		if offset+int(nalLen) > len(data) {
			return nil, fmt.Errorf("NAL unit extends beyond data: offset=%d len=%d total=%d", offset, nalLen, len(data))
		}
		if nalLen == 0 {
			continue
		}
		nalData := data[offset : offset+int(nalLen)]
		header := nalData[0]
		if header&0x80 != 0 {
			return nil, fmt.Errorf("forbidden_zero_bit is not zero")
		}
		rbsp := removeEmulationPreventionBytes(nalData[1:])
		units = append(units, NALUnit{
			RefIDC: (header >> 5) & 0x03,
			Type:   header & 0x1f,
			Data:   rbsp,
		})
		offset += int(nalLen)
	}
	return units, nil
}

// removeEmulationPreventionBytes removes 0x03 bytes from the sequence
// 0x00 0x00 0x03 that prevent start code emulation in NAL unit payloads.
func removeEmulationPreventionBytes(data []byte) []byte {
	if len(data) < 3 {
		out := make([]byte, len(data))
		copy(out, data)
		return out
	}
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if i+2 < len(data) && data[i] == 0x00 && data[i+1] == 0x00 && data[i+2] == 0x03 {
			out = append(out, 0x00, 0x00)
			i += 2 // skip the 0x03
		} else {
			out = append(out, data[i])
		}
	}
	return out
}
