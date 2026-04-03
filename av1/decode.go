package av1

import (
	"fmt"
	"image"
)

// Decoder is a stateful AV1 decoder.
type Decoder struct {
	seqHdr   *SequenceHeader
	frameHdr *FrameHeader

	img *image.YCbCr
	cdf *CDFTables

	// Superblock and MI grid dimensions.
	sbCols int
	sbRows int
	miCols int
	miRows int

	// Reference frame pool (8 slots).
	refFrames [numRefFrames]*image.YCbCr

	// Diagnostic counters accumulated across all tiles in a frame.
	diagBlockCount int
	diagSkipCount  int
	diagTxbCount   int
	diagTxbNzCount int
	diagPartitions []int // first N partition decisions (partition type values)

	// First block diagnostics.
	diagFirstBlock     *blockInfo
	diagFirstTXBCoeffs []int32 // raw coefficient levels (before dequant)
	diagFirstTXBDCQ    int     // DC quantizer used
	diagFirstTXBACQ    int     // AC quantizer used
	diagBaseQIdx       int     // frame header BaseQIdx
	diagFirstTXBEOB    int     // EOB position of first TXB
	diagFirstTXBN      int     // coefficient count (n) of first TXB
	diagCodecPosAfterSB1 int  // codec byte position after first SB
}

// NewDecoder creates a new AV1 decoder.
func NewDecoder() *Decoder {
	return &Decoder{}
}

// DecodePacket decodes an AV1 packet (one or more OBUs) into a frame.
// Returns nil image if the packet contains no displayable frame.
func (d *Decoder) DecodePacket(data []byte) (*image.YCbCr, error) {
	obus, err := ParseOBUs(data)
	if err != nil {
		return nil, fmt.Errorf("parse OBUs: %w", err)
	}

	var result *image.YCbCr

	for _, obu := range obus {
		switch obu.Header.Type {
		case OBUTemporalDelimiter:
			// No action needed.

		case OBUSequenceHeader:
			sh, err := ParseSequenceHeader(obu.Data)
			if err != nil {
				return nil, fmt.Errorf("parse sequence header: %w", err)
			}
			d.seqHdr = sh

		case OBUFrameHeader:
			if d.seqHdr == nil {
				return nil, fmt.Errorf("frame header before sequence header")
			}
			fh, _, err := ParseFrameHeader(obu.Data, d.seqHdr)
			if err != nil {
				return nil, fmt.Errorf("parse frame header: %w", err)
			}
			d.frameHdr = fh

			if fh.ShowExistingFrame {
				ref := d.refFrames[fh.FrameToShowMapIdx]
				if ref != nil {
					result = ref
				}
				continue
			}

			d.updateDimensions()

		case OBUFrame:
			// OBU_FRAME contains both frame header and tile group.
			if d.seqHdr == nil {
				return nil, fmt.Errorf("frame OBU before sequence header")
			}
			fh, headerBits, err := ParseFrameHeader(obu.Data, d.seqHdr)
			if err != nil {
				return nil, fmt.Errorf("parse frame header in frame OBU: %w", err)
			}
			d.frameHdr = fh

			if fh.ShowExistingFrame {
				ref := d.refFrames[fh.FrameToShowMapIdx]
				if ref != nil {
					result = ref
				}
				continue
			}

			d.updateDimensions()

			// Tile group data follows the frame header.
			headerBytes := (headerBits + 7) / 8
			d.ensureImg()
			d.cdf = NewCDFTables(d.frameHdr.Quant.BaseQIdx)

			// Reset diagnostic counters for this frame.
			d.diagBlockCount = 0
			d.diagSkipCount = 0
			d.diagTxbCount = 0
			d.diagTxbNzCount = 0
			d.diagPartitions = nil
			d.diagFirstBlock = nil
			d.diagFirstTXBCoeffs = nil
			d.diagBaseQIdx = fh.Quant.BaseQIdx

			if headerBytes < len(obu.Data) {
				tileData := obu.Data[headerBytes:]
				if err := d.decodeTileGroup(tileData); err != nil {
					return d.img, fmt.Errorf("decode tile group: %w", err)
				}
			}
			result = d.img

		case OBUTileGroup:
			if d.img != nil && d.cdf != nil {
				if err := d.decodeTileGroup(obu.Data); err != nil {
					return nil, fmt.Errorf("decode tile group: %w", err)
				}
			}

		case OBUPadding, OBUMetadata, OBURedundantFrameHeader:
			// Skip.
		}
	}

	return result, nil
}

// updateDimensions recalculates grid dimensions from the current headers.
func (d *Decoder) updateDimensions() {
	fh := d.frameHdr
	d.miCols = fh.MICols
	d.miRows = fh.MIRows
	d.sbCols = fh.SBCols
	d.sbRows = fh.SBRows
}

// ensureImg allocates the frame buffer if needed.
func (d *Decoder) ensureImg() {
	fh := d.frameHdr
	w := fh.FrameWidth
	h := fh.FrameHeight
	if d.img != nil && d.img.Rect.Dx() == w && d.img.Rect.Dy() == h {
		return
	}
	d.img = image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
}
