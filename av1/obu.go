package av1

import "fmt"

// OBU type constants (AV1 spec 6.2.2).
const (
	OBUSequenceHeader       = 1
	OBUTemporalDelimiter    = 2
	OBUFrameHeader          = 3
	OBUTileGroup            = 4
	OBUMetadata             = 5
	OBUFrame                = 6
	OBURedundantFrameHeader = 7
	OBUTileList             = 8
	OBUPadding              = 15
)

// OBUHeader contains the parsed header of an Open Bitstream Unit.
type OBUHeader struct {
	Type         int
	HasExtension bool
	HasSizeField bool
	TemporalID   int
	SpatialID    int
}

// OBU represents a parsed Open Bitstream Unit.
type OBU struct {
	Header OBUHeader
	Data   []byte // payload data after header
}

// parseOBUHeader parses an OBU header from a byte slice.
// Returns the header and the number of bytes consumed.
func parseOBUHeader(data []byte) (OBUHeader, int, error) {
	if len(data) < 1 {
		return OBUHeader{}, 0, fmt.Errorf("OBU header: not enough data")
	}

	b := data[0]
	forbidden := (b >> 7) & 1
	if forbidden != 0 {
		return OBUHeader{}, 0, fmt.Errorf("OBU header: forbidden bit set")
	}

	hdr := OBUHeader{
		Type:         int((b >> 3) & 0xF),
		HasExtension: (b>>2)&1 == 1,
		HasSizeField: (b>>1)&1 == 1,
	}

	consumed := 1

	if hdr.HasExtension {
		if len(data) < 2 {
			return OBUHeader{}, 0, fmt.Errorf("OBU extension header: not enough data")
		}
		ext := data[1]
		hdr.TemporalID = int((ext >> 5) & 0x7)
		hdr.SpatialID = int((ext >> 3) & 0x3)
		consumed = 2
	}

	return hdr, consumed, nil
}

// ParseOBUs parses a byte slice into a sequence of OBUs.
func ParseOBUs(data []byte) ([]OBU, error) {
	var obus []OBU
	pos := 0

	for pos < len(data) {
		hdr, hdrSize, err := parseOBUHeader(data[pos:])
		if err != nil {
			return nil, fmt.Errorf("OBU at offset %d: %w", pos, err)
		}
		pos += hdrSize

		var payloadSize int
		if hdr.HasSizeField {
			size, bytesRead, err := ParseLEB128(data[pos:])
			if err != nil {
				return nil, fmt.Errorf("OBU size at offset %d: %w", pos, err)
			}
			pos += bytesRead
			payloadSize = int(size)
		} else {
			payloadSize = len(data) - pos
		}

		if pos+payloadSize > len(data) {
			return nil, fmt.Errorf("OBU at offset %d: payload size %d exceeds remaining data %d", pos, payloadSize, len(data)-pos)
		}

		obus = append(obus, OBU{
			Header: hdr,
			Data:   data[pos : pos+payloadSize],
		})
		pos += payloadSize
	}

	return obus, nil
}

// OBUTypeName returns a human-readable name for an OBU type.
func OBUTypeName(t int) string {
	switch t {
	case OBUSequenceHeader:
		return "SEQUENCE_HEADER"
	case OBUTemporalDelimiter:
		return "TEMPORAL_DELIMITER"
	case OBUFrameHeader:
		return "FRAME_HEADER"
	case OBUTileGroup:
		return "TILE_GROUP"
	case OBUMetadata:
		return "METADATA"
	case OBUFrame:
		return "FRAME"
	case OBURedundantFrameHeader:
		return "REDUNDANT_FRAME_HEADER"
	case OBUTileList:
		return "TILE_LIST"
	case OBUPadding:
		return "PADDING"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", t)
	}
}
