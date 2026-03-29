package av1

// Block size constants (AV1 spec 6.10.4).
const (
	Block4x4     = 0
	Block4x8     = 1
	Block8x4     = 2
	Block8x8     = 3
	Block8x16    = 4
	Block16x8    = 5
	Block16x16   = 6
	Block16x32   = 7
	Block32x16   = 8
	Block32x32   = 9
	Block32x64   = 10
	Block64x32   = 11
	Block64x64   = 12
	Block64x128  = 13
	Block128x64  = 14
	Block128x128 = 15
	Block4x16    = 16
	Block16x4    = 17
	Block8x32    = 18
	Block32x8    = 19
	Block16x64   = 20
	Block64x16   = 21
	NumBlockSizes = 22
)

// BlockWidth returns the width in pixels for a block size.
var BlockWidth = [NumBlockSizes]int{
	4, 4, 8, 8, 8, 16, 16, 16, 32, 32, 32, 64, 64, 64, 128, 128, 4, 16, 8, 32, 16, 64,
}

// BlockHeight returns the height in pixels for a block size.
var BlockHeight = [NumBlockSizes]int{
	4, 8, 4, 8, 16, 8, 16, 32, 16, 32, 64, 32, 64, 128, 64, 128, 16, 4, 32, 8, 64, 16,
}

// BlockWidthLog2 returns log2 of block width / 4.
var BlockWidthLog2 = [NumBlockSizes]int{
	0, 0, 1, 1, 1, 2, 2, 2, 3, 3, 3, 4, 4, 4, 5, 5, 0, 2, 1, 3, 2, 4,
}

// BlockHeightLog2 returns log2 of block height / 4.
var BlockHeightLog2 = [NumBlockSizes]int{
	0, 1, 0, 1, 2, 1, 2, 3, 2, 3, 4, 3, 4, 5, 4, 5, 2, 0, 3, 1, 4, 2,
}

// Partition type constants.
const (
	PartitionNone  = 0
	PartitionHorz  = 1
	PartitionVert  = 2
	PartitionSplit = 3
	PartitionHorzA = 4
	PartitionHorzB = 5
	PartitionVertA = 6
	PartitionVertB = 7
	PartitionHorz4 = 8
	PartitionVert4 = 9
	NumPartitions  = 10
)

// TX size constants.
const (
	TX4x4   = 0
	TX8x8   = 1
	TX16x16 = 2
	TX32x32 = 3
	TX64x64 = 4
	NumTXSizes = 5
)

// TX type constants.
const (
	TxTypeDCT_DCT     = 0
	TxTypeADST_DCT    = 1
	TxTypeDCT_ADST    = 2
	TxTypeADST_ADST   = 3
	TxTypeFLIPADST_DCT = 4
	TxTypeDCT_FLIPADST = 5
	TxTypeFLIPADST_FLIPADST = 6
	TxTypeADST_FLIPADST = 7
	TxTypeFLIPADST_ADST = 8
	TxTypeIDENTITY_IDENTITY = 9
	TxTypeIDENTITY_DCT = 10
	TxTypeDCT_IDENTITY = 11
	TxTypeIDENTITY_ADST = 12
	TxTypeADST_IDENTITY = 13
	TxTypeIDENTITY_FLIPADST = 14
	TxTypeFLIPADST_IDENTITY = 15
	NumTXTypes = 16
)

// Intra prediction mode constants.
const (
	IntraDC        = 0
	IntraVertical  = 1
	IntraHorizontal = 2
	IntraD45       = 3
	IntraD135      = 4
	IntraD113      = 5
	IntraD157      = 6
	IntraD203      = 7
	IntraD67       = 8
	IntraSmooth    = 9
	IntraSmoothV   = 10
	IntraSmoothH   = 11
	IntraPaeth     = 12
	NumIntraModes  = 13
	UV_CFL_PRED    = 13 // Chroma-from-Luma UV mode
)

// SubSize maps (block_size, partition_type) → sub-block size.
// -1 means the partition is not allowed for that block size.
var SubSize = [NumBlockSizes][NumPartitions]int{
	// Block4x4: only NONE
	{Block4x4, -1, -1, -1, -1, -1, -1, -1, -1, -1},
	// Block4x8
	{Block4x8, Block4x4, -1, -1, -1, -1, -1, -1, -1, -1},
	// Block8x4
	{Block8x4, -1, Block4x4, -1, -1, -1, -1, -1, -1, -1},
	// Block8x8
	{Block8x8, Block8x4, Block4x8, Block4x4, -1, -1, -1, -1, -1, -1},
	// Block8x16
	{Block8x16, Block8x8, Block4x16, Block4x8, -1, -1, -1, -1, -1, -1},
	// Block16x8
	{Block16x8, Block16x4, Block8x8, Block8x4, -1, -1, -1, -1, -1, -1},
	// Block16x16
	{Block16x16, Block16x8, Block8x16, Block8x8, -1, -1, -1, -1, -1, -1},
	// Block16x32
	{Block16x32, Block16x16, Block8x32, Block8x16, -1, -1, -1, -1, -1, -1},
	// Block32x16
	{Block32x16, Block32x8, Block16x16, Block16x8, -1, -1, -1, -1, -1, -1},
	// Block32x32
	{Block32x32, Block32x16, Block16x32, Block16x16, Block32x16, Block32x16, Block16x32, Block16x32, -1, -1},
	// Block32x64
	{Block32x64, Block32x32, Block16x64, Block16x32, -1, -1, -1, -1, -1, -1},
	// Block64x32
	{Block64x32, Block64x16, Block32x32, Block32x16, -1, -1, -1, -1, -1, -1},
	// Block64x64
	{Block64x64, Block64x32, Block32x64, Block32x32, Block64x32, Block64x32, Block32x64, Block32x64, Block64x16, Block16x64},
	// Block64x128
	{Block64x128, Block64x64, Block32x128, Block32x64, -1, -1, -1, -1, -1, -1},
	// Block128x64
	{Block128x64, Block128x32, Block64x64, Block64x32, -1, -1, -1, -1, -1, -1},
	// Block128x128
	{Block128x128, Block128x64, Block64x128, Block64x64, Block128x64, Block128x64, Block64x128, Block64x128, Block128x32, Block32x128},
	// Rectangular sizes have limited partition options.
	{Block4x16, Block4x8, -1, -1, -1, -1, -1, -1, -1, -1},
	{Block16x4, -1, Block8x4, -1, -1, -1, -1, -1, -1, -1},
	{Block8x32, Block8x16, -1, -1, -1, -1, -1, -1, -1, -1},
	{Block32x8, -1, Block16x8, -1, -1, -1, -1, -1, -1, -1},
	{Block16x64, Block16x32, -1, -1, -1, -1, -1, -1, -1, -1},
	{Block64x16, -1, Block32x16, -1, -1, -1, -1, -1, -1, -1},
}

// Missing block sizes referenced in SubSize that don't exist as coded sizes.
// These are used only as intermediate values and mapped to actual decodable sizes.
const (
	Block32x128 = -2 // placeholder, not a real block size
	Block128x32 = -3
)

// TXSizeForBlockSize maps block size to the largest TX size that fits.
var TXSizeForBlockSize = [NumBlockSizes]int{
	TX4x4, TX4x4, TX4x4, TX8x8, TX8x8, TX8x8, TX16x16, TX16x16,
	TX16x16, TX32x32, TX32x32, TX32x32, TX64x64, TX64x64, TX64x64, TX64x64,
	TX4x4, TX4x4, TX8x8, TX8x8, TX16x16, TX16x16,
}

// TXWidth returns the width in pixels for a TX size.
var TXWidth = [NumTXSizes]int{4, 8, 16, 32, 64}

// TXHeight returns the height in pixels for a TX size.
var TXHeight = [NumTXSizes]int{4, 8, 16, 32, 64}

// MISizeWide returns the width in MI units (4-pixel blocks) for a block size.
var MISizeWide = [NumBlockSizes]int{
	1, 1, 2, 2, 2, 4, 4, 4, 8, 8, 8, 16, 16, 16, 32, 32, 1, 4, 2, 8, 4, 16,
}

// MISizeTall returns the height in MI units (4-pixel blocks) for a block size.
var MISizeTall = [NumBlockSizes]int{
	1, 2, 1, 2, 4, 2, 4, 8, 4, 8, 16, 8, 16, 32, 16, 32, 4, 1, 8, 2, 16, 4,
}

// SmoothWeights for SMOOTH prediction modes.
// AV1 spec 7.10.2.6.
var SmoothWeights = [192]int{
	// 4-sample weights
	255, 149, 85, 64,
	// 8-sample weights
	255, 197, 146, 105, 73, 50, 37, 64,
	// 16-sample weights
	255, 225, 196, 170, 145, 123, 102, 84,
	68, 54, 43, 33, 26, 20, 17, 64,
	// 32-sample weights
	255, 240, 225, 210, 196, 182, 169, 157,
	145, 133, 122, 111, 101, 92, 83, 74,
	66, 59, 52, 45, 39, 34, 29, 25,
	21, 17, 14, 12, 10, 9, 8, 64,
	// 64-sample weights (AV1 spec)
	255, 248, 240, 233, 225, 218, 210, 203,
	196, 189, 182, 176, 169, 163, 156, 150,
	144, 138, 133, 127, 121, 116, 111, 106,
	101, 96, 91, 86, 82, 77, 73, 69,
	65, 61, 57, 54, 50, 47, 44, 41,
	38, 35, 32, 29, 27, 25, 22, 20,
	18, 16, 15, 13, 12, 10, 9, 8,
	7, 6, 6, 5, 5, 4, 4, 64,
}

// SmoothWeightsOffset returns the offset into SmoothWeights for a given block dimension.
func SmoothWeightsOffset(n int) int {
	switch n {
	case 4:
		return 0
	case 8:
		return 4
	case 16:
		return 12
	case 32:
		return 28
	case 64:
		return 60 // 64-sample weights start after 4+8+16+32=60
	default:
		return 0
	}
}
